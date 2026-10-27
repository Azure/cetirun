// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package kube

import (
	"context"
	"fmt"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetInstanceEvents pins observations to an Agent incarnation, including when
// an older Agent with the same name still has events in the cluster.
func (c *Client) GetInstanceEvents(ctx context.Context, instance *api.Agent) ([]corev1.Event, error) {
	if instance.UID == "" {
		return nil, fmt.Errorf("Agent UID is required for diagnostics")
	}
	list := &corev1.EventList{}
	if err := c.client.List(ctx, list, client.MatchingFields{
		"involvedObject.uid":  string(instance.UID),
		"involvedObject.kind": "Agent",
	}); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// GetInstanceDiagnosticNamespace never falls back to a cluster-wide query.
// Legacy namespaces without owner references retain the established naming
// convention; namespaces explicitly owned by a different incarnation fail.
func (c *Client) GetInstanceDiagnosticNamespace(ctx context.Context, instance *api.Agent) (string, error) {
	ns := instance.Status.Namespace
	if ns == "" {
		return "", nil
	}
	if ns != "cetirun-"+instance.Name {
		return "", fmt.Errorf("Agent namespace does not match its resource name")
	}
	object := &corev1.Namespace{}
	if err := c.client.Get(ctx, client.ObjectKey{Name: ns}, object); err != nil {
		return "", err
	}
	if len(object.OwnerReferences) > 0 {
		for _, owner := range object.OwnerReferences {
			if owner.APIVersion == api.GroupVersion.String() && owner.Kind == "Agent" && owner.Name == instance.Name && owner.UID == instance.UID && instance.UID != "" {
				return ns, nil
			}
		}
		return "", fmt.Errorf("namespace belongs to another resource")
	}
	return ns, nil
}
