// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/controller"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload/testfixture"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// newKubernetesExecutionFixture prepares the native adapter's two independent
// start gates: a held Agent/expectation and a held StatefulSet. Policy status
// is accepted explicitly because these tests exercise execution, not policy
// admission or the Gateway control plane.
func newKubernetesExecutionFixture(t *testing.T, typ string) *preparationFixture {
	t.Helper()
	ctx := context.Background()
	f := newPreparationFixture(t, typ)

	box := &api.Sandbox{}
	if err := f.client.Get(ctx, client.ObjectKeyFromObject(f.fixture.Sandbox), box); err != nil {
		t.Fatalf("read Sandbox fixture: %v", err)
	}
	box.Spec.Storage.Home.AccessMode = corev1.ReadWriteOncePod
	if err := f.client.Update(ctx, box); err != nil {
		t.Fatal(err)
	}
	home := &corev1.PersistentVolumeClaim{}
	if err := f.client.Get(ctx, client.ObjectKeyFromObject(f.fixture.Input.Home), home); err != nil {
		t.Fatalf("read HOME fixture: %v", err)
	}
	home.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod}
	if err := f.client.Update(ctx, home); err != nil {
		t.Fatal(err)
	}

	team := &api.Team{}
	policy := &api.AgentPolicy{}
	if err := f.client.Get(ctx, client.ObjectKeyFromObject(f.fixture.Team), team); err != nil {
		t.Fatalf("read Team fixture: %v", err)
	}
	team.Status.ObservedGeneration = team.Generation
	team.Status.Conditions = []metav1.Condition{{
		Type: api.ConditionTeamAccepted, Status: metav1.ConditionTrue, ObservedGeneration: team.Generation,
	}}
	// Team does not opt into the fake client's status-subresource emulation in
	// the shared preparation fixture; an ordinary update is its faithful API
	// shape in this focused controller fixture.
	if err := f.client.Update(ctx, team); err != nil {
		t.Fatalf("accept Team fixture: %v", err)
	}
	if err := f.client.Get(ctx, client.ObjectKeyFromObject(f.fixture.Policy), policy); err != nil {
		t.Fatalf("read AgentPolicy fixture: %v", err)
	}
	policy.Status.ObservedGeneration = policy.Generation
	policy.Status.Conditions = []metav1.Condition{{
		Type: api.ConditionAgentPolicyAccepted, Status: metav1.ConditionTrue, ObservedGeneration: policy.Generation,
	}}
	if err := f.client.Status().Update(ctx, policy); err != nil {
		t.Fatalf("accept AgentPolicy fixture: %v", err)
	}

	registration := f.config.Registrations[box.Spec.Type]
	registration.Capabilities.HomeAccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod}
	f.config.Registrations[box.Spec.Type] = registration
	// Policy resolution requires a configured Gateway even when this Agent has
	// no model entitlement. The execution gate deliberately accepts that
	// no-entitlement state without issuing a provider credential.
	f.reconciler.AIGateway = controller.AIGatewayConfig{
		Enabled: true, URL: "http://ai-gateway.cetirun-system.svc.cluster.local:4000",
		MasterKeySecretName: "ai-gateway-master-key", MasterKeySecretKey: "master-key",
	}
	if err := f.client.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: "ai-gateway-master-key", Namespace: f.reconciler.OperatorNamespace,
	}, Data: map[string][]byte{"master-key": []byte("test-master-key")}}); err != nil {
		t.Fatal(err)
	}
	installKubernetesPrerequisites(t, f, &controller.KubernetesRWOPWriterExclusion{APIReader: f.client})
	return f
}

func executionStatefulSet(t *testing.T, f *preparationFixture) *appsv1.StatefulSet {
	t.Helper()
	sts := maybeExecutionStatefulSet(t, f)
	if sts == nil {
		t.Fatal("Sandbox StatefulSet does not exist")
	}
	return sts
}

func maybeExecutionStatefulSet(t *testing.T, f *preparationFixture) *appsv1.StatefulSet {
	t.Helper()
	list := &appsv1.StatefulSetList{}
	if err := f.client.List(context.Background(), list, client.InNamespace(f.fixture.Namespace.Name)); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) == 0 {
		return nil
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected one Sandbox StatefulSet, got %d", len(list.Items))
	}
	return list.Items[0].DeepCopy()
}

func reconcileExecutionUntil(t *testing.T, f *preparationFixture, condition func() bool) {
	t.Helper()
	for range 16 {
		if err := f.reconcile(); err != nil {
			t.Fatal(err)
		}
		if condition() {
			return
		}
	}
	agent := f.current(t)
	sts := maybeExecutionStatefulSet(t, f)
	t.Fatalf("reconciliation did not reach expected execution state: phase=%q finalizers=%v conditions=%v statefulSet=%+v",
		agent.Status.Phase, agent.Finalizers, agent.Status.Conditions, sts)
}

