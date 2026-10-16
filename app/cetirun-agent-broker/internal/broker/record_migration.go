// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"context"
	"errors"
	"reflect"
)

// MigrationResult contains aggregate, non-sensitive migration results. The
// migration deliberately does not expose record IDs or encrypted content.
type MigrationResult struct {
	AgentConnectionsMoved       int
	AgentConnectionsResumed     int
	DelegatedConnectionsKept    int
	GrantsMoved                 int
	GrantsResumed               int
	ConnectionReferencesCreated int
}

var (
	errAgentRecordMigrationDisabled = errors.New("Agent record namespaces are disabled")
	errAgentRecordMigrationOrphan   = errors.New("Agent record migration found an orphaned record")
	errAgentRecordMigrationConflict = errors.New("Agent record migration found a divergent target record")
)

// MigrateAgentRecords moves instance-scoped records from the historical shared
// credential namespace to the namespace owned by their Agent. It is an
// explicit, offline operation: all Broker replicas must be quiesced for its
// duration. Kubernetes cannot enforce that condition, so the caller is
// responsible for scaling or otherwise stopping every Broker before invoking
// this function.
//
// The operation is safe to retry after interruption. A matching target record
// is resumed and its central source is then removed with a resource-version
// precondition. A divergent target is never overwritten. Delegated connections
// remain centrally stored because their credentials can be shared by Agents.
func MigrateAgentRecords(ctx context.Context, cfg Config) (MigrationResult, error) {
	if !cfg.AgentRecordNamespaces {
		return MigrationResult{}, errAgentRecordMigrationDisabled
	}
	store, err := newKubeClient(cfg, newKeyVaultWrapper(cfg))
	if err != nil {
		return MigrationResult{}, err
	}
	return migrateAgentRecords(ctx, store)
}

