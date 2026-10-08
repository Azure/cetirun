// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// KubernetesRWOPWriterExclusion is the production boundary for the native
// Kubernetes adapter. HOME must use ReadWriteOncePod, the exact owned
// StatefulSet must be stopped, and no Pod may remain in the workload namespace.
// The operator never force-deletes a Pod, so an unreachable writer keeps the
// Sandbox stopped until Kubernetes and the CSI driver have released it.
type KubernetesRWOPWriterExclusion struct {
	APIReader client.Reader
}

var _ SandboxWriterExclusionSource = (*KubernetesRWOPWriterExclusion)(nil)

func (s *KubernetesRWOPWriterExclusion) CheckQuiesced(ctx context.Context, agent *api.Agent, prerequisites SandboxPrerequisites) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.APIReader == nil || agent == nil || agent.UID == "" || prerequisites.NamespaceUID == "" ||
		prerequisites.Home.Name == "" || prerequisites.Home.UID == "" {
		return fmt.Errorf("%w: exact Kubernetes Sandbox identities are required", sandbox.ErrObservationUnavailable)
	}
	namespaceName := instanceNamespace(agent.Name)
	namespace := &corev1.Namespace{}
	if err := s.APIReader.Get(ctx, types.NamespacedName{Name: namespaceName}, namespace); err != nil {
		if apierrors.IsNotFound(err) && !agent.DeletionTimestamp.IsZero() {
			// Kubernetes only finishes namespace deletion after all namespaced
			// objects are gone. This is therefore a stronger cleanup fence than
			// rediscovering the already deleted HOME, workload, and Pods by name.
			return ctx.Err()
		}
		return fmt.Errorf("inspect Sandbox namespace: %w", err)
	}
	if namespace.UID != prerequisites.NamespaceUID || !sandboxBuiltinOwned(namespace, agent) {
		return fmt.Errorf("%w: Sandbox namespace differs from the exact Agent-owned namespace", sandbox.ErrObservationUnavailable)
	}
	home := &corev1.PersistentVolumeClaim{}
	if err := s.APIReader.Get(ctx, types.NamespacedName{Namespace: namespaceName, Name: prerequisites.Home.Name}, home); err != nil {
		if apierrors.IsNotFound(err) && !agent.DeletionTimestamp.IsZero() && !namespace.DeletionTimestamp.IsZero() {
			return s.checkStoppedWorkloadAndPods(ctx, agent, namespaceName)
		}
		return fmt.Errorf("inspect Sandbox HOME: %w", err)
	}
	if home.UID != prerequisites.Home.UID || !home.DeletionTimestamp.IsZero() ||
		!equality.Semantic.DeepEqual(home.Spec.AccessModes, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod}) ||
		home.Annotations[sandboxworkload.AgentUIDAnnotation] != string(agent.UID) {
		return fmt.Errorf("%w: Sandbox HOME must be the exact Agent-owned ReadWriteOncePod claim", sandbox.ErrObservationUnavailable)
	}
	return s.checkStoppedWorkloadAndPods(ctx, agent, namespaceName)
}

func (s *KubernetesRWOPWriterExclusion) checkStoppedWorkloadAndPods(ctx context.Context, agent *api.Agent, namespace string) error {
	workload := &appsv1.StatefulSet{}
	err := s.APIReader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: instanceServiceName(agent.Name)}, workload)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("inspect Sandbox workload: %w", err)
	}
	if err == nil {
		if workload.UID == "" || !workload.DeletionTimestamp.IsZero() || ptr.Deref(workload.Spec.Replicas, -1) != 0 ||
			!sandboxBuiltinOwned(workload, agent) {
			return fmt.Errorf("%w: the exact owned Sandbox StatefulSet is not stopped", sandbox.ErrObservationUnavailable)
		}
	}
	pods := &corev1.PodList{}
	if err := s.APIReader.List(ctx, pods, client.InNamespace(namespace), client.Limit(1)); err != nil {
		return fmt.Errorf("inspect stopped Sandbox Pods: %w", err)
	}
	if len(pods.Items) != 0 {
		return fmt.Errorf("%w: Sandbox waits for the previous Pod to disappear without force deletion", sandbox.ErrObservationUnavailable)
	}
	return ctx.Err()
}
