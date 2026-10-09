// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
)

// AdminTeamHandler implements CRUD for Team resources under
// /api/v1/admin/teams. Authorization is enforced by an upstream middleware
// (auth.RequireAdmin); this handler does not check the admin flag itself.
type AdminTeamHandler struct {
	Kube *kube.Client
}

// teamNamePattern enforces a DNS-1123 label for the cluster-scoped team
// name. Mirrors the alias pattern used elsewhere in the portal.
var teamNamePattern = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

// TeamSkillStorageDTO mirrors cetirunv1alpha1.SkillStorageSpec.
type TeamSkillStorageDTO struct {
	ResourceGroup    string `json:"resourceGroup"`
	StorageAccount   string `json:"storageAccount"`
	ContainerName    string `json:"containerName"`
	StorageClassName string `json:"storageClassName,omitempty"`
	StorageSize      string `json:"storageSize,omitempty"`
	MountPath        string `json:"mountPath,omitempty"`
}

// TeamDTO is the JSON shape for Team admin endpoints.
type TeamDTO struct {
	Name                 string               `json:"name"`
	UID                  string               `json:"uid"`
	ResourceVersion      string               `json:"resourceVersion"`
	Role                 string               `json:"role,omitempty"`
	Description          []string             `json:"description,omitempty"`
	DefaultProvider      string               `json:"defaultProvider,omitempty"`
	Skills               []string             `json:"skills,omitempty"`
	SkillStorage         *TeamSkillStorageDTO `json:"skillStorage,omitempty"`
	NormalAgentPolicyRef string               `json:"normalAgentPolicyRef"`
	AdminAgentPolicyRef  string               `json:"adminAgentPolicyRef,omitempty"`
	SandboxRefs          []string             `json:"sandboxRefs"`
	DefaultSandboxRef    string               `json:"defaultSandboxRef"`
	CreatedAt            string               `json:"createdAt,omitempty"`
}

// TeamUpsertRequest is the JSON body for create/update.
// For create, Name is taken from the URL or body; for update, Name is the
// URL path parameter and the body's name (if set) must match.
type TeamUpsertRequest struct {
	Name                 string               `json:"name,omitempty"`
	UID                  string               `json:"uid,omitempty"`
	ResourceVersion      string               `json:"resourceVersion,omitempty"`
	Role                 string               `json:"role,omitempty"`
	Description          []string             `json:"description,omitempty"`
	DefaultProvider      string               `json:"defaultProvider,omitempty"`
	Skills               []string             `json:"skills,omitempty"`
	SkillStorage         *TeamSkillStorageDTO `json:"skillStorage,omitempty"`
	NormalAgentPolicyRef string               `json:"normalAgentPolicyRef"`
	AdminAgentPolicyRef  string               `json:"adminAgentPolicyRef,omitempty"`
	SandboxRefs          []string             `json:"sandboxRefs"`
	DefaultSandboxRef    string               `json:"defaultSandboxRef"`
}

func toTeamDTO(t *cetirunv1alpha1.Team) TeamDTO {
	dto := TeamDTO{
		Name:                 t.Name,
		UID:                  string(t.UID),
		ResourceVersion:      t.ResourceVersion,
		Role:                 t.Spec.MemberContext.Role,
		Description:          t.Spec.MemberContext.Description,
		DefaultProvider:      t.Spec.Defaults.Provider,
		Skills:               t.Spec.Skills,
		NormalAgentPolicyRef: t.Spec.NormalAgentPolicyRef,
		AdminAgentPolicyRef:  t.Spec.AdminAgentPolicyRef,
		SandboxRefs:          append([]string(nil), t.Spec.SandboxRefs...),
		DefaultSandboxRef:    t.Spec.DefaultSandboxRef,
		CreatedAt:            t.CreationTimestamp.UTC().Format("2006-01-02T15:04:05Z"),
	}
	if t.Spec.SkillStorage != nil {
		dto.SkillStorage = &TeamSkillStorageDTO{
			ResourceGroup:    t.Spec.SkillStorage.ResourceGroup,
			StorageAccount:   t.Spec.SkillStorage.StorageAccount,
			ContainerName:    t.Spec.SkillStorage.ContainerName,
			StorageClassName: t.Spec.SkillStorage.StorageClassName,
			StorageSize:      t.Spec.SkillStorage.StorageSize,
			MountPath:        t.Spec.SkillStorage.MountPath,
		}
	}
	return dto
}

