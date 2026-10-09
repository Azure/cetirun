// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller_test

import (
	"context"
	"slices"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/controller"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxbinding"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload/testfixture"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func newKubernetesExecutionFixture(t *testing.T, typ string) *preparationFixture {
	t.Helper()
	ctx := context.Background()
	f := newPreparationFixture(t, typ)
	subject := api.EntraSubject{
		Kind: "User", TenantID: "11111111-1111-1111-1111-111111111111",
		ObjectID: "22222222-2222-2222-2222-222222222222",
	}

	box := &api.Sandbox{}
	if err := f.client.Get(ctx, client.ObjectKeyFromObject(f.fixture.Sandbox), box); err != nil {
		t.Fatal(err)
	}
	box.Spec.Storage.Home.AccessMode = corev1.ReadWriteOncePod
	if err := f.client.Update(ctx, box); err != nil {
		t.Fatal(err)
	}
	home := &corev1.PersistentVolumeClaim{}
	if err := f.client.Get(ctx, client.ObjectKeyFromObject(f.fixture.Input.Home), home); err != nil {
		t.Fatal(err)
	}
	home.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod}
	if err := f.client.Update(ctx, home); err != nil {
		t.Fatal(err)
	}

	agent := f.current(t)
	team := &api.Team{}
	policy := &api.AgentPolicy{}
	if err := f.client.Get(ctx, client.ObjectKeyFromObject(f.fixture.Team), team); err != nil {
		t.Fatal(err)
	}
	team.Spec.Membership.Subjects = []api.EntraSubject{subject}
	team.Status.ObservedGeneration = team.Generation
	team.Status.Conditions = []metav1.Condition{{
		Type: api.ConditionTeamAccepted, Status: metav1.ConditionTrue, ObservedGeneration: team.Generation,
	}}
	if err := f.client.Update(ctx, team); err != nil {
		t.Fatal(err)
	}
	if err := f.client.Get(ctx, client.ObjectKeyFromObject(f.fixture.Policy), policy); err != nil {
		t.Fatal(err)
	}
	policy.Status.ObservedGeneration = policy.Generation
	policy.Status.Conditions = []metav1.Condition{{
		Type: api.ConditionAgentPolicyAccepted, Status: metav1.ConditionTrue, ObservedGeneration: policy.Generation,
	}}
	if err := f.client.Status().Update(ctx, policy); err != nil {
		t.Fatal(err)
	}
	agent.Annotations[api.AnnotationOwnerTenantID] = subject.TenantID
	agent.Annotations[api.AnnotationOwnerObjectID] = subject.ObjectID
	agent.Spec.TeamAuthorization = api.TeamAuthorization{Subject: subject, AuthorizedAt: metav1.Now()}
	record, err := sandboxbinding.Snapshot(agent, team, sandbox.Resources{
		Sandboxes:     map[string]*api.Sandbox{box.Name: box},
		AgentPolicies: map[string]*api.AgentPolicy{policy.Name: policy},
	})
	if err != nil {
		t.Fatal(err)
	}
	if agent.Annotations == nil {
		agent.Annotations = map[string]string{}
	}
	agent.Annotations[sandboxbinding.AuthorizationAnnotation] = record
	if err := f.client.Update(ctx, agent); err != nil {
		t.Fatal(err)
	}

	registration := f.config.Registrations[box.Spec.Type]
	registration.Capabilities.HomeAccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod}
	f.config.Registrations[box.Spec.Type] = registration
	f.reconciler.AIGateway = controller.AIGatewayConfig{
		Enabled: true, URL: "http://ai-gateway.cetirun-system.svc.cluster.local:4000",
		MasterKeySecretName: "ai-gateway-master-key", MasterKeySecretKey: "master-key",
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

func reconcileUntil(t *testing.T, f *preparationFixture, condition func() bool) {
	t.Helper()
	for range 12 {
		if err := f.reconcile(); err != nil {
			t.Fatal(err)
		}
		if condition() {
			return
		}
	}
	agent := f.current(t)
	sts := maybeExecutionStatefulSet(t, f)
	t.Fatalf("reconciliation did not reach the expected state: phase=%q finalizers=%v conditions=%v statefulSet=%+v",
		agent.Status.Phase, agent.Finalizers, agent.Status.Conditions, sts)
}

func createReadyExecutionPod(t *testing.T, f *preparationFixture, node *corev1.Node) {
	t.Helper()
	ctx := context.Background()
	expected := f.expectation(t)
	pod := testfixture.Pod(expected)
	pod.UID = "running-pod-uid"
	pod.Spec.NodeName = node.Name
	for _, key := range []string{corev1.LabelTopologyRegion, corev1.LabelTopologyZone} {
		pod.Labels[key] = node.Labels[key]
	}
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if err := f.client.Create(ctx, pod); err != nil {
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
			// The fake client has no StatefulSet controller. Create the exact
			// ControllerRevision and status observation that a real controller
			// publishes before the Operator is allowed to start the workload.
			preparedRevision(t, f)

			reconcileUntil(t, f, func() bool {
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
			reconcileUntil(t, f, func() bool {
				agent := f.current(t)
				return agent.Status.Phase == api.PhaseReady &&
					apimeta.IsStatusConditionTrue(agent.Status.Conditions, api.ConditionSandboxReady) &&
					apimeta.IsStatusConditionTrue(agent.Status.Conditions, api.ConditionReady)
			})

			// Reconciliation after a controller restart must derive readiness from
			// Kubernetes state rather than an in-memory execution permit.
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

			// Losing the running node restores both holds and scales down, but the
			// Operator must leave the old Pod for Kubernetes/CSI to terminate.
			if err := f.client.Get(ctx, client.ObjectKeyFromObject(node), node); err != nil {
				t.Fatal(err)
			}
			node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}
			if err := f.client.Status().Update(ctx, node); err != nil {
				t.Fatal(err)
			}
			_ = f.reconcile() // preparation waits until the old Pod disappears
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
