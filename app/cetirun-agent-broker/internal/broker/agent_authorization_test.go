// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	testAgentPrincipalID = "55555555-5555-5555-5555-555555555555"
	testSubscriptionID   = "66666666-6666-6666-6666-666666666666"
	testRoleDefinitionID = "/subscriptions/" + testSubscriptionID + "/providers/Microsoft.Authorization/roleDefinitions/acdd72a7-3385-48ef-bd42-f606fba81ae7"
)

// prepareAgentAuthorizationFixture converts the established encrypted-store
// fixture to the independent Azure identity shape without bypassing the Broker
// storage layer. The returned grant deliberately starts pending.
func prepareAgentAuthorizationFixture(t *testing.T) *authorizationFixture {
	t.Helper()
	fixture := newAuthorizationFixture(t, "azure", "azure_resource_groups_list", Constraints{SubscriptionIDs: []string{testSubscriptionID}})
	fixture.agent.Spec.AgentIdentity = &struct {
		Enabled bool `json:"enabled"`
	}{Enabled: true}
	fixture.agent.Status.AgentIdentity = &struct {
		Phase    string `json:"phase"`
		ClientID string `json:"clientId"`
		ObjectID string `json:"objectId"`
	}{Phase: "Ready", ClientID: testFirstAgentID, ObjectID: testAgentPrincipalID}

	connection, connectionVersion, err := fixture.server.store.getConnection(context.Background(), fixture.connection.Connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	connection.Credential = nil
	connection.Connection.Mode = modeAgent
	connection.Connection.Identity = &IdentityBinding{
		InstanceUID: fixture.agent.Metadata.UID,
		TenantID:    fixture.server.cfg.TenantID,
		ClientID:    testFirstAgentID,
		PrincipalID: testAgentPrincipalID,
	}
	connection.Connection.AccountID = testAgentPrincipalID
	connection.Connection.AccountDisplay = "Agent " + fixture.agent.Metadata.Name
	connection.Connection.Capabilities = providerTools("azure")
	connection.Connection.Revision = 7
	if err := fixture.server.store.putConnection(context.Background(), connection, connectionVersion); err != nil {
		t.Fatal(err)
	}
	fixture.connection = *connection

	grant, err := buildGrant(grantRequest{
		ConnectionID: fixture.connection.Connection.ID,
		Tools:        []string{"azure_resource_groups_list"},
		Constraints:  Constraints{SubscriptionIDs: []string{testSubscriptionID}},
		ExpiresAt:    time.Now().UTC().Add(time.Hour),
	}, fixture.connection.Connection.Owner, fixture.agent.Metadata.UID, fixture.connection.Connection, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	grant.ID = fixture.grant.ID
	stored, grantVersion, err := fixture.server.store.getGrant(context.Background(), grant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Grant.ID != grant.ID {
		t.Fatalf("stored grant ID = %q", stored.Grant.ID)
	}
	if err := fixture.server.store.putGrant(context.Background(), &grantRecord{Grant: grant}, grantVersion); err != nil {
		t.Fatal(err)
	}
	fixture.grant = grant
	return fixture
}

func (fixture *authorizationFixture) agentToolRequest(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	input := toolRequest{
		CallerProof:        "synthetic-workload-proof",
		Tool:               fixture.grant.Tools[0],
		ConnectionID:       fixture.connection.Connection.ID,
		Arguments:          map[string]any{"subscription_id": testSubscriptionID},
		GrantRevision:      fixture.grant.Revision,
		ConnectionRevision: fixture.connection.Connection.Revision,
		CatalogVersion:     catalogVersion,
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/tool-authorizations/check", bytes.NewReader(body))
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

func (fixture *authorizationFixture) setVerifiedAgentGrant(t *testing.T) {
	t.Helper()
	record, version, err := fixture.server.store.getGrant(context.Background(), fixture.grant.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	record.Grant.Authorization = &ResourceAuthorization{
		State: "verified", Source: "direct_rbac", BindingRevision: fixture.connection.Connection.Revision,
		VerifiedAt: &now, ValidUntil: &record.Grant.ExpiresAt,
		Scopes: []string{"/subscriptions/" + testSubscriptionID}, Actions: []string{"*/read"}, RequestIDs: []string{"request-id"},
	}
	if err := fixture.server.store.putGrant(context.Background(), record, version); err != nil {
		t.Fatal(err)
	}
	fixture.grant = record.Grant
}

func TestAgentGrantRequiresVerifiedEvidenceBoundToIdentityRevision(t *testing.T) {
	fixture := prepareAgentAuthorizationFixture(t)
	if got := fixture.agentToolRequest(t).Code; got != http.StatusForbidden {
		t.Fatalf("pending Agent grant status = %d, want %d", got, http.StatusForbidden)
	}

	fixture.setVerifiedAgentGrant(t)
	if got := fixture.agentToolRequest(t).Code; got != http.StatusOK {
		t.Fatalf("verified Agent grant status = %d, want %d", got, http.StatusOK)
	}

	fixture.mutateGrant(t, func(grant *AgentGrant) { grant.Authorization.BindingRevision++ })
	if got := fixture.agentToolRequest(t).Code; got != http.StatusForbidden {
		t.Fatalf("stale binding evidence status = %d, want %d", got, http.StatusForbidden)
	}
}

func TestAgentGrantRejectsMismatchedIdentityBinding(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Connection)
	}{
		{name: "instance UID", mutate: func(connection *Connection) { connection.Identity.InstanceUID = "77777777-7777-7777-7777-777777777777" }},
		{name: "principal", mutate: func(connection *Connection) { connection.Identity.PrincipalID = "88888888-8888-8888-8888-888888888888" }},
		{name: "tenant", mutate: func(connection *Connection) { connection.Identity.TenantID = "99999999-9999-9999-9999-999999999999" }},
		{name: "mode", mutate: func(connection *Connection) { connection.Mode, connection.Identity = modeDelegated, nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := prepareAgentAuthorizationFixture(t)
			fixture.setVerifiedAgentGrant(t)
			record, version, err := fixture.server.store.getConnection(context.Background(), fixture.connection.Connection.ID)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&record.Connection)
			if err := fixture.server.store.putConnection(context.Background(), record, version); err != nil {
				t.Fatal(err)
			}
			fixture.connection = *record
			if got := fixture.agentToolRequest(t).Code; got != http.StatusForbidden {
				t.Fatalf("mismatched %s status = %d, want %d", test.name, got, http.StatusForbidden)
			}
		})
	}
}

func TestAgentIdentityBindingRequiresPrincipalObjectID(t *testing.T) {
	fixture := prepareAgentAuthorizationFixture(t)
	fixture.agent.Status.AgentIdentity.ObjectID = ""
	if agent := agentFromDocument(fixture.agent); agent.AgentIdentityObjectID != "" || connectionMatchesAgent(fixture.connection.Connection, agent, fixture.server.cfg.TenantID) {
		t.Fatalf("Agent without object ID was accepted: %#v", agent)
	}
}

func TestAgentGrantCreateAndUpdateResetVerification(t *testing.T) {
	fixture := prepareAgentAuthorizationFixture(t)
	fixture.setVerifiedAgentGrant(t)

	connection := fixture.connection.Connection
	owner := fixture.grant.Owner
	created, err := buildGrant(grantRequest{ConnectionID: connection.ID, Tools: fixture.grant.Tools, Constraints: fixture.grant.Constraints, ExpiresAt: fixture.grant.ExpiresAt}, owner, fixture.grant.InstanceUID, connection, time.Now().UTC())
	if err != nil || created.Authorization == nil || created.Authorization.State != "pending" || created.Authorization.BindingRevision != connection.Revision {
		t.Fatalf("created Agent grant = %#v, %v", created, err)
	}

	record, version, err := fixture.server.store.getGrant(context.Background(), fixture.grant.ID)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := buildGrant(grantRequest{ConnectionID: connection.ID, Tools: fixture.grant.Tools, Constraints: fixture.grant.Constraints, ExpiresAt: fixture.grant.ExpiresAt}, owner, fixture.grant.InstanceUID, connection, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	updated.ID, updated.CreatedAt, updated.Revision = record.Grant.ID, record.Grant.CreatedAt, record.Grant.Revision+1
	if err := fixture.server.store.putGrant(context.Background(), &grantRecord{Grant: updated}, version); err != nil {
		t.Fatal(err)
	}
	if updated.Authorization == nil || updated.Authorization.State != "pending" || updated.Authorization.VerifiedAt != nil || updated.Authorization.ValidUntil != nil {
		t.Fatalf("updated Agent grant preserved verification: %#v", updated.Authorization)
	}
}

func TestGrantVerificationDoesNotOverwriteConcurrentChanges(t *testing.T) {
	tests := []struct {
		name   string
		change func(*authorizationFixture)
	}{
		{
			name: "revocation",
			change: func(fixture *authorizationFixture) {
				fixture.mutateGrant(t, func(grant *AgentGrant) { grant.Revoked = true; grant.Revision++ })
			},
		},
		{
			name: "identity replacement",
			change: func(fixture *authorizationFixture) {
				record, version, err := fixture.server.store.getConnection(context.Background(), fixture.connection.Connection.ID)
				if err != nil {
					t.Fatal(err)
				}
				record.Connection.Identity.PrincipalID = "88888888-8888-8888-8888-888888888888"
				record.Connection.Revision++
				if err := fixture.server.store.putConnection(context.Background(), record, version); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := prepareAgentAuthorizationFixture(t)
			configureAgentVerificationTransport(t, fixture, test.change)
			request := httptest.NewRequest(http.MethodPost, "/v1/instances/agent/tool-grants/grant/verify", nil)
			request.SetPathValue("name", "agent")
			request.SetPathValue("id", fixture.grant.ID)
			request.Header.Set("X-CetiRun-Owner-Tenant-ID", fixture.grant.Owner.TenantID)
			request.Header.Set("X-CetiRun-Owner-Object-ID", fixture.grant.Owner.ObjectID)
			request.Header.Set("X-CetiRun-Instance-UID", fixture.grant.InstanceUID)
			request.Header.Set("If-Match", "1")
			response := httptest.NewRecorder()
			fixture.server.grantVerify(response, request)
			if response.Code != http.StatusConflict {
				t.Fatalf("verify response = %d, want %d: %s", response.Code, http.StatusConflict, response.Body.String())
			}
			after, _, err := fixture.server.store.getGrant(context.Background(), fixture.grant.ID)
			if err != nil {
				t.Fatal(err)
			}
			if test.name == "revocation" && !after.Grant.Revoked {
				t.Fatal("verification overwrote concurrent revocation")
			}
			if after.Grant.Authorization == nil || after.Grant.Authorization.State != "pending" {
				t.Fatalf("verification overwrote concurrent state: %#v", after.Grant.Authorization)
			}
		})
	}
}

func configureAgentVerificationTransport(t *testing.T, fixture *authorizationFixture, concurrentChange func(*authorizationFixture)) {
	t.Helper()
	federated := filepath.Join(t.TempDir(), "federated-token")
	if err := os.WriteFile(federated, []byte("federated-workload-assertion"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.server.cfg.FederatedTokenFile = federated
	oauthCalls := 0
	fixture.server.oauth = &oauthClient{cfg: fixture.server.cfg, client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		oauthCalls++
		if request.Method != http.MethodPost || request.URL.Host != "login.microsoftonline.com" {
			t.Fatalf("unexpected Agent OAuth request %s %s", request.Method, request.URL)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatal(err)
		}
		if oauthCalls == 1 && form.Get("client_id") == fixture.server.cfg.BlueprintClientID {
			return securityJSONResponse(t, http.StatusOK, map[string]any{"access_token": "opaque-exchange", "token_type": "Bearer", "expires_in": 3600}), nil
		}
		if oauthCalls == 2 && form.Get("client_id") == testFirstAgentID && form.Get("client_assertion") == "opaque-exchange" {
			return securityJSONResponse(t, http.StatusOK, map[string]any{"access_token": "opaque-agent-token", "token_type": "Bearer", "expires_in": 3600}), nil
		}
		t.Fatalf("unexpected OAuth form %#v", form)
		return nil, nil
	})}}
	armCalls := 0
	fixture.server.arm = &armAuthorizationClient{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		armCalls++
		if got := request.Header.Get("Authorization"); got != "Bearer opaque-agent-token" {
			t.Fatalf("ARM authorization = %q", got)
		}
		switch armCalls {
		case 1:
			concurrentChange(fixture)
			properties := map[string]any{
				"principalId":      testAgentPrincipalID,
				"principalType":    "ServicePrincipal",
				"scope":            "/subscriptions/" + testSubscriptionID,
				"roleDefinitionId": testRoleDefinitionID,
				"condition":        "",
			}
			return securityJSONResponse(t, http.StatusOK, map[string]any{"value": []any{map[string]any{"properties": properties}}}), nil
		case 2:
			return securityJSONResponse(t, http.StatusOK, map[string]any{"id": testRoleDefinitionID, "properties": map[string]any{"permissions": []any{map[string]any{"actions": []string{"*/read"}}}}}), nil
		case 3:
			return securityJSONResponse(t, http.StatusOK, map[string]any{"value": []any{map[string]any{"actions": []string{"*/read"}}}}), nil
		case 4:
			return securityJSONResponse(t, http.StatusOK, map[string]any{"value": []any{}}), nil
		default:
			t.Fatalf("unexpected ARM request %s", request.URL)
			return nil, nil
		}
	})}}
}

