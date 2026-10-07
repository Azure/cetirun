// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CetiAdminHandler accepts only narrow operational proposals authenticated by
// a Kubernetes-projected ServiceAccount token. It never accepts a caller
// supplied requester, projection revisions, approval or target UID.
type CetiAdminHandler struct {
	Kube                   *kube.Client
	Audience               string
	AllowedServiceAccounts map[string]struct{}
	Now                    func() time.Time
}

func NewCetiAdminHandler(k *kube.Client, audience string, accounts []string) *CetiAdminHandler {
	allowed := map[string]struct{}{}
	for _, account := range accounts {
		if account = strings.TrimSpace(account); account != "" {
			allowed[account] = struct{}{}
		}
	}
	return &CetiAdminHandler{Kube: k, Audience: audience, AllowedServiceAccounts: allowed}
}
func (h *CetiAdminHandler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

type cetiAdminActionRequest struct {
	Target           cetiAdminActionTarget                     `json:"target"`
	Operation        cetirunv1alpha1.GovernanceActionOperation `json:"operation"`
	Reason           string                                    `json:"reason"`
	ExpiresInSeconds int64                                     `json:"expiresInSeconds,omitempty"`
}

// cetiAdminActionTarget is purposefully a typed reference, rather than a
// string, to prevent this bounded endpoint becoming a generic resource-action
// API as other governance targets are added.
type cetiAdminActionTarget struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

const (
	cetiAdminIdempotencyKeyHeader = "Idempotency-Key"
	cetiAdminActionNamePrefix     = "governanceaction-"
)

// cetiAdminGovernanceObservation is deliberately not an Agent or
// GovernanceStatus serialization. This internal endpoint is used by
// CetiAdmin for a bounded operational decision, so its response must stay
// limited to non-sensitive lifecycle and governance evidence even if either
// source API grows additional fields in the future.
type cetiAdminGovernanceObservation struct {
	Name       string                              `json:"name"`
	Phase      string                              `json:"phase,omitempty"`
	Governance cetiAdminGovernanceObservationState `json:"governance"`
	Gateway    *cetiAdminGatewayObservation        `json:"gateway,omitempty"`
}

type cetiAdminGovernanceObservationState struct {
	Phase       string                          `json:"phase,omitempty"`
	Coverage    string                          `json:"coverage,omitempty"`
	Conditions  []cetiAdminRedactedCondition    `json:"conditions,omitempty"`
	Projections cetiAdminProjectionObservations `json:"projections"`
}

// cetiAdminRedactedCondition intentionally excludes Condition.Message. A
// condition message is free-form text and can contain operational details
// which CetiAdmin does not need to make a bounded governance request.
type cetiAdminRedactedCondition struct {
	Type   string                 `json:"type"`
	Status metav1.ConditionStatus `json:"status"`
	Reason string                 `json:"reason,omitempty"`
}

type cetiAdminProjectionObservations struct {
	SandboxRevision          string `json:"sandboxRevision,omitempty"`
	GatewayPolicyRevision    string `json:"gatewayPolicyRevision,omitempty"`
	CredentialPolicyRevision string `json:"credentialPolicyRevision,omitempty"`
}

type cetiAdminGatewayObservation struct {
	Phase            string       `json:"phase,omitempty"`
	Blocked          bool         `json:"blocked"`
	BudgetUSD        string       `json:"budgetUSD,omitempty"`
	BudgetDuration   string       `json:"budgetDuration,omitempty"`
	SpendUSD         string       `json:"spendUSD,omitempty"`
	BudgetResetAt    *metav1.Time `json:"budgetResetAt,omitempty"`
	LastVerifiedTime *metav1.Time `json:"lastVerifiedTime,omitempty"`
}

func (h *CetiAdminHandler) authenticate(r *http.Request) (string, bool) {
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if token == "" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return "", false
	}
	username, authenticated, err := h.Kube.ReviewServiceAccountToken(r.Context(), token, h.Audience)
	if err != nil || !authenticated {
		return "", false
	}
	_, ok := h.AllowedServiceAccounts[username]
	return username, ok
}

