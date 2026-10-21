// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
)

const (
	// aiGatewayKeySecretName is the instance-namespace Secret holding the
	// Agent's virtual key. It is deliberately not the multi-purpose
	// gateway-token Secret, which the Portal reads for browser-facing values.
	aiGatewayKeySecretName  = "ai-gateway-virtual-key"
	aiGatewayKeySecretField = "api-key"
	// aiGatewaySharedAccessGroup is the Gateway access group of the models
	// administrators share with legacy Agents. Policy-bound keys never receive it.
	aiGatewaySharedAccessGroup = "cetirun-shared"
	// aiGatewayModelAccessGroupPrefix identifies an opaque per-configured-model
	// access group. The Gateway chart derives its suffix from the canonical
	// model configuration; the controller only trusts groups returned by the
	// Gateway catalog and never attempts to recreate the suffix from a
	// LiteLLM-generated deployment ID.
	aiGatewayModelAccessGroupPrefix = "cetirun-model-"
	// aiGatewaySharedDeploymentIDPrefix pairs a chart-derived deployment ID
	// with the access-group suffix. The catalog trusts neither value alone.
	aiGatewaySharedDeploymentIDPrefix = "cetirun-shared-"
	// aiGatewayTeamPrefix prefixes the Gateway team of a CetiRun Team.
	aiGatewayTeamPrefix           = "cetirun-team-"
	aiGatewayBudgetDuration       = "1mo"
	aiGatewayVerificationInterval = 10 * time.Minute
	aiGatewayOwnerCacheTTL        = time.Hour
	aiGatewayKeyIDLength          = 12
	aiGatewayAgentUIDLabel        = "cetirun.microsoft.com/agent-uid"
	aiGatewayKeyAliasAnnotation   = "cetirun.microsoft.com/ai-gateway-key-alias"
	aiGatewayKeyIDPodAnnotation   = "cetirun.microsoft.com/ai-gateway-key-id"
	// Placeholder default models for an Agent without any model yet. The
	// Agent's configuration needs a default; the Portal explains the state.
	aiGatewaySignInRequiredModel = cetirunv1alpha1.AIGatewaySignInRequiredModel
	aiGatewayNoModelsModel       = cetirunv1alpha1.AIGatewayNoModelsModel
	// aiGatewayReasonKeySecretLost reports that the Agent's own key exists in
	// the Gateway while its value is no longer in the key Secret.
	aiGatewayReasonKeySecretLost = "KeySecretLost"
	// aiGatewayReasonKeyRecordLost reports that the Agent previously issued a
	// key but the Gateway no longer has its durable record. Recreating it would
	// silently reset spend, budget-reset time, and emergency block state.
	aiGatewayReasonKeyRecordLost = "KeyRecordLost"
	// aiGatewayReasonSuspended marks a block the operator itself applied. It is
	// the only blocked state a later authorized resume may clear; any other
	// blocked key is an administrator emergency block and remains fail-closed.
	aiGatewayReasonSuspended = "Suspended"
	aiGatewayReasonNoModels  = "NoModelEntitlement"
	// aiGatewayReasonPolicyReprojection is an operator-owned, temporary block
	// placed before an old policy projection is replaced. It may be cleared
	// only after the new key grant has been read back successfully.
	aiGatewayReasonPolicyReprojection = "PolicyReprojection"
	// aiGatewayReasonDisabled reports an Agent whose chart takes its models
	// only from the Gateway in a deployment that has not enabled it.
	aiGatewayReasonDisabled = "AIGatewayDisabled"
)

var errAIGatewayKeySecretLost = errors.New("the recorded AI Gateway key value is no longer available in its Secret")

// AIGatewayConfig is the deployment-level AI Gateway integration. With
// Enabled false the operator creates no keys, but still revokes existing keys
// on Agent deletion when URL and the master key Secret are configured.
type AIGatewayConfig struct {
	Enabled bool
	// URL is the Gateway's in-cluster base URL:
	// http://<service>.<namespace>.svc.cluster.local:<port>.
	URL string
	// MasterKeySecretName and MasterKeySecretKey locate the Gateway master key
	// in the operator namespace.
	MasterKeySecretName string
	MasterKeySecretKey  string
	// DefaultBudget initializes legacy keys without replacing later Gateway
	// administrator changes. Policy-bound keys use the effective policy budget.
	DefaultBudget float64
	// DefaultModel is the preferred default model of Agents that use the
	// Gateway when the Agent names none, or names one it cannot use.
	DefaultModel string
	// CopilotProxyImage is the image of the per-Agent GitHub Copilot proxy,
	// the Gateway image. Empty turns GitHub Copilot off for every Agent.
	CopilotProxyImage string
}

// ParseAIGatewayConfig reads the operator's AI Gateway settings from the
// environment variables the operator chart renders.
func ParseAIGatewayConfig(getenv func(string) string) (AIGatewayConfig, error) {
	cfg := AIGatewayConfig{
		Enabled:             strings.EqualFold(strings.TrimSpace(getenv("AI_GATEWAY_ENABLED")), "true"),
		URL:                 strings.TrimRight(strings.TrimSpace(getenv("AI_GATEWAY_URL")), "/"),
		MasterKeySecretName: strings.TrimSpace(getenv("AI_GATEWAY_MASTER_KEY_SECRET")),
		MasterKeySecretKey:  strings.TrimSpace(getenv("AI_GATEWAY_MASTER_KEY_SECRET_KEY")),
		DefaultModel:        strings.TrimSpace(getenv("AI_GATEWAY_DEFAULT_MODEL")),
		CopilotProxyImage:   strings.TrimSpace(getenv("AI_GATEWAY_COPILOT_PROXY_IMAGE")),
	}
	if cfg.MasterKeySecretKey == "" {
		cfg.MasterKeySecretKey = "master-key"
	}
	if raw := strings.TrimSpace(getenv("AI_GATEWAY_DEFAULT_BUDGET")); raw != "" {
		budget, err := strconv.ParseFloat(raw, 64)
		// ParseFloat accepts NaN and infinities. NaN passes every comparison,
		// and the key would be created without a budget.
		if err != nil || math.IsNaN(budget) || math.IsInf(budget, 0) || budget <= 0 {
			return AIGatewayConfig{}, fmt.Errorf("AI_GATEWAY_DEFAULT_BUDGET must be a positive number of US dollars")
		}
		cfg.DefaultBudget = budget
	}
	if cfg.CopilotProxyImage != "" && !validImageReference(cfg.CopilotProxyImage) {
		return AIGatewayConfig{}, fmt.Errorf("AI_GATEWAY_COPILOT_PROXY_IMAGE must be an image reference with a tag or digest")
	}
	if !cfg.Enabled && cfg.URL == "" {
		return cfg, nil
	}
	if _, _, err := aiGatewayEndpoint(cfg.URL); err != nil {
		return AIGatewayConfig{}, err
	}
	if cfg.MasterKeySecretName == "" {
		return AIGatewayConfig{}, fmt.Errorf("AI_GATEWAY_MASTER_KEY_SECRET is required")
	}
	if cfg.Enabled && cfg.DefaultBudget == 0 {
		return AIGatewayConfig{}, fmt.Errorf("AI_GATEWAY_DEFAULT_BUDGET is required when the AI Gateway is enabled")
	}
	return cfg, nil
}

func (c AIGatewayConfig) configured() bool {
	return c.URL != "" && c.MasterKeySecretName != ""
}

// aiGatewayEndpoint returns the namespace and port of an in-cluster Gateway
// URL. Agents reach the Gateway through a NetworkPolicy rule for exactly that
// namespace and port, so the URL must name a cluster Service.
func aiGatewayEndpoint(raw string) (string, int, error) {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Scheme != "http" || endpoint.User != nil || endpoint.Path != "" ||
		endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return "", 0, fmt.Errorf("AI Gateway URL must be an in-cluster HTTP service URL without a path")
	}
	parts := strings.Split(endpoint.Hostname(), ".")
	if len(parts) != 5 || parts[0] == "" || parts[1] == "" || parts[2] != "svc" || parts[3] != "cluster" || parts[4] != "local" {
		return "", 0, fmt.Errorf("AI Gateway URL must use service.namespace.svc.cluster.local")
	}
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("AI Gateway URL must include a port")
	}
	return parts[1], port, nil
}

