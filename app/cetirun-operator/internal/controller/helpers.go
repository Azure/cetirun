package controller

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	istioapiv1beta1 "istio.io/api/networking/v1beta1"
	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

const (
	// istioGateway is the Istio Gateway resource that handles external traffic.
	istioGateway = "aks-istio-ingress/cetirun-gateway"

	// openclawPort is the port the OpenClaw Gateway listens on.
	openclawPort = 18789

	// hermesAPIPort is the port the Hermes OpenAI-compatible API listens on.
	hermesAPIPort = 8642

	// hermesDashboardPort is the port the native Hermes Dashboard listens on.
	hermesDashboardPort = 9119

	// cetirunAuthPort is the port the in-pod CetiRun auth helper service listens on.
	// It exposes /v1/login/* and /v1/status for the Portal "Copilot Auth" UI.
	cetirunAuthPort = 18790

	// ttydPort is the port ttyd web terminal listens on.
	ttydPort = 7681

	// novncPort is the port noVNC/websockify listens on.
	novncPort = 6080

	// msteamsWebhookPort is the default port the MS Teams webhook listener runs on.
	msteamsWebhookPort = 3978

	// defaultStorageClass is the CetiRun cluster standard StorageClass for instance home volumes.
	// Applied by deploy/storageclass.yaml. Override via spec.helm.values.persistence.storageClass.
	defaultStorageClass = "azureblob-nfs-premium"
)

// instanceNamespace returns the Kubernetes namespace for a given alias.
func instanceNamespace(alias string) string {
	return fmt.Sprintf("cetirun-%s", alias)
}

// helmReleaseName returns the Helm release name for a given alias.
// TODO: rename to "cetirun-<alias>" once existing instances are migrated.
// Changing the release name requires migration logic (uninstall old release,
// rebind PVC, install new release) since Helm tracks releases by name.
func helmReleaseName(alias string) string {
	return fmt.Sprintf("openclaw-%s", alias)
}

// instanceServiceName returns the Kubernetes Service name created by the Helm chart.
// The chart fullname template is: if release contains chart name → release name,
// else → "<release>-<chart>". Release is "openclaw-<alias>", chart name is "cetirun"
// (they have no contains relationship), so fullname = "openclaw-<alias>-cetirun".
// TODO: will become "cetirun-<alias>" after helmReleaseName migration (since
// "cetirun-<alias>" contains "cetirun", fullname collapses to release name).
func instanceServiceName(alias string) string {
	return fmt.Sprintf("openclaw-%s-cetirun", alias)
}

// accessURL returns the HTTPS URL for the given instance alias.
func accessURL(alias, domainSuffix string) string {
	return fmt.Sprintf("https://%s.%s", alias, domainSuffix)
}

// externalHostname returns the external hostname for the given alias.
func externalHostname(alias, domainSuffix string) string {
	return fmt.Sprintf("%s.%s", alias, domainSuffix)
}

// instanceKey returns the primary key used to derive all resource names for an instance.
// It is the instanceName from the CRD spec.
func instanceKey(instanceName string) string {
	return instanceName
}

// accessURLPathBased returns the canonical native UI URL for an instance.
// e.g. "https://cetiruntest.aksdev.io/ui/alice-work/"
func accessURLPathBased(instanceName, domainSuffix string) string {
	return fmt.Sprintf("https://%s/ui/%s/", domainSuffix, instanceName)
}

// virtualServiceName returns the Istio VirtualService name for a given alias.
func virtualServiceName(alias string) string {
	return fmt.Sprintf("cetirun-%s", alias)
}

