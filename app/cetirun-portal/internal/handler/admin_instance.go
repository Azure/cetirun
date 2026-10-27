// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"log/slog"
	"net/http"

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
	Name              string                      `json:"name"`
	Alias             string                      `json:"alias"`
	DisplayName       string                      `json:"displayName,omitempty"`
	Email             string                      `json:"email,omitempty"`
	Team              string                      `json:"team,omitempty"`
	Agent             cetirunv1alpha1.HarnessSpec `json:"agent"`
	UID               string                      `json:"uid"`
	Phase             string                      `json:"phase,omitempty"`
	Ready             bool                        `json:"ready"`
	TeamChangePending bool                        `json:"teamChangePending"`
	CreatedAt         string                      `json:"createdAt"`
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
			Name:              ins.Name,
			Alias:             ins.Spec.User.Alias,
			DisplayName:       ins.Spec.User.DisplayName,
			Email:             ins.Spec.User.Email,
			Team:              instanceTeam(ins),
			Agent:             agentconfig.Defaults(ins.Spec.Agent),
			UID:               string(ins.UID),
			Phase:             ins.Status.Phase,
			Ready:             isReady(ins),
			TeamChangePending: ins.TeamChangePending(),
			CreatedAt:         ins.CreationTimestamp.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"instances": out})
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
