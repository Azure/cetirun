// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
)

func newAdminPolicyHandler(objs ...client.Object) *AdminPolicyHandler {
	return &AdminPolicyHandler{Kube: kube.NewClient(newFakeClient(objs...))}
}

func validPolicyRequest() AgentPolicyUpsertRequest {
	return AgentPolicyUpsertRequest{
		Name:      "standard",
		Harnesses: AgentPolicyHarnessesDTO{Allowed: []cetirunv1alpha1.AgentType{cetirunv1alpha1.AgentTypeOpenClaw}},
		Skills:    AgentPolicySkillsDTO{Allowed: []string{"npm:@scope/skill"}},
		ModelAccess: AgentPolicyModelAccessDTO{
			Copilot:      AgentPolicyCopilotAccessDTO{Allowed: true},
			SharedModels: AgentPolicySharedModelsAccessDTO{Allowed: []string{"gpt-4.1"}},
			GatewayBudget: &AgentPolicyGatewayBudgetDTO{
				DefaultMonthlyUSD: "20",
				MaxMonthlyUSD:     "50",
			},
		},
	}
}

func TestAdminPolicyCreateAndListOnlyV1Fields(t *testing.T) {
	h := newAdminPolicyHandler()
	body, _ := json.Marshal(validPolicyRequest())
	create := httptest.NewRequest(http.MethodPost, "/api/v1/admin/policies", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.CreatePolicy(w, create)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status %d: %s", w.Code, w.Body.String())
	}
	var created AgentPolicyDTO
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Name != "standard" || !created.ModelAccess.Copilot.Allowed || created.ModelAccess.GatewayBudget == nil {
		t.Fatalf("unexpected policy response: %+v", created)
	}

	list := httptest.NewRequest(http.MethodGet, "/api/v1/admin/policies", nil)
	w = httptest.NewRecorder()
	h.ListPolicies(w, list)
	if w.Code != http.StatusOK {
		t.Fatalf("list status %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Policies []AgentPolicyDTO `json:"policies"`
	}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Policies) != 1 || response.Policies[0].Name != "standard" || len(response.Policies[0].Skills.Allowed) != 1 {
		t.Fatalf("unexpected policies: %+v", response.Policies)
	}
}

