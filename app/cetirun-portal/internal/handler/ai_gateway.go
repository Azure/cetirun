// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
)

// ModelAccessRequest selects the model sources of a new OpenClaw Agent when
// the platform AI Gateway is enabled. An omitted switch counts as on.
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
		return AIGatewayCatalogResponse{SharedModels: []cetirunv1alpha1.AIGatewayCatalogModel{}}
	}
	return AIGatewayCatalogResponse{Known: known, SharedModels: catalog.SharedModels, GitHubCopilot: catalog.GitHubCopilot}
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

// validateAIGatewayCreate validates only the user-requested model sources.
// Omission is intentionally no request: the Operator intersects explicit
// requests with the resolved AgentPolicy, so an empty list must not turn into
// broad model access.
func (h *InstanceHandler) validateAIGatewayCreate(w http.ResponseWriter, r *http.Request, req CreateInstanceRequest, agentType cetirunv1alpha1.AgentType) (string, *cetirunv1alpha1.ModelAccessSpec, bool) {
	team := strings.TrimSpace(req.Team)
	if !h.Config.AIGatewayEnabled {
		return req.Team, nil, true
	}
	if team == "" {
		writeError(w, http.StatusBadRequest, "team_required", "Choose a Team for the agent")
		return "", nil, false
	}
	if _, err := h.Kube.GetTeam(r.Context(), team); err != nil {
		if kube.IsNotFound(err) {
			writeError(w, http.StatusBadRequest, "unknown_team", "Team '"+team+"' does not exist")
			return "", nil, false
		}
		slog.Error("Failed to read Team", "team", team, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to check the Team")
		return "", nil, false
	}
	if agentType == cetirunv1alpha1.AgentTypeCustom {
		return team, nil, true
	}
	if req.ModelAccess == nil {
		return team, nil, true
	}
	return team, &cetirunv1alpha1.ModelAccessSpec{SharedModels: req.ModelAccess.SharedModels, GitHubCopilot: req.ModelAccess.GitHubCopilot}, true
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
