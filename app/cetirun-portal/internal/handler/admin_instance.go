// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"log/slog"
	"net/http"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/agentconfig"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
)

// AdminInstanceHandler exposes a cluster-wide read-only view of
// Agent resources for admin users. Modifications still go through
// the user-scoped /api/v1/instances endpoints in InstanceHandler.
type AdminInstanceHandler struct {
	Kube *kube.Client
}

// AdminInstanceSummary is the row shape for the admin instances list.
type AdminInstanceSummary struct {
	Name          string                      `json:"name"`
	Alias         string                      `json:"alias"`
	DisplayName   string                      `json:"displayName,omitempty"`
	Email         string                      `json:"email,omitempty"`
	Team          string                      `json:"team,omitempty"`
	Agent         cetirunv1alpha1.HarnessSpec `json:"agent"`
	UID           string                      `json:"uid"`
	Phase         string                      `json:"phase,omitempty"`
	Policy        string                      `json:"policy"`
	Gateway       string                      `json:"gateway"`
	Sandbox       string                      `json:"sandbox"`
	AgentIdentity string                      `json:"agentIdentity"`
	Ready         bool                        `json:"ready"`
	CreatedAt     string                      `json:"createdAt"`
}

// ListAllInstances handles GET /api/v1/admin/instances.
func (h *AdminInstanceHandler) ListAllInstances(w http.ResponseWriter, r *http.Request) {
	items, err := h.Kube.ListAllInstances(r.Context())
	if err != nil {
		slog.Error("admin: list instances failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list instances")
		return
	}
	out := make([]AdminInstanceSummary, 0, len(items))
	for i := range items {
		ins := &items[i]
		out = append(out, AdminInstanceSummary{
			Name:          ins.Name,
			Alias:         ins.Spec.User.Alias,
			DisplayName:   ins.Spec.User.DisplayName,
			Email:         ins.Spec.User.Email,
			Team:          instanceTeam(ins),
			Agent:         agentconfig.Defaults(ins.Spec.Agent),
			UID:           string(ins.UID),
			Phase:         ins.Status.Phase,
			Policy:        instancePolicyPhase(ins),
			Gateway:       instanceGatewayPhase(ins),
			Sandbox:       instanceSandboxPhase(ins),
			AgentIdentity: instanceAgentIdentityPhase(ins),
			Ready:         isReady(ins),
			CreatedAt:     ins.CreationTimestamp.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"instances": out})
}

func instanceGatewayPhase(ins *cetirunv1alpha1.Agent) string {
	if ins.Status.AIGateway != nil {
		if ins.Status.AIGateway.ObservedGeneration != ins.Generation {
			return "Updating"
		}
		if ins.Status.AIGateway.Phase != "" {
			return ins.Status.AIGateway.Phase
		}
		return "Unknown"
	}
	if ins.Spec.ModelAccess == nil || (!ins.Spec.SharedModelsEnabled() && !ins.Spec.GitHubCopilotEnabled()) {
		return "Not requested"
	}
	return "Unknown"
}

func instanceSandboxPhase(ins *cetirunv1alpha1.Agent) string {
	if ins.Spec.SandboxRef == "" {
		return "Not requested"
	}
	for _, condition := range ins.Status.Conditions {
		if condition.Type != cetirunv1alpha1.ConditionSandboxReady {
			continue
		}
		if condition.ObservedGeneration != ins.Generation {
			return "Updating"
		}
		if condition.Status == metav1.ConditionTrue {
			return "Ready"
		}
		if condition.Reason != "" {
			return condition.Reason
		}
		return "Pending"
	}
	return "Unknown"
}

func instanceAgentIdentityPhase(ins *cetirunv1alpha1.Agent) string {
	if ins.Spec.AgentIdentity == nil || !ins.Spec.AgentIdentity.Enabled {
		return "Not requested"
	}
	if ins.Status.AgentIdentity == nil || ins.Status.AgentIdentity.Phase == "" {
		return "Unknown"
	}
	if ins.Status.AgentIdentity.ObservedGeneration != ins.Generation {
		return "Updating"
	}
	return ins.Status.AgentIdentity.Phase
}

// instancePolicyPhase reports policy resolution only. Gateway, Sandbox and
// identity execution are intentionally exposed through their own statuses.
func instancePolicyPhase(ins *cetirunv1alpha1.Agent) string {
	if ins.Status.Policy == nil || ins.Status.Policy.Phase == "" {
		return "Unknown"
	}
	if ins.Status.Policy.ObservedGeneration != ins.Generation {
		return "Updating"
	}
	if ins.Status.Policy.Phase == "Resolved" || ins.Status.Policy.Phase == "Denied" {
		return ins.Status.Policy.Phase
	}
	return "Unknown"
}

// instanceTeam returns the Team selected by the Agent.
func instanceTeam(ins *cetirunv1alpha1.Agent) string {
	return ins.Spec.Team
}

func isReady(ins *cetirunv1alpha1.Agent) bool {
	for _, c := range ins.Status.Conditions {
		if c.Type == "Ready" {
			return c.Status == "True"
		}
	}
	return false
}
