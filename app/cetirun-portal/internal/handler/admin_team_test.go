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

func TestAdminTeam_List(t *testing.T) {
	team := &cetirunv1alpha1.Team{
		ObjectMeta: metav1.ObjectMeta{Name: "aks", UID: "aks-uid"},
		Spec: cetirunv1alpha1.TeamSpec{
			AgentPolicyRef: "standard", SandboxRefs: []string{"standard"}, DefaultSandboxRef: "standard",
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
		ObjectMeta: metav1.ObjectMeta{Name: "aks", UID: "aks-uid"},
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
		AgentPolicyRef:  "standard", SandboxRefs: []string{"standard"}, DefaultSandboxRef: "standard",
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
	body, _ := json.Marshal(TeamUpsertRequest{Name: "1bad", AgentPolicyRef: "standard", SandboxRefs: []string{"standard"}, DefaultSandboxRef: "standard"})
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
		Name: "sre", AgentPolicyRef: "standard", SandboxRefs: []string{"standard"}, DefaultSandboxRef: "standard",
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
		ObjectMeta: metav1.ObjectMeta{Name: "aks", UID: "aks-uid"},
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
		ObjectMeta: metav1.ObjectMeta{Name: "aks", UID: "aks-uid"},
		Spec: cetirunv1alpha1.TeamSpec{
			AgentPolicyRef: "standard", SandboxRefs: []string{"standard"}, DefaultSandboxRef: "standard",
			MemberContext: cetirunv1alpha1.TeamContextSpec{Role: "old"},
		},
	})
	uid, resourceVersion := currentTeamVersion(t, h, "aks")
	body, _ := json.Marshal(TeamUpsertRequest{Role: "new", UID: uid, ResourceVersion: resourceVersion, AgentPolicyRef: "standard", SandboxRefs: []string{"standard"}, DefaultSandboxRef: "standard"})
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

func TestAdminTeamUpdateReplacesPolicyAndSandboxBindings(t *testing.T) {
	team := &cetirunv1alpha1.Team{
		ObjectMeta: metav1.ObjectMeta{Name: "governed", UID: "governed-uid"},
		Spec: cetirunv1alpha1.TeamSpec{
			AgentPolicyRef: "old-policy",
			SandboxRefs:    []string{"standard", "restricted"}, DefaultSandboxRef: "standard",
			MemberContext: cetirunv1alpha1.TeamContextSpec{Role: "old"},
		},
	}
	h := newAdminTeamHandler(team)
	uid, resourceVersion := currentTeamVersion(t, h, team.Name)
	body, _ := json.Marshal(TeamUpsertRequest{Role: "new", UID: uid, ResourceVersion: resourceVersion, AgentPolicyRef: "new-policy", SandboxRefs: []string{"restricted"}, DefaultSandboxRef: "restricted"})
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
	if got.Spec.MemberContext.Role != "new" || got.Spec.AgentPolicyRef != "new-policy" || !reflect.DeepEqual(got.Spec.SandboxRefs, []string{"restricted"}) || got.Spec.DefaultSandboxRef != "restricted" {
		t.Fatalf("Portal edit did not apply the requested bindings: %+v", got.Spec)
	}
}

func TestAdminTeam_Delete(t *testing.T) {
	h := newAdminTeamHandler(&cetirunv1alpha1.Team{
		ObjectMeta: metav1.ObjectMeta{Name: "aks", UID: "aks-uid"},
	})
	uid, resourceVersion := currentTeamVersion(t, h, "aks")
	body, _ := json.Marshal(map[string]string{"uid": uid, "resourceVersion": resourceVersion})
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/teams/aks", bytes.NewReader(body))
	req.SetPathValue("name", "aks")
	w := httptest.NewRecorder()
	h.DeleteTeam(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("status %d, want 204", w.Code)
	}
}

func TestAdminTeamRejectsMissingAndStaleVersions(t *testing.T) {
	team := &cetirunv1alpha1.Team{ObjectMeta: metav1.ObjectMeta{Name: "aks", UID: "aks-uid"}, Spec: cetirunv1alpha1.TeamSpec{AgentPolicyRef: "standard", SandboxRefs: []string{"standard"}, DefaultSandboxRef: "standard"}}
	h := newAdminTeamHandler(team)
	missing := httptest.NewRequest(http.MethodPut, "/api/v1/admin/teams/aks", bytes.NewBufferString(`{"agentPolicyRef":"standard","sandboxRefs":["standard"],"defaultSandboxRef":"standard"}`))
	missing.SetPathValue("name", "aks")
	w := httptest.NewRecorder()
	h.UpdateTeam(w, missing)
	if w.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing update status %d, want 428: %s", w.Code, w.Body.String())
	}
	uid, resourceVersion := currentTeamVersion(t, h, "aks")
	if _, err := h.Kube.UpdateTeamSpec(context.Background(), "aks", team.Spec, kube.TeamVersion{UID: types.UID(uid), ResourceVersion: resourceVersion}); err != nil {
		t.Fatal(err)
	}
	stale := httptest.NewRequest(http.MethodPut, "/api/v1/admin/teams/aks", bytes.NewBufferString(`{"uid":"aks-uid","resourceVersion":"`+resourceVersion+`","agentPolicyRef":"standard","sandboxRefs":["standard"],"defaultSandboxRef":"standard"}`))
	stale.SetPathValue("name", "aks")
	w = httptest.NewRecorder()
	h.UpdateTeam(w, stale)
	if w.Code != http.StatusConflict {
		t.Fatalf("stale update status %d, want 409: %s", w.Code, w.Body.String())
	}
	missingDelete := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/teams/aks", nil)
	missingDelete.SetPathValue("name", "aks")
	w = httptest.NewRecorder()
	h.DeleteTeam(w, missingDelete)
	if w.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing delete status %d, want 428: %s", w.Code, w.Body.String())
	}
	staleDelete := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/teams/aks", bytes.NewBufferString(`{"uid":"aks-uid","resourceVersion":"`+resourceVersion+`"}`))
	staleDelete.SetPathValue("name", "aks")
	w = httptest.NewRecorder()
	h.DeleteTeam(w, staleDelete)
	if w.Code != http.StatusConflict {
		t.Fatalf("stale delete status %d, want 409: %s", w.Code, w.Body.String())
	}
}
