// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testAgentName       = "agent-a"
	testAgentUID        = "11111111-1111-1111-1111-111111111111"
	testOwnerTenantID   = "22222222-2222-2222-2222-222222222222"
	testOwnerObjectID   = "33333333-3333-3333-3333-333333333333"
	testAgentClientID   = "44444444-4444-4444-4444-444444444444"
	testBlueprintID     = "55555555-5555-5555-5555-555555555555"
	testAuthorizationID = "66666666-6666-6666-6666-666666666666"
)

type recordAPIFixture struct {
	t               *testing.T
	mu              sync.Mutex
	secret          *secretDocument
	resourceVersion int
	agentPhase      string
}

func (f *recordAPIFixture) handler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/apis/cetirun.microsoft.com/v1alpha1/agents" {
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{f.agentDocument()}})
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/apis/cetirun.microsoft.com/v1alpha1/agents/"+testAgentName {
		_ = json.NewEncoder(w).Encode(f.agentDocument())
		return
	}
	collection := "/api/v1/namespaces/cetirun-credentials/secrets"
	item := collection + "/agent-authorization-" + testAgentUID
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == collection:
		items := []secretDocument{}
		if f.secret != nil {
			items = append(items, *f.secret)
		}
		_ = json.NewEncoder(w).Encode(secretList{Items: items})
	case r.Method == http.MethodGet && r.URL.Path == item:
		if f.secret == nil {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(f.secret)
	case r.Method == http.MethodPost && r.URL.Path == collection:
		if f.secret != nil {
			http.Error(w, "conflict", http.StatusConflict)
			return
		}
		f.storeSecret(w, r, http.StatusCreated)
	case r.Method == http.MethodPut && r.URL.Path == item:
		var incoming secretDocument
		if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
			f.t.Errorf("decode Secret: %v", err)
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		if f.secret == nil || incoming.Metadata.ResourceVersion != f.secret.Metadata.ResourceVersion {
			http.Error(w, "conflict", http.StatusConflict)
			return
		}
		f.resourceVersion++
		incoming.Metadata.ResourceVersion = strconv.Itoa(f.resourceVersion)
		f.secret = &incoming
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}
}

func (f *recordAPIFixture) storeSecret(w http.ResponseWriter, r *http.Request, status int) {
	var incoming secretDocument
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		f.t.Errorf("decode Secret: %v", err)
		http.Error(w, "invalid", http.StatusBadRequest)
		return
	}
	f.resourceVersion++
	incoming.Metadata.ResourceVersion = strconv.Itoa(f.resourceVersion)
	f.secret = &incoming
	w.WriteHeader(status)
}

func (f *recordAPIFixture) agentDocument() map[string]any {
	phase := f.agentPhase
	if phase == "" {
		phase = "Ready"
	}
	return map[string]any{
		"metadata": map[string]any{
			"name": testAgentName, "uid": testAgentUID,
			"annotations": map[string]string{
				"agentidentity.cetirun.microsoft.com/owner-tenant-id": testOwnerTenantID,
				"agentidentity.cetirun.microsoft.com/owner-object-id": testOwnerObjectID,
			},
		},
		"spec": map[string]any{
			"user":          map[string]any{"alias": "owner"},
			"agentIdentity": map[string]any{"enabled": true},
		},
		"status": map[string]any{
			"phase": phase, "namespace": "cetirun-owner",
			"agentIdentity": map[string]any{"phase": "Ready", "clientId": testAgentClientID},
		},
	}
}

func newRecordTestStore(t *testing.T) (*kubeClient, *recordAPIFixture, func()) {
	t.Helper()
	fixture := &recordAPIFixture{t: t}
	server := httptest.NewServer(http.HandlerFunc(fixture.handler))
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("synthetic-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &kubeClient{
		baseURL: server.URL, namespace: "cetirun-credentials", tokenFile: tokenFile,
		client: server.Client(), wrapper: memoryWrapper{key: []byte("unit-test-key")},
		blueprintClientID: testBlueprintID, authorizationClientID: testAuthorizationID,
	}
	return store, fixture, server.Close
}

func connectedRecord() *AuthorizationRecord {
	return &AuthorizationRecord{
		InstanceUID: testAgentUID, InstanceName: testAgentName,
		OwnerTenantID: testOwnerTenantID, OwnerObjectID: testOwnerObjectID,
		AgentIdentityClientID: testAgentClientID, Generation: 7,
		Tokens: &TokenCache{AccessToken: "user-access-token", RefreshToken: "refresh-token", ExpiresAt: time.Now().Add(time.Hour)},
	}
}

