// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestConnectionBindingUsesOwnedInstanceUIDWithoutAgentIdentity(t *testing.T) {
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/instances/alice-agent/connection-bindings" {
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
	request := verifiedToolRequest(httptest.NewRequest(http.MethodGet, "/api/v1/instances/alice-agent/connection-bindings", nil))
	request.SetPathValue("name", "alice-agent")
	response := httptest.NewRecorder()

	handler.ListConnectionBindings(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestToolIdentityUsesOwnedInstanceUIDAndOnlyAzure(t *testing.T) {
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/instances/alice-agent/tool-identities" {
			t.Fatalf("unexpected Broker request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-CetiRun-Instance-UID"); got != toolInstanceUID {
			t.Fatalf("instance UID header = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != `{"provider":"azure"}` {
			t.Fatalf("body = %q, err = %v", body, err)
		}
		_, _ = w.Write([]byte(`{"id":"agent-identity"}`))
	}))
	defer broker.Close()
	handler := NewToolConnectionsHandler(kube.NewClient(newFakeClient(toolInstance())), broker.URL, "https://portal.example.invalid")
	handler.HTTPClient = broker.Client()
	request := verifiedToolRequest(httptest.NewRequest(http.MethodPost, "/api/v1/instances/alice-agent/tool-identities", strings.NewReader(`{"provider":"azure"}`)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://portal.example.invalid")
	request.SetPathValue("name", "alice-agent")
	response := httptest.NewRecorder()

	handler.CreateToolIdentity(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}

	request = verifiedToolRequest(httptest.NewRequest(http.MethodPost, "/api/v1/instances/alice-agent/tool-identities", strings.NewReader(`{"provider":"microsoft"}`)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://portal.example.invalid")
	request.SetPathValue("name", "alice-agent")
	response = httptest.NewRecorder()
	handler.CreateToolIdentity(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "unsupported_identity_provider") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestConnectionBindingMutationsRejectCustomAgentsButHistoryLists(t *testing.T) {
	instance := toolInstance()
	instance.Spec.Agent.Type = cetirunv1alpha1.AgentTypeCustom
	called := 0
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected Broker request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":"grant-1","revision":7}]`))
	}))
	defer broker.Close()
	handler := NewToolConnectionsHandler(kube.NewClient(newFakeClient(instance)), broker.URL, "https://portal.example.invalid")
	handler.HTTPClient = broker.Client()
	request := verifiedToolRequest(httptest.NewRequest(http.MethodGet, "/api/v1/instances/alice-agent/connection-bindings", nil))
	request.SetPathValue("name", "alice-agent")
	response := httptest.NewRecorder()

	handler.ListConnectionBindings(response, request)
	if response.Code != http.StatusOK || called != 1 || response.Header().Get("X-CetiRun-Connection-Bindings-Read-Only") != "true" {
		t.Fatalf("history response = %d %s, downstream calls = %d, read-only = %q", response.Code, response.Body.String(), called, response.Header().Get("X-CetiRun-Connection-Bindings-Read-Only"))
	}

	request = verifiedToolRequest(httptest.NewRequest(http.MethodPost, "/api/v1/instances/alice-agent/tool-identities", strings.NewReader(`{"provider":"azure"}`)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://portal.example.invalid")
	request.SetPathValue("name", "alice-agent")
	response = httptest.NewRecorder()
	handler.CreateToolIdentity(response, request)
	if response.Code != http.StatusBadRequest || called != 1 || !strings.Contains(response.Body.String(), "unsupported_agent_type") {
		t.Fatalf("mutation response = %d %s, downstream calls = %d", response.Code, response.Body.String(), called)
	}
}

func TestConnectionBindingHistoryListsSandboxedAgentsButCreationRejects(t *testing.T) {
	instance := toolInstance()
	instance.Spec.SandboxRef = "restricted"
	called := 0
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected Broker request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer broker.Close()
	handler := NewToolConnectionsHandler(kube.NewClient(newFakeClient(instance)), broker.URL, "https://portal.example.invalid")
	handler.HTTPClient = broker.Client()
	request := verifiedToolRequest(httptest.NewRequest(http.MethodGet, "/api/v1/instances/alice-agent/connection-bindings", nil))
	request.SetPathValue("name", "alice-agent")
	response := httptest.NewRecorder()

	handler.ListConnectionBindings(response, request)
	if response.Code != http.StatusOK || called != 1 || response.Header().Get("X-CetiRun-Connection-Bindings-Read-Only") != "true" {
		t.Fatalf("history response = %d %s, downstream calls = %d, read-only = %q", response.Code, response.Body.String(), called, response.Header().Get("X-CetiRun-Connection-Bindings-Read-Only"))
	}

	request = verifiedToolRequest(httptest.NewRequest(http.MethodPost, "/api/v1/instances/alice-agent/connection-bindings", nil))
	request.Header.Set("Origin", "https://portal.example.invalid")
	request.Header.Set("If-Match", "7")
	request.SetPathValue("name", "alice-agent")
	request.SetPathValue("id", "grant-1")
	response = httptest.NewRecorder()
	handler.CreateConnectionBinding(response, request)
	if response.Code != http.StatusBadRequest || called != 1 || !strings.Contains(response.Body.String(), "unsupported_sandbox") {
		t.Fatalf("verification response = %d %s, downstream calls = %d", response.Code, response.Body.String(), called)
	}
}

func TestConnectionBindingsAllowSandboxCleanup(t *testing.T) {
	instance := toolInstance()
	instance.Spec.SandboxRef = "restricted"
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/instances/alice-agent/connection-bindings/grant-1" {
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
	request := verifiedToolRequest(httptest.NewRequest(http.MethodDelete, "/api/v1/instances/alice-agent/connection-bindings/grant-1", nil))
	request.Header.Set("Origin", "https://portal.example.invalid")
	request.Header.Set("If-Match", `"7"`)
	request.SetPathValue("name", "alice-agent")
	request.SetPathValue("id", "grant-1")
	response := httptest.NewRecorder()

	handler.DeleteConnectionBinding(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestConnectionBindingUpdateRequiresRevisionAndKnownService(t *testing.T) {
	handler := NewToolConnectionsHandler(kube.NewClient(newFakeClient(toolInstance())), "http://unused.invalid", "https://portal.example.invalid")
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	handler.Now = func() time.Time { return now }
	request := verifiedToolRequest(httptest.NewRequest(http.MethodPut, "/api/v1/instances/alice-agent/connection-bindings/grant-1", strings.NewReader(`{"connectionId":"conn-1","services":["unknown_service"],"enabled":true}`)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://portal.example.invalid")
	request.SetPathValue("name", "alice-agent")
	request.SetPathValue("id", "grant-1")
	response := httptest.NewRecorder()

	handler.UpdateConnectionBinding(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_request") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}

	request = verifiedToolRequest(httptest.NewRequest(http.MethodPut, "/api/v1/instances/alice-agent/connection-bindings/grant-1", strings.NewReader(`{"connectionId":"conn-1","services":["github"],"enabled":true}`)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://portal.example.invalid")
	request.SetPathValue("name", "alice-agent")
	request.SetPathValue("id", "grant-1")
	response = httptest.NewRecorder()
	handler.UpdateConnectionBinding(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "revision_required") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestConnectionBindingUpdateUsesCurrentRevisionAtBrokerBoundary(t *testing.T) {
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/v1/instances/alice-agent/connection-bindings/grant-1" {
			t.Fatalf("unexpected Broker request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("If-Match"); got != "7" {
			t.Fatalf("If-Match = %q, want 7", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || !strings.Contains(string(body), `"revision":7`) {
			t.Fatalf("body = %q, err = %v", body, err)
		}
		_, _ = w.Write([]byte(`{"id":"grant-1","revision":8}`))
	}))
	defer broker.Close()
	handler := NewToolConnectionsHandler(kube.NewClient(newFakeClient(toolInstance())), broker.URL, "https://portal.example.invalid")
	handler.HTTPClient = broker.Client()
	handler.Now = func() time.Time { return time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC) }
	request := verifiedToolRequest(httptest.NewRequest(http.MethodPut, "/api/v1/instances/alice-agent/connection-bindings/grant-1", strings.NewReader(`{"connectionId":"conn-1","services":["github"],"enabled":true,"revision":7}`)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://portal.example.invalid")
	request.SetPathValue("name", "alice-agent")
	request.SetPathValue("id", "grant-1")
	response := httptest.NewRecorder()

	handler.UpdateConnectionBinding(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestConnectionBindingsHideOtherOwners(t *testing.T) {
	instance := toolInstance()
	instance.Annotations[cetirunv1alpha1.AnnotationOwnerObjectID] = "44444444-4444-4444-4444-444444444444"
	called := false
	broker := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer broker.Close()
	handler := NewToolConnectionsHandler(kube.NewClient(newFakeClient(instance)), broker.URL, "https://portal.example.invalid")
	handler.HTTPClient = broker.Client()
	request := verifiedToolRequest(httptest.NewRequest(http.MethodGet, "/api/v1/instances/alice-agent/connection-bindings", nil))
	request.SetPathValue("name", "alice-agent")
	response := httptest.NewRecorder()

	handler.ListConnectionBindings(response, request)
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

func TestPersonalConnectionBindingUsesStateScopedSecureCookie(t *testing.T) {
	for _, provider := range []string{"github", "google"} {
		t.Run(provider, func(t *testing.T) { testPersonalConnectionBinding(t, provider) })
	}
}

func testPersonalConnectionBinding(t *testing.T, provider string) {
	t.Helper()
	authorizationEndpoint := "https://github.com/login/oauth/authorize"
	if provider == "google" {
		authorizationEndpoint = "https://accounts.google.com/o/oauth2/v2/auth"
	}
	now := time.Date(2026, time.October, 16, 10, 0, 0, 0, time.UTC)
	state, err := newConnectionBinding()
	if err != nil {
		t.Fatal(err)
	}
	var generatedBinding string
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/connections/start":
			generatedBinding = r.Header.Get(connectionBindingHeader)
			if !validConnectionBinding(generatedBinding) || generatedBinding == "browser-spoof" {
				t.Fatalf("binding header = %q", generatedBinding)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":                     "conn-1",
				"authorizationUrl":       authorizationEndpoint + "?state=" + state,
				"expiresAt":              now.Add(5 * time.Minute).Format(time.RFC3339),
				"browserBindingRequired": true,
			})
		case "/api/v1/connections/callback":
			if got := r.Header.Get(connectionBindingHeader); got != generatedBinding {
				t.Fatalf("callback binding = %q, want generated proof", got)
			}
			if err := r.ParseForm(); err != nil || r.Form.Get("state") != state || r.Form.Get("code") != "code-1" {
				t.Fatalf("callback form = %#v, err = %v", r.Form, err)
			}
			_, _ = w.Write([]byte("<html>connected</html>"))
		default:
			t.Fatalf("unexpected Broker path %s", r.URL.Path)
		}
	}))
	defer broker.Close()
	handler := NewToolConnectionsHandler(nil, broker.URL, "https://portal.example.invalid")
	handler.HTTPClient = broker.Client()
	handler.Now = func() time.Time { return now }

	start := verifiedToolRequest(httptest.NewRequest(http.MethodPost, "/api/v1/connections/start", strings.NewReader(`{"provider":"`+provider+`"}`)))
	start.Header.Set("Content-Type", "application/json")
	start.Header.Set("Origin", "https://portal.example.invalid")
	start.Header.Set(connectionBindingHeader, "browser-spoof")
	startResponse := httptest.NewRecorder()
	handler.StartConnection(startResponse, start)
	if startResponse.Code != http.StatusOK || strings.Contains(startResponse.Body.String(), "browserBindingRequired") || strings.Contains(startResponse.Body.String(), generatedBinding) {
		t.Fatalf("start response = %d %s", startResponse.Code, startResponse.Body.String())
	}
	cookies := startResponse.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookie count = %d", len(cookies))
	}
	cookie := cookies[0]
	expectedName, ok := connectionBindingCookieName(state)
	if !ok || cookie.Name != expectedName || cookie.Value != generatedBinding || cookie.Path != "/" || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge < 1 || cookie.MaxAge > 600 {
		t.Fatalf("unsafe binding cookie: %#v", cookie)
	}

	callback := httptest.NewRequest(http.MethodGet, "/api/v1/connections/callback?state="+url.QueryEscape(state)+"&code=code-1", nil)
	callback.Header.Set(connectionBindingHeader, "browser-spoof")
	callback.AddCookie(cookie)
	callbackResponse := httptest.NewRecorder()
	handler.ConnectionCallback(callbackResponse, callback)
	if callbackResponse.Code != http.StatusOK {
		t.Fatalf("callback response = %d %s", callbackResponse.Code, callbackResponse.Body.String())
	}
	cleared := callbackResponse.Result().Cookies()
	if len(cleared) != 1 || cleared[0].Name != cookie.Name || cleared[0].MaxAge >= 0 || cleared[0].Path != "/" || !cleared[0].Secure || !cleared[0].HttpOnly || cleared[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("cleared cookie = %#v", cleared)
	}
}

func TestPersonalCallbackDoesNotForwardMissingOrWrongBindingCookie(t *testing.T) {
	state, err := newConnectionBinding()
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.Header.Get(connectionBindingHeader); got != "" {
			t.Fatalf("unexpected callback binding %q", got)
		}
		_, _ = w.Write([]byte("<html>rejected</html>"))
	}))
	defer broker.Close()
	handler := NewToolConnectionsHandler(nil, broker.URL, "https://portal.example.invalid")
	handler.HTTPClient = broker.Client()

	for _, cookie := range []*http.Cookie{nil, {Name: connectionBindingCookiePrefix + "other", Value: state, Path: "/", Secure: true, HttpOnly: true}} {
		callback := httptest.NewRequest(http.MethodGet, "/api/v1/connections/callback?state="+url.QueryEscape(state)+"&code=code-1", nil)
		callback.Header.Set(connectionBindingHeader, "browser-spoof")
		if cookie != nil {
			callback.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		handler.ConnectionCallback(response, callback)
		if response.Code != http.StatusOK {
			t.Fatalf("callback response = %d %s", response.Code, response.Body.String())
		}
		cleared := response.Result().Cookies()
		if len(cleared) != 1 || cleared[0].MaxAge >= 0 {
			t.Fatalf("cleared cookie = %#v", cleared)
		}
	}
	if requests != 2 {
		t.Fatalf("callback requests = %d", requests)
	}
}

func TestGitHubReconnectCreatesIndependentStateBoundCookies(t *testing.T) {
	now := time.Date(2026, time.October, 16, 10, 0, 0, 0, time.UTC)
	states := make([]string, 2)
	for index := range states {
		var err error
		states[index], err = newConnectionBinding()
		if err != nil {
			t.Fatal(err)
		}
	}
	call := 0
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/connections/conn-1/reconnect" || !validConnectionBinding(r.Header.Get(connectionBindingHeader)) {
			t.Fatalf("reconnect request %s binding=%q", r.URL.Path, r.Header.Get(connectionBindingHeader))
		}
		if call >= len(states) {
			t.Fatal("too many reconnect requests")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"connectionId":           "conn-1",
			"authorizationUrl":       "https://github.com/login/oauth/authorize?state=" + states[call],
			"expiresAt":              now.Add(5 * time.Minute).Format(time.RFC3339),
			"browserBindingRequired": true,
		})
		call++
	}))
	defer broker.Close()
	handler := NewToolConnectionsHandler(nil, broker.URL, "https://portal.example.invalid")
	handler.HTTPClient = broker.Client()
	handler.Now = func() time.Time { return now }

	cookieNames := make([]string, 0, len(states))
	for range states {
		request := verifiedToolRequest(httptest.NewRequest(http.MethodPost, "/api/v1/connections/conn-1/reconnect", nil))
		request.SetPathValue("id", "conn-1")
		request.Header.Set("Origin", "https://portal.example.invalid")
		response := httptest.NewRecorder()
		handler.ReconnectConnection(response, request)
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "browserBindingRequired") {
			t.Fatalf("reconnect response = %d %s", response.Code, response.Body.String())
		}
		cookies := response.Result().Cookies()
		if len(cookies) != 1 || !strings.HasPrefix(cookies[0].Name, connectionBindingCookiePrefix) || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].MaxAge < 1 || cookies[0].MaxAge > 600 {
			t.Fatalf("reconnect cookie = %#v", cookies)
		}
		cookieNames = append(cookieNames, cookies[0].Name)
	}
	if cookieNames[0] == cookieNames[1] {
		t.Fatalf("simultaneous reconnect cookies collided: %q", cookieNames[0])
	}
}

