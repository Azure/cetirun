// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
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