func TestPortalStartCannotReviveAuthorizationAfterAgentBeginsDeleting(t *testing.T) {
	const (
		name       = "agent-a"
		uid        = "11111111-1111-1111-1111-111111111111"
		tenant     = "22222222-2222-2222-2222-222222222222"
		owner      = "33333333-3333-3333-3333-333333333333"
		agentID    = "44444444-4444-4444-4444-444444444444"
		blueprint  = "55555555-5555-5555-5555-555555555555"
		authClient = "66666666-6666-6666-6666-666666666666"
	)
	reads, writes := 0, 0
	kubeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/cetirun.microsoft.com/v1alpha1/agents/"+name:
			reads++
			phase := "Ready"
			if reads > 1 {
				phase = "Deleting"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"metadata": map[string]any{
					"name": name, "uid": uid,
					"annotations": map[string]string{
						"agentidentity.cetirun.microsoft.com/owner-tenant-id": tenant,
						"agentidentity.cetirun.microsoft.com/owner-object-id": owner,
					},
				},
				"spec": map[string]any{"agentIdentity": map[string]any{"enabled": true}},
				"status": map[string]any{
					"phase":         "Ready",
					"namespace":     "cetirun-agent-a",
					"agentIdentity": map[string]any{"phase": phase, "clientId": agentID},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/namespaces/cetirun-credentials/secrets/agent-authorization-"+uid:
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/namespaces/cetirun-credentials/secrets":
			writes++
			w.WriteHeader(http.StatusCreated)
		default:
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer kubeAPI.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("synthetic-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &kubeClient{
		baseURL: kubeAPI.URL, namespace: "cetirun-credentials", tokenFile: tokenFile,
		client: kubeAPI.Client(), wrapper: memoryWrapper{key: []byte("unit-test-key")},
		blueprintClientID: blueprint, authorizationClientID: authClient,
	}
	server := &Server{
		cfg:   Config{TransactionTTL: time.Minute},
		store: store, mux: http.NewServeMux(),
	}
	server.routes()
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/instances/"+uid+"/authorization/start", nil)
	req.Header.Set("X-CetiRun-Instance-UID", uid)
	req.Header.Set("X-CetiRun-Instance-Name", name)
	req.Header.Set("X-CetiRun-Owner-Tenant-ID", tenant)
	req.Header.Set("X-CetiRun-Owner-Object-ID", owner)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusConflict)
	}
	if reads != 2 || writes != 0 {
		t.Fatalf("Agent reads = %d, Secret writes = %d; expected revalidation without write", reads, writes)
	}
}