func toTeamSpec(req TeamUpsertRequest) cetirunv1alpha1.TeamSpec {
	spec := cetirunv1alpha1.TeamSpec{
		NormalAgentPolicyRef: strings.TrimSpace(req.NormalAgentPolicyRef),
		AdminAgentPolicyRef:  strings.TrimSpace(req.AdminAgentPolicyRef),
		SandboxRefs:          append([]string(nil), req.SandboxRefs...),
		DefaultSandboxRef:    strings.TrimSpace(req.DefaultSandboxRef),
		MemberContext: cetirunv1alpha1.TeamContextSpec{
			Role:        req.Role,
			Description: req.Description,
		},
		Defaults: cetirunv1alpha1.TeamDefaultsSpec{
			Provider: req.DefaultProvider,
		},
		Skills: req.Skills,
	}
	if req.SkillStorage != nil {
		spec.SkillStorage = &cetirunv1alpha1.SkillStorageSpec{
			ResourceGroup:    req.SkillStorage.ResourceGroup,
			StorageAccount:   req.SkillStorage.StorageAccount,
			ContainerName:    req.SkillStorage.ContainerName,
			StorageClassName: req.SkillStorage.StorageClassName,
			StorageSize:      req.SkillStorage.StorageSize,
			MountPath:        req.SkillStorage.MountPath,
		}
	}
	return spec
}

func validatePolicyTeam(req TeamUpsertRequest) string {
	if strings.TrimSpace(req.NormalAgentPolicyRef) == "" {
		return "normalAgentPolicyRef is required"
	}
	if len(req.SandboxRefs) == 0 || strings.TrimSpace(req.DefaultSandboxRef) == "" {
		return "sandboxRefs and defaultSandboxRef are required"
	}
	for _, sandbox := range req.SandboxRefs {
		if strings.TrimSpace(sandbox) == req.DefaultSandboxRef {
			return ""
		}
	}
	return "defaultSandboxRef must appear in sandboxRefs"
}

// validateSkillStorage returns a non-empty error message when the optional
// skillStorage block is present but missing CRD-required fields. The fields
// resourceGroup, storageAccount, containerName are marked required in the
// Team CRD, so accepting empty values would persist an invalid spec.
func validateSkillStorage(s *TeamSkillStorageDTO) string {
	if s == nil {
		return ""
	}
	switch {
	case strings.TrimSpace(s.ResourceGroup) == "":
		return "skillStorage.resourceGroup is required"
	case strings.TrimSpace(s.StorageAccount) == "":
		return "skillStorage.storageAccount is required"
	case strings.TrimSpace(s.ContainerName) == "":
		return "skillStorage.containerName is required"
	}
	return ""
}

// ListTeams handles GET /api/v1/admin/teams.
func (h *AdminTeamHandler) ListTeams(w http.ResponseWriter, r *http.Request) {
	teams, err := h.Kube.ListTeams(r.Context())
	if err != nil {
		slog.Error("admin: list teams failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list teams")
		return
	}
	out := make([]TeamDTO, 0, len(teams))
	for i := range teams {
		out = append(out, toTeamDTO(&teams[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"teams": out})
}

// GetTeam handles GET /api/v1/admin/teams/{name}.
func (h *AdminTeamHandler) GetTeam(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !teamNamePattern.MatchString(name) {
		writeError(w, http.StatusBadRequest, "invalid_name", "Invalid team name")
		return
	}
	team, err := h.Kube.GetTeam(r.Context(), name)
	if err != nil {
		if kube.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "team_not_found", "Team "+name+" not found")
			return
		}
		slog.Error("admin: get team failed", "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get team")
		return
	}
	writeJSON(w, http.StatusOK, toTeamDTO(team))
}

