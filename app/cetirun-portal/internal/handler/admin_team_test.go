// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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

func TestAdminTeam_WriteErrors(t *testing.T) {
	missingGovernance := apierrors.NewInvalid(schema.GroupKind{Group: cetirunv1alpha1.GroupVersion.Group, Kind: "Team"}, "example-team", field.ErrorList{
		field.Required(field.NewPath("spec", "agentPolicyRef"), ""),
		field.Required(field.NewPath("spec", "defaultSandboxRef"), ""),
		field.Required(field.NewPath("spec", "sandboxRefs"), ""),
	})
	invalidSkill := apierrors.NewInvalid(schema.GroupKind{Kind: "Team"}, "example-team", field.ErrorList{
		field.Invalid(field.NewPath("spec", "skills").Index(0), "sensitive-rejected-value", "sensitive-admission-detail"),
		field.Invalid(field.NewPath("metadata", "annotations").Key("sensitive-map-key"), "value", "detail"),
	})
	tests := []struct {
		name     string
		err      error
		status   int
		code     string
		contains []string
	}{
		{
			name: "required governance", err: missingGovernance, status: http.StatusUnprocessableEntity, code: "invalid_team",
			contains: []string{"spec.agentPolicyRef: Required value", "spec.defaultSandboxRef: Required value", "spec.sandboxRefs: Required value", "this form cannot configure", "Portal and installed CRDs are compatible"},
		},
		{
			name: "invalid skill", err: invalidSkill, status: http.StatusUnprocessableEntity, code: "invalid_team",
			contains: []string{"spec.skills[0]: Invalid value", "Check the Team fields"},
		},
		{
			name: "invalid without details", err: &apierrors.StatusError{ErrStatus: metav1.Status{Reason: metav1.StatusReasonInvalid, Message: "sensitive-status-message", Code: 422}},
			status: http.StatusUnprocessableEntity, code: "invalid_team", contains: []string{"Team configuration was rejected", "Portal logs"},
		},
		{
			name: "unknown failure", err: errors.New("sensitive-transport-detail"), status: http.StatusInternalServerError, code: "internal_error",
			contains: []string{"Failed to"},
		},
		{
			name: "forbidden", err: apierrors.NewForbidden(schema.GroupResource{Resource: "teams"}, "example-team", errors.New("sensitive-rbac-detail")),
			status: http.StatusForbidden, code: "rbac_forbidden", contains: []string{"Check ClusterRole bindings"},
		},
	}
	for _, operation := range []string{"create", "update"} {
		for _, tt := range tests {
			t.Run(operation+"/"+tt.name, func(t *testing.T) {
				base := newFakeClient(&cetirunv1alpha1.Team{ObjectMeta: metav1.ObjectMeta{Name: "example-team"}})
				c := interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{
					Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error { return tt.err },
					Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
						return tt.err
					},
				})
				h := &AdminTeamHandler{Kube: kube.NewClient(c)}
				w := httptest.NewRecorder()
				if operation == "create" {
					h.CreateTeam(w, httptest.NewRequest(http.MethodPost, "/api/v1/admin/teams", strings.NewReader(`{"name":"example-team"}`)))
				} else {
					r := httptest.NewRequest(http.MethodPut, "/api/v1/admin/teams/example-team", strings.NewReader(`{"role":"Engineer"}`))
					r.SetPathValue("name", "example-team")
					h.UpdateTeam(w, r)
				}
				if w.Code != tt.status {
					t.Fatalf("status = %d, want %d: %s", w.Code, tt.status, w.Body.String())
				}
				var response struct {
					Error struct{ Code, Message string }
				}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if response.Error.Code != tt.code {
					t.Errorf("code = %q, want %q", response.Error.Code, tt.code)
				}
				for _, want := range tt.contains {
					if !strings.Contains(response.Error.Message, want) {
						t.Errorf("message %q does not contain %q", response.Error.Message, want)
					}
				}
				if strings.Contains(w.Body.String(), "sensitive-") {
					t.Errorf("response exposed raw error details: %s", w.Body.String())
				}
			})
		}
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

func TestAdminTeamUpdatePreservesGovernance(t *testing.T) {
	team := &cetirunv1alpha1.Team{
		ObjectMeta: metav1.ObjectMeta{Name: "governed"},
		Spec: cetirunv1alpha1.TeamSpec{
			AgentPolicyRef: "governed-policy",
			SandboxRefs:    []string{"standard", "restricted"}, DefaultSandboxRef: "standard",
			MemberContext: cetirunv1alpha1.TeamContextSpec{Role: "old"},
		},
	}
	h := newAdminTeamHandler(team)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/teams/governed", bytes.NewBufferString(`{"role":"new"}`))
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