func createReadyExecutionPod(t *testing.T, f *preparationFixture, node *corev1.Node) {
	t.Helper()
	expected := f.expectation(t)
	pod := testfixture.Pod(expected)
	pod.UID = "running-pod-uid"
	pod.Spec.NodeName = node.Name
	for _, key := range []string{corev1.LabelTopologyRegion, corev1.LabelTopologyZone} {
		pod.Labels[key] = node.Labels[key]
	}
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if err := f.client.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
}

func TestKubernetesSandboxExecutionStartRestartAndNodeFailure(t *testing.T) {
	for _, typ := range []string{"openclaw", "hermes"} {
		t.Run(typ, func(t *testing.T) {
			ctx := context.Background()
			f := newKubernetesExecutionFixture(t, typ)
			if err := f.reconcile(); err != nil {
				t.Fatal(err)
			}
			// The fake client does not have a StatefulSet controller. Supply its
			// exact revision observation before the adapter can release either
			// start gate.
			preparedRevision(t, f)

			reconcileExecutionUntil(t, f, func() bool {
				sts := maybeExecutionStatefulSet(t, f)
				agent := f.current(t)
				return sts != nil && ptr.Deref(sts.Spec.Replicas, -1) == 1 &&
					!slices.Contains(agent.Finalizers, sandbox.HoldFinalizer) && !f.expectation(t).ExecutionHeld
			})

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "sandbox-node", UID: "sandbox-node-uid", Labels: map[string]string{
					corev1.LabelTopologyRegion: "example-region", corev1.LabelTopologyZone: "example-zone",
				}},
				Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{
					Type: corev1.NodeReady, Status: corev1.ConditionTrue,
				}}},
			}
			if err := f.client.Create(ctx, node); err != nil {
				t.Fatal(err)
			}
			createReadyExecutionPod(t, f, node)
			reconcileExecutionUntil(t, f, func() bool {
				agent := f.current(t)
				return agent.Status.Phase == api.PhaseReady &&
					apimeta.IsStatusConditionTrue(agent.Status.Conditions, api.ConditionSandboxReady) &&
					apimeta.IsStatusConditionTrue(agent.Status.Conditions, api.ConditionReady)
			})

			// A controller restart has no in-memory permit to restore. Readiness
			// must be derived from the held expectation, Pod and Node topology.
			previous := f.reconciler
			f.reconciler = &controller.AgentReconciler{
				Client: previous.Client, APIReader: previous.APIReader, Scheme: previous.Scheme,
				OperatorNamespace: previous.OperatorNamespace, OperatorSAName: previous.OperatorSAName,
				DomainSuffix: previous.DomainSuffix, DefaultModel: previous.DefaultModel,
				CetiRunHelmValues: previous.CetiRunHelmValues, SandboxPreparation: previous.SandboxPreparation,
				AIGateway: previous.AIGateway,
			}
			if err := f.reconcile(); err != nil {
				t.Fatal(err)
			}
			if f.current(t).Status.Phase != api.PhaseReady {
				t.Fatal("controller restart lost the running Sandbox observation")
			}

			// Losing the Node restores both holds and scales down. The old RWOP
			// writer is deliberately left to Kubernetes/CSI; force deletion would
			// violate the writer-exclusion contract.
			if err := f.client.Get(ctx, client.ObjectKeyFromObject(node), node); err != nil {
				t.Fatal(err)
			}
			node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}
			if err := f.client.Status().Update(ctx, node); err != nil {
				t.Fatal(err)
			}
			_ = f.reconcile() // preparation waits for the old writer Pod to disappear
			agent := f.current(t)
			if ptr.Deref(executionStatefulSet(t, f).Spec.Replicas, -1) != 0 ||
				!slices.Contains(agent.Finalizers, sandbox.HoldFinalizer) || !f.expectation(t).ExecutionHeld {
				t.Fatal("node failure did not restore the fail-closed execution hold")
			}
			pod := &corev1.Pod{}
			if err := f.client.Get(ctx, client.ObjectKey{
				Namespace: f.fixture.Namespace.Name, Name: f.expectation(t).Workload.Name + "-0",
			}, pod); err != nil {
				t.Fatal("node failure force-deleted the old writer Pod:", err)
			}
		})
	}
}

