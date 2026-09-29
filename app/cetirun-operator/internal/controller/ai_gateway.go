// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
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
	// aiGatewaySharedAccessGroup is the Gateway access group for models shared
	// with the Teams listed in the operator configuration. Administrators
	// assign shared models to it with model_info.access_groups.
	aiGatewaySharedAccessGroup    = "cetirun-shared"
	aiGatewayBudgetDuration       = "1mo"
	aiGatewayVerificationInterval = 10 * time.Minute
	aiGatewayKeyIDLength          = 12
	aiGatewayAgentUIDLabel        = "cetirun.microsoft.com/agent-uid"
	aiGatewayKeyAliasAnnotation   = "cetirun.microsoft.com/ai-gateway-key-alias"
	aiGatewayKeyIDPodAnnotation   = "cetirun.microsoft.com/ai-gateway-key-id"
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
	// DefaultModel is the model configured on Agents that use the Gateway.
	DefaultModel string
	// SharedModelTeams lists the Teams whose Agents may use shared models.
	SharedModelTeams []string
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
	}
	if cfg.MasterKeySecretKey == "" {
		cfg.MasterKeySecretKey = "master-key"
	}
	for _, team := range strings.Split(getenv("AI_GATEWAY_SHARED_MODEL_TEAMS"), ",") {
		if team = strings.TrimSpace(team); team != "" && !slices.Contains(cfg.SharedModelTeams, team) {
			cfg.SharedModelTeams = append(cfg.SharedModelTeams, team)
		}
	}
	if raw := strings.TrimSpace(getenv("AI_GATEWAY_DEFAULT_BUDGET")); raw != "" {
		budget, err := strconv.ParseFloat(raw, 64)
		if err != nil || budget <= 0 {
			return AIGatewayConfig{}, fmt.Errorf("AI_GATEWAY_DEFAULT_BUDGET must be a positive number of US dollars")
		}
		cfg.DefaultBudget = budget
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
	DeleteKeys(context.Context, []string) error
	DeleteKeyByAlias(context.Context, string) error
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

func (r *AgentReconciler) aiGatewayAccessGroups(team *cetirunv1alpha1.Team) []string {
	if team != nil && slices.Contains(r.AIGateway.SharedModelTeams, team.Name) {
		return []string{aiGatewaySharedAccessGroup}
	}
	return nil
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

// aiGatewayHarnessSupported reports whether the built-in harness honours a
// custom OpenAI-compatible base URL. Hermes does not yet (its native config
// ignores secrets.apiBase), so Hermes Agents get a key but keep their
// current provider.
func aiGatewayHarnessSupported(agentType cetirunv1alpha1.AgentType) bool {
	return agentType == cetirunv1alpha1.AgentTypeOpenClaw
}

// aiGatewayChartValue is the agent chart value that wires the key and the
// Gateway egress. Chart versions without it would ignore routing values, so
// Agents pinned to them are not routed.
const aiGatewayChartValue = "aiGateway"

// aiGatewayRequired reports whether the deployment wants this Agent's model
// traffic on the Gateway: the feature is on, a model is configured, the
// harness supports a custom endpoint, and the Agent's Team may use shared
// models. Chart support and a provisioned key are checked separately.
func (r *AgentReconciler) aiGatewayRequired(instance *cetirunv1alpha1.Agent, team *cetirunv1alpha1.Team) bool {
	return r.AIGateway.Enabled && r.AIGateway.DefaultModel != "" &&
		aiGatewayHarnessSupported(instance.Spec.Agent.Type) && len(r.aiGatewayAccessGroups(team)) > 0
}

// aiGatewayRouted reports the routing decision recorded by the last
// successful key reconcile. It depends on a provisioned key rather than on
// current Gateway health, so a Gateway outage does not flip the provider.
func (r *AgentReconciler) aiGatewayRouted(instance *cetirunv1alpha1.Agent) bool {
	status := instance.Status.AIGateway
	return r.AIGateway.Enabled && status != nil && status.Routed && status.KeyID != "" && status.KeySecretName != ""
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

// reconcileAIGateway makes sure the Agent's virtual key exists in the Gateway
// with the desired access groups. The key value is written to the instance
// Secret before the create request, so an unknown create outcome is resolved
// by looking the value up instead of creating a second key. The budget is
// written only at creation: administrators may change it in the Gateway.
// chartSupported reports whether the Agent's pinned chart can route model
// traffic through the Gateway.
func (r *AgentReconciler) reconcileAIGateway(ctx context.Context, instance *cetirunv1alpha1.Agent, team *cetirunv1alpha1.Team, chartSupported bool) error {
	status := ensureAIGatewayStatus(instance)
	status.KeyAlias = aiGatewayKeyAlias(instance)
	status.KeySecretName = aiGatewayKeySecretName
	status.ObservedGeneration = instance.Generation
	if status.Phase == "" {
		status.Phase = cetirunv1alpha1.AIGatewayPhaseProvisioning
	}
	groups := r.aiGatewayAccessGroups(team)
	models := aiGatewayKeyModels(groups)

	client, err := r.aiGatewayClient(ctx)
	if err != nil {
		return aiGatewayDegraded(instance, "GatewayConfigUnavailable", err)
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
	case !slices.Equal(sortedCopy(key.Models), models):
		if err := client.UpdateKeyModels(ctx, token, models); err != nil {
			return aiGatewayDegraded(instance, "KeyUpdateFailed", err)
		}
		r.EventRecorder.Eventf(instance, corev1.EventTypeNormal, "AIGatewayKeyUpdated",
			"Updated AI Gateway virtual key %s access to %s", status.KeyAlias, strings.Join(models, ","))
	}
	now := metav1.Now()
	status.KeyID = token[:aiGatewayKeyIDLength]
	status.AccessGroups = groups
	status.Phase = cetirunv1alpha1.AIGatewayPhaseReady
	status.Reason = ""
	status.LastVerifiedTime = &now
	status.Routed = r.aiGatewayRequired(instance, team) && chartSupported
	message := "AI Gateway virtual key is ready"
	if r.aiGatewayRequired(instance, team) && !chartSupported {
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
	key := types.NamespacedName{Namespace: instanceNamespace(instance.Name), Name: aiGatewayKeySecretName}
	secret := &corev1.Secret{}
	err := r.Get(ctx, key, secret)
	if err != nil && !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("get AI Gateway key Secret: %w", err)
	}
	exists := err == nil
	if exists && !replace {
		value := string(secret.Data[aiGatewayKeySecretField])
		if secret.Labels[aiGatewayAgentUIDLabel] == string(instance.UID) && aigateway.ValidKeyValue(value) {
			return value, nil
		}
	}
	value, err := aigateway.NewKeyValue()
	if err != nil {
		return "", err
	}
	if secret.Labels == nil {
		secret.Labels = map[string]string{}
	}
	secret.Labels["app.kubernetes.io/managed-by"] = "cetirun-operator"
	secret.Labels["app.kubernetes.io/instance"] = instance.Spec.User.Alias
	secret.Labels[aiGatewayAgentUIDLabel] = string(instance.UID)
	if secret.Annotations == nil {
		secret.Annotations = map[string]string{}
	}
	secret.Annotations[aiGatewayKeyAliasAnnotation] = aiGatewayKeyAlias(instance)
	secret.Type = corev1.SecretTypeOpaque
	secret.Data = map[string][]byte{aiGatewayKeySecretField: []byte(value)}
	if exists {
		if err := r.Update(ctx, secret); err != nil {
			return "", fmt.Errorf("replace AI Gateway key Secret: %w", err)
		}
		return value, nil
	}
	secret.Name, secret.Namespace = key.Name, key.Namespace
	if err := r.Create(ctx, secret); err != nil {
		return "", fmt.Errorf("create AI Gateway key Secret: %w", err)
	}
	return value, nil
}

// deleteAIGatewayKey revokes the Agent's key before its workload and
// namespace are removed. Revocation is by the UID-derived alias and, when the
// Secret is still readable, by the key itself, so a key is removed even if an
// administrator renamed it. Deletion is blocked while revocation fails; when
// the Gateway is not configured at all there is nothing to call, which is
// reported as a warning event.
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
// another provider, endpoint, or key.
func (r *AgentReconciler) applyAIGatewayValues(values map[string]any, instance *cetirunv1alpha1.Agent) error {
	values["aiGateway"] = map[string]any{"enabled": false, "keySecretName": "", "keySecretKey": aiGatewayKeySecretField}
	if !r.aiGatewayRouted(instance) {
		return nil
	}
	namespace, port, err := aiGatewayEndpoint(r.AIGateway.URL)
	if err != nil {
		return err
	}
	values["secrets"] = deepMerge(values["secrets"], map[string]any{
		"provider": "openai",
		"authMode": "api_key",
		"apiBase":  r.AIGateway.URL + "/v1",
		"model":    r.AIGateway.DefaultModel,
	})
	values["aiGateway"] = map[string]any{
		"enabled": true, "keySecretName": aiGatewayKeySecretName, "keySecretKey": aiGatewayKeySecretField,
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
// configuration change triggers a full reconcile, and the recorded routing
// and key, so a new key rolls the workload. It is empty for deployments that
// never enabled the feature, which keeps their existing hashes unchanged.
func (r *AgentReconciler) aiGatewayRevision(instance *cetirunv1alpha1.Agent, team *cetirunv1alpha1.Team) string {
	if !r.AIGateway.Enabled && instance.Status.AIGateway == nil {
		return ""
	}
	parts := []string{"ai-gateway", strconv.FormatBool(r.aiGatewayRequired(instance, team))}
	if r.aiGatewayRouted(instance) {
		parts = append(parts, "routed", r.AIGateway.URL, r.AIGateway.DefaultModel, instance.Status.AIGateway.KeyID)
	}
	return strings.Join(parts, "|")
}
