// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"regexp"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
)

// AdminPolicyHandler implements administrative CRUD for the Portal-managed
// portion of cluster-scoped AgentPolicy resources. Runtime components remain
// responsible for interpreting and enforcing the policy fields they own.
type AdminPolicyHandler struct {
	Kube *kube.Client
}

var policyNamePattern = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)
var monthlyUSDAmountPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]{1,6})?$`)

func validAgentPolicyResourceName(name string) bool {
	return len(validation.IsDNS1123Subdomain(name)) == 0
}

type AgentPolicyHarnessesDTO struct {
	Allowed []cetirunv1alpha1.AgentType `json:"allowed"`
}

type AgentPolicySkillsDTO struct {
	Allowed []string `json:"allowed,omitempty"`
}

type AgentPolicyCopilotAccessDTO struct {
	Allowed bool `json:"allowed,omitempty"`
}

type AgentPolicySharedModelsAccessDTO struct {
	Allowed []string `json:"allowed,omitempty"`
}

type AgentPolicyGatewayBudgetDTO struct {
	DefaultMonthlyUSD string `json:"defaultMonthlyUSD"`
	MaxMonthlyUSD     string `json:"maxMonthlyUSD"`
}

type AgentPolicyModelAccessDTO struct {
	Copilot       AgentPolicyCopilotAccessDTO      `json:"copilot,omitempty"`
	SharedModels  AgentPolicySharedModelsAccessDTO `json:"sharedModels,omitempty"`
	GatewayBudget *AgentPolicyGatewayBudgetDTO     `json:"gatewayBudget,omitempty"`
	// RateLimits replaces the policy's limits on save; omitting it removes them.
	RateLimits *AgentPolicyRateLimitsDTO `json:"rateLimits,omitempty"`
}

// AgentPolicyDTO exposes the fields managed by the Portal. Updates preserve
// fields owned by runtime components that are not represented here.
type AgentPolicyDTO struct {
	Name            string                    `json:"name"`
	UID             string                    `json:"uid"`
	ResourceVersion string                    `json:"resourceVersion"`
	Harnesses       AgentPolicyHarnessesDTO   `json:"harnesses"`
	Skills          AgentPolicySkillsDTO      `json:"skills,omitempty"`
	ModelAccess     AgentPolicyModelAccessDTO `json:"modelAccess,omitempty"`
	CreatedAt       string                    `json:"createdAt,omitempty"`
}

// AgentPolicyUpsertRequest is the public create/update body.
type AgentPolicyUpsertRequest struct {
	Name            string                    `json:"name,omitempty"`
	UID             string                    `json:"uid,omitempty"`
	ResourceVersion string                    `json:"resourceVersion,omitempty"`
	Harnesses       AgentPolicyHarnessesDTO   `json:"harnesses"`
	Skills          AgentPolicySkillsDTO      `json:"skills,omitempty"`
	ModelAccess     AgentPolicyModelAccessDTO `json:"modelAccess,omitempty"`
}

func toAgentPolicyDTO(policy *cetirunv1alpha1.AgentPolicy) AgentPolicyDTO {
	return AgentPolicyDTO{
		Name:            policy.Name,
		UID:             string(policy.UID),
		ResourceVersion: policy.ResourceVersion,
		Harnesses:       AgentPolicyHarnessesDTO{Allowed: append([]cetirunv1alpha1.AgentType(nil), policy.Spec.Harnesses.Allowed...)},
		Skills:          AgentPolicySkillsDTO{Allowed: append([]string(nil), policy.Spec.Skills.Allowed...)},
		ModelAccess: AgentPolicyModelAccessDTO{
			Copilot:       AgentPolicyCopilotAccessDTO{Allowed: policy.Spec.ModelAccess.Copilot.Allowed},
			SharedModels:  AgentPolicySharedModelsAccessDTO{Allowed: append([]string(nil), policy.Spec.ModelAccess.SharedModels.Allowed...)},
			GatewayBudget: toAgentPolicyGatewayBudgetDTO(policy.Spec.ModelAccess.GatewayBudget),
			RateLimits:    toAgentPolicyRateLimitsDTO(policy.Spec.ModelAccess.RateLimits),
		},
		CreatedAt: policy.CreationTimestamp.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

func toAgentPolicyGatewayBudgetDTO(budget *cetirunv1alpha1.AgentPolicyGatewayBudget) *AgentPolicyGatewayBudgetDTO {
	if budget == nil {
		return nil
	}
	return &AgentPolicyGatewayBudgetDTO{
		DefaultMonthlyUSD: budget.DefaultMonthlyUSD,
		MaxMonthlyUSD:     budget.MaxMonthlyUSD,
	}
}

func toAgentPolicySpec(req AgentPolicyUpsertRequest) cetirunv1alpha1.AgentPolicySpec {
	return cetirunv1alpha1.AgentPolicySpec{
		Harnesses: cetirunv1alpha1.AgentPolicyHarnesses{Allowed: append([]cetirunv1alpha1.AgentType(nil), req.Harnesses.Allowed...)},
		Skills:    cetirunv1alpha1.AgentPolicySkills{Allowed: append([]string(nil), req.Skills.Allowed...)},
		ModelAccess: cetirunv1alpha1.AgentPolicyModelAccess{
			Copilot:       cetirunv1alpha1.AgentPolicyCopilotAccess{Allowed: req.ModelAccess.Copilot.Allowed},
			SharedModels:  cetirunv1alpha1.AgentPolicySharedModelsAccess{Allowed: append([]string(nil), req.ModelAccess.SharedModels.Allowed...)},
			GatewayBudget: toAgentPolicyGatewayBudget(req.ModelAccess.GatewayBudget),
			RateLimits:    toAgentPolicyRateLimits(req.ModelAccess.RateLimits),
		},
	}
}

func toAgentPolicyGatewayBudget(budget *AgentPolicyGatewayBudgetDTO) *cetirunv1alpha1.AgentPolicyGatewayBudget {
	if budget == nil {
		return nil
	}
	return &cetirunv1alpha1.AgentPolicyGatewayBudget{
		DefaultMonthlyUSD: budget.DefaultMonthlyUSD,
		MaxMonthlyUSD:     budget.MaxMonthlyUSD,
	}
}

func validateAgentPolicyRequest(req AgentPolicyUpsertRequest) string {
	if len(req.Harnesses.Allowed) == 0 {
		return "harnesses.allowed must contain at least one harness"
	}
	seenHarness := map[cetirunv1alpha1.AgentType]bool{}
	for _, harness := range req.Harnesses.Allowed {
		if harness != cetirunv1alpha1.AgentTypeOpenClaw && harness != cetirunv1alpha1.AgentTypeHermes {
			return "harnesses.allowed may contain only openclaw or hermes"
		}
		if seenHarness[harness] {
			return "harnesses.allowed must not contain duplicate entries"
		}
		seenHarness[harness] = true
	}
	hasModelEntitlement := req.ModelAccess.Copilot.Allowed || len(req.ModelAccess.SharedModels.Allowed) > 0
	if hasModelEntitlement && req.ModelAccess.GatewayBudget == nil {
		return "model access requires gatewayBudget"
	}
	if !hasModelEntitlement && req.ModelAccess.GatewayBudget != nil {
		return "gatewayBudget requires Copilot or a shared model"
	}
	if budget := req.ModelAccess.GatewayBudget; budget != nil {
		if !monthlyUSDAmountPattern.MatchString(budget.DefaultMonthlyUSD) || !monthlyUSDAmountPattern.MatchString(budget.MaxMonthlyUSD) {
			return "gatewayBudget amounts must be positive USD amounts with at most six decimal places"
		}
		defaultBudget, _ := new(big.Rat).SetString(budget.DefaultMonthlyUSD)
		maxBudget, _ := new(big.Rat).SetString(budget.MaxMonthlyUSD)
		if defaultBudget.Sign() <= 0 || maxBudget.Sign() <= 0 {
			return "gatewayBudget amounts must be greater than zero"
		}
		if defaultBudget.Cmp(maxBudget) > 0 {
			return "gatewayBudget.defaultMonthlyUSD must not exceed gatewayBudget.maxMonthlyUSD"
		}
	}
	for _, model := range req.ModelAccess.SharedModels.Allowed {
		if strings.TrimSpace(model) == "" || strings.ContainsAny(model, "\t\r\n ") {
			return "sharedModels.allowed entries must be non-empty model identifiers without whitespace"
		}
	}
	return validateAgentPolicyRateLimits(req.ModelAccess.RateLimits)
}

func agentPolicyVersion(req AgentPolicyUpsertRequest) (kube.AgentPolicyVersion, string) {
	if req.UID == "" || req.ResourceVersion == "" {
		return kube.AgentPolicyVersion{}, "uid and resourceVersion from the current AgentPolicy are required"
	}
	return kube.AgentPolicyVersion{UID: types.UID(req.UID), ResourceVersion: req.ResourceVersion}, ""
}

// ListPolicies handles GET /api/v1/admin/policies.
func (h *AdminPolicyHandler) ListPolicies(w http.ResponseWriter, r *http.Request) {
	policies, err := h.Kube.ListAgentPolicies(r.Context())
	if err != nil {
		slog.Error("admin: list agent policies failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list agent policies")
		return
	}
	out := make([]AgentPolicyDTO, 0, len(policies))
	for i := range policies {
		out = append(out, toAgentPolicyDTO(&policies[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"policies": out})
}

// GetPolicy handles GET /api/v1/admin/policies/{name}.
func (h *AdminPolicyHandler) GetPolicy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validAgentPolicyResourceName(name) {
		writeError(w, http.StatusBadRequest, "invalid_name", "Invalid policy name")
		return
	}
	policy, err := h.Kube.GetAgentPolicy(r.Context(), name)
	if err != nil {
		if kube.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "policy_not_found", "AgentPolicy "+name+" not found")
			return
		}
		slog.Error("admin: get agent policy failed", "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to get agent policy")
		return
	}
	writeJSON(w, http.StatusOK, toAgentPolicyDTO(policy))
}

// CreatePolicy handles POST /api/v1/admin/policies.
func (h *AdminPolicyHandler) CreatePolicy(w http.ResponseWriter, r *http.Request) {
	var req AgentPolicyUpsertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Invalid JSON body")
		return
	}
	if !policyNamePattern.MatchString(req.Name) {
		writeError(w, http.StatusBadRequest, "invalid_name", "name must be a DNS-1123 label (start with letter, lowercase, max 63 chars)")
		return
	}
	if msg := validateAgentPolicyRequest(req); msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_policy", msg)
		return
	}
	policy := &cetirunv1alpha1.AgentPolicy{ObjectMeta: metav1.ObjectMeta{Name: req.Name}, Spec: toAgentPolicySpec(req)}
	created, err := h.Kube.CreateAgentPolicy(r.Context(), policy)
	if err != nil {
		if kube.IsAlreadyExists(err) {
			writeError(w, http.StatusConflict, "policy_exists", "AgentPolicy "+req.Name+" already exists")
			return
		}
		if kube.IsForbidden(err) {
			writeError(w, http.StatusForbidden, "policy_forbidden", "Kubernetes policy or RBAC denied creating this AgentPolicy")
			return
		}
		slog.Error("admin: create agent policy failed", "name", req.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to create agent policy")
		return
	}
	writeJSON(w, http.StatusCreated, toAgentPolicyDTO(created))
}

// UpdatePolicy handles PUT /api/v1/admin/policies/{name}.
func (h *AdminPolicyHandler) UpdatePolicy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validAgentPolicyResourceName(name) {
		writeError(w, http.StatusBadRequest, "invalid_name", "Invalid policy name")
		return
	}
	var req AgentPolicyUpsertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "Invalid JSON body")
		return
	}
	if req.Name != "" && req.Name != name {
		writeError(w, http.StatusBadRequest, "name_mismatch", "body.name does not match URL name")
		return
	}
	if msg := validateAgentPolicyRequest(req); msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_policy", msg)
		return
	}
	version, msg := agentPolicyVersion(req)
	if msg != "" {
		writeError(w, http.StatusPreconditionRequired, "version_required", msg)
		return
	}
	updated, err := h.Kube.UpdateAgentPolicySpec(r.Context(), name, toAgentPolicySpec(req), version)
	if err != nil {
		if kube.IsConfigurationCoordinationUnavailable(err) {
			writeError(w, http.StatusServiceUnavailable, "policy_change_unavailable", "Policy coordination is unavailable or the write outcome is uncertain; do not resubmit until an administrator recovers the recorded operation")
			return
		}
		if kube.IsConfigurationCoordinationConflict(err) {
			writeError(w, http.StatusConflict, "policy_change_conflict", "Another coordinated policy change or governed execution is in progress; retry after it completes")
			return
		}
		if kube.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "policy_not_found", "AgentPolicy "+name+" not found")
			return
		}
		if kube.IsForbidden(err) {
			writeError(w, http.StatusForbidden, "policy_forbidden", "Kubernetes policy or RBAC denied updating this AgentPolicy")
			return
		}
		if errors.Is(err, kube.ErrAgentPolicyChanged) || apierrors.IsConflict(err) {
			writeError(w, http.StatusConflict, "policy_changed", kube.ErrAgentPolicyChanged.Error())
			return
		}
		slog.Error("admin: update agent policy failed", "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update agent policy")
		return
	}
	writeJSON(w, http.StatusOK, toAgentPolicyDTO(updated))
}

// DeletePolicy handles DELETE /api/v1/admin/policies/{name}.
func (h *AdminPolicyHandler) DeletePolicy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validAgentPolicyResourceName(name) {
		writeError(w, http.StatusBadRequest, "invalid_name", "Invalid policy name")
		return
	}
	var versionRequest struct {
		UID             string `json:"uid"`
		ResourceVersion string `json:"resourceVersion"`
	}
	if err := json.NewDecoder(r.Body).Decode(&versionRequest); err != nil || versionRequest.UID == "" || versionRequest.ResourceVersion == "" {
		writeError(w, http.StatusPreconditionRequired, "version_required", "uid and resourceVersion from the current AgentPolicy are required")
		return
	}
	version := kube.AgentPolicyVersion{UID: types.UID(versionRequest.UID), ResourceVersion: versionRequest.ResourceVersion}
	if err := h.Kube.DeleteAgentPolicy(r.Context(), name, version); err != nil {
		if kube.IsConfigurationCoordinationUnavailable(err) {
			writeError(w, http.StatusServiceUnavailable, "policy_change_unavailable", "Policy coordination is unavailable or the delete outcome is uncertain; do not resubmit until an administrator recovers the recorded operation")
			return
		}
		if kube.IsConfigurationCoordinationConflict(err) {
			writeError(w, http.StatusConflict, "policy_change_conflict", "Another coordinated policy change or governed execution is in progress; retry after it completes")
			return
		}
		if kube.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "policy_not_found", "AgentPolicy "+name+" not found")
			return
		}
		if kube.IsForbidden(err) {
			writeError(w, http.StatusForbidden, "policy_forbidden", "Kubernetes policy or RBAC denied deleting this AgentPolicy; verify that no Team still references it")
			return
		}
		if errors.Is(err, kube.ErrAgentPolicyChanged) || apierrors.IsConflict(err) {
			writeError(w, http.StatusConflict, "policy_changed", kube.ErrAgentPolicyChanged.Error())
			return
		}
		slog.Error("admin: delete agent policy failed", "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete agent policy")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