func TestPortalEndPersistsTombstoneWhenAuthorizationRecordIsMissing(t *testing.T) {
	store, _, closeServer := newRecordTestStore(t)
	defer closeServer()
	server := &Server{cfg: Config{}, store: store, mux: http.NewServeMux()}
	server.routes()
	req := httptest.NewRequest(http.MethodDelete, "/internal/v1/instances/"+testAgentUID+"/authorization", nil)
	req.Header.Set("X-CetiRun-Instance-UID", testAgentUID)
	req.Header.Set("X-CetiRun-Instance-Name", testAgentName)
	req.Header.Set("X-CetiRun-Owner-Tenant-ID", testOwnerTenantID)
	req.Header.Set("X-CetiRun-Owner-Object-ID", testOwnerObjectID)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	record, _, err := store.getRecord(context.Background(), testAgentUID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Generation != 1 || !record.Revoked || record.Transaction != nil || record.Tokens != nil {
		t.Fatalf("End did not create a clean generation tombstone: %#v", record)
	}
}

func TestPortalEndRevokesAuthorizationWhenAgentIsNotReady(t *testing.T) {
	store, fixture, closeServer := newRecordTestStore(t)
	defer closeServer()
	if err := store.putRecord(context.Background(), connectedRecord(), ""); err != nil {
		t.Fatal(err)
	}
	fixture.agentPhase = "Failed"
	server := &Server{cfg: Config{}, store: store, mux: http.NewServeMux()}
	server.routes()
	req := httptest.NewRequest(http.MethodDelete, "/internal/v1/instances/"+testAgentUID+"/authorization", nil)
	req.Header.Set("X-CetiRun-Instance-UID", testAgentUID)
	req.Header.Set("X-CetiRun-Instance-Name", testAgentName)
	req.Header.Set("X-CetiRun-Owner-Tenant-ID", testOwnerTenantID)
	req.Header.Set("X-CetiRun-Owner-Object-ID", testOwnerObjectID)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	record, _, err := store.getRecord(context.Background(), testAgentUID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Generation != 8 || !record.Revoked || record.Tokens != nil {
		t.Fatalf("End did not revoke the authorization: %#v", record)
	}
}

func TestRevocationRejectsStaleCallbackAndRefreshWrites(t *testing.T) {
	for _, mutation := range []struct {
		name  string
		apply func(*AuthorizationRecord)
	}{
		{name: "callback", apply: func(record *AuthorizationRecord) {
			record.Transaction = &Transaction{StateHash: stateHash(strings.Repeat("a", 43)), Consumed: true}
			record.Tokens = &TokenCache{AccessToken: "callback-access", RefreshToken: "callback-refresh", ExpiresAt: time.Now().Add(time.Hour)}
		}},
		{name: "refresh", apply: func(record *AuthorizationRecord) {
			record.Tokens.AccessToken = "refreshed-access"
			record.Tokens.RefreshToken = "refreshed-refresh"
		}},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			store, _, closeServer := newRecordTestStore(t)
			defer closeServer()
			if err := store.putRecord(context.Background(), connectedRecord(), ""); err != nil {
				t.Fatal(err)
			}
			stale, staleVersion, err := store.getRecord(context.Background(), testAgentUID)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.revokeRecord(context.Background(), Agent{
				Name: testAgentName, UID: testAgentUID, OwnerTenantID: testOwnerTenantID,
				OwnerObjectID: testOwnerObjectID, AgentIdentityClientID: testAgentClientID,
			}); err != nil {
				t.Fatal(err)
			}
			mutation.apply(stale)
			if err := store.putRecord(context.Background(), stale, staleVersion); !errors.Is(err, errConflict) {
				t.Fatalf("stale %s write error = %v, want conflict", mutation.name, err)
			}
			current, _, err := store.getRecord(context.Background(), testAgentUID)
			if err != nil {
				t.Fatal(err)
			}
			if current.Generation != 8 || !current.Revoked || current.Tokens != nil {
				t.Fatalf("stale %s write changed tombstone: %#v", mutation.name, current)
			}
		})
	}
}

