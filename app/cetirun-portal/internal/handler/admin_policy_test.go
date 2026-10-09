// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func policyHandler(objs ...client.Object) *AdminPolicyHandler {
	return &AdminPolicyHandler{Kube: kube.NewClient(newFakeClient(objs...))}
}

func TestAdminPolicyCRUD(t *testing.T) {
	h := policyHandler()
	body := []byte(`{"apiVersion":"cetirun.microsoft.com/v1alpha1","kind":"AgentPolicy","metadata":{"name":"normal"},"spec":{"harnesses":{"allowed":["hermes"]},"skills":{"allowed":[]},"modelAccess":{"copilot":{"allowed":false},"sharedModels":{"allowed":[]}},"agentIdentity":{"allowed":false,"required":false,"credentialServices":[]},"entraID":{"enabled":false},"sandboxSelection":{"allowMultiple":false},"security":{"readOnlyRootFilesystem":true},"network":{"blockedCIDRs":[],"blockedFQDNs":[],"blockedPorts":[],"allowedInternalCIDRs":[],"ingress":{"mode":"authenticated","requireMTLS":false}}}}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/agent-policies", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	h.CreateAgentPolicy(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create policy status=%d body=%s", response.Code, response.Body.String())
	}
	get := httptest.NewRequest(http.MethodGet, "/api/v1/admin/agent-policies/normal", nil)
	get.SetPathValue("name", "normal")
	response = httptest.NewRecorder()
	h.GetAgentPolicy(response, get)
	if response.Code != http.StatusOK {
		t.Fatalf("get policy status=%d body=%s", response.Code, response.Body.String())
	}

	body = []byte(`{"apiVersion":"cetirun.microsoft.com/v1alpha1","kind":"Sandbox","metadata":{"name":"standard"},"spec":{"type":"kata","resources":{"agent":{"requests":{"cpu":"500m","memory":"1Gi","ephemeral-storage":"2Gi"},"limits":{"cpu":"500m","memory":"1Gi","ephemeral-storage":"2Gi"}},"helpers":{"requests":{"cpu":"100m","memory":"128Mi","ephemeral-storage":"512Mi"},"limits":{"cpu":"100m","memory":"128Mi","ephemeral-storage":"512Mi"}}},"storage":{"home":{"storageClassName":"managed-csi-premium","size":"10Gi","accessMode":"ReadWriteOncePod"},"scratch":{"sizeLimit":"2Gi"}}}}`)
	request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/sandboxes", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	h.CreateSandbox(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create sandbox status=%d body=%s", response.Code, response.Body.String())
	}
}

func policyRequest(method, path, name string, body any) (*http.Request, *httptest.ResponseRecorder) {
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	request.SetPathValue("name", name)
	return request, httptest.NewRecorder()
}

func TestAdminPolicyMutationRequiresCurrentIdentity(t *testing.T) {
	policy := &cetirunv1alpha1.AgentPolicy{ObjectMeta: metav1.ObjectMeta{
		Name: "normal", UID: types.UID("policy-uid"), ResourceVersion: "7",
	}}
	sandbox := &cetirunv1alpha1.Sandbox{ObjectMeta: metav1.ObjectMeta{
		Name: "standard", UID: types.UID("sandbox-uid"), ResourceVersion: "9",
	}}
	h := policyHandler(policy, sandbox)
	for _, test := range []struct {
		name   string
		method string
		path   string
		object string
		call   func(http.ResponseWriter, *http.Request)
	}{
		{"update policy", http.MethodPut, "/api/v1/admin/agent-policies/normal", "normal", h.UpdateAgentPolicy},
		{"delete policy", http.MethodDelete, "/api/v1/admin/agent-policies/normal", "normal", h.DeleteAgentPolicy},
		{"update Sandbox", http.MethodPut, "/api/v1/admin/sandboxes/standard", "standard", h.UpdateSandbox},
		{"delete Sandbox", http.MethodDelete, "/api/v1/admin/sandboxes/standard", "standard", h.DeleteSandbox},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := map[string]any{}
			if test.method == http.MethodPut {
				body["spec"] = map[string]any{}
			}
			request, response := policyRequest(test.method, test.path, test.object, body)
			test.call(response, request)
			if response.Code != http.StatusPreconditionRequired {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestAdminPolicyMutationRejectsStaleResource(t *testing.T) {
	policy := &cetirunv1alpha1.AgentPolicy{ObjectMeta: metav1.ObjectMeta{
		Name: "normal", UID: types.UID("policy-uid"), ResourceVersion: "7",
	}}
	h := policyHandler(policy)
	for _, test := range []struct {
		name   string
		method string
		body   map[string]any
		call   func(http.ResponseWriter, *http.Request)
	}{
		{"update", http.MethodPut, map[string]any{"uid": "policy-uid", "resourceVersion": "6", "spec": map[string]any{}}, h.UpdateAgentPolicy},
		{"delete", http.MethodDelete, map[string]any{"uid": "old-policy-uid", "resourceVersion": "7"}, h.DeleteAgentPolicy},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, response := policyRequest(test.method, "/api/v1/admin/agent-policies/normal", "normal", test.body)
			test.call(response, request)
			if response.Code != http.StatusConflict {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			current, err := h.Kube.GetAgentPolicy(t.Context(), "normal")
			if err != nil || current.UID != policy.UID {
				t.Fatalf("stale request changed replacement resource: current=%+v err=%v", current, err)
			}
		})
	}
}

func TestAdminPolicyPatchRequiresSuspensionAndCreatesIntent(t *testing.T) {
	agent := sampleInstance()
	agent.Name, agent.UID, agent.ResourceVersion = "alice", types.UID("agent-uid"), "7"
	agent.Spec.Suspended = true
	agent.Spec.Team, agent.Spec.SandboxRef = "old", "standard"
	agent.Spec.PolicyClass = cetirunv1alpha1.AgentPolicyClassNormal
	policy := &cetirunv1alpha1.AgentPolicy{ObjectMeta: metav1.ObjectMeta{Name: "normal", UID: "policy-uid"}}
	sandbox := &cetirunv1alpha1.Sandbox{ObjectMeta: metav1.ObjectMeta{Name: "standard", UID: "sandbox-uid"}}
	team := &cetirunv1alpha1.Team{ObjectMeta: metav1.ObjectMeta{Name: "new", UID: "team-uid"}, Spec: cetirunv1alpha1.TeamSpec{NormalAgentPolicyRef: "normal", SandboxRefs: []string{"standard"}, DefaultSandboxRef: "standard"}}
	h := policyHandler(agent, policy, sandbox, team)
	body := []byte(`{"uid":"agent-uid","resourceVersion":"7","team":"new","sandboxRef":"standard","policyClass":"normal"}`)
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/instances/alice/policy", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.SetPathValue("name", "alice")
	response := httptest.NewRecorder()
	h.UpdateInstancePolicy(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", response.Code, response.Body.String())
	}
	updated, err := h.Kube.GetInstanceByName(t.Context(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Spec.Team != "new" || updated.Annotations["cetirun.microsoft.com/sandbox-reauthorize"] == "" {
		t.Fatalf("policy binding or intent missing: %+v", updated)
	}
}

func TestAdminPolicyPatchRejectsActiveAgent(t *testing.T) {
	agent := sampleInstance()
	agent.Name, agent.UID, agent.ResourceVersion = "alice", types.UID("agent-uid"), "7"
	agent.Spec.Team, agent.Spec.SandboxRef = "team", "standard"
	agent.Spec.PolicyClass = cetirunv1alpha1.AgentPolicyClassNormal
	team := &cetirunv1alpha1.Team{ObjectMeta: metav1.ObjectMeta{Name: "team"}, Spec: cetirunv1alpha1.TeamSpec{NormalAgentPolicyRef: "normal", SandboxRefs: []string{"standard"}, DefaultSandboxRef: "standard"}}
	h := policyHandler(agent, team)
	body := []byte(`{"uid":"agent-uid","resourceVersion":"7","monthlyBudgetUSD":"10"}`)
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/instances/alice/policy", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.SetPathValue("name", "alice")
	response := httptest.NewRecorder()
	h.UpdateInstancePolicy(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("active update status=%d body=%s", response.Code, response.Body.String())
	}
}
