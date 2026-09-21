// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cetirunv1beta1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1beta1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
)

func TestAdminInstance_ListAll(t *testing.T) {
	c := newFakeClient(
		&cetirunv1beta1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: "alice"},
			Spec: cetirunv1beta1.AgentSpec{
				AgentType: "openclaw",
				Team:      "aks",
				User: cetirunv1beta1.UserSpec{
					Alias: "alice", DisplayName: "Alice", Email: "alice@example.com",
				},
			},
			Status: cetirunv1beta1.AgentStatus{
				Phase: "Ready",
				Conditions: []metav1.Condition{
					{Type: "Ready", Status: "True"},
				},
			},
		},
		&cetirunv1beta1.Agent{
			ObjectMeta: metav1.ObjectMeta{Name: "bob"},
			Spec: cetirunv1beta1.AgentSpec{
				AgentType: "openclaw",
				User:      cetirunv1beta1.UserSpec{Alias: "bob", Team: "legacy"},
			},
			Status: cetirunv1beta1.AgentStatus{Phase: "Pending"},
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
		t.Errorf("bob team should fall back to user.team, got %q", byName["bob"].Team)
	}
}