func TestARMAuthorizationEvidenceRequiresDirectUnconditionalAgentAssignment(t *testing.T) {
	principal := testAgentPrincipalID
	tests := []struct {
		name        string
		assignment  map[string]any
		wantOK      bool
		partialRole bool
	}{
		{name: "direct service principal", assignment: map[string]any{"principalId": principal, "principalType": "ServicePrincipal", "scope": "/subscriptions/" + testSubscriptionID, "roleDefinitionId": testRoleDefinitionID, "condition": ""}, wantOK: true},
		{name: "partial direct role plus inherited reader", assignment: map[string]any{"principalId": principal, "principalType": "ServicePrincipal", "scope": "/subscriptions/" + testSubscriptionID, "roleDefinitionId": testRoleDefinitionID}, partialRole: true},
		{name: "conditional", assignment: map[string]any{"principalId": principal, "principalType": "ServicePrincipal", "scope": "/subscriptions/" + testSubscriptionID, "roleDefinitionId": testRoleDefinitionID, "condition": "x"}},
		{name: "inherited scope", assignment: map[string]any{"principalId": principal, "principalType": "ServicePrincipal", "scope": "/providers/Microsoft.Management/managementGroups/root", "roleDefinitionId": testRoleDefinitionID, "condition": ""}},
		{name: "different principal", assignment: map[string]any{"principalId": "88888888-8888-8888-8888-888888888888", "principalType": "ServicePrincipal", "scope": "/subscriptions/" + testSubscriptionID, "roleDefinitionId": testRoleDefinitionID, "condition": ""}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := &armAuthorizationClient{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				if request.Header.Get("Authorization") != "Bearer opaque-agent-token" {
					t.Fatalf("unexpected ARM token")
				}
				switch calls {
				case 1:
					return securityJSONResponse(t, http.StatusOK, map[string]any{"value": []any{map[string]any{"properties": test.assignment}}}), nil
				case 2:
					if request.URL.Path != testRoleDefinitionID {
						t.Fatalf("unexpected role definition path %s", request.URL.Path)
					}
					actions := []string{"*/read"}
					if test.partialRole {
						actions = []string{"Microsoft.Authorization/roleAssignments/read"}
					}
					return securityJSONResponse(t, http.StatusOK, map[string]any{"id": testRoleDefinitionID, "properties": map[string]any{"permissions": []any{map[string]any{"actions": actions}}}}), nil
				case 3:
					return securityJSONResponse(t, http.StatusOK, map[string]any{"value": []any{map[string]any{"actions": []string{"*/read"}}}}), nil
				default:
					return securityJSONResponse(t, http.StatusOK, map[string]any{"value": []any{}}), nil
				}
			})}}
			scopes, _, err := client.verify(context.Background(), "opaque-agent-token", principal, []string{testSubscriptionID}, []string{"*/read"})
			if test.wantOK && (err != nil || !reflect.DeepEqual(scopes, []string{"/subscriptions/" + testSubscriptionID})) {
				t.Fatalf("verify = %#v, %v", scopes, err)
			}
			if !test.wantOK && err == nil {
				t.Fatal("invalid direct role assignment evidence was accepted")
			}
		})
	}
}

