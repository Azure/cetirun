// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	migrationAgentUID = "11111111-1111-1111-1111-111111111111"
	migrationTenant   = "22222222-2222-2222-2222-222222222222"
	migrationOwner    = "33333333-3333-3333-3333-333333333333"
)

type migrationFixture struct {
	t                  *testing.T
	store              *kubeClient
	agent              agentDocument
	secrets            map[string]map[string]secretDocument
	version            uint64
	conflictNextDelete bool
	server             *httptest.Server
}

func newMigrationFixture(t *testing.T) *migrationFixture {
	t.Helper()
	fixture := &migrationFixture{t: t, secrets: map[string]map[string]secretDocument{"credentials": {}, "agents": {}}}
	fixture.agent.Metadata.Name, fixture.agent.Metadata.UID = "agent", migrationAgentUID
	fixture.agent.Metadata.Annotations = map[string]string{
		"agentidentity.cetirun.microsoft.com/owner-tenant-id": migrationTenant,
		"agentidentity.cetirun.microsoft.com/owner-object-id": migrationOwner,
	}
	fixture.agent.Status.Namespace, fixture.agent.Status.Phase = "agents", "Ready"
	fixture.agent.Spec.Agent.Type = "hermes"
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(fixture.server.Close)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.store = &kubeClient{
		baseURL: fixture.server.URL, namespace: "credentials", tokenFile: tokenFile, agentNamespaces: true,
		client: fixture.server.Client(), wrapper: memoryWrapper{key: []byte("migration-encryption-key")},
	}
	return fixture
}

func (fixture *migrationFixture) serveHTTP(response http.ResponseWriter, request *http.Request) {
	fixture.t.Helper()
	if request.Method == http.MethodGet && request.URL.Path == "/apis/cetirun.microsoft.com/v1alpha1/agents" {
		fixture.writeJSON(response, http.StatusOK, agentList{Items: []agentDocument{fixture.agent}})
		return
	}
	if request.Method == http.MethodGet && request.URL.Path == "/api/v1/namespaces/agents" {
		fixture.writeJSON(response, http.StatusOK, map[string]any{"metadata": secretMetadata{OwnerReferences: []ownerReference{agentRecordOwner(agentFromDocument(fixture.agent))}}})
		return
	}
	const prefix = "/api/v1/namespaces/"
	if !strings.HasPrefix(request.URL.Path, prefix) {
		fixture.t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
	}
	rest := strings.TrimPrefix(request.URL.Path, prefix)
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || parts[1] != "secrets" || (len(parts) != 2 && len(parts) != 3) {
		fixture.t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
	}
	namespace := parts[0]
	if _, ok := fixture.secrets[namespace]; !ok {
		fixture.t.Fatalf("unexpected namespace %q", namespace)
	}
	if len(parts) == 2 {
		fixture.handleSecretCollection(response, request, namespace)
		return
	}
	fixture.handleSecret(response, request, namespace, parts[2])
}

func (fixture *migrationFixture) handleSecretCollection(response http.ResponseWriter, request *http.Request, namespace string) {
	switch request.Method {
	case http.MethodGet:
		items := []secretDocument{}
		for _, secret := range fixture.secrets[namespace] {
			if migrationSecretMatches(secret, request.URL.Query().Get("labelSelector")) {
				items = append(items, secret)
			}
		}
		fixture.writeJSON(response, http.StatusOK, secretList{Items: items})
	case http.MethodPost:
		var secret secretDocument
		if err := json.NewDecoder(request.Body).Decode(&secret); err != nil {
			fixture.t.Fatal(err)
		}
		if _, exists := fixture.secrets[namespace][secret.Metadata.Name]; exists {
			fixture.writeJSON(response, http.StatusConflict, map[string]any{})
			return
		}
		fixture.version++
		secret.Metadata.Namespace = namespace
		secret.Metadata.UID = "secret-" + strconv.FormatUint(fixture.version, 10)
		secret.Metadata.ResourceVersion = strconv.FormatUint(fixture.version, 10)
		fixture.secrets[namespace][secret.Metadata.Name] = secret
		fixture.writeJSON(response, http.StatusCreated, secret)
	default:
		fixture.t.Fatalf("unexpected collection request: %s", request.Method)
	}
}