func TestCredentialIsNotReturnedWhenConnectionEndsDuringExchange(t *testing.T) {
	store, _, closeKube := newRecordTestStore(t)
	defer closeKube()
	if err := store.putRecord(context.Background(), connectedRecord(), ""); err != nil {
		t.Fatal(err)
	}
	authSDK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Validate":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tid": testOwnerTenantID, "oid": testOwnerObjectID, "aud": testBlueprintID,
				"azp": testAuthorizationID, "scp": "access_agent",
			})
		case "/AuthorizationHeader/AzureResourceManager":
			if err := store.revokeRecord(context.Background(), Agent{
				Name: testAgentName, UID: testAgentUID, OwnerTenantID: testOwnerTenantID,
				OwnerObjectID: testOwnerObjectID, AgentIdentityClientID: testAgentClientID,
			}); err != nil {
				t.Error(err)
				http.Error(w, "revoke failed", http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"authorizationHeader": "Bearer delegated-token"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer authSDK.Close()
	server := &Server{
		cfg:     Config{BlueprintClientID: testBlueprintID, AuthorizationClientID: testAuthorizationID},
		store:   store,
		authSDK: &authSDKClient{baseURL: authSDK.URL, client: authSDK.Client()},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/issue", strings.NewReader(`{"service":"azure-resource-manager"}`))
	req.Header.Set("X-Forwarded-Client-Cert", "URI=spiffe://cluster.local/ns/cetirun-owner/sa/openclaw-agent-a-cetirun")
	response := httptest.NewRecorder()
	server.issueCredential(response, req)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusUnauthorized, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "delegated-token") {
		t.Fatal("credential escaped after the authorization generation was revoked")
	}
}

func TestCredentialIsNotReturnedWhenConnectionEndsDuringFinalWrite(t *testing.T) {
	store, _, closeKube := newRecordTestStore(t)
	defer closeKube()
	if err := store.putRecord(context.Background(), connectedRecord(), ""); err != nil {
		t.Fatal(err)
	}
	authSDK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Validate":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tid": testOwnerTenantID, "oid": testOwnerObjectID, "aud": testBlueprintID,
				"azp": testAuthorizationID, "scp": "access_agent",
			})
		case "/AuthorizationHeader/AzureResourceManager":
			_ = json.NewEncoder(w).Encode(map[string]string{"authorizationHeader": "Bearer delegated-token"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer authSDK.Close()
	revokeStore := *store
	baseTransport := store.client.Transport
	var revokeOnce sync.Once
	store.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var revokeErr error
		if r.Method == http.MethodPut {
			revokeOnce.Do(func() {
				revokeErr = revokeStore.revokeRecord(r.Context(), Agent{
					Name: testAgentName, UID: testAgentUID, OwnerTenantID: testOwnerTenantID,
					OwnerObjectID: testOwnerObjectID, AgentIdentityClientID: testAgentClientID,
				})
			})
			if revokeErr != nil {
				return nil, revokeErr
			}
		}
		return baseTransport.RoundTrip(r)
	})}
	server := &Server{
		cfg:     Config{BlueprintClientID: testBlueprintID, AuthorizationClientID: testAuthorizationID},
		store:   store,
		authSDK: &authSDKClient{baseURL: authSDK.URL, client: authSDK.Client()},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/issue", strings.NewReader(`{"service":"azure-resource-manager"}`))
	req.Header.Set("X-Forwarded-Client-Cert", "URI=spiffe://cluster.local/ns/cetirun-owner/sa/openclaw-agent-a-cetirun")
	response := httptest.NewRecorder()
	server.issueCredential(response, req)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusUnauthorized, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "delegated-token") {
		t.Fatal("credential escaped after revocation won the final write")
	}
}

func TestCredentialIsReturnedWhenConcurrentAuditWriteWins(t *testing.T) {
	store, _, closeKube := newRecordTestStore(t)
	defer closeKube()
	if err := store.putRecord(context.Background(), connectedRecord(), ""); err != nil {
		t.Fatal(err)
	}
	authSDK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Validate":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tid": testOwnerTenantID, "oid": testOwnerObjectID, "aud": testBlueprintID,
				"azp": testAuthorizationID, "scp": "access_agent",
			})
		case "/AuthorizationHeader/AzureResourceManager":
			_ = json.NewEncoder(w).Encode(map[string]string{"authorizationHeader": "Bearer delegated-token"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer authSDK.Close()
	concurrentStore := *store
	baseTransport := store.client.Transport
	var updateOnce sync.Once
	store.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var updateErr error
		if r.Method == http.MethodPut {
			updateOnce.Do(func() {
				record, version, err := concurrentStore.getRecord(r.Context(), testAgentUID)
				if err != nil {
					updateErr = err
					return
				}
				now := time.Now().UTC()
				record.LastUsed = &now
				updateErr = concurrentStore.putRecord(r.Context(), record, version)
			})
			if updateErr != nil {
				return nil, updateErr
			}
		}
		return baseTransport.RoundTrip(r)
	})}
	server := &Server{
		cfg:     Config{BlueprintClientID: testBlueprintID, AuthorizationClientID: testAuthorizationID},
		store:   store,
		authSDK: &authSDKClient{baseURL: authSDK.URL, client: authSDK.Client()},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/issue", strings.NewReader(`{"service":"azure-resource-manager"}`))
	req.Header.Set("X-Forwarded-Client-Cert", "URI=spiffe://cluster.local/ns/cetirun-owner/sa/openclaw-agent-a-cetirun")
	response := httptest.NewRecorder()
	server.issueCredential(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "delegated-token") {
		t.Fatal("credential was lost after a concurrent audit update")
	}
}

