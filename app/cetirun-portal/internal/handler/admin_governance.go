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
	"time"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/auth"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AdminGovernanceHandler owns the cluster-scoped governance control-plane
// endpoints. It is mounted exclusively behind RequireAdmin.
type AdminGovernanceHandler struct {
	Kube *kube.Client
	Now  func() time.Time
}

func (h *AdminGovernanceHandler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func governanceObjectName(r *http.Request) (string, bool) {
	name := r.PathValue("name")
	if !teamNamePattern.MatchString(name) {
		return "", false
	}
	return name, true
}

func (h *AdminGovernanceHandler) ListAgentPolicies(w http.ResponseWriter, r *http.Request) {
	h.list(w, r, func() (any, error) { return h.Kube.ListAgentPolicies(r.Context()) })
}
func (h *AdminGovernanceHandler) GetAgentPolicy(w http.ResponseWriter, r *http.Request) {
	name, ok := governanceObjectName(r)
	if !ok {
		writeError(w, 400, "invalid_name", "Invalid policy name")
		return
	}
	item, err := h.Kube.GetAgentPolicy(r.Context(), name)
	h.writeObject(w, item, err)
}
func (h *AdminGovernanceHandler) CreateAgentPolicy(w http.ResponseWriter, r *http.Request) {
	var item cetirunv1alpha1.AgentPolicy
	if !decodeStrictGovernance(w, r, &item) || !teamNamePattern.MatchString(item.Name) {
		if item.Name != "" {
			writeError(w, 400, "invalid_name", "Invalid policy name")
		}
		return
	}
	created, err := h.Kube.CreateAgentPolicy(r.Context(), &item)
	h.writeCreated(w, created, err)
}
func (h *AdminGovernanceHandler) UpdateAgentPolicy(w http.ResponseWriter, r *http.Request) {
	name, ok := governanceObjectName(r)
	if !ok {
		writeError(w, 400, "invalid_name", "Invalid policy name")
		return
	}
	var req struct {
		Spec cetirunv1alpha1.AgentPolicySpec `json:"spec"`
	}
	if !decodeStrictGovernance(w, r, &req) {
		return
	}
	item, err := h.Kube.UpdateAgentPolicy(r.Context(), name, req.Spec)
	h.writeObject(w, item, err)
}
func (h *AdminGovernanceHandler) DeleteAgentPolicy(w http.ResponseWriter, r *http.Request) {
	name, ok := governanceObjectName(r)
	if !ok {
		writeError(w, 400, "invalid_name", "Invalid policy name")
		return
	}
	h.delete(w, h.Kube.DeleteAgentPolicy(r.Context(), name))
}

func (h *AdminGovernanceHandler) ListSandboxes(w http.ResponseWriter, r *http.Request) {
	h.list(w, r, func() (any, error) { return h.Kube.ListSandboxes(r.Context()) })
}
func (h *AdminGovernanceHandler) GetSandbox(w http.ResponseWriter, r *http.Request) {
	name, ok := governanceObjectName(r)
	if !ok {
		writeError(w, 400, "invalid_name", "Invalid sandbox name")
		return
	}
	item, err := h.Kube.GetSandbox(r.Context(), name)
	h.writeObject(w, item, err)
}
func (h *AdminGovernanceHandler) CreateSandbox(w http.ResponseWriter, r *http.Request) {
	var item cetirunv1alpha1.Sandbox
	if !decodeStrictGovernance(w, r, &item) || !teamNamePattern.MatchString(item.Name) {
		if item.Name != "" {
			writeError(w, 400, "invalid_name", "Invalid sandbox name")
		}
		return
	}
	created, err := h.Kube.CreateSandbox(r.Context(), &item)
	h.writeCreated(w, created, err)
}
func (h *AdminGovernanceHandler) UpdateSandbox(w http.ResponseWriter, r *http.Request) {
	name, ok := governanceObjectName(r)
	if !ok {
		writeError(w, 400, "invalid_name", "Invalid sandbox name")
		return
	}
	var req struct {
		Spec cetirunv1alpha1.SandboxSpec `json:"spec"`
	}
	if !decodeStrictGovernance(w, r, &req) {
		return
	}
	item, err := h.Kube.UpdateSandbox(r.Context(), name, req.Spec)
	h.writeObject(w, item, err)
}
func (h *AdminGovernanceHandler) DeleteSandbox(w http.ResponseWriter, r *http.Request) {
	name, ok := governanceObjectName(r)
	if !ok {
		writeError(w, 400, "invalid_name", "Invalid sandbox name")
		return
	}
	h.delete(w, h.Kube.DeleteSandbox(r.Context(), name))
}

func (h *AdminGovernanceHandler) ListGovernanceActions(w http.ResponseWriter, r *http.Request) {
	h.list(w, r, func() (any, error) { return h.Kube.ListGovernanceActions(r.Context()) })
}
func (h *AdminGovernanceHandler) GetGovernanceAction(w http.ResponseWriter, r *http.Request) {
	name, ok := governanceObjectName(r)
	if !ok {
		writeError(w, 400, "invalid_name", "Invalid governance action name")
		return
	}
	item, err := h.Kube.GetGovernanceAction(r.Context(), name)
	h.writeObject(w, item, err)
}

func (h *AdminGovernanceHandler) DecideGovernanceAction(w http.ResponseWriter, r *http.Request) {
	name, ok := governanceObjectName(r)
	if !ok {
		writeError(w, 400, "invalid_name", "Invalid governance action name")
		return
	}
	var req struct {
		Decision string `json:"decision"`
	}
	if !decodeStrictGovernance(w, r, &req) {
		return
	}
	if req.Decision != "Approved" && req.Decision != "Denied" {
		writeError(w, 400, "invalid_decision", "decision must be Approved or Denied")
		return
	}
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok || claims.TenantID == "" || claims.ObjectID == "" {
		writeError(w, 403, "verified_admin_required", "A verified platform administrator is required")
		return
	}
	item, err := h.Kube.GetGovernanceAction(r.Context(), name)
	if err != nil {
		h.writeObject(w, nil, err)
		return
	}
	if item.Spec.Approval != nil {
		writeError(w, 409, "already_decided", "This action has already been decided")
		return
	}
	if item.Status.Phase != "" && item.Status.Phase != "Proposed" && item.Status.Phase != "AwaitingApproval" {
		writeError(w, http.StatusConflict, "action_not_approvable", "This action is no longer awaiting approval")
		return
	}
	if !item.Spec.ExpiresAt.After(h.now()) {
		writeError(w, 409, "action_expired", "This action has expired")
		return
	}
	digest, err := cetirunv1alpha1.GovernanceActionDigest(item.Spec)
	if err != nil {
		writeError(w, 500, "internal_error", "Unable to digest action")
		return
	}
	approval := cetirunv1alpha1.GovernanceActionApproval{Decision: req.Decision, Approver: cetirunv1alpha1.GovernanceSubject{Kind: "User", TenantID: claims.TenantID, ObjectID: claims.ObjectID}, ActionDigest: digest, ApprovedAt: metav1.NewTime(h.now().UTC()), ExpiresAt: item.Spec.ExpiresAt}
	updated, err := h.Kube.ApproveGovernanceAction(r.Context(), name, approval)
	h.writeObject(w, updated, err)
}

// UpdateInstanceGovernance is the only Portal route that may set an Agent's
// administrator-controlled policy class, budget override, or suspension.
func (h *AdminGovernanceHandler) UpdateInstanceGovernance(w http.ResponseWriter, r *http.Request) {
	name, ok := governanceObjectName(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_name", "Invalid Agent name")
		return
	}
	var req struct {
		UID              string                            `json:"uid"`
		ResourceVersion  string                            `json:"resourceVersion"`
		Team             *string                           `json:"team,omitempty"`
		PolicyClass      *cetirunv1alpha1.AgentPolicyClass `json:"policyClass,omitempty"`
		SandboxRef       *string                           `json:"sandboxRef,omitempty"`
		GatewayBudgetUSD *string                           `json:"gatewayBudgetUSD,omitempty"`
		Suspended        *bool                             `json:"suspended,omitempty"`
	}
	if !decodeStrictGovernance(w, r, &req) {
		return
	}
	if req.UID == "" || req.ResourceVersion == "" {
		writeError(w, http.StatusPreconditionRequired, "version_required", "uid and resourceVersion from the current Agent are required")
		return
	}
	if req.PolicyClass != nil && *req.PolicyClass != cetirunv1alpha1.AgentPolicyClassNormal && *req.PolicyClass != cetirunv1alpha1.AgentPolicyClassAdmin {
		writeError(w, 400, "invalid_policy_class", "policyClass must be normal or admin")
		return
	}
	if req.Team != nil {
		*req.Team = strings.TrimSpace(*req.Team)
	}
	if req.SandboxRef != nil {
		*req.SandboxRef = strings.TrimSpace(*req.SandboxRef)
	}
	if req.GatewayBudgetUSD != nil {
		*req.GatewayBudgetUSD = strings.TrimSpace(*req.GatewayBudgetUSD)
	}
	current, err := h.Kube.GetInstanceByName(r.Context(), name)
	if err != nil {
		h.writeObject(w, nil, err)
		return
	}
	if string(current.UID) != req.UID || current.ResourceVersion != req.ResourceVersion {
		h.writeObject(w, nil, kube.ErrInstanceChanged)
		return
	}
	claims, claimsOK := auth.ClaimsFromContext(r.Context())
	update := kube.InstanceGovernanceUpdate{
		ExpectedUID: req.UID, ExpectedResourceVersion: req.ResourceVersion, Team: req.Team,
		PolicyClass: req.PolicyClass, SandboxRef: req.SandboxRef, GatewayBudgetUSD: req.GatewayBudgetUSD,
		Suspended: req.Suspended,
	}
	targetTeam := current.Spec.Team
	if req.Team != nil {
		targetTeam = *req.Team
	}
	if targetTeam == "" || (req.SandboxRef != nil && *req.SandboxRef == "") {
		writeError(w, http.StatusBadRequest, "invalid_binding", "team and sandboxRef must be non-empty")
		return
	}
	team, err := h.Kube.GetTeam(r.Context(), targetTeam)
	if err != nil {
		h.writeObject(w, nil, err)
		return
	}
	targetSandbox := current.Spec.SandboxRef
	if req.SandboxRef != nil {
		targetSandbox = *req.SandboxRef
	}
	if !slices.Contains(team.Spec.SandboxRefs, targetSandbox) {
		writeError(w, http.StatusBadRequest, "sandbox_not_allowed", "sandboxRef is not permitted by the target Team")
		return
	}
	if targetTeam != current.Spec.Team {
		if !claimsOK || !ownsInstance(current, claims) {
			writeError(w, http.StatusForbidden, "owner_proof_required", "Team rebinding requires the verified Agent owner")
			return
		}
		subject, ok := governanceSubjectForClaims(team, claims)
		if !ok {
			writeError(w, http.StatusForbidden, "team_membership_required", "The verified owner is not a member of the target Team")
			return
		}
		update.TeamAuthorization = &cetirunv1alpha1.TeamAuthorization{Subject: subject, AuthorizedAt: metav1.NewTime(h.now().UTC())}
	}
	item, err := h.Kube.UpdateInstanceGovernance(r.Context(), name, update)
	h.writeObject(w, item, err)
}

func governanceSubjectForClaims(team *cetirunv1alpha1.Team, claims auth.Claims) (cetirunv1alpha1.GovernanceSubject, bool) {
	for _, subject := range team.Spec.Membership.Subjects {
		if !strings.EqualFold(subject.TenantID, claims.TenantID) {
			continue
		}
		if subject.Kind == "User" && strings.EqualFold(subject.ObjectID, claims.ObjectID) {
			return subject, true
		}
		if subject.Kind == "Group" && slices.ContainsFunc(claims.Groups, func(group string) bool {
			return strings.EqualFold(group, subject.ObjectID)
		}) {
			return subject, true
		}
	}
	return cetirunv1alpha1.GovernanceSubject{}, false
}

func decodeStrictGovernance(w http.ResponseWriter, r *http.Request, target any) bool {
	data, err := readGovernanceBody(w, r)
	if err != nil {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		writeError(w, 400, "invalid_body", "Invalid JSON body")
		return false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, 400, "invalid_body", "Invalid JSON body")
		return false
	}
	return true
}
func readGovernanceBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, 415, "invalid_content_type", "Governance requests must use application/json")
		return nil, fmt.Errorf("content type")
	}
	var body bytes.Buffer
	if _, err := body.ReadFrom(http.MaxBytesReader(w, r.Body, 64<<10)); err != nil {
		writeError(w, 400, "invalid_body", "Request body is invalid or exceeds 64 KiB")
		return nil, err
	}
	if !json.Valid(body.Bytes()) {
		writeError(w, 400, "invalid_body", "Invalid JSON body")
		return nil, fmt.Errorf("invalid JSON")
	}
	return body.Bytes(), nil
}
func (h *AdminGovernanceHandler) list(w http.ResponseWriter, _ *http.Request, run func() (any, error)) {
	item, err := run()
	h.writeObject(w, item, err)
}
func (h *AdminGovernanceHandler) writeCreated(w http.ResponseWriter, item any, err error) {
	if err != nil {
		h.writeObject(w, nil, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}
func (h *AdminGovernanceHandler) writeObject(w http.ResponseWriter, item any, err error) {
	if err != nil {
		if errors.Is(err, kube.ErrInstanceChanged) {
			writeError(w, http.StatusConflict, "instance_changed", "Agent changed or was replaced; reload before saving")
			return
		}
		if errors.Is(err, kube.ErrGovernanceRequiresSuspension) {
			writeError(w, http.StatusConflict, "suspension_required", kube.ErrGovernanceRequiresSuspension.Error())
			return
		}
		if kube.IsNotFound(err) {
			writeError(w, 404, "not_found", "Governance resource not found")
			return
		}
		if kube.IsAlreadyExists(err) {
			writeError(w, 409, "already_exists", "Governance resource already exists")
			return
		}
		writeError(w, 500, "internal_error", "Governance operation failed")
		return
	}
	writeJSON(w, http.StatusOK, item)
}
func (h *AdminGovernanceHandler) delete(w http.ResponseWriter, err error) {
	if err != nil {
		h.writeObject(w, nil, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