// operatorConfigHash returns a short hex hash of operator-level config fields
// that should trigger a Helm upgrade when changed. This includes the domain
// suffix, a hash of the gateway Secret contents, the skills storage account
// override, the chart registry, operator-level helm values, and Azure Teams
// config so that configuration changes trigger a full reconcile with Helm upgrade.
// Chart version is intentionally excluded: version changes are tracked via
// spec.helm.version (which bumps the CR generation).
func operatorConfigHash(domainSuffix, secretHash, skillsStorageAccount, cetirunHelmValuesHash, chartRegistry string, azureTeamsConfig ...string) string {
	var payload string
	if skillsStorageAccount == "" && cetirunHelmValuesHash == "" && chartRegistry == "" && len(azureTeamsConfig) == 0 {
		// Legacy format — kept for operatorConfigHash unit tests that call
		// the function without Azure config args. The reconciler always goes
		// through configHash() which supplies Azure args, so existing CRs
		// will see a one-time hash mismatch and reconcile on operator upgrade.
		payload = domainSuffix + "\x00" + secretHash
	} else {
		// Extended format including optional overrides.
		payload = domainSuffix + "\x00" + secretHash + "\x00" + skillsStorageAccount + "\x00" + cetirunHelmValuesHash + "\x00" + chartRegistry
		for _, v := range azureTeamsConfig {
			payload += "\x00" + v
		}
	}
	h := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(h[:8]) // 16-char prefix is plenty for this purpose
}

// hashCetiRunHelmValues returns a stable hash string of the operator-level helm values map.
// Returns empty string for nil/empty maps.
func hashCetiRunHelmValues(values map[string]interface{}) string {
	if len(values) == 0 {
		return ""
	}
	data, err := json.Marshal(values)
	if err != nil {
		return "error"
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:8])
}

