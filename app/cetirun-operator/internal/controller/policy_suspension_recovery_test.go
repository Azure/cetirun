// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestReconciledSandboxSuspensionCompletesAfterPolicyBlocksItsKey(t *testing.T) {
	ctx := context.Background()
	f := readyPolicyRateLimitFixture(t, "openclaw", true)

	initial := f.current(t)
	keyID := initial.Status.AIGateway.KeyID
	generated := len(f.gateway.generated)
	var token string
	for value := range f.gateway.keys {
		token = value
	}
	f.gateway.keys[token].Spend = ptr.To(2.5)
	team := &api.Team{}
	if err := f.kube.Get(ctx, types.NamespacedName{Name: initial.Spec.Team}, team); err != nil {
		t.Fatal(err)
	}
	team.Spec.SandboxRefs = nil
	if err := f.kube.Update(ctx, team); err != nil {
		t.Fatal(err)
	}
	team.Status.ObservedGeneration = team.Generation
	for index := range team.Status.Conditions {
		if team.Status.Conditions[index].Type == api.ConditionTeamAccepted {
			team.Status.Conditions[index].ObservedGeneration = team.Generation
		}
	}
	if err := f.kube.Status().Update(ctx, team); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reconcile(); err != nil {
		t.Fatal("policy denial did not hold the running Agent:", err)
	}
	denied := f.current(t)
	if denied.Status.AIGateway == nil || denied.Status.AIGateway.KeyID != keyID ||
		denied.Status.AIGateway.Reason != aiGatewayReasonPolicyReprojection || !denied.Status.AIGateway.Blocked ||
		f.gateway.keys[token] == nil || !f.gateway.keys[token].Blocked || f.gateway.keys[token].Spend == nil || *f.gateway.keys[token].Spend != 2.5 ||
		len(f.gateway.keys) != 1 || len(f.gateway.deletedTokens) != 0 || len(f.gateway.deletedAliases) != 0 {
		t.Fatalf("policy denial did not preserve and block the key: status=%+v key=%+v deleted=%v aliases=%v", denied.Status.AIGateway, f.gateway.keys[token], f.gateway.deletedTokens, f.gateway.deletedAliases)
	}

	denied.Spec.Suspended = true
	denied.Generation++
	if err := f.kube.Update(ctx, denied); err != nil {
		t.Fatal(err)
	}
	// The policy block receipt survives an operator restart without changing
	// the existing key or its ledger.
	f.r = restartEntraReconciler(f.r)
	if _, err := f.reconcile(); err != nil {
		t.Fatal("suspension did not confirm the prior policy block:", err)
	}
	current := f.current(t)
	if current.Status.Phase != api.PhaseSuspended || current.Status.AccessURL != "" ||
		current.Status.Policy == nil || current.Status.Policy.Phase != "Denied" {
		t.Fatalf("confirmed suspended denial was not retained: phase=%s access=%q policy=%+v", current.Status.Phase, current.Status.AccessURL, current.Status.Policy)
	}
	if current.Status.ObservedGeneration != current.Generation {
		t.Fatalf("confirmed suspension did not observe the current generation: observed=%d generation=%d", current.Status.ObservedGeneration, current.Generation)
	}
	for _, kind := range []string{api.ConditionReady, api.ConditionPodReady, api.ConditionSandboxReady, api.ConditionIngressReady} {
		condition := apimeta.FindStatusCondition(current.Status.Conditions, kind)
		if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != "Suspended" ||
			condition.ObservedGeneration != current.Generation {
			t.Fatalf("confirmed suspension did not record current %s state: %+v", kind, condition)
		}
	}
	if condition := apimeta.FindStatusCondition(current.Status.Conditions, api.ConditionAIGatewayReady); condition == nil ||
		condition.Status != metav1.ConditionFalse || condition.Reason != "Suspended" ||
		condition.ObservedGeneration != current.Generation {
		t.Fatalf("confirmed suspension did not record blocked Gateway state: %+v", condition)
	}
	if replicas := ptr.Deref(f.workload(t).Spec.Replicas, -1); replicas != 0 {
		t.Fatalf("confirmed suspension did not stop workload: replicas=%d", replicas)
	}
	if current.Status.AIGateway == nil || current.Status.AIGateway.KeyID != keyID || !current.Status.AIGateway.Blocked ||
		f.gateway.keys[token] == nil || !f.gateway.keys[token].Blocked || f.gateway.keys[token].Spend == nil || *f.gateway.keys[token].Spend != 2.5 ||
		len(f.gateway.generated) != generated || len(f.gateway.keys) != 1 {
		t.Fatalf("suspension changed the blocked key: status=%+v generated=%d key=%+v", current.Status.AIGateway, len(f.gateway.generated), f.gateway.keys[token])
	}

	if _, err := f.reconcile(); err != nil {
		t.Fatal("repeated suspension did not retain the confirmed stop:", err)
	}
	if current = f.current(t); current.Status.Phase != api.PhaseSuspended || len(f.gateway.generated) != generated ||
		f.gateway.keys[token] == nil || !f.gateway.keys[token].Blocked || len(f.gateway.deletedTokens) != 0 || len(f.gateway.deletedAliases) != 0 {
		t.Fatalf("repeated suspension changed the confirmed blocked lifetime: phase=%s generated=%d key=%+v", current.Status.Phase, len(f.gateway.generated), f.gateway.keys[token])
	}

	current.Spec.Suspended = false
	current.Generation++
	if err := f.kube.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reconcile(); err != nil {
		t.Fatal("denied resume did not remain held:", err)
	}
	if current = f.current(t); current.Status.Phase == api.PhaseReady || len(f.gateway.generated) != generated ||
		f.gateway.keys[token] == nil || !f.gateway.keys[token].Blocked || len(f.gateway.keys) != 1 {
		t.Fatalf("denied resume changed the blocked key: phase=%s generated=%d key=%+v", current.Status.Phase, len(f.gateway.generated), f.gateway.keys[token])
	}

	if err := f.kube.Get(ctx, types.NamespacedName{Name: current.Spec.Team}, team); err != nil {
		t.Fatal(err)
	}
	team.Spec.SandboxRefs = []string{current.Spec.SandboxRef}
	team.Spec.DefaultSandboxRef = current.Spec.SandboxRef
	if err := f.kube.Update(ctx, team); err != nil {
		t.Fatal(err)
	}
	if err := f.kube.Get(ctx, types.NamespacedName{Name: team.Name}, team); err != nil {
		t.Fatal(err)
	}
	team.Status.ObservedGeneration = team.Generation
	for index := range team.Status.Conditions {
		if team.Status.Conditions[index].Type == api.ConditionTeamAccepted {
			team.Status.Conditions[index].ObservedGeneration = team.Generation
		}
	}
	if err := f.kube.Status().Update(ctx, team); err != nil {
		t.Fatal(err)
	}
	settleReadyForTest(t, f)
	current = f.current(t)
	if current.Status.AIGateway == nil || current.Status.AIGateway.KeyID != keyID || current.Status.AIGateway.Blocked ||
		f.gateway.keys[token] == nil || f.gateway.keys[token].Blocked || f.gateway.keys[token].Spend == nil || *f.gateway.keys[token].Spend != 2.5 ||
		len(f.gateway.generated) != generated || len(f.gateway.deletedTokens) != 0 || len(f.gateway.deletedAliases) != 0 {
		t.Fatalf("policy recovery did not resume the original key: status=%+v generated=%d key=%+v deleted=%v aliases=%v", current.Status.AIGateway, len(f.gateway.generated), f.gateway.keys[token], f.gateway.deletedTokens, f.gateway.deletedAliases)
	}
}

