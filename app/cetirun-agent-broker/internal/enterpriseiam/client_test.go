// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package enterpriseiam

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSubmitUsesMTLSAndExactBinding(t *testing.T) {
	request := testRequest(time.Now().Add(time.Hour))
	server, clientCertificate := newMTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/permission-requests" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Idempotency-Key") != request.ID {
			t.Fatalf("Idempotency-Key = %q", r.Header.Get("Idempotency-Key"))
		}
		if r.Header.Get("Authorization") != "" {
			t.Fatal("unexpected bearer or user authorization header")
		}
		var received PermissionRequest
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		if received.AgentUID != request.AgentUID || received.PrincipalID != request.PrincipalID {
			t.Fatalf("received unexpected binding: %#v", received)
		}
		writeDecision(t, w, request, StateApplied)
	})
	defer server.Close()

	client := newTestClient(t, server, clientCertificate)
	decision, err := client.Submit(context.Background(), request)
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if !decision.Applied(time.Now()) {
		t.Fatalf("decision = %#v, want current applied decision", decision)
	}
}

func TestRejectsCrossPrincipalResponse(t *testing.T) {
	request := testRequest(time.Now().Add(time.Hour))
	server, clientCertificate := newMTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		response := testDecision(request, StateApplied)
		response.Evidence.PrincipalID = "other-principal"
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Fatal(err)
		}
	})
	defer server.Close()

	_, err := newTestClient(t, server, clientCertificate).Submit(context.Background(), request)
	if ErrorCode(err) != string(errorBinding) {
		t.Fatalf("Submit() error = %v, code = %q; want binding mismatch", err, ErrorCode(err))
	}
}

func TestRejectsExpiredRequestBeforeNetwork(t *testing.T) {
	request := testRequest(time.Now().Add(-time.Second))
	client := &Client{now: time.Now}
	_, err := client.Submit(context.Background(), request)
	if !IsExpired(err) {
		t.Fatalf("Submit() error = %v, want expired request", err)
	}
}

func TestRejectsStaleEvidence(t *testing.T) {
	request := testRequest(time.Now().Add(time.Hour))
	server, clientCertificate := newMTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		response := testDecision(request, StateApplied)
		response.Evidence.EvaluatedAt = time.Now().Add(-2 * time.Hour)
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Fatal(err)
		}
	})
	defer server.Close()

	_, err := newTestClient(t, server, clientCertificate).Submit(context.Background(), request)
	if ErrorCode(err) != string(errorStale) {
		t.Fatalf("Submit() error = %v, code = %q; want stale evidence", err, ErrorCode(err))
	}
}

func TestCancelRequiresRevokedState(t *testing.T) {
	request := testRequest(time.Now().Add(time.Hour))
	server, clientCertificate := newMTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/permission-requests/req-123:cancel" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		writeDecision(t, w, request, StatePending)
	})
	defer server.Close()

	_, err := newTestClient(t, server, clientCertificate).Cancel(context.Background(), request)
	if ErrorCode(err) != string(errorRejected) {
		t.Fatalf("Cancel() error = %v, code = %q; want rejected", err, ErrorCode(err))
	}
}

func TestRejectsOversizedResponse(t *testing.T) {
	request := testRequest(time.Now().Add(time.Hour))
	server, clientCertificate := newMTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"padding":"` + strings.Repeat("x", 1024) + `"}`))
	})
	defer server.Close()

	client := newTestClient(t, server, clientCertificate)
	client.maxResponse = 32
	_, err := client.Submit(context.Background(), request)
	if ErrorCode(err) != string(errorTooLarge) {
		t.Fatalf("Submit() error = %v, code = %q; want response too large", err, ErrorCode(err))
	}
}

func testRequest(expiresAt time.Time) PermissionRequest {
	return PermissionRequest{
		ID:          "req-123",
		AgentUID:    "agent-uid",
		Provider:    "azure",
		Mode:        "app-only",
		TenantID:    "tenant-id",
		PrincipalID: "agent-principal",
		ClientID:    "agent-client",
		Scopes:      []string{"/subscriptions/example/resourceGroups/rg"},
		Actions:     []string{"Microsoft.Resources/subscriptions/resourceGroups/read"},
		ExpiresAt:   expiresAt,
		Requester:   "owner-id",
		Revision:    "7",
	}
}

func testDecision(request PermissionRequest, state State) Decision {
	return Decision{
		ExternalRequestID: "iam-123",
		State:             state,
		Evidence: Evidence{
			ID:          request.ID,
			AgentUID:    request.AgentUID,
			Provider:    request.Provider,
			Mode:        request.Mode,
			TenantID:    request.TenantID,
			PrincipalID: request.PrincipalID,
			ClientID:    request.ClientID,
			Scopes:      request.Scopes,
			Actions:     request.Actions,
			ExpiresAt:   request.ExpiresAt,
			Requester:   request.Requester,
			Revision:    request.Revision,
			EvaluatedAt: time.Now(),
		},
	}
}

func writeDecision(t *testing.T, writer http.ResponseWriter, request PermissionRequest, state State) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(testDecision(request, state)); err != nil {
		t.Fatal(err)
	}
}

func newTestClient(t *testing.T, server *httptest.Server, clientCertificate tls.Certificate) *Client {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	client, err := NewClient(Config{
		BaseURL: server.URL,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{clientCertificate},
			RootCAs:      pool,
			MinVersion:   tls.VersionTLS12,
		},
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func newMTLSServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, tls.Certificate) {
	t.Helper()
	clientCertificate, certificateAuthority := newClientCertificate(t)
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  certificateAuthority,
		MinVersion: tls.VersionTLS12,
	}
	server.StartTLS()
	return server, clientCertificate
}

func newClientCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "enterprise-iam-test-ca"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "enterprise-iam-test-client"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, ca, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate := tls.Certificate{Certificate: [][]byte{clientDER, caDER}, PrivateKey: clientKey}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return certificate, pool
}
