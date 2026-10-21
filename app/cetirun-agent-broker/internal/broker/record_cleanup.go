// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"context"
	"errors"
)

func (k *kubeClient) deleteAgentRecords(ctx context.Context, agent Agent) error {
	stores := []*kubeClient{}
	// Unsupported runtimes rely on namespace/Agent garbage collection.
	if k.agentNamespaces && agent.Namespace != "" && !agent.UnsupportedRuntime {
		target, err := k.agentRecordStore(ctx, agent.UID, agent.Name)
		if err != nil && !errors.Is(err, errNotFound) {
			return err
		}
		if err == nil {
			stores = append(stores, target)
		}
	}
	if !k.agentNamespaces {
		stores = append(stores, k)
	}
	owner := Owner{agent.OwnerTenantID, agent.OwnerObjectID}
	for _, target := range stores {
		bindings, err := target.listToolSecrets(ctx, "binding", recordLabelPrefix+"instance-uid="+agent.UID)
		if errors.Is(err, errNotFound) && target.recordAgent != nil {
			continue
		}
		if err != nil {
			return err
		}
		for _, secret := range bindings.Items {
			record, err := target.decodeBinding(ctx, secret)
			if err != nil {
				return err
			}
			if record.Binding.InstanceUID != agent.UID || !sameOwner(record.Binding.Owner, owner) || secret.Metadata.Name != toolRecordName("binding", record.Binding.ID) {
				return errors.New("Binding cleanup ownership is invalid")
			}
			if err := target.deleteToolSecret(ctx, secret); err != nil {
				return err
			}
		}

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
	}
	return nil
}
