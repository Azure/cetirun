// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

type authorizationFixture struct {
	server     *Server
	agent      agentDocument
	connection connectionRecord
	grant      AgentGrant
	secrets    map[string]secretDocument
	version    uint64
}

func securityJSONResponse(t *testing.T, status int, value any) *http.Response {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}
}

func newAuthorizationFixture(t *testing.T, provider, tool string, constraints Constraints) *authorizationFixture {
	t.Helper()
	owner := Owner{TenantID: "22222222-2222-2222-2222-222222222222", ObjectID: "33333333-3333-3333-3333-333333333333"}
	now := time.Now().UTC().Truncate(time.Second)
	fixture := &authorizationFixture{secrets: map[string]secretDocument{}}
	fixture.agent.Metadata.Name = "agent"
	fixture.agent.Spec.Agent.Type = "openclaw"
	fixture.agent.Metadata.UID = "11111111-1111-1111-1111-111111111111"
	fixture.agent.Metadata.Annotations = map[string]string{
		"agentidentity.cetirun.microsoft.com/owner-tenant-id": owner.TenantID,
		"agentidentity.cetirun.microsoft.com/owner-object-id": owner.ObjectID,
	}
	fixture.agent.Status.Namespace, fixture.agent.Status.Phase = "agents", "Ready"
	cfg := Config{
		ToolServiceNamespace: "cetirun-system", ToolServiceAccount: "agent-tools-mcp", WorkloadTokenAudience: "agent-tools-mcp",
		TenantID: owner.TenantID, BlueprintClientID: "44444444-4444-4444-4444-444444444444", AuthorizationClientID: "55555555-5555-5555-5555-555555555555",
		PortalOrigin: "https://portal.example.com", RedirectURI: "https://portal.example.com/api/v1/connections/callback",
		AzureOBOPermissions: []string{"user_impersonation"}, MicrosoftOBOPermissions: []string{"Mail.Read", "Mail.Send", "Calendars.ReadWrite"},
	}
	fixture.connection = connectionRecord{Connection: Connection{
		ID: "connection", Owner: owner, Provider: provider, AccountID: "original-account", AccountDisplay: "original@example.com",
		Capabilities: providerTools(provider), Revision: 1, Status: "connected", CreatedAt: now, UpdatedAt: now,
	}, Credential: &providerCredential{AccessToken: "synthetic-provider-token", RefreshToken: "synthetic-refresh-token", ExpiresAt: now.Add(time.Hour)}}
	fixture.grant = AgentGrant{
		ID: "grant", InstanceUID: fixture.agent.Metadata.UID, ConnectionID: fixture.connection.Connection.ID, Owner: owner,
		Tools: []string{tool}, Constraints: constraints, ExpiresAt: now.Add(time.Hour), Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	tokenFile := filepath.Join(t.TempDir(), "synthetic-kubernetes-token")
	if err := os.WriteFile(tokenFile, []byte("synthetic-kubernetes-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &kubeClient{baseURL: "https://kubernetes.example.com", namespace: "credentials", tokenFile: tokenFile, wrapper: memoryWrapper{key: []byte("synthetic-encryption-key")}}
	store.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		path := request.URL.Path
		if request.Method == http.MethodPost && path == "/apis/authentication.k8s.io/v1/tokenreviews" {
			var input tokenReview
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(input.Spec.Audiences, []string{"agent-tools-mcp"}) {
				t.Fatalf("TokenReview audiences = %#v", input.Spec.Audiences)
			}
			return securityJSONResponse(t, http.StatusOK, map[string]any{"status": map[string]any{
				"authenticated": true, "audiences": []string{"agent-tools-mcp"},
				"user": map[string]any{"username": "system:serviceaccount:agents:openclaw-agent-cetirun", "uid": "service-account-uid", "extra": map[string][]string{
					"authentication.kubernetes.io/pod-name": {"agent-pod"}, "authentication.kubernetes.io/pod-uid": {"pod-uid"},
				}},
			}}), nil
		}
		switch path {
		case "/api/v1/namespaces/agents/serviceaccounts/openclaw-agent-cetirun":
			return securityJSONResponse(t, http.StatusOK, map[string]any{"metadata": map[string]string{"uid": "service-account-uid"}}), nil
		case "/api/v1/namespaces/agents/pods/agent-pod":
			return securityJSONResponse(t, http.StatusOK, map[string]any{"metadata": map[string]string{"uid": "pod-uid"}, "spec": map[string]string{"serviceAccountName": "openclaw-agent-cetirun"}}), nil
		case "/apis/cetirun.microsoft.com/v1alpha1/agents":
			return securityJSONResponse(t, http.StatusOK, agentList{Items: []agentDocument{fixture.agent}}), nil
		case "/apis/cetirun.microsoft.com/v1alpha1/agents/agent":
			return securityJSONResponse(t, http.StatusOK, fixture.agent), nil
		}
		prefix := "/api/v1/namespaces/credentials/secrets"
		if !strings.HasPrefix(path, prefix) {
			t.Fatalf("unexpected Kubernetes request: %s %s", request.Method, path)
		}
		name := strings.TrimPrefix(path, prefix+"/")
		if request.Method == http.MethodPost || request.Method == http.MethodPut {
			var secret secretDocument
			if err := json.NewDecoder(request.Body).Decode(&secret); err != nil {
				t.Fatal(err)
			}
			existing, present := fixture.secrets[secret.Metadata.Name]
			if (request.Method == http.MethodPost && present) || (request.Method == http.MethodPut && (!present || secret.Metadata.ResourceVersion != existing.Metadata.ResourceVersion)) {
				return securityJSONResponse(t, http.StatusConflict, map[string]any{}), nil
			}
			fixture.version++
			secret.Metadata.ResourceVersion = strconv.FormatUint(fixture.version, 10)
			fixture.secrets[secret.Metadata.Name] = secret
			return securityJSONResponse(t, http.StatusOK, secret), nil
		}
		if path == prefix {
			items := []secretDocument{}
			for _, secret := range fixture.secrets {
				matches := true
				for _, selector := range strings.Split(request.URL.Query().Get("labelSelector"), ",") {
					if selector == "" {
						continue
					}
					label, value, present := strings.Cut(selector, "=")
					if !present || secret.Metadata.Labels[label] != value {
						matches = false
					}
				}
				if matches {
					items = append(items, secret)
				}
			}
			return securityJSONResponse(t, http.StatusOK, secretList{Items: items}), nil
		}
		secret, present := fixture.secrets[name]
		if !present {
			return securityJSONResponse(t, http.StatusNotFound, map[string]any{}), nil
		}
		return securityJSONResponse(t, http.StatusOK, secret), nil
	})}
	fixture.server = &Server{cfg: cfg, store: store, mux: http.NewServeMux(), tlsMux: http.NewServeMux()}
	fixture.server.routes()
	if err := store.putConnection(context.Background(), &fixture.connection, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.putGrant(context.Background(), &grantRecord{Grant: fixture.grant}, ""); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture *authorizationFixture) request(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	input := toolRequest{CallerProof: "synthetic-workload-proof", Tool: fixture.grant.Tools[0], ConnectionID: fixture.connection.Connection.ID, Arguments: map[string]any{}, GrantRevision: fixture.grant.Revision, ConnectionRevision: fixture.connection.Connection.Revision, CatalogVersion: catalogVersion}
	if fixture.connection.Connection.Provider == "github" {
		input.Arguments["repository_id"] = "42"
	}
	if fixture.connection.Connection.Provider == "azure" {
		input.Arguments["subscription_id"] = "subscription"
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	identity, err := url.Parse("spiffe://cluster.local/ns/cetirun-system/sa/agent-tools-mcp")
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{URIs: []*url.URL{identity}}
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate}, VerifiedChains: [][]*x509.Certificate{{certificate}}}
	response := httptest.NewRecorder()
	fixture.server.TLSHandler().ServeHTTP(response, request)
	return response
}