func TestSubscriptionListUsesGrantScopeWithoutSubscriptionArgument(t *testing.T) {
	fixture := newAuthorizationFixture(t, "azure", "azure_subscriptions_list", Constraints{SubscriptionIDs: []string{testSubscriptionID}})
	input := toolRequest{CallerProof: "synthetic-workload-proof", Tool: "azure_subscriptions_list", ConnectionID: fixture.connection.Connection.ID, Arguments: map[string]any{}, GrantRevision: fixture.grant.Revision, ConnectionRevision: fixture.connection.Connection.Revision, CatalogVersion: catalogVersion}
	if _, _, _, _, err := fixture.server.checkToolAuthorization(context.Background(), input, true); err != nil {
		t.Fatalf("subscription list: %v", err)
	}
	if validateToolArguments("azure_subscriptions_list", map[string]any{}, Constraints{}) == nil {
		t.Fatal("unbounded subscription list accepted")
	}
}

func TestDelegatedGrantStopsAfterUnsupportedRuntimeTransition(t *testing.T) {
	for _, kind := range []string{"sandbox", "custom", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newAuthorizationFixture(t, "github", "github_repositories_get", Constraints{RepositoryIDs: []string{"42"}})
			input := toolRequest{CallerProof: "synthetic-workload-proof", Tool: "github_repositories_get", ConnectionID: fixture.connection.Connection.ID, Arguments: map[string]any{"repository_id": "42"}, GrantRevision: fixture.grant.Revision, ConnectionRevision: fixture.connection.Connection.Revision, CatalogVersion: catalogVersion}
			if _, _, _, _, err := fixture.server.checkToolAuthorization(context.Background(), input, true); err != nil {
				t.Fatal(err)
			}
			if kind == "sandbox" {
				fixture.agent.Spec.SandboxRef = "sandbox"
			} else {
				fixture.agent.Spec.Agent.Type = kind
			}
			for _, requireRevision := range []bool{false, true} {
				if _, _, _, _, err := fixture.server.checkToolAuthorization(context.Background(), input, requireRevision); err == nil {
					t.Fatal("unsupported runtime retained delegated access")
				}
			}
		})
	}
}

