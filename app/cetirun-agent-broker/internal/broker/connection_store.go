// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"time"
)

func toolRecordName(kind, id string) string { return "agent-tools-" + kind + "-" + id }

func connectionAAD(connection Connection) []byte {
	return []byte(fmt.Sprintf("agent-tools-connection-v1\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d", connection.ID, connection.Owner.TenantID, connection.Owner.ObjectID, connection.Provider, connection.AccountID, connection.Revision))
}

func bindingAAD(binding ConnectionBinding) []byte {
	return []byte(fmt.Sprintf("agent-tools-binding-v1\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d", binding.ID, binding.Owner.TenantID, binding.Owner.ObjectID, binding.InstanceUID, binding.ConnectionID, binding.Revision))
}

func recordRevision(data map[string]string) uint64 {
	raw, err := base64.StdEncoding.DecodeString(data["revision"])
	if err != nil {
		return 0
	}
	var revision uint64
	_, _ = fmt.Sscanf(string(raw), "%d", &revision)
	return revision
}

func (k *kubeClient) getToolSecret(ctx context.Context, kind, id string) (secretDocument, error) {
	var secret secretDocument
	_, err := k.request(ctx, http.MethodGet, "/api/v1/namespaces/"+url.PathEscape(k.namespace)+"/secrets/"+url.PathEscape(toolRecordName(kind, id)), nil, &secret)
	if err == nil && k.recordAgent != nil && !recordOwnedBy(secret, *k.recordAgent) {
		return secret, errors.New("Agent record ownership is invalid")
	}
	return secret, err
}

func (k *kubeClient) putToolSecret(ctx context.Context, kind, id string, labels, annotations, data map[string]string, resourceVersion string) error {
	labels["app.kubernetes.io/managed-by"] = "cetirun-agent-broker"
	labels["agenttools.cetirun.microsoft.com/record-type"] = kind
	secret := secretDocument{APIVersion: "v1", Kind: "Secret", Metadata: secretMetadata{Name: toolRecordName(kind, id), Namespace: k.namespace, ResourceVersion: resourceVersion, Labels: labels, Annotations: annotations}, Data: data}
	if k.recordAgent != nil {
		secret.Metadata.OwnerReferences = []ownerReference{agentRecordOwner(*k.recordAgent)}
	}
	endpoint, method := "/api/v1/namespaces/"+url.PathEscape(k.namespace)+"/secrets", http.MethodPost
	if resourceVersion != "" {
		endpoint, method = endpoint+"/"+url.PathEscape(secret.Metadata.Name), http.MethodPut
	}
	_, err := k.request(ctx, method, endpoint, secret, nil)
	return err
}

func recordInstanceMetadata(labels map[string]string, name, namespace string) map[string]string {
	annotations := map[string]string{}
	for key, value := range map[string]string{"instance-name": name, "instance-namespace": namespace} {
		if value == "" {
			continue
		}
		qualified := "agenttools.cetirun.microsoft.com/" + key
		annotations[qualified] = value
		if dnsLabel(value) {
			labels[qualified] = value
		}
	}
	return annotations
}

