// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
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
	// administrators share with every Agent. Administrators assign a model to
	// it with model_info.access_groups.
	aiGatewaySharedAccessGroup = "cetirun-shared"
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
)

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
	// DefaultBudget is the monthly budget in US dollars written when a key is
	// created. Later changes made in the Gateway are not overwritten.
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
		if err != nil || budget <= 0 {
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
	UpdateKeyOwner(context.Context, string, string, string) error
	DeleteKeys(context.Context, []string) error
	DeleteKeyByAlias(context.Context, string) error
	EnsureTeam(context.Context, string, string, map[string]string) error
	EnsureUser(context.Context, string, map[string]string) error
	EnsureTeamMember(context.Context, string, string) error
	ListModels(context.Context) ([]aigateway.Deployment, error)
	AddModel(context.Context, aigateway.ModelRequest) error
	DeleteModel(context.Context, string) error
	PricedModels(context.Context) (map[string]bool, error)
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
// the Gateway: its configuration takes the Gateway endpoint, the key and the
// model list, and it no longer carries its own Copilot login. Hermes is not
// yet, so Hermes Agents get a key but keep their current provider.
func aiGatewayHarnessSupported(agentType cetirunv1alpha1.AgentType) bool {
	return agentType == cetirunv1alpha1.AgentTypeOpenClaw
}

// aiGatewayChartValue and aiGatewayChartModelsValue are the agent chart
// values that wire the key, the model list and the Copilot proxy. Chart
// versions without them would ignore or reject routing values, so Agents
// pinned to them are not routed.
const (
	aiGatewayChartValue        = "aiGateway"
	aiGatewayChartModelsValue  = "models"
	aiGatewayChartCopilotValue = "copilotProxy"
)

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
	return r.AIGateway.Enabled && aiGatewayHarnessSupported(instance.Spec.Agent.Type)
}