func TestDeploymentRuntimeQualificationAppliesToBothIdentityModes(t *testing.T) {
	fixture := prepareAgentAuthorizationFixture(t)
	fixture.server.cfg.ToolRuntimeTypes = []string{"hermes"}
	agent := agentFromDocument(fixture.agent)
	if fixture.server.connectionMatchesAgent(fixture.connection.Connection, agent) {
		t.Fatal("unqualified OpenClaw binding accepted")
	}
	delegated := Connection{Provider: "github", Mode: modeDelegated}
	if fixture.server.connectionMatchesAgent(delegated, agent) {
		t.Fatal("unqualified delegated runtime accepted")
	}
	fixture.agent.Spec.Agent.Type = "hermes"
	agent = agentFromDocument(fixture.agent)
	if !fixture.server.connectionMatchesAgent(fixture.connection.Connection, agent) || !fixture.server.connectionMatchesAgent(delegated, agent) {
		t.Fatal("qualified Hermes rejected")
	}
}

func TestAzureWriteGrantRequiresExplicitSafeResourceGroups(t *testing.T) {
	fixture := prepareAgentAuthorizationFixture(t)
	connection := fixture.connection.Connection
	owner := fixture.grant.Owner
	now := time.Now().UTC()
	for _, test := range []struct {
		name        string
		connection  Connection
		constraints Constraints
		wantOK      bool
	}{
		{name: "missing", connection: connection, constraints: Constraints{SubscriptionIDs: []string{testSubscriptionID}}},
		{name: "unsafe", connection: connection, constraints: Constraints{SubscriptionIDs: []string{testSubscriptionID}, ResourceGroupNames: []string{"demo*"}}},
		{name: "trailing dot", connection: connection, constraints: Constraints{SubscriptionIDs: []string{testSubscriptionID}, ResourceGroupNames: []string{"demo."}}},
		{name: "delegated", connection: Connection{Provider: "azure", Mode: modeDelegated, Status: "connected", Capabilities: providerTools("azure")}, constraints: Constraints{SubscriptionIDs: []string{testSubscriptionID}, ResourceGroupNames: []string{"demo"}}},
		{name: "independent", connection: connection, constraints: Constraints{SubscriptionIDs: []string{testSubscriptionID}, ResourceGroupNames: []string{"Demo-Group"}}, wantOK: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := buildGrant(grantRequest{ConnectionID: "connection", Tools: []string{"azure_resource_groups_create"}, Constraints: test.constraints, ExpiresAt: now.Add(time.Hour)}, owner, fixture.grant.InstanceUID, test.connection, now)
			if (err == nil) != test.wantOK {
				t.Fatalf("buildGrant error = %v, want success %t", err, test.wantOK)
			}
		})
	}
}