func TestCredentialAdoptsConcurrentRefreshOfSameGeneration(t *testing.T) {
	store, _, closeKube := newRecordTestStore(t)
	defer closeKube()
	record := connectedRecord()
	record.Tokens.ExpiresAt = time.Now().Add(time.Minute)
	if err := store.putRecord(context.Background(), record, ""); err != nil {
		t.Fatal(err)
	}
	assertionFile := filepath.Join(t.TempDir(), "federated-token")
	if err := os.WriteFile(assertionFile, []byte("synthetic-assertion"), 0o600); err != nil {
		t.Fatal(err)
	}
	oauth := &oauthClient{
		cfg: Config{
			TenantID: testOwnerTenantID, AuthorizationClientID: testAuthorizationID,
			FederatedTokenFile: assertionFile,
		},
		client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{"access_token":"losing-refresh","refresh_token":"losing-refresh-token","expires_in":3600}`)),
			}, nil
		})},
	}
	authSDK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Validate":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tid": testOwnerTenantID, "oid": testOwnerObjectID, "aud": testBlueprintID,
				"azp": testAuthorizationID, "scp": "access_agent",
			})
		case "/AuthorizationHeader/AzureResourceManager":
			if got := r.Header.Get("Authorization"); got != "Bearer winning-refresh" {
				t.Errorf("Auth SDK Authorization = %q, want concurrent winning token", got)
				http.Error(w, "unexpected synthetic token", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"authorizationHeader": "Bearer delegated-token"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer authSDK.Close()
	concurrentStore := *store
	baseTransport := store.client.Transport
	var refreshOnce sync.Once
	store.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var refreshErr error
		if r.Method == http.MethodPut {
			refreshOnce.Do(func() {
				latest, version, err := concurrentStore.getRecord(r.Context(), testAgentUID)
				if err != nil {
					refreshErr = err
					return
				}
				latest.Tokens = &TokenCache{
					AccessToken: "winning-refresh", RefreshToken: "winning-refresh-token",
					ExpiresAt: time.Now().Add(time.Hour),
				}
				refreshErr = concurrentStore.putRecord(r.Context(), latest, version)
			})
			if refreshErr != nil {
				return nil, refreshErr
			}
		}
		return baseTransport.RoundTrip(r)
	})}
	server := &Server{
		cfg:   Config{BlueprintClientID: testBlueprintID, AuthorizationClientID: testAuthorizationID},
		store: store, oauth: oauth,
		authSDK: &authSDKClient{baseURL: authSDK.URL, client: authSDK.Client()},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/issue", strings.NewReader(`{"service":"azure-resource-manager"}`))
	req.Header.Set("X-Forwarded-Client-Cert", "URI=spiffe://cluster.local/ns/cetirun-owner/sa/openclaw-agent-a-cetirun")
	response := httptest.NewRecorder()
	server.issueCredential(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "delegated-token") {
		t.Fatal("credential was lost after adopting a concurrent refresh")
	}
}

func TestCredentialIssueReturnsOnlyDownstreamToken(t *testing.T) {
	store, _, closeKube := newRecordTestStore(t)
	defer closeKube()
	if err := store.putRecord(context.Background(), connectedRecord(), ""); err != nil {
		t.Fatal(err)
	}
	authSDK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Validate":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tid": testOwnerTenantID, "oid": testOwnerObjectID, "aud": testBlueprintID,
				"azp": testAuthorizationID, "scp": "access_agent",
			})
		case "/AuthorizationHeader/AzureResourceManager":
			_ = json.NewEncoder(w).Encode(map[string]string{"authorizationHeader": "Bearer delegated-token"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer authSDK.Close()
	server := &Server{
		cfg:     Config{BlueprintClientID: testBlueprintID, AuthorizationClientID: testAuthorizationID},
		store:   store,
		authSDK: &authSDKClient{baseURL: authSDK.URL, client: authSDK.Client()},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/issue", strings.NewReader(`{"service":"azure-resource-manager"}`))
	req.Header.Set("X-Forwarded-Client-Cert", "URI=spiffe://cluster.local/ns/cetirun-owner/sa/openclaw-agent-a-cetirun")
	response := httptest.NewRecorder()
	requestSecurityHeaders(http.HandlerFunc(server.issueCredential)).ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("credential response Cache-Control = %q, want no-store", response.Header().Get("Cache-Control"))
	}
	var payload map[string]string
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload["tokenType"] != "Bearer" || payload["accessToken"] != "delegated-token" || payload["correlationId"] == "" {
		t.Fatalf("unexpected credential response: %#v", payload)
	}
	if _, found := payload["authorizationHeader"]; found {
		t.Fatal("response exposed a reusable authorization header")
	}
	record, _, err := store.getRecord(context.Background(), testAgentUID)
	if err != nil {
		t.Fatal(err)
	}
	if record.LastUsed == nil {
		t.Fatal("successful credential issuance did not update last used")
	}
}

func TestCallbackCannotOverwriteReconnectTransactionAfterTokenExchange(t *testing.T) {
	store, _, closeKube := newRecordTestStore(t)
	defer closeKube()
	stateA := strings.Repeat("a", 43)
	record := &AuthorizationRecord{
		InstanceUID: testAgentUID, InstanceName: testAgentName,
		OwnerTenantID: testOwnerTenantID, OwnerObjectID: testOwnerObjectID,
		AgentIdentityClientID: testAgentClientID, Generation: 7,
		Transaction: &Transaction{
			StateHash: stateHash(stateA), CodeVerifier: "verifier-a", Nonce: "nonce-a", ExpiresAt: time.Now().Add(time.Minute),
		},
	}
	if err := store.putRecord(context.Background(), record, ""); err != nil {
		t.Fatal(err)
	}
	assertionFile := filepath.Join(t.TempDir(), "federated-token")
	if err := os.WriteFile(assertionFile, []byte("synthetic-assertion"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const keyID = "test-key"
	verifier := newJWTVerifier(testOwnerTenantID, testAuthorizationID, time.Second)
	verifier.keys[keyID] = &key.PublicKey
	verifier.fetched = time.Now()
	idToken := signedTestIDToken(t, key, keyID, "RS256", map[string]any{
		"iss": verifier.issuer, "aud": testAuthorizationID, "tid": testOwnerTenantID, "oid": testOwnerObjectID,
		"nonce": "nonce-a", "preferred_username": "user@example.invalid",
		"exp": time.Now().Add(time.Hour).Unix(), "nbf": time.Now().Add(-time.Minute).Unix(),
	})
	exchangeStarted := make(chan struct{})
	releaseExchange := make(chan struct{})
	oauth := &oauthClient{
		cfg: Config{TenantID: testOwnerTenantID, AuthorizationClientID: testAuthorizationID, FederatedTokenFile: assertionFile},
		client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			close(exchangeStarted)
			<-releaseExchange
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","id_token":"` + idToken + `","expires_in":3600}`))}, nil
		})},
		verifier: verifier,
	}
	authSDK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Validate" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tid": testOwnerTenantID, "oid": testOwnerObjectID, "aud": testBlueprintID,
			"azp": testAuthorizationID, "scp": "access_agent",
		})
	}))
	defer authSDK.Close()
	server := &Server{
		cfg: Config{
			TenantID: testOwnerTenantID, BlueprintClientID: testBlueprintID, AuthorizationClientID: testAuthorizationID,
			FederatedTokenFile: assertionFile, TransactionTTL: time.Minute, PortalOrigin: "https://portal.example.invalid",
		},
		store: store, oauth: oauth,
		authSDK: &authSDKClient{baseURL: authSDK.URL, client: authSDK.Client()}, mux: http.NewServeMux(),
	}
	server.routes()
	callbackResponse := httptest.NewRecorder()
	callbackRequest := httptest.NewRequest(http.MethodPost, "/internal/v1/authorization/callback", strings.NewReader("state="+stateA+"&code=synthetic-code"))
	callbackRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	callbackDone := make(chan struct{})
	go func() {
		defer close(callbackDone)
		server.Handler().ServeHTTP(callbackResponse, callbackRequest)
	}()
	select {
	case <-exchangeStarted:
	case <-time.After(time.Second):
		t.Fatal("callback did not reach the blocked token exchange")
	}
	startRequest := httptest.NewRequest(http.MethodPost, "/internal/v1/instances/"+testAgentUID+"/authorization/start", nil)
	startRequest.Header.Set("X-CetiRun-Instance-UID", testAgentUID)
	startRequest.Header.Set("X-CetiRun-Instance-Name", testAgentName)
	startRequest.Header.Set("X-CetiRun-Owner-Tenant-ID", testOwnerTenantID)
	startRequest.Header.Set("X-CetiRun-Owner-Object-ID", testOwnerObjectID)
	startResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(startResponse, startRequest)
	if startResponse.Code != http.StatusOK {
		t.Fatalf("Connect/Reconnect status = %d, want %d: %s", startResponse.Code, http.StatusOK, startResponse.Body.String())
	}
	newRecord, _, err := store.getRecord(context.Background(), testAgentUID)
	if err != nil {
		t.Fatal(err)
	}
	if newRecord.Transaction == nil || newRecord.Transaction.StateHash == stateHash(stateA) || newRecord.Tokens != nil {
		t.Fatalf("Reconnect did not replace callback transaction: %#v", newRecord)
	}
	stateBHash := newRecord.Transaction.StateHash
	close(releaseExchange)
	select {
	case <-callbackDone:
	case <-time.After(time.Second):
		t.Fatal("callback did not complete after token exchange was released")
	}
	if callbackResponse.Code != http.StatusOK {
		t.Fatalf("callback status = %d, want completion page", callbackResponse.Code)
	}
	current, _, err := store.getRecord(context.Background(), testAgentUID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Transaction == nil || current.Transaction.StateHash != stateBHash || current.Transaction.Consumed || current.Tokens != nil {
		t.Fatalf("stale callback overwrote reconnect transaction: %#v", current)
	}
}

