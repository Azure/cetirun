// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"errors"
	"log/slog"
	"net/http"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/agentconfig"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
)

// AdminInstanceHandler exposes cluster-wide Agent lifecycle administration.
type AdminInstanceHandler struct {
	Kube *kube.Client
}

// AdminInstanceSummary is the row shape for the admin instances list.
type AdminInstanceSummary struct {
	Name            string                      `json:"name"`
	Alias           string                      `json:"alias"`
	DisplayName     string                      `json:"displayName,omitempty"`
	Email           string                      `json:"email,omitempty"`
	Team            string                      `json:"team,omitempty"`
	Agent           cetirunv1alpha1.HarnessSpec `json:"agent"`
	UID             string                      `json:"uid"`
	ResourceVersion string                      `json:"resourceVersion"`
	Suspended       bool                        `json:"suspended"`
	Phase           string                      `json:"phase,omitempty"`
	Ready           bool                        `json:"ready"`
	CreatedAt       string                      `json:"createdAt"`
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
			Name:            ins.Name,
			Alias:           ins.Spec.User.Alias,
			DisplayName:     ins.Spec.User.DisplayName,
			Email:           ins.Spec.User.Email,
			Team:            instanceTeam(ins),
			Agent:           agentconfig.Defaults(ins.Spec.Agent),
			UID:             string(ins.UID),
			ResourceVersion: ins.ResourceVersion,
			Suspended:       ins.Spec.Suspended,
			Phase:           ins.Status.Phase,
			Ready:           isReady(ins),
			CreatedAt:       ins.CreationTimestamp.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"instances": out})
}

// UpdateLifecycle handles PATCH /api/v1/admin/instances/{name}. Lifecycle is
// deliberately separate from policy assignment: suspension is an immediate
// administrator decision and does not create an approval resource.
func (h *AdminInstanceHandler) UpdateLifecycle(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !teamNamePattern.MatchString(name) {
		writeError(w, http.StatusBadRequest, "invalid_name", "Invalid Agent name")
		return
	}
	var req struct {
		UID             string `json:"uid"`
		ResourceVersion string `json:"resourceVersion"`
		Suspended       *bool  `json:"suspended"`
	}
	if !decodeStrictAdmin(w, r, &req) {
		return
	}
	if req.UID == "" || req.ResourceVersion == "" {
		writeError(w, http.StatusPreconditionRequired, "version_required", "uid and resourceVersion from the current Agent are required")
		return
	}
	if req.Suspended == nil {
		writeError(w, http.StatusBadRequest, "suspended_required", "suspended is required")
		return
	}
	item, err := h.Kube.UpdateInstanceLifecycle(r.Context(), name, req.UID, req.ResourceVersion, *req.Suspended)
	if err != nil {
		switch {
		case errors.Is(err, kube.ErrInstanceChanged):
			writeError(w, http.StatusConflict, "instance_changed", "Agent changed or was replaced; reload before saving")
		case errors.Is(err, kube.ErrInstanceDeleting):
			writeError(w, http.StatusConflict, "instance_deleting", "Agent is being deleted")
		case kube.IsNotFound(err):
			writeError(w, http.StatusNotFound, "not_found", "Agent not found")
		default:
			slog.Error("admin: update Agent lifecycle failed", "name", name, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update Agent lifecycle")
		}
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// instanceTeam returns the Team binding for an instance.
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