func TestAzureWriteAuthorizationBindsResourceGroupCaseInsensitively(t *testing.T) {
	constraints := Constraints{SubscriptionIDs: []string{testSubscriptionID}, ResourceGroupNames: []string{"Demo-Group"}}
	if err := validateToolArguments("azure_resource_group_tags_update", map[string]any{"subscription_id": testSubscriptionID, "resource_group_name": "demo-group"}, constraints); err != nil {
		t.Fatalf("case-insensitive resource group was rejected: %v", err)
	}
	if err := validateToolArguments("azure_resource_group_tags_update", map[string]any{"subscription_id": testSubscriptionID, "resource_group_name": "other"}, constraints); err == nil {
		t.Fatal("ungranted resource group was accepted")
	}
}

func TestPersistedAgentConnectionProjectsWriteCatalogWithoutWideningGrant(t *testing.T) {
	fixture := prepareAgentAuthorizationFixture(t)
	fixture.connection.Connection.Capabilities = providerTools("azure") // persisted pre-write catalog
	if got := connectionCapabilities(fixture.connection.Connection); !contains(got, "azure_resource_groups_create") || !contains(got, "azure_resource_group_tags_update") {
		t.Fatalf("projected Agent capabilities = %#v", got)
	}
	if contains(fixture.grant.Tools, "azure_resource_groups_create") || contains(fixture.grant.Tools, "azure_resource_group_tags_update") {
		t.Fatalf("existing read grant was widened: %#v", fixture.grant.Tools)
	}
	input := toolRequest{CallerProof: "synthetic-workload-proof", Tool: "azure_resource_groups_create", ConnectionID: fixture.connection.Connection.ID, Arguments: map[string]any{"subscription_id": testSubscriptionID, "resource_group_name": "demo", "location": "eastus", "operation_id": "operation-1"}, GrantRevision: fixture.grant.Revision, ConnectionRevision: fixture.connection.Connection.Revision, CatalogVersion: catalogVersion}
	if _, _, _, _, err := fixture.server.checkToolAuthorization(context.Background(), input, true); err == nil {
		t.Fatal("existing verified read grant authorized a newly cataloged write")
	}
}

