// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/internal/chataccess"
	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestReconciledCustomLifecycleUsesCommonRuntimeAndPrivateChatRoute(t *testing.T) {
	ctx := context.Background()
	caller := chataccess.Caller{Namespace: "cetirun-portal", PodLabels: map[string]string{"app": "portal"}}
	f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, "custom", true, false,
		func(r *AgentReconciler, kube client.Client) {
			policy := &api.AgentPolicy{}
			if err := kube.Get(ctx, client.ObjectKey{Name: "policy-example"}, policy); err != nil {
				t.Fatal(err)
			}
			policy.Spec.Harnesses.Allowed = append(policy.Spec.Harnesses.Allowed, api.AgentTypeCustom)
			if err := kube.Update(ctx, policy); err != nil {
				t.Fatal(err)
			}
			agent := &api.Agent{}
			if err := kube.Get(ctx, client.ObjectKey{Name: "example"}, agent); err != nil {
				t.Fatal(err)
			}
			r.CustomApprovedImages = []string{*agent.Spec.Agent.Image}
			r.ChatAccess = chataccess.Config{Enabled: true, Callers: []chataccess.Caller{caller}}
		},
	)
	// The first common Custom pass records the Gateway projection. The next
	// pass consumes that projection and submits the typed workload bundle.
	f.reconcileOK(t)
	setReconciledCustomWorkloadReady(t, f)
	f.reconcileOK(t)
	assertReconciledCustomReady(t, f, caller)
	assertNoLegacyCustomCompletion(t, f)
	// A Ready Custom Agent must keep its approved execution identity on a
	// subsequent ordinary reconciliation. This catches divergence between the
	// policy-time compiler inputs and the reconciled execution plan.
	beforeWorkload := f.workload(t)
	beforeKeyID, beforeStages := f.current(t).Status.AIGateway.KeyID, f.installer.stages
	f.reconcileOK(t)
	afterWorkload := f.workload(t)
	after := f.current(t)
	if after.Status.Phase != api.PhaseReady || afterWorkload.UID != beforeWorkload.UID ||
		after.Status.AIGateway.KeyID != beforeKeyID || f.installer.stages != beforeStages {
		t.Fatalf("repeated Ready Custom reconcile changed execution identity: workload=%s/%s key=%q/%q stages=%d/%d phase=%s",
			beforeWorkload.UID, afterWorkload.UID, beforeKeyID, after.Status.AIGateway.KeyID,
			beforeStages, f.installer.stages, after.Status.Phase)
	}
	if after.Status.Policy == nil || after.Status.Sandbox == nil ||
		after.Status.Policy.Enforcement.Sandbox.DesiredRevision != after.Status.Sandbox.AppliedRevision ||
		!isConditionTrue(after.Status.Conditions, api.ConditionPolicyApplied) {
		t.Fatalf("Ready Custom policy was not current: policy=%#v sandbox=%#v conditions=%v", after.Status.Policy, after.Status.Sandbox, after.Status.Conditions)
	}
	originalChat := f.r.ChatAccess
	f.r.ChatAccess = chataccess.Config{Enabled: true, Callers: []chataccess.Caller{{
		Namespace: "cetirun-portal", PodLabels: map[string]string{"app": "different-portal"},
	}}}
	changed, err := f.r.resolvePolicy(ctx, after)
	if err != nil || !policyReprojectionMustHold(after, changed) {
		t.Fatalf("changed Custom Chat caller did not require a policy hold: resolution=%#v err=%v", changed, err)
	}
	f.r.ChatAccess = originalChat

	agent := f.current(t)
	agent.Spec.Suspended = true
	agent.Generation++
	if err := f.kube.Update(ctx, agent); err != nil {
		t.Fatal(err)
	}
	f.reconcileOK(t)
	setReconciledCustomWorkloadStopped(t, f)
	f.reconcileOK(t)

	current := f.current(t)
	if current.Status.Phase != api.PhaseSuspended {
		t.Fatalf("suspended common Custom phase = %s", current.Status.Phase)
	}
	workload := f.workload(t)
	if workload.Spec.Replicas == nil || *workload.Spec.Replicas != 0 {
		t.Fatalf("suspension left common Custom workload running: %#v", workload.Spec.Replicas)
	}
	chatRoute := &unstructured.Unstructured{}
	chatRoute.SetGroupVersionKind(chataccess.RouteGVK)
	if err := f.kube.Get(ctx, client.ObjectKey{Namespace: instanceNamespace(current.Name), Name: chataccess.RouteName(current.Name)}, chatRoute); !apierrors.IsNotFound(err) {
		t.Fatalf("suspension retained private Chat route: %v", err)
	}
	assertNoLegacyCustomCompletion(t, f)

	current.Spec.Suspended = false
	current.Generation++
	if err := f.kube.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	f.reconcileOK(t)
	setReconciledCustomWorkloadReady(t, f)
	f.reconcileOK(t)
	assertReconciledCustomReady(t, f, caller)
	assertNoLegacyCustomCompletion(t, f)
}