func (fixture *authorizationFixture) mutateGrant(t *testing.T, change func(*AgentGrant)) {
	t.Helper()
	record, version, err := fixture.server.store.getGrant(context.Background(), fixture.grant.ID)
	if err != nil {
		t.Fatal(err)
	}
	change(&record.Grant)
	if err := fixture.server.store.putGrant(context.Background(), record, version); err != nil {
		t.Fatal(err)
	}
}

func (fixture *authorizationFixture) enableEntra(t *testing.T) {
	t.Helper()
	document, err := json.Marshal(fixture.agent)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(document, &fields); err != nil {
		t.Fatal(err)
	}
	fields["spec"].(map[string]any)["agentIdentity"] = map[string]any{"enabled": true}
	fields["status"].(map[string]any)["agentIdentity"] = map[string]any{"phase": "Ready", "clientId": "66666666-6666-6666-6666-666666666666"}
	document, err = json.Marshal(fields)
	if err != nil || json.Unmarshal(document, &fixture.agent) != nil {
		t.Fatal("cannot construct synthetic Agent Identity")
	}
}

func TestWorkloadProofRejectsTerminatingBindings(t *testing.T) {
	for _, resource := range []string{"active", "serviceaccount", "pod"} {
		t.Run(resource, func(t *testing.T) {
			fixture := newAuthorizationFixture(t, "github", "github_issues_list", Constraints{RepositoryIDs: []string{"42"}})
			transport := fixture.server.store.client.Transport
			fixture.server.store.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if resource == "serviceaccount" && strings.Contains(request.URL.Path, "/serviceaccounts/") {
					return securityJSONResponse(t, http.StatusOK, map[string]any{"metadata": map[string]any{
						"uid": "service-account-uid", "deletionTimestamp": time.Now().UTC(),
					}}), nil
				}
				if resource == "pod" && strings.Contains(request.URL.Path, "/pods/") {
					return securityJSONResponse(t, http.StatusOK, map[string]any{
						"metadata": map[string]any{"uid": "pod-uid", "deletionTimestamp": time.Now().UTC()},
						"spec":     map[string]string{"serviceAccountName": "openclaw-agent-cetirun"},
					}), nil
				}
				return transport.RoundTrip(request)
			})
			_, err := fixture.server.store.verifyWorkloadProof(context.Background(), "synthetic-workload-proof", "agent-tools-mcp")
			if resource == "active" {
				if err != nil {
					t.Fatalf("active workload rejected: %v", err)
				}
			} else if !errors.Is(err, errWorkloadProofInvalid) {
				t.Fatalf("terminating %s proof = %v, want invalid proof", resource, err)
			}
		})
	}
}