// aiGatewayKeyClient is the part of the Gateway API the reconciler uses.
type aiGatewayKeyClient interface {
	GetKey(context.Context, string) (*aigateway.Key, error)
	GenerateKey(context.Context, aigateway.GenerateRequest) error
	UpdateKeyModels(context.Context, string, []string) error
	UpdateKeyBudget(context.Context, string, float64, string) error
	SetKeyBlocked(context.Context, string, bool) error
	UpdateKeyOwner(context.Context, string, string, string) error
	DeleteKeys(context.Context, []string) error
	DeleteKeyByAlias(context.Context, string) error
	EnsureTeam(context.Context, string, string, map[string]string) error
	EnsureUser(context.Context, string, map[string]string) error
	EnsureTeamMember(context.Context, string, string) error
	ListModels(context.Context) ([]aigateway.Deployment, error)
	BlockUnpricedModels(context.Context) (bool, error)
	AddModel(context.Context, aigateway.ModelRequest) error
	DeleteModel(context.Context, string) error
	PricedModels(context.Context) (map[string]bool, error)
	UpdateKeyAliases(context.Context, string, map[string]string) error
}

func (r *AgentReconciler) aiGatewayClient(ctx context.Context) (aiGatewayKeyClient, error) {
	if r.aiGatewayClientFactory != nil {
		return r.aiGatewayClientFactory(ctx)
	}
	namespace := r.OperatorNamespace
	if namespace == "" {
		namespace = "cetirun-system"
	}
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: r.AIGateway.MasterKeySecretName}, secret); err != nil {
		return nil, fmt.Errorf("read AI Gateway master key Secret: %w", err)
	}
	masterKey := strings.TrimSpace(string(secret.Data[r.AIGateway.MasterKeySecretKey]))
	if masterKey == "" {
		return nil, fmt.Errorf("AI Gateway master key Secret has no %q value", r.AIGateway.MasterKeySecretKey)
	}
	httpClient := r.AIGatewayHTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: aigateway.DefaultTimeout}
	}
	return aigateway.New(r.AIGateway.URL, masterKey, httpClient)
}

// aiGatewayKeyAlias is derived from the immutable Agent UID so that a key
// can be found, and a lost create response recovered, without any state
// outside the Gateway. The name is included for administrators.
func aiGatewayKeyAlias(instance *cetirunv1alpha1.Agent) string {
	return fmt.Sprintf("cetirun-%s-%s", instance.Name, instance.UID)
}

// aiGatewayTeamID is the Gateway team of a CetiRun Team. Keys of Agents in
// the Team belong to it, which gives administrators a Team total and per-Team
// usage in the Gateway.
func aiGatewayTeamID(team string) string {
	if team == "" {
		return ""
	}
	return aiGatewayTeamPrefix + team
}

// aiGatewayAccessGroups are the groups the Agent's key is granted: the shared
// models unless the owner turned them off, and the Agent's own Copilot models.
func (r *AgentReconciler) aiGatewayAccessGroups(instance *cetirunv1alpha1.Agent, copilot bool) []string {
	var groups []string
	if instance.Spec.SharedModelsEnabled() {
		groups = append(groups, aiGatewaySharedAccessGroup)
	}
	if copilot {
		groups = append(groups, copilotAccessGroup(instance))
	}
	slices.Sort(groups)
	return groups
}

// aiGatewayGrant is a non-secret key projection. Policy-bound shared grants
// use only collision-safe deployment groups, never the legacy broad group.
type aiGatewayGrant struct {
	groups      []string
	budget      float64
	copilot     bool
	active      bool
	policyBound bool
}

func (r *AgentReconciler) resolveAIGatewayGrant(ctx context.Context, client aiGatewayKeyClient, instance *cetirunv1alpha1.Agent) (aiGatewayGrant, error) {
	required, err := r.agentPolicyRequired(ctx, instance)
	if err != nil {
		return aiGatewayGrant{}, err
	}
	if !required {
		return r.resolveLegacyAIGatewayGrant(ctx, client, instance)
	}
	resolution, err := r.resolvePolicy(ctx, instance)
	if err != nil {
		return aiGatewayGrant{}, err
	}
	budgetText, models, err := effectivePolicyModels(instance, resolution.policy)
	if err != nil {
		return aiGatewayGrant{}, err
	}
	shared := instance.Spec.SharedModelsEnabled()
	if instance.Spec.GitHubCopilotEnabled() && !r.aiGatewayCopilotOffered() {
		return aiGatewayGrant{}, fmt.Errorf("GitHub Copilot was requested but this deployment does not offer a Copilot proxy")
	}
	copilot := instance.Spec.GitHubCopilotEnabled() && resolution.policy.Spec.ModelAccess.Copilot.Allowed
	if !shared && !copilot {
		return aiGatewayGrant{}, nil
	}
	if budgetText == "" {
		return aiGatewayGrant{}, fmt.Errorf("Policy model entitlement has no Gateway budget")
	}
	budget, err := strconv.ParseFloat(budgetText, 64)
	if err != nil || math.IsNaN(budget) || math.IsInf(budget, 0) || budget <= 0 {
		return aiGatewayGrant{}, fmt.Errorf("Policy Gateway budget is not a finite positive US-dollar value")
	}
	groups := []string{}
	if shared {
		// A current Gateway catalog is the authorization evidence that binds
		// policy model identifiers to concrete deployment groups. The seeded
		// Portal catalog intentionally cannot satisfy this check.
		if err := r.refreshAIGatewayCatalogForGrant(ctx, client); err != nil {
			return aiGatewayGrant{}, err
		}
		var complete bool
		groups, complete = r.aiGatewayCatalog.resolvedGroups(models)
		if !complete || len(groups) == 0 {
			return aiGatewayGrant{}, fmt.Errorf("the Gateway catalog has no collision-safe deployment group for an authorized shared model")
		}
	}
	if copilot {
		groups = append(groups, copilotAccessGroup(instance))
	}
	slices.Sort(groups)
	return aiGatewayGrant{groups: slices.Compact(groups), budget: budget, copilot: copilot, active: true, policyBound: true}, nil
}

func (r *AgentReconciler) resolveLegacyAIGatewayGrant(ctx context.Context, client aiGatewayKeyClient, instance *cetirunv1alpha1.Agent) (aiGatewayGrant, error) {
	copilot := r.aiGatewayCopilotWanted(instance)
	shared := instance.Spec.SharedModelsEnabled()
	if !shared && !copilot {
		return aiGatewayGrant{}, nil
	}
	groups := r.aiGatewayAccessGroups(instance, copilot)
	if shared {
		if err := r.refreshAIGatewayCatalogForGrant(ctx, client); err != nil {
			return aiGatewayGrant{}, err
		}
		// Current Gateway charts use per-deployment groups. Preserve access
		// to those as well as legacy catalog entries tagged cetirun-shared.
		groups = append(groups, r.aiGatewayCatalog.legacySharedGroups()...)
	}
	slices.Sort(groups)
	return aiGatewayGrant{
		groups: slices.Compact(groups), budget: r.AIGateway.DefaultBudget, copilot: copilot, active: true,
	}, nil
}

// aiGatewayKeyModels converts access groups to the key's model list. The
// Gateway treats an empty list as access to every model, so a key without
// groups carries LiteLLM's explicit no-access entry instead.
func aiGatewayKeyModels(groups []string) []string {
	if len(groups) == 0 {
		return []string{aigateway.NoModels}
	}
	models := slices.Clone(groups)
	slices.Sort(models)
	return models
}

// aiGatewayHarnessSupported reports whether the built-in harness is wired to
// the Gateway: its configuration takes the Gateway endpoint, key, and model
// list instead of a provider credential or in-agent Copilot login. The pinned
// chart must declare the harness-specific paths too (aiGatewayChartSupports).
func aiGatewayHarnessSupported(agentType cetirunv1alpha1.AgentType) bool {
	return agentType == cetirunv1alpha1.AgentTypeOpenClaw || agentType == cetirunv1alpha1.AgentTypeHermes
}

