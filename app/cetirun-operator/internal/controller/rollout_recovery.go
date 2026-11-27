// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/cetiadmin"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const rolloutRecoveryPod = "cetirun.microsoft.com/rollout-recovery-pod"

// OrderedReady can wait indefinitely for an unready Pod from a superseded
// revision. Only the ordinary, successfully applied Helm path may intervene.
func (r *AgentReconciler) recoverStalledRollout(ctx context.Context, agent *api.Agent, team *api.Team) error {
	if agent.UID == "" || agent.ResourceVersion == "" || agent.Spec.Suspended || !agent.DeletionTimestamp.IsZero() ||
		(agent.Spec.Agent.Type != api.AgentTypeHermes && agent.Spec.Agent.Type != api.AgentTypeOpenClaw) ||
		agent.Spec.PlannedMigration != nil || agent.Status.PlannedMigration != nil || cetiadmin.MaintenanceUnfinished(agent) ||
		sandboxRequested(agent, team) {
		return nil
	}
	reader := r.customReader()
	key := client.ObjectKey{Namespace: instanceNamespace(agent.Name), Name: instanceServiceName(agent.Name)}
	sts := &appsv1.StatefulSet{}
	if err := reader.Get(ctx, key, sts); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !stalledSingleReplica(sts) {
		return nil
	}
	if len(sts.OwnerReferences) != 0 || !attributableRuntimeObject(sts, agent, helmReleaseName(agent.Name)) {
		return fmt.Errorf("stalled rollout: cannot confirm ordinary Helm StatefulSet ownership")
	}
	pod := &corev1.Pod{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: key.Namespace, Name: key.Name + "-0"}, pod); err != nil {
		return client.IgnoreNotFound(err)
	}
	owner := metav1.GetControllerOf(pod)
	if owner == nil || owner.APIVersion != appsv1.SchemeGroupVersion.String() || owner.Kind != "StatefulSet" ||
		owner.Name != sts.Name || owner.UID != sts.UID {
		return fmt.Errorf("stalled rollout: cannot confirm ordinal-zero Pod ownership")
	}
	revision := pod.Labels[appsv1.StatefulSetRevisionLabel]
	if revision == "" || revision == sts.Status.UpdateRevision || pod.UID == "" || pod.ResourceVersion == "" ||
		pod.Spec.NodeName == "" || !pod.DeletionTimestamp.IsZero() || ptr.Deref(pod.Spec.TerminationGracePeriodSeconds, 30) <= 0 ||
		(pod.Status.Phase != corev1.PodRunning && pod.Status.Phase != corev1.PodPending) ||
		!slices.ContainsFunc(pod.Status.Conditions, func(c corev1.PodCondition) bool {
			return c.Type == corev1.PodReady && c.Status == corev1.ConditionFalse &&
				!c.LastTransitionTime.IsZero() && time.Since(c.LastTransitionTime.Time) >= time.Minute
		}) {
		return nil
	}
	namespace := &corev1.Namespace{}
	if err := reader.Get(ctx, client.ObjectKey{Name: key.Namespace}, namespace); err != nil {
		return err
	}
	if err := validateConnectionsMCPBrokerNamespaceOwner(namespace, agent); err != nil {
		return err
	}
	if namespace.UID == "" || !namespace.DeletionTimestamp.IsZero() || namespace.Labels[sandboxworkload.ProtectedNamespaceLabel] == "true" ||
		namespace.Annotations[cetiadmin.MaintenanceRequestID] != "" || namespace.Annotations[cetiadmin.MaintenanceAgentName] != "" ||
		namespace.Annotations[cetiadmin.MaintenanceAgentUID] != "" {
		return nil
	}
	node := &corev1.Node{}
	if err := reader.Get(ctx, client.ObjectKey{Name: pod.Spec.NodeName}, node); err != nil {
		return err
	}
	if node.UID == "" || !node.DeletionTimestamp.IsZero() || !sandboxNodeReady(node) || slices.ContainsFunc(node.Spec.Taints, func(t corev1.Taint) bool {
		return t.Key == corev1.TaintNodeNotReady || t.Key == corev1.TaintNodeUnreachable
	}) {
		return nil
	}
	if team != nil {
		current := &api.Team{}
		if err := reader.Get(ctx, client.ObjectKeyFromObject(team), current); err != nil {
			return err
		}
		if current.UID != team.UID || current.ResourceVersion != team.ResourceVersion || !current.DeletionTimestamp.IsZero() {
			return nil
		}
	}
	if r.CetiAdmin != nil {
		_, managed, err := r.CetiAdmin.BindingForAgent(ctx, agent)
		if err != nil || managed {
			return err
		}
	}
	current := &api.Agent{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(agent), current); err != nil {
		return err
	}
	if current.UID != agent.UID || current.ResourceVersion != agent.ResourceVersion {
		return nil
	}

	// Reserve one Pod UID per StatefulSet generation, outside the Pod template.
	// A retry (including after manager failover) can only act on that same UID.
	prefix := fmt.Sprintf("%d/", sts.Generation)
	claim := prefix + string(pod.UID)
	if previous := sts.Annotations[rolloutRecoveryPod]; previous != claim {
		if strings.HasPrefix(previous, prefix) {
			return nil
		}
		before := sts.DeepCopy()
		sts.Annotations[rolloutRecoveryPod] = claim
		return r.Patch(ctx, sts, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
	}
	latest := &appsv1.StatefulSet{}
	if err := reader.Get(ctx, key, latest); err != nil {
		return client.IgnoreNotFound(err)
	}
	if latest.UID != sts.UID || latest.ResourceVersion != sts.ResourceVersion {
		return nil
	}
	if err := r.Delete(ctx, pod, client.Preconditions{UID: &pod.UID, ResourceVersion: &pod.ResourceVersion}); err != nil {
		return client.IgnoreNotFound(err)
	}
	r.EventRecorder.Eventf(agent, corev1.EventTypeNormal, "StalledRolloutPodReplaced",
		"Requested graceful replacement of unready Pod %s (revision %s, desired %s)", pod.Name, revision, sts.Status.UpdateRevision)
	return nil
}

func stalledSingleReplica(sts *appsv1.StatefulSet) bool {
	rolling := sts.Spec.UpdateStrategy.RollingUpdate
	return sts.UID != "" && sts.ResourceVersion != "" && sts.DeletionTimestamp.IsZero() && sts.Generation > 0 &&
		sts.Spec.PodManagementPolicy == appsv1.OrderedReadyPodManagement && sts.Spec.UpdateStrategy.Type == appsv1.RollingUpdateStatefulSetStrategyType &&
		(rolling == nil || ptr.Deref(rolling.Partition, 0) == 0) && (sts.Spec.Ordinals == nil || sts.Spec.Ordinals.Start == 0) &&
		ptr.Deref(sts.Spec.Replicas, 0) == 1 && sts.Status.Replicas == 1 && sts.Status.ReadyReplicas == 0 && sts.Status.UpdatedReplicas == 0 &&
		sts.Status.ObservedGeneration == sts.Generation && sts.Status.CurrentRevision != "" && sts.Status.UpdateRevision != ""
}
