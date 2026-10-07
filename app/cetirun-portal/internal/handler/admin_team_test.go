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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
)

func newAdminTeamHandler(objs ...client.Object) *AdminTeamHandler {
	for _, obj := range objs {
		if obj.GetUID() == "" {
			obj.SetUID(types.UID("test-" + obj.GetName()))
		}
	}
	c := newFakeClient(objs...)
	return &AdminTeamHandler{Kube: kube.NewClient(c)}
}

func currentTeamVersion(t *testing.T, h *AdminTeamHandler, name string) (string, string) {
	t.Helper()
	team, err := h.Kube.GetTeam(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return string(team.UID), team.ResourceVersion
}

func governedTeamUpdateRequest(t *testing.T, h *AdminTeamHandler, name string) TeamUpsertRequest {
	t.Helper()
	req := governedTeamRequest()
	req.UID, req.ResourceVersion = currentTeamVersion(t, h, name)
	return req
}

func governedTeamRequest() TeamUpsertRequest {
	return TeamUpsertRequest{
		Membership: []cetirunv1alpha1.GovernanceSubject{{
			Kind: "User", TenantID: managementTenant, ObjectID: managementOwner,
		}},
		NormalAgentPolicyRef: "normal",
		SandboxRefs:          []string{"standard"},
		DefaultSandboxRef:    "standard",
	}
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
	var got TeamDTO
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.UID == "" || got.ResourceVersion == "" {
		t.Fatalf("Team response omitted concurrency version: %+v", got)
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
	request := governedTeamRequest()
	request.Name, request.Role, request.DefaultProvider, request.Skills = "sre", "SRE", "anthropic", []string{"npm:@scope/skill"}
	body, _ := json.Marshal(request)
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
	request := governedTeamUpdateRequest(t, h, "aks")
	request.SkillStorage = &TeamSkillStorageDTO{ResourceGroup: " "} // whitespace-only
	body, _ := json.Marshal(request)
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
	request := governedTeamUpdateRequest(t, h, "aks")
	request.Role = "new"
	request.AdminAgentPolicyRef = "admin"
	request.SandboxRefs = []string{"standard", "restricted"}
	body, _ := json.Marshal(request)
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

func TestAdminTeamUpdatePreservesGovernance(t *testing.T) {
	team := &cetirunv1alpha1.Team{
		ObjectMeta: metav1.ObjectMeta{Name: "governed"},
		Spec: cetirunv1alpha1.TeamSpec{
			Membership:           cetirunv1alpha1.TeamMembershipSpec{Subjects: []cetirunv1alpha1.GovernanceSubject{{Kind: "User", TenantID: managementTenant, ObjectID: managementOwner}}},
			NormalAgentPolicyRef: "normal", AdminAgentPolicyRef: "admin",
			SandboxRefs: []string{"standard", "restricted"}, DefaultSandboxRef: "standard",
			MemberContext: cetirunv1alpha1.TeamContextSpec{Role: "old"},
		},
	}
	h := newAdminTeamHandler(team)
	request := governedTeamUpdateRequest(t, h, team.Name)
	request.Role = "new"
	request.AdminAgentPolicyRef = "admin"
	request.SandboxRefs = []string{"standard", "restricted"}
	body, _ := json.Marshal(request)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/teams/governed", bytes.NewReader(body))
	req.SetPathValue("name", team.Name)
	w := httptest.NewRecorder()
	h.UpdateTeam(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	got, err := h.Kube.GetTeam(context.Background(), team.Name)
	if err != nil {
		t.Fatal(err)
	}
	want := team.Spec.DeepCopy()
	want.MemberContext.Role = "new"
	if !reflect.DeepEqual(got.Spec, *want) {
		t.Fatalf("ordinary Portal edit changed governance: got %+v, want %+v", got.Spec, *want)
	}
}

func TestAdminTeam_Update_RequiresVersion(t *testing.T) {
	h := newAdminTeamHandler(&cetirunv1alpha1.Team{ObjectMeta: metav1.ObjectMeta{Name: "aks"}})
	body, _ := json.Marshal(governedTeamRequest())
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/teams/aks", bytes.NewReader(body))
	req.SetPathValue("name", "aks")
	w := httptest.NewRecorder()
	h.UpdateTeam(w, req)
	if w.Code != http.StatusPreconditionRequired || !bytes.Contains(w.Body.Bytes(), []byte(`"version_required"`)) {
		t.Fatalf("status %d, body=%s", w.Code, w.Body.String())
	}
}

func TestAdminTeam_Update_RejectsStaleVersion(t *testing.T) {
	h := newAdminTeamHandler(&cetirunv1alpha1.Team{ObjectMeta: metav1.ObjectMeta{Name: "aks"}})
	request := governedTeamUpdateRequest(t, h, "aks")
	request.ResourceVersion = "stale"
	body, _ := json.Marshal(request)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/teams/aks", bytes.NewReader(body))
	req.SetPathValue("name", "aks")
	w := httptest.NewRecorder()
	h.UpdateTeam(w, req)
	if w.Code != http.StatusConflict || !bytes.Contains(w.Body.Bytes(), []byte(`"team_changed"`)) {
		t.Fatalf("status %d, body=%s", w.Code, w.Body.String())
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