// aiGatewayChartValue and aiGatewayChartModelsValue are the agent chart
// values that wire the key, the model list and the Copilot proxy. Chart
// versions without them would ignore or reject routing values, so Agents
// pinned to them are not routed.
const (
	aiGatewayChartValue        = "aiGateway"
	aiGatewayChartModelsValue  = "models"
	aiGatewayChartCopilotValue = "copilotProxy"
	// aiGatewayChartHermesValue and aiGatewayChartHermesKeyEnvValue name the
	// variable that carries the key to Hermes. Charts that route only
	// OpenClaw do not declare it, and reject a routed Hermes.
	aiGatewayChartHermesValue       = "hermes"
	aiGatewayChartHermesKeyEnvValue = "keyEnv"
	// aiGatewayChartResponsesValue lists the Copilot models the proxy must
	// reach through the Responses API; older charts do not declare it.
	aiGatewayChartResponsesValue = "responsesModels"
	// aiGatewayChartProxyKeyValue identifies the proxy's master key on its
	// Pod template; older charts do not declare it.
	aiGatewayChartProxyKeyValue = "masterKeyId"
	// aiGatewayChartSignInValue identifies the sign-in whose models the Agent
	// uses on the proxy's Pod template; older charts do not declare it.
	aiGatewayChartSignInValue = "signInGeneration"
)

// chartValuePaths is the part of a prepared release that reports the values
// its chart declares.
type chartValuePaths interface {
	SupportsValuePath(path ...string) bool
}

// aiGatewayChartSupports reports whether the Agent's chart routes its harness
// through the Gateway: the key, the model list and the Copilot proxy, and for
// Hermes the variable that carries the key.
func aiGatewayChartSupports(prepared chartValuePaths, agentType cetirunv1alpha1.AgentType) bool {
	if !prepared.SupportsValuePath(aiGatewayChartValue, aiGatewayChartModelsValue) ||
		!prepared.SupportsValuePath(aiGatewayChartValue, aiGatewayChartCopilotValue) {
		return false
	}
	if agentType == cetirunv1alpha1.AgentTypeHermes {
		return prepared.SupportsValuePath(aiGatewayChartValue, aiGatewayChartHermesValue, aiGatewayChartHermesKeyEnvValue)
	}
	return true
}

// validImageReference accepts repository:tag, repository@sha256:<digest> or
// both, without whitespace.
func validImageReference(ref string) bool {
	if ref == "" || strings.ContainsAny(ref, " \t\r\n\"'") {
		return false
	}
	name, digest, hasDigest := strings.Cut(ref, "@")
	if hasDigest && !digestPattern.MatchString(digest) {
		return false
	}
	tagged := strings.LastIndex(name, ":") > strings.LastIndex(name, "/")
	return name != "" && (tagged || hasDigest)
}

var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// aiGatewayRequired reports whether the deployment wants this Agent's model
// traffic on the Gateway. Chart support and a provisioned key are checked
// separately.
func (r *AgentReconciler) aiGatewayRequired(instance *cetirunv1alpha1.Agent) bool {
	return r.AIGateway.Enabled && (aiGatewayHarnessSupported(instance.Spec.Agent.Type) || r.customGatewaySelected(instance))
}

// aiGatewayCopilotOffered reports whether the deployment gives Agents a
// GitHub Copilot proxy: it configured the proxy image.
func (r *AgentReconciler) aiGatewayCopilotOffered() bool {
	return r.AIGateway.CopilotProxyImage != ""
}

// aiGatewayCopilotWanted reports whether the Agent gets a GitHub Copilot
// proxy once routed: the owner kept the switch on and the deployment
// offers the proxy.
func (r *AgentReconciler) aiGatewayCopilotWanted(instance *cetirunv1alpha1.Agent) bool {
	return r.aiGatewayRequired(instance) && r.aiGatewayCopilotOffered() && instance.Spec.GitHubCopilotEnabled()
}

// aiGatewayRouted reports the routing decision recorded by the last
// successful key reconcile. It depends on a provisioned key rather than on
// current Gateway health, so a Gateway outage does not flip the provider.
func (r *AgentReconciler) aiGatewayRouted(instance *cetirunv1alpha1.Agent) bool {
	status := instance.Status.AIGateway
	return r.AIGateway.Enabled && status != nil && status.Routed && status.KeyID != "" && status.KeySecretName != ""
}

// aiGatewayCopilotActive reports whether the routed Agent runs a Copilot
// proxy, as recorded by the last key reconcile.
func (r *AgentReconciler) aiGatewayCopilotActive(instance *cetirunv1alpha1.Agent) bool {
	return r.aiGatewayRouted(instance) && r.aiGatewayCopilotWanted(instance) && instance.Status.AIGateway.Copilot != nil
}

func aiGatewayVerificationDue(instance *cetirunv1alpha1.Agent, enabled bool, now time.Time) bool {
	if !enabled {
		return false
	}
	status := instance.Status.AIGateway
	if status == nil || status.Phase != cetirunv1alpha1.AIGatewayPhaseReady || status.LastVerifiedTime == nil {
		return true
	}
	return now.Sub(status.LastVerifiedTime.Time) >= aiGatewayVerificationInterval
}

// aiGatewayReconcileDue reports whether the Agent needs the full Gateway
// reconcile now rather than the lightweight pass: its key verification is
// due, its Copilot proxy is starting or failed, or the Gateway no longer has
// one of its Copilot routes. Without the last, an Agent queued because its
// routes are gone would take the lightweight pass and wait for the
// verification after all.
func (r *AgentReconciler) aiGatewayReconcileDue(instance *cetirunv1alpha1.Agent, now time.Time) bool {
	enabled := r.AIGateway.Enabled
	return aiGatewayVerificationDue(instance, enabled, now) || aiGatewayCopilotPending(instance, enabled) ||
		enabled && r.copilotRoutesLost(instance)
}

// aiGatewayCopilotPending reports a Copilot proxy that is starting or failed
// to register its models. Such an Agent is reconciled on its short requeue
// rather than waiting for the next key verification.
func aiGatewayCopilotPending(instance *cetirunv1alpha1.Agent, enabled bool) bool {
	status := instance.Status.AIGateway
	if !enabled || status == nil || status.Copilot == nil {
		return false
	}
	phase := status.Copilot.Phase
	return phase == cetirunv1alpha1.AIGatewayCopilotPhaseStarting || phase == cetirunv1alpha1.AIGatewayCopilotPhaseDegraded
}

func ensureAIGatewayStatus(instance *cetirunv1alpha1.Agent) *cetirunv1alpha1.AIGatewayStatus {
	if instance.Status.AIGateway == nil {
		instance.Status.AIGateway = &cetirunv1alpha1.AIGatewayStatus{}
	}
	return instance.Status.AIGateway
}

func aiGatewayDegraded(instance *cetirunv1alpha1.Agent, reason string, err error) error {
	status := ensureAIGatewayStatus(instance)
	status.Phase = cetirunv1alpha1.AIGatewayPhaseDegraded
	// Reason is also the durable ownership receipt for a block made by the
	// operator. Keep that receipt while reporting the more specific failure on
	// AIGatewayReady. Otherwise a transient grant/catalog failure after the
	// key was blocked would relabel it as an unknown administrator block, and
	// the operator could never safely restore the same key after recovery.
	if !aiGatewayOperatorOwnedBlock(status.Reason) {
		status.Reason = reason
	}
	setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionAIGatewayReady,
		metav1.ConditionFalse, reason, err.Error())
	return err
}

// aiGatewayOwnerCache remembers Gateway teams, users and memberships the
// operator already ensured, so a steady reconcile does not repeat the calls.
type aiGatewayOwnerCache struct {
	mu      sync.Mutex
	ensured map[string]time.Time
}

func (c *aiGatewayOwnerCache) fresh(key string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	at, ok := c.ensured[key]
	return ok && now.Sub(at) < aiGatewayOwnerCacheTTL
}

func (c *aiGatewayOwnerCache) mark(key string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ensured == nil {
		c.ensured = map[string]time.Time{}
	}
	c.ensured[key] = now
}