func TestKubernetesSandboxExecutionSuspendResumeWaitsForPreviousRWOPWriter(t *testing.T) {
	ctx := context.Background()
	f := newKubernetesExecutionFixture(t, "hermes")
	if err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	preparedRevision(t, f)
	reconcileExecutionUntil(t, f, func() bool {
		sts := maybeExecutionStatefulSet(t, f)
		agent := f.current(t)
		return sts != nil && ptr.Deref(sts.Spec.Replicas, -1) == 1 &&
			!slices.Contains(agent.Finalizers, sandbox.HoldFinalizer) && !f.expectation(t).ExecutionHeld
	})
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "resume-node", UID: "resume-node-uid", Labels: map[string]string{
		corev1.LabelTopologyRegion: "example-region", corev1.LabelTopologyZone: "example-zone",
	}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	if err := f.client.Create(ctx, node); err != nil {
		t.Fatal(err)
	}
	createReadyExecutionPod(t, f, node)
	reconcileExecutionUntil(t, f, func() bool { return f.current(t).Status.Phase == api.PhaseReady })

	// Gateway key blocking is its own policy executor. Model-less fixture
	// Agents have no key to block, so model the state after that executor has
	// already fenced external access and exercise the native execution path.
	agent := f.current(t)
	agent.Status.Phase = api.PhaseProvisioning
	agent.Status.AccessURL = ""
	apimeta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
		Type: api.ConditionReady, Status: metav1.ConditionFalse, Reason: "Suspending",
		ObservedGeneration: agent.Generation,
	})
	if err := f.client.Status().Update(ctx, agent); err != nil {
		t.Fatal(err)
	}
	agent = f.current(t)
	agent.Spec.Suspended = true
	agent.Generation++ // fake clients do not emulate the apiserver generation bump.
	if err := f.client.Update(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	if current := f.current(t); current.Status.Phase != api.PhaseSuspended ||
		!slices.Contains(current.Finalizers, sandbox.HoldFinalizer) || !f.expectation(t).ExecutionHeld ||
		ptr.Deref(executionStatefulSet(t, f).Spec.Replicas, -1) != 0 {
		t.Fatal("suspension did not restore the held, stopped Sandbox state")
	}

	agent = f.current(t)
	agent.Spec.Suspended = false
	agent.Generation++
	if err := f.client.Update(ctx, agent); err != nil {
		t.Fatal(err)
	}
	// Resume must not start another RWOP writer while Kubernetes still owns the
	// prior Pod. The execution adapter leaves the Pod intact and remains held.
	if err := f.reconcile(); err == nil {
		t.Fatal("resume accepted an existing RWOP writer Pod")
	}
	if current := f.current(t); !slices.Contains(current.Finalizers, sandbox.HoldFinalizer) ||
		!f.expectation(t).ExecutionHeld || ptr.Deref(executionStatefulSet(t, f).Spec.Replicas, -1) != 0 {
		t.Fatal("unsafe resume released the execution hold before writer exclusion")
	}

	pod := &corev1.Pod{}
	podKey := client.ObjectKey{Namespace: f.fixture.Namespace.Name, Name: f.expectation(t).Workload.Name + "-0"}
	if err := f.client.Get(ctx, podKey, pod); err != nil {
		t.Fatal(err)
	}
	if err := f.client.Delete(ctx, pod); err != nil {
		t.Fatal(err)
	}
	reconcileExecutionUntil(t, f, func() bool {
		sts := maybeExecutionStatefulSet(t, f)
		current := f.current(t)
		return sts != nil && ptr.Deref(sts.Spec.Replicas, -1) == 1 &&
			!slices.Contains(current.Finalizers, sandbox.HoldFinalizer) && !f.expectation(t).ExecutionHeld
	})
	createReadyExecutionPod(t, f, node)
	reconcileExecutionUntil(t, f, func() bool {
		return f.current(t).Status.Phase == api.PhaseReady &&
			apimeta.IsStatusConditionTrue(f.current(t).Status.Conditions, api.ConditionSandboxReady)
	})
}

func TestKubernetesSandboxExecutionHoldsPodAdmissionWhenAgentHoldPatchFails(t *testing.T) {
	f := newKubernetesExecutionFixture(t, "hermes")
	if err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	preparedRevision(t, f)
	reconcileExecutionUntil(t, f, func() bool {
		agent := f.current(t)
		return !slices.Contains(agent.Finalizers, sandbox.HoldFinalizer) &&
			!f.expectation(t).ExecutionHeld &&
			ptr.Deref(executionStatefulSet(t, f).Spec.Replicas, -1) == 1
	})

	ctx := context.Background()
	agent := f.current(t)
	agent.Spec.Suspended = true
	agent.Generation++
	if err := f.client.Update(ctx, agent); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("synthetic Agent hold patch failure")
	f.reconciler.Client = interceptor.NewClient(f.client.(client.WithWatch), interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, object client.Object, patch client.Patch, options ...client.PatchOption) error {
			if _, ok := object.(*api.Agent); ok {
				return injected
			}
			return c.Patch(ctx, object, patch, options...)
		},
	})
	if err := f.reconcile(); !errors.Is(err, injected) {
		t.Fatalf("Agent hold failure was hidden: %v", err)
	}
	if !f.expectation(t).ExecutionHeld {
		t.Fatal("Pod admission remained released after the Agent hold write failed")
	}
	if replicas := ptr.Deref(executionStatefulSet(t, f).Spec.Replicas, -1); replicas != 0 {
		t.Fatalf("workload remained active after hold failure: replicas=%d", replicas)
	}
}