// aiGatewayCopilotWanted reports whether the Agent gets a GitHub Copilot
// proxy once routed: the owner kept the switch on and the deployment
// configured the proxy image.
func (r *AgentReconciler) aiGatewayCopilotWanted(instance *cetirunv1alpha1.Agent) bool {
	return r.aiGatewayRequired(instance) && r.AIGateway.CopilotProxyImage != "" && instance.Spec.GitHubCopilotEnabled()
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

func ensureAIGatewayStatus(instance *cetirunv1alpha1.Agent) *cetirunv1alpha1.AIGatewayStatus {
	if instance.Status.AIGateway == nil {
		instance.Status.AIGateway = &cetirunv1alpha1.AIGatewayStatus{}
	}
	return instance.Status.AIGateway
}

func aiGatewayDegraded(instance *cetirunv1alpha1.Agent, reason string, err error) error {
	status := ensureAIGatewayStatus(instance)
	status.Phase = cetirunv1alpha1.AIGatewayPhaseDegraded
	status.Reason = reason
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
// Agent in the Team. It returns the team and user IDs for the key.
func (r *AgentReconciler) ensureAIGatewayOwner(ctx context.Context, client aiGatewayKeyClient, instance *cetirunv1alpha1.Agent) (string, string, error) {
	now := time.Now()
	teamID := aiGatewayTeamID(instance.Spec.Team)
	userID := instance.Spec.User.Alias
	if teamID != "" && !r.aiGatewayOwners.fresh("team:"+teamID, now) {
		if err := client.EnsureTeam(ctx, teamID, instance.Spec.Team, map[string]string{"cetirun_team": instance.Spec.Team}); err != nil {
			return "", "", fmt.Errorf("ensure Gateway team %s: %w", teamID, err)
		}
		r.aiGatewayOwners.mark("team:"+teamID, now)
	}
	if userID != "" && !r.aiGatewayOwners.fresh("user:"+userID, now) {
		if err := client.EnsureUser(ctx, userID, map[string]string{"cetirun_user": userID}); err != nil {
			return "", "", fmt.Errorf("ensure Gateway user %s: %w", userID, err)
		}
		r.aiGatewayOwners.mark("user:"+userID, now)
	}
	if teamID != "" && userID != "" && !r.aiGatewayOwners.fresh("member:"+teamID+"/"+userID, now) {
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
// budget is written only at creation: administrators may change it in the
// Gateway. chartSupported reports whether the Agent's pinned chart can route
// model traffic through the Gateway.
func (r *AgentReconciler) reconcileAIGateway(ctx context.Context, instance *cetirunv1alpha1.Agent, chartSupported bool) error {
	status := ensureAIGatewayStatus(instance)
	status.KeyAlias = aiGatewayKeyAlias(instance)
	status.KeySecretName = aiGatewayKeySecretName
	status.ObservedGeneration = instance.Generation
	if status.Phase == "" {
		status.Phase = cetirunv1alpha1.AIGatewayPhaseProvisioning
	}
	routed := r.aiGatewayRequired(instance) && chartSupported
	copilot := routed && r.aiGatewayCopilotWanted(instance)
	groups := r.aiGatewayAccessGroups(instance, copilot)
	models := aiGatewayKeyModels(groups)

	client, err := r.aiGatewayClient(ctx)
	if err != nil {
		return aiGatewayDegraded(instance, "GatewayConfigUnavailable", err)
	}
	teamID, userID, err := r.ensureAIGatewayOwner(ctx, client, instance)
	if err != nil {
		return aiGatewayDegraded(instance, "GatewayOwnerFailed", err)
	}
	value, err := r.ensureAIGatewayKeySecret(ctx, instance, false)
	if err != nil {
		return aiGatewayDegraded(instance, "KeySecretFailed", err)
	}
	token := aigateway.Token(value)
	key, err := client.GetKey(ctx, token)
	if err != nil {
		return aiGatewayDegraded(instance, "GatewayUnavailable", err)
	}
	switch {
	case key == nil:
		request := aigateway.GenerateRequest{
			Key: value, Alias: status.KeyAlias, Models: models,
			MaxBudget: r.AIGateway.DefaultBudget, BudgetDuration: aiGatewayBudgetDuration,
			TeamID: teamID, UserID: userID,
			Metadata: map[string]string{
				"cetirun_agent": instance.Name, "cetirun_agent_uid": string(instance.UID),
				"cetirun_user": instance.Spec.User.Alias, "cetirun_team": instance.Spec.Team,
			},
		}
		err := client.GenerateKey(ctx, request)
		if errors.Is(err, aigateway.ErrAliasExists) {
			// The alias embeds this Agent's UID, so the existing key is this
			// Agent's, with a value no longer held here (for example, the
			// Secret was deleted). Revoke it and create one for this value.
			if err = client.DeleteKeyByAlias(ctx, status.KeyAlias); err == nil {
				err = client.GenerateKey(ctx, request)
			}
		}
		if err != nil {
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
	}
	now := metav1.Now()
	status.KeyID = token[:aiGatewayKeyIDLength]
	status.AccessGroups = groups
	status.Team = teamID
	status.Phase = cetirunv1alpha1.AIGatewayPhaseReady
	status.Reason = ""
	status.LastVerifiedTime = &now
	status.Routed = routed

	// Copilot registration problems are reported on the Copilot status and
	// never block the key: shared models keep working.
	r.reconcileAIGatewayCopilot(ctx, client, instance, copilot)
	r.refreshAIGatewayCatalog(ctx, client)
	if routed {
		entries, defaultModel := r.aiGatewayModelEntries(instance)
		status.Models = make([]string, 0, len(entries))
		for _, entry := range entries {
			status.Models = append(status.Models, entry.ID)
		}
		status.DefaultModel = defaultModel
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

func sortedCopy(values []string) []string {
	out := slices.Clone(values)
	slices.Sort(out)
	return out
}

// ensureAIGatewayKeySecret returns the Agent's key value, creating the Secret
// when needed. A Secret written for another Agent UID, or holding a value in
// an unexpected form, gets a new value; replace forces a new value.
func (r *AgentReconciler) ensureAIGatewayKeySecret(ctx context.Context, instance *cetirunv1alpha1.Agent, replace bool) (string, error) {
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
}

// deleteAIGatewayKey revokes the Agent's key and removes its Copilot model
// registrations before its workload and namespace are removed. Revocation is
// by the UID-derived alias and, when the Secret is still readable, by the key
// itself, so a key is removed even if an administrator renamed it. Deletion
// is blocked while revocation fails; when the Gateway is not configured at
// all there is nothing to call, which is reported as a warning event.
func (r *AgentReconciler) deleteAIGatewayKey(ctx context.Context, instance *cetirunv1alpha1.Agent) error {
	if instance.Status.AIGateway == nil && !r.AIGateway.Enabled {
		return nil
	}
	if !r.AIGateway.configured() {
		r.EventRecorder.Event(instance, corev1.EventTypeWarning, "AIGatewayKeyNotRevoked",
			"The AI Gateway is not configured, so the Agent's virtual key could not be revoked")
		return nil
	}
	status := ensureAIGatewayStatus(instance)
	if status.Phase != cetirunv1alpha1.AIGatewayPhaseDeleting {
		status.Phase = cetirunv1alpha1.AIGatewayPhaseDeleting
		status.Routed = false
		if err := r.updateStatus(ctx, instance); err != nil {
			return fmt.Errorf("persist AI Gateway deletion gate: %w", err)
		}
	}
	client, err := r.aiGatewayClient(ctx)
	if err != nil {
		return err
	}
	if err := r.deleteCopilotRegistrations(ctx, client, instance); err != nil {
		return fmt.Errorf("remove Copilot model registrations: %w", err)
	}
	secret := &corev1.Secret{}
	err = r.Get(ctx, types.NamespacedName{Namespace: instanceNamespace(instance.Name), Name: aiGatewayKeySecretName}, secret)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("get AI Gateway key Secret: %w", err)
	}
	if err == nil && secret.Labels[aiGatewayAgentUIDLabel] == string(instance.UID) {
		if value := string(secret.Data[aiGatewayKeySecretField]); aigateway.ValidKeyValue(value) {
			if err := client.DeleteKeys(ctx, []string{aigateway.Token(value)}); err != nil {
				return fmt.Errorf("revoke AI Gateway virtual key: %w", err)
			}
		}
	}
	if err := client.DeleteKeyByAlias(ctx, aiGatewayKeyAlias(instance)); err != nil {
		return fmt.Errorf("revoke AI Gateway virtual key by alias: %w", err)
	}
	r.EventRecorder.Eventf(instance, corev1.EventTypeNormal, "AIGatewayKeyRevoked",
		"Revoked AI Gateway virtual key %s", aiGatewayKeyAlias(instance))
	return nil
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
	entries, defaultModel := r.aiGatewayModelEntries(instance)
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
		"model":    defaultModel,
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
	}
	if annotation := instance.Annotations[cetirunv1alpha1.AnnotationCopilotModels]; annotation != "" {
		sum := sha256.Sum256([]byte(annotation))
		parts = append(parts, "copilot-models", hex.EncodeToString(sum[:8]))
	}
	if shared, known := r.aiGatewayCatalog.sharedNames(); known && instance.Spec.SharedModelsEnabled() {
		parts = append(parts, "shared", strings.Join(shared, ","))
	}
	return strings.Join(parts, "|")
}