// ensureAIGatewayOwner makes sure the Gateway team of the Agent's Team, the
// owner's Gateway user and the membership exist. Keys carry both, so usage
// can be read per Team and per user, and a Team total applies to every
// Agent in the Team. It returns the team and user IDs for the key. force
// ignores what the cache says was already ensured.
func (r *AgentReconciler) ensureAIGatewayOwner(ctx context.Context, client aiGatewayKeyClient, instance *cetirunv1alpha1.Agent, force bool) (string, string, error) {
	now := time.Now()
	teamID := aiGatewayTeamID(instance.Spec.Team)
	userID := instance.Spec.User.Alias
	if teamID != "" && (force || !r.aiGatewayOwners.fresh("team:"+teamID, now)) {
		if err := client.EnsureTeam(ctx, teamID, instance.Spec.Team, map[string]string{"cetirun_team": instance.Spec.Team}); err != nil {
			return "", "", fmt.Errorf("ensure Gateway team %s: %w", teamID, err)
		}
		r.aiGatewayOwners.mark("team:"+teamID, now)
	}
	if userID != "" && (force || !r.aiGatewayOwners.fresh("user:"+userID, now)) {
		if err := client.EnsureUser(ctx, userID, map[string]string{"cetirun_user": userID}); err != nil {
			return "", "", fmt.Errorf("ensure Gateway user %s: %w", userID, err)
		}
		r.aiGatewayOwners.mark("user:"+userID, now)
	}
	if teamID != "" && userID != "" && (force || !r.aiGatewayOwners.fresh("member:"+teamID+"/"+userID, now)) {
		if err := client.EnsureTeamMember(ctx, teamID, userID); err != nil {
			return "", "", fmt.Errorf("add Gateway user %s to team %s: %w", userID, teamID, err)
		}
		r.aiGatewayOwners.mark("member:"+teamID+"/"+userID, now)
	}
	return teamID, userID, nil
}

