// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func stalledWorkload(t *testing.T) (*deployedAgent, *appsv1.StatefulSet, *corev1.Pod) {
	t.Helper()
	f := newDeployedAgent(t)
	ctx := t.Context()
	f.verificationDue(t)
	agent := f.agent(t)
	key := client.ObjectKey{Namespace: instanceNamespace(agent.Name), Name: instanceServiceName(agent.Name)}
	sts := &appsv1.StatefulSet{}
	if err := f.r.Get(ctx, key, sts); err != nil {
		t.Fatal(err)
	}
	sts.UID = "statefulset-uid"
	sts.Labels = map[string]string{"app.kubernetes.io/managed-by": "Helm"}
	sts.Annotations = map[string]string{"meta.helm.sh/release-name": helmReleaseName(agent.Name), "meta.helm.sh/release-namespace": key.Namespace}
	sts.Spec.PodManagementPolicy = appsv1.OrderedReadyPodManagement
	sts.Spec.UpdateStrategy.Type = appsv1.RollingUpdateStatefulSetStrategyType
	if err := f.r.Update(ctx, sts); err != nil {
		t.Fatal(err)
	}
	sts.Status = appsv1.StatefulSetStatus{ObservedGeneration: sts.Generation, Replicas: 1, CurrentRevision: "first", UpdateRevision: "corrected"}
	if err := f.r.Status().Update(ctx, sts); err != nil {
		t.Fatal(err)
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "test-node", UID: "node-uid"},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: key.Name + "-0", Namespace: key.Namespace, UID: "failed-pod",
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(sts, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))},
		Labels:          map[string]string{appsv1.StatefulSetRevisionLabel: "failed"}},
		Spec: corev1.PodSpec{NodeName: node.Name}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{
			{Type: corev1.PodReady, Status: corev1.ConditionFalse, LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Hour))},
		}}}
	for _, object := range []client.Object{node, pod} {
		if err := f.r.Create(ctx, object); err != nil {
			t.Fatal(err)
		}
	}
	return f, sts, pod
}

func TestOrdinaryStalledRolloutRecovery(t *testing.T) {
	f, sts, pod := stalledWorkload(t)
	ctx := t.Context()
	reconcile := func() {
		t.Helper()
		if _, err := f.r.Reconcile(ctx, f.req); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		reconcile()
	}
	key := client.ObjectKeyFromObject(pod)
	if err := f.r.Get(ctx, key, &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("corrected release still has its superseded unready Pod: %v", err)
	}
	if f.agent(t).Status.Phase == api.PhaseReady || f.chart.installs != 1 {
		t.Fatal("recovery published Ready or reinstalled Helm before a replacement became ready")
	}
	// Even if a replacement unexpectedly reports the failed revision, the
	// same desired generation must not keep deleting distinct Pods.
	replacement := pod.DeepCopy()
	replacement.UID, replacement.ResourceVersion = "replacement", ""
	if err := f.r.Create(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		reconcile()
	}
	if err := f.r.Get(ctx, key, replacement); err != nil {
		t.Fatalf("replacement was not retained: %v", err)
	}
	current := &appsv1.StatefulSet{}
	if err := f.r.Get(ctx, client.ObjectKeyFromObject(sts), current); err != nil || current.UID != sts.UID || current.Generation != sts.Generation {
		t.Fatalf("workload was replaced or its spec changed: %v", err)
	}
}

func TestStalledRolloutLeavesOtherPodsAlone(t *testing.T) {
	for _, scenario := range []string{"target-revision", "ready", "starting", "partitioned", "on-delete", "unobserved-template", "unhealthy-node", "governed", "foreign-pod", "registry-outage"} {
		t.Run(scenario, func(t *testing.T) {
			f, sts, pod := stalledWorkload(t)
			ctx := t.Context()
			agent := f.agent(t)
			switch scenario {
			case "target-revision":
				pod.Labels[appsv1.StatefulSetRevisionLabel] = sts.Status.UpdateRevision
			case "ready":
				pod.Status.Conditions[0].Status = corev1.ConditionTrue
			case "starting":
				pod.Status.Conditions[0].LastTransitionTime = metav1.Now()
			case "partitioned":
				sts.Spec.UpdateStrategy.RollingUpdate = &appsv1.RollingUpdateStatefulSetStrategy{Partition: ptr.To[int32](1)}
			case "on-delete":
				sts.Spec.UpdateStrategy.Type = appsv1.OnDeleteStatefulSetStrategyType
			case "unobserved-template":
				sts.Status.ObservedGeneration--
			case "unhealthy-node":
				node := &corev1.Node{}
				if err := f.r.Get(ctx, client.ObjectKey{Name: pod.Spec.NodeName}, node); err != nil {
					t.Fatal(err)
				}
				node.Status.Conditions[0].Status = corev1.ConditionUnknown
				if err := f.r.Status().Update(ctx, node); err != nil {
					t.Fatal(err)
				}
			case "governed":
				agent.Spec.SandboxRef = "protected"
			case "foreign-pod":
				pod.OwnerReferences[0].UID = "different-workload"
			case "registry-outage":
				f.chart.err = &helm.ChartDownloadError{Version: agent.Spec.Helm.Version, Err: errors.New("registry unavailable")}
			}
			for _, obj := range []client.Object{pod, sts} {
				// The fake client exposes the status subresource independently.
				status := obj.DeepCopyObject().(client.Object)
				if err := f.r.Update(ctx, obj); err != nil {
					t.Fatal(err)
				}
				status.SetResourceVersion(obj.GetResourceVersion())
				if err := f.r.Status().Update(ctx, status); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				var err error
				if scenario == "registry-outage" {
					_, err = f.r.Reconcile(ctx, f.req)
				} else {
					err = f.r.recoverStalledRollout(ctx, agent, nil)
				}
				if (err != nil) != (scenario == "foreign-pod") {
					t.Fatalf("unexpected recovery result: %v", err)
				}
			}
			if err := f.r.Get(ctx, client.ObjectKeyFromObject(pod), &corev1.Pod{}); err != nil {
				t.Fatalf("ineligible Pod was not retained: %v", err)
			}
			if err := f.r.Get(ctx, client.ObjectKeyFromObject(sts), sts); err != nil || sts.Annotations[rolloutRecoveryPod] != "" {
				t.Fatalf("ineligible rollout claimed a recovery: %v", err)
			}
		})
	}
}

