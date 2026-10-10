// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
)

func TestAdminInstance_ListAll(t *testing.T) {
	c := newFakeClient(
		&cetirunv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: "alice"},
			Spec: cetirunv1alpha1.AgentSpec{
				Agent: cetirunv1alpha1.HarnessSpec{Type: cetirunv1alpha1.AgentTypeOpenClaw},
				Team:  "aks",
				User: cetirunv1alpha1.UserSpec{
					Alias: "alice", DisplayName: "Alice", Email: "alice@example.com",
				},
			},
			Status: cetirunv1alpha1.AgentStatus{
				Phase: "Ready",
				Conditions: []metav1.Condition{
					{Type: "Ready", Status: "True"},
				},
			},
		},
		&cetirunv1alpha1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: "bob"},
			Spec: cetirunv1alpha1.AgentSpec{
				Agent: cetirunv1alpha1.HarnessSpec{Type: cetirunv1alpha1.AgentTypeOpenClaw},
				Team:  "platform",
				User:  cetirunv1alpha1.UserSpec{Alias: "bob"},
			},
			Status: cetirunv1alpha1.AgentStatus{Phase: "Pending"},
		},
	)
	h := &AdminInstanceHandler{Kube: kube.NewClient(c)}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/instances", nil)
	w := httptest.NewRecorder()
	h.ListAllInstances(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Instances []AdminInstanceSummary `json:"instances"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Instances) != 2 {
		t.Fatalf("want 2 instances, got %d", len(resp.Instances))
	}
	byName := map[string]AdminInstanceSummary{}
	for _, ins := range resp.Instances {
		byName[ins.Name] = ins
	}
	if !byName["alice"].Ready || byName["alice"].Team != "aks" {
		t.Errorf("alice unexpected: %+v", byName["alice"])
	}
	if byName["bob"].Ready {
		t.Errorf("bob should not be ready")
	}
	if byName["bob"].Team != "platform" {
		t.Errorf("bob team = %q, want platform", byName["bob"].Team)
	}
}

func TestAdminInstanceComponentPhasesRemainIndependentFromPolicy(t *testing.T) {
	requested := &cetirunv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Generation: 1},
		Spec: cetirunv1alpha1.AgentSpec{
			SandboxRef:    "standard",
			ModelAccess:   &cetirunv1alpha1.ModelAccessSpec{SharedModels: ptr.To(true)},
			AgentIdentity: &cetirunv1alpha1.AgentIdentitySpec{Enabled: true},
		},
		Status: cetirunv1alpha1.AgentStatus{
			Policy:        &cetirunv1alpha1.PolicyStatus{Phase: "Resolved", ObservedGeneration: 1},
			AIGateway:     &cetirunv1alpha1.AIGatewayStatus{Phase: cetirunv1alpha1.AIGatewayPhaseReady, ObservedGeneration: 1},
			AgentIdentity: &cetirunv1alpha1.AgentIdentityStatus{Phase: cetirunv1alpha1.AgentIdentityPhaseProvisioning, ObservedGeneration: 1},
			Conditions:    []metav1.Condition{{Type: cetirunv1alpha1.ConditionSandboxReady, Status: metav1.ConditionFalse, Reason: "PreparationPending", ObservedGeneration: 1}},
		},
	}
	if got := instancePolicyPhase(requested); got != "Resolved" {
		t.Errorf("policy = %q, want Resolved", got)
	}
	if got := instanceGatewayPhase(requested); got != "Ready" {
		t.Errorf("gateway = %q, want Ready", got)
	}
	if got := instanceSandboxPhase(requested); got != "PreparationPending" {
		t.Errorf("sandbox = %q, want PreparationPending", got)
	}
	if got := instanceAgentIdentityPhase(requested); got != "Provisioning" {
		t.Errorf("identity = %q, want Provisioning", got)
	}

	notRequested := &cetirunv1alpha1.Agent{}
	if got := instancePolicyPhase(notRequested); got != "Unknown" {
		t.Errorf("empty policy = %q, want Unknown", got)
	}
	if got := instanceGatewayPhase(notRequested); got != "Not requested" {
		t.Errorf("empty gateway = %q, want Not requested", got)
	}
	if got := instanceSandboxPhase(notRequested); got != "Not requested" {
		t.Errorf("empty sandbox = %q, want Not requested", got)
	}
	if got := instanceAgentIdentityPhase(notRequested); got != "Not requested" {
		t.Errorf("empty identity = %q, want Not requested", got)
	}
}

func TestAdminInstanceComponentPhasesShowUpdatingWhenStale(t *testing.T) {
	ins := &cetirunv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Generation: 2},
		Spec: cetirunv1alpha1.AgentSpec{
			SandboxRef: "standard", ModelAccess: &cetirunv1alpha1.ModelAccessSpec{SharedModels: ptr.To(true)},
			AgentIdentity: &cetirunv1alpha1.AgentIdentitySpec{Enabled: true},
		},
		Status: cetirunv1alpha1.AgentStatus{
			Policy:        &cetirunv1alpha1.PolicyStatus{Phase: "Resolved", ObservedGeneration: 1},
			AIGateway:     &cetirunv1alpha1.AIGatewayStatus{Phase: cetirunv1alpha1.AIGatewayPhaseReady, ObservedGeneration: 1},
			AgentIdentity: &cetirunv1alpha1.AgentIdentityStatus{Phase: cetirunv1alpha1.AgentIdentityPhaseReady, ObservedGeneration: 1},
			Conditions:    []metav1.Condition{{Type: cetirunv1alpha1.ConditionSandboxReady, Status: metav1.ConditionTrue, ObservedGeneration: 1}},
		},
	}
	for name, phase := range map[string]string{
		"policy": instancePolicyPhase(ins), "gateway": instanceGatewayPhase(ins),
		"sandbox": instanceSandboxPhase(ins), "identity": instanceAgentIdentityPhase(ins),
	} {
		if phase != "Updating" {
			t.Errorf("%s = %q, want Updating", name, phase)
		}
	}
}