func TestCredentialRequestValidation(t *testing.T) {
	server := &Server{mux: http.NewServeMux()}
	server.routes()
	cases := []struct {
		name string
		body string
		code string
	}{
		{
			name: "unsupported service",
			body: `{"service":"microsoft-graph"}`,
			code: "unsupported_service",
		},
		{
			name: "caller-selected scope",
			body: `{"service":"azure-resource-manager","scope":"https://example.invalid/.default"}`,
			code: "invalid_request",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/credentials/issue", strings.NewReader(test.body))
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusBadRequest, response.Body.String())
			}
			var payload apiError
			if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if payload.Error.Code != test.code {
				t.Fatalf("error code = %q, want %s", payload.Error.Code, test.code)
			}
		})
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/azure/subscriptions", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("removed Azure tool route returned %d, want 404", response.Code)
	}
}

func TestCredentialIssueRequiresVerifiedConnectedWorkload(t *testing.T) {
	t.Run("missing workload identity", func(t *testing.T) {
		server := &Server{}
		request := httptest.NewRequest(http.MethodPost, "/v1/credentials/issue", strings.NewReader(`{"service":"azure-resource-manager"}`))
		response := httptest.NewRecorder()
		server.issueCredential(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusForbidden, response.Body.String())
		}
	})

	t.Run("disconnected workload", func(t *testing.T) {
		store, _, closeKube := newRecordTestStore(t)
		defer closeKube()
		server := &Server{store: store}
		request := httptest.NewRequest(http.MethodPost, "/v1/credentials/issue", strings.NewReader(`{"service":"azure-resource-manager"}`))
		request.Header.Set("X-Forwarded-Client-Cert", "URI=spiffe://cluster.local/ns/cetirun-owner/sa/openclaw-agent-a-cetirun")
		response := httptest.NewRecorder()
		server.issueCredential(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusUnauthorized, response.Body.String())
		}
		var payload apiError
		if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Error.Code != "authorization_required" {
			t.Fatalf("error code = %q, want authorization_required", payload.Error.Code)
		}
	})
}

