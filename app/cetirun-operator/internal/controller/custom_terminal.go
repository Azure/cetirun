// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strconv"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/runtimebindings"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/terminal"
	"github.com/Azure/CetiRun/app/internal/terminalpolicy"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func customTerminalRequested(values map[string]any) bool {
	settings, _ := values["ttyd"].(map[string]any)
	return settings["enabled"] != false
}

func (r *AgentReconciler) ensureTerminalReadRoles(ctx context.Context, instance *api.Agent) error {
	if err := r.checkAgentGeneration(ctx, instance); err != nil {
		return err
	}
	role, reader := terminal.ReadRoles(instanceNamespace(instance.Name), instanceServiceName(instance.Name), instance.Name, string(instance.UID))
	for _, desired := range []client.Object{role, reader} {
		current := desired.DeepCopyObject().(client.Object)
		err := r.customReader().Get(ctx, client.ObjectKeyFromObject(desired), current)
		if apierrors.IsNotFound(err) {
			if err := r.Create(ctx, desired); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !hasAgentOwner(current, instance) || current.GetAnnotations()[customAgentUID] != string(instance.UID) {
			return fmt.Errorf("terminal read role has foreign ownership")
		}
		before := current.DeepCopyObject().(client.Object)
		switch value := current.(type) {
		case *rbacv1.Role:
			if equality.Semantic.DeepEqual(value.Rules, role.Rules) {
				continue
			}
			value.Rules = role.Rules
		case *rbacv1.ClusterRole:
			if equality.Semantic.DeepEqual(value.Rules, reader.Rules) {
				continue
			}
			value.Rules = reader.Rules
		}
		if err := r.Patch(ctx, current, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			return err
		}
	}
	return nil
}

// Include both the Service address and backend addresses because CNI policy
// implementations can observe traffic before or after Service translation.
func (r *AgentReconciler) terminalAPIEndpoints(ctx context.Context) ([]runtimebindings.APIServerEndpoint, error) {
	service := &corev1.Service{}
	if err := r.customReader().Get(ctx, types.NamespacedName{Namespace: "default", Name: "kubernetes"}, service); err != nil {
		return nil, err
	}
	endpoints := map[string]runtimebindings.APIServerEndpoint{}
	add := func(address string, port int32) bool {
		ip, err := netip.ParseAddr(address)
		if err == nil && !ip.IsUnspecified() && !ip.IsMulticast() && port > 0 && port <= 65535 {
			endpoints[ip.String()+":"+strconv.Itoa(int(port))] = runtimebindings.APIServerEndpoint{IP: ip.String(), Port: port}
			return true
		}
		return false
	}
	addresses := service.Spec.ClusterIPs
	if len(addresses) == 0 {
		addresses = []string{service.Spec.ClusterIP}
	}
	for _, address := range addresses {
		for _, port := range service.Spec.Ports {
			if port.Protocol == corev1.ProtocolTCP && (port.Name == "https" || port.Port == 443) {
				add(address, port.Port)
			}
		}
	}
	backendCount := 0
	slices := &discoveryv1.EndpointSliceList{}
	if err := r.customReader().List(ctx, slices, client.InNamespace("default"),
		client.MatchingLabels{discoveryv1.LabelServiceName: "kubernetes"}); err != nil {
		return nil, err
	}
	for _, slice := range slices.Items {
		if slice.AddressType != discoveryv1.AddressTypeIPv4 && slice.AddressType != discoveryv1.AddressTypeIPv6 {
			continue
		}
		for _, endpoint := range slice.Endpoints {
			if !ptr.Deref(endpoint.Conditions.Ready, true) {
				continue
			}
			for _, port := range slice.Ports {
				if port.Port == nil || ptr.Deref(port.Protocol, corev1.ProtocolTCP) != corev1.ProtocolTCP {
					continue
				}
				for _, address := range endpoint.Addresses {
					if add(address, *port.Port) {
						backendCount++
					}
				}
			}
		}
	}
	if backendCount == 0 {
		legacy := &corev1.Endpoints{}
		if err := r.customReader().Get(ctx, types.NamespacedName{Namespace: "default", Name: "kubernetes"}, legacy); err != nil {
			return nil, err
		}
		for _, subset := range legacy.Subsets {
			for _, address := range subset.Addresses {
				for _, port := range subset.Ports {
					if port.Protocol == corev1.ProtocolTCP {
						if add(address.IP, port.Port) {
							backendCount++
						}
					}
				}
			}
		}
	}
	if backendCount == 0 || len(endpoints) == 0 || len(endpoints) > 64 {
		return nil, fmt.Errorf("API server network endpoints are not ready")
	}
	keys := make([]string, 0, len(endpoints))
	for key := range endpoints {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]runtimebindings.APIServerEndpoint, 0, len(keys))
	for _, key := range keys {
		result = append(result, endpoints[key])
	}
	return result, nil
}

func (r *AgentReconciler) stopCustomTerminal(ctx context.Context, instance *api.Agent) error {
	deployment := &appsv1.Deployment{}
	key := types.NamespacedName{Namespace: instanceNamespace(instance.Name), Name: terminalpolicy.WorkloadName(instanceServiceName(instance.Name))}
	if err := r.customReader().Get(ctx, key, deployment); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if !hasCustomWorkloadOwner(deployment, instance) || deployment.Annotations[customAgentUID] != string(instance.UID) {
		return fmt.Errorf("Custom terminal owner is not confirmed")
	}
	if ptr.Deref(deployment.Spec.Replicas, 0) == 0 {
		return nil
	}
	before := deployment.DeepCopy()
	deployment.Spec.Replicas = ptr.To[int32](0)
	return r.Patch(ctx, deployment, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
}

func (r *AgentReconciler) customTerminalReady(ctx context.Context, instance *api.Agent) (bool, error) {
	deployment := &appsv1.Deployment{}
	key := types.NamespacedName{Namespace: instanceNamespace(instance.Name), Name: terminalpolicy.WorkloadName(instanceServiceName(instance.Name))}
	if err := r.customReader().Get(ctx, key, deployment); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if !hasAgentOwner(deployment, instance) || deployment.Annotations[customAgentUID] != string(instance.UID) {
		return false, fmt.Errorf("Custom terminal owner is not confirmed")
	}
	return deployment.DeletionTimestamp.IsZero() && ptr.Deref(deployment.Spec.Replicas, 0) == 1 &&
		deployment.Status.ObservedGeneration >= deployment.Generation &&
		deployment.Status.AvailableReplicas > 0, nil
}