// reconcileAIGateway makes sure the Agent's virtual key exists in the Gateway
// with the desired access groups, team and user, and reconciles the Agent's
// Copilot proxy registrations and model list. The key value is written to the
// instance Secret before the create request, so an unknown create outcome is
// resolved by looking the value up instead of creating a second key. The
// budget, access groups, and blocked state are policy reconciled and read
// back without replacing the key, so current-period LiteLLM spend survives.
// chartSupported reports whether the Agent's pinned chart can route model
// traffic through the Gateway.
func (r *AgentReconciler) reconcileAIGateway(ctx context.Context, instance *cetirunv1alpha1.Agent, chartSupported bool) error {
	// Only a due verification moves the verification time: a status that
	// changed on every pass would re-queue the Agent at once, again and again.
	verificationDue := aiGatewayVerificationDue(instance, true, time.Now())
	status := ensureAIGatewayStatus(instance)
	status.KeyAlias = aiGatewayKeyAlias(instance)
	status.KeySecretName = aiGatewayKeySecretName
	status.ObservedGeneration = instance.Generation
	if status.Phase == "" {
		status.Phase = cetirunv1alpha1.AIGatewayPhaseProvisioning
	}
	client, err := r.aiGatewayClient(ctx)
	if err != nil {
		return aiGatewayDegraded(instance, "GatewayConfigUnavailable", err)
	}
	grant, err := r.resolveAIGatewayGrant(ctx, client, instance)
	if err != nil {
		return aiGatewayDegraded(instance, "PolicyGrantInvalid", err)
	}
	if !grant.active {
		blocked, err := r.blockAIGatewayKeyIfPresent(ctx, client, instance, aiGatewayReasonNoModels)
		if err != nil {
			if aiGatewayOperatorOwnedBlock(status.Reason) {
				return err
			}
			return aiGatewayDegraded(instance, "KeyBlockFailed", err)
		}
		status = ensureAIGatewayStatus(instance)
		status.Routed, status.AccessGroups, status.Models, status.DefaultModel, status.Blocked = false, nil, nil, "", blocked
		status.Phase, status.Reason = cetirunv1alpha1.AIGatewayPhaseReady, aiGatewayReasonNoModels
		setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionAIGatewayReady,
			metav1.ConditionFalse, aiGatewayReasonNoModels, "The effective policy grants no AI Gateway model source")
		// Do not let a no-entitlement Agent fall back to the chart's prior
		// provider configuration. The reconciler stops before a Helm render;
		// an existing Gateway key has already been blocked above.
		return aiGatewayDegraded(instance, aiGatewayReasonNoModels, fmt.Errorf("the effective policy grants no AI Gateway model source"))
	}
	routed := r.aiGatewayRequired(instance) && chartSupported
	copilot := routed && grant.copilot
	groups := slices.Clone(grant.groups)
	models := aiGatewayKeyModels(groups)
	teamID, userID, err := r.ensureAIGatewayOwner(ctx, client, instance, false)
	if err != nil {
		return aiGatewayDegraded(instance, "GatewayOwnerFailed", err)
	}
	value, err := r.ensureAIGatewayKeySecret(ctx, instance, false)
	if err != nil {
		if errors.Is(err, errAIGatewayKeySecretLost) {
			if status.Reason != aiGatewayReasonKeySecretLost {
				r.EventRecorder.Eventf(instance, corev1.EventTypeWarning, "AIGatewayKeySecretLost",
					"The Agent's virtual key %s was previously issued, but its value is no longer held in Secret %s. Restore the Secret or delete the key in the Gateway before issuing a replacement.",
					status.KeyAlias, aiGatewayKeySecretName)
			}
			return aiGatewayDegraded(instance, aiGatewayReasonKeySecretLost, err)
		}
		return aiGatewayDegraded(instance, "KeySecretFailed", err)
	}
	token := aigateway.Token(value)
	key, err := client.GetKey(ctx, token)
	if err != nil {
		return aiGatewayDegraded(instance, "GatewayUnavailable", err)
	}
	switch {
	case key == nil && status.KeyID != "":
		return aiGatewayDegraded(instance, aiGatewayReasonKeyRecordLost,
			fmt.Errorf("the previously issued virtual key record is missing from the AI Gateway; explicit recovery is required to preserve spend and block intent"))
	case key == nil:
		// A missing key can mean the Gateway lost its data, teams and users
		// included: ensure the owner again before creating the key.
		if _, _, err := r.ensureAIGatewayOwner(ctx, client, instance, true); err != nil {
			return aiGatewayDegraded(instance, "GatewayOwnerFailed", err)
		}
		request := aigateway.GenerateRequest{
			Key: value, Alias: status.KeyAlias, Models: models,
			MaxBudget: grant.budget, BudgetDuration: aiGatewayBudgetDuration,
			Blocked: instance.Spec.Suspended,
			TeamID:  teamID, UserID: userID,
			Aliases: copilotKeyAliases(status.Copilot),
			Metadata: map[string]string{
				"cetirun_agent": instance.Name, "cetirun_agent_uid": string(instance.UID),
				"cetirun_user": instance.Spec.User.Alias, "cetirun_team": instance.Spec.Team,
			},
		}
		if instance.Spec.Suspended {
			if err := r.recordAIGatewayBlockIntent(ctx, instance, aiGatewayReasonSuspended,
				"The AI Gateway key will remain blocked while the Agent is suspended"); err != nil {
				return fmt.Errorf("persist AI Gateway suspension intent: %w", err)
			}
		}
		err := client.GenerateKey(ctx, request)
		if errors.Is(err, aigateway.ErrAliasExists) {
			// The alias embeds this Agent's UID, so the existing key is this
			// Agent's, with a value no longer held here (for example, the
			// Secret was deleted). Replacing it would reset its spend, put
			// back the default budget in place of an administrator's and
			// unblock a blocked key, so an administrator decides instead.
			if status.Reason != aiGatewayReasonKeySecretLost {
				r.EventRecorder.Eventf(instance, corev1.EventTypeWarning, "AIGatewayKeySecretLost",
					"The Agent's virtual key %s exists in the AI Gateway, but its value is no longer in the Secret %s. "+
						"Restore the Secret, or delete the key in the Gateway to have a new key created with the default budget.",
					status.KeyAlias, aiGatewayKeySecretName)
			}
			return aiGatewayDegraded(instance, aiGatewayReasonKeySecretLost,
				fmt.Errorf("the virtual key %s exists in the Gateway, but its value is no longer held", status.KeyAlias))
		}
		if err != nil {
			if instance.Spec.Suspended {
				return fmt.Errorf("create blocked AI Gateway key for suspended Agent: %w", err)
			}
			return aiGatewayDegraded(instance, "KeyCreateFailed", err)
		}
		r.EventRecorder.Eventf(instance, corev1.EventTypeNormal, "AIGatewayKeyCreated",
			"Created AI Gateway virtual key %s", status.KeyAlias)
	case key.Alias != status.KeyAlias:
		// Never adopt or change a key that belongs to another alias. Replace
		// the Secret value; the next pass creates this Agent's own key.
		if _, err := r.ensureAIGatewayKeySecret(ctx, instance, true); err != nil {
			return aiGatewayDegraded(instance, "KeySecretFailed", err)
		}
		return aiGatewayDegraded(instance, "KeyAliasMismatch",
			fmt.Errorf("the virtual key Secret held a key with another alias; a new key will be created"))
	default:
		// An already-blocked key is operator-owned only when a previous
		// suspension or no-entitlement transition recorded that fact. Never
		// relabel an administrator emergency block as a suspension: doing so
		// would make a later resume silently unblock it.
		if key.Blocked && !aiGatewayOperatorOwnedBlock(status.Reason) {
			return aiGatewayDegraded(instance, "KeyBlocked", fmt.Errorf("the AI Gateway key is blocked outside a CetiRun suspension or no-entitlement transition"))
		}
		// Suspend is a revocation boundary. Block before doing any other
		// update; later read-back proves the key stayed the same.
		if instance.Spec.Suspended && !key.Blocked {
			if err := r.recordAIGatewayBlockIntent(ctx, instance, aiGatewayReasonSuspended,
				"The AI Gateway key will be blocked while the Agent is suspended"); err != nil {
				return fmt.Errorf("persist AI Gateway suspension intent: %w", err)
			}
			if err := client.SetKeyBlocked(ctx, token, true); err != nil {
				return fmt.Errorf("block AI Gateway key for suspended Agent: %w", err)
			}
			key.Blocked = true
		}
		if !slices.Equal(sortedCopy(key.Models), models) {
			if err := client.UpdateKeyModels(ctx, token, models); err != nil {
				return aiGatewayDegraded(instance, "KeyUpdateFailed", err)
			}
			r.EventRecorder.Eventf(instance, corev1.EventTypeNormal, "AIGatewayKeyUpdated",
				"Updated AI Gateway virtual key %s access to %s", status.KeyAlias, strings.Join(models, ","))
		}
		if key.TeamID != teamID || key.UserID != userID {
			if err := client.UpdateKeyOwner(ctx, token, teamID, userID); err != nil {
				return aiGatewayDegraded(instance, "KeyUpdateFailed", err)
			}
		}
		if grant.policyBound && (key.MaxBudget == nil || math.Abs(*key.MaxBudget-grant.budget) > 0.0000001 || key.BudgetDuration != aiGatewayBudgetDuration) {
			if err := client.UpdateKeyBudget(ctx, token, grant.budget, aiGatewayBudgetDuration); err != nil {
				return aiGatewayDegraded(instance, "KeyBudgetUpdateFailed", err)
			}
		}
	}
	// Management responses are not enforcement evidence. Re-read the stored
	// key after every create/update and reject drift before exposing the
	// workload as routed.
	key, err = client.GetKey(ctx, token)
	if err != nil {
		return aiGatewayDegraded(instance, "GatewayUnavailable", err)
	}
	if key == nil || key.Alias != status.KeyAlias || key.TeamID != teamID || key.UserID != userID ||
		!slices.Equal(sortedCopy(key.Models), models) ||
		(grant.policyBound && (key.MaxBudget == nil || math.Abs(*key.MaxBudget-grant.budget) > 0.0000001 || key.BudgetDuration != aiGatewayBudgetDuration)) {
		return aiGatewayDegraded(instance, "KeyReadbackMismatch", fmt.Errorf("the AI Gateway key does not match its resolved grant"))
	}
	if instance.Spec.Suspended {
		if !key.Blocked {
			return aiGatewayDegraded(instance, "KeyReadbackMismatch", fmt.Errorf("the suspended Agent key is not blocked"))
		}
	} else if key.Blocked {
		if !aiGatewayOperatorOwnedBlock(status.Reason) {
			return aiGatewayDegraded(instance, "KeyBlocked", fmt.Errorf("the AI Gateway key is blocked outside a CetiRun suspension or no-entitlement transition"))
		}
		if err := client.SetKeyBlocked(ctx, token, false); err != nil {
			return aiGatewayDegraded(instance, "KeyUnblockFailed", err)
		}
		key, err = client.GetKey(ctx, token)
		if err != nil || key == nil || key.Blocked {
			if err == nil {
				err = fmt.Errorf("the AI Gateway key remained blocked after resume")
			}
			return aiGatewayDegraded(instance, "KeyReadbackMismatch", err)
		}
	}
	// A write-ahead block intent may have refreshed instance.Status and
	// invalidated a pointer taken before that status patch.
	status = ensureAIGatewayStatus(instance)
	now := metav1.Now()
	status.KeyID = token[:aiGatewayKeyIDLength]
	status.AccessGroups = groups
	status.Team = teamID
	status.BudgetUSD = ""
	if key.MaxBudget != nil {
		status.BudgetUSD = strconv.FormatFloat(*key.MaxBudget, 'f', -1, 64)
	}
	status.BudgetDuration = key.BudgetDuration
	status.Blocked = key.Blocked
	if key.Spend != nil {
		status.SpendUSD = strconv.FormatFloat(*key.Spend, 'f', -1, 64)
	} else {
		status.SpendUSD = ""
	}
	if key.BudgetResetAt != nil {
		resetAt := metav1.NewTime(*key.BudgetResetAt)
		status.BudgetResetAt = &resetAt
	} else {
		status.BudgetResetAt = nil
	}
	if instance.Status.Policy != nil {
		status.PolicyRevision = instance.Status.Policy.Enforcement.Gateway.DesiredRevision
	} else {
		status.PolicyRevision = ""
	}
	if fingerprint, known := r.aiGatewayCatalog.managedFingerprint(); known {
		status.PricingRevision = fingerprint
	} else {
		status.PricingRevision = ""
	}
	status.Phase = cetirunv1alpha1.AIGatewayPhaseReady
	if instance.Spec.Suspended {
		status.Reason = aiGatewayReasonSuspended
	} else {
		status.Reason = ""
	}
	if verificationDue || status.LastVerifiedTime == nil {
		status.LastVerifiedTime = &now
	}
	status.Routed = routed

	// Copilot registration problems are reported on the Copilot status and
	// never block the key: shared models keep working.
	r.reconcileAIGatewayCopilot(ctx, client, instance, copilot && !instance.Spec.Suspended)
	r.reconcileAIGatewayKeyAliases(ctx, client, instance, token, key)
	r.refreshAIGatewayCatalog(ctx, client)
	if routed {
		entries, defaultModel, omitted := r.aiGatewayModelEntries(instance)
		status.Models = make([]string, 0, len(entries))
		for _, entry := range entries {
			status.Models = append(status.Models, entry.ID)
		}
		status.DefaultModel = defaultModel
		if len(omitted) > 0 {
			listed := omitted
			if len(listed) > 5 {
				listed = append(slices.Clone(listed[:5]), "...")
			}
			r.EventRecorder.Eventf(instance, corev1.EventTypeWarning, "AIGatewayModelsLimited",
				"%d shared models are left out of the Agent's configuration, which lists at most %d models: %s",
				len(omitted), aiGatewayMaxModels, strings.Join(listed, ", "))
		}
	} else {
		status.Models, status.DefaultModel = nil, ""
	}

	message := "AI Gateway virtual key is ready"
	if r.aiGatewayRequired(instance) && !chartSupported {
		message = "AI Gateway virtual key is ready; the Agent's chart version cannot route model traffic through the Gateway"
	}
	setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionAIGatewayReady,
		metav1.ConditionTrue, "VirtualKeyReady", message)
	return nil
}

func aiGatewayOperatorOwnedBlock(reason string) bool {
	return reason == aiGatewayReasonSuspended || reason == aiGatewayReasonNoModels || reason == aiGatewayReasonPolicyReprojection
}

// recordAIGatewayBlockIntent durably records why CetiRun is allowed to own a
// blocked key before changing the external Gateway. A restart can then finish
// the transition without adopting an administrator's pre-existing block.
func (r *AgentReconciler) recordAIGatewayBlockIntent(ctx context.Context, instance *cetirunv1alpha1.Agent, reason, message string) error {
	status := ensureAIGatewayStatus(instance)
	if status.Reason == reason {
		return nil
	}
	status.Blocked = false
	status.Routed = false
	status.Reason = reason
	status.Phase = cetirunv1alpha1.AIGatewayPhaseReady
	setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionAIGatewayReady,
		metav1.ConditionFalse, reason, message)
	return r.updateStatus(ctx, instance)
}

