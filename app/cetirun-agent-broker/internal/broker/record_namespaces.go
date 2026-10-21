// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

const recordLabelPrefix = "agenttools.cetirun.microsoft.com/"

func agentRecordOwner(agent Agent) ownerReference {
	return ownerReference{APIVersion: "cetirun.microsoft.com/v1alpha1", Kind: "Agent", Name: agent.Name, UID: agent.UID}
}

func (k *kubeClient) agentRecordStore(ctx context.Context, uid string, names ...string) (*kubeClient, error) {
	var agent Agent
	var err error
	if len(names) == 1 && names[0] != "" {
		// A stored name is only a lookup hint. Recreated objects must not gain access.
		agent, err = k.getServiceAgent(ctx, names[0])
		if err == nil && agent.UID != uid {
			return nil, errNotFound
		}
	} else {
		agent, err = k.getServiceAgentByUID(ctx, uid)
	}
	if err != nil {
		return nil, err
	}
	return k.recordStoreForAgent(ctx, agent)
}

func (k *kubeClient) recordStoreForAgent(ctx context.Context, agent Agent) (*kubeClient, error) {
	if !dnsLabel(agent.Namespace) || agent.Namespace == k.namespace || !uuidPattern.MatchString(agent.UID) {
		return nil, errors.New("Agent record namespace is invalid")
	}
	var namespace struct {
		Metadata secretMetadata `json:"metadata"`
	}
	if _, err := k.request(ctx, http.MethodGet, "/api/v1/namespaces/"+url.PathEscape(agent.Namespace), nil, &namespace); err != nil {
		return nil, err
	}
	owned := false
	for _, owner := range namespace.Metadata.OwnerReferences {
		if owner == agentRecordOwner(agent) {
			owned = true
		}
	}
	if !owned {
		return nil, errors.New("Agent namespace ownership is invalid")
	}
	copy := *k
	copy.namespace, copy.agentNamespaces, copy.recordAgent = agent.Namespace, false, &agent
	copy.recordNamespaceDeleting = namespace.Metadata.DeletionTimestamp != nil
	return &copy, nil
}

func recordOwnedBy(secret secretDocument, agent Agent) bool {
	return len(secret.Metadata.OwnerReferences) == 1 && secret.Metadata.OwnerReferences[0] == agentRecordOwner(agent)
}

func (k *kubeClient) validateConnectionLocation(record *connectionRecord) error {
	if k.recordAgent == nil {
		return nil
	}
	c, a := record.Connection, k.recordAgent
	if connectionMode(c) != modeAgent || c.Identity == nil || c.Identity.InstanceUID != a.UID || !sameOwner(c.Owner, Owner{a.OwnerTenantID, a.OwnerObjectID}) {
		return errors.New("Connection does not belong to the Agent namespace")
	}
	return nil
}

func (k *kubeClient) validateBindingLocation(record *bindingRecord) error {
	if a := k.recordAgent; a != nil && (record.Binding.InstanceUID != a.UID || !sameOwner(record.Binding.Owner, Owner{a.OwnerTenantID, a.OwnerObjectID})) {
		return errors.New("Binding does not belong to the Agent namespace")
	}
	return nil
}

func (k *kubeClient) ownerAgentStores(ctx context.Context, owner Owner) ([]*kubeClient, error) {
	agents, err := k.listAgents(ctx)
	if err != nil {
		return nil, err
	}
	result := []*kubeClient{}
	for _, document := range agents.Items {
		a := agentFromDocument(document)
		if a.UnsupportedRuntime || a.Deleting || a.Namespace == "" || !sameOwner(owner, Owner{a.OwnerTenantID, a.OwnerObjectID}) {
			continue
		}
		target, err := k.recordStoreForAgent(ctx, a)
		if errors.Is(err, errNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		result = append(result, target)
	}
	return result, nil
}

func (k *kubeClient) listToolSecrets(ctx context.Context, kind, selector string) (secretList, error) {
	selector = recordLabelPrefix + "record-type=" + kind + func() string {
		if selector != "" {
			return "," + selector
		}
		return ""
	}()
	result := secretList{}
	continuation := ""
	for {
		query := url.Values{"labelSelector": {selector}, "limit": {"50"}}
		if continuation != "" {
			query.Set("continue", continuation)
		}
		var page secretList
		if _, err := k.request(ctx, http.MethodGet, "/api/v1/namespaces/"+url.PathEscape(k.namespace)+"/secrets?"+query.Encode(), nil, &page); err != nil {
			return secretList{}, err
		}
		if k.recordAgent != nil {
			for _, secret := range page.Items {
				if !recordOwnedBy(secret, *k.recordAgent) {
					return secretList{}, errors.New("Agent record ownership is invalid")
				}
			}
		}
		result.Items = append(result.Items, page.Items...)
		if page.Metadata.Continue == "" {
			return result, nil
		}
		if page.Metadata.Continue == continuation {
			return secretList{}, errors.New("Kubernetes pagination did not advance")
		}
		continuation = page.Metadata.Continue
	}
}

func (k *kubeClient) deleteToolSecret(ctx context.Context, secret secretDocument) error {
	if secret.Metadata.Namespace != k.namespace || secret.Metadata.UID == "" || secret.Metadata.ResourceVersion == "" {
		return errors.New("Secret deletion requires namespace, UID and resource version")
	}
	body := map[string]any{"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": map[string]string{"uid": secret.Metadata.UID, "resourceVersion": secret.Metadata.ResourceVersion}}
	_, err := k.request(ctx, http.MethodDelete, "/api/v1/namespaces/"+url.PathEscape(k.namespace)+"/secrets/"+url.PathEscape(secret.Metadata.Name), body, nil)
	if errors.Is(err, errNotFound) {
		return nil
	}
	return err
}

func (k *kubeClient) getConnectionForAgent(ctx context.Context, agent Agent, id string) (*connectionRecord, string, error) {
	target, err := k.recordStoreForAgent(ctx, agent)
	if err != nil {
		return nil, "", err
	}
	return target.getConnection(ctx, id)
}
func (k *kubeClient) getOwnedConnection(ctx context.Context, owner Owner, id string) (*connectionRecord, string, error) {
	stores, err := k.ownerAgentStores(ctx, owner)
	if err != nil {
		return nil, "", err
	}
	var found *connectionRecord
	var version string
	for _, target := range stores {
		record, v, err := target.getConnection(ctx, id)
		if errors.Is(err, errNotFound) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		if found != nil {
			return nil, "", errors.New("Connection has ambiguous ownership")
		}
		found, version = record, v
	}
	if found == nil {
		return nil, "", errNotFound
	}
	return found, version, nil
}