// secretContentHash returns a short hex hash of the Secret's Data map contents.
func secretContentHash(data map[string][]byte) string {
	if len(data) == 0 {
		return ""
	}
	h := sha256.New()
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write(data[k])
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// generateRandomToken generates a cryptographically random token of the given byte length,
// returned as a base64-encoded string.
func generateRandomToken(byteLen int) (string, error) {
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}

// buildHelmValues constructs the Helm values map for installing/upgrading the openclaw chart.
// The optional final argument is the operator deployment default model. An
// GitHub Copilot instance's spec.model takes precedence. The variadic form
// keeps the helper convenient for unit tests and callers that do not need a
// default.
// Merge order (later wins): controller defaults → cetirunHelmValues → team
// context/skills → per-instance spec.helm.values → controller-enforced model.
func buildHelmValues(instance *cetirunv1alpha1.OpenClawInstance, team *cetirunv1alpha1.OpenClawTeam, domainSuffix string, skillsStorageAccount string, cetirunHelmValues map[string]interface{}, chartRegistry string, defaultModels ...string) (map[string]interface{}, error) {
	alias := instance.Spec.User.Alias
	spec := instance.Spec
	agentType := spec.AgentType
	if agentType == "" {
		agentType = cetirunv1alpha1.AgentTypeOpenClaw
	}
	// userTeam falls back to spec.team when user.team is unset, so the team
	// label propagates into USER.md / extraEnv even when callers only set the
	// top-level team reference.
	userTeam := spec.User.Team
	if userTeam == "" {
		userTeam = spec.Team
	}

	values := map[string]interface{}{
		"agentType": agentType,
		// Point to the pre-created gateway token secret.
		// The chart maps provider names into each agent's native config shape.
		"secrets": map[string]interface{}{
			"existingSecret": spec.GatewayTokenSecretRef,
			"provider":       "github_copilot",
			"model":          "",
		},
		// User identity — templates USER.md in the ConfigMap.
		"user": map[string]interface{}{
			"alias":       alias,
			"displayName": spec.User.DisplayName,
			"team":        userTeam,
			"baseDomain":  domainSuffix,
		},
		// User identification via extra env vars.
		// Must be []interface{} (not []map[string]interface{}) so that Helm's JSON
		// schema validator accepts the value for array-typed fields.
		"extraEnv": []interface{}{
			map[string]interface{}{"name": "CETIRUN_USER_ALIAS", "value": alias},
			map[string]interface{}{"name": "CETIRUN_USER_DISPLAY_NAME", "value": spec.User.DisplayName},
			map[string]interface{}{"name": "CETIRUN_USER_TEAM", "value": userTeam},
		},
		// Persistent home volume.
		// storageClass defaults to azureblob-nfs-premium (the CetiRun cluster standard).
		// Override via spec.helm.values.persistence.storageClass if needed.
		"persistence": map[string]interface{}{
			"storageClass": defaultStorageClass,
			"accessMode":   "ReadWriteMany",
			"size":         "20Gi",
		},
		// NetworkPolicy: AKS managed Istio places istiod in aks-istio-system and
		// the ingress gateway in aks-istio-ingress (different from upstream istio-system).
		"networkPolicy": map[string]interface{}{
			"istioNamespace":        "aks-istio-system",
			"istioIngressNamespace": "aks-istio-ingress",
		},
		// Desktop sidecar: inject the noVNC WebSocket path prefix so the
		// auto-connect redirect includes ?path=<instance>/desktop/websockify.
		"desktop": map[string]interface{}{
			"novncPathPrefix": instance.Name + "/desktop/",
		},
	}

	// Operator-level Helm value overrides (from CETIRUN_HELM_VALUES).
	// Merged after hardcoded defaults but before controller-enforced values
	// (suspended, tailscale, teams) so that the controller always wins.
	for k, v := range cetirunHelmValues {
		values[k] = deepMerge(values[k], v)
	}

	// Apply OpenClawTeam defaults — higher priority than cetirunHelmValues
	// (deepMerge is called with team values as the override side, so they win
	// on key collisions), lower priority than per-instance spec.helm.values
	// which is applied at the bottom of this function.
	if team != nil {
		if team.Spec.Defaults.Provider != "" {
			values["secrets"] = deepMerge(values["secrets"], map[string]interface{}{
				"provider": team.Spec.Defaults.Provider,
			})
		}
		userPatch := map[string]interface{}{}
		if team.Spec.MemberContext.Role != "" {
			userPatch["role"] = team.Spec.MemberContext.Role
		}
		if len(team.Spec.MemberContext.Description) > 0 {
			userPatch["description"] = stringsToInterfaces(team.Spec.MemberContext.Description)
		}
		if len(userPatch) > 0 {
			values["user"] = deepMerge(values["user"], userPatch)
		}
	}

	// Apply OpenClawTeam shared skill storage → chart's skills.shared mode.
	// Operator maps team.SkillStorage to the chart's existing
	// skills.{shared,resourceGroup,storageAccount,containerName,...} fields, which
	// auto-provision a PV+PVC per instance namespace pointing at the same
	// Azure Blob NFS container.
	if team != nil && team.Spec.SkillStorage != nil {
		s := team.Spec.SkillStorage
		storage := map[string]interface{}{
			"shared":         true,
			"resourceGroup":  s.ResourceGroup,
			"storageAccount": s.StorageAccount,
			"containerName":  s.ContainerName,
		}
		if s.StorageClassName != "" {
			storage["storageClassName"] = s.StorageClassName
		}
		if s.StorageSize != "" {
			storage["storageSize"] = s.StorageSize
		}
		if s.MountPath != "" {
			storage["mountPath"] = s.MountPath
		}
		values["skills"] = deepMerge(values["skills"], storage)
	}

	// Compute and apply effective skills (team base + instance overrides).
	effective := computeEffectiveSkills(team, instance.Spec.SkillOverrides.Add, instance.Spec.SkillOverrides.Remove)
	if len(effective) > 0 {
		npmSkills, clawHubSkills, classifyErr := classifySkills(effective)
		if classifyErr != nil {
			return nil, fmt.Errorf("classify skills: %w", classifyErr)
		}
		skillsPatch := map[string]interface{}{}
		if len(npmSkills) > 0 {
			skillsPatch["npm"] = stringsToInterfaces(npmSkills)
		}
		if len(clawHubSkills) > 0 {
			skillsPatch["clawHub"] = stringsToInterfaces(clawHubSkills)
		}
		if len(skillsPatch) > 0 {
			values["skills"] = deepMerge(values["skills"], skillsPatch)
		}
	}

	// Scale to zero when the instance is suspended (access restriction).
	// PVCs are preserved by the StatefulSet's whenScaled: Retain policy.
	// Placed after cetirunHelmValues merge so suspension cannot be overridden.
	if instance.Spec.Suspended {
		values["replicaCount"] = 0
	}

	// When Tailscale is enabled, turn on tailscale serve by default so that the
	// gateway is accessible via HTTPS on the tailnet (device identity is automatic).
	if spec.Tailscale != nil {
		values["tailscale"] = map[string]interface{}{
			"serve": true,
		}
	}

	// Override skills storage account if configured at the operator level.
	// Deprecated: use cetirunHelmValues with skills.storageAccount instead.
	// Kept for backward compatibility; takes precedence over cetirunHelmValues.skills.storageAccount.
	if skillsStorageAccount != "" {
		values["skills"] = deepMerge(values["skills"], map[string]interface{}{
			"storageAccount": skillsStorageAccount,
		})
	}

	// Override image repositories to match the operator's chart registry.
	// This ensures both openclaw-gateway and cetirun-desktop images are
	// pulled from the same ACR that hosts the Helm chart.
	if chartRegistry != "" {
		values["openclaw"] = deepMerge(values["openclaw"], map[string]interface{}{
			"image": map[string]interface{}{
				"repository": chartRegistry + "/openclaw-gateway",
			},
		})
		values["hermes"] = deepMerge(values["hermes"], map[string]interface{}{
			"image": map[string]interface{}{
				"repository": chartRegistry + "/hermes-gateway",
			},
		})
		values["desktop"] = deepMerge(values["desktop"], map[string]interface{}{
			"image": map[string]interface{}{
				"repository": chartRegistry + "/cetirun-desktop",
			},
		})
	}

	// MS Teams integration.
	// When msteams.Enabled is true, the operator uses workload identity (managed identity)
	// for bot auth and the webhook port is configured.
	// Since openclaw 2026.4.11, Teams support is included in the upstream image —
	// no separate image tag suffix is needed.
	if spec.MSTeams != nil && spec.MSTeams.Enabled {
		msteams := spec.MSTeams
		webhookPort := int32(msteamsWebhookPort)
		if msteams.WebhookPort > 0 {
			webhookPort = msteams.WebhookPort
		}
		values["msteamsWebhookPort"] = int(webhookPort)

		// Enable workload identity once a non-empty app ID is available. Prefer the
		// resolved app ID from status for auto-provisioned Teams apps, and fall back
		// to the spec value when status is not yet populated.
		values["msteamsAuthType"] = "managedIdentity"
		azureWorkloadIdentityClientID := msteams.AppID
		if instance.Status.MSTeams != nil && instance.Status.MSTeams.AppID != "" {
			azureWorkloadIdentityClientID = instance.Status.MSTeams.AppID
		}
		azureWorkloadIdentityEnabled := azureWorkloadIdentityClientID != ""
		azureWorkloadIdentity := map[string]interface{}{
			"enabled": azureWorkloadIdentityEnabled,
		}
		if azureWorkloadIdentityEnabled {
			azureWorkloadIdentity["clientId"] = azureWorkloadIdentityClientID
		}
		values["azureWorkloadIdentity"] = azureWorkloadIdentity
		values["serviceAccount"] = deepMerge(values["serviceAccount"], map[string]interface{}{
			"create": true,
		})
	}

	// User-supplied Helm value overrides. Each key in spec.helm.values is a top-level
	// Helm value name; its JSON value is deep-merged into the operator defaults so that
	// nested keys (e.g. persistence.storageClass) can be overridden without silently
	// dropping sibling defaults (e.g. persistence.size). Slices are replaced entirely,
	// matching standard Helm --values merge semantics.
	for k, rawExt := range spec.Helm.Values {
		if rawExt.Raw == nil {
			continue
		}
		var v interface{}
		if err := json.Unmarshal(rawExt.Raw, &v); err != nil {
			return nil, fmt.Errorf("unmarshal helm.values[%s]: %w", k, err)
		}
		values[k] = deepMerge(values[k], v)
	}

	// The native OpenClaw Control UI must know the externally visible UI
	// prefix so it emits prefixed asset, API, media, and WebSocket URLs. This is
	// platform routing and must not be overridden by user Helm values.
	if agentType == cetirunv1alpha1.AgentTypeOpenClaw {
		values["openclaw"] = deepMerge(values["openclaw"], map[string]interface{}{
			"config": map[string]interface{}{
				"gateway": map[string]interface{}{
					"controlUi": map[string]interface{}{
						"basePath": "/ui/" + instance.Name,
					},
				},
			},
		})
	}

	// Model selection is intentionally declarative and applies only to GitHub
	// Copilot. An explicit spec.model wins for that provider, followed by the
	// deployment default. OpenAI/Anthropic retain their provider-specific Helm
	// model even if an older instance has a stale spec.model override.
	secrets, ok := values["secrets"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("helm values secrets must be an object")
	}
	provider, _ := secrets["provider"].(string)
	provider = strings.TrimSpace(provider)
	if provider == "" {
		provider = "github_copilot"
	}
	if provider == "github_copilot" {
		model := strings.TrimSpace(spec.Model)
		if model == "" && len(defaultModels) > 0 {
			model = strings.TrimSpace(defaultModels[0])
		}
		if model != "" {
			values["secrets"] = deepMerge(secrets, map[string]interface{}{
				"model": model,
			})
		}
	}

	return values, nil
}

// deepMerge recursively merges override into base.
// - If both values are map[string]interface{}, keys are merged recursively.
// - For all other types (including slices), override replaces base entirely.
// This matches standard Helm --values merge semantics.
func deepMerge(base, override interface{}) interface{} {
	baseMap, baseOk := base.(map[string]interface{})
	overrideMap, overrideOk := override.(map[string]interface{})
	if !baseOk || !overrideOk {
		return override
	}
	merged := make(map[string]interface{}, len(baseMap))
	for k, v := range baseMap {
		merged[k] = v
	}
	for k, v := range overrideMap {
		if existing, ok := merged[k]; ok {
			merged[k] = deepMerge(existing, v)
		} else {
			merged[k] = v
		}
	}
	return merged
}

// teamResourceVersion returns the OpenClawTeam ResourceVersion (or "" when
// team is nil). Used as a cache-invalidation token in configHash so that
// edits to a referenced team — which do not bump the instance Generation —
// still force a full reconcile when the team-watch handler re-enqueues the
// instance.
func teamResourceVersion(team *cetirunv1alpha1.OpenClawTeam) string {
	if team == nil {
		return ""
	}
	return team.ResourceVersion
}

// stringsToInterfaces converts a []string to []interface{} so that values
// passed to Helm's schema validator are treated as untyped JSON arrays.
// The Helm v3 SDK schema validator rejects strongly typed Go slices like
// []string with "invalid jsonType" errors; values.yaml-loaded arrays are
// always []interface{}, so this restores that shape.
func stringsToInterfaces(in []string) []interface{} {
	out := make([]interface{}, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}

// computeEffectiveSkills returns (base ∪ add) \ remove as a sorted, deduplicated slice.
// team may be nil (treated as empty base).
func computeEffectiveSkills(team *cetirunv1alpha1.OpenClawTeam, add, remove []string) []string {
	set := map[string]struct{}{}
	if team != nil {
		for _, s := range team.Spec.Skills {
			set[s] = struct{}{}
		}
	}
	for _, s := range add {
		set[s] = struct{}{}
	}
	for _, s := range remove {
		delete(set, s)
	}
	result := make([]string, 0, len(set))
	for s := range set {
		result = append(result, s)
	}
	sort.Strings(result)
	return result
}

// classifySkills splits skills by prefix into npm and clawHub lists.
// npm:PKG → PKG (prefix stripped)
// other   → clawHub as-is
// "pvc:" prefix is rejected.
func classifySkills(skills []string) (npm []string, clawHub []string, err error) {
	for _, s := range skills {
		switch {
		case strings.HasPrefix(s, "pvc:"):
			return nil, nil, fmt.Errorf("invalid skill %q: pvc: prefix is not supported", s)
		case strings.HasPrefix(s, "npm:"):
			pkg := strings.TrimPrefix(s, "npm:")
			if pkg == "" {
				return nil, nil, fmt.Errorf("invalid skill %q: npm: prefix requires a package name", s)
			}
			npm = append(npm, pkg)
		default:
			clawHub = append(clawHub, s)
		}
	}
	return
}

// buildVirtualService builds the Istio VirtualService
// It exposes native agent UIs below /ui/<instanceName>. OpenClaw's native Control UI
// retains that UI prefix through its gateway.controlUi.basePath setting;
// the Terminal, Desktop, Hermes, and in-pod auth routes use their own rewrites.
//
// The instance name is not used as a top-level native UI route, which keeps it isolated
// from Portal SPA routes such as /instances/<name>.
func buildVirtualService(instance *cetirunv1alpha1.OpenClawInstance, domainSuffix string) *istiov1beta1.VirtualService {
	name := instance.Name
	ns := instanceNamespace(name)
	svcHost := fmt.Sprintf("%s.%s.svc.cluster.local", instanceServiceName(name), ns)
	instancePrefix := "/" + name
	uiPrefix := "/ui/" + name
	agentType := instance.Spec.AgentType
	if agentType == "" {
		agentType = cetirunv1alpha1.AgentTypeOpenClaw
	}

	makeRoute := func(matchPrefix, rewriteURI string, port uint32, timeoutSecs int64) *istioapiv1beta1.HTTPRoute {
		route := &istioapiv1beta1.HTTPRoute{
			Match: []*istioapiv1beta1.HTTPMatchRequest{
				{Uri: &istioapiv1beta1.StringMatch{MatchType: &istioapiv1beta1.StringMatch_Prefix{Prefix: matchPrefix}}},
			},
			Rewrite: &istioapiv1beta1.HTTPRewrite{Uri: rewriteURI},
			Route: []*istioapiv1beta1.HTTPRouteDestination{
				{Destination: &istioapiv1beta1.Destination{
					Host: svcHost,
					Port: &istioapiv1beta1.PortSelector{Number: port},
				}},
			},
		}
		if timeoutSecs > 0 {
			route.Timeout = durationpb.New(time.Duration(timeoutSecs) * time.Second)
			route.Retries = &istioapiv1beta1.HTTPRetry{Attempts: 3, PerTryTimeout: durationpb.New(100 * time.Second)}
		}
		return route
	}
	makeRedirect := func(exactPath string) *istioapiv1beta1.HTTPRoute {
		return &istioapiv1beta1.HTTPRoute{
			Match: []*istioapiv1beta1.HTTPMatchRequest{
				{Uri: &istioapiv1beta1.StringMatch{MatchType: &istioapiv1beta1.StringMatch_Exact{Exact: exactPath}}},
			},
			Redirect: &istioapiv1beta1.HTTPRedirect{
				Uri:          exactPath + "/",
				RedirectCode: 301,
			},
		}
	}
	makeWebSocketRoute := func(exactPath string, port uint32) *istioapiv1beta1.HTTPRoute {
		return &istioapiv1beta1.HTTPRoute{
			Match: []*istioapiv1beta1.HTTPMatchRequest{
				{
					Uri: &istioapiv1beta1.StringMatch{MatchType: &istioapiv1beta1.StringMatch_Exact{Exact: exactPath}},
					Headers: map[string]*istioapiv1beta1.StringMatch{
						"upgrade": {MatchType: &istioapiv1beta1.StringMatch_Exact{Exact: "websocket"}},
					},
				},
			},
			Route: []*istioapiv1beta1.HTTPRouteDestination{
				{Destination: &istioapiv1beta1.Destination{
					Host: svcHost,
					Port: &istioapiv1beta1.PortSelector{Number: port},
				}},
			},
			Timeout: durationpb.New(300 * time.Second),
			Retries: &istioapiv1beta1.HTTPRetry{Attempts: 3, PerTryTimeout: durationpb.New(100 * time.Second)},
		}
	}

	routes := []*istioapiv1beta1.HTTPRoute{
		makeRedirect(uiPrefix),
		makeRedirect(instancePrefix + "/desktop"),
		makeRedirect(instancePrefix + "/term"),
	}
	if agentType == cetirunv1alpha1.AgentTypeHermes {
		routes = append(routes,
			makeRoute(uiPrefix+"/v1", "/v1", hermesAPIPort, 300),
			makeRoute(uiPrefix+"/health", "/health", hermesAPIPort, 0),
			makeRoute(instancePrefix+"/cetirun-auth/", "/", cetirunAuthPort, 60),
			makeRoute(instancePrefix+"/desktop", "/desktop", novncPort, 0),
			makeRoute(instancePrefix+"/term", "/term", ttydPort, 0),
		)
		dashboardRoute := makeRoute(uiPrefix+"/", "/", hermesDashboardPort, 0)
		dashboardRoute.Headers = &istioapiv1beta1.Headers{
			Request: &istioapiv1beta1.Headers_HeaderOperations{
				Add: map[string]string{"x-forwarded-prefix": uiPrefix},
			},
		}
		routes = append(routes, dashboardRoute)
	} else {
		// OpenClaw's Control UI normalizes controlUi.basePath by removing the
		// trailing slash before constructing its WebSocket URL. Preserve the
		// canonical HTTP redirect for /ui/<instance>, but route an Upgrade request
		// on that exact path directly to the gateway because WebSockets cannot
		// follow a 301 redirect.
		routes = append([]*istioapiv1beta1.HTTPRoute{makeWebSocketRoute(uiPrefix, openclawPort)}, routes...)
		routes = append(routes,
			makeRoute(instancePrefix+"/cetirun-auth/", "/", cetirunAuthPort, 60),
			makeRoute(instancePrefix+"/desktop", "/desktop", novncPort, 0),
			makeRoute(instancePrefix+"/term", "/term", ttydPort, 0),
			&istioapiv1beta1.HTTPRoute{
				Match: []*istioapiv1beta1.HTTPMatchRequest{{
					Uri: &istioapiv1beta1.StringMatch{MatchType: &istioapiv1beta1.StringMatch_Prefix{Prefix: uiPrefix + "/"}},
				}},
				Route: []*istioapiv1beta1.HTTPRouteDestination{{Destination: &istioapiv1beta1.Destination{
					Host: svcHost,
					Port: &istioapiv1beta1.PortSelector{Number: openclawPort},
				}}},
				Timeout: durationpb.New(300 * time.Second),
				Retries: &istioapiv1beta1.HTTPRetry{Attempts: 3, PerTryTimeout: durationpb.New(100 * time.Second)},
			},
		)
	}

	// MS Teams webhook at POST /<instanceName>/api/messages.
	//
	// The gateway AuthorizationPolicy statically bypasses oauth2-proxy for POST
	// */api/messages on every instance, so this exact path is reachable anonymously at
	// the routing layer. For instances that have a live VirtualService we narrow that
	// bypass back to Teams-enabled instances here:
	//   - Teams enabled  → route the webhook POST to the msteams webhook port.
	//   - Teams disabled → return an explicit 404 so the anonymous POST cannot fall
	//     through to the instance catch-all (e.g. the Hermes API port).
	//
	// This is a routing-layer narrowing, not a complete boundary: it only applies while
	// this VirtualService exists. A suspended instance (whose VS is deleted, see the
	// suspend handler in openclawinstance_controller.go) or a never-created name has no
	// route here, so an anonymous POST to its /api/messages still bypasses oauth2-proxy
	// and falls through to the portal SPA catch-all. The application layer (Bot Framework
	// JWT validation on a real Teams instance; the portal serving static index.html
	// otherwise) is the backstop in those windows.
	//
	// Either route is prepended so it is first-match within this per-instance
	// VirtualService. It must never be expressed as a shared/global */api/messages rule:
	// cross-VirtualService route precedence on a shared host is not a safe contract and a
	// global rule could shadow other instances.
	webhookMatch := []*istioapiv1beta1.HTTPMatchRequest{
		{
			Uri:    &istioapiv1beta1.StringMatch{MatchType: &istioapiv1beta1.StringMatch_Exact{Exact: instancePrefix + "/api/messages"}},
			Method: &istioapiv1beta1.StringMatch{MatchType: &istioapiv1beta1.StringMatch_Exact{Exact: "POST"}},
		},
	}
	var webhookRoute *istioapiv1beta1.HTTPRoute
	if instance.Spec.MSTeams != nil && instance.Spec.MSTeams.Enabled {
		port := uint32(msteamsWebhookPort)
		if instance.Spec.MSTeams.WebhookPort != 0 {
			port = uint32(instance.Spec.MSTeams.WebhookPort)
		}
		webhookRoute = &istioapiv1beta1.HTTPRoute{
			Match:   webhookMatch,
			Rewrite: &istioapiv1beta1.HTTPRewrite{Uri: "/api/messages"},
			Route: []*istioapiv1beta1.HTTPRouteDestination{
				{Destination: &istioapiv1beta1.Destination{
					Host: svcHost,
					Port: &istioapiv1beta1.PortSelector{Number: port},
				}},
			},
		}
	} else {
		webhookRoute = &istioapiv1beta1.HTTPRoute{
			Match: webhookMatch,
			DirectResponse: &istioapiv1beta1.HTTPDirectResponse{
				Status: 404,
			},
		}
	}
	routes = append([]*istioapiv1beta1.HTTPRoute{webhookRoute}, routes...)

	return &istiov1beta1.VirtualService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      virtualServiceName(name),
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "cetirun-operator",
				"app.kubernetes.io/instance":   name,
			},
		},
		Spec: istioapiv1beta1.VirtualService{
			Hosts:    []string{domainSuffix},
			Gateways: []string{istioGateway},
			ExportTo: []string{"*"},
			Http:     routes,
		},
	}
}

// setCondition upserts a metav1.Condition into the conditions slice.
func setCondition(conditions *[]metav1.Condition, condType string, status metav1.ConditionStatus, reason, message string) {
	now := metav1.Now()
	for i, c := range *conditions {
		if c.Type == condType {
			if c.Status == status && c.Reason == reason && c.Message == message {
				// No change.
				return
			}
			(*conditions)[i].Status = status
			(*conditions)[i].Reason = reason
			(*conditions)[i].Message = message
			(*conditions)[i].LastTransitionTime = now
			return
		}
	}
	*conditions = append(*conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
	})
}

// isConditionTrue returns true if the named condition exists and has status True.
func isConditionTrue(conditions []metav1.Condition, condType string) bool {
	for _, c := range conditions {
		if c.Type == condType {
			return c.Status == metav1.ConditionTrue
		}
	}
	return false
}