func (fixture *migrationFixture) handleSecret(response http.ResponseWriter, request *http.Request, namespace, name string) {
	secret, exists := fixture.secrets[namespace][name]
	switch request.Method {
	case http.MethodGet:
		if !exists {
			fixture.writeJSON(response, http.StatusNotFound, map[string]any{})
			return
		}
		fixture.writeJSON(response, http.StatusOK, secret)
	case http.MethodPut:
		if !exists {
			fixture.writeJSON(response, http.StatusNotFound, map[string]any{})
			return
		}
		var replacement secretDocument
		if err := json.NewDecoder(request.Body).Decode(&replacement); err != nil {
			fixture.t.Fatal(err)
		}
		if replacement.Metadata.ResourceVersion != secret.Metadata.ResourceVersion {
			fixture.writeJSON(response, http.StatusConflict, map[string]any{})
			return
		}
		fixture.version++
		replacement.Metadata.Namespace, replacement.Metadata.UID = namespace, secret.Metadata.UID
		replacement.Metadata.ResourceVersion = strconv.FormatUint(fixture.version, 10)
		fixture.secrets[namespace][name] = replacement
		fixture.writeJSON(response, http.StatusOK, replacement)
	case http.MethodDelete:
		if !exists {
			fixture.writeJSON(response, http.StatusNotFound, map[string]any{})
			return
		}
		if fixture.conflictNextDelete {
			fixture.conflictNextDelete = false
			fixture.version++
			secret.Metadata.ResourceVersion = strconv.FormatUint(fixture.version, 10)
			fixture.secrets[namespace][name] = secret
			fixture.writeJSON(response, http.StatusConflict, map[string]any{})
			return
		}
		var options struct {
			Preconditions map[string]string `json:"preconditions"`
		}
		if err := json.NewDecoder(request.Body).Decode(&options); err != nil {
			fixture.t.Fatal(err)
		}
		if options.Preconditions["uid"] != secret.Metadata.UID || options.Preconditions["resourceVersion"] != secret.Metadata.ResourceVersion {
			fixture.writeJSON(response, http.StatusConflict, map[string]any{})
			return
		}
		delete(fixture.secrets[namespace], name)
		fixture.writeJSON(response, http.StatusOK, map[string]any{})
	default:
		fixture.t.Fatalf("unexpected item request: %s", request.Method)
	}
}

func (fixture *migrationFixture) writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	if err := json.NewEncoder(response).Encode(value); err != nil {
		fixture.t.Fatal(err)
	}
}

func migrationSecretMatches(secret secretDocument, selector string) bool {
	for _, requirement := range strings.Split(selector, ",") {
		if requirement == "" {
			continue
		}
		key, value, found := strings.Cut(requirement, "=")
		if !found || secret.Metadata.Labels[key] != value {
			return false
		}
	}
	return true
}

func (fixture *migrationFixture) centralStore() *kubeClient {
	central := *fixture.store
	central.agentNamespaces, central.recordAgent = false, nil
	return &central
}

func (fixture *migrationFixture) putCentralConnection(t *testing.T, record *connectionRecord) {
	t.Helper()
	if err := fixture.centralStore().putConnection(context.Background(), record, ""); err != nil {
		t.Fatal(err)
	}
}

func (fixture *migrationFixture) putCentralGrant(t *testing.T, record *grantRecord) {
	t.Helper()
	if err := fixture.centralStore().putGrant(context.Background(), record, ""); err != nil {
		t.Fatal(err)
	}
}