func TestReconciledSandboxSuspensionStillRejectsAnUnrecordedMissingKey(t *testing.T) {
	ctx := context.Background()
	f := readyPolicyRateLimitFixture(t, "openclaw", true)

	current := f.current(t)
	generated := len(f.gateway.generated)
	for token := range f.gateway.keys {
		delete(f.gateway.keys, token)
	}
	current.Spec.Suspended = true
	current.Generation++
	if err := f.kube.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reconcile(); err == nil {
		t.Fatal("an unrecorded missing key completed suspension")
	}
	current = f.current(t)
	if current.Status.Phase != api.PhasePending || len(f.gateway.generated) != generated {
		t.Fatalf("an unrecorded missing key was not held fail-closed: phase=%s generated=%d", current.Status.Phase, len(f.gateway.generated))
	}
}

func TestCommonCustomDeniedSuspensionClearsAccessConditions(t *testing.T) {
	ctx := context.Background()
	f := customTerminalFixture(t, "kata")
	readyCustomWorkload(t, f)
	terminal := customTerminalDeployment(t, f)
	terminal.Status = appsv1.DeploymentStatus{
		ObservedGeneration: terminal.Generation, Replicas: 1, ReadyReplicas: 1,
		UpdatedReplicas: 1, AvailableReplicas: 1,
	}
	if err := f.kube.Status().Update(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		f.reconcileOK(t)
	}
	current := f.current(t)
	if current.Status.Phase != api.PhaseReady ||
		!apimeta.IsStatusConditionTrue(current.Status.Conditions, api.ConditionTerminalConfigured) ||
		!apimeta.IsStatusConditionTrue(current.Status.Conditions, api.ConditionAIGatewayReady) {
		t.Fatalf("Custom fixture did not reach actual Ready state: %+v", current.Status)
	}
	// Desktop is disabled by this current Custom fixture, but an older
	// configuration can have left its successful condition in status. Verify
	// the denied-suspension path withdraws that stale access receipt too.
	setCondition(&current.Status.Conditions, api.ConditionDesktopConfigured, metav1.ConditionTrue,
		"HistoricalDesktopConfigured", "Synthetic prior desktop configuration")
	for index := range current.Status.Conditions {
		if current.Status.Conditions[index].Type == api.ConditionDesktopConfigured {
			current.Status.Conditions[index].ObservedGeneration = current.Generation
		}
	}
	if err := f.kube.Status().Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	if condition := apimeta.FindStatusCondition(f.current(t).Status.Conditions, api.ConditionDesktopConfigured); condition == nil ||
		condition.Status != metav1.ConditionTrue || condition.Reason != "HistoricalDesktopConfigured" {
		t.Fatalf("Custom fixture did not retain the seeded historical desktop state: %+v", condition)
	}

	team := &api.Team{}
	if err := f.kube.Get(ctx, types.NamespacedName{Name: current.Spec.Team}, team); err != nil {
		t.Fatal(err)
	}
	team.Spec.SandboxRefs = nil
	if err := f.kube.Update(ctx, team); err != nil {
		t.Fatal(err)
	}
	team.Status.ObservedGeneration = team.Generation
	for index := range team.Status.Conditions {
		if team.Status.Conditions[index].Type == api.ConditionTeamAccepted {
			team.Status.Conditions[index].ObservedGeneration = team.Generation
		}
	}
	if err := f.kube.Status().Update(ctx, team); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reconcile(); err == nil {
		t.Fatal("denied Custom Agent did not wait for workload shutdown")
	}
	main := f.workload(t)
	main.Status = appsv1.StatefulSetStatus{ObservedGeneration: main.Generation}
	if err := f.kube.Status().Update(ctx, main); err != nil {
		t.Fatal(err)
	}
	terminal = customTerminalDeployment(t, f)
	terminal.Status = appsv1.DeploymentStatus{ObservedGeneration: terminal.Generation}
	if err := f.kube.Status().Update(ctx, terminal); err != nil {
		t.Fatal(err)
	}

	current = f.current(t)
	current.Spec.Suspended = true
	current.Generation++
	if err := f.kube.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	f.reconcileOK(t)
	current = f.current(t)
	if current.Status.Phase != api.PhaseSuspended || current.Status.ObservedGeneration != current.Generation {
		t.Fatalf("denied Custom suspension was not confirmed at its current generation: %+v", current.Status)
	}
	for _, kind := range []string{api.ConditionReady, api.ConditionPodReady, api.ConditionSandboxReady,
		api.ConditionIngressReady, api.ConditionAIGatewayReady, api.ConditionDesktopConfigured, api.ConditionTerminalConfigured} {
		condition := apimeta.FindStatusCondition(current.Status.Conditions, kind)
		if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != "Suspended" ||
			condition.ObservedGeneration != current.Generation {
			t.Fatalf("denied Custom suspension left stale %s state: %+v", kind, condition)
		}
	}
}