func TestEncryptedRecordsPreservePrivateAuthorizationBindings(t *testing.T) {
	fixture := newAuthorizationFixture(t, "github", "github_issues_list", Constraints{RepositoryIDs: []string{"42"}})
	connection, _, err := fixture.server.store.getConnection(context.Background(), fixture.connection.Connection.ID)
	if err != nil || !reflect.DeepEqual(connection, &fixture.connection) {
		t.Fatalf("connection storage round trip = %#v, %v", connection, err)
	}
	fixture.mutateGrant(t, func(grant *AgentGrant) { grant.Revoked = true })
	stored, _, err := fixture.server.store.getGrant(context.Background(), fixture.grant.ID)
	if err != nil || !stored.Grant.Revoked || !sameOwner(stored.Grant.Owner, fixture.grant.Owner) || !stored.Grant.CreatedAt.Equal(fixture.grant.CreatedAt) {
		t.Fatalf("grant private storage fields were lost: %#v, %v", stored, err)
	}
	for _, view := range []any{connection.Connection, stored.Grant} {
		body, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		for _, private := range []string{"ownerTenantId", "ownerObjectId", "accountId", "revoked", "updatedAt"} {
			if bytes.Contains(body, []byte(`"`+private+`"`)) {
				t.Fatalf("private field %s appeared in API view", private)
			}
		}
	}
}

