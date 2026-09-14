// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteError(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, http.StatusUnauthorized, "test_code", "test message")

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}

	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}

	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}

	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatal("response missing 'error' field")
	}
	if errObj["code"] != "test_code" {
		t.Errorf("error code = %v, want %q", errObj["code"], "test_code")
	}
	if errObj["message"] != "test message" {
		t.Errorf("error message = %v, want %q", errObj["message"], "test message")
	}
}

func TestMiddleware_MissingAuthHeaders(t *testing.T) {
	handler := Middleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestMiddleware_ValidAuthHeaders(t *testing.T) {
	var capturedClaims Claims
	handler := Middleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := ClaimsFromContext(r.Context())
		if !ok {
			t.Error("claims not found in context")
		}
		capturedClaims = claims
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("X-Auth-Request-Preferred-Username", "alice@example.com")
	req.Header.Set("X-Auth-Request-Email", "alice@example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if capturedClaims.Alias != "alice" {
		t.Errorf("alias = %q, want %q", capturedClaims.Alias, "alice")
	}
	if capturedClaims.Email != "alice@example.com" {
		t.Errorf("email = %q, want %q", capturedClaims.Email, "alice@example.com")
	}
	if capturedClaims.DisplayName != "alice@example.com" {
		t.Errorf("displayName = %q, want %q", capturedClaims.DisplayName, "alice@example.com")
	}
}

func TestMiddleware_InvalidAlias(t *testing.T) {
	handler := Middleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("X-Auth-Request-Preferred-Username", "123invalid@example.com") // starts with number
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}