type policyBlockFailureGateway struct {
	*fakeAIGateway
	err error
}

func (g *policyBlockFailureGateway) SetKeyBlocked(context.Context, string, bool) error {
	return g.err
}

func TestPolicyDeniedSuspensionWaitsForGatewayBlock(t *testing.T) {
	ctx := context.Background()
	f := readyPolicyRateLimitFixture(t, "openclaw", true)

	current := f.current(t)
	current.Spec.Suspended = true
	current.Generation++
	if err := f.kube.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	current = f.current(t)
	failure := errors.New("synthetic Gateway block failure")
	gateway := &policyBlockFailureGateway{fakeAIGateway: f.gateway, err: failure}
	f.r.aiGatewayClientFactory = func(context.Context) (aiGatewayKeyClient, error) { return gateway, nil }
	if _, err := f.r.holdPolicyDenied(ctx, current); !errors.Is(err, failure) {
		t.Fatalf("Gateway block failure was hidden: %v", err)
	}

	current = f.current(t)
	if current.Status.Phase != api.PhasePending || current.Status.AIGateway == nil ||
		current.Status.AIGateway.Reason != aiGatewayReasonSuspended || current.Status.AIGateway.KeyID == "" {
		t.Fatalf("failed policy block did not remain pending with its identity: phase=%s gateway=%+v", current.Status.Phase, current.Status.AIGateway)
	}
	if len(f.gateway.keys) != 1 || len(f.gateway.generated) != 1 {
		t.Fatalf("failed policy block changed the Gateway lifetime: keys=%d generated=%d", len(f.gateway.keys), len(f.gateway.generated))
	}
	if replicas := ptr.Deref(f.workload(t).Spec.Replicas, -1); replicas != 0 {
		t.Fatalf("Gateway block failure skipped independent workload stop: replicas=%d", replicas)
	}
}

