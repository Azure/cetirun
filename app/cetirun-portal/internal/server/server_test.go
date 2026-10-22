// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/Azure/CetiRun/app/cetirun-portal/internal/auth"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/handler"
)

func TestSPAHandler_ExistingFile(t *testing.T) {
	staticFS := fstest.MapFS{
		"index.html":     {Data: []byte("<html>test</html>")},
		"assets/main.js": {Data: []byte("console.log('hello')")},
	}

	fileServer := http.FileServer(http.FS(staticFS))
	handler := spaHandler(fileServer, staticFS)

	req := httptest.NewRequest(http.MethodGet, "/assets/main.js", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if w.Body.String() != "console.log('hello')" {
		t.Errorf("body = %q, want %q", w.Body.String(), "console.log('hello')")
	}
}

func TestSPAHandler_ThirdPartyNotices(t *testing.T) {
	notice := "CetiRun Portal third-party notices\nCopyright Example Authors\n"
	staticFS := fstest.MapFS{
		"index.html":               {Data: []byte("<html>SPA</html>")},
		"THIRD_PARTY_LICENSES.txt": {Data: []byte(notice)},
	}
	handler := spaHandler(http.FileServer(http.FS(staticFS)), staticFS)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/THIRD_PARTY_LICENSES.txt", nil))
	if response.Code != http.StatusOK || response.Body.String() != notice {
		t.Fatalf("notice response: status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestSPAHandler_IndexFallback(t *testing.T) {
	staticFS := fstest.MapFS{
		"index.html": {Data: []byte("<html>SPA</html>")},
	}

	fileServer := http.FileServer(http.FS(staticFS))
	handler := spaHandler(fileServer, staticFS)

	req := httptest.NewRequest(http.MethodGet, "/some/spa/route", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()
	if body != "<html>SPA</html>" {
		t.Errorf("body = %q, want index.html content", body)
	}
}

func TestSPAHandler_RootPath(t *testing.T) {
	staticFS := fstest.MapFS{
		"index.html": {Data: []byte("<html>root</html>")},
	}

	fileServer := http.FileServer(http.FS(staticFS))
	handler := spaHandler(fileServer, staticFS)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()
	if body != "<html>root</html>" {
		t.Errorf("body = %q, want index.html content", body)
	}
}

func TestOwnerSuspensionRoutesRequireAuthenticationAndVersion(t *testing.T) {
	mux := New(fstest.MapFS{}, &handler.InstanceHandler{}, &handler.HealthHandler{},
		&handler.FeaturesHandler{}, &handler.ConfigHandler{}, &handler.MeHandler{},
		&handler.AdminTeamHandler{}, &handler.AdminInstanceHandler{}, &handler.AdminPolicyHandler{},
		auth.Middleware(nil), auth.RequireAdmin(), nil, nil)
	for _, path := range []string{"/api/v1/instance/suspend", "/api/v1/instance/resume",
		"/api/v1/instances/alice/suspend", "/api/v1/instances/alice/resume"} {
		for _, signedIn := range []bool{false, true} {
			r := httptest.NewRequest(http.MethodPost, path, nil)
			want := http.StatusUnauthorized
			if signedIn {
				r.Header.Set("X-Auth-Request-Email", "alice@example.com")
				want = http.StatusPreconditionRequired
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != want {
				t.Fatalf("%s signedIn=%t: %d, want %d", path, signedIn, w.Code, want)
			}
		}
	}
}

func TestConnectionRoutesAreOptInAndAuthenticated(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		var tools *handler.ToolConnectionsHandler
		if enabled {
			tools = handler.NewToolConnectionsHandler(nil, "http://broker.example.com", "https://portal.example.com")
		}
		legacy := handler.NewAgentAuthorizationHandler(nil, "http://broker.example.com", "https://portal.example.com")
		mux := New(fstest.MapFS{}, &handler.InstanceHandler{}, &handler.HealthHandler{},
			&handler.FeaturesHandler{}, &handler.ConfigHandler{}, &handler.MeHandler{},
			&handler.AdminTeamHandler{}, &handler.AdminInstanceHandler{}, &handler.AdminPolicyHandler{},
			auth.Middleware(nil), auth.RequireAdmin(), legacy, tools)
		for _, route := range []struct{ method, path string }{
			{http.MethodGet, "/api/v1/connections"},
			{http.MethodPost, "/api/v1/instances/demo/tool-identities"},
			{http.MethodGet, "/api/v1/instances/demo/connection-bindings"},
			{http.MethodPost, "/api/v1/instances/demo/connection-bindings"},
			{http.MethodDelete, "/api/v1/instances/demo/connection-bindings/binding-id"},
		} {
			for _, signedIn := range []bool{false, true} {
				req := httptest.NewRequest(route.method, route.path, nil)
				req.Header.Set("Origin", "https://portal.example.com")
				if signedIn {
					req.Header.Set("X-Auth-Request-Email", "alice@example.com")
				}
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, req)
				want := http.StatusUnauthorized
				if signedIn {
					want = http.StatusNotFound
					if enabled {
						want = http.StatusForbidden
					}
				}
				if response.Code != want {
					t.Errorf("%s %s enabled=%v signedIn=%v: got %d want %d", route.method, route.path, enabled, signedIn, response.Code, want)
				}
			}
		}
		// Opting into the panel must not remove existing authorization routes.
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/demo/agent-authorization", nil)
		req.Header.Set("X-Auth-Request-Email", "alice@example.com")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		if response.Code != http.StatusForbidden {
			t.Errorf("legacy route enabled=%v: got %d want 403", enabled, response.Code)
		}
	}
}
