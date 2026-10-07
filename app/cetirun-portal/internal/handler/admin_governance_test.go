// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/auth"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func governancePatchFixture() (*AdminGovernanceHandler, client.Client, *cetirunv1alpha1.Agent) {
	agent := sampleInstance()
	agent.Name = "alice-work"
	agent.Spec.PolicyClass = cetirunv1alpha1.AgentPolicyClassNormal
	policy := &cetirunv1alpha1.AgentPolicy{ObjectMeta: metav1.ObjectMeta{Name: "normal", UID: "policy-normal"}}
	sandbox := &cetirunv1alpha1.Sandbox{ObjectMeta: metav1.ObjectMeta{Name: "standard", UID: "sandbox-standard"}}
	teams := []client.Object{
		&cetirunv1alpha1.Team{ObjectMeta: metav1.ObjectMeta{Name: "aks", UID: "team-aks"}, Spec: cetirunv1alpha1.TeamSpec{
			NormalAgentPolicyRef: "normal", SandboxRefs: []string{"standard"},
		}},
		&cetirunv1alpha1.Team{ObjectMeta: metav1.ObjectMeta{Name: "platform", UID: "team-platform"}, Spec: cetirunv1alpha1.TeamSpec{
			NormalAgentPolicyRef: "normal", SandboxRefs: []string{"standard"}, Membership: cetirunv1alpha1.TeamMembershipSpec{
				Subjects: []cetirunv1alpha1.GovernanceSubject{{Kind: "User", TenantID: managementTenant, ObjectID: managementOwner}},
			},
		}},
	}
	objects := append([]client.Object{agent, policy, sandbox}, teams...)
	fc := newFakeClient(objects...)
	return &AdminGovernanceHandler{Kube: kube.NewClient(fc), Now: func() time.Time { return time.Unix(100, 0).UTC() }}, fc, agent
}

func governancePatchRequest(body any, claims *auth.Claims) (*http.Request, *httptest.ResponseRecorder) {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/instances/alice-work/governance", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("name", "alice-work")
	if claims != nil {
		req = req.WithContext(auth.NewContext(req.Context(), *claims))
	}
	return req, httptest.NewRecorder()
}