// beginAIGatewayPolicyReprojection blocks an active operator-owned key before
// the Agent controller holds its workload for a changed policy revision. It
// records distinct ownership for this temporary block, so reconciliation can
// clear it only after it has read back the replacement grant. A pre-existing
// block without that ownership remains an administrator emergency block.
func (r *AgentReconciler) beginAIGatewayPolicyReprojection(ctx context.Context, instance *cetirunv1alpha1.Agent) error {
	secret, err := r.aiGatewayKeySecret(ctx, instance)
	if err != nil {
		return err
	}
	if secret == nil || !aigateway.ValidKeyValue(string(secret.Data[aiGatewayKeySecretField])) {
		if aiGatewayKeyWasIssued(instance) {
			return errAIGatewayKeySecretLost
		}
		return nil
	}
	client, err := r.aiGatewayClient(ctx)
	if err != nil {
		return err
	}
	token := aigateway.Token(string(secret.Data[aiGatewayKeySecretField]))
	key, err := client.GetKey(ctx, token)
	if err != nil {
		return err
	}
	if key == nil {
		if aiGatewayKeyWasIssued(instance) {
			return fmt.Errorf("the previously issued AI Gateway key record is missing")
		}
		return nil
	}
	if key.Alias != aiGatewayKeyAlias(instance) {
		return fmt.Errorf("the AI Gateway key Secret identifies another key alias")
	}
	status := ensureAIGatewayStatus(instance)
	if key.Blocked && !aiGatewayOperatorOwnedBlock(status.Reason) {
		return fmt.Errorf("the AI Gateway key is already blocked outside an operator policy reprojection")
	}
	// Persist ownership before the external side effect. If this write fails,
	// compute is still held by the caller but the key is left untouched. If a
	// later receipt write fails after blocking, the next reconcile can safely
	// resume because this durable intent distinguishes the block from an
	// administrator emergency block.
	if status.Reason != aiGatewayReasonPolicyReprojection {
		status.Blocked = false
		status.Routed = false
		status.Reason = aiGatewayReasonPolicyReprojection
		status.Phase = cetirunv1alpha1.AIGatewayPhaseReady
		setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionAIGatewayReady,
			metav1.ConditionFalse, aiGatewayReasonPolicyReprojection, "The old AI Gateway grant will be blocked until the current policy projection is verified")
		if err := r.updateStatus(ctx, instance); err != nil {
			return fmt.Errorf("persist AI Gateway policy-reprojection intent: %w", err)
		}
		status = ensureAIGatewayStatus(instance)
	}
	if !key.Blocked {
		if err := client.SetKeyBlocked(ctx, token, true); err != nil {
			return err
		}
		key, err = client.GetKey(ctx, token)
		if err != nil {
			return err
		}
		if key == nil || !key.Blocked {
			return fmt.Errorf("the AI Gateway key was not blocked for policy reprojection")
		}
	}
	status.Blocked = true
	status.Routed = false
	status.Reason = aiGatewayReasonPolicyReprojection
	status.Phase = cetirunv1alpha1.AIGatewayPhaseReady
	setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionAIGatewayReady,
		metav1.ConditionFalse, aiGatewayReasonPolicyReprojection, "The old AI Gateway grant is blocked until the current policy projection is verified")
	return r.updateStatus(ctx, instance)
}

// blockAIGatewayKeyIfPresent revokes an existing operator-owned key without
// creating a Secret or a replacement key. It is used for an entitlement that
// has tightened to no model sources. A missing Secret cannot safely identify
// a key, so the UID-derived alias remains untouched rather than risking an
// unrelated key or resetting spend.
func (r *AgentReconciler) blockAIGatewayKeyIfPresent(ctx context.Context, client aiGatewayKeyClient, instance *cetirunv1alpha1.Agent, reason string) (bool, error) {
	secret, err := r.aiGatewayKeySecret(ctx, instance)
	if err != nil {
		return false, err
	}
	if secret == nil {
		if aiGatewayKeyWasIssued(instance) {
			return false, errAIGatewayKeySecretLost
		}
		return false, nil
	}
	value := string(secret.Data[aiGatewayKeySecretField])
	if !aigateway.ValidKeyValue(value) {
		if aiGatewayKeyWasIssued(instance) {
			return false, errAIGatewayKeySecretLost
		}
		return false, nil
	}
	token := aigateway.Token(value)
	key, err := client.GetKey(ctx, token)
	if err != nil || key == nil {
		return false, err
	}
	if key.Alias != aiGatewayKeyAlias(instance) {
		return false, fmt.Errorf("the AI Gateway key Secret identifies another key alias")
	}
	status := ensureAIGatewayStatus(instance)
	if key.Blocked && !aiGatewayOperatorOwnedBlock(status.Reason) {
		return false, fmt.Errorf("the AI Gateway key is already blocked outside a CetiRun policy transition")
	}
	if status.Reason != reason {
		if err := r.recordAIGatewayBlockIntent(ctx, instance, reason,
			"The AI Gateway key will be blocked because the effective policy grants no model source"); err != nil {
			return false, fmt.Errorf("persist AI Gateway block intent: %w", err)
		}
	}
	if !key.Blocked {
		if err := client.SetKeyBlocked(ctx, token, true); err != nil {
			return false, err
		}
	}
	key, err = client.GetKey(ctx, token)
	if err != nil {
		return false, err
	}
	if key == nil || !key.Blocked {
		return false, fmt.Errorf("the AI Gateway key was not blocked")
	}
	return true, nil
}

func aiGatewayKeyWasIssued(instance *cetirunv1alpha1.Agent) bool {
	return instance.Status.AIGateway != nil && instance.Status.AIGateway.KeyID != ""
}

func sortedCopy(values []string) []string {
	out := slices.Clone(values)
	slices.Sort(out)
	return out
}

// ensureAIGatewayKeySecret returns the Agent's key value, creating the Secret
// when needed. A Secret written for another Agent UID, or holding a value in
// an unexpected form, gets a new value; replace forces a new value.
func (r *AgentReconciler) ensureAIGatewayKeySecret(ctx context.Context, instance *cetirunv1alpha1.Agent, replace bool) (string, error) {
	// Once a key ID was recorded, a missing or malformed Secret is an
	// irrecoverable continuity boundary. Generating a new value before proving
	// the old key is absent can create a second alias after a transient Gateway
	// outage and resets the old key's spend. Only an Agent with no recorded key
	// is allowed to create its first Secret here.
	if !replace && aiGatewayKeyWasIssued(instance) {
		secret, err := r.aiGatewayKeySecret(ctx, instance)
		if err != nil {
			return "", err
		}
		if secret == nil || !aigateway.ValidKeyValue(string(secret.Data[aiGatewayKeySecretField])) {
			return "", errAIGatewayKeySecretLost
		}
	}
	return r.ensureAgentOwnedValue(ctx, instance, aiGatewayKeySecretName, aiGatewayKeySecretField, replace,
		map[string]string{aiGatewayKeyAliasAnnotation: aiGatewayKeyAlias(instance)})
}

// ensureAgentOwnedValue keeps a generated sk- value in an operator-owned
// instance-namespace Secret labelled with the Agent UID. A Secret of another
// Agent UID, or one holding a malformed value, gets a new value.
func (r *AgentReconciler) ensureAgentOwnedValue(ctx context.Context, instance *cetirunv1alpha1.Agent, name, field string, replace bool, annotations map[string]string) (string, error) {
	key := types.NamespacedName{Namespace: instanceNamespace(instance.Name), Name: name}
	secret := &corev1.Secret{}
	err := r.Get(ctx, key, secret)
	if err != nil && !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("get Secret %s: %w", name, err)
	}
	exists := err == nil
	if exists && !replace {
		value := string(secret.Data[field])
		if secret.Labels[aiGatewayAgentUIDLabel] == string(instance.UID) && aigateway.ValidKeyValue(value) {
			base := secret.DeepCopy()
			labelAgentSecret(secret, instance)
			if secret.Annotations == nil {
				secret.Annotations = map[string]string{}
			}
			for k, v := range annotations {
				secret.Annotations[k] = v
			}
			if !equality.Semantic.DeepEqual(base.Labels, secret.Labels) ||
				!equality.Semantic.DeepEqual(base.Annotations, secret.Annotations) ||
				!equality.Semantic.DeepEqual(base.OwnerReferences, secret.OwnerReferences) {
				if err := r.Update(ctx, secret); err != nil {
					return "", fmt.Errorf("repair Secret %s ownership: %w", name, err)
				}
			}
			return value, nil
		}
	}
	value, err := aigateway.NewKeyValue()
	if err != nil {
		return "", err
	}
	labelAgentSecret(secret, instance)
	if secret.Annotations == nil {
		secret.Annotations = map[string]string{}
	}
	for k, v := range annotations {
		secret.Annotations[k] = v
	}
	secret.Type = corev1.SecretTypeOpaque
	secret.Data = map[string][]byte{field: []byte(value)}
	if exists {
		if err := r.Update(ctx, secret); err != nil {
			return "", fmt.Errorf("replace Secret %s: %w", name, err)
		}
		return value, nil
	}
	secret.Name, secret.Namespace = key.Name, key.Namespace
	if err := r.Create(ctx, secret); err != nil {
		return "", fmt.Errorf("create Secret %s: %w", name, err)
	}
	return value, nil
}

