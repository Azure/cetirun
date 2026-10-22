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
		if r.Method != http.MethodGet || r.URL.Path != "/v1/connections" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-CetiRun-Owner-Tenant-ID") != toolOwnerTenant || r.Header.Get("X-CetiRun-Owner-Object-ID") != toolOwnerObject {
			t.Fatal("verified owner was not forwarded")
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-CetiRun-Owner-Object-ID") == "browser-forged" {
			t.Fatal("browser headers were forwarded")
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer broker.Close()
	h := NewToolConnectionsHandler(nil, broker.URL, "https://portal.example.invalid")
	h.HTTPClient = broker.Client()
	r := verifiedToolRequest(httptest.NewRequest(http.MethodGet, "/api/v1/connections", nil))
	r.Header.Set("Authorization", "Bearer browser-token")
	r.Header.Set("X-CetiRun-Owner-Object-ID", "browser-forged")
	w := httptest.NewRecorder()
	h.ListConnections(w, r)
	if w.Code != http.StatusOK || w.Body.String() != "[]" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response = %d %q headers=%v", w.Code, w.Body.String(), w.Header())
	}
}

func TestToolConnectionsRequireVerifiedOwner(t *testing.T) {
	called := false
	broker := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer broker.Close()
	h := NewToolConnectionsHandler(nil, broker.URL, "https://portal.example.invalid")
	h.HTTPClient = broker.Client()
	w := httptest.NewRecorder()
	h.ListConnections(w, httptest.NewRequest(http.MethodGet, "/api/v1/connections", nil))
	if w.Code != http.StatusForbidden || called {
		t.Fatalf("response=%d downstream=%t", w.Code, called)
	}
}

func TestToolIdentityUsesKubernetesUIDAndAzureOnly(t *testing.T) {
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/instances/alice-agent/tool-identities" || r.Header.Get("X-CetiRun-Instance-UID") != toolInstanceUID {
			t.Fatalf("request=%s %s uid=%q", r.Method, r.URL.Path, r.Header.Get("X-CetiRun-Instance-UID"))
		}
		if r.Header.Get("X-CetiRun-Instance-Name") != "alice-agent" {
			t.Fatal("missing trusted instance name")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"provider":"azure"}` {
			t.Fatalf("body=%q", body)
		}
		_, _ = w.Write([]byte(`{"id":"connection-1"}`))
	}))
	defer broker.Close()
	h := NewToolConnectionsHandler(kube.NewClient(newFakeClient(toolInstance())), broker.URL, "https://portal.example.invalid")
	h.HTTPClient = broker.Client()
	r := verifiedToolRequest(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"provider":"azure"}`)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://portal.example.invalid")
	r.Header.Set("X-CetiRun-Instance-UID", "browser-forged")
	r.SetPathValue("name", "alice-agent")
	w := httptest.NewRecorder()
	h.CreateToolIdentity(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("response=%d %s", w.Code, w.Body.String())
	}

	r = verifiedToolRequest(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"provider":"github"}`)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://portal.example.invalid")
	r.SetPathValue("name", "alice-agent")
	w = httptest.NewRecorder()
	h.CreateToolIdentity(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "unsupported_identity_provider") {
		t.Fatalf("response=%d %s", w.Code, w.Body.String())
	}
}

func TestToolConnectionsHideOtherOwnersIncludingAdmins(t *testing.T) {
	instance := toolInstance()
	instance.Annotations[cetirunv1alpha1.AnnotationOwnerObjectID] = "44444444-4444-4444-4444-444444444444"
	called := false
	broker := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer broker.Close()
	h := NewToolConnectionsHandler(kube.NewClient(newFakeClient(instance)), broker.URL, "https://portal.example.invalid")
	h.HTTPClient = broker.Client()
	r := verifiedToolRequest(httptest.NewRequest(http.MethodGet, "/", nil))
	r = r.WithContext(auth.NewContext(r.Context(), auth.Claims{TenantID: toolOwnerTenant, ObjectID: toolOwnerObject, IsAdmin: true}))
	r.SetPathValue("name", "alice-agent")
	w := httptest.NewRecorder()
	h.ListConnectionBindings(w, r)
	if w.Code != http.StatusNotFound || called {
		t.Fatalf("response=%d downstream=%t", w.Code, called)
	}
}

func TestAzureBindingValidatesInputAndForwardsRevisionOnDisconnect(t *testing.T) {
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			if r.URL.Path != "/v1/instances/alice-agent/connection-bindings" {
				t.Fatal(r.URL.Path)
			}
			body, _ := io.ReadAll(r.Body)
			if string(body) != `{"connectionId":"connection-1","services":["azure_arm"],"enabled":true}` {
				t.Fatalf("body=%q", body)
			}
		case http.MethodDelete:
			if r.URL.Path != "/v1/instances/alice-agent/connection-bindings/binding-1" || r.Header.Get("If-Match") != "7" {
				t.Fatalf("delete=%s if-match=%q", r.URL.Path, r.Header.Get("If-Match"))
			}
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer broker.Close()
	h := NewToolConnectionsHandler(kube.NewClient(newFakeClient(toolInstance())), broker.URL, "https://portal.example.invalid")
	h.HTTPClient = broker.Client()
	for _, body := range []string{
		`{"connectionId":"connection-1","services":["github"],"enabled":true}`,
		`{"connectionId":"connection-1","services":["azure_arm"],"enabled":false}`,
		`{"connectionId":"connection-1","services":["azure_arm"],"enabled":true,"principalId":"forged"}`,
	} {
		r := verifiedToolRequest(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://portal.example.invalid")
		r.SetPathValue("name", "alice-agent")
		w := httptest.NewRecorder()
		h.CreateConnectionBinding(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d", body, w.Code)
		}
	}
	r := verifiedToolRequest(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"connectionId":"connection-1","services":["azure_arm"],"enabled":true}`)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://portal.example.invalid")
	r.SetPathValue("name", "alice-agent")
	w := httptest.NewRecorder()
	h.CreateConnectionBinding(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("create=%d %s", w.Code, w.Body.String())
	}

	r = verifiedToolRequest(httptest.NewRequest(http.MethodDelete, "/", nil))
	r.Header.Set("Origin", "https://portal.example.invalid")
	r.SetPathValue("name", "alice-agent")
	r.SetPathValue("id", "binding-1")
	w = httptest.NewRecorder()
	h.DeleteConnectionBinding(w, r)
	if w.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing revision=%d", w.Code)
	}
	r.Header.Set("If-Match", `"7"`)
	w = httptest.NewRecorder()
	h.DeleteConnectionBinding(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete=%d %s", w.Code, w.Body.String())
	}
}