// CreateTeam handles POST /api/v1/admin/teams.
func (h *AdminTeamHandler) CreateTeam(w http.ResponseWriter, r *http.Request) {
	var req TeamUpsertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Invalid JSON body")
		return
	}
	if !teamNamePattern.MatchString(req.Name) {
		writeError(w, http.StatusBadRequest, "invalid_name",
			"name must be a DNS-1123 label (start with letter, lowercase, max 63 chars)")
		return
	}
	if msg := validateSkillStorage(req.SkillStorage); msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_skill_storage", msg)
		return
	}
	if msg := validatePolicyTeam(req); msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_policy", msg)
		return
	}
	team := &cetirunv1alpha1.Team{
		ObjectMeta: metav1.ObjectMeta{Name: req.Name},
		Spec:       toTeamSpec(req),
	}
	created, err := h.Kube.CreateTeam(r.Context(), team)
	if err != nil {
		if kube.IsAlreadyExists(err) {
			writeError(w, http.StatusConflict, "team_exists", "Team "+req.Name+" already exists")
			return
		}
		if kube.IsForbidden(err) {
			slog.Error("admin: create team forbidden by k8s RBAC", "name", req.Name, "error", err)
			writeError(w, http.StatusForbidden, "rbac_forbidden",
				"Portal service account is not allowed to create teams. Check ClusterRole bindings.")
			return
		}
		slog.Error("admin: create team failed", "name", req.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to create team")
		return
	}
	writeJSON(w, http.StatusCreated, toTeamDTO(created))
}

// UpdateTeam handles PUT /api/v1/admin/teams/{name}. The request body
// replaces the complete Team configuration, including policy bindings.
func (h *AdminTeamHandler) UpdateTeam(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !teamNamePattern.MatchString(name) {
		writeError(w, http.StatusBadRequest, "invalid_name", "Invalid team name")
		return
	}
	var req TeamUpsertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Invalid JSON body")
		return
	}
	if req.Name != "" && req.Name != name {
		writeError(w, http.StatusBadRequest, "name_mismatch",
			"body.name does not match URL name")
		return
	}
	if req.UID == "" || req.ResourceVersion == "" {
		writeError(w, http.StatusPreconditionRequired, "version_required",
			"uid and resourceVersion from the current Team are required")
		return
	}
	if msg := validateSkillStorage(req.SkillStorage); msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_skill_storage", msg)
		return
	}
	if msg := validatePolicyTeam(req); msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_policy", msg)
		return
	}
	updated, err := h.Kube.UpdateTeamSpec(r.Context(), name, req.UID, req.ResourceVersion, toTeamSpec(req))
	if err != nil {
		if kube.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "team_not_found", "Team "+name+" not found")
			return
		}
		if kube.IsForbidden(err) {
			slog.Error("admin: update team forbidden by k8s RBAC", "name", name, "error", err)
			writeError(w, http.StatusForbidden, "rbac_forbidden",
				"Portal service account is not allowed to update teams. Check ClusterRole bindings.")
			return
		}
		if errors.Is(err, kube.ErrTeamChanged) || kube.IsConflict(err) {
			writeError(w, http.StatusConflict, "team_changed", kube.ErrTeamChanged.Error())
			return
		}
		slog.Error("admin: update team failed", "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update team")
		return
	}
	writeJSON(w, http.StatusOK, toTeamDTO(updated))
}

// DeleteTeam handles DELETE /api/v1/admin/teams/{name}.
func (h *AdminTeamHandler) DeleteTeam(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !teamNamePattern.MatchString(name) {
		writeError(w, http.StatusBadRequest, "invalid_name", "Invalid team name")
		return
	}
	if err := h.Kube.DeleteTeam(r.Context(), name); err != nil {
		if kube.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "team_not_found", "Team "+name+" not found")
			return
		}
		if kube.IsForbidden(err) {
			slog.Error("admin: delete team forbidden by k8s RBAC", "name", name, "error", err)
			writeError(w, http.StatusForbidden, "rbac_forbidden",
				"Portal service account is not allowed to delete teams. Check ClusterRole bindings.")
			return
		}
		slog.Error("admin: delete team failed", "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete team")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
