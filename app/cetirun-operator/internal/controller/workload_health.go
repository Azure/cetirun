// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"
	"time"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// observeWorkload records the health of a built-in Agent's StatefulSet in its
// in-memory status. While the rollout is incomplete or the StatefulSet cannot
// be read, PodReady and Ready are False and the result says when to look
// again, with the phase set to Provisioning. Once the
// rollout is complete, PodReady and Ready are True and the phase is Ready,
// with the Agent's access URL. The caller writes the status.
func (r *AgentReconciler) observeWorkload(ctx context.Context, instance *cetirunv1alpha1.Agent) (bool, ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("name", instance.Name)
	ns := instanceNamespace(instance.Name)
	stsName := instanceServiceName(instance.Name)
	sts := &appsv1.StatefulSet{}
	stsKey := types.NamespacedName{Namespace: ns, Name: stsName}
	if err := r.Get(ctx, stsKey, sts); err != nil {
		instance.Status.Phase = cetirunv1alpha1.PhaseProvisioning
		if errors.IsNotFound(err) {
			logger.Info("StatefulSet not found yet, requeueing", "name", stsName, "namespace", ns)
			setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionPodReady,
				metav1.ConditionFalse, "StatefulSetNotFound", fmt.Sprintf("StatefulSet %s not found", stsName))
			setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionReady,
				metav1.ConditionFalse, "PodNotReady", "StatefulSet not found")
			return false, ctrl.Result{RequeueAfter: 15 * time.Second}, nil
		}
		logger.Error(err, "Failed to get StatefulSet", "name", stsName, "namespace", ns)
		setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionPodReady,
			metav1.ConditionFalse, "StatefulSetGetFailed", fmt.Sprintf("Failed to get StatefulSet %s: %v", stsName, err))
		setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionReady,
			metav1.ConditionFalse, "PodNotReady", "Failed to get StatefulSet")
		return false, ctrl.Result{}, err
	}

	desiredReplicas := int32(1)
	if sts.Spec.Replicas != nil {
		desiredReplicas = *sts.Spec.Replicas
	}
	rolloutComplete := (sts.Status.UpdatedReplicas >= desiredReplicas || registryOnlyRollout(sts)) &&
		sts.Status.ReadyReplicas >= desiredReplicas &&
		(sts.Status.ObservedGeneration >= sts.Generation || registryOnlyRollout(sts))
	if !rolloutComplete {
		logger.Info("StatefulSet rollout not complete, requeueing",
			"statefulset", stsName,
			"readyReplicas", sts.Status.ReadyReplicas,
			"updatedReplicas", sts.Status.UpdatedReplicas,
			"desiredReplicas", desiredReplicas,
			"stsGeneration", sts.Generation,
			"stsObservedGeneration", sts.Status.ObservedGeneration)
		setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionPodReady,
			metav1.ConditionFalse, "PodNotReady",
			fmt.Sprintf("Waiting for rollout (ready: %d, updated: %d, desired: %d)",
				sts.Status.ReadyReplicas, sts.Status.UpdatedReplicas, desiredReplicas))
		setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionReady,
			metav1.ConditionFalse, "PodNotReady", "StatefulSet rollout not complete")
		instance.Status.Phase = cetirunv1alpha1.PhaseProvisioning
		return false, ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionPodReady,
		metav1.ConditionTrue, "PodReady", "StatefulSet pod is running and ready")
	instance.Status.Phase = cetirunv1alpha1.PhaseReady
	instance.Status.AccessURL = accessURLPathBased(instance.Name, r.DomainSuffix)
	instance.Status.Namespace = ns
	instance.Status.HelmReleaseName = helmReleaseName(instance.Name)
	setCondition(&instance.Status.Conditions, cetirunv1alpha1.ConditionReady,
		metav1.ConditionTrue, "InstanceReady", "Instance is fully operational")
	return true, ctrl.Result{}, nil
}