func TestToolConnectionRequestsRejectBodiesPastLimit(t *testing.T) {
	called := false
	broker := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer broker.Close()
	h := NewToolConnectionsHandler(kube.NewClient(newFakeClient(toolInstance())), broker.URL, "https://portal.example.invalid")
	h.HTTPClient = broker.Client()

	for _, suffix := range []string{
		strings.Repeat(" ", maxToolRequestBytes),
		strings.Repeat(" ", maxToolRequestBytes) + `{"provider":"azure"}`,
	} {
		r := verifiedToolRequest(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"provider":"azure"}`+suffix)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://portal.example.invalid")
		r.SetPathValue("name", "alice-agent")
		w := httptest.NewRecorder()
		h.CreateToolIdentity(w, r)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid_request") || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("suffix=%d status=%d body=%s cache=%q", len(suffix), w.Code, w.Body.String(), w.Header().Get("Cache-Control"))
		}
	}
	if called {
		t.Fatal("oversized request reached the Broker")
	}
}

func TestUnsupportedRuntimeIsReadOnlyButAllowsCleanup(t *testing.T) {
	instance := toolInstance()
	instance.Spec.SandboxRef = "restricted"
	calls := 0
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		if r.Method != http.MethodDelete {
			t.Fatalf("unexpected method %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer broker.Close()
	h := NewToolConnectionsHandler(kube.NewClient(newFakeClient(instance)), broker.URL, "https://portal.example.invalid")
	h.HTTPClient = broker.Client()
	r := verifiedToolRequest(httptest.NewRequest(http.MethodGet, "/", nil))
	r.SetPathValue("name", "alice-agent")
	w := httptest.NewRecorder()
	h.ListConnectionBindings(w, r)
	if w.Code != http.StatusOK || w.Header().Get(connectionBindingsReadOnly) != "true" || calls != 1 {
		t.Fatalf("list=%d readonly=%q calls=%d", w.Code, w.Header().Get(connectionBindingsReadOnly), calls)
	}
	r = verifiedToolRequest(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"provider":"azure"}`)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://portal.example.invalid")
	r.SetPathValue("name", "alice-agent")
	w = httptest.NewRecorder()
	h.CreateToolIdentity(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "unsupported_sandbox") || calls != 1 {
		t.Fatalf("create=%d calls=%d", w.Code, calls)
	}
	r = verifiedToolRequest(httptest.NewRequest(http.MethodDelete, "/", nil))
	r.Header.Set("Origin", "https://portal.example.invalid")
	r.Header.Set("If-Match", "4")
	r.SetPathValue("name", "alice-agent")
	r.SetPathValue("id", "old-binding")
	w = httptest.NewRecorder()
	h.DeleteConnectionBinding(w, r)
	if w.Code != http.StatusNoContent || calls != 2 {
		t.Fatalf("delete=%d calls=%d", w.Code, calls)
	}
}

