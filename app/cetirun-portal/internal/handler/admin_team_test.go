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

func newAdminTeamHandler(objs ...client.Object) *AdminTeamHandler {
	c := newFakeClient(objs...)
	return &AdminTeamHandler{Kube: kube.NewClient(c)}
}

func TestAdminTeam_List(t *testing.T) {
	team := &cetirunv1alpha1.Team{
		ObjectMeta: metav1.ObjectMeta{Name: "aks"},
		Spec: cetirunv1alpha1.TeamSpec{
			MemberContext: cetirunv1alpha1.TeamContextSpec{Role: "AKS Engineer"},
			Defaults:      cetirunv1alpha1.TeamDefaultsSpec{Provider: "github_copilot"},
			Skills:        []string{"@anthropic/mcp-server-fetch"},
		},
	}
	h := newAdminTeamHandler(team)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/teams", nil)
	w := httptest.NewRecorder()
	h.ListTeams(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var resp struct {
		Teams []TeamDTO `json:"teams"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Teams) != 1 || resp.Teams[0].Name != "aks" || resp.Teams[0].Role != "AKS Engineer" {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestAdminTeam_Get(t *testing.T) {
	h := newAdminTeamHandler(&cetirunv1alpha1.Team{
		ObjectMeta: metav1.ObjectMeta{Name: "aks"},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/teams/aks", nil)
	req.SetPathValue("name", "aks")
	w := httptest.NewRecorder()
	h.GetTeam(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, body=%s", w.Code, w.Body.String())
	}
}

func TestAdminTeam_Get_NotFound(t *testing.T) {
	h := newAdminTeamHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/teams/missing", nil)
	req.SetPathValue("name", "missing")
	w := httptest.NewRecorder()
	h.GetTeam(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", w.Code)
	}
}

func TestAdminTeam_Create(t *testing.T) {
	h := newAdminTeamHandler()
	body, _ := json.Marshal(TeamUpsertRequest{
		Name:            "sre",
		Role:            "SRE",
		DefaultProvider: "anthropic",
		Skills:          []string{"npm:@scope/skill"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/teams", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.CreateTeam(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d, body=%s", w.Code, w.Body.String())
	}
	var got TeamDTO
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Name != "sre" || got.DefaultProvider != "anthropic" {
		t.Errorf("unexpected DTO: %+v", got)
	}
}

func TestAdminTeam_Create_InvalidName(t *testing.T) {
	h := newAdminTeamHandler()
	body, _ := json.Marshal(TeamUpsertRequest{Name: "1bad"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/teams", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.CreateTeam(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", w.Code)
	}
}

func TestAdminTeam_Create_InvalidSkillStorage(t *testing.T) {
	h := newAdminTeamHandler()
	// skillStorage present but missing required containerName
	body, _ := json.Marshal(TeamUpsertRequest{
		Name: "sre",
		SkillStorage: &TeamSkillStorageDTO{
			ResourceGroup:  "rg",
			StorageAccount: "sa",
			// ContainerName intentionally empty
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/teams", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.CreateTeam(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestAdminTeam_Update_InvalidSkillStorage(t *testing.T) {
	h := newAdminTeamHandler(&cetirunv1alpha1.Team{
		ObjectMeta: metav1.ObjectMeta{Name: "aks"},
	})
	body, _ := json.Marshal(TeamUpsertRequest{
		SkillStorage: &TeamSkillStorageDTO{ResourceGroup: " "}, // whitespace-only
	})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/teams/aks", bytes.NewReader(body))
	req.SetPathValue("name", "aks")
	w := httptest.NewRecorder()
	h.UpdateTeam(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestAdminTeam_Update(t *testing.T) {
	h := newAdminTeamHandler(&cetirunv1alpha1.Team{
		ObjectMeta: metav1.ObjectMeta{Name: "aks"},
		Spec: cetirunv1alpha1.TeamSpec{
			MemberContext: cetirunv1alpha1.TeamContextSpec{Role: "old"},
		},
	})
	body, _ := json.Marshal(TeamUpsertRequest{Role: "new"})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/teams/aks", bytes.NewReader(body))
	req.SetPathValue("name", "aks")
	w := httptest.NewRecorder()
	h.UpdateTeam(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, body=%s", w.Code, w.Body.String())
	}
	var got TeamDTO
	_ = json.NewDecoder(w.Body).Decode(&got)
	if got.Role != "new" {
		t.Errorf("role = %q, want new", got.Role)
	}
}

func TestAdminTeam_Delete(t *testing.T) {
	h := newAdminTeamHandler(&cetirunv1alpha1.Team{
		ObjectMeta: metav1.ObjectMeta{Name: "aks"},
	})
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/teams/aks", nil)
	req.SetPathValue("name", "aks")
	w := httptest.NewRecorder()
	h.DeleteTeam(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("status %d, want 204", w.Code)
	}
}
