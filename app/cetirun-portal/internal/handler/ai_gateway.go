// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
)

// ModelAccessRequest selects the model sources of a new OpenClaw or Hermes
// Agent when the platform AI Gateway is enabled. An omitted switch counts as
// on.
type ModelAccessRequest struct {
	SharedModels  *bool `json:"sharedModels,omitempty"`
	GitHubCopilot *bool `json:"githubCopilot,omitempty"`
}

// AIGatewayResponse describes an Agent's models in the AI Gateway. It holds
// names and states only, never keys or addresses.
type AIGatewayResponse struct {
	// Routed reports whether the Agent's model traffic uses the Gateway.
	Routed bool `json:"routed"`
	// Phase and Reason report the Agent's virtual key.
	Phase  string `json:"phase,omitempty"`
	Reason string `json:"reason,omitempty"`
	// SharedModels and GitHubCopilot are the owner's effective switches.
	SharedModels  bool `json:"sharedModels"`
	GitHubCopilot bool `json:"githubCopilot"`
	// Models are the models the Agent is configured with, default first
	// when it has one.
	Models       []string                  `json:"models"`
	DefaultModel string                    `json:"defaultModel,omitempty"`
	Copilot      *AIGatewayCopilotResponse `json:"copilot,omitempty"`
}

// AIGatewayCopilotResponse describes the Agent's GitHub Copilot proxy.
type AIGatewayCopilotResponse struct {
	// Phase is SignInRequired, Starting, Ready or Degraded.
	Phase  string `json:"phase,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Models are the registered Copilot models, as the Agent names them.
	Models []string `json:"models"`
	// Unpriced are the account's models left out for lack of a price.
	Unpriced []string `json:"unpriced,omitempty"`
}

// AIGatewayCatalogResponse is the JSON response for GET /api/v1/ai-gateway/catalog.
type AIGatewayCatalogResponse struct {
	// Known is false until the operator has published the catalog.
	Known        bool                                    `json:"known"`
	SharedModels []cetirunv1alpha1.AIGatewayCatalogModel `json:"sharedModels"`
	// GitHubCopilot reports whether the deployment offers GitHub Copilot to
	// Agents.
	GitHubCopilot bool `json:"githubCopilot"`
	// AgentTypes are the agent types new Agents can take the Gateway's models
	// with: those the operator's default agent chart routes through it. They
	// are null while the operator has not read that chart.
	AgentTypes []cetirunv1alpha1.AgentType `json:"agentTypes"`
}

// agentTypeNames are the agent types the Gateway can route, as the Portal
// names them.
var agentTypeNames = map[cetirunv1alpha1.AgentType]string{
	cetirunv1alpha1.AgentTypeOpenClaw: "OpenClaw",
	cetirunv1alpha1.AgentTypeHermes:   "Hermes",
}

func isAIGatewayPlaceholder(model string) bool {
	return model == cetirunv1alpha1.AIGatewayNoModelsModel || model == cetirunv1alpha1.AIGatewaySignInRequiredModel
}

// aiGatewayResponse summarises the Agent's Gateway status, or returns nil
// when the Agent has none.
func aiGatewayResponse(instance *cetirunv1alpha1.Agent) *AIGatewayResponse {
	status := instance.Status.AIGateway
	if status == nil {
		return nil
	}
	resp := &AIGatewayResponse{
		Routed:        status.Routed,
		Phase:         status.Phase,
		Reason:        status.Reason,
		SharedModels:  instance.Spec.SharedModelsEnabled(),
		GitHubCopilot: instance.Spec.GitHubCopilotEnabled(),
		Models:        []string{},
	}
	if !isAIGatewayPlaceholder(status.DefaultModel) {
		resp.DefaultModel = status.DefaultModel
	}
	for _, model := range status.Models {
		if !isAIGatewayPlaceholder(model) {
			resp.Models = append(resp.Models, model)
		}
	}
	if copilot := status.Copilot; copilot != nil {
		resp.Copilot = &AIGatewayCopilotResponse{
			Phase:    copilot.Phase,
			Reason:   copilot.Reason,
			Models:   append([]string{}, copilot.Models...),
			Unpriced: append([]string(nil), copilot.Unpriced...),
		}
	}
	return resp
}

// sharedModelCatalog reads the operator's catalog; an unreadable catalog
// counts as unknown.
func (h *InstanceHandler) sharedModelCatalog(ctx context.Context) AIGatewayCatalogResponse {
	catalog, known, err := h.Kube.GetAIGatewayCatalog(ctx, h.Config.AIGatewayCatalogNamespace)
	if err != nil {
		slog.Warn("Failed to read the AI Gateway catalog", "error", err)
		return AIGatewayCatalogResponse{SharedModels: []cetirunv1alpha1.AIGatewayCatalogModel{}, AgentTypes: []cetirunv1alpha1.AgentType{}}
	}
	var agentTypes []cetirunv1alpha1.AgentType
	switch {
	case !known:
		agentTypes = []cetirunv1alpha1.AgentType{}
	case catalog.Routing == nil:
		// A catalog from an earlier operator, which routes only OpenClaw.
		agentTypes = []cetirunv1alpha1.AgentType{cetirunv1alpha1.AgentTypeOpenClaw}
	default:
		// Null while the operator has not read its default agent chart.
		agentTypes = catalog.Routing.AgentTypes
	}
	return AIGatewayCatalogResponse{Known: known, SharedModels: catalog.SharedModels, GitHubCopilot: catalog.GitHubCopilot, AgentTypes: agentTypes}
}

// GetAIGatewayCatalog handles GET /api/v1/ai-gateway/catalog: the shared
// models new Agents can use, by name and limits.
func (h *InstanceHandler) GetAIGatewayCatalog(w http.ResponseWriter, r *http.Request) {
	if !h.Config.AIGatewayEnabled {
		writeError(w, http.StatusNotFound, "feature_disabled", "The AI Gateway is not enabled for this deployment")
		return
	}
	writeJSON(w, http.StatusOK, h.sharedModelCatalog(r.Context()))
}

// validateAIGatewayCreate applies the Team and AI Gateway rules to a new Agent:
// it must name an existing Team with a default Sandbox, and an OpenClaw or Hermes Agent must be of a
// type the operator's agent chart routes through the Gateway, once the
// operator has read that chart, and have a model to use, from shared models
// it keeps on while the catalog lists at least one, or from GitHub Copilot it
// keeps on while the deployment offers it. Omitted switches select the sources
// currently offered by the deployment. The normalized, explicit model access
// is always returned for a Gateway-routed built-in Agent so the Portal and
// Agent API do not assign different meanings to an omitted modelAccess.
func (h *InstanceHandler) validateAIGatewayCreate(w http.ResponseWriter, r *http.Request, req CreateInstanceRequest, agentType cetirunv1alpha1.AgentType) (string, string, *cetirunv1alpha1.ModelAccessSpec, bool) {
	team, sandboxRef, ok := h.resolveTeamBinding(w, r, req.Team)
	if !ok {
		return "", "", nil, false
	}
	if !h.Config.AIGatewayEnabled {
		return team, sandboxRef, nil, true
	}
	if agentType != cetirunv1alpha1.AgentTypeOpenClaw && agentType != cetirunv1alpha1.AgentTypeHermes {
		// Custom Agents bring their own models.
		return team, sandboxRef, nil, true
	}
	catalog := h.sharedModelCatalog(r.Context())
	if !catalog.Known {
		writeError(w, http.StatusServiceUnavailable, "model_sources_unknown", "The AI Gateway has not published its models yet. Try again in a minute.")
		return "", "", nil, false
	}
	if catalog.AgentTypes == nil {
		// The operator has not read its default agent chart, which the new
		// Agent would be pinned to.
		writeError(w, http.StatusServiceUnavailable, "agent_types_unknown",
			"The operator has not read its agent chart yet. Try again in a minute.")
		return "", "", nil, false
	}
	if !slices.Contains(catalog.AgentTypes, agentType) {
		// The operator would leave the Agent off the Gateway and hand the
		// Gateway's model names to the agent's own provider.
		writeError(w, http.StatusConflict, "agent_chart_unsupported",
			"This deployment's agent chart does not route "+agentTypeNames[agentType]+
				" agents through the AI Gateway. Ask an administrator to upgrade the agent chart.")
		return "", "", nil, false
	}
	sharedAvailable := len(catalog.SharedModels) > 0
	shared, copilot := sharedAvailable, catalog.GitHubCopilot
	if req.ModelAccess != nil {
		if req.ModelAccess.SharedModels != nil {
			shared = *req.ModelAccess.SharedModels
		}
		if req.ModelAccess.GitHubCopilot != nil {
			copilot = *req.ModelAccess.GitHubCopilot
		}
	}
	if shared && !sharedAvailable {
		writeError(w, http.StatusBadRequest, "invalid_model_access", "Shared models were requested, but this deployment currently offers none")
		return "", "", nil, false
	}
	if copilot && !catalog.GitHubCopilot {
		writeError(w, http.StatusBadRequest, "invalid_model_access", "GitHub Copilot was requested, but this deployment does not offer it")
		return "", "", nil, false
	}
	if !shared && !copilot {
		if !sharedAvailable && !catalog.GitHubCopilot {
			writeError(w, http.StatusConflict, "no_model_source",
				"No models are available: there are no shared models and GitHub Copilot is turned off. Ask an administrator to add a shared model.")
		} else {
			writeError(w, http.StatusBadRequest, "invalid_model_access", "Keep shared models or GitHub Copilot on")
		}
		return "", "", nil, false
	}
	return team, sandboxRef, &cetirunv1alpha1.ModelAccessSpec{SharedModels: &shared, GitHubCopilot: &copilot}, true
}

func (h *InstanceHandler) resolveTeamBinding(w http.ResponseWriter, r *http.Request, requestedTeam string) (string, string, bool) {
	team := strings.TrimSpace(requestedTeam)
	if team == "" {
		if !h.Config.AIGatewayEnabled {
			return "", "", true
		}
		writeError(w, http.StatusBadRequest, "team_required", "Choose a Team for the agent")
		return "", "", false
	}
	selectedTeam, err := h.Kube.GetTeam(r.Context(), team)
	if err != nil {
		if kube.IsNotFound(err) {
			writeError(w, http.StatusBadRequest, "unknown_team", "Team '"+team+"' does not exist")
			return "", "", false
		}
		slog.Error("Failed to read Team", "team", team, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to check the Team")
		return "", "", false
	}
	if !selectedTeam.Spec.PolicyConfigured() {
		return team, "", true
	}
	sandboxRef := strings.TrimSpace(selectedTeam.Spec.DefaultSandboxRef)
	if sandboxRef == "" || selectedTeam.Spec.AgentPolicyRef == "" ||
		!slices.Contains(selectedTeam.Spec.SandboxRefs, sandboxRef) {
		writeError(w, http.StatusConflict, "team_not_configured", "The selected Team has incomplete policy or Sandbox configuration")
		return "", "", false
	}
	return team, sandboxRef, true
}

// aiGatewayRouted reports whether the Agent's model traffic uses the Gateway,
// which replaces the in-agent GitHub Copilot sign-in helper.
func aiGatewayRouted(instance *cetirunv1alpha1.Agent) bool {
	return instance.Status.AIGateway != nil && instance.Status.AIGateway.Routed
}

// servesSignInHelper reports whether the Agent's workload serves the in-agent
// cetirun-auth sign-in helper, as the operator records it. OpenClaw charts
// with the per-Agent Copilot proxy carry none. Before the operator recorded
// it, the helper ran wherever the AI Gateway did not replace it.
func servesSignInHelper(instance *cetirunv1alpha1.Agent) bool {
	if instance.Status.SignInHelper != nil {
		return *instance.Status.SignInHelper
	}
	return !aiGatewayRouted(instance)
}