func TestOwnerChangeDeniesOldGrantsOnEveryToolRoute(t *testing.T) {
	fixture := newAuthorizationFixture(t, "github", "github_issues_list", Constraints{RepositoryIDs: []string{"42"}})
	fixture.agent.Metadata.Annotations["agentidentity.cetirun.microsoft.com/owner-object-id"] = "77777777-7777-7777-7777-777777777777"
	for _, path := range []string{"/internal/v1/tool-authorizations/check", "/internal/v1/tool-credentials/issue"} {
		response := fixture.request(t, path)
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"permission_denied"`) {
			t.Fatalf("owner-changed %s = %d %s", path, response.Code, response.Body)
		}
	}
	response := fixture.request(t, "/internal/v1/tool-authorizations/list")
	var result struct {
		Connections []Connection `json:"connections"`
		Grants      []AgentGrant `json:"grants"`
		Tools       []string     `json:"tools"`
	}
	if json.Unmarshal(response.Body.Bytes(), &result) != nil || response.Code != http.StatusOK || len(result.Connections) != 0 || len(result.Grants) != 0 || !reflect.DeepEqual(result.Tools, []string{"connections_status"}) {
		t.Fatalf("owner-changed list leaked authorization: %d %s", response.Code, response.Body)
	}
}

func TestListRejectsConnectionOwnedByDifferentCurrentOwner(t *testing.T) {
	fixture := newAuthorizationFixture(t, "github", "github_issues_list", Constraints{RepositoryIDs: []string{"42"}})
	newOwnerID := "77777777-7777-7777-7777-777777777777"
	fixture.agent.Metadata.Annotations["agentidentity.cetirun.microsoft.com/owner-object-id"] = newOwnerID
	fixture.mutateGrant(t, func(grant *AgentGrant) { grant.Owner.ObjectID = newOwnerID })
	response := fixture.request(t, "/internal/v1/tool-authorizations/list")
	var result struct {
		Connections []Connection `json:"connections"`
		Grants      []AgentGrant `json:"grants"`
	}
	if json.Unmarshal(response.Body.Bytes(), &result) != nil || response.Code != http.StatusOK || len(result.Connections) != 0 || len(result.Grants) != 0 {
		t.Fatalf("list exposed another owner's connection: %d %s", response.Code, response.Body)
	}
}

func TestConfiguredOBOCapabilitiesDoNotInferDefaultOrWildcardConsent(t *testing.T) {
	for _, permissions := range [][]string{nil, {"access_agent"}, {"*"}, {"https://graph.microsoft.com/.default"}} {
		cfg := Config{AzureOBOPermissions: permissions, MicrosoftOBOPermissions: permissions}
		for _, provider := range []string{"azure", "microsoft"} {
			if capabilities := configuredOBOCapabilities(cfg, provider); len(capabilities) != 0 {
				t.Fatalf("unconfigured permission names inferred %s capabilities: %#v", provider, capabilities)
			}
		}
	}
	capabilities := configuredOBOCapabilities(Config{MicrosoftOBOPermissions: []string{"Mail.Read"}}, "microsoft")
	if !contains(capabilities, "mail_messages_get") || contains(capabilities, "mail_send") || contains(capabilities, "calendar_events_create") {
		t.Fatalf("read-only configured permissions exposed write capabilities: %#v", capabilities)
	}
}

func TestIssuanceRechecksAuthorizationAfterOBO(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testing.T, *authorizationFixture)
	}{
		{name: "unchanged"},
		{name: "revoked", change: func(t *testing.T, fixture *authorizationFixture) {
			fixture.mutateGrant(t, func(grant *AgentGrant) { grant.Revoked = true; grant.Revision++ })
		}},
		{name: "restricted", change: func(t *testing.T, fixture *authorizationFixture) {
			fixture.mutateGrant(t, func(grant *AgentGrant) {
				grant.Constraints.RecipientDomains = []string{"example.net"}
				grant.Revision++
			})
		}},
		{name: "constraints-with-same-revision", change: func(t *testing.T, fixture *authorizationFixture) {
			fixture.mutateGrant(t, func(grant *AgentGrant) { grant.Constraints.AllowInvites = true })
		}},
		{name: "expired", change: func(t *testing.T, fixture *authorizationFixture) {
			fixture.mutateGrant(t, func(grant *AgentGrant) { grant.ExpiresAt = time.Now().Add(-time.Second) })
		}},
		{name: "expiry-with-same-revision", change: func(t *testing.T, fixture *authorizationFixture) {
			fixture.mutateGrant(t, func(grant *AgentGrant) { grant.ExpiresAt = grant.ExpiresAt.Add(time.Hour) })
		}},
		{name: "owner-changed", change: func(_ *testing.T, fixture *authorizationFixture) {
			fixture.agent.Metadata.Annotations["agentidentity.cetirun.microsoft.com/owner-object-id"] = "77777777-7777-7777-7777-777777777777"
		}},
		{name: "grant-owner-changed", change: func(t *testing.T, fixture *authorizationFixture) {
			fixture.mutateGrant(t, func(grant *AgentGrant) { grant.Owner.ObjectID = "77777777-7777-7777-7777-777777777777" })
		}},
		{name: "instance-suspended", change: func(_ *testing.T, fixture *authorizationFixture) { fixture.agent.Spec.Suspended = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAuthorizationFixture(t, "microsoft", "mail_messages_get", Constraints{})
			fixture.enableEntra(t)
			called := false
			token := "header." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"scp":"Mail.Read","exp":%d}`, time.Now().Add(time.Hour).Unix()))) + ".signature"
			fixture.server.authSDK = &authSDKClient{baseURL: "http://127.0.0.1:5000", client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path != "/AuthorizationHeader/MicrosoftGraph" {
					t.Fatalf("unexpected OBO service: %s", request.URL.Path)
				}
				called = true
				if test.change != nil {
					test.change(t, fixture)
				}
				return securityJSONResponse(t, http.StatusOK, map[string]string{"authorizationHeader": "Bearer " + token}), nil
			})}}
			response := fixture.request(t, "/internal/v1/tool-credentials/issue")
			if !called {
				t.Fatalf("request never reached OBO: %d %s", response.Code, response.Body)
			}
			if test.change == nil {
				if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"accessToken"`) {
					t.Fatalf("unchanged authorization issuance = %d %s", response.Code, response.Body)
				}
			} else if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"authorization_changed"`) || strings.Contains(response.Body.String(), "accessToken") {
				t.Fatalf("changed authorization returned a credential: %d %s", response.Code, response.Body)
			}
		})
	}
}

