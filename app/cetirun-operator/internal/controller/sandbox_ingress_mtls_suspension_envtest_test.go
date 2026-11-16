// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package controller

import (
	"context"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestReconciledSandboxStaleIngressMTLSSuspensionAPIServer(t *testing.T) {
	ctrl.SetLogger(logr.Discard())
	ctx := context.Background()
	h := newNativeStartAPIHarnessWithRuntime(t, true, true, corev1.ReadWriteOnce, "openclaw", api.SandboxKata, nil)
	installer := &nativeSandboxInstaller{kube: h.kube, chart: h.localHelm.chart}
	h.installer = installer
	h.r.HelmClient = &sharedSandboxInstaller{nativeSandboxInstaller: installer}
	if _, err := h.r.reconcilePolicy(h.ctx, h.current(t)); err != nil {
		t.Fatal(err)
	}
	if err := h.r.bootstrapReconciledSandbox(h.ctx, h.current(t)); err != nil {
		t.Fatal(err)
	}
	h.create(t, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: instanceNamespace(h.agent.Name)}})
	h.create(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}})
	withIngressMTLS(t, "openclaw", true)(h.r, h.admin)
	policy := &api.AgentPolicy{}
	team := &api.Team{}
	if err := h.admin.Get(h.ctx, client.ObjectKey{Name: h.agent.Spec.Team}, team); err != nil {
		t.Fatal(err)
	}
	if err := h.admin.Get(h.ctx, client.ObjectKey{Name: team.Spec.AgentPolicyRef}, policy); err != nil {
		t.Fatal(err)
	}
	policyReconciler := &AgentPolicyReconciler{Client: h.kube, APIReader: h.kube, Source: h.r.SandboxPreparation.Configuration, NativeExecution: true}
	if _, err := policyReconciler.Reconcile(h.ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(policy)}); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		h.reconcileOK(t)
	}
	workload := h.workload(t)
	workload.Status = appsv1.StatefulSetStatus{ObservedGeneration: workload.Generation, Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1}
	if err := h.admin.Status().Update(h.ctx, workload); err != nil {
		t.Fatal(err)
	}
	h.reconcileOK(t)
	if current := h.current(t); current.Status.Phase != api.PhaseReady {
		t.Fatalf("served fixture did not reach Ready: status=%+v", current.Status)
	}
	workloadUID := h.workload(t).UID
	expected := reconciledExpectationForTest(t, h.nativeSandboxFixture)
	home := &corev1.PersistentVolumeClaim{}
	homeKey := client.ObjectKey{Namespace: expected.Namespace.Name, Name: expected.Home.Name}
	if err := h.admin.Get(h.ctx, homeKey, home); err != nil {
		t.Fatal(err)
	}
	homeUID := home.UID

	secret := &corev1.Secret{}
	secretKey := client.ObjectKey{Namespace: instanceNamespace(h.agent.Name), Name: "agent-ingress-tls"}
	if err := h.admin.Get(h.ctx, secretKey, secret); err != nil {
		t.Fatal(err)
	}
	secret.Annotations = map[string]string{"example.com/test": "stale"}
	if err := h.admin.Update(h.ctx, secret); err != nil {
		t.Fatal(err)
	}
	if _, err := h.reconcile(); err == nil {
		t.Fatal("served stale mTLS approval did not stop the runtime")
	}
	workload = h.workload(t)
	if ptr.Deref(workload.Spec.Replicas, -1) != 0 {
		t.Fatal("served stale mTLS approval did not request zero replicas")
	}
	h.assertNoRoute(t)

	current := &api.Agent{}
	if err := h.member.Get(h.ctx, client.ObjectKey{Name: h.agent.Name}, current); err != nil {
		t.Fatal(err)
	}
	before := current.DeepCopy()
	current.Spec.Suspended = true
	if err := h.member.Patch(h.ctx, current, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		t.Fatal("request served suspension: ", err)
	}
	if result, err := h.reconcile(); err != nil || result.RequeueAfter != cleanupWaitRequeue {
		t.Fatal("served suspension did not wait for workload shutdown with bounded retry:", result, err)
	}
	pending := h.current(t)
	if pending.Status.Phase != api.PhasePending || pending.Status.ObservedGeneration == pending.Generation {
		t.Fatalf("served suspension acknowledged a lagging shutdown: status=%+v", pending.Status)
	}
	workload = h.workload(t)
	workload.Status = appsv1.StatefulSetStatus{ObservedGeneration: workload.Generation}
	if err := h.admin.Status().Update(h.ctx, workload); err != nil {
		t.Fatal(err)
	}
	if _, err := h.reconcile(); err == nil {
		t.Fatal("served suspension unexpectedly accepted the stale mTLS approval")
	}
	current = h.current(t)
	if current.Status.Phase != api.PhaseSuspended || current.Status.ObservedGeneration != current.Generation || current.Status.AccessURL != "" {
		t.Fatalf("served stale mTLS suspension did not acknowledge the current stopped generation: status=%+v", current.Status)
	}
	for _, kind := range []string{api.ConditionReady, api.ConditionPodReady} {
		condition := apimeta.FindStatusCondition(current.Status.Conditions, kind)
		if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != "Suspended" || condition.ObservedGeneration != current.Generation {
			t.Fatalf("served suspension did not record current %s: %+v", kind, condition)
		}
	}
	for _, kind := range []string{api.ConditionSandboxReady, api.ConditionIngressReady} {
		condition := apimeta.FindStatusCondition(current.Status.Conditions, kind)
		if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != "IngressMTLSApprovalStale" ||
			condition.ObservedGeneration != current.Generation {
			t.Fatalf("served suspension lost stale %s diagnosis: %+v", kind, condition)
		}
	}
	if actual := reconciledExpectationForTest(t, h.nativeSandboxFixture); actual.Home != expected.Home || h.workload(t).UID != workloadUID {
		t.Fatal("served stale mTLS suspension replaced the retained HOME or workload")
	}
	if err := h.admin.Get(h.ctx, homeKey, home); err != nil || home.UID != homeUID {
		t.Fatal("served stale mTLS suspension replaced or removed HOME: ", err)
	}
	if _, found, err := h.r.SandboxNativeCoordination.ReadNative(ctx, current.UID); err != nil || found {
		t.Fatal("served stale mTLS suspension retained its reservation:", err)
	}

	if err := h.admin.Get(h.ctx, secretKey, secret); err != nil {
		t.Fatal(err)
	}
	approveMTLSReplacementForRecovery(t, h.nativeSandboxFixture, secret)
	if err := h.member.Get(h.ctx, client.ObjectKey{Name: h.agent.Name}, current); err != nil {
		t.Fatal(err)
	}
	before = current.DeepCopy()
	current.Spec.Suspended = false
	if err := h.member.Patch(h.ctx, current, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		t.Fatal("request served resume: ", err)
	}
	// The first resume pass releases the old hold. Reconcile again until the
	// recovered release is applied, then acknowledge that current StatefulSet.
	for range 3 {
		h.reconcileOK(t)
	}
	workload = h.workload(t)
	workload.Status = appsv1.StatefulSetStatus{ObservedGeneration: workload.Generation, Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1}
	if err := h.admin.Status().Update(h.ctx, workload); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		h.reconcileOK(t)
	}
	recovered := h.current(t)
	if recovered.Status.Phase != api.PhaseReady || recovered.Status.ObservedGeneration != recovered.Generation ||
		h.workload(t).UID != workloadUID {
		t.Fatalf("served explicit mTLS reapproval did not recover the retained workload: status=%+v", recovered.Status)
	}
	if err := h.admin.Get(h.ctx, homeKey, home); err != nil || home.UID != homeUID {
		t.Fatal("served explicit mTLS reapproval replaced or removed HOME: ", err)
	}
}