func TestConnectionMutationsRequireAllowedOrigin(t *testing.T) {
	called := false
	broker := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer broker.Close()
	h := NewToolConnectionsHandler(kube.NewClient(newFakeClient(toolInstance())), broker.URL, "https://portal.example.invalid")
	h.HTTPClient = broker.Client()
	r := verifiedToolRequest(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"provider":"azure"}`)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://attacker.example.invalid")
	r.SetPathValue("name", "alice-agent")
	w := httptest.NewRecorder()
	h.CreateToolIdentity(w, r)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "invalid_origin") || called {
		t.Fatalf("response=%d downstream=%t", w.Code, called)
	}
}

func TestConnectionProxyBoundsInvalidResponsesAndDoesNotFollowRedirects(t *testing.T) {
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/connections" {
			http.Redirect(w, r, "/redirect-target", http.StatusFound)
			return
		}
		t.Fatal("proxy followed redirect")
	}))
	defer redirect.Close()
	h := NewToolConnectionsHandler(nil, redirect.URL, "https://portal.example.invalid")
	w := httptest.NewRecorder()
	h.ListConnections(w, verifiedToolRequest(httptest.NewRequest(http.MethodGet, "/", nil)))
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "dependency_invalid") {
		t.Fatalf("redirect status=%d body=%s", w.Code, w.Body.String())
	}

	oversized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, maxToolBrokerResponseBytes+1))
	}))
	defer oversized.Close()
	h = NewToolConnectionsHandler(nil, oversized.URL, "https://portal.example.invalid")
	w = httptest.NewRecorder()
	h.ListConnections(w, verifiedToolRequest(httptest.NewRequest(http.MethodGet, "/", nil)))
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "dependency_invalid") {
		t.Fatalf("oversized status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestToolConnectionsPreserveBrokerConflicts(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		method     string
	}{
		{"identity not ready", `{"error":{"code":"identity_not_ready","message":"Agent identity is not ready"}}`, http.MethodPost},
		{"stale revision", `{"error":{"code":"revision_conflict","message":"Binding changed"}}`, http.MethodDelete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer broker.Close()
			h := NewToolConnectionsHandler(kube.NewClient(newFakeClient(toolInstance())), broker.URL, "https://portal.example.invalid")
			r := verifiedToolRequest(httptest.NewRequest(tc.method, "/", strings.NewReader(`{"provider":"azure"}`)))
			r.Header.Set("Origin", "https://portal.example.invalid")
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("If-Match", "1")
			r.SetPathValue("name", "alice-agent")
			r.SetPathValue("id", "binding-1")
			w := httptest.NewRecorder()
			if tc.method == http.MethodPost {
				h.CreateToolIdentity(w, r)
			} else {
				h.DeleteConnectionBinding(w, r)
			}
			if w.Code != http.StatusConflict || w.Body.String() != tc.body || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("response=%d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestToolConnectionProxyTimeout(t *testing.T) {
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer broker.Close()
	h := NewToolConnectionsHandler(nil, broker.URL, "https://portal.example.invalid")
	h.HTTPClient.Timeout = 20 * time.Millisecond
	w := httptest.NewRecorder()
	h.ListConnections(w, verifiedToolRequest(httptest.NewRequest(http.MethodGet, "/", nil)))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "dependency_unavailable") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response=%d %s", w.Code, w.Body.String())
	}
}