func TestIssuanceRechecksGrantAfterProviderRefresh(t *testing.T) {
	fixture := newAuthorizationFixture(t, "github", "github_issues_list", Constraints{RepositoryIDs: []string{"42"}})
	record, version, err := fixture.server.store.getConnection(context.Background(), fixture.connection.Connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	record.Credential.ExpiresAt = time.Now().Add(time.Second)
	if err := fixture.server.store.putConnection(context.Background(), record, version); err != nil {
		t.Fatal(err)
	}
	fixture.server.providers = &providerClient{cfg: fixture.server.cfg, client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://github.com/login/oauth/access_token" {
			t.Fatalf("unexpected refresh endpoint: %s", request.URL)
		}
		fixture.mutateGrant(t, func(grant *AgentGrant) { grant.Revoked = true; grant.Revision++ })
		return securityJSONResponse(t, http.StatusOK, map[string]any{"access_token": "synthetic-refreshed-token", "expires_in": 3600}), nil
	})}}
	response := fixture.request(t, "/internal/v1/tool-credentials/issue")
	if response.Code != http.StatusConflict || strings.Contains(response.Body.String(), "accessToken") {
		t.Fatalf("revocation during refresh returned a credential: %d %s", response.Code, response.Body)
	}
}

func TestReconnectNeverReplacesAccountEvenWithoutActiveGrants(t *testing.T) {
	for _, grantState := range []string{"active", "expired", "absent"} {
		t.Run(grantState, func(t *testing.T) {
			fixture := newAuthorizationFixture(t, "github", "github_issues_list", Constraints{RepositoryIDs: []string{"42"}})
			fixture.server.cfg.PortalNamespace, fixture.server.cfg.PortalServiceAccount = "portal", "portal"
			if grantState == "expired" {
				fixture.mutateGrant(t, func(grant *AgentGrant) { grant.ExpiresAt = time.Now().Add(-time.Hour) })
			} else if grantState == "absent" {
				delete(fixture.secrets, toolRecordName("grant", fixture.grant.ID))
			}
			state := strings.Repeat("a", 43)
			record, version, err := fixture.server.store.getConnection(context.Background(), fixture.connection.Connection.ID)
			if err != nil {
				t.Fatal(err)
			}
			browserBinding := strings.Repeat("b", 43)
			record.Transaction = &connectionTransaction{Provider: "github", StateHash: stateHash(state), BrowserBindingHash: stateHash(browserBinding), ExpectedAccountID: record.Connection.AccountID, ExpiresAt: time.Now().Add(time.Hour)}
			record.Connection.Status, record.Credential = "connecting", nil
			if err := fixture.server.store.putConnection(context.Background(), record, version); err != nil {
				t.Fatal(err)
			}
			fixture.server.providers = &providerClient{cfg: fixture.server.cfg, client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path == "/login/oauth/access_token" {
					return securityJSONResponse(t, http.StatusOK, map[string]any{"access_token": "synthetic-swapped-token", "expires_in": 3600}), nil
				}
				return securityJSONResponse(t, http.StatusOK, map[string]any{"id": 99, "login": "different-account"}), nil
			})}}
			response := httptest.NewRecorder()
			callback := httptest.NewRequest(http.MethodGet, "/api/v1/connections/callback?state="+state+"&code=synthetic-code", nil)
			callback.Header.Set(connectionBindingHeader, browserBinding)
			callback.Header.Set("X-Forwarded-Client-Cert", "URI=spiffe://cluster.local/ns/portal/sa/portal")
			fixture.server.Handler().ServeHTTP(response, callback)
			current, _, err := fixture.server.store.getConnection(context.Background(), record.Connection.ID)
			if err != nil || current.Connection.AccountID != "original-account" || current.Credential != nil || current.Connection.Status != "interaction_required" || current.Transaction != nil || !strings.Contains(response.Body.String(), "could not be completed") {
				t.Fatalf("reconnect changed account (%s): %#v, %v, %s", grantState, current, err, response.Body)
			}
		})
	}
}

