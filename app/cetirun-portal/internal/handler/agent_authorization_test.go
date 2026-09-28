// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/auth"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestAgentAuthorizationCallbackPreservesSecurityHeaders(t *testing.T) {
	called := false
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost || r.URL.Path != "/internal/v1/authorization/callback" {
			t.Fatalf("unexpected callback request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Fatal("incoming Authorization header was forwarded to the Agent Identity Broker")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != "state=opaque&code=opaque" {
			t.Fatalf("callback body = %q", body)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write([]byte("callback complete"))
	}))
	defer broker.Close()

	handler := NewAgentAuthorizationHandler(nil, broker.URL, "https://portal.example.invalid")
	handler.HTTPClient = broker.Client()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-authorization/callback", strings.NewReader("state=opaque&code=opaque"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer must-not-be-forwarded")
	response := httptest.NewRecorder()
	handler.Callback(response, req)

	if !called || response.Code != http.StatusOK {
		t.Fatalf("callback status = %d, downstream called = %t", response.Code, called)
	}
	for key, expected := range map[string]string{
		"Cache-Control":           "no-store",
		"Content-Security-Policy": "default-src 'none'; script-src 'unsafe-inline'",
		"Referrer-Policy":         "no-referrer",
		"X-Content-Type-Options":  "nosniff",
	} {
		if got := response.Header().Get(key); got != expected {
			t.Errorf("%s = %q, want %q", key, got, expected)
		}
	}
}

func TestAgentAuthorizationForwardsOnlyVerifiedOwner(t *testing.T) {
	const tenantID = "11111111-1111-1111-1111-111111111111"
	const objectID = "22222222-2222-2222-2222-222222222222"
	const instanceUID = "33333333-3333-3333-3333-333333333333"
	instance := &cetirunv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alice-agent",
			UID:  types.UID(instanceUID),
			Annotations: map[string]string{
				cetirunv1alpha1.AnnotationOwnerTenantID: tenantID,
				cetirunv1alpha1.AnnotationOwnerObjectID: objectID,
			},
		},
		Spec: cetirunv1alpha1.AgentSpec{
			Agent:         cetirunv1alpha1.HarnessSpec{Type: cetirunv1alpha1.AgentTypeHermes},
			User:          cetirunv1alpha1.UserSpec{Alias: "alice", DisplayName: "Alice"},
			AgentIdentity: &cetirunv1alpha1.AgentIdentitySpec{Enabled: true},
		},
	}
	called := false
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost || r.URL.Path != "/internal/v1/instances/"+instanceUID+"/authorization/start" {
			t.Fatalf("unexpected Broker request: %s %s", r.Method, r.URL.Path)
		}
		for key, expected := range map[string]string{
			"X-CetiRun-Instance-UID":     instanceUID,
			"X-CetiRun-Instance-Name":    "alice-agent",
			"X-CetiRun-Owner-Tenant-ID":  tenantID,
			"X-CetiRun-Owner-Object-ID":  objectID,
			"X-CetiRun-Owner-Login-Hint": "opaque-login-hint",
		} {
			if got := r.Header.Get(key); got != expected {
				t.Errorf("%s = %q, want %q", key, got, expected)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"authorizationUrl":"https://login.example.invalid"}`))
	}))
	defer broker.Close()

	handler := NewAgentAuthorizationHandler(kube.NewClient(newFakeClient(instance)), broker.URL, "https://portal.example.invalid")
	handler.HTTPClient = broker.Client()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances/alice-agent/agent-authorization/start", nil)
	req.SetPathValue("name", "alice-agent")
	req.Header.Set("Origin", "https://portal.example.invalid")
	req = req.WithContext(auth.NewContext(req.Context(), auth.Claims{
		Alias: "alice", TenantID: tenantID, ObjectID: objectID, LoginHint: "opaque-login-hint",
	}))
	response := httptest.NewRecorder()

	handler.Start(response, req)

	if !called || response.Code != http.StatusOK {
		t.Fatalf("response = %d %s, Broker called = %t", response.Code, response.Body.String(), called)
	}
}

func TestAgentAuthorizationRejectsOwnerMismatchWithoutCallingBroker(t *testing.T) {
	instance := &cetirunv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alice-agent",
			UID:  types.UID("33333333-3333-3333-3333-333333333333"),
			Annotations: map[string]string{
				cetirunv1alpha1.AnnotationOwnerTenantID: "11111111-1111-1111-1111-111111111111",
				cetirunv1alpha1.AnnotationOwnerObjectID: "22222222-2222-2222-2222-222222222222",
			},
		},
		Spec: cetirunv1alpha1.AgentSpec{
			Agent:         cetirunv1alpha1.HarnessSpec{Type: cetirunv1alpha1.AgentTypeHermes},
			User:          cetirunv1alpha1.UserSpec{Alias: "alice", DisplayName: "Alice"},
			AgentIdentity: &cetirunv1alpha1.AgentIdentitySpec{Enabled: true},
		},
	}
	called := false
	broker := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer broker.Close()
	handler := NewAgentAuthorizationHandler(kube.NewClient(newFakeClient(instance)), broker.URL, "https://portal.example.invalid")
	handler.HTTPClient = broker.Client()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/alice-agent/agent-authorization", nil)
	req.SetPathValue("name", "alice-agent")
	req = req.WithContext(auth.NewContext(req.Context(), auth.Claims{
		Alias:    "alice",
		TenantID: "11111111-1111-1111-1111-111111111111",
		ObjectID: "44444444-4444-4444-4444-444444444444",
	}))
	response := httptest.NewRecorder()

	handler.Status(response, req)

	if response.Code != http.StatusForbidden || called {
		t.Fatalf("response = %d %s, Broker called = %t", response.Code, response.Body.String(), called)
	}
}

func TestAgentAuthorizationCallbackRejectsNonFormRequest(t *testing.T) {
	handler := NewAgentAuthorizationHandler(nil, "http://unused.invalid", "https://portal.example.invalid")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent-authorization/callback", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.Callback(response, req)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}
