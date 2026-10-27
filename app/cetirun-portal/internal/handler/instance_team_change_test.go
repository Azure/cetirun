// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/auth"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func teamChangeHandler() *InstanceHandler {
	instance := sampleInstance()
	target := &api.Team{ObjectMeta: metav1.ObjectMeta{Name: "restricted"}, Spec: api.TeamSpec{
		AgentPolicyRef: "restricted-policy", SandboxRefs: []string{"restricted-sandbox"}, DefaultSandboxRef: "restricted-sandbox",
	}}
	sandbox := &api.Sandbox{ObjectMeta: metav1.ObjectMeta{Name: "restricted-sandbox"}}
	return &InstanceHandler{Kube: kube.NewClient(newFakeClient(instance, target, sandbox))}
}

func teamChangeRequest(method string, claims auth.Claims, body string) *http.Request {
	r := httptest.NewRequest(method, "/api/v1/instances/alice/team-change", bytes.NewBufferString(body))
	r.SetPathValue("name", "alice")
	r.Header.Set("Idempotency-Key", "team-change-0001")
	return r.WithContext(auth.NewContext(r.Context(), claims))
}

func TestTeamChangeRequestAllowsRunningOwnerAndTracksStatus(t *testing.T) {
	h := teamChangeHandler()
	body := `{"uid":"agent-alice","resourceVersion":"1","targetTeam":"restricted","targetSandbox":"restricted-sandbox"}`
	w := httptest.NewRecorder()
	h.RequestTeamChangeByName(w, teamChangeRequest(http.MethodPost, auth.Claims{Alias: "alice"}, body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var response TeamChangeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Suspended || !response.Pending || response.Team != "aks" || response.Request == nil || response.Request.TargetTeam != "restricted" {
		t.Fatalf("unexpected operation response: %+v", response)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("POST Cache-Control=%q, want no-store", got)
	}
	get := httptest.NewRecorder()
	h.GetTeamChangeByName(get, teamChangeRequest(http.MethodGet, auth.Claims{Alias: "alice"}, ""))
	if get.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", get.Code, get.Body.String())
	}
}

func TestTeamChangeModelSourceRequestDoesNotRewriteExistingBudget(t *testing.T) {
	instance := sampleInstance()
	instance.Spec.ModelAccess = &api.ModelAccessSpec{SharedModels: ptr.To(true), MonthlyBudgetUSD: "7"}
	target := &api.Team{ObjectMeta: metav1.ObjectMeta{Name: "restricted"}, Spec: api.TeamSpec{
		AgentPolicyRef: "restricted-policy", SandboxRefs: []string{"restricted-sandbox"}, DefaultSandboxRef: "restricted-sandbox",
	}}
	sandbox := &api.Sandbox{ObjectMeta: metav1.ObjectMeta{Name: "restricted-sandbox"}}
	h := &InstanceHandler{Kube: kube.NewClient(newFakeClient(instance, target, sandbox))}
	w := httptest.NewRecorder()
	h.RequestTeamChangeByName(w, teamChangeRequest(http.MethodPost, auth.Claims{Alias: "alice"},
		`{"uid":"agent-alice","resourceVersion":"1","targetTeam":"restricted","modelAccess":{"sharedModels":false,"githubCopilot":true}}`))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	stored, err := h.Kube.GetInstanceByName(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Spec.ModelAccess.MonthlyBudgetUSD != "7" {
		t.Fatalf("Portal Team change rewrote the existing budget: %+v", stored.Spec.ModelAccess)
	}
	request := stored.Spec.TeamChangeRequest
	if request == nil || request.ModelAccess == nil || request.ModelAccess.MonthlyBudgetUSD != "" ||
		ptr.Deref(request.ModelAccess.SharedModels, true) || !ptr.Deref(request.ModelAccess.GitHubCopilot, false) {
		t.Fatalf("Portal did not isolate Team-change model source selection: %+v", request)
	}
}

func TestTeamChangeRequestOnlyAllowsOwnerOrAdministrator(t *testing.T) {
	body := `{"uid":"agent-alice","resourceVersion":"1","targetTeam":"restricted"}`
	for _, claims := range []auth.Claims{{Alias: "mallory"}, {Alias: "operator", IsAdmin: true}} {
		h := teamChangeHandler()
		w := httptest.NewRecorder()
		h.RequestTeamChangeByName(w, teamChangeRequest(http.MethodPost, claims, body))
		if claims.IsAdmin && w.Code != http.StatusAccepted {
			t.Fatalf("administrator status=%d body=%s", w.Code, w.Body.String())
		}
		if !claims.IsAdmin && w.Code != http.StatusForbidden {
			t.Fatalf("outsider status=%d body=%s", w.Code, w.Body.String())
		}
	}
}

func TestTeamChangeRequestRetriesRecordedStatusBeforeTargetLookup(t *testing.T) {
	instance := sampleInstance()
	original := api.TeamChangeRequest{
		RequestID: "team-change-0001", ExpectedUID: instance.UID, ExpectedResourceVersion: instance.ResourceVersion,
		TargetTeam: "removed-team",
	}
	resolved := *original.DeepCopy()
	resolved.TargetSandbox = "removed-sandbox"
	instance.Status.TeamChange = &api.TeamChangeStatus{
		TeamChangeRequest: resolved,
		OriginalRequest:   original.DeepCopy(),
		Phase:             api.TeamChangeCompleted,
	}
	// Deliberately omit the target Team and Sandbox. A retry of the accepted
	// operation must not re-resolve resources that may since have been deleted.
	h := &InstanceHandler{Kube: kube.NewClient(newFakeClient(instance))}

	t.Run("exact owner retry returns the recorded status", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.RequestTeamChangeByName(w, teamChangeRequest(http.MethodPost, auth.Claims{Alias: "alice"},
			`{"uid":"agent-alice","resourceVersion":"1","targetTeam":"removed-team"}`))
		if w.Code != http.StatusAccepted {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var response TeamChangeResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Status == nil || response.Status.OriginalRequest == nil || response.Status.OriginalRequest.TargetSandbox != "" {
			t.Fatalf("recorded status=%+v", response.Status)
		}
	})

	t.Run("same ID with a different payload is rejected before target lookup", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.RequestTeamChangeByName(w, teamChangeRequest(http.MethodPost, auth.Claims{Alias: "alice"},
			`{"uid":"agent-alice","resourceVersion":"1","targetTeam":"removed-team","targetSandbox":"removed-sandbox"}`))
		if w.Code != http.StatusConflict || errorCode(t, w) != "team_change_pending" {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("authorization precedes recorded status lookup", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.RequestTeamChangeByName(w, teamChangeRequest(http.MethodPost, auth.Claims{Alias: "mallory"},
			`{"uid":"agent-alice","resourceVersion":"1","targetTeam":"removed-team"}`))
		if w.Code != http.StatusForbidden || errorCode(t, w) != "forbidden" {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	})
}

func TestGenericPatchRejectsTeamChange(t *testing.T) {
	h := teamChangeHandler()
	w := httptest.NewRecorder()
	h.UpdateInstanceByName(w, teamChangeRequest(http.MethodPatch, auth.Claims{Alias: "alice"}, `{"uid":"agent-alice","resourceVersion":"1","team":"restricted"}`))
	if w.Code != http.StatusBadRequest || errorCode(t, w) != "team_change_requires_operation" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestTeamChangeOperationBlocksUpdateAPI(t *testing.T) {
	h := teamChangeHandler()
	start := httptest.NewRecorder()
	h.RequestTeamChangeByName(start, teamChangeRequest(http.MethodPost, auth.Claims{Alias: "alice"},
		`{"uid":"agent-alice","resourceVersion":"1","targetTeam":"restricted"}`))
	if start.Code != http.StatusAccepted {
		t.Fatalf("start status=%d body=%s", start.Code, start.Body.String())
	}
	current, err := h.Kube.GetInstanceByName(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.UpdateInstanceByName(w, teamChangeRequest(http.MethodPatch, auth.Claims{Alias: "alice"},
		`{"uid":"`+string(current.UID)+`","resourceVersion":"`+current.ResourceVersion+`","displayName":"Renamed"}`))
	if w.Code != http.StatusConflict || errorCode(t, w) != "team_change_pending" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestTeamChangeOperationRejectsResumeAPI(t *testing.T) {
	instance := sampleInstance()
	instance.Spec.Suspended = true
	instance.Spec.TeamChangeRequest = &api.TeamChangeRequest{
		RequestID: "team-change-0001", ExpectedUID: instance.UID, ExpectedResourceVersion: instance.ResourceVersion, TargetTeam: "restricted",
	}
	h := &InstanceHandler{Kube: kube.NewClient(newFakeClient(instance))}
	w := httptest.NewRecorder()
	r := teamChangeRequest(http.MethodPost, auth.Claims{Alias: "alice"}, `{"uid":"agent-alice","resourceVersion":"1"}`)
	h.ResumeInstanceByName(w, r)
	if w.Code != http.StatusConflict || errorCode(t, w) != "team_change_pending" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