func TestRequiredARMActionsAndPermissionUnion(t *testing.T) {
	actions, err := requiredARMActions([]string{"azure_resource_groups_create", "azure_resource_group_tags_update"})
	if err != nil || !reflect.DeepEqual(actions, []string{"*/read", "Microsoft.Resources/subscriptions/resourceGroups/write", "Microsoft.Resources/tags/write"}) {
		t.Fatalf("required actions = %#v, %v", actions, err)
	}
	permissions := []armPermission{
		{Actions: []string{"*/read"}},
		{Actions: []string{"Microsoft.Resources/subscriptions/resourceGroups/write"}},
		{Actions: []string{"Microsoft.Resources/tags/write"}},
	}
	if !permissionsAllowActions(permissions, actions) {
		t.Fatal("permission union did not authorize required actions")
	}
	if permissionsAllowActions([]armPermission{{Actions: []string{"*/read"}}}, actions) {
		t.Fatal("Reader permissions authorized writes")
	}
	readAndTagWrite := []string{"*/read", "Microsoft.Resources/tags/write"}
	readerAndExcludedTagWriter := []armPermission{
		{Actions: []string{"*/read"}},
		{Actions: []string{"*"}, NotActions: []string{"Microsoft.Resources/tags/write"}},
	}
	if permissionsAllowActions(readerAndExcludedTagWriter, readAndTagWrite) {
		t.Fatal("NotActions exclusion was ignored after a separate Reader permission satisfied reads")
	}
	if !permissionsAllowActions(append(readerAndExcludedTagWriter, armPermission{Actions: []string{"Microsoft.Resources/tags/write"}}), readAndTagWrite) {
		t.Fatal("a separate role grant did not restore the excluded write action")
	}
	if permissionsAllowActions([]armPermission{{Actions: []string{"*"}, NotActions: []string{"Microsoft.Compute/virtualMachines/read"}}}, []string{"*/read"}) {
		t.Fatal("a role excluding one read action proved unrestricted reads")
	}
	if !permissionsAllowActions([]armPermission{
		{Actions: []string{"*/read"}},
		{Actions: []string{"*"}, NotActions: []string{"Microsoft.Authorization/roleAssignments/write"}},
	}, []string{"*/read", "Microsoft.Resources/subscriptions/resourceGroups/write"}) {
		t.Fatal("Reader plus Contributor did not prove the required read and write actions")
	}
	if !armActionMatches("*read", "read/Microsoft.Resources/read") {
		t.Fatal("glob matching rejected a repeated suffix")
	}
	if _, err := requiredARMActions([]string{"azure_unknown"}); err == nil {
		t.Fatal("unknown Azure tool was accepted")
	}
}