func TestToolRuntimeQualificationListsHistoryButBlocksManagement(t *testing.T) {
	instance := toolInstance()
	instance.Spec.Agent.Type = cetirunv1alpha1.AgentTypeOpenClaw
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/instances/alice-agent/connection-bindings" {
			t.Fatalf("unexpected Broker request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":"grant-1","revision":7}]`))
	}))
	defer broker.Close()
	h := NewToolConnectionsHandler(kube.NewClient(newFakeClient(instance)), broker.URL, "https://portal.example.com")
	h.HTTPClient = broker.Client()
	h.RuntimeTypes = []string{"hermes"}
	request := verifiedToolRequest(httptest.NewRequest(http.MethodGet, "/api/v1/instances/alice-agent/connection-bindings", nil))
	request.SetPathValue("name", "alice-agent")
	response := httptest.NewRecorder()
	h.ListConnectionBindings(response, request)
	if response.Code != http.StatusOK || response.Header().Get("X-CetiRun-Connection-Bindings-Read-Only") != "true" {
		t.Fatalf("history response = %d %s, read-only = %q", response.Code, response.Body.String(), response.Header().Get("X-CetiRun-Connection-Bindings-Read-Only"))
	}

	request = verifiedToolRequest(httptest.NewRequest(http.MethodPost, "/api/v1/instances/alice-agent/tool-identities", nil))
	request.SetPathValue("name", "alice-agent")
	request.Header.Set("Origin", "https://portal.example.com")
	response = httptest.NewRecorder()
	h.CreateToolIdentity(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "unsupported_agent_type") {
		t.Fatalf("management response = %d %s", response.Code, response.Body.String())
	}
}

func TestIndependentAuthorizationResponseAndUsageProxy(t *testing.T) {
	for _, operation := range []string{"start", "authorization", "usage"} {
		t.Run(operation, func(t *testing.T) {
			broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-CetiRun-Owner-Object-ID") != toolOwnerObject || r.Header.Get("Authorization") != "" {
					t.Fatal("invalid owner forwarding")
				}
				switch operation {
				case "start":
					if r.URL.Path != "/v1/connections/start" {
						t.Fatal(r.URL.Path)
					}
					_, _ = w.Write([]byte(`{"authorizationId":"oauth-123","authorizationUrl":"https://provider.example.com/authorize","expiresAt":"2026-10-19T16:00:00Z"}`))
				case "authorization":
					if r.URL.Path != "/v1/connection-authorizations/oauth-123" {
						t.Fatal(r.URL.Path)
					}
					_, _ = w.Write([]byte(`{"authorizationId":"oauth-123","status":"complete","connectionId":"canonical"}`))
				case "usage":
					if r.URL.Path != "/v1/connections/connection-123/usage" {
						t.Fatal(r.URL.Path)
					}
					_, _ = w.Write([]byte(`{"connectionId":"connection-123","agents":[],"canRemove":true}`))
				}
			}))
			defer broker.Close()
			h := NewToolConnectionsHandler(nil, broker.URL, "https://portal.example.com")
			h.HTTPClient = broker.Client()
			r := verifiedToolRequest(httptest.NewRequest("POST", "/", strings.NewReader(`{"provider":"microsoft"}`)))
			r.Header.Set("Origin", "https://portal.example.com")
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer synthetic-browser-token")
			w := httptest.NewRecorder()
			switch operation {
			case "start":
				h.StartConnection(w, r)
			case "authorization":
				r.SetPathValue("id", "oauth-123")
				h.GetConnectionAuthorization(w, r)
			case "usage":
				r.SetPathValue("id", "connection-123")
				h.GetConnectionUsage(w, r)
			}
			if w.Code != 200 {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if operation == "start" {
				var payload map[string]any
				if json.Unmarshal(w.Body.Bytes(), &payload) != nil || payload["authorizationId"] != "oauth-123" || payload["connectionId"] != nil {
					t.Fatal("authorization ID without a Connection ID was lost")
				}
			}
		})
	}
}
