// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

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

func TestInternalGovernanceObservationRequiresProjectedServiceAccountToken(t *testing.T) {
	// This checks the route boundary itself: the internal mux must not expose a
	// read without first reaching CetiAdmin's projected-token authentication.
	mux := NewInternalMux(&handler.InstanceHandler{}, handler.NewCetiAdminHandler(nil, "cetirun-governance-actions", []string{"system:serviceaccount:cetirun-system:cetiadmin"}))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/internal/v1/agents/example/governance", nil))
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