func labelAgentSecret(secret *corev1.Secret, instance *cetirunv1alpha1.Agent) {
	if secret.Labels == nil {
		secret.Labels = map[string]string{}
	}
	secret.Labels["app.kubernetes.io/managed-by"] = "cetirun-operator"
	secret.Labels["app.kubernetes.io/instance"] = instance.Spec.User.Alias
	secret.Labels[aiGatewayAgentUIDLabel] = string(instance.UID)
	// Every operator-generated credential projection has the same Agent
	// lifetime, including native Hermes and OpenClaw Sandboxes.  The Sandbox
	// execution adapter requires this ownership proof before it authorizes the
	// Secret in a Pod; a UID label alone is mutable metadata, not ownership.
	secret.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(instance, cetirunv1alpha1.GroupVersion.WithKind("Agent"))}
}

// deleteAIGatewayKey revokes the Agent's key and removes its Copilot model
// registrations before its workload and namespace are removed. Revocation is
// by the UID-derived alias and, when the key Secret is still readable, by the
// key itself, so a key is removed even if an administrator renamed it, or the
// operator stopped before it saved the Agent's status, or the Secret is gone.
// Whenever a Gateway is configured, deletion waits while revocation fails,
// whether or not the deployment still creates keys: turning that off removes
// no key it created. Only a deployment with no Gateway configured at all has
// nothing to call, which is reported as a warning event for an Agent that
// recorded a key.
func (r *AgentReconciler) deleteAIGatewayKey(ctx context.Context, instance *cetirunv1alpha1.Agent) error {
	if !r.AIGateway.configured() {
		if instance.Status.AIGateway != nil {
			r.EventRecorder.Event(instance, corev1.EventTypeWarning, "AIGatewayKeyNotRevoked",
				"The AI Gateway is not configured, so the Agent's virtual key could not be revoked")
		}
		return nil
	}
	return r.revokeAIGatewayKey(ctx, instance)
}

// aiGatewayKeySecret returns the Agent's virtual key Secret, or nil when its
// namespace holds none labelled with this Agent's UID. A namespace that does
// not exist holds none: that is established first, with the namespace read
// the deletion path already needs, so an Agent that was never provisioned is
// finalized without any access to Secrets.
func (r *AgentReconciler) aiGatewayKeySecret(ctx context.Context, instance *cetirunv1alpha1.Agent) (*corev1.Secret, error) {
	namespace := instanceNamespace(instance.Name)
	if err := r.customReader().Get(ctx, types.NamespacedName{Name: namespace}, &corev1.Namespace{}); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get Agent namespace: %w", err)
	}
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: aiGatewayKeySecretName}, secret)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get AI Gateway key Secret: %w", err)
	}
	if secret.Labels[aiGatewayAgentUIDLabel] != string(instance.UID) {
		return nil, nil
	}
	return secret, nil
}

func (r *AgentReconciler) revokeAIGatewayKey(ctx context.Context, instance *cetirunv1alpha1.Agent) error {
	var failures []error
	status := ensureAIGatewayStatus(instance)
	if status.Phase != cetirunv1alpha1.AIGatewayPhaseDeleting {
		status.Phase = cetirunv1alpha1.AIGatewayPhaseDeleting
		status.Routed = false
		if err := r.updateStatus(ctx, instance); err != nil {
			// Status is observability, not the revocation boundary. Continue all
			// independent cleanup attempts even when the deletion gate cannot be
			// persisted, then return the joined failures for retry.
			failures = append(failures, fmt.Errorf("persist AI Gateway deletion gate: %w", err))
		}
	}
	client, err := r.aiGatewayClient(ctx)
	if err != nil {
		failures = append(failures, err)
		return errors.Join(failures...)
	}
	if err := r.deleteCopilotRegistrations(ctx, client, instance); err != nil {
		failures = append(failures, fmt.Errorf("remove Copilot model registrations: %w", err))
	}
	secret, err := r.aiGatewayKeySecret(ctx, instance)
	if err != nil {
		failures = append(failures, err)
	}
	if secret != nil {
		if value := string(secret.Data[aiGatewayKeySecretField]); aigateway.ValidKeyValue(value) {
			if err := client.DeleteKeys(ctx, []string{aigateway.Token(value)}); err != nil {
				failures = append(failures, fmt.Errorf("revoke AI Gateway virtual key: %w", err))
			}
		}
	}
	if err := client.DeleteKeyByAlias(ctx, aiGatewayKeyAlias(instance)); err != nil {
		failures = append(failures, fmt.Errorf("revoke AI Gateway virtual key by alias: %w", err))
	}
	if len(failures) == 0 {
		r.EventRecorder.Eventf(instance, corev1.EventTypeNormal, "AIGatewayKeyRevoked",
			"Revoked AI Gateway virtual key %s", aiGatewayKeyAlias(instance))
	}
	return errors.Join(failures...)
}

// recordSignInHelper records whether the Agent's workload serves the in-agent
// model sign-in helper; the /<name>/cetirun-auth/ route and the Portal's
// in-agent sign-in follow it. An OpenClaw chart without the helper takes its
// models only from the AI Gateway, so with the Gateway disabled the Agent
// reports why it has no model sign-in.
func (r *AgentReconciler) recordSignInHelper(instance *cetirunv1alpha1.Agent, serves bool) {
	instance.Status.SignInHelper = &serves
	if !r.AIGateway.Enabled && !serves && instance.Spec.Agent.Type == cetirunv1alpha1.AgentTypeOpenClaw {
		setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionAIGatewayReady, metav1.ConditionFalse,
			aiGatewayReasonDisabled, "This OpenClaw chart takes its models only from the AI Gateway, which the deployment has not enabled")
	}
}

// servesSignInHelper reports whether the Agent's workload serves the in-agent
// model sign-in helper, as recorded at its last render. Before the operator
// recorded it, the helper ran wherever the AI Gateway did not replace it.
func servesSignInHelper(instance *cetirunv1alpha1.Agent) bool {
	if instance.Status.SignInHelper != nil {
		return *instance.Status.SignInHelper
	}
	return !aiGatewayRoutedStatus(instance)
}

// applyAIGatewayValues renders the controller-owned model access values. It
// runs after per-instance Helm values, so a routed Agent cannot select
// another provider, endpoint, key or proxy.
func (r *AgentReconciler) applyAIGatewayValues(values map[string]any, instance *cetirunv1alpha1.Agent) error {
	values["aiGateway"] = map[string]any{"enabled": false, "keySecretName": "", "keySecretKey": aiGatewayKeySecretField}
	if !r.aiGatewayRouted(instance) {
		return nil
	}
	namespace, port, err := aiGatewayEndpoint(r.AIGateway.URL)
	if err != nil {
		return err
	}
	entries, defaultModel, _ := r.aiGatewayModelEntries(instance)
	models := make([]any, 0, len(entries))
	for _, entry := range entries {
		model := map[string]any{"id": entry.ID, "name": entry.Name}
		if entry.ContextWindow > 0 {
			model["contextWindow"] = entry.ContextWindow
		}
		if entry.MaxTokens > 0 {
			model["maxTokens"] = entry.MaxTokens
		}
		models = append(models, model)
	}
	values["secrets"] = deepMerge(values["secrets"], map[string]any{
		"provider": "openai",
		"authMode": "api_key",
		"apiBase":  r.AIGateway.URL + "/v1",
		// Provider credentials may not survive in the chart-created Secret or
		// Hermes persistent home when an Agent is routed through the Gateway.
		// The chart consumes the virtual key from its separate Secret instead.
		"apiKey": "",
		"model":  defaultModel,
	})
	proxy := map[string]any{"enabled": false}
	if r.aiGatewayCopilotActive(instance) {
		proxy = map[string]any{
			"enabled":             true,
			"image":               r.AIGateway.CopilotProxyImage,
			"masterKeySecretName": aiGatewayProxySecretName,
			"tokenSecretName":     cetirunv1alpha1.CopilotTokenSecretName,
		}
	}
	values["aiGateway"] = map[string]any{
		"enabled": true, "keySecretName": aiGatewayKeySecretName, "keySecretKey": aiGatewayKeySecretField,
		"models": models, "copilotProxy": proxy,
	}
	values["networkPolicy"] = deepMerge(values["networkPolicy"], map[string]any{
		"gatewayNamespace": namespace, "gatewayPort": port,
	})
	// The key reaches the workload as an environment variable, so a new key
	// value must roll the Pod. KeyID changes exactly when the value does.
	values["podAnnotations"] = deepMerge(values["podAnnotations"], map[string]any{
		aiGatewayKeyIDPodAnnotation: instance.Status.AIGateway.KeyID,
	})
	return nil
}

