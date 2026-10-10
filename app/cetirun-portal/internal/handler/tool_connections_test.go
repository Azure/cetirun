// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/auth"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	toolOwnerTenant = "11111111-1111-1111-1111-111111111111"
	toolOwnerObject = "22222222-2222-2222-2222-222222222222"
	toolInstanceUID = "33333333-3333-3333-3333-333333333333"
)

func toolInstance() *cetirunv1alpha1.Agent {
	return &cetirunv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{
		Name: "alice-agent", UID: types.UID(toolInstanceUID),
		Annotations: map[string]string{
			cetirunv1alpha1.AnnotationOwnerTenantID: toolOwnerTenant,
			cetirunv1alpha1.AnnotationOwnerObjectID: toolOwnerObject,
		},
	}, Spec: cetirunv1alpha1.AgentSpec{Agent: cetirunv1alpha1.HarnessSpec{Type: cetirunv1alpha1.AgentTypeHermes}}}
}

func verifiedToolRequest(request *http.Request) *http.Request {
	return request.WithContext(auth.NewContext(request.Context(), auth.Claims{TenantID: toolOwnerTenant, ObjectID: toolOwnerObject}))
}

func TestToolConnectionsListForwardsOnlyVerifiedOwner(t *testing.T) {
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/connections" || r.Method != http.MethodGet {
			t.Fatalf("unexpected Broker request: %s %s", r.Method, r.URL.Path)
		}
		for key, expected := range map[string]string{
			"X-CetiRun-Owner-Tenant-ID": toolOwnerTenant,
			"X-CetiRun-Owner-Object-ID": toolOwnerObject,
			"X-CetiRun-Catalog-Version": toolCatalogVersion,
		} {
			if got := r.Header.Get(key); got != expected {
				t.Errorf("%s = %q, want %q", key, got, expected)
			}
		}
		if r.Header.Get("Authorization") != "" {
			t.Fatal("browser authorization header was forwarded")
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer broker.Close()
	handler := NewToolConnectionsHandler(nil, broker.URL, "https://portal.example.invalid")
	handler.HTTPClient = broker.Client()
	request := verifiedToolRequest(httptest.NewRequest(http.MethodGet, "/api/v1/connections", nil))
	request.Header.Set("Authorization", "Bearer must-not-be-forwarded")
	response := httptest.NewRecorder()

	handler.ListConnections(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "[]" {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestToolConnectionsRejectsUnverifiedOwner(t *testing.T) {
	called := false
	broker := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer broker.Close()
	handler := NewToolConnectionsHandler(nil, broker.URL, "https://portal.example.invalid")
	handler.HTTPClient = broker.Client()
	response := httptest.NewRecorder()
	handler.ListConnections(response, httptest.NewRequest(http.MethodGet, "/api/v1/connections", nil))
	if response.Code != http.StatusForbidden || called {
		t.Fatalf("response = %d, downstream called = %t", response.Code, called)
	}
}

func TestToolGrantUsesOwnedInstanceUIDWithoutAgentIdentity(t *testing.T) {
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/instances/alice-agent/tool-grants" {
			t.Fatalf("unexpected Broker request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-CetiRun-Instance-UID"); got != toolInstanceUID {
			t.Fatalf("instance UID header = %q", got)
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer broker.Close()
	handler := NewToolConnectionsHandler(kube.NewClient(newFakeClient(toolInstance())), broker.URL, "https://portal.example.invalid")
	handler.HTTPClient = broker.Client()
	request := verifiedToolRequest(httptest.NewRequest(http.MethodGet, "/api/v1/instances/alice-agent/tool-grants", nil))
	request.SetPathValue("name", "alice-agent")
	response := httptest.NewRecorder()

	handler.ListToolGrants(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestToolGrantsRejectCustomAgents(t *testing.T) {
	instance := toolInstance()
	instance.Spec.Agent.Type = cetirunv1alpha1.AgentTypeCustom
	called := false
	broker := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer broker.Close()
	handler := NewToolConnectionsHandler(kube.NewClient(newFakeClient(instance)), broker.URL, "https://portal.example.invalid")
	handler.HTTPClient = broker.Client()
	request := verifiedToolRequest(httptest.NewRequest(http.MethodGet, "/api/v1/instances/alice-agent/tool-grants", nil))
	request.SetPathValue("name", "alice-agent")
	response := httptest.NewRecorder()

	handler.ListToolGrants(response, request)
	if response.Code != http.StatusBadRequest || called || !strings.Contains(response.Body.String(), "unsupported_agent_type") {
		t.Fatalf("response = %d %s, downstream called = %t", response.Code, response.Body.String(), called)
	}
}

func TestToolGrantsRejectSandboxedAgents(t *testing.T) {
	instance := toolInstance()
	instance.Spec.SandboxRef = "restricted"
	called := false
	broker := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer broker.Close()
	handler := NewToolConnectionsHandler(kube.NewClient(newFakeClient(instance)), broker.URL, "https://portal.example.invalid")
	handler.HTTPClient = broker.Client()
	request := verifiedToolRequest(httptest.NewRequest(http.MethodGet, "/api/v1/instances/alice-agent/tool-grants", nil))
	request.SetPathValue("name", "alice-agent")
	response := httptest.NewRecorder()

	handler.ListToolGrants(response, request)
	if response.Code != http.StatusBadRequest || called || !strings.Contains(response.Body.String(), "unsupported_sandbox") {
		t.Fatalf("response = %d %s, downstream called = %t", response.Code, response.Body.String(), called)
	}
}

func TestToolGrantsAllowSandboxCleanup(t *testing.T) {
	instance := toolInstance()
	instance.Spec.SandboxRef = "restricted"
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/instances/alice-agent/tool-grants/grant-1" {
			t.Fatalf("unexpected Broker request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("If-Match"); got != "7" {
			t.Fatalf("If-Match = %q, want 7", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer broker.Close()
	handler := NewToolConnectionsHandler(kube.NewClient(newFakeClient(instance)), broker.URL, "https://portal.example.invalid")
	handler.HTTPClient = broker.Client()
	request := verifiedToolRequest(httptest.NewRequest(http.MethodDelete, "/api/v1/instances/alice-agent/tool-grants/grant-1", nil))
	request.Header.Set("Origin", "https://portal.example.invalid")
	request.Header.Set("If-Match", `"7"`)
	request.SetPathValue("name", "alice-agent")
	request.SetPathValue("id", "grant-1")
	response := httptest.NewRecorder()

	handler.DeleteToolGrant(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestToolGrantUpdateRequiresRevisionAndBoundedExpiry(t *testing.T) {
	handler := NewToolConnectionsHandler(kube.NewClient(newFakeClient(toolInstance())), "http://unused.invalid", "https://portal.example.invalid")
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	handler.Now = func() time.Time { return now }
	request := verifiedToolRequest(httptest.NewRequest(http.MethodPut, "/api/v1/instances/alice-agent/tool-grants/grant-1", strings.NewReader(`{"connectionId":"conn-1","tools":["github_contents_get"],"constraints":{},"expiresAt":"2027-01-09T00:00:01Z"}`)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://portal.example.invalid")
	request.SetPathValue("name", "alice-agent")
	request.SetPathValue("id", "grant-1")
	response := httptest.NewRecorder()

	handler.UpdateToolGrant(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_expiry") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}

	request = verifiedToolRequest(httptest.NewRequest(http.MethodPut, "/api/v1/instances/alice-agent/tool-grants/grant-1", strings.NewReader(`{"connectionId":"conn-1","tools":["github_contents_get"],"constraints":{},"expiresAt":"2026-10-11T00:00:00Z"}`)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://portal.example.invalid")
	request.SetPathValue("name", "alice-agent")
	request.SetPathValue("id", "grant-1")
	response = httptest.NewRecorder()
	handler.UpdateToolGrant(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "revision_required") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestToolGrantUpdateUsesCurrentRevisionAtBrokerBoundary(t *testing.T) {
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/v1/instances/alice-agent/tool-grants/grant-1" {
			t.Fatalf("unexpected Broker request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("If-Match"); got != "7" {
			t.Fatalf("If-Match = %q, want 7", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || strings.Contains(string(body), "revision") {
			t.Fatalf("body = %q, err = %v", body, err)
		}
		_, _ = w.Write([]byte(`{"id":"grant-1","revision":8}`))
	}))
	defer broker.Close()
	handler := NewToolConnectionsHandler(kube.NewClient(newFakeClient(toolInstance())), broker.URL, "https://portal.example.invalid")
	handler.HTTPClient = broker.Client()
	handler.Now = func() time.Time { return time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC) }
	request := verifiedToolRequest(httptest.NewRequest(http.MethodPut, "/api/v1/instances/alice-agent/tool-grants/grant-1", strings.NewReader(`{"connectionId":"conn-1","tools":["github_contents_get"],"constraints":{"repositoryIds":["1"]},"expiresAt":"2026-10-11T00:00:00Z","revision":7}`)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://portal.example.invalid")
	request.SetPathValue("name", "alice-agent")
	request.SetPathValue("id", "grant-1")
	response := httptest.NewRecorder()

	handler.UpdateToolGrant(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestToolGrantsHideOtherOwners(t *testing.T) {
	instance := toolInstance()
	instance.Annotations[cetirunv1alpha1.AnnotationOwnerObjectID] = "44444444-4444-4444-4444-444444444444"
	called := false
	broker := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer broker.Close()
	handler := NewToolConnectionsHandler(kube.NewClient(newFakeClient(instance)), broker.URL, "https://portal.example.invalid")
	handler.HTTPClient = broker.Client()
	request := verifiedToolRequest(httptest.NewRequest(http.MethodGet, "/api/v1/instances/alice-agent/tool-grants", nil))
	request.SetPathValue("name", "alice-agent")
	response := httptest.NewRecorder()

	handler.ListToolGrants(response, request)
	if response.Code != http.StatusNotFound || called {
		t.Fatalf("response = %d, downstream called = %t", response.Code, called)
	}
}

func TestStartConnectionRestrictsProviderAndRequestBody(t *testing.T) {
	called := false
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost || r.URL.Path != "/v1/connections/start" {
			t.Fatalf("unexpected Broker request: %s %s", r.Method, r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != `{"provider":"github"}` {
			t.Fatalf("body = %q, err = %v", body, err)
		}
		_, _ = w.Write([]byte(`{"id":"conn-1","authorizationUrl":"https://example.invalid","expiresAt":"2026-10-10T01:00:00Z"}`))
	}))
	defer broker.Close()
	handler := NewToolConnectionsHandler(nil, broker.URL, "https://portal.example.invalid")
	handler.HTTPClient = broker.Client()
	request := verifiedToolRequest(httptest.NewRequest(http.MethodPost, "/api/v1/connections/start", strings.NewReader(`{"provider":"github"}`)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://portal.example.invalid")
	response := httptest.NewRecorder()
	handler.StartConnection(response, request)
	if response.Code != http.StatusOK || !called || !strings.Contains(response.Body.String(), `"connectionId":"conn-1"`) {
		t.Fatalf("response = %d %s, called = %t", response.Code, response.Body.String(), called)
	}

	request = verifiedToolRequest(httptest.NewRequest(http.MethodPost, "/api/v1/connections/start", strings.NewReader(`{"provider":"arbitrary"}`)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://portal.example.invalid")
	response = httptest.NewRecorder()
	handler.StartConnection(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_provider") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}