func TestCommonCustomRuntimeAuthenticationRejectsReplacedBootstrapSecret(t *testing.T) {
	ctx := context.Background()
	f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, "custom", true, false,
		func(r *AgentReconciler, kube client.Client) {
			policy := &api.AgentPolicy{}
			if err := kube.Get(ctx, client.ObjectKey{Name: "policy-example"}, policy); err != nil {
				t.Fatal(err)
			}
			policy.Spec.Harnesses.Allowed = append(policy.Spec.Harnesses.Allowed, api.AgentTypeCustom)
			if err := kube.Update(ctx, policy); err != nil {
				t.Fatal(err)
			}
			agent := &api.Agent{}
			if err := kube.Get(ctx, client.ObjectKey{Name: "example"}, agent); err != nil {
				t.Fatal(err)
			}
			r.CustomApprovedImages = []string{*agent.Spec.Agent.Image}
		},
	)
	secret := &corev1.Secret{}
	key := client.ObjectKey{Namespace: instanceNamespace(f.agent.Name), Name: sandboxRuntimeSecretName(f.agent)}
	if err := f.kube.Get(ctx, key, secret); err != nil {
		t.Fatal(err)
	}
	secret.OwnerReferences = nil
	secret.Data = map[string][]byte{}
	if err := f.kube.Update(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if err := f.r.ensureCommonCustomRuntimeAuthentication(ctx, f.current(t), logr.Discard()); err == nil {
		t.Fatal("foreign replacement runtime Secret accepted for authentication mutation")
	}
	if err := f.kube.Get(ctx, key, secret); err != nil {
		t.Fatal(err)
	}
	if len(secret.Data["CETIRUN_AUTH_KEY"]) != 0 {
		t.Fatal("foreign replacement runtime Secret received an authentication key")
	}
}

func setReconciledCustomWorkloadReady(t *testing.T, f *nativeSandboxFixture) {
	t.Helper()
	ctx := context.Background()
	workload := &appsv1.StatefulSet{}
	if err := f.kube.Get(ctx, client.ObjectKey{Namespace: instanceNamespace(f.agent.Name), Name: instanceServiceName(f.agent.Name)}, workload); err != nil {
		current := f.current(t)
		t.Fatalf("read common Custom workload: %v; phase=%s conditions=%v", err, current.Status.Phase, current.Status.Conditions)
	}
	workload.Status.ObservedGeneration = workload.Generation
	workload.Status.Replicas, workload.Status.ReadyReplicas, workload.Status.UpdatedReplicas = 1, 1, 1
	if err := f.kube.Status().Update(ctx, workload); err != nil {
		t.Fatal(err)
	}
}

func setReconciledCustomWorkloadStopped(t *testing.T, f *nativeSandboxFixture) {
	t.Helper()
	workload := f.workload(t)
	workload.Status.ObservedGeneration = workload.Generation
	workload.Status.Replicas, workload.Status.ReadyReplicas, workload.Status.UpdatedReplicas = 0, 0, 0
	if err := f.kube.Status().Update(context.Background(), workload); err != nil {
		t.Fatal(err)
	}
}

func assertReconciledCustomReady(t *testing.T, f *nativeSandboxFixture, caller chataccess.Caller) {
	t.Helper()
	ctx := context.Background()
	current := f.current(t)
	if current.Status.Phase != api.PhaseReady {
		t.Fatalf("common Custom did not become Ready: phase=%s conditions=%v", current.Status.Phase, current.Status.Conditions)
	}
	chatRoute := &unstructured.Unstructured{}
	chatRoute.SetGroupVersionKind(chataccess.RouteGVK)
	if err := f.kube.Get(ctx, client.ObjectKey{Namespace: instanceNamespace(current.Name), Name: chataccess.RouteName(current.Name)}, chatRoute); err != nil {
		t.Fatalf("common Custom private Chat route was not ready: %v", err)
	}
	if err := chataccess.ValidatePrivateRoute(chatRoute, current.Name, string(current.UID), instanceNamespace(current.Name), *current.Spec.Agent.Port, caller); err != nil {
		t.Fatalf("common Custom private Chat route contract: %v", err)
	}
}

func assertNoLegacyCustomCompletion(t *testing.T, f *nativeSandboxFixture) {
	t.Helper()
	var records corev1.ConfigMapList
	if err := f.kube.List(context.Background(), &records); err != nil {
		t.Fatal(err)
	}
	for index := range records.Items {
		if records.Items[index].Data["completion"] != "" {
			t.Fatalf("common Custom wrote legacy completion record %s/%s", records.Items[index].Namespace, records.Items[index].Name)
		}
	}
}
