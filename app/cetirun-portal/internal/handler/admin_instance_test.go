// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
				User:  cetirunv1alpha1.UserSpec{Alias: "bob"},
				Team:  "legacy",
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
	if byName["bob"].Team != "legacy" {
		t.Errorf("bob team = %q, want legacy", byName["bob"].Team)
	}
}

func TestAdminInstanceUpdateLifecycle(t *testing.T) {
	for _, suspended := range []bool{true, false} {
		t.Run(map[bool]string{true: "suspend", false: "resume"}[suspended], func(t *testing.T) {
			agent := sampleInstance()
			agent.Spec.Suspended = !suspended
			fc := newFakeClient(agent)
			h := &AdminInstanceHandler{Kube: kube.NewClient(fc)}
			body, err := json.Marshal(map[string]any{"uid": string(agent.UID), "resourceVersion": agent.ResourceVersion, "suspended": suspended})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/instances/"+agent.Name, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.SetPathValue("name", agent.Name)
			response := httptest.NewRecorder()
			h.UpdateLifecycle(response, req)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			updated := &cetirunv1alpha1.Agent{}
			if err := fc.Get(t.Context(), client.ObjectKey{Name: agent.Name}, updated); err != nil {
				t.Fatal(err)
			}
			if updated.Spec.Suspended != suspended {
				t.Fatalf("suspended=%t, want %t", updated.Spec.Suspended, suspended)
			}
		})
	}
}

func TestAdminInstanceUpdateLifecycleRejectsStaleVersion(t *testing.T) {
	agent := sampleInstance()
	h := &AdminInstanceHandler{Kube: kube.NewClient(newFakeClient(agent))}
	body := []byte(`{"uid":"agent-alice","resourceVersion":"stale","suspended":true}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/instances/alice", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("name", agent.Name)
	response := httptest.NewRecorder()
	h.UpdateLifecycle(response, req)
	if response.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