func TestPolicyDeniedSuspensionWaitsForWorkloadStop(t *testing.T) {
	ctx := context.Background()
	f := readyPolicyRateLimitFixture(t, "openclaw", true)

	current := f.current(t)
	current.Spec.Suspended = true
	current.Generation++
	if err := f.kube.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	current = f.current(t)
	failure := errors.New("synthetic workload stop failure")
	f.r.Client = interceptor.NewClient(f.r.Client.(client.WithWatch), interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, object client.Object, patch client.Patch, options ...client.PatchOption) error {
			if _, ok := object.(*appsv1.StatefulSet); ok {
				return failure
			}
			return c.Patch(ctx, object, patch, options...)
		},
	})
	if _, err := f.r.holdPolicyDenied(ctx, current); !errors.Is(err, failure) {
		t.Fatalf("workload stop failure was hidden: %v", err)
	}

	current = f.current(t)
	if current.Status.Phase != api.PhasePending || current.Status.AIGateway == nil ||
		current.Status.AIGateway.Reason != aiGatewayReasonSuspended || current.Status.AIGateway.KeyID == "" {
		t.Fatalf("failed workload stop incorrectly completed suspension: phase=%s gateway=%+v", current.Status.Phase, current.Status.AIGateway)
	}
	condition := apimeta.FindStatusCondition(current.Status.Conditions, api.ConditionPodReady)
	if current.Status.ObservedGeneration != current.Generation || condition == nil ||
		condition.Status != metav1.ConditionFalse || condition.Reason != "SuspensionPending" ||
		condition.ObservedGeneration != current.Generation {
		t.Fatalf("failed workload stop did not record current pending stop state: observed=%d generation=%d pod=%+v", current.Status.ObservedGeneration, current.Generation, condition)
	}
	if gateway := apimeta.FindStatusCondition(current.Status.Conditions, api.ConditionAIGatewayReady); gateway == nil ||
		gateway.Status != metav1.ConditionFalse || gateway.Reason != "SuspensionPending" ||
		gateway.ObservedGeneration != current.Generation {
		t.Fatalf("failed workload stop did not record the completed key block: %+v", gateway)
	}
	if len(f.gateway.keys) != 1 || len(f.gateway.generated) != 1 {
		t.Fatalf("workload stop failure changed the blocked key: keys=%d generated=%d", len(f.gateway.keys), len(f.gateway.generated))
	}
	if replicas := ptr.Deref(f.workload(t).Spec.Replicas, -1); replicas != 1 {
		t.Fatalf("injected workload-stop failure did not leave the workload unchanged: replicas=%d", replicas)
	}
}
