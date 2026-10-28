// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package controller

import (
	"context"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload/testfixture"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// This uses a local API server and the served Agent webhooks. Workload and
// Gateway observations remain synthetic: envtest has no StatefulSet, PVC, or
// kubelet controller. It covers the API-server generation increment and the
// Team preflight checkpoint only: a rejected request leaves the admitted
// source unchanged at that boundary. It deliberately does not run the ordinary
// common-execution Reconcile loop, which is covered by its focused regressions
// and live verification.
func TestTeamChangeRejectedRequestRetainsServedRuntimeProof(t *testing.T) {
	h := newNativeStartAPIHarnessForAgent(t, true, false, corev1.ReadWriteOncePod, "openclaw")
	f := h.nativeSandboxFixture
	f.seedRetainedBootstrap(t)
	h.create(t, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Namespace: instanceNamespace(f.agent.Name), Name: "default",
	}})
	f.prepare(t)
	_, held := f.expectation(t)
	if _, err := f.reconcile(); err != nil {
		t.Fatalf("release served retained hold: %v", err)
	}
	h.assertHoldAdmission(t, held.Revision, true)
	_, running := f.expectation(t)
	pod := testfixture.Pod(*running)
	pod.Spec.Overhead = nil
	h.create(t, pod)
	f.reconcileOK(t)
	claimBound := changeNativeClaimDuringRecheck(f, running.Home, func(ctx context.Context, key client.ObjectKey) error {
		return bindNativeTestClaim(ctx, h.admin, key)
	})
	h.observeReadyPod(t, pod)
	f.reconcileOK(t)
	if !claimBound() {
		t.Fatal("served source did not observe the synthetic HOME binding")
	}
	f.assertReady(t)

	before := f.current(t)
	beforeGeneration, beforeKey := before.Generation, before.Status.AIGateway.KeyID
	beforePod := f.pod(t).UID
	beforeWorkload := f.workload(t)
	target := servedIncompatibleTeamChangeTarget(t, h, "served-incompatible-team-change")
	before.Spec.TeamChangeRequest = &api.TeamChangeRequest{
		RequestID: "served-incompatible-0001", ExpectedUID: before.UID,
		ExpectedResourceVersion: before.ResourceVersion, TargetTeam: target.team.Name,
	}
	if err := h.admin.Update(h.ctx, before); err != nil {
		t.Fatalf("write served incompatible Team request: %v", err)
	}
	requested := f.current(t)
	if requested.Generation != beforeGeneration+1 || requested.ExecutionGeneration() != beforeGeneration {
		t.Fatalf("request-only served generation was not normalized: raw=%d execution=%d source=%d", requested.Generation, requested.ExecutionGeneration(), beforeGeneration)
	}

	for pass := 0; pass < 2; pass++ {
		current := f.current(t)
		allowed, _, err := f.r.reconcileTeamChange(h.ctx, current)
		if err != nil || allowed {
			t.Fatalf("served Team preflight pass %d allowed=%t err=%v", pass, allowed, err)
		}
	}
	after := f.current(t)
	afterWorkload := f.workload(t)
	if after.Status.TeamChange == nil || after.Status.TeamChange.Phase != api.TeamChangeFailed ||
		after.Status.TeamChange.FailureReason != "PreflightFailed" || after.Status.TeamChange.Fenced || after.Status.TeamChange.Stopped ||
		after.Status.Phase != api.PhaseReady || !apimeta.IsStatusConditionTrue(after.Status.Conditions, api.ConditionReady) ||
		after.ExecutionGeneration() != beforeGeneration || after.Status.AIGateway.KeyID != beforeKey ||
		f.pod(t).UID != beforePod || ptr.Deref(afterWorkload.Spec.Replicas, 0) != 1 ||
		beforeWorkload.UID != afterWorkload.UID {
		t.Fatalf("rejected served Team request changed the running source: checkpoint=%+v status=%+v", after.Status.TeamChange, after.Status)
	}
}

func servedIncompatibleTeamChangeTarget(t *testing.T, h *nativeStartAPIHarness, name string) teamChangeTargetFixture {
	t.Helper()
	agent := h.current(t)
	source := &api.Team{}
	if err := h.admin.Get(h.ctx, client.ObjectKey{Name: agent.Spec.Team}, source); err != nil {
		t.Fatal(err)
	}
	box := &api.Sandbox{}
	if err := h.admin.Get(h.ctx, client.ObjectKey{Name: agent.Spec.SandboxRef}, box); err != nil {
		t.Fatal(err)
	}
	policy := &api.AgentPolicy{}
	if err := h.admin.Get(h.ctx, client.ObjectKey{Name: source.Spec.AgentPolicyRef}, policy); err != nil {
		t.Fatal(err)
	}
	targetBox := box.DeepCopy()
	targetBox.Name, targetBox.ResourceVersion, targetBox.UID = name+"-sandbox", "", ""
	targetBox.Spec.Storage.Home.Size = resource.MustParse("101Gi")
	if err := h.admin.Create(h.ctx, targetBox); err != nil {
		t.Fatal(err)
	}
	target := source.DeepCopy()
	target.Name, target.ResourceVersion, target.UID = name, "", ""
	target.Spec.SandboxRefs = []string{targetBox.Name}
	target.Spec.DefaultSandboxRef = targetBox.Name
	if err := h.admin.Create(h.ctx, target); err != nil {
		t.Fatal(err)
	}
	target.Status = *source.Status.DeepCopy()
	target.Status.ObservedGeneration = target.Generation
	for index := range target.Status.Conditions {
		target.Status.Conditions[index].ObservedGeneration = target.Generation
	}
	if err := h.admin.Status().Update(h.ctx, target); err != nil {
		t.Fatal(err)
	}
	return teamChangeTargetFixture{team: target, sandbox: targetBox, policy: policy}
}