func TestAdminPolicyRejectsInvalidBudget(t *testing.T) {
	h := newAdminPolicyHandler()
	req := validPolicyRequest()
	req.ModelAccess.GatewayBudget.DefaultMonthlyUSD = "51"
	body, _ := json.Marshal(req)
	w := httptest.NewRecorder()
	h.CreatePolicy(w, httptest.NewRequest(http.MethodPost, "/api/v1/admin/policies", bytes.NewReader(body)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", w.Code, w.Body.String())
	}
}

func TestAdminPolicyRequiresMatchingModelAccessAndPositiveBudget(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*AgentPolicyUpsertRequest)
	}{
		{"copilot without budget", func(req *AgentPolicyUpsertRequest) { req.ModelAccess.GatewayBudget = nil }},
		{"shared model without budget", func(req *AgentPolicyUpsertRequest) {
			req.ModelAccess.Copilot.Allowed = false
			req.ModelAccess.GatewayBudget = nil
		}},
		{"budget without model access", func(req *AgentPolicyUpsertRequest) {
			req.ModelAccess.Copilot.Allowed = false
			req.ModelAccess.SharedModels.Allowed = nil
		}},
		{"zero default budget", func(req *AgentPolicyUpsertRequest) { req.ModelAccess.GatewayBudget.DefaultMonthlyUSD = "0" }},
		{"zero maximum budget", func(req *AgentPolicyUpsertRequest) { req.ModelAccess.GatewayBudget.MaxMonthlyUSD = "0" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newAdminPolicyHandler()
			req := validPolicyRequest()
			test.edit(&req)
			body, _ := json.Marshal(req)
			w := httptest.NewRecorder()
			h.CreatePolicy(w, httptest.NewRequest(http.MethodPost, "/api/v1/admin/policies", bytes.NewReader(body)))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestAdminPolicyUpdateAndDelete(t *testing.T) {
	const policyName = "standard.policy"
	policy := &cetirunv1alpha1.AgentPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: policyName, UID: "policy-uid"},
		Spec:       toAgentPolicySpec(validPolicyRequest()),
	}
	policy.Spec.Security.ReadOnlyRootFilesystem = new(bool)
	policy.Spec.Network.BlockedCIDRs = []string{"10.0.0.0/8"}
	policy.Spec.AgentIdentity.Allowed = true
	policy.Spec.EntraID.Enabled = true
	policy.Spec.SandboxSelection.AllowMultiple = true
	policy.Spec.KubernetesAccess = &cetirunv1alpha1.AgentPolicyAdminKubernetesAccess{
		ServiceAccountName:  "agent",
		ClusterRoleBindings: []cetirunv1alpha1.AgentPolicyClusterRoleBinding{{ClusterRoleName: "view"}},
	}
	preserved := *policy.Spec.DeepCopy()
	h := newAdminPolicyHandler(policy)
	get := httptest.NewRequest(http.MethodGet, "/api/v1/admin/policies/"+policyName, nil)
	get.SetPathValue("name", policyName)
	w := httptest.NewRecorder()
	h.GetPolicy(w, get)
	if w.Code != http.StatusOK {
		t.Fatalf("get status %d: %s", w.Code, w.Body.String())
	}
	current, err := h.Kube.GetAgentPolicy(context.Background(), policyName)
	if err != nil {
		t.Fatal(err)
	}
	req := validPolicyRequest()
	req.Name = ""
	req.UID, req.ResourceVersion = string(current.UID), current.ResourceVersion
	req.Harnesses.Allowed = []cetirunv1alpha1.AgentType{cetirunv1alpha1.AgentTypeHermes}
	body, _ := json.Marshal(req)
	update := httptest.NewRequest(http.MethodPut, "/api/v1/admin/policies/"+policyName, bytes.NewReader(body))
	update.SetPathValue("name", policyName)
	w = httptest.NewRecorder()
	h.UpdatePolicy(w, update)
	if w.Code != http.StatusOK {
		t.Fatalf("update status %d: %s", w.Code, w.Body.String())
	}
	stored, err := h.Kube.GetAgentPolicy(context.Background(), policyName)
	if err != nil || len(stored.Spec.Harnesses.Allowed) != 1 || stored.Spec.Harnesses.Allowed[0] != cetirunv1alpha1.AgentTypeHermes {
		t.Fatalf("stored policy = %+v, err=%v", stored, err)
	}
	if stored.Spec.Security.ReadOnlyRootFilesystem == nil ||
		*stored.Spec.Security.ReadOnlyRootFilesystem != *preserved.Security.ReadOnlyRootFilesystem ||
		len(stored.Spec.Network.BlockedCIDRs) != 1 || stored.Spec.Network.BlockedCIDRs[0] != preserved.Network.BlockedCIDRs[0] ||
		!reflect.DeepEqual(stored.Spec.AgentIdentity, preserved.AgentIdentity) || stored.Spec.EntraID != preserved.EntraID ||
		stored.Spec.SandboxSelection != preserved.SandboxSelection ||
		!reflect.DeepEqual(stored.Spec.KubernetesAccess, preserved.KubernetesAccess) {
		t.Fatalf("Portal update discarded component-specific policy controls: before=%+v after=%+v", preserved, stored.Spec)
	}
	deleteBody, _ := json.Marshal(map[string]string{"uid": string(stored.UID), "resourceVersion": stored.ResourceVersion})
	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/policies/"+policyName, bytes.NewReader(deleteBody))
	deleteRequest.SetPathValue("name", policyName)
	w = httptest.NewRecorder()
	h.DeletePolicy(w, deleteRequest)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete status %d: %s", w.Code, w.Body.String())
	}
}

func TestAdminPolicyRejectsStaleUpdateAndDelete(t *testing.T) {
	policy := &cetirunv1alpha1.AgentPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "standard", UID: "policy-uid"},
		Spec:       toAgentPolicySpec(validPolicyRequest()),
	}
	h := newAdminPolicyHandler(policy)
	stale, err := h.Kube.GetAgentPolicy(context.Background(), "standard")
	if err != nil {
		t.Fatal(err)
	}
	version := kube.AgentPolicyVersion{UID: stale.UID, ResourceVersion: stale.ResourceVersion}
	changed := *stale.Spec.DeepCopy()
	changed.Harnesses.Allowed = []cetirunv1alpha1.AgentType{cetirunv1alpha1.AgentTypeHermes}
	if _, err := h.Kube.UpdateAgentPolicySpec(context.Background(), "standard", changed, version); err != nil {
		t.Fatal(err)
	}
	req := validPolicyRequest()
	req.Name = ""
	req.UID, req.ResourceVersion = string(stale.UID), stale.ResourceVersion
	body, _ := json.Marshal(req)
	update := httptest.NewRequest(http.MethodPut, "/api/v1/admin/policies/standard", bytes.NewReader(body))
	update.SetPathValue("name", "standard")
	w := httptest.NewRecorder()
	h.UpdatePolicy(w, update)
	if w.Code != http.StatusConflict {
		t.Fatalf("stale update status %d, want 409: %s", w.Code, w.Body.String())
	}
	deleteBody, _ := json.Marshal(map[string]string{"uid": string(stale.UID), "resourceVersion": stale.ResourceVersion})
	delete := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/policies/standard", bytes.NewReader(deleteBody))
	delete.SetPathValue("name", "standard")
	w = httptest.NewRecorder()
	h.DeletePolicy(w, delete)
	if w.Code != http.StatusConflict {
		t.Fatalf("stale delete status %d, want 409: %s", w.Code, w.Body.String())
	}
}
