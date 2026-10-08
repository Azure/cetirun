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
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func policyPatchFixture() (*AdminPolicyHandler, client.Client, *cetirunv1alpha1.Agent) {
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
				Subjects: []cetirunv1alpha1.EntraSubject{{Kind: "User", TenantID: managementTenant, ObjectID: managementOwner}},
			},
		}},
	}
	objects := append([]client.Object{agent, policy, sandbox}, teams...)
	fc := newFakeClient(objects...)
	return &AdminPolicyHandler{Kube: kube.NewClient(fc), Now: func() time.Time { return time.Unix(100, 0).UTC() }}, fc, agent
}

func policyPatchRequest(body any, claims *auth.Claims) (*http.Request, *httptest.ResponseRecorder) {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/instances/alice-work/policy", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("name", "alice-work")
	if claims != nil {
		req = req.WithContext(auth.NewContext(req.Context(), *claims))
	}
	return req, httptest.NewRecorder()
}

func TestUpdateInstancePolicyRequiresCurrentIdentity(t *testing.T) {
	for _, body := range []map[string]any{
		{"resourceVersion": "1"},
		{"uid": "agent-alice"},
	} {
		h, _, _ := policyPatchFixture()
		req, response := policyPatchRequest(body, nil)
		h.UpdateInstancePolicy(response, req)
		if response.Code != http.StatusPreconditionRequired {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	}
}

func TestUpdateInstancePolicyRejectsStaleVersion(t *testing.T) {
	h, _, agent := policyPatchFixture()
	req, response := policyPatchRequest(map[string]any{
		"uid": string(agent.UID), "resourceVersion": "stale",
	}, nil)
	h.UpdateInstancePolicy(response, req)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestUpdateInstancePolicyRequiresStoppedTransition(t *testing.T) {
	h, _, agent := policyPatchFixture()
	req, response := policyPatchRequest(map[string]any{
		"uid": string(agent.UID), "resourceVersion": agent.ResourceVersion, "monthlyBudgetUSD": "10",
	}, nil)
	h.UpdateInstancePolicy(response, req)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestUpdateInstancePolicyRebindRequiresOwnerMembership(t *testing.T) {
	h, _, agent := policyPatchFixture()
	claims := auth.Claims{TenantID: managementTenant, ObjectID: "not-the-owner"}
	req, response := policyPatchRequest(map[string]any{
		"uid": string(agent.UID), "resourceVersion": agent.ResourceVersion, "team": "platform",
	}, &claims)
	h.UpdateInstancePolicy(response, req)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestUpdateInstancePolicyWritesFreshUIDBoundReauthorization(t *testing.T) {
	h, fc, agent := policyPatchFixture()
	agent.Spec.Suspended = true
	if err := fc.Update(t.Context(), agent); err != nil {
		t.Fatal(err)
	}
	claims := auth.Claims{TenantID: managementTenant, ObjectID: managementOwner}
	req, response := policyPatchRequest(map[string]any{
		"uid": string(agent.UID), "resourceVersion": agent.ResourceVersion, "team": "platform",
	}, &claims)
	h.UpdateInstancePolicy(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	updated := &cetirunv1alpha1.Agent{}
	if err := fc.Get(t.Context(), client.ObjectKey{Name: agent.Name}, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Spec.Team != "platform" || updated.Spec.TeamAuthorization.Subject.ObjectID != managementOwner {
		t.Fatalf("policy assignment was not updated: %+v", updated.Spec)
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
