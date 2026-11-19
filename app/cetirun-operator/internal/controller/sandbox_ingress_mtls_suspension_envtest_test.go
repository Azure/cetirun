// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package controller

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/ingressmtls"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestInitialIngressMTLSBootstrapDeletionAPIServer(t *testing.T) {
	ctrl.SetLogger(logr.Discard())
	h := newNativeStartAPIHarnessWithRuntime(t, true, true, corev1.ReadWriteOncePod, "custom", api.SandboxKata, nil)
	h.installer = &nativeSandboxInstaller{kube: h.kube, chart: h.localHelm.chart}
	h.r.HelmClient = &sharedSandboxInstaller{nativeSandboxInstaller: h.installer}
	if slices.Contains(h.current(t).Finalizers, api.Finalizer) {
		t.Fatal("served creation unexpectedly seeded the ordinary finalizer")
	}
	if _, err := h.r.reconcilePolicy(h.ctx, h.current(t)); err != nil {
		t.Fatal(err)
	}
	if err := h.r.bootstrapReconciledSandbox(h.ctx, h.current(t)); err != nil {
		t.Fatal(err)
	}
	h.create(t, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: instanceNamespace(h.agent.Name)}})
	h.create(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform"}})
	h.create(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "portal-system"}})
	current := h.current(t)
	current.Spec.Agent.WebUI = false
	if err := h.admin.Update(h.ctx, current); err != nil {
		t.Fatal(err)
	}
	withCustomMTLS(t, false)(h.r, h.admin)
	updateContainerPlatformForTest(t, h.r, h.admin, func(document *SandboxPlatformConfiguration) {
		document.Platforms[api.SandboxKata].IngressMTLS.Bindings = []ingressmtls.Binding{}
	})
	h.reconcileOK(t)
	bootstrap, receipt, err := h.r.readSandboxBootstrap(h.ctx, h.current(t))
	if err != nil || !receipt.Reconciled {
		t.Fatal("served first-approval hold has no common bootstrap receipt:", err)
	}
	home := &corev1.PersistentVolumeClaim{}
	if err := h.admin.Get(h.ctx, client.ObjectKey{Namespace: receipt.Namespace, Name: h.r.SandboxPreparation.bootstrap.HomeClaimName}, home); err != nil {
		t.Fatal(err)
	}
	homeUID := home.UID
	for _, suspended := range []bool{true, false, true} {
		current = h.current(t)
		before := current.DeepCopy()
		current.Spec.Suspended = suspended
		if err := h.member.Patch(h.ctx, current, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			t.Fatal("member lifecycle request failed:", err)
		}
		h.reconcileOK(t)
		current = h.current(t)
		phase := api.PhasePending
		if suspended {
			phase = api.PhaseSuspended
		}
		if current.Status.Phase != phase || suspended && current.Status.ObservedGeneration != current.Generation {
			t.Fatal("served initial approval suspension/resume regressed")
		}
		if len(current.Finalizers) != 2 || !slices.Contains(current.Finalizers, sandbox.BootstrapFinalizer) ||
			!slices.Contains(current.Finalizers, sandbox.HoldFinalizer) {
			t.Fatalf("served enrollment has unexpected finalizers: %v", current.Finalizers)
		}
	}
	stoppedVersion := current.ResourceVersion
	h.reconcileOK(t)
	if h.current(t).ResourceVersion != stoppedVersion || h.installer.stages != 0 || len(h.gateway.generated) != 0 {
		t.Fatal("unchanged initial suspension rewrote status or submitted execution")
	}
	if err := h.admin.Get(h.ctx, client.ObjectKeyFromObject(home), home); err != nil || home.UID != homeUID {
		t.Fatal("first-approval lifecycle changed HOME:", err)
	}
	current = h.current(t)
	if err := h.member.Delete(h.ctx, current, client.Preconditions{UID: &current.UID, ResourceVersion: &current.ResourceVersion}); err != nil {
		t.Fatal("member conditional DELETE failed:", err)
	}
	h.r = restartEntraReconciler(h.r)
	result, err := h.reconcile()
	if err != nil || result.RequeueAfter != 5*time.Second || h.current(t).Status.Phase != api.PhaseDeleting {
		t.Fatalf("served already-terminating bootstrap-only Agent never entered cleanup: %+v %v", result, err)
	}
	namespace := &corev1.Namespace{}
	if err := h.admin.Get(h.ctx, client.ObjectKey{Name: receipt.Namespace}, namespace); err != nil ||
		namespace.DeletionTimestamp.IsZero() || len(namespace.Finalizers) != 0 {
		t.Fatal("served bootstrap cleanup did not release and delete the exact namespace:", err)
	}
	h.reconcileOK(t)
	if current = h.current(t); slices.Contains(current.Finalizers, api.Finalizer) ||
		!slices.Contains(current.Finalizers, sandbox.BootstrapFinalizer) || !slices.Contains(current.Finalizers, sandbox.HoldFinalizer) {
		t.Fatal("cleanup added the ordinary finalizer or released state before namespace absence")
	}
	h.finishNamespaceGC(t, namespace)
	h.reconcileOK(t)
	for _, object := range []client.Object{current, namespace, home, bootstrap} {
		if err := h.admin.Get(h.ctx, client.ObjectKeyFromObject(object), object); !apierrors.IsNotFound(err) {
			t.Fatalf("served bootstrap-only deletion retained %T: %v", object, err)
		}
	}
}

