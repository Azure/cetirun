// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestReconciledSandboxStaleIngressMTLSAcknowledgesConfirmedSuspension(t *testing.T) {
	for _, kind := range []string{"openclaw", "hermes"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, kind, true, false,
				withReconciledCoordination(t), withIngressMTLS(t, kind))
			workload := f.workload(t)
			workload.Status = appsv1.StatefulSetStatus{
				ObservedGeneration: workload.Generation, Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1,
			}
			if err := f.kube.Status().Update(ctx, workload); err != nil {
				t.Fatal(err)
			}
			f.reconcileOK(t)
			before := f.current(t)
			if before.Status.Phase != api.PhaseReady {
				t.Fatalf("fixture did not reach Ready: phase=%s", before.Status.Phase)
			}
			workloadUID := f.workload(t).UID
			expected := reconciledExpectationForTest(t, f)
			home := &corev1.PersistentVolumeClaim{}
			homeKey := client.ObjectKey{Namespace: expected.Namespace.Name, Name: expected.Home.Name}
			if err := f.kube.Get(ctx, homeKey, home); err != nil {
				t.Fatal(err)
			}
			homeUID := home.UID

			// Mutating metadata changes the strictly pinned server Secret resourceVersion
			// without changing its identity or certificate data.
			server := &corev1.Secret{}
			serverKey := client.ObjectKey{Namespace: expected.Namespace.Name, Name: "agent-ingress-tls"}
			if err := f.kube.Get(ctx, serverKey, server); err != nil {
				t.Fatal(err)
			}
			server.Annotations = map[string]string{"example.com/test": "stale"}
			if err := f.kube.Update(ctx, server); err != nil {
				t.Fatal(err)
			}
			if _, err := f.reconcile(); err == nil {
				t.Fatal("stale server approval did not hold the running Agent")
			}
			if ptr.Deref(f.workload(t).Spec.Replicas, -1) != 0 {
				t.Fatal("stale server approval did not stop the workload")
			}
			if err := f.kube.Get(ctx, client.ObjectKey{Namespace: expected.Namespace.Name, Name: virtualServiceName(before.Name)}, &istiov1beta1.VirtualService{}); !apierrors.IsNotFound(err) {
				t.Fatal("stale server approval retained the native route:", err)
			}

			// Complete the physical shutdown that a real StatefulSet controller reports.
			workload = f.workload(t)
			workload.Status = appsv1.StatefulSetStatus{ObservedGeneration: workload.Generation}
			if err := f.kube.Status().Update(ctx, workload); err != nil {
				t.Fatal(err)
			}
			if _, err := f.reconcile(); err == nil {
				t.Fatal("stale mTLS approval unexpectedly recovered")
			}
			if _, found, err := f.r.SandboxNativeCoordination.ReadNative(ctx, before.UID); err != nil || found {
				t.Fatal("stale mTLS hold did not release its native reservation:", err)
			}

			current := f.current(t)
			current.Spec.Suspended, current.Generation = true, current.Generation+1
			if err := f.kube.Update(ctx, current); err != nil {
				t.Fatal(err)
			}
			// A reservation can already have been released by the automatic mTLS
			// hold. Suspension still has to re-observe that no Pod is left before
			// it acknowledges this later generation.
			lingering := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Namespace: expected.Namespace.Name, Name: "stale-mtls-lingering", UID: "stale-mtls-lingering",
			}}
			if err := f.kube.Create(ctx, lingering); err != nil {
				t.Fatal(err)
			}
			if _, err := f.reconcile(); err == nil {
				t.Fatal("suspension accepted a lingering Pod after its reservation was released")
			}
			pending := f.current(t)
			if pending.Status.Phase != api.PhasePending || pending.Status.ObservedGeneration == pending.Generation {
				t.Fatalf("unobserved shutdown acknowledged stale-mTLS suspension: status=%+v", pending.Status)
			}
			if err := f.kube.Delete(ctx, lingering); err != nil {
				t.Fatal(err)
			}
			route := &istiov1beta1.VirtualService{ObjectMeta: metav1.ObjectMeta{
				Namespace: expected.Namespace.Name, Name: virtualServiceName(before.Name), Finalizers: []string{"example.com/hold"},
				OwnerReferences: []metav1.OwnerReference{w.AgentOwner(before.Name, before.UID)},
			}}
			if err := f.kube.Create(ctx, route); err != nil {
				t.Fatal(err)
			}
			if _, err := f.reconcile(); err == nil {
				t.Fatal("suspension accepted route deletion before its finalizer completed")
			}
			pending = f.current(t)
			if pending.Status.Phase != api.PhasePending || pending.Status.ObservedGeneration == pending.Generation {
				t.Fatalf("route-finalized shutdown acknowledged stale-mTLS suspension: status=%+v", pending.Status)
			}
			if err := f.kube.Get(ctx, client.ObjectKeyFromObject(route), route); err != nil {
				t.Fatal("suspension did not request native route removal:", err)
			}
			route.Finalizers = nil
			if err := f.kube.Update(ctx, route); err != nil {
				t.Fatal("finish native route finalization: ", err)
			}

			// A concurrent resume must make the status patch fail rather than let
			// this stale suspended reconcile overwrite the newer intent.
			originalClient := f.r.Client
			resumed := false
			f.r.Client = interceptor.NewClient(originalClient.(client.WithWatch), interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, object client.Object, patch client.Patch, options ...client.SubResourcePatchOption) error {
					if !resumed && subresource == "status" {
						if agent, ok := object.(*api.Agent); ok && agent.Name == before.Name &&
							agent.Spec.Suspended && agent.Status.Phase == api.PhaseSuspended {
							latest := &api.Agent{}
							if err := c.Get(ctx, client.ObjectKeyFromObject(agent), latest); err != nil {
								return err
							}
							latest.Spec.Suspended = false
							latest.Generation++
							if err := c.Update(ctx, latest); err != nil {
								return err
							}
							resumed = true
						}
					}
					return c.SubResource(subresource).Patch(ctx, object, patch, options...)
				},
			})
			if _, err := f.reconcile(); err == nil || !resumed {
				t.Fatal("stale suspension status patch did not encounter the concurrent resume:", err)
			}
			if resumedAgent := f.current(t); resumedAgent.Spec.Suspended || resumedAgent.Status.Phase == api.PhaseSuspended ||
				resumedAgent.Status.ObservedGeneration == resumedAgent.Generation {
				t.Fatalf("stale suspension acknowledgement overwrote the concurrent resume: status=%+v", resumedAgent.Status)
			}
			f.r.Client = originalClient
			current = f.current(t)
			current.Spec.Suspended, current.Generation = true, current.Generation+1
			if err := f.kube.Update(ctx, current); err != nil {
				t.Fatal(err)
			}
			if _, err := f.reconcile(); err == nil {
				t.Fatal("suspension unexpectedly ignored the stale mTLS approval")
			}
			current = f.current(t)
			if current.Status.Phase != api.PhaseSuspended || current.Status.AccessURL != "" ||
				current.Status.ObservedGeneration != current.Generation || current.Status.Namespace != before.Status.Namespace ||
				reconciledExpectationForTest(t, f).Home != expected.Home || ptr.Deref(f.workload(t).Spec.Replicas, -1) != 0 {
				t.Fatalf("confirmed stale-mTLS suspension did not retain the stopped lifetime: status=%+v", current.Status)
			}
			if err := f.kube.Get(ctx, homeKey, home); err != nil || home.UID != homeUID {
				t.Fatal("confirmed stale-mTLS suspension replaced or removed HOME:", err)
			}
			for _, want := range []struct{ kind, reason string }{
				{api.ConditionReady, "Suspended"}, {api.ConditionPodReady, "Suspended"},
				{api.ConditionSandboxReady, "IngressMTLSApprovalStale"}, {api.ConditionIngressReady, "IngressMTLSApprovalStale"},
			} {
				condition := apimeta.FindStatusCondition(current.Status.Conditions, want.kind)
				if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != want.reason ||
					condition.ObservedGeneration != current.Generation {
					t.Fatalf("suspension did not retain current %s status: %+v", want.kind, condition)
				}
			}
			if current.Status.AIGateway == nil || !current.Status.AIGateway.Blocked ||
				!allTestGatewayKeysBlocked(f) {
				t.Fatalf("confirmed suspension did not revoke Gateway access: status=%+v keys=%+v", current.Status.AIGateway, f.gateway.keys)
			}

			f.r = restartEntraReconciler(f.r)
			settledVersion := current.ResourceVersion
			for range 2 {
				if _, err := f.reconcile(); err == nil {
					t.Fatal("steady stale suspension unexpectedly recovered")
				}
				if got := f.current(t); got.Status.Phase != api.PhaseSuspended || got.Status.ObservedGeneration != got.Generation ||
					got.ResourceVersion != settledVersion {
					t.Fatalf("restart rewrote confirmed suspension: status=%+v", got.Status)
				}
			}

			// A resume remains held until the platform explicitly records the new
			// resourceVersion. Reapproval must recover the same lifetime rather than
			// silently adopting the changed Secret.
			current = f.current(t)
			current.Spec.Suspended, current.Generation = false, current.Generation+1
			if err := f.kube.Update(ctx, current); err != nil {
				t.Fatal(err)
			}
			if _, err := f.reconcile(); err == nil {
				t.Fatal("resume accepted the stale mTLS approval")
			}
			if got := f.current(t); got.Status.Phase == api.PhaseReady || ptr.Deref(f.workload(t).Spec.Replicas, -1) != 0 {
				t.Fatalf("stale mTLS resume restarted its workload: status=%+v replicas=%d", got.Status, ptr.Deref(f.workload(t).Spec.Replicas, -1))
			}
			if err := f.kube.Get(ctx, client.ObjectKey{Namespace: expected.Namespace.Name, Name: virtualServiceName(before.Name)}, &istiov1beta1.VirtualService{}); !apierrors.IsNotFound(err) {
				t.Fatal("stale mTLS resume restored the native route:", err)
			}
			pods := &corev1.PodList{}
			if err := f.kube.List(ctx, pods, client.InNamespace(expected.Namespace.Name)); err != nil || len(pods.Items) != 0 {
				t.Fatalf("stale mTLS resume retained or started Pods: pods=%d err=%v", len(pods.Items), err)
			}
			if err := f.kube.Get(ctx, serverKey, server); err != nil {
				t.Fatal(err)
			}
			approveMTLSReplacementForRecovery(t, f, server)
			f.reconcileOK(t)
			workload = f.workload(t)
			workload.Status = appsv1.StatefulSetStatus{
				ObservedGeneration: workload.Generation, Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1,
			}
			if err := f.kube.Status().Update(ctx, workload); err != nil {
				t.Fatal(err)
			}
			f.reconcileOK(t)
			recovered := f.current(t)
			if recovered.Status.Phase != api.PhaseReady || recovered.Status.ObservedGeneration != recovered.Generation ||
				reconciledExpectationForTest(t, f).Home != expected.Home || f.workload(t).UID != workloadUID {
				t.Fatalf("explicit mTLS reapproval did not recover the retained lifetime: status=%+v", recovered.Status)
			}
			if err := f.kube.Get(ctx, homeKey, home); err != nil || home.UID != homeUID {
				t.Fatal("explicit mTLS reapproval replaced or removed HOME:", err)
			}
		})
	}
}

func allTestGatewayKeysBlocked(f *nativeSandboxFixture) bool {
	if len(f.gateway.keys) == 0 {
		return false
	}
	for _, key := range f.gateway.keys {
		if !key.Blocked {
			return false
		}
	}
	return true
}