func (k *kubeClient) encodeConnection(ctx context.Context, record *connectionRecord) (map[string]string, error) {
	plain, err := json.Marshal(storedConnectionRecord{connectionRecord: record, OwnerTenantID: record.Connection.Owner.TenantID, OwnerObjectID: record.Connection.Owner.ObjectID, AccountID: record.Connection.AccountID, UpdatedAt: record.Connection.UpdatedAt})
	if err != nil {
		return nil, err
	}
	aad := connectionAAD(record.Connection)
	envelope, err := seal(ctx, k.wrapper, plain, aad)
	if err != nil {
		return nil, err
	}
	return map[string]string{"envelope.json": base64.StdEncoding.EncodeToString(envelope), "revision": base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d", record.Connection.Revision)))}, nil
}

func (k *kubeClient) decodeConnection(ctx context.Context, secret secretDocument) (*connectionRecord, error) {
	id, ownerTenant, ownerObject, provider := secret.Metadata.Labels["agenttools.cetirun.microsoft.com/id"], secret.Metadata.Labels["agenttools.cetirun.microsoft.com/owner-tenant"], secret.Metadata.Labels["agenttools.cetirun.microsoft.com/owner-object"], secret.Metadata.Labels["agenttools.cetirun.microsoft.com/provider"]
	revision := recordRevision(secret.Data)
	if id == "" || ownerTenant == "" || ownerObject == "" || provider == "" || revision == 0 {
		return nil, errors.New("connection record metadata is invalid")
	}
	envelope, err := base64.StdEncoding.DecodeString(secret.Data["envelope.json"])
	if err != nil {
		return nil, errors.New("connection record encoding is invalid")
	}
	stub := Connection{ID: id, Owner: Owner{TenantID: ownerTenant, ObjectID: ownerObject}, Provider: provider, AccountID: secret.Metadata.Labels["agenttools.cetirun.microsoft.com/account-id"], Revision: revision}
	aad := connectionAAD(stub)
	plain, err := openEnvelope(ctx, k.wrapper, envelope, aad)
	if err != nil {
		return nil, err
	}
	stored := storedConnectionRecord{connectionRecord: &connectionRecord{}}
	if json.Unmarshal(plain, &stored) != nil {
		return nil, errors.New("connection record is invalid")
	}
	record := stored.connectionRecord
	record.Connection.Owner = Owner{TenantID: stored.OwnerTenantID, ObjectID: stored.OwnerObjectID}
	record.Connection.AccountID, record.Connection.UpdatedAt = stored.AccountID, stored.UpdatedAt
	if record.Connection.ID != id || record.Connection.Revision != revision || !sameOwner(record.Connection.Owner, stub.Owner) || record.Connection.Provider != provider || record.Connection.AccountID != stub.AccountID {
		return nil, errors.New("connection record is invalid")
	}
	return record, nil
}

func (k *kubeClient) getConnection(ctx context.Context, id string) (*connectionRecord, string, error) {
	secret, err := k.getToolSecret(ctx, "connection", id)
	if err != nil {
		return nil, "", err
	}
	record, err := k.decodeConnection(ctx, secret)
	if err == nil {
		err = k.validateConnectionLocation(record)
	}
	return record, secret.Metadata.ResourceVersion, err
}

func (k *kubeClient) putConnection(ctx context.Context, record *connectionRecord, resourceVersion string) error {
	if k.agentNamespaces && connectionMode(record.Connection) == modeAgent {
		if record.Connection.Identity == nil {
			return errors.New("Agent identity binding is missing")
		}
		target, err := k.agentRecordStore(ctx, record.Connection.Identity.InstanceUID, record.InstanceName)
		if err != nil {
			return err
		}
		if target.recordAgent.Deleting {
			return errors.New("Agent is deleting")
		}
		if err := target.validateConnectionLocation(record); err != nil {
			return err
		}
		record.InstanceName, record.InstanceNamespace = target.recordAgent.Name, target.recordAgent.Namespace
		return target.putConnection(ctx, record, resourceVersion)
	}
	if err := k.validateConnectionLocation(record); err != nil {
		return err
	}
	if k.recordAgent != nil && (k.recordAgent.Deleting || k.recordNamespaceDeleting) {
		return errors.New("Agent is deleting")
	}

	data, err := k.encodeConnection(ctx, record)
	if err != nil {
		return err
	}
	labels := map[string]string{
		"agenttools.cetirun.microsoft.com/id":           record.Connection.ID,
		"agenttools.cetirun.microsoft.com/owner-tenant": record.Connection.Owner.TenantID,
		"agenttools.cetirun.microsoft.com/owner-object": record.Connection.Owner.ObjectID,
		"agenttools.cetirun.microsoft.com/provider":     record.Connection.Provider,
		"agenttools.cetirun.microsoft.com/account-id":   record.Connection.AccountID,
		"agenttools.cetirun.microsoft.com/mode":         connectionMode(record.Connection),
	}
	var annotations map[string]string
	if connectionMode(record.Connection) == modeAgent && record.Connection.Identity != nil {
		labels["agenttools.cetirun.microsoft.com/instance-uid"] = record.Connection.Identity.InstanceUID
		annotations = recordInstanceMetadata(labels, record.InstanceName, record.InstanceNamespace)
	}
	return k.putToolSecret(ctx, "connection", record.Connection.ID, labels, annotations, data, resourceVersion)
}

func (k *kubeClient) connectionRecords(ctx context.Context, selector string) ([]struct {
	record  *connectionRecord
	version string
}, error) {
	list, err := k.listToolSecrets(ctx, "connection", selector)
	if err != nil {
		return nil, err
	}
	result := make([]struct {
		record  *connectionRecord
		version string
	}, 0, len(list.Items))
	for _, secret := range list.Items {
		if secret.Metadata.Labels["agenttools.cetirun.microsoft.com/record-type"] != "connection" {
			continue
		}
		record, err := k.decodeConnection(ctx, secret)
		if err != nil {
			return nil, err
		}
		if err := k.validateConnectionLocation(record); err != nil {
			return nil, err
		}
		result = append(result, struct {
			record  *connectionRecord
			version string
		}{record, secret.Metadata.ResourceVersion})
	}
	return result, nil
}

func (k *kubeClient) listConnections(ctx context.Context, owner Owner) ([]Connection, error) {
	connections := []Connection{}
	if k.agentNamespaces {
		stores, err := k.ownerAgentStores(ctx, owner)
		if err != nil {
			return nil, err
		}
		for _, target := range stores {
			local, err := target.listConnections(ctx, owner)
			if err != nil {
				return nil, err
			}
			connections = append(connections, local...)
		}
	} else {
		records, err := k.connectionRecords(ctx, recordLabelPrefix+"owner-tenant="+owner.TenantID+","+recordLabelPrefix+"owner-object="+owner.ObjectID)
		if err != nil {
			return nil, err
		}
		for _, item := range records {
			if !item.record.Deleted && sameOwner(item.record.Connection.Owner, owner) {
				connections = append(connections, item.record.Connection)
			}
		}
	}
	sort.Slice(connections, func(i, j int) bool { return connections[i].CreatedAt.Before(connections[j].CreatedAt) })
	return connections, nil
}

func (k *kubeClient) encodeBinding(ctx context.Context, record *bindingRecord) (map[string]string, error) {
	plain, err := json.Marshal(storedBindingRecord{bindingRecord: record, OwnerTenantID: record.Binding.Owner.TenantID, OwnerObjectID: record.Binding.Owner.ObjectID, Revoked: record.Binding.Revoked, CreatedAt: record.Binding.CreatedAt, UpdatedAt: record.Binding.UpdatedAt})
	if err != nil {
		return nil, err
	}
	envelope, err := seal(ctx, k.wrapper, plain, bindingAAD(record.Binding))
	if err != nil {
		return nil, err
	}
	return map[string]string{"envelope.json": base64.StdEncoding.EncodeToString(envelope), "revision": base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d", record.Binding.Revision)))}, nil
}

func (k *kubeClient) decodeBinding(ctx context.Context, secret secretDocument) (*bindingRecord, error) {
	id, ownerTenant, ownerObject, instanceUID, connectionID := secret.Metadata.Labels["agenttools.cetirun.microsoft.com/id"], secret.Metadata.Labels["agenttools.cetirun.microsoft.com/owner-tenant"], secret.Metadata.Labels["agenttools.cetirun.microsoft.com/owner-object"], secret.Metadata.Labels["agenttools.cetirun.microsoft.com/instance-uid"], secret.Metadata.Labels["agenttools.cetirun.microsoft.com/connection-id"]
	stub := ConnectionBinding{ID: id, Owner: Owner{TenantID: ownerTenant, ObjectID: ownerObject}, InstanceUID: instanceUID, ConnectionID: connectionID, Revision: recordRevision(secret.Data)}
	if id == "" || ownerTenant == "" || ownerObject == "" || instanceUID == "" || connectionID == "" || stub.Revision == 0 {
		return nil, errors.New("binding record metadata is invalid")
	}
	envelope, err := base64.StdEncoding.DecodeString(secret.Data["envelope.json"])
	if err != nil {
		return nil, errors.New("binding record encoding is invalid")
	}
	plain, err := openEnvelope(ctx, k.wrapper, envelope, bindingAAD(stub))
	if err != nil {
		return nil, err
	}
	stored := storedBindingRecord{bindingRecord: &bindingRecord{}}
	if json.Unmarshal(plain, &stored) != nil {
		return nil, errors.New("binding record is invalid")
	}
	record := stored.bindingRecord
	record.Binding.Owner = Owner{TenantID: stored.OwnerTenantID, ObjectID: stored.OwnerObjectID}
	record.Binding.Revoked, record.Binding.CreatedAt, record.Binding.UpdatedAt = stored.Revoked, stored.CreatedAt, stored.UpdatedAt
	if record.Binding.ID != id || record.Binding.Revision != stub.Revision || !sameOwner(record.Binding.Owner, stub.Owner) || record.Binding.InstanceUID != instanceUID || record.Binding.ConnectionID != connectionID {
		return nil, errors.New("binding record is invalid")
	}
	return record, nil
}

func (k *kubeClient) getBinding(ctx context.Context, id string, agents ...Agent) (*bindingRecord, string, error) {
	if k.agentNamespaces {
		if len(agents) != 1 {
			return nil, "", errors.New("Agent scope is required")
		}
		target, err := k.agentRecordStore(ctx, agents[0].UID, agents[0].Name)
		if err != nil {
			return nil, "", err
		}
		return target.getBinding(ctx, id)
	}
	secret, err := k.getToolSecret(ctx, "binding", id)
	if err != nil {
		return nil, "", err
	}
	record, err := k.decodeBinding(ctx, secret)
	if err == nil {
		err = k.validateBindingLocation(record)
	}
	return record, secret.Metadata.ResourceVersion, err
}

func (k *kubeClient) putBinding(ctx context.Context, record *bindingRecord, resourceVersion string) error {
	if k.agentNamespaces {
		target, err := k.agentRecordStore(ctx, record.Binding.InstanceUID, record.InstanceName)
		if err != nil {
			return err
		}
		if target.recordAgent.Deleting && !record.Binding.Revoked {
			return errors.New("Agent is deleting")
		}
		if err := target.validateBindingLocation(record); err != nil {
			return err
		}
		record.InstanceName, record.InstanceNamespace = target.recordAgent.Name, target.recordAgent.Namespace
		return target.putBinding(ctx, record, resourceVersion)
	}
	if err := k.validateBindingLocation(record); err != nil {
		return err
	}
	if k.recordAgent != nil && (k.recordAgent.Deleting || k.recordNamespaceDeleting) {
		return errors.New("Agent is deleting")
	}

	data, err := k.encodeBinding(ctx, record)
	if err != nil {
		return err
	}
	binding := record.Binding
	labels := map[string]string{
		"agenttools.cetirun.microsoft.com/id":            binding.ID,
		"agenttools.cetirun.microsoft.com/owner-tenant":  binding.Owner.TenantID,
		"agenttools.cetirun.microsoft.com/owner-object":  binding.Owner.ObjectID,
		"agenttools.cetirun.microsoft.com/instance-uid":  binding.InstanceUID,
		"agenttools.cetirun.microsoft.com/connection-id": binding.ConnectionID,
	}
	annotations := recordInstanceMetadata(labels, record.InstanceName, record.InstanceNamespace)
	return k.putToolSecret(ctx, "binding", binding.ID, labels, annotations, data, resourceVersion)
}

func (k *kubeClient) bindingRecords(ctx context.Context, selector string) ([]struct {
	record  *bindingRecord
	version string
}, error) {
	list, err := k.listToolSecrets(ctx, "binding", selector)
	if err != nil {
		return nil, err
	}
	result := make([]struct {
		record  *bindingRecord
		version string
	}, 0, len(list.Items))
	for _, secret := range list.Items {
		if secret.Metadata.Labels["agenttools.cetirun.microsoft.com/record-type"] != "binding" {
			continue
		}
		record, err := k.decodeBinding(ctx, secret)
		if err != nil {
			return nil, err
		}
		if err := k.validateBindingLocation(record); err != nil {
			return nil, err
		}
		result = append(result, struct {
			record  *bindingRecord
			version string
		}{record, secret.Metadata.ResourceVersion})
	}
	return result, nil
}

func (k *kubeClient) listBindings(ctx context.Context, owner Owner, instanceUID string, names ...string) ([]ConnectionBinding, error) {
	if k.agentNamespaces {
		target, err := k.agentRecordStore(ctx, instanceUID, names...)
		if err != nil {
			return nil, err
		}
		return target.listBindings(ctx, owner, instanceUID)
	}
	selector := "agenttools.cetirun.microsoft.com/owner-tenant=" + owner.TenantID + ",agenttools.cetirun.microsoft.com/owner-object=" + owner.ObjectID
	if instanceUID != "" {
		selector += ",agenttools.cetirun.microsoft.com/instance-uid=" + instanceUID
	}
	records, err := k.bindingRecords(ctx, selector)
	if err != nil {
		return nil, err
	}
	bindings := make([]ConnectionBinding, 0, len(records))
	for _, item := range records {
		if sameOwner(item.record.Binding.Owner, owner) {
			bindings = append(bindings, item.record.Binding)
		}
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].CreatedAt.Before(bindings[j].CreatedAt) })
	return bindings, nil
}

func sameOwner(left, right Owner) bool {
	return sameIdentityID(left.TenantID, right.TenantID) && sameIdentityID(left.ObjectID, right.ObjectID)
}

func activeBinding(binding ConnectionBinding, now time.Time) bool {
	return !binding.Revoked && binding.Enabled
}

type storedConnectionRecord struct {
	*connectionRecord
	OwnerTenantID string    `json:"ownerTenantId"`
	OwnerObjectID string    `json:"ownerObjectId"`
	AccountID     string    `json:"accountId"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type storedBindingRecord struct {
	*bindingRecord
	OwnerTenantID string    `json:"ownerTenantId"`
	OwnerObjectID string    `json:"ownerObjectId"`
	Revoked       bool      `json:"revoked"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}