func TestARMVerificationAggregatesDirectRolesAndRejectsReaderForWrite(t *testing.T) {
	tagRole := "/subscriptions/" + testSubscriptionID + "/providers/Microsoft.Authorization/roleDefinitions/77777777-7777-7777-7777-777777777777"
	assignment := func(role string) map[string]any {
		return map[string]any{"properties": map[string]any{"principalId": testAgentPrincipalID, "principalType": "ServicePrincipal", "scope": "/subscriptions/" + testSubscriptionID, "roleDefinitionId": role, "condition": ""}}
	}
	newClient := func(assignments []any) *armAuthorizationClient {
		return &armAuthorizationClient{client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			switch {
			case strings.Contains(request.URL.Path, "/roleAssignments"):
				return securityJSONResponse(t, http.StatusOK, map[string]any{"value": assignments}), nil
			case request.URL.Path == testRoleDefinitionID:
				return securityJSONResponse(t, http.StatusOK, map[string]any{"id": testRoleDefinitionID, "properties": map[string]any{"permissions": []any{map[string]any{"actions": []string{"*/read"}}}}}), nil
			case request.URL.Path == tagRole:
				return securityJSONResponse(t, http.StatusOK, map[string]any{"id": tagRole, "properties": map[string]any{"permissions": []any{map[string]any{"actions": []string{"Microsoft.Resources/tags/write"}}}}}), nil
			case strings.HasSuffix(request.URL.Path, "/permissions"):
				return securityJSONResponse(t, http.StatusOK, map[string]any{"value": []any{map[string]any{"actions": []string{"*/read", "Microsoft.Resources/tags/write"}}}}), nil
			case strings.HasSuffix(request.URL.Path, "/resourcegroups"):
				return securityJSONResponse(t, http.StatusOK, map[string]any{"value": []any{}}), nil
			default:
				t.Fatalf("unexpected ARM request %s", request.URL)
				return nil, nil
			}
		})}}
	}
	required := []string{"*/read", "Microsoft.Resources/tags/write"}
	if _, _, err := newClient([]any{assignment(testRoleDefinitionID), assignment(tagRole)}).verify(context.Background(), "token", testAgentPrincipalID, []string{testSubscriptionID}, required); err != nil {
		t.Fatalf("direct Reader + Tag Contributor verification failed: %v", err)
	}
	if _, _, err := newClient([]any{assignment(testRoleDefinitionID)}).verify(context.Background(), "token", testAgentPrincipalID, []string{testSubscriptionID}, required); !errors.Is(err, errDownstreamForbidden) {
		t.Fatalf("Reader verified a write action: %v", err)
	}
}