func TestStalledRolloutRechecksPodVersionOnDeletion(t *testing.T) {
	f, _, pod := stalledWorkload(t)
	ctx := t.Context()
	agent := f.agent(t)
	if err := f.r.recoverStalledRollout(ctx, agent, nil); err != nil {
		t.Fatal(err)
	}
	original := f.r.Client.(client.WithWatch)
	f.r.APIReader = original
	f.r.Client = interceptor.NewClient(original, interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.DeleteOption) error {
			var deletion client.DeleteOptions
			deletion.ApplyOptions(opts)
			if deletion.GracePeriodSeconds != nil || deletion.Preconditions == nil ||
				ptr.Deref(deletion.Preconditions.UID, "") != pod.UID || ptr.Deref(deletion.Preconditions.ResourceVersion, "") != pod.ResourceVersion {
				t.Fatal("deletion was not graceful and bound to the observed UID and resourceVersion")
			}
			pod.Status.Conditions[0].Status = corev1.ConditionTrue
			if err := c.Status().Update(ctx, pod); err != nil {
				t.Fatal(err)
			}
			return c.Delete(ctx, object, opts...)
		},
	})
	if err := f.r.recoverStalledRollout(ctx, agent, nil); !apierrors.IsConflict(err) {
		t.Fatalf("expected a conflict with the new Ready observation, got %v", err)
	}
	if err := f.r.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil || !sandboxPodReady(pod) {
		t.Fatalf("the now-ready Pod was not retained: %v", err)
	}
}

func TestWorkloadReadinessUsesCurrentRollout(t *testing.T) {
	for _, scenario := range []string{"rollout-pending", "rollout-complete", "read-failed", "missing", "client-only"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			if err := appsv1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "example-agent"}}
			current := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name: instanceServiceName(agent.Name), Namespace: instanceNamespace(agent.Name),
					UID: "current-workload", Generation: 2,
				},
				Spec: appsv1.StatefulSetSpec{Replicas: ptr.To[int32](1)},
				Status: appsv1.StatefulSetStatus{
					ObservedGeneration: 1, ReadyReplicas: 1, UpdatedReplicas: 1,
				},
			}
			cached := current.DeepCopy()
			cached.Generation = 1
			if scenario == "rollout-complete" {
				current.Status.ObservedGeneration = current.Generation
				cached.Status.ReadyReplicas = 0
			}
			kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(current).Build()
			if scenario == "missing" {
				if err := kube.Delete(ctx, current); err != nil {
					t.Fatal(err)
				}
			}
			cachedReads, currentReads := 0, 0
			cachedClient := interceptor.NewClient(kube, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
					if workload, ok := object.(*appsv1.StatefulSet); ok {
						cachedReads++
						cached.DeepCopyInto(workload)
						return nil
					}
					return c.Get(ctx, key, object, opts...)
				},
			})
			injected := errors.New("synthetic current workload read failure")
			reader := interceptor.NewClient(kube, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
					currentReads++
					if scenario == "read-failed" {
						return injected
					}
					return c.Get(ctx, key, object, opts...)
				},
			})
			r := &AgentReconciler{Client: cachedClient, APIReader: reader}
			if scenario == "client-only" {
				r.APIReader = nil
			}
			ready, result, err := r.observeWorkload(ctx, agent)
			wantReady := scenario == "rollout-complete" || scenario == "client-only"
			if ready != wantReady || (err != nil) != (scenario == "read-failed") {
				t.Fatalf("readiness used stale cache state: ready=%t want=%t result=%+v err=%v", ready, wantReady, result, err)
			}
			if scenario == "read-failed" && !errors.Is(err, injected) {
				t.Fatal("current read failure was hidden:", err)
			}
			if scenario != "client-only" && (cachedReads != 0 || currentReads != 1) {
				t.Fatalf("readiness did not use exactly one current read: cached=%d current=%d", cachedReads, currentReads)
			}
		})
	}
}