func TestMicrosoftIntermediateAccessAgentTokenSupportsConfiguredGrants(t *testing.T) {
	for _, provider := range []string{"azure", "microsoft"} {
		t.Run(provider, func(t *testing.T) {
			tool, constraints := "mail_messages_get", Constraints{}
			if provider == "azure" {
				tool, constraints = "azure_resources_search", Constraints{SubscriptionIDs: []string{"subscription"}}
			}
			fixture := newAuthorizationFixture(t, provider, tool, constraints)
			fixture.enableEntra(t)
			cfg := fixture.server.cfg
			key, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Fatal(err)
			}
			state, nonce := strings.Repeat("a", 43), "synthetic-nonce"
			oauth := newOAuthClient(cfg)
			oauth.verifier.keys["synthetic-key"], oauth.verifier.fetched = &key.PublicKey, time.Now()
			idToken := signedTestIDToken(t, key, "synthetic-key", "RS256", map[string]any{
				"iss": oauth.verifier.issuer, "aud": cfg.AuthorizationClientID, "tid": cfg.TenantID, "oid": fixture.grant.Owner.ObjectID,
				"nonce": nonce, "preferred_username": "owner@example.com", "exp": time.Now().Add(time.Hour).Unix(),
			})
			claims := map[string]any{"aud": cfg.BlueprintClientID, "scp": "access_agent", "tid": cfg.TenantID, "oid": fixture.grant.Owner.ObjectID, "azp": cfg.AuthorizationClientID, "exp": time.Now().Add(time.Hour).Unix()}
			payload, err := json.Marshal(claims)
			if err != nil {
				t.Fatal(err)
			}
			intermediate := "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
			if len(microsoftCapabilities(intermediate, provider)) != 0 || microsoftTokenAllowsTool(intermediate, tool) {
				t.Fatal("intermediate access_agent token was treated as a downstream token")
			}
			oauth.cfg.FederatedTokenFile = filepath.Join(t.TempDir(), "synthetic-assertion")
			if err := os.WriteFile(oauth.cfg.FederatedTokenFile, []byte("synthetic-assertion"), 0o600); err != nil {
				t.Fatal(err)
			}
			oauth.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.Method != http.MethodPost || !strings.HasSuffix(request.URL.Path, "/oauth2/v2.0/token") {
					t.Fatalf("unexpected Microsoft OAuth endpoint: %s", request.URL)
				}
				return securityJSONResponse(t, http.StatusOK, oauthTokens{AccessToken: intermediate, RefreshToken: "synthetic-refresh", IDToken: idToken, ExpiresIn: 3600}), nil
			})}
			fixture.server.oauth = oauth
			fixture.server.authSDK = &authSDKClient{baseURL: "http://127.0.0.1:5000", client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path != "/Validate" || request.Header.Get("Authorization") != "Bearer "+intermediate {
					t.Fatalf("unexpected intermediate validation: %s", request.URL.Path)
				}
				return securityJSONResponse(t, http.StatusOK, claims), nil
			})}}
			record, version, err := fixture.server.store.getConnection(context.Background(), fixture.connection.Connection.ID)
			if err != nil {
				t.Fatal(err)
			}
			record.Connection.AccountID, record.Connection.Status, record.Connection.Capabilities = "", "connecting", []string{}
			record.Transaction = &connectionTransaction{Provider: provider, StateHash: stateHash(state), Nonce: nonce, ExpiresAt: time.Now().Add(time.Hour)}
			if err := fixture.server.store.putConnection(context.Background(), record, version); err != nil {
				t.Fatal(err)
			}
			callback := httptest.NewRecorder()
			form := url.Values{"state": {state}, "code": {"synthetic-code"}}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/connections/callback", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			fixture.server.Handler().ServeHTTP(callback, request)
			current, _, err := fixture.server.store.getConnection(context.Background(), record.Connection.ID)
			if err != nil || current.Connection.Status != "connected" || !contains(current.Connection.Capabilities, tool) {
				t.Fatalf("access_agent callback did not expose configured grant capability: %#v %v %s", current, err, callback.Body)
			}
			grant, err := buildGrant(grantRequest{ConnectionID: current.Connection.ID, Tools: []string{tool}, Constraints: constraints}, fixture.grant.Owner, fixture.grant.InstanceUID, current.Connection, time.Now())
			if err != nil || !contains(grant.Tools, tool) {
				t.Fatalf("configured OBO tool cannot be granted: %#v, %v", grant, err)
			}
			fixture.connection = *current
			for _, scopes := range []string{"access_agent", "Mail.Read", "user_impersonation"} {
				token := "header." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"scp":%q,"exp":%d}`, scopes, time.Now().Add(time.Hour).Unix()))) + ".signature"
				fixture.server.authSDK.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
					return securityJSONResponse(t, http.StatusOK, map[string]string{"authorizationHeader": "Bearer " + token}), nil
				})
				response := fixture.request(t, "/internal/v1/tool-credentials/issue")
				allowed := (provider == "azure" && scopes == "user_impersonation") || (provider == "microsoft" && scopes == "Mail.Read")
				if (response.Code == http.StatusOK) != allowed || (!allowed && strings.Contains(response.Body.String(), "accessToken")) {
					t.Fatalf("actual OBO scope %s authorization = %d %s", scopes, response.Code, response.Body)
				}
			}
		})
	}
}