func migrateAgentRecords(ctx context.Context, store *kubeClient) (MigrationResult, error) {
	var result MigrationResult
	// Read the source strictly from the historical central namespace. A scoped
	// store must never use its normal lookup routing while this migration is
	// deciding whether it is safe to delete a source Secret.
	central := *store
	central.agentNamespaces = false
	central.recordAgent = nil

	connections, err := central.connectionRecords(ctx, "")
	if err != nil {
		return result, err
	}
	connectionByID := make(map[string]*connectionRecord, len(connections))
	for _, item := range connections {
		connectionByID[item.record.Connection.ID] = item.record
	}

	agents := map[string]Agent{}
	getAgent := func(uid string) (Agent, error) {
		if agent, ok := agents[uid]; ok {
			return agent, nil
		}
		agent, getErr := central.getAgentByUID(ctx, uid)
		if getErr != nil || agent.UID != uid || !dnsLabel(agent.Name) || !dnsLabel(agent.Namespace) ||
			!uuidPattern.MatchString(agent.OwnerTenantID) || !uuidPattern.MatchString(agent.OwnerObjectID) {
			return Agent{}, errAgentRecordMigrationOrphan
		}
		agents[uid] = agent
		return agent, nil
	}

	for _, item := range connections {
		record := item.record
		if connectionMode(record.Connection) != modeAgent {
			result.DelegatedConnectionsKept++
			continue
		}
		if record.Connection.Identity == nil || !uuidPattern.MatchString(record.Connection.Identity.InstanceUID) {
			return result, errAgentRecordMigrationOrphan
		}
		agent, agentErr := getAgent(record.Connection.Identity.InstanceUID)
		if agentErr != nil || !sameOwner(record.Connection.Owner, Owner{TenantID: agent.OwnerTenantID, ObjectID: agent.OwnerObjectID}) {
			return result, errAgentRecordMigrationOrphan
		}
		target, targetErr := central.agentRecordStore(ctx, agent.UID)
		if targetErr != nil {
			return result, targetErr
		}
		copy := cloneConnectionRecord(record)
		copy.InstanceName, copy.InstanceNamespace = agent.Name, agent.Namespace
		existing, _, getErr := target.getConnection(ctx, copy.Connection.ID)
		switch {
		case getErr == nil:
			if !sameConnectionRecord(existing, copy) {
				return result, errAgentRecordMigrationConflict
			}
			result.AgentConnectionsResumed++
		case errors.Is(getErr, errNotFound):
			if putErr := target.putConnection(ctx, copy, ""); putErr != nil {
				return result, putErr
			}
			verified, _, verifyErr := target.getConnection(ctx, copy.Connection.ID)
			if verifyErr != nil || !sameConnectionRecord(verified, copy) {
				return result, errAgentRecordMigrationConflict
			}
			result.AgentConnectionsMoved++
		default:
			return result, getErr
		}
		source, sourceErr := central.getToolSecret(ctx, "connection", copy.Connection.ID)
		if sourceErr != nil {
			return result, sourceErr
		}
		decoded, decodeErr := central.decodeConnection(ctx, source)
		if decodeErr != nil || !sameConnectionRecord(decoded, record) {
			return result, errAgentRecordMigrationConflict
		}
		if deleteErr := central.deleteToolSecret(ctx, source); deleteErr != nil {
			return result, deleteErr
		}
	}

	grants, err := central.grantRecords(ctx, "")
	if err != nil {
		return result, err
	}
	for _, item := range grants {
		record := item.record
		if !uuidPattern.MatchString(record.Grant.InstanceUID) {
			return result, errAgentRecordMigrationOrphan
		}
		agent, agentErr := getAgent(record.Grant.InstanceUID)
		if agentErr != nil || !sameOwner(record.Grant.Owner, Owner{TenantID: agent.OwnerTenantID, ObjectID: agent.OwnerObjectID}) {
			return result, errAgentRecordMigrationOrphan
		}
		target, targetErr := central.agentRecordStore(ctx, agent.UID)
		if targetErr != nil {
			return result, targetErr
		}

		if !record.Grant.Revoked {
			connection := connectionByID[record.Grant.ConnectionID]
			if connection == nil {
				// A previous invocation may have completed the connection move before
				// being interrupted. Its per-Agent copy is the only acceptable resume
				// source; do not infer a binding from a same-named object elsewhere.
				connection, _, targetErr = target.getConnection(ctx, record.Grant.ConnectionID)
				if targetErr != nil {
					return result, errAgentRecordMigrationOrphan
				}
			}
			if !sameOwner(connection.Connection.Owner, record.Grant.Owner) {
				return result, errAgentRecordMigrationOrphan
			}
			if connectionMode(connection.Connection) == modeAgent {
				if connection.Connection.Identity == nil || connection.Connection.Identity.InstanceUID != agent.UID {
					return result, errAgentRecordMigrationOrphan
				}
			} else {
				_, beforeErr := target.getToolSecret(ctx, "connection", connection.Connection.ID)
				if beforeErr != nil && !errors.Is(beforeErr, errNotFound) {
					return result, beforeErr
				}
				if ensureErr := store.ensureConnectionReference(ctx, agent, connection.Connection); ensureErr != nil {
					return result, ensureErr
				}
				if errors.Is(beforeErr, errNotFound) {
					result.ConnectionReferencesCreated++
				}
			}
		}

		copy := cloneGrantRecord(record)
		copy.InstanceName, copy.InstanceNamespace = agent.Name, agent.Namespace
		existing, _, getErr := target.getGrant(ctx, copy.Grant.ID)
		switch {
		case getErr == nil:
			if !sameGrantRecord(existing, copy) {
				return result, errAgentRecordMigrationConflict
			}
			result.GrantsResumed++
		case errors.Is(getErr, errNotFound):
			if putErr := target.putGrant(ctx, copy, ""); putErr != nil {
				return result, putErr
			}
			verified, _, verifyErr := target.getGrant(ctx, copy.Grant.ID)
			if verifyErr != nil || !sameGrantRecord(verified, copy) {
				return result, errAgentRecordMigrationConflict
			}
			result.GrantsMoved++
		default:
			return result, getErr
		}
		source, sourceErr := central.getToolSecret(ctx, "grant", copy.Grant.ID)
		if sourceErr != nil {
			return result, sourceErr
		}
		decoded, decodeErr := central.decodeGrant(ctx, source)
		if decodeErr != nil || !sameGrantRecord(decoded, record) {
			return result, errAgentRecordMigrationConflict
		}
		if deleteErr := central.deleteToolSecret(ctx, source); deleteErr != nil {
			return result, deleteErr
		}
	}
	return result, nil
}

func cloneConnectionRecord(record *connectionRecord) *connectionRecord {
	copy := *record
	return &copy
}

func cloneGrantRecord(record *grantRecord) *grantRecord {
	copy := *record
	return &copy
}

func sameConnectionRecord(left, right *connectionRecord) bool {
	if left == nil || right == nil {
		return left == right
	}
	leftCopy, rightCopy := cloneConnectionRecord(left), cloneConnectionRecord(right)
	leftCopy.InstanceName, leftCopy.InstanceNamespace = "", ""
	rightCopy.InstanceName, rightCopy.InstanceNamespace = "", ""
	return reflect.DeepEqual(leftCopy, rightCopy)
}

func sameGrantRecord(left, right *grantRecord) bool {
	if left == nil || right == nil {
		return left == right
	}
	leftCopy, rightCopy := cloneGrantRecord(left), cloneGrantRecord(right)
	leftCopy.InstanceName, leftCopy.InstanceNamespace = "", ""
	rightCopy.InstanceName, rightCopy.InstanceNamespace = "", ""
	return reflect.DeepEqual(leftCopy, rightCopy)
}
