// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"context"
	"errors"
)

// Called by the authenticated Operator before the Agent finalizer is removed.
// Shared personal credentials are never owned or deleted by an Agent.
func (k *kubeClient) deleteAgentRecords(ctx context.Context, agent Agent) error {
	stores := []*kubeClient{}
	// Unsupported runtimes (including Sandbox) cannot use instance grants and
	// do not receive Broker Secret RBAC. Their finalizer must still clean legacy
	// central records. Any local records from a previous supported runtime carry
	// the immutable Agent owner reference and are removed by Agent/namespace GC;
	// do not expand Sandbox access solely to perform eager cleanup.
	if k.agentNamespaces && agent.Namespace != "" && !agent.UnsupportedRuntime {
		target, err := k.agentRecordStore(ctx, agent.UID)
		if err != nil && !errors.Is(err, errNotFound) {
			return err
		}
		if err == nil {
			stores = append(stores, target)
		}
	}
	central := *k
	central.agentNamespaces, central.recordAgent = false, nil
	stores = append(stores, &central)
	owner := Owner{agent.OwnerTenantID, agent.OwnerObjectID}
	for _, target := range stores {
		grants, err := target.listToolSecrets(ctx, "grant", recordLabelPrefix+"instance-uid="+agent.UID)
		if errors.Is(err, errNotFound) && target.recordAgent != nil {
			continue
		}
		if err != nil {
			return err
		}
		for _, secret := range grants.Items {
			record, err := target.decodeGrant(ctx, secret)
			if err != nil {
				return err
			}
			if record.Grant.InstanceUID != agent.UID || !sameOwner(record.Grant.Owner, owner) || secret.Metadata.Name != toolRecordName("grant", record.Grant.ID) {
				return errors.New("Grant cleanup ownership is invalid")
			}
			if err := target.deleteToolSecret(ctx, secret); err != nil {
				return err
			}
		}
		// Older independent Connections did not have an instance UID label.
		connections, err := target.listToolSecrets(ctx, "connection", recordLabelPrefix+"owner-tenant="+owner.TenantID+","+recordLabelPrefix+"owner-object="+owner.ObjectID)
		if errors.Is(err, errNotFound) && target.recordAgent != nil {
			continue
		}
		if err != nil {
			return err
		}
		for _, secret := range connections.Items {
			record, err := target.decodeConnection(ctx, secret)
			if err != nil {
				return err
			}
			c := record.Connection
			if connectionMode(c) != modeAgent || c.Identity == nil || c.Identity.InstanceUID != agent.UID {
				continue
			}
			if !sameOwner(c.Owner, owner) || secret.Metadata.Name != toolRecordName("connection", c.ID) {
				return errors.New("Connection cleanup ownership is invalid")
			}
			if err := target.deleteToolSecret(ctx, secret); err != nil {
				return err
			}
		}
		if target.recordAgent == nil {
			continue
		}
		references, err := target.listToolSecrets(ctx, "connection-reference", recordLabelPrefix+"instance-uid="+agent.UID)
		if errors.Is(err, errNotFound) && target.recordAgent != nil {
			continue
		}
		if err != nil {
			return err
		}
		for _, secret := range references.Items {
			id := secret.Metadata.Labels[recordLabelPrefix+"connection-id"]
			if !validConnectionReference(secret, agent, id) || secret.Metadata.Name != toolRecordName("connection", id) {
				return errors.New("Connection reference cleanup ownership is invalid")
			}
			if err := target.deleteToolSecret(ctx, secret); err != nil {
				return err
			}
		}
	}
	return nil
}
