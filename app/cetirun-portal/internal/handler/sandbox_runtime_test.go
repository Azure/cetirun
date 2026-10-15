// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/config"
)

func TestSandboxCreatePinsRuntimeBeforeFirstWrite(t *testing.T) {
	team, policy := creationPolicy()
	policy.Spec.Harnesses.Allowed = []api.AgentType{api.AgentTypeOpenClaw}
	h, _ := creationPolicyHandler(team, policy)
	w := httptest.NewRecorder()
	h.CreateInstance(w, managementRequest(http.MethodPost, `{"agent":{"type":"openclaw"},"team":"policy-team","modelAccess":{"sharedModels":true,"githubCopilot":false}}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	a, err := h.Kube.GetInstance(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	profile := h.Config.SandboxRuntimes["openclaw"]
	if a.Spec.Helm.Version != profile.ChartVersion || a.Spec.Agent.Image == nil || *a.Spec.Agent.Image != profile.Image ||
		a.Spec.Agent.Port == nil || *a.Spec.Agent.Port != profile.Port || a.Spec.SandboxRef != team.Spec.DefaultSandboxRef {
		t.Fatalf("creation did not persist the approved inputs: %+v", a.Spec)
	}
}

func TestSandboxCreateRequiresValidRuntimeWithoutWrites(t *testing.T) {
	for _, profile := range []config.SandboxRuntime{
		{},
		{ChartVersion: "latest", Image: "registry.example.com/hermes:latest", Port: 8642},
		{ChartVersion: "1.2.3", Image: "registry.example.com/hermes@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Port: 18789},
	} {
		team, policy := creationPolicy()
		h, fc := creationPolicyHandler(team, policy)
		h.Config.SandboxRuntimes["hermes"] = profile
		w := httptest.NewRecorder()
		h.CreateInstance(w, managementRequest(http.MethodPost, `{"agent":{"type":"hermes"},"team":"policy-team","gatewayToken":"synthetic-token","modelAccess":{"sharedModels":true,"githubCopilot":false}}`))
		if w.Code != http.StatusServiceUnavailable || errorCode(t, w) != "runtime_unavailable" {
			t.Fatalf("missing runtime: %d %s", w.Code, w.Body.String())
		}
		assertNoCreationWrites(t, fc)
	}
}

func TestSandboxCreatePreservesExplicitRuntimeRequests(t *testing.T) {
	team, policy := creationPolicy()
	h, _ := creationPolicyHandler(team, policy)
	w := httptest.NewRecorder()
	// The Kubernetes admission layer, not Portal defaulting, decides whether
	// this explicit image/port is qualified by the selected runtime chart.
	h.CreateInstance(w, managementRequest(http.MethodPost, `{"agent":{"type":"hermes","image":"registry.example.com/request:1","port":9000},"team":"policy-team","modelAccess":{"sharedModels":true,"githubCopilot":false}}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	a, err := h.Kube.GetInstance(context.Background(), "alice")
	if err != nil || a.Spec.Agent.Image == nil || *a.Spec.Agent.Image != "registry.example.com/request:1" || a.Spec.Agent.Port == nil || *a.Spec.Agent.Port != 9000 {
		t.Fatalf("explicit request was overwritten: %v", err)
	}
}