func TestInitialIngressMTLSSuspensionAPIServer(t *testing.T) {
	ctrl.SetLogger(logr.Discard())
	for _, scenario := range []string{"missing approval", "stale Portal client"} {
		t.Run(scenario, func(t *testing.T) {
			h := newNativeStartAPIHarnessWithRuntime(t, true, true, corev1.ReadWriteOnce, "custom", api.SandboxKata, nil)
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
			h.create(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "portal-system"}})
			current := h.current(t)
			current.Spec.Agent.WebUI = false
			if err := h.admin.Update(h.ctx, current); err != nil {
				t.Fatal(err)
			}
			withCustomMTLS(t, false)(h.r, h.admin)
			var approved []ingressmtls.Binding
			updateContainerPlatformForTest(t, h.r, h.admin, func(document *SandboxPlatformConfiguration) {
				registration := document.Platforms[api.SandboxKata].IngressMTLS
				approved = append([]ingressmtls.Binding(nil), registration.Bindings...)
				if scenario == "missing approval" {
					registration.Bindings = []ingressmtls.Binding{}
				} else {
					registration.Bindings[0].ChatClients = append([]ingressmtls.ChatClient(nil), approved[0].ChatClients...)
					registration.Bindings[0].ChatClients[0].Secret.UID = "wrong-portal-secret-uid"
				}
			})
			policyReconciler := &AgentPolicyReconciler{Client: h.kube, APIReader: h.kube, Source: h.r.SandboxPreparation.Configuration, NativeExecution: true}
			if _, err := policyReconciler.Reconcile(h.ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: "policy-example"}}); err != nil {
				t.Fatal(err)
			}
			for range 4 {
				if _, err := h.reconcile(); err != nil && !errors.Is(err, ingressmtls.ErrApprovalStale) {
					t.Fatal(err)
				}
			}
			current = h.current(t)
			namespace := &corev1.Namespace{}
			if err := h.admin.Get(h.ctx, client.ObjectKey{Name: instanceNamespace(current.Name)}, namespace); err != nil {
				t.Fatal(err)
			}
			home := &corev1.PersistentVolumeClaim{}
			homeKey := client.ObjectKey{Namespace: namespace.Name, Name: h.r.SandboxPreparation.bootstrap.HomeClaimName}
			if err := h.admin.Get(h.ctx, homeKey, home); err != nil {
				t.Fatal(err)
			}
			homeUID := home.UID
			if err := h.admin.Get(h.ctx, client.ObjectKey{Namespace: h.r.OperatorNamespace,
				Name: w.PodExpectationName(namespace.Name, namespace.UID)}, &corev1.ConfigMap{}); !apierrors.IsNotFound(err) {
				t.Fatal("served initial enrollment created an execution expectation:", err)
			}
			before := current.DeepCopy()
			current.Spec.Suspended = true
			if err := h.member.Patch(h.ctx, current, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
				t.Fatal("member suspension rejected:", err)
			}
			for range 2 {
				if _, err := h.reconcile(); err != nil && !errors.Is(err, ingressmtls.ErrApprovalStale) {
					t.Fatal(err)
				}
			}
			stopped := h.current(t)
			if stopped.Status.Phase != api.PhaseSuspended || stopped.Status.ObservedGeneration != stopped.Generation {
				t.Fatalf("served first-enrollment suspension was not acknowledged: %+v", stopped.Status)
			}
			reason := "IngressMTLSApprovalPending"
			if scenario == "stale Portal client" {
				reason = "IngressMTLSApprovalStale"
			}
			condition := apimeta.FindStatusCondition(stopped.Status.Conditions, api.ConditionIngressReady)
			if condition == nil || condition.Reason != reason || condition.ObservedGeneration != stopped.Generation {
				t.Fatalf("served initial suspension lost approval diagnosis: %+v", condition)
			}
			h.r = restartEntraReconciler(h.r)
			for range 3 {
				if _, err := h.reconcile(); err != nil && !errors.Is(err, ingressmtls.ErrApprovalStale) {
					t.Fatal(err)
				}
				if h.current(t).ResourceVersion != stopped.ResourceVersion {
					t.Fatal("served unchanged first-enrollment suspension changed resourceVersion")
				}
			}
			before = stopped.DeepCopy()
			stopped.Spec.Suspended = false
			if err := h.member.Patch(h.ctx, stopped, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
				t.Fatal("member conditional resume rejected:", err)
			}
			for range 2 {
				if _, err := h.reconcile(); err != nil && !errors.Is(err, ingressmtls.ErrApprovalStale) {
					t.Fatal(err)
				}
			}
			if h.current(t).Status.Phase != api.PhasePending || installer.stages != 0 {
				t.Fatal("served resume bypassed first-enrollment approval")
			}
			updateContainerPlatformForTest(t, h.r, h.admin, func(document *SandboxPlatformConfiguration) {
				document.Platforms[api.SandboxKata].IngressMTLS.Bindings = approved
			})
			for range 4 {
				h.reconcileOK(t)
			}
			workload := h.workload(t)
			workload.Status = appsv1.StatefulSetStatus{ObservedGeneration: workload.Generation, Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1}
			if err := h.admin.Status().Update(h.ctx, workload); err != nil {
				t.Fatal(err)
			}
			h.reconcileOK(t)
			if h.current(t).Status.Phase != api.PhaseReady {
				t.Fatal("served explicit enrollment did not recover the same lifetime")
			}
			if err := h.admin.Get(h.ctx, homeKey, home); err != nil || home.UID != homeUID {
				t.Fatal("served enrollment replaced HOME:", err)
			}
		})
	}
}

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
	if result, err := h.reconcile(); err != nil || result.RequeueAfter != 5*time.Second {
		t.Fatalf("served suspension did not retry unacknowledged shutdown: result=%+v, err=%v", result, err)
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
