// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"net/http"
	"strings"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/auth"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
	"k8s.io/apimachinery/pkg/types"
)

// TeamChangeRequest is the Portal input for a durable Team-change operation.
// The idempotency key comes from the header so clients can retry a lost 202.
type TeamChangeRequest struct {
	UID             string              `json:"uid"`
	ResourceVersion string              `json:"resourceVersion"`
	TargetTeam      string              `json:"targetTeam"`
	TargetSandbox   string              `json:"targetSandbox,omitempty"`
	Model           string              `json:"model,omitempty"`
	ModelAccess     *ModelAccessRequest `json:"modelAccess,omitempty"`
}

// TeamChangeResponse contains only the operation metadata needed to track or
// retry a Team change. It is also safe for the administrator list workflow.
type TeamChangeResponse struct {
	UID             string                 `json:"uid"`
	ResourceVersion string                 `json:"resourceVersion"`
	Team            string                 `json:"team"`
	Sandbox         string                 `json:"sandbox"`
	Suspended       bool                   `json:"suspended"`
	Pending         bool                   `json:"pending"`
	Request         *api.TeamChangeRequest `json:"request,omitempty"`
	Status          *api.TeamChangeStatus  `json:"status,omitempty"`
}

func teamChangeResponse(instance *api.Agent) TeamChangeResponse {
	return TeamChangeResponse{
		UID:             string(instance.UID),
		ResourceVersion: instance.ResourceVersion,
		Team:            instance.Spec.Team,
		Sandbox:         instance.Spec.SandboxRef,
		Suspended:       instance.Spec.Suspended,
		Pending:         instance.TeamChangePending(),
		Request:         instance.Spec.TeamChangeRequest,
		Status:          instance.Status.TeamChange,
	}
}

func (h *InstanceHandler) GetTeamChangeByName(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not_authenticated", "Authentication is required")
		return
	}
	name := r.PathValue("name")
	instance, err := h.Kube.GetInstanceByName(r.Context(), name)
	if err != nil {
		writeManagementError(w, err)
		return
	}
	if !claims.IsAdmin && !ownsInstance(instance, claims) {
		writeError(w, http.StatusForbidden, "forbidden", "Only the instance owner or a platform administrator may change this Agent's Team")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, teamChangeResponse(instance))
}

func (h *InstanceHandler) RequestTeamChangeByName(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not_authenticated", "Authentication is required")
		return
	}
	if !h.validManagementOrigin(w, r) {
		return
	}
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "invalid_name", "Agent name is required")
		return
	}
	var request TeamChangeRequest
	if _, ok := decodeManagementRequest(w, r, &request); !ok {
		return
	}
	request.TargetTeam = strings.TrimSpace(request.TargetTeam)
	request.TargetSandbox = strings.TrimSpace(request.TargetSandbox)
	request.Model = strings.TrimSpace(request.Model)
	if request.UID == "" || request.ResourceVersion == "" || request.TargetTeam == "" ||
		!api.ValidRestartRequestID(r.Header.Get("Idempotency-Key")) || len(request.Model) > 256 {
		writeError(w, http.StatusBadRequest, "invalid_team_change", "Provide uid, resourceVersion, targetTeam, an optional valid model, and a valid Idempotency-Key")
		return
	}
	instance, err := h.Kube.GetInstanceByName(r.Context(), name)
	if err != nil {
		writeManagementError(w, err)
		return
	}
	if !claims.IsAdmin && !ownsInstance(instance, claims) {
		writeError(w, http.StatusForbidden, "forbidden", "Only the instance owner or a platform administrator may change this Agent's Team")
		return
	}
	operation := api.TeamChangeRequest{
		RequestID:               r.Header.Get("Idempotency-Key"),
		ExpectedUID:             types.UID(request.UID),
		ExpectedResourceVersion: request.ResourceVersion,
		TargetTeam:              request.TargetTeam,
		TargetSandbox:           request.TargetSandbox,
		Model:                   request.Model,
		ModelAccess:             modelAccessSpec(request.ModelAccess),
	}
	// An accepted operation must be retriable even if its target Team was
	// removed after the Portal lost its 202 response. Authorization happens
	// before this lookup so a request ID never reveals another owner's state.
	if accepted, found := instance.TeamChangeRequestForID(operation.RequestID); found {
		if !api.TeamChangeRequestsEqual(accepted, &operation) {
			writeManagementError(w, kube.ErrTeamChangePending)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusAccepted, teamChangeResponse(instance))
		return
	}
	// Validate the selected Team binding before recording a request. HOME and
	// final model authorization are controller preflight checks because they
	// depend on the reconciled execution and must remain true until stopping.
	target, _, err := h.resolveSelectedTeam(r, request.TargetTeam)
	if err != nil {
		writeManagementError(w, err)
		return
	}
	if request.TargetSandbox != "" {
		if !containsString(target.Spec.SandboxRefs, request.TargetSandbox) {
			writeError(w, http.StatusBadRequest, "sandbox_not_allowed", "The selected Sandbox is not allowed by the target Team")
			return
		}
		if _, err := h.Kube.GetSandbox(r.Context(), request.TargetSandbox); err != nil {
			writeManagementError(w, err)
			return
		}
	}
	stored, err := h.Kube.RequestTeamChangeByName(r.Context(), name, operation, func(current *api.Agent) error {
		if !claims.IsAdmin && !ownsInstance(current, claims) {
			return &managementError{status: http.StatusForbidden, code: "forbidden", message: "Only the instance owner or a platform administrator may change this Agent's Team"}
		}
		return nil
	})
	if err != nil {
		writeManagementError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusAccepted, teamChangeResponse(stored))
}

func modelAccessSpec(input *ModelAccessRequest) *api.ModelAccessSpec {
	if input == nil {
		return nil
	}
	return &api.ModelAccessSpec{SharedModels: input.SharedModels, GitHubCopilot: input.GitHubCopilot}
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