func migrationAgentConnection(now time.Time) *connectionRecord {
	return &connectionRecord{Connection: Connection{
		ID: "agent-connection", Owner: Owner{TenantID: migrationTenant, ObjectID: migrationOwner}, Provider: "azure", Mode: modeAgent,
		Identity:  &IdentityBinding{InstanceUID: migrationAgentUID, TenantID: migrationTenant, ClientID: "44444444-4444-4444-4444-444444444444", PrincipalID: "55555555-5555-5555-5555-555555555555"},
		AccountID: "55555555-5555-5555-5555-555555555555", Status: "connected", Revision: 1, CreatedAt: now, UpdatedAt: now,
	}}
}

func migrationDelegatedConnection(now time.Time) *connectionRecord {
	return &connectionRecord{Connection: Connection{
		ID: "shared-connection", Owner: Owner{TenantID: migrationTenant, ObjectID: migrationOwner}, Provider: "github", AccountID: "42", Status: "connected", Revision: 1, CreatedAt: now, UpdatedAt: now,
	}, Credential: &providerCredential{AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", ExpiresAt: now.Add(time.Hour)}}
}

func migrationGrant(id, connectionID string, now time.Time) *grantRecord {
	return &grantRecord{Grant: AgentGrant{
		ID: id, Owner: Owner{TenantID: migrationTenant, ObjectID: migrationOwner}, InstanceUID: migrationAgentUID, ConnectionID: connectionID,
		Tools: []string{"azure_resource_groups_list"}, ExpiresAt: now.Add(time.Hour), Revision: 1, CreatedAt: now, UpdatedAt: now,
	}}
}

func TestMigrateAgentRecordsMovesScopedRecordsAndRetainsSharedCredential(t *testing.T) {
	fixture, now := newMigrationFixture(t), time.Now().UTC().Truncate(time.Second)
	fixture.putCentralConnection(t, migrationAgentConnection(now))
	fixture.putCentralConnection(t, migrationDelegatedConnection(now))
	fixture.putCentralGrant(t, migrationGrant("agent-grant", "agent-connection", now))
	fixture.putCentralGrant(t, migrationGrant("shared-grant", "shared-connection", now))

	result, err := migrateAgentRecords(context.Background(), fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	if result.AgentConnectionsMoved != 1 || result.DelegatedConnectionsKept != 1 || result.GrantsMoved != 2 || result.ConnectionReferencesCreated != 1 {
		t.Fatalf("unexpected migration result: %#v", result)
	}
	if _, present := fixture.secrets["credentials"][toolRecordName("connection", "agent-connection")]; present {
		t.Fatal("central Agent Connection was retained")
	}
	if _, present := fixture.secrets["credentials"][toolRecordName("connection", "shared-connection")]; !present {
		t.Fatal("shared delegated Connection was deleted")
	}
	for _, name := range []string{toolRecordName("grant", "agent-grant"), toolRecordName("grant", "shared-grant")} {
		if _, present := fixture.secrets["credentials"][name]; present {
			t.Fatalf("central Grant %q was retained", name)
		}
		if _, present := fixture.secrets["agents"][name]; !present {
			t.Fatalf("Agent Grant %q was not moved", name)
		}
	}
	reference := fixture.secrets["agents"][toolRecordName("connection", "shared-connection")]
	if reference.Metadata.Labels[recordLabelPrefix+"record-type"] != "connection-reference" || len(reference.Data) != 0 {
		t.Fatalf("shared credential reference = %#v", reference)
	}

	// Exercise the normal namespaced lookup and write paths after cutover. This
	// ensures the migration did not merely arrange Secret locations that the
	// Broker cannot use for independent and delegated authorization.
	agent := agentFromDocument(fixture.agent)
	if connection, _, lookupErr := fixture.store.getConnectionForAgent(context.Background(), agent, "agent-connection"); lookupErr != nil || connection.Connection.Identity == nil {
		t.Fatalf("Agent Connection lookup = %#v, %v", connection, lookupErr)
	}
	if connection, _, lookupErr := fixture.store.getConnectionForAgent(context.Background(), agent, "shared-connection"); lookupErr != nil || connection.Credential == nil {
		t.Fatalf("delegated Connection lookup = %#v, %v", connection, lookupErr)
	}
	if grants, lookupErr := fixture.store.listGrants(context.Background(), Owner{TenantID: migrationTenant, ObjectID: migrationOwner}, migrationAgentUID); lookupErr != nil || len(grants) != 2 {
		t.Fatalf("Agent Grant list = %#v, %v", grants, lookupErr)
	}
	newGrant := migrationGrant("new-shared-grant", "shared-connection", now)
	if putErr := fixture.store.putGrant(context.Background(), newGrant, ""); putErr != nil {
		t.Fatal(putErr)
	}
	if stored, _, lookupErr := fixture.store.getGrant(context.Background(), newGrant.Grant.ID, agent); lookupErr != nil || stored.Grant.ID != newGrant.Grant.ID {
		t.Fatalf("new Agent Grant lookup = %#v, %v", stored, lookupErr)
	}
}

func TestMigrateAgentRecordsResumesMatchingTarget(t *testing.T) {
	fixture, now := newMigrationFixture(t), time.Now().UTC().Truncate(time.Second)
	record := migrationAgentConnection(now)
	fixture.putCentralConnection(t, record)
	agent := agentFromDocument(fixture.agent)
	target, err := fixture.centralStore().recordStoreForAgent(context.Background(), agent)
	if err != nil {
		t.Fatal(err)
	}
	copy := cloneConnectionRecord(record)
	copy.InstanceName, copy.InstanceNamespace = agent.Name, agent.Namespace
	if err := target.putConnection(context.Background(), copy, ""); err != nil {
		t.Fatal(err)
	}

	result, err := migrateAgentRecords(context.Background(), fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	if result.AgentConnectionsMoved != 0 || result.AgentConnectionsResumed != 1 {
		t.Fatalf("unexpected resume result: %#v", result)
	}
	if _, present := fixture.secrets["credentials"][toolRecordName("connection", record.Connection.ID)]; present {
		t.Fatal("resumed central source was not deleted")
	}
}

func TestMigrateAgentRecordsRejectsDivergentTarget(t *testing.T) {
	fixture, now := newMigrationFixture(t), time.Now().UTC().Truncate(time.Second)
	record := migrationAgentConnection(now)
	fixture.putCentralConnection(t, record)
	agent := agentFromDocument(fixture.agent)
	target, err := fixture.centralStore().recordStoreForAgent(context.Background(), agent)
	if err != nil {
		t.Fatal(err)
	}
	divergent := cloneConnectionRecord(record)
	divergent.Connection.Status = "disconnected"
	if err := target.putConnection(context.Background(), divergent, ""); err != nil {
		t.Fatal(err)
	}

	_, err = migrateAgentRecords(context.Background(), fixture.store)
	if !errors.Is(err, errAgentRecordMigrationConflict) {
		t.Fatalf("migration error = %v, want divergent target conflict", err)
	}
	if _, present := fixture.secrets["credentials"][toolRecordName("connection", record.Connection.ID)]; !present {
		t.Fatal("divergent source was deleted")
	}
}

func TestMigrateAgentRecordsUsesDeletePreconditions(t *testing.T) {
	fixture, now := newMigrationFixture(t), time.Now().UTC().Truncate(time.Second)
	record := migrationAgentConnection(now)
	fixture.putCentralConnection(t, record)
	fixture.conflictNextDelete = true

	_, err := migrateAgentRecords(context.Background(), fixture.store)
	if !errors.Is(err, errConflict) {
		t.Fatalf("migration error = %v, want CAS conflict", err)
	}
	if _, present := fixture.secrets["credentials"][toolRecordName("connection", record.Connection.ID)]; !present {
		t.Fatal("source was deleted after a CAS conflict")
	}
	if _, present := fixture.secrets["agents"][toolRecordName("connection", record.Connection.ID)]; !present {
		t.Fatal("verified target was not retained after a CAS conflict")
	}
}