func TestUpdateInstanceGovernanceRequiresCurrentIdentity(t *testing.T) {
	for _, body := range []map[string]any{
		{"resourceVersion": "1", "suspended": true},
		{"uid": "agent-alice", "suspended": true},
	} {
		h, _, _ := governancePatchFixture()
		req, response := governancePatchRequest(body, nil)
		h.UpdateInstanceGovernance(response, req)
		if response.Code != http.StatusPreconditionRequired {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	}
}

func TestUpdateInstanceGovernanceRejectsStaleVersion(t *testing.T) {
	h, _, agent := governancePatchFixture()
	req, response := governancePatchRequest(map[string]any{
		"uid": string(agent.UID), "resourceVersion": "stale", "suspended": true,
	}, nil)
	h.UpdateInstanceGovernance(response, req)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestUpdateInstanceGovernanceRequiresStoppedTransition(t *testing.T) {
	h, _, agent := governancePatchFixture()
	req, response := governancePatchRequest(map[string]any{
		"uid": string(agent.UID), "resourceVersion": agent.ResourceVersion, "gatewayBudgetUSD": "10",
	}, nil)
	h.UpdateInstanceGovernance(response, req)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestUpdateInstanceGovernanceRebindRequiresOwnerMembership(t *testing.T) {
	h, _, agent := governancePatchFixture()
	claims := auth.Claims{TenantID: managementTenant, ObjectID: "not-the-owner"}
	req, response := governancePatchRequest(map[string]any{
		"uid": string(agent.UID), "resourceVersion": agent.ResourceVersion, "team": "platform", "suspended": true,
	}, &claims)
	h.UpdateInstanceGovernance(response, req)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestUpdateInstanceGovernanceWritesFreshUIDBoundReauthorization(t *testing.T) {
	h, fc, agent := governancePatchFixture()
	agent.Spec.Suspended = true
	if err := fc.Update(t.Context(), agent); err != nil {
		t.Fatal(err)
	}
	claims := auth.Claims{TenantID: managementTenant, ObjectID: managementOwner}
	req, response := governancePatchRequest(map[string]any{
		"uid": string(agent.UID), "resourceVersion": agent.ResourceVersion, "team": "platform", "suspended": true,
	}, &claims)
	h.UpdateInstanceGovernance(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	updated := &cetirunv1alpha1.Agent{}
	if err := fc.Get(t.Context(), client.ObjectKey{Name: agent.Name}, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Spec.Team != "platform" || updated.Spec.Governance.TeamAuthorization.Subject.ObjectID != managementOwner {
		t.Fatalf("governance binding was not updated: %+v", updated.Spec)
	}
	var intent struct {
		RequestID string `json:"requestID"`
		Binding   struct {
			AgentUID, TeamUID, SandboxUID, AgentPolicyUID string
			PolicyClass                                   cetirunv1alpha1.AgentPolicyClass
		} `json:"binding"`
	}
	if err := json.Unmarshal([]byte(updated.Annotations["cetirun.microsoft.com/sandbox-reauthorize"]), &intent); err != nil {
		t.Fatal(err)
	}
	if intent.RequestID == "" || intent.Binding.AgentUID != string(agent.UID) || intent.Binding.TeamUID != "team-platform" ||
		intent.Binding.SandboxUID != "sandbox-standard" || intent.Binding.AgentPolicyUID != "policy-normal" ||
		intent.Binding.PolicyClass != cetirunv1alpha1.AgentPolicyClassNormal {
		t.Fatalf("reauthorization is not bound to the selected object UIDs: %+v", intent)
	}
}

func TestGovernanceActionDigestBindsImmutableProposalButNotApproval(t *testing.T) {
	spec := cetirunv1alpha1.GovernanceActionSpec{
		Requester:           cetirunv1alpha1.GovernanceActionRequester{Kind: "CetiAdmin", Subject: "system:serviceaccount:cetirun-system:cetiadmin"},
		TargetRef:           cetirunv1alpha1.GovernanceActionTargetRef{APIVersion: "cetirun.microsoft.com/v1alpha1", Kind: "Agent", Name: "example", UID: types.UID("agent-uid"), Generation: 7},
		Operation:           cetirunv1alpha1.GovernanceActionSuspend,
		ExpectedProjections: cetirunv1alpha1.GovernanceActionProjections{AuthorizationRevision: "authorization-revision"},
		Reason:              "operator observed a bounded failure",
		RequestedAt:         metav1.NewTime(time.Unix(1, 0)), ExpiresAt: metav1.NewTime(time.Unix(2, 0)),
	}
	first, err := cetirunv1alpha1.GovernanceActionDigest(spec)
	if err != nil || first == "" {
		t.Fatalf("GovernanceActionDigest() = %q, %v", first, err)
	}
	spec.Approval = &cetirunv1alpha1.GovernanceActionApproval{Decision: "Approved"}
	second, err := cetirunv1alpha1.GovernanceActionDigest(spec)
	if err != nil || first != second {
		t.Fatalf("approval must not change action digest: %q != %q (%v)", first, second, err)
	}
	spec.TargetRef.Generation++
	third, err := cetirunv1alpha1.GovernanceActionDigest(spec)
	if err != nil || first == third {
		t.Fatalf("target generation must change action digest: %q == %q (%v)", first, third, err)
	}
}

func TestCetiAdminAllowlistFailsClosed(t *testing.T) {
	h := NewCetiAdminHandler(nil, "cetirun-governance-actions", nil)
	if _, ok := h.AllowedServiceAccounts["system:serviceaccount:cetirun-system:cetiadmin"]; ok {
		t.Fatal("an empty CetiAdmin allowlist must not authorize any service account")
	}
}