func TestCallbackLookupRateLimitStopsBeforeKubernetesLookup(t *testing.T) {
	lookups := 0
	kubeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lookups++
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/api/v1/namespaces/cetirun-credentials/secrets") {
			http.Error(w, "unexpected request", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
	}))
	defer kubeAPI.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("synthetic-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{
		store: &kubeClient{
			baseURL: kubeAPI.URL, namespace: "cetirun-credentials", tokenFile: tokenFile,
			client: kubeAPI.Client(), wrapper: memoryWrapper{key: []byte("unit-test-key")},
		},
		now: func() time.Time { return time.Unix(0, 0) },
	}
	form := "state=" + strings.Repeat("a", 43) + "&code=synthetic-code"
	for index := 0; index < maxCallbackLookupsPerSecond; index++ {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/internal/v1/authorization/callback", strings.NewReader(form))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		server.callback(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("callback %d status = %d, want %d", index, response.Code, http.StatusOK)
		}
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/authorization/callback", strings.NewReader(form))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	server.callback(response, request)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("limited callback status = %d, want %d", response.Code, http.StatusTooManyRequests)
	}
	if lookups != maxCallbackLookupsPerSecond {
		t.Fatalf("Kubernetes lookups = %d, want %d", lookups, maxCallbackLookupsPerSecond)
	}
}
