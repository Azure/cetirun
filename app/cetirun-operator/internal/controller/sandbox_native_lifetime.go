// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"
	"slices"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *AgentReconciler) recordNativePodLifetime(ctx context.Context, state *corev1.ConfigMap, expected *w.Expectation, pod *corev1.Pod) error {
	owner := metav1.OwnerReference{
		APIVersion: "apps/v1", Kind: "StatefulSet", Name: expected.Workload.Name, UID: expected.Workload.UID,
		Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true),
	}
	if !expected.SinglePod || pod.UID == "" || pod.Name != expected.Workload.Name+"-0" ||
		pod.Namespace != expected.Namespace.Name || !equality.Semantic.DeepEqual(pod.OwnerReferences, []metav1.OwnerReference{owner}) ||
		(expected.PodUID != "" && expected.PodUID != pod.UID) {
		return fmt.Errorf("native Sandbox Pod lifetime differs from the recorded workload")
	}
	if expected.PodUID == "" {
		if !slices.Contains(pod.Finalizers, w.NativePodFinalizer) {
			return fmt.Errorf("native Sandbox Pod has no initial lifetime protection")
		}
		expected.PodUID = pod.UID
		if err := r.saveNativeExpectation(ctx, state, expected); err != nil {
			return fmt.Errorf("record initial native Pod lifetime: %w", err)
		}
	}
	if !slices.Contains(pod.Finalizers, w.NativePodFinalizer) {
		return nil
	}
	base := pod.DeepCopy()
	pod.Finalizers = slices.DeleteFunc(slices.Clone(pod.Finalizers), func(value string) bool {
		return value == w.NativePodFinalizer
	})
	if err := r.Patch(ctx, pod, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("release recorded native Pod lifetime: %w", err)
	}
	return nil
}

// Cleanup consumes the initial allowance even if deletion or a source failure
// races the first observation. No current policy or runtime readiness is needed.
func (r *AgentReconciler) finishNativePodLifetime(ctx context.Context, agent *api.Agent) error {
	pod := &corev1.Pod{}
	key := client.ObjectKey{Namespace: instanceNamespace(agent.Name), Name: instanceServiceName(agent.Name) + "-0"}
	if err := r.customReader().Get(ctx, key, pod); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !slices.Contains(pod.Finalizers, w.NativePodFinalizer) {
		return nil
	}
	state, expected, err := r.readNativeSandboxExpectation(ctx, agent, true)
	if err != nil {
		return err
	}
	return r.recordNativePodLifetime(ctx, state, expected, pod)
}
