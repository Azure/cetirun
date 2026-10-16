// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestSandboxCleanupSignalsWithoutSandboxRef(t *testing.T) {
	for name, signal := range map[string]string{
		"authorization binding": `{"metadata":{"annotations":{"cetirun.microsoft.com/sandbox-authorization":""}}}`,
		"reauthorize binding":   `{"metadata":{"annotations":{"cetirun.microsoft.com/sandbox-reauthorize":""}}}`,
		"hold finalizer":        `{"metadata":{"finalizers":["cetirun.microsoft.com/sandbox-hold"]}}`,
		"preparation finalizer": `{"metadata":{"finalizers":["cetirun.microsoft.com/sandbox-preparation"]}}`,
		"bootstrap finalizer":   `{"metadata":{"finalizers":["cetirun.microsoft.com/sandbox-bootstrap"]}}`,
		"model budget":          `{"spec":{"modelAccess":{"monthlyBudgetUSD":"10"}}}`,
		"team policy binding":   `{"status":{"policy":{}}}`,
		"gateway policy":        `{"status":{"aiGateway":{"policyRevision":"revision"}}}`,
		"sandbox status":        `{"status":{"sandbox":{}}}`,
		"sandbox condition":     `{"status":{"conditions":[{"type":"SandboxReady","status":"False"}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newMigrationFixture(t)
			f.putCentralConnection(t, migrationAgentConnection(time.Now().UTC()))
			if err := json.Unmarshal([]byte(signal), &f.agent); err != nil {
				t.Fatal(err)
			}
			agent := agentFromDocument(f.agent)
			if f.agent.Spec.SandboxRef != "" || !agent.UnsupportedRuntime {
				t.Fatal("persisted Sandbox deletion signal was not recognized")
			}
			base := f.store.client.Transport
			f.store.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/api/v1/namespaces/agents/secrets" {
					return securityJSONResponse(t, http.StatusForbidden, map[string]any{}), nil
				}
				return base.RoundTrip(r)
			})
			if err := f.store.deleteAgentRecords(context.Background(), agent); err != nil {
				t.Fatal(err)
			}
			if len(f.secrets["credentials"]) != 0 {
				t.Fatal("legacy central record prevented Sandbox finalizer cleanup")
			}
		})
	}
}

func TestAgentRecordCleanupPreservesSharedCredentialsAndOtherAgentGrants(t *testing.T) {
	f := newMigrationFixture(t)
	ctx, now := context.Background(), time.Now().UTC().Truncate(time.Second)
	shared := migrationDelegatedConnection(now)
	f.putCentralConnection(t, shared)
	f.putCentralConnection(t, migrationAgentConnection(now))
	f.putCentralGrant(t, migrationGrant("personal", shared.Connection.ID, now))
	if _, err := migrateAgentRecords(ctx, f.store); err != nil {
		t.Fatal(err)
	}
	other := migrationGrant("other-agent", shared.Connection.ID, now)
	other.Grant.InstanceUID = "66666666-6666-6666-6666-666666666666"
	f.putCentralGrant(t, other)
	// A legacy record left during interrupted migration is also cleaned.
	f.putCentralGrant(t, migrationGrant("legacy", shared.Connection.ID, now))
	f.agent.Metadata.DeletionTimestamp = &now
	agent := agentFromDocument(f.agent)
	if err := f.store.deleteAgentRecords(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if len(f.secrets["agents"]) != 0 {
		t.Fatal("Agent-local records survived cleanup")
	}
	if len(f.secrets["credentials"]) != 2 {
		t.Fatal("cleanup deleted shared or foreign records")
	}
	retained, _, err := f.centralStore().getConnection(ctx, shared.Connection.ID)
	if err != nil || !sameConnectionRecord(retained, shared) {
		t.Fatal("shared OAuth credential changed")
	}
	if err := f.store.deleteAgentRecords(ctx, agent); err != nil {
		t.Fatalf("repeated cleanup: %v", err)
	}
}

func TestAgentRecordCleanupRetriesDeleteConflict(t *testing.T) {
	f := newMigrationFixture(t)
	ctx, now := context.Background(), time.Now().UTC().Truncate(time.Second)
	f.putCentralGrant(t, migrationGrant("legacy", "unused", now))
	f.conflictNextDelete = true
	agent := agentFromDocument(f.agent)
	if err := f.store.deleteAgentRecords(ctx, agent); !errors.Is(err, errConflict) {
		t.Fatalf("cleanup conflict = %v", err)
	}
	if len(f.secrets["credentials"]) != 1 {
		t.Fatal("conflicting Secret was removed")
	}
	if err := f.store.deleteAgentRecords(ctx, agent); err != nil {
		t.Fatal(err)
	}
}

func TestAgentRecordCleanupHandlesMissingNamespace(t *testing.T) {
	f := newMigrationFixture(t)
	ctx, now := context.Background(), time.Now().UTC().Truncate(time.Second)
	f.putCentralGrant(t, migrationGrant("legacy", "unused", now))
	base := f.store.client.Transport
	f.store.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/v1/namespaces/agents" {
			return securityJSONResponse(t, http.StatusNotFound, map[string]any{}), nil
		}
		return base.RoundTrip(r)
	})
	if err := f.store.deleteAgentRecords(ctx, agentFromDocument(f.agent)); err != nil {
		t.Fatal(err)
	}
	if len(f.secrets["credentials"]) != 0 {
		t.Fatal("missing namespace prevented legacy cleanup")
	}
}

func TestDeletingAgentCannotCreateGrantConnectionOrReference(t *testing.T) {
	f := newMigrationFixture(t)
	ctx, now := context.Background(), time.Now().UTC().Truncate(time.Second)
	shared := migrationDelegatedConnection(now)
	f.putCentralConnection(t, shared)
	f.agent.Metadata.DeletionTimestamp = &now
	if err := f.store.putGrant(ctx, migrationGrant("new", shared.Connection.ID, now), ""); err == nil {
		t.Fatal("deleting Agent accepted a grant")
	}
	if err := f.store.putConnection(ctx, migrationAgentConnection(now), ""); err == nil {
		t.Fatal("deleting Agent accepted a connection")
	}
	if err := f.store.ensureConnectionReference(ctx, agentFromDocument(f.agent), shared.Connection); err == nil {
		t.Fatal("deleting Agent accepted a reference")
	}
	if len(f.secrets["agents"]) != 0 {
		t.Fatal("deleting Agent received new records")
	}
	legacy := newAuthorizationFixture(t, "github", "github_issues_list", Constraints{RepositoryIDs: []string{"42"}})
	legacy.agent.Metadata.DeletionTimestamp = &now
	if response := grantCreateRequest(t, legacy); response.Code != http.StatusForbidden {
		t.Fatalf("portal create while deleting = %d", response.Code)
	}
}

func TestRecreatedAgentCannotReadOldUIDGrant(t *testing.T) {
	f := newMigrationFixture(t)
	ctx, now := context.Background(), time.Now().UTC().Truncate(time.Second)
	f.putCentralGrant(t, migrationGrant("old", "unused", now))
	// Revoked grants can migrate without a live connection.
	old, version, err := f.centralStore().getGrant(ctx, "old")
	if err != nil {
		t.Fatal(err)
	}
	old.Grant.Revoked = true
	if err := f.centralStore().putGrant(ctx, old, version); err != nil {
		t.Fatal(err)
	}
	if _, err := migrateAgentRecords(ctx, f.store); err != nil {
		t.Fatal(err)
	}
	f.agent.Metadata.UID = "66666666-6666-6666-6666-666666666666"
	agent := agentFromDocument(f.agent)
	if _, _, err := f.store.getGrant(ctx, "old", agent); err == nil {
		t.Fatal("new UID inherited old Agent grant")
	}
	secret := f.secrets["agents"][toolRecordName("grant", "old")]
	secret.Metadata.OwnerReferences = []ownerReference{agentRecordOwner(agent)}
	f.secrets["agents"][secret.Metadata.Name] = secret
	if _, _, err := f.store.getGrant(ctx, "old", agent); err == nil {
		t.Fatal("changing metadata bypassed encrypted UID binding")
	}
}

func TestCleanupRetainsAccessForTerminatingNamespace(t *testing.T) {
	f := newMigrationFixture(t)
	ctx, now := context.Background(), time.Now().UTC().Truncate(time.Second)
	f.putCentralConnection(t, migrationAgentConnection(now))
	if _, err := migrateAgentRecords(ctx, f.store); err != nil {
		t.Fatal(err)
	}
	f.agent.Metadata.DeletionTimestamp = &now
	base := f.store.client.Transport
	f.store.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/v1/namespaces/agents" {
			return securityJSONResponse(t, http.StatusOK, map[string]any{"metadata": secretMetadata{
				DeletionTimestamp: &now, OwnerReferences: []ownerReference{agentRecordOwner(agentFromDocument(f.agent))},
			}}), nil
		}
		return base.RoundTrip(r)
	})
	if err := f.store.deleteAgentRecords(ctx, agentFromDocument(f.agent)); err != nil {
		t.Fatal(err)
	}
	if len(f.secrets["agents"]) != 0 {
		t.Fatal("terminating namespace prevented cleanup")
	}
}

func TestSandboxCleanupDoesNotRequireLocalSecretAccess(t *testing.T) {
	f := newMigrationFixture(t)
	ctx, now := context.Background(), time.Now().UTC().Truncate(time.Second)
	f.putCentralConnection(t, migrationAgentConnection(now))
	if _, err := migrateAgentRecords(ctx, f.store); err != nil {
		t.Fatal(err)
	}
	// A runtime transition can leave UID-owned local records. Sandbox has no
	// Broker Secret binding; its namespace/Agent GC will remove these records.
	f.putCentralConnection(t, migrationAgentConnection(now))
	f.agent.Spec.SandboxRef = "later-sandbox"
	f.agent.Metadata.DeletionTimestamp = &now
	base := f.store.client.Transport
	localRequests := 0
	f.store.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/v1/namespaces/agents/secrets" {
			localRequests++
			return securityJSONResponse(t, http.StatusForbidden, map[string]any{}), nil
		}
		return base.RoundTrip(r)
	})
	if err := f.store.deleteAgentRecords(ctx, agentFromDocument(f.agent)); err != nil {
		t.Fatal(err)
	}
	if localRequests != 0 || len(f.secrets["credentials"]) != 0 || len(f.secrets["agents"]) != 1 {
		t.Fatal("Sandbox cleanup required local access or did not clean legacy records")
	}
	for _, secret := range f.secrets["agents"] {
		if !recordOwnedBy(secret, agentFromDocument(f.agent)) {
			t.Fatal("local record is not protected by Agent UID garbage collection")
		}
	}
}
