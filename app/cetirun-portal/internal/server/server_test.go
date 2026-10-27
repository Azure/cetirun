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

func TestConnectionRoutesAreOptInAndAuthenticated(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		var tools *handler.ToolConnectionsHandler
		if enabled {
			tools = handler.NewToolConnectionsHandler(nil, "http://broker.example.com", "https://portal.example.com")
		}
		mux := New(fstest.MapFS{}, &handler.InstanceHandler{}, &handler.HealthHandler{},
			&handler.FeaturesHandler{}, &handler.ConfigHandler{}, &handler.MeHandler{},
			&handler.AdminTeamHandler{}, &handler.AdminInstanceHandler{}, &handler.AdminPolicyHandler{},
			auth.Middleware(nil), auth.RequireAdmin(), tools)
		for _, route := range []struct{ method, path string }{
			{http.MethodGet, "/api/v1/connections"},
			{http.MethodPost, "/api/v1/connections/start"},
			{http.MethodGet, "/api/v1/connections/id"},
			{http.MethodGet, "/api/v1/connections/id/usage"},
			{http.MethodGet, "/api/v1/connection-authorizations/id"},
			{http.MethodDelete, "/api/v1/connections/id"},
			{http.MethodPost, "/api/v1/connections/id/reconnect"},
			{http.MethodPut, "/api/v1/instances/demo/connection-bindings/binding-id"},
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
		// The final cutover removes the legacy authorization API.
		req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/demo/agent-authorization", nil)
		req.Header.Set("X-Auth-Request-Email", "alice@example.com")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		if response.Code != http.StatusNotFound {
			t.Errorf("legacy route enabled=%v: got %d want 404", enabled, response.Code)
		}
	}
}

func TestAdminAgentManagementRoutesRequireAuthenticatedAdministrator(t *testing.T) {
	mux := New(fstest.MapFS{}, &handler.InstanceHandler{}, &handler.HealthHandler{},
		&handler.FeaturesHandler{}, &handler.ConfigHandler{}, &handler.MeHandler{},
		&handler.AdminTeamHandler{}, &handler.AdminInstanceHandler{}, &handler.AdminPolicyHandler{},
		auth.Middleware(auth.NewAdminSet([]string{"administrator"})), auth.RequireAdmin())

	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/admin/instances/foreign-agent"},
		{http.MethodGet, "/api/v1/admin/instances/foreign-agent/status"},
		{http.MethodPatch, "/api/v1/admin/instances/foreign-agent"},
		{http.MethodDelete, "/api/v1/admin/instances/foreign-agent"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			unauthenticated := httptest.NewRecorder()
			mux.ServeHTTP(unauthenticated, httptest.NewRequest(route.method, route.path, nil))
			if unauthenticated.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated status=%d want=%d", unauthenticated.Code, http.StatusUnauthorized)
			}

			nonAdminRequest := httptest.NewRequest(route.method, route.path, nil)
			nonAdminRequest.Header.Set("X-Auth-Request-Email", "owner@example.com")
			nonAdmin := httptest.NewRecorder()
			mux.ServeHTTP(nonAdmin, nonAdminRequest)
			if nonAdmin.Code != http.StatusForbidden {
				t.Fatalf("non-admin status=%d want=%d body=%s", nonAdmin.Code, http.StatusForbidden, nonAdmin.Body.String())
			}
		})
	}
}
