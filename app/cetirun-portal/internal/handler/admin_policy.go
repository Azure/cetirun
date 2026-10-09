// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
)

// AdminPolicyHandler manages the cluster-scoped policy resources and the
// administrator-only Agent policy assignment endpoint. Routes are protected by
// RequireAdmin; this type deliberately has no Team membership behavior.
type AdminPolicyHandler struct{ Kube *kube.Client }

type policyResourcePrecondition struct {
	UID             string `json:"uid"`
	ResourceVersion string `json:"resourceVersion"`
}

func policyObjectName(r *http.Request) (string, bool) {
	name := r.PathValue("name")
	return name, teamNamePattern.MatchString(name)
}

func (h *AdminPolicyHandler) ListAgentPolicies(w http.ResponseWriter, r *http.Request) {
	items, err := h.Kube.ListAgentPolicies(r.Context())
	h.writeObject(w, items, err)
}
func (h *AdminPolicyHandler) GetAgentPolicy(w http.ResponseWriter, r *http.Request) {
	name, ok := policyObjectName(r)
	if !ok {
		writeError(w, 400, "invalid_name", "Invalid policy name")
		return
	}
	item, err := h.Kube.GetAgentPolicy(r.Context(), name)
	h.writeObject(w, item, err)
}
func (h *AdminPolicyHandler) CreateAgentPolicy(w http.ResponseWriter, r *http.Request) {
	var item cetirunv1alpha1.AgentPolicy
	if !decodeStrictPolicy(w, r, &item) {
		return
	}
	if !teamNamePattern.MatchString(item.Name) {
		writeError(w, 400, "invalid_name", "Invalid policy name")
		return
	}
	created, err := h.Kube.CreateAgentPolicy(r.Context(), &item)
	if err != nil {
		h.writeObject(w, nil, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}
func (h *AdminPolicyHandler) UpdateAgentPolicy(w http.ResponseWriter, r *http.Request) {
	name, ok := policyObjectName(r)
	if !ok {
		writeError(w, 400, "invalid_name", "Invalid policy name")
		return
	}
	var request struct {
		policyResourcePrecondition
		Spec cetirunv1alpha1.AgentPolicySpec `json:"spec"`
	}
	if !decodeStrictPolicy(w, r, &request) {
		return
	}
	if !requirePolicyResourceVersion(w, request.policyResourcePrecondition) {
		return
	}
	item, err := h.Kube.UpdateAgentPolicy(r.Context(), name, request.UID, request.ResourceVersion, request.Spec)
	h.writeObject(w, item, err)
}
func (h *AdminPolicyHandler) DeleteAgentPolicy(w http.ResponseWriter, r *http.Request) {
	name, ok := policyObjectName(r)
	if !ok {
		writeError(w, 400, "invalid_name", "Invalid policy name")
		return
	}
	var request policyResourcePrecondition
	if !decodeStrictPolicy(w, r, &request) || !requirePolicyResourceVersion(w, request) {
		return
	}
	h.delete(w, h.Kube.DeleteAgentPolicy(r.Context(), name, request.UID, request.ResourceVersion))
}

func (h *AdminPolicyHandler) ListSandboxes(w http.ResponseWriter, r *http.Request) {
	items, err := h.Kube.ListSandboxes(r.Context())
	h.writeObject(w, items, err)
}
func (h *AdminPolicyHandler) GetSandbox(w http.ResponseWriter, r *http.Request) {
	name, ok := policyObjectName(r)
	if !ok {
		writeError(w, 400, "invalid_name", "Invalid sandbox name")
		return
	}
	item, err := h.Kube.GetSandbox(r.Context(), name)
	h.writeObject(w, item, err)
}
func (h *AdminPolicyHandler) CreateSandbox(w http.ResponseWriter, r *http.Request) {
	var item cetirunv1alpha1.Sandbox
	if !decodeStrictPolicy(w, r, &item) {
		return
	}
	if !teamNamePattern.MatchString(item.Name) {
		writeError(w, 400, "invalid_name", "Invalid sandbox name")
		return
	}
	created, err := h.Kube.CreateSandbox(r.Context(), &item)
	if err != nil {
		h.writeObject(w, nil, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}
func (h *AdminPolicyHandler) UpdateSandbox(w http.ResponseWriter, r *http.Request) {
	name, ok := policyObjectName(r)
	if !ok {
		writeError(w, 400, "invalid_name", "Invalid sandbox name")
		return
	}
	var request struct {
		policyResourcePrecondition
		Spec cetirunv1alpha1.SandboxSpec `json:"spec"`
	}
	if !decodeStrictPolicy(w, r, &request) {
		return
	}
	if !requirePolicyResourceVersion(w, request.policyResourcePrecondition) {
		return
	}
	item, err := h.Kube.UpdateSandbox(r.Context(), name, request.UID, request.ResourceVersion, request.Spec)
	h.writeObject(w, item, err)
}
func (h *AdminPolicyHandler) DeleteSandbox(w http.ResponseWriter, r *http.Request) {
	name, ok := policyObjectName(r)
	if !ok {
		writeError(w, 400, "invalid_name", "Invalid sandbox name")
		return
	}
	var request policyResourcePrecondition
	if !decodeStrictPolicy(w, r, &request) || !requirePolicyResourceVersion(w, request) {
		return
	}
	h.delete(w, h.Kube.DeleteSandbox(r.Context(), name, request.UID, request.ResourceVersion))
}

func requirePolicyResourceVersion(w http.ResponseWriter, request policyResourcePrecondition) bool {
	if request.UID == "" || request.ResourceVersion == "" {
		writeError(w, http.StatusPreconditionRequired, "version_required", "uid and resourceVersion from the current policy resource are required")
		return false
	}
	return true
}

// UpdateInstancePolicy changes an Agent's policy assignment. The Agent must
// already be suspended. The kube client emits the admission reauthorization
// intent when the selection changes.
func (h *AdminPolicyHandler) UpdateInstancePolicy(w http.ResponseWriter, r *http.Request) {
	name, ok := policyObjectName(r)
	if !ok {
		writeError(w, 400, "invalid_name", "Invalid Agent name")
		return
	}
	var request struct {
		UID              string                            `json:"uid"`
		ResourceVersion  string                            `json:"resourceVersion"`
		Team             *string                           `json:"team,omitempty"`
		PolicyClass      *cetirunv1alpha1.AgentPolicyClass `json:"policyClass,omitempty"`
		SandboxRef       *string                           `json:"sandboxRef,omitempty"`
		MonthlyBudgetUSD *string                           `json:"monthlyBudgetUSD,omitempty"`
	}
	if !decodeStrictPolicy(w, r, &request) {
		return
	}
	if request.UID == "" || request.ResourceVersion == "" {
		writeError(w, http.StatusPreconditionRequired, "version_required", "uid and resourceVersion from the current Agent are required")
		return
	}
	if request.PolicyClass != nil && *request.PolicyClass != cetirunv1alpha1.AgentPolicyClassNormal && *request.PolicyClass != cetirunv1alpha1.AgentPolicyClassAdmin {
		writeError(w, 400, "invalid_policy_class", "policyClass must be normal or admin")
		return
	}
	for _, value := range []*string{request.Team, request.SandboxRef, request.MonthlyBudgetUSD} {
		if value != nil {
			*value = strings.TrimSpace(*value)
		}
	}
	current, err := h.Kube.GetInstanceByName(r.Context(), name)
	if err != nil {
		h.writeObject(w, nil, err)
		return
	}
	if current.UID != "" && (string(current.UID) != request.UID || current.ResourceVersion != request.ResourceVersion) {
		h.writeObject(w, nil, kube.ErrInstanceChanged)
		return
	}
	targetTeam := current.Spec.Team
	if request.Team != nil {
		targetTeam = *request.Team
	}
	targetSandbox := current.Spec.SandboxRef
	if request.SandboxRef != nil {
		targetSandbox = *request.SandboxRef
	}
	if targetTeam == "" || targetSandbox == "" {
		writeError(w, 400, "invalid_binding", "team and sandboxRef must be non-empty")
		return
	}
	team, err := h.Kube.GetTeam(r.Context(), targetTeam)
	if err != nil {
		h.writeObject(w, nil, err)
		return
	}
	if !slices.Contains(team.Spec.SandboxRefs, targetSandbox) {
		writeError(w, 400, "sandbox_not_allowed", "sandboxRef is not permitted by the target Team")
		return
	}
	update := kube.InstancePolicyUpdate{ExpectedUID: request.UID, ExpectedResourceVersion: request.ResourceVersion, Team: request.Team, PolicyClass: request.PolicyClass, SandboxRef: request.SandboxRef, MonthlyBudgetUSD: request.MonthlyBudgetUSD}
	item, err := h.Kube.UpdateInstancePolicy(r.Context(), name, update)
	h.writeObject(w, item, err)
}

func decodeStrictPolicy(w http.ResponseWriter, r *http.Request, target any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, 415, "invalid_content_type", "Administrative requests must use application/json")
		return false
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil || !json.Valid(data) {
		writeError(w, 400, "invalid_body", "Invalid JSON body")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, 400, "invalid_body", "Invalid JSON body")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, 400, "invalid_body", "Invalid JSON body")
		return false
	}
	return true
}

func (h *AdminPolicyHandler) writeObject(w http.ResponseWriter, item any, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, item)
		return
	}
	switch {
	case errors.Is(err, kube.ErrPolicyResourceChanged), kube.IsConflict(err):
		writeError(w, 409, "resource_changed", kube.ErrPolicyResourceChanged.Error())
	case errors.Is(err, kube.ErrInstanceChanged):
		writeError(w, 409, "instance_changed", "Agent changed or was replaced; reload before saving")
	case errors.Is(err, kube.ErrPolicyChangeRequiresSuspension):
		writeError(w, 409, "suspension_required", err.Error())
	case kube.IsNotFound(err):
		writeError(w, 404, "not_found", "Policy resource not found")
	case kube.IsAlreadyExists(err):
		writeError(w, 409, "already_exists", "Policy resource already exists")
	default:
		writeError(w, 500, "internal_error", fmt.Sprintf("Policy operation failed: %v", err))
	}
}
func (h *AdminPolicyHandler) delete(w http.ResponseWriter, err error) {
	if err != nil {
		h.writeObject(w, nil, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