// applyCopilotResponsesModels tells the Agent's Copilot proxy which of its
// registered models GitHub Copilot serves only through the Responses API, so
// the proxy bridges the chat completion requests agents send for them.
func (r *AgentReconciler) applyCopilotResponsesModels(values map[string]any, instance *cetirunv1alpha1.Agent) {
	gateway, _ := values["aiGateway"].(map[string]any)
	proxy, _ := gateway["copilotProxy"].(map[string]any)
	if proxy == nil || proxy["enabled"] != true {
		return
	}
	registered := map[string]bool{}
	if status := instance.Status.AIGateway; status != nil && status.Copilot != nil {
		for _, route := range copilotRoutes(status.Copilot) {
			registered[route.Model] = true
		}
	}
	models, _ := parseCopilotModels(instance.Annotations[cetirunv1alpha1.AnnotationCopilotModels])
	ids := []any{}
	for _, model := range models {
		if model.ResponsesOnly && registered[model.ID] {
			ids = append(ids, model.ID)
		}
	}
	slices.SortFunc(ids, func(a, b any) int { return strings.Compare(a.(string), b.(string)) })
	proxy["responsesModels"] = ids
}

// applyCopilotProxyKeyID gives the chart the ID of the proxy's master key,
// which goes on the proxy's Pod template: the proxy reads the key when it
// starts, so a replaced key restarts it.
func applyCopilotProxyKeyID(values map[string]any, instance *cetirunv1alpha1.Agent) {
	gateway, _ := values["aiGateway"].(map[string]any)
	proxy, _ := gateway["copilotProxy"].(map[string]any)
	if proxy == nil || proxy["enabled"] != true {
		return
	}
	if status := instance.Status.AIGateway; status != nil && status.Copilot != nil {
		proxy[aiGatewayChartProxyKeyValue] = status.Copilot.ProxyKeyID
	}
}

// copilotSignInGenerationPattern is the form the Portal gives sign-in
// generations; the chart accepts no other.
var copilotSignInGenerationPattern = regexp.MustCompile(`^[0-9a-f]{1,64}$`)

// applyCopilotSignInGeneration gives the chart the generation of the sign-in
// whose models the Agent uses, which goes on the proxy's Pod template. The
// proxy keeps the Copilot token it exchanged until that token expires,
// without reading the GitHub token again, so a new sign-in, possibly of
// another account, would otherwise take effect only then. The new Pod starts
// with an empty cache. A sign-in stored before generations has none, and
// changes nothing.
func applyCopilotSignInGeneration(values map[string]any, instance *cetirunv1alpha1.Agent) {
	gateway, _ := values["aiGateway"].(map[string]any)
	proxy, _ := gateway["copilotProxy"].(map[string]any)
	if proxy == nil || proxy["enabled"] != true {
		return
	}
	if generation := instance.Annotations[cetirunv1alpha1.AnnotationCopilotSignInGeneration]; copilotSignInGenerationPattern.MatchString(generation) {
		proxy[aiGatewayChartSignInValue] = generation
	}
}

// aiGatewayUsesDeploymentDefault reports whether the deployment's default
// model can decide a routed Agent's default: the Agent names no model, or
// names one that is not among its models.
func aiGatewayUsesDeploymentDefault(instance *cetirunv1alpha1.Agent) bool {
	model := aiGatewayRequestedModel(instance)
	if model == "" {
		return true
	}
	status := instance.Status.AIGateway
	return status == nil || !slices.Contains(status.Models, model)
}

// aiGatewayRevision is the part of the operator configuration hash that
// affects the Agent's model access. It covers the desired routing, so a
// configuration change triggers a full reconcile; the recorded routing, key
// and model list, so a new key or model list rolls the workload; and what the
// next reconcile would change, the Portal's Copilot model list and the shared
// models in the Gateway, so those changes are not skipped. It is empty for
// deployments that never enabled the feature, which keeps their hashes.
func (r *AgentReconciler) aiGatewayRevision(instance *cetirunv1alpha1.Agent) string {
	if !r.AIGateway.Enabled && instance.Status.AIGateway == nil {
		return ""
	}
	parts := []string{"ai-gateway", strconv.FormatBool(r.aiGatewayRequired(instance)),
		strconv.FormatBool(instance.Spec.SharedModelsEnabled()), strconv.FormatBool(r.aiGatewayCopilotWanted(instance)),
		instance.Spec.Team}
	if r.aiGatewayRouted(instance) {
		status := instance.Status.AIGateway
		parts = append(parts, "routed", r.AIGateway.URL, status.KeyID, status.DefaultModel, strings.Join(status.Models, ","),
			strconv.FormatBool(r.aiGatewayCopilotActive(instance)), r.AIGateway.CopilotProxyImage)
		// The deployment's default model selects the next default of an Agent
		// that names no model, or one it cannot use, so changing only that
		// setting must not take the lightweight path. Agents with their own
		// usable choice are left out, as configHash does for the Copilot
		// default.
		if defaultModel := strings.TrimSpace(r.AIGateway.DefaultModel); defaultModel != "" && aiGatewayUsesDeploymentDefault(instance) {
			parts = append(parts, "default-model", defaultModel)
		}
		// The proxy reads its master key when it starts, so a new key must
		// roll it.
		if r.aiGatewayCopilotActive(instance) && status.Copilot.ProxyKeyID != "" {
			parts = append(parts, "proxy-key", status.Copilot.ProxyKeyID)
		}
		// The proxy's Responses-only models follow the registered routes. A
		// Copilot model that joins a shared model leaves the Agent's model
		// list as it is, so the routes count on their own.
		if r.aiGatewayCopilotActive(instance) {
			var routes []string
			for _, route := range copilotRoutes(status.Copilot) {
				routes = append(routes, route.Model+"="+route.Name)
			}
			// copilot-names: Copilot models are named by their Gateway
			// name, not GitHub's display name; Agents configured with the
			// display names are re-rendered once.
			parts = append(parts, "copilot-routes", strings.Join(routes, ","), "copilot-names:gateway")
		}
	}
	if annotation := instance.Annotations[cetirunv1alpha1.AnnotationCopilotModels]; annotation != "" {
		sum := sha256.Sum256([]byte(annotation))
		parts = append(parts, "copilot-models", hex.EncodeToString(sum[:8]))
	}
	// Every sign-in changes this, so a repeated sign-in with an unchanged
	// model list still reaches the operator.
	if signedIn := instance.Annotations[cetirunv1alpha1.AnnotationCopilotSignedInAt]; signedIn != "" {
		parts = append(parts, "copilot-signed-in", signedIn)
	}
	if shared, known := r.aiGatewayCatalog.shared(); known && instance.Spec.SharedModelsEnabled() {
		// The limits reach the Agent's configuration too, so a change to them
		// alone must roll it.
		models := make([]string, 0, len(shared))
		for _, model := range shared {
			models = append(models, fmt.Sprintf("%s:%d:%d", model.Name, model.ContextWindow, model.MaxOutputTokens))
		}
		parts = append(parts, "shared", strings.Join(models, ","))
	}
	return strings.Join(parts, "|")
}
