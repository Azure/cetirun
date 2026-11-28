// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package usage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func identityEnvironment(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestWorkloadIdentityFromEnvironmentNamesWhatIsMissing(t *testing.T) {
	_, err := WorkloadIdentityFromEnvironment(identityEnvironment(map[string]string{"AZURE_TENANT_ID": "tenant"}))
	if err == nil || !strings.Contains(err.Error(), "AZURE_CLIENT_ID, AZURE_FEDERATED_TOKEN_FILE") || strings.Contains(err.Error(), "AZURE_TENANT_ID,") {
		t.Fatalf("missing variables were not named: %v", err)
	}
	if !strings.Contains(err.Error(), "azure.workload.identity/use") {
		t.Fatalf("the error does not say how the webhook sets the variables: %v", err)
	}
	complete := map[string]string{"AZURE_CLIENT_ID": "client", "AZURE_TENANT_ID": "tenant", "AZURE_FEDERATED_TOKEN_FILE": "/token"}
	identity, err := WorkloadIdentityFromEnvironment(identityEnvironment(complete))
	if err != nil || identity.Authority != "https://login.microsoftonline.com/" {
		t.Fatalf("default authority: %+v, %v", identity, err)
	}
	complete["AZURE_AUTHORITY_HOST"] = "http://login.example.com/"
	if _, err := WorkloadIdentityFromEnvironment(identityEnvironment(complete)); err == nil {
		t.Fatal("an authority without https was accepted")
	}
}

func tokenFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "azure-identity-token")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTokenExchangesTheProjectedTokenAndKeepsIt(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/tenant-id/oauth2/v2.0/token" {
			t.Errorf("token request %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		for field, want := range map[string]string{
			"client_id":             "client-id",
			"scope":                 "https://adx.example.com/.default",
			"grant_type":            "client_credentials",
			"client_assertion_type": "urn:ietf:params:oauth:client-assertion-type:jwt-bearer",
			"client_assertion":      "projected.service.account",
		} {
			if got := r.PostForm.Get(field); got != want {
				t.Errorf("%s = %q, want %q", field, got, want)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token_type":"Bearer","expires_in":3600,"access_token":"token-` + string(rune('0'+calls.Load())) + `"}`))
	}))
	defer server.Close()
	now := time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC)
	identity := &WorkloadIdentity{ClientID: "client-id", TenantID: "tenant-id", TokenFile: tokenFile(t, "projected.service.account\n"),
		Authority: server.URL + "/", HTTP: server.Client(), now: func() time.Time { return now }}

	for range 2 {
		token, err := identity.Token(context.Background(), "https://adx.example.com/")
		if err != nil || token != "token-1" {
			t.Fatalf("token = %q, %v", token, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("the token was not kept: %d requests", calls.Load())
	}
	now = now.Add(56 * time.Minute)
	if token, err := identity.Token(context.Background(), "https://adx.example.com"); err != nil || token != "token-2" {
		t.Fatalf("token near its expiry = %q, %v", token, err)
	}
}

func TestTokenNamesTheEntraErrorWithoutItsTraceIDs(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"AADSTS700213: No matching federated identity record found for presented assertion subject 'system:serviceaccount:cetirun-system:cetirun-portal'.\r\nTrace ID: 11111111 Correlation ID: 22222222 Timestamp: 2026-11-27 00:00:00Z"}`))
	}))
	defer server.Close()
	identity := &WorkloadIdentity{ClientID: "client-id", TenantID: "tenant-id", TokenFile: tokenFile(t, "assertion"), Authority: server.URL, HTTP: server.Client()}
	_, err := identity.Token(context.Background(), "https://adx.example.com")
	if err == nil || !strings.Contains(err.Error(), "HTTP 400: invalid_client: AADSTS700213: No matching federated identity record") || strings.Contains(err.Error(), "Trace ID") {
		t.Fatalf("Entra error = %v", err)
	}
}

func TestTokenNeedsTheProjectedToken(t *testing.T) {
	identity := &WorkloadIdentity{ClientID: "client-id", TenantID: "tenant-id", TokenFile: filepath.Join(t.TempDir(), "missing"), Authority: "https://login.example.com/"}
	if _, err := identity.Token(context.Background(), "https://adx.example.com"); err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Fatalf("missing token file: %v", err)
	}
}