func (h *CetiAdminHandler) Submit(w http.ResponseWriter, r *http.Request) {
	requester, ok := h.authenticate(r)
	if !ok {
		writeError(w, 403, "cetiadmin_unauthorized", "A trusted CetiAdmin projected service account token is required")
		return
	}
	idempotencyKey, ok := cetiAdminIdempotencyKey(r.Header.Get(cetiAdminIdempotencyKeyHeader))
	if !ok {
		writeError(w, 400, "invalid_idempotency_key", "Idempotency-Key must be 1 to 128 printable ASCII characters")
		return
	}
	var request cetiAdminActionRequest
	if !decodeStrictGovernance(w, r, &request) {
		return
	}
	if request.Target.Kind != "Agent" || !teamNamePattern.MatchString(request.Target.Name) || strings.TrimSpace(request.Reason) == "" || len(request.Reason) > 1024 {
		writeError(w, 400, "invalid_action", "target.kind Agent, target.name, and a reason of at most 1024 characters are required")
		return
	}
	if request.Operation != cetirunv1alpha1.GovernanceActionSuspend && request.Operation != cetirunv1alpha1.GovernanceActionResume && request.Operation != cetirunv1alpha1.GovernanceActionRestart && request.Operation != cetirunv1alpha1.GovernanceActionReconcile {
		writeError(w, 400, "invalid_action", "operation must be Suspend, Resume, Restart or Reconcile")
		return
	}
	ttl := request.ExpiresInSeconds
	if ttl == 0 {
		ttl = 900
	}
	if ttl < 60 || ttl > 3600 {
		writeError(w, 400, "invalid_expiry", "expiresInSeconds must be between 60 and 3600")
		return
	}
	actionName := cetiAdminActionName(requester, idempotencyKey)
	if existing, err := h.Kube.GetGovernanceAction(r.Context(), actionName); err == nil {
		if cetiAdminActionMatchesRequest(existing, requester, request, ttl) {
			writeJSON(w, http.StatusOK, existing)
			return
		}
		writeError(w, http.StatusConflict, "idempotency_key_reused", "Idempotency-Key was already used for a different governance action")
		return
	} else if !kube.IsNotFound(err) {
		writeError(w, 500, "internal_error", "Unable to read existing governance action")
		return
	}
	agent, err := h.Kube.GetInstanceByName(r.Context(), request.Target.Name)
	if err != nil {
		if kube.IsNotFound(err) {
			writeError(w, 404, "agent_not_found", "Agent not found")
		} else {
			writeError(w, 500, "internal_error", "Unable to read target Agent")
		}
		return
	}
	governance := agent.Status.Governance
	if governance == nil || governance.ObservedGeneration != agent.Generation || governance.Authorization == nil || governance.Authorization.Revision == "" {
		writeError(w, 409, "governance_not_authorized", "Target Agent has no current governance authorization snapshot")
		return
	}
	// A Suspend proposal is the phase-1 emergency fail-closed operation and is
	// useful while workload execution remains held. Operations that could start
	// or roll compute require full phase-2 enforcement evidence.
	if request.Operation != cetirunv1alpha1.GovernanceActionSuspend && governance.Phase != "Ready" {
		writeError(w, 409, "governance_not_ready", "Target Agent governance is not ready for this operation")
		return
	}
	now := h.now().UTC()
	projections := cetirunv1alpha1.GovernanceActionProjections{AuthorizationRevision: governance.Authorization.Revision, SandboxRevision: governance.Projections.Sandbox.ExpectedRevision, GatewayPolicyRevision: governance.Projections.Gateway.ExpectedRevision, CredentialPolicyRevision: governance.Projections.CredentialPolicy.ExpectedRevision}
	action := &cetirunv1alpha1.GovernanceAction{ObjectMeta: metav1.ObjectMeta{Name: actionName}, Spec: cetirunv1alpha1.GovernanceActionSpec{Requester: cetirunv1alpha1.GovernanceActionRequester{Kind: "CetiAdmin", Subject: requester}, TargetRef: cetirunv1alpha1.GovernanceActionTargetRef{APIVersion: "cetirun.microsoft.com/v1alpha1", Kind: "Agent", Name: agent.Name, UID: agent.UID, Generation: agent.Generation}, Operation: request.Operation, ExpectedProjections: projections, Reason: strings.TrimSpace(request.Reason), RequestedAt: metav1.NewTime(now), ExpiresAt: metav1.NewTime(now.Add(time.Duration(ttl) * time.Second))}}
	created, err := h.Kube.CreateGovernanceAction(r.Context(), action)
	if err != nil {
		if kube.IsAlreadyExists(err) {
			existing, getErr := h.Kube.GetGovernanceAction(r.Context(), actionName)
			if getErr == nil && cetiAdminActionMatchesRequest(existing, requester, request, ttl) {
				writeJSON(w, http.StatusOK, existing)
				return
			}
			if getErr == nil {
				writeError(w, http.StatusConflict, "idempotency_key_reused", "Idempotency-Key was already used for a different governance action")
				return
			}
		}
		writeError(w, 500, "internal_error", "Unable to create governance action")
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// cetiAdminIdempotencyKey rejects whitespace and control characters so the
// header cannot be transformed differently by proxies. The key is used only
// to derive an object name; it is never persisted in a Kubernetes object.
func cetiAdminIdempotencyKey(key string) (string, bool) {
	if len(key) == 0 || len(key) > 128 {
		return "", false
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x21 || key[i] > 0x7e {
			return "", false
		}
	}
	return key, true
}

func cetiAdminActionName(requester, key string) string {
	sum := sha256.Sum256([]byte(requester + "\x00" + key))
	// 17-byte prefix plus 23 hash bytes (46 hex characters) is a DNS label
	// within Kubernetes' 63-character metadata.name limit.
	return cetiAdminActionNamePrefix + hex.EncodeToString(sum[:23])
}

func cetiAdminActionMatchesRequest(item *cetirunv1alpha1.GovernanceAction, requester string, request cetiAdminActionRequest, ttl int64) bool {
	return item.Spec.Requester.Kind == "CetiAdmin" &&
		item.Spec.Requester.Subject == requester &&
		item.Spec.TargetRef.APIVersion == "cetirun.microsoft.com/v1alpha1" &&
		item.Spec.TargetRef.Kind == "Agent" &&
		item.Spec.TargetRef.Name == request.Target.Name &&
		item.Spec.Operation == request.Operation &&
		item.Spec.Reason == strings.TrimSpace(request.Reason) &&
		item.Spec.ExpiresAt.Sub(item.Spec.RequestedAt.Time) == time.Duration(ttl)*time.Second
}

func (h *CetiAdminHandler) Get(w http.ResponseWriter, r *http.Request) {
	requester, ok := h.authenticate(r)
	if !ok {
		writeError(w, 403, "cetiadmin_unauthorized", "A trusted CetiAdmin projected service account token is required")
		return
	}
	name, ok := governanceObjectName(r)
	if !ok {
		writeError(w, 400, "invalid_name", "Invalid governance action name")
		return
	}
	item, err := h.Kube.GetGovernanceAction(r.Context(), name)
	if err != nil {
		if kube.IsNotFound(err) {
			writeError(w, 404, "not_found", "Governance action not found")
		} else {
			writeError(w, 500, "internal_error", "Unable to read governance action")
		}
		return
	}
	// An allowlisted ServiceAccount is allowed to submit and read only its own
	// proposals. Do not let one trusted workload inspect another workload's
	// reasons, target references, or approval state.
	if item.Spec.Requester.Kind != "CetiAdmin" || item.Spec.Requester.Subject != requester {
		writeError(w, 404, "not_found", "Governance action not found")
		return
	}
	writeJSON(w, 200, item)
}

// GetAgentGovernanceObservation returns the small, redacted governance
// observation required by CetiAdmin. It is intentionally served only from the
// internal mux and uses the same projected-ServiceAccount authentication as
// governance action submission.
func (h *CetiAdminHandler) GetAgentGovernanceObservation(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticate(r); !ok {
		writeError(w, 403, "cetiadmin_unauthorized", "A trusted CetiAdmin projected service account token is required")
		return
	}
	name := r.PathValue("name")
	if !teamNamePattern.MatchString(name) {
		writeError(w, 400, "invalid_name", "Invalid Agent name")
		return
	}
	agent, err := h.Kube.GetInstanceByName(r.Context(), name)
	if err != nil {
		if kube.IsNotFound(err) {
			writeError(w, 404, "agent_not_found", "Agent not found")
		} else {
			writeError(w, 500, "internal_error", "Unable to read target Agent")
		}
		return
	}
	writeJSON(w, http.StatusOK, newCetiAdminGovernanceObservation(agent))
}

func newCetiAdminGovernanceObservation(agent *cetirunv1alpha1.Agent) cetiAdminGovernanceObservation {
	observation := cetiAdminGovernanceObservation{Name: agent.Name, Phase: agent.Status.Phase}
	for _, condition := range agent.Status.Conditions {
		observation.Governance.Conditions = append(observation.Governance.Conditions, cetiAdminRedactedCondition{
			Type: condition.Type, Status: condition.Status, Reason: condition.Reason,
		})
	}
	if governance := agent.Status.Governance; governance != nil {
		observation.Governance.Phase = governance.Phase
		observation.Governance.Coverage = governance.Coverage
		observation.Governance.Projections = cetiAdminProjectionObservations{
			SandboxRevision:          governance.Projections.Sandbox.ExpectedRevision,
			GatewayPolicyRevision:    governance.Projections.Gateway.ExpectedRevision,
			CredentialPolicyRevision: governance.Projections.CredentialPolicy.ExpectedRevision,
		}
	}
	if gateway := agent.Status.AIGateway; gateway != nil {
		observation.Gateway = &cetiAdminGatewayObservation{
			Phase: gateway.Phase, Blocked: gateway.Blocked, BudgetUSD: gateway.BudgetUSD,
			BudgetDuration: gateway.BudgetDuration, SpendUSD: gateway.SpendUSD,
			BudgetResetAt: gateway.BudgetResetAt, LastVerifiedTime: gateway.LastVerifiedTime,
		}
	}
	return observation
}
