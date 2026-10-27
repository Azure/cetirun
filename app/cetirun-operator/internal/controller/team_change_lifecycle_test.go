// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxbinding"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxcoordination"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type teamChangeTargetFixture struct {
	team    *api.Team
	sandbox *api.Sandbox
	policy  *api.AgentPolicy
}

func createTeamChangeTarget(t *testing.T, f *nativeSandboxFixture, name string, incompatibleHome bool) teamChangeTargetFixture {
	t.Helper()
	ctx := context.Background()
	agent := f.current(t)
	source := &api.Team{}
	if err := f.kube.Get(ctx, client.ObjectKey{Name: agent.Spec.Team}, source); err != nil {
		t.Fatal(err)
	}
	box := &api.Sandbox{}
	if err := f.kube.Get(ctx, client.ObjectKey{Name: agent.Spec.SandboxRef}, box); err != nil {
		t.Fatal(err)
	}
	policy := &api.AgentPolicy{}
	if err := f.kube.Get(ctx, client.ObjectKey{Name: source.Spec.AgentPolicyRef}, policy); err != nil {
		t.Fatal(err)
	}

	targetBox := box.DeepCopy()
	targetBox.Name, targetBox.ResourceVersion = name+"-sandbox", ""
	targetBox.UID = types.UID(name + "-sandbox-uid")
	if incompatibleHome {
		targetBox.Spec.Storage.Home.AccessMode = corev1.ReadWriteMany
	}
	if err := f.kube.Create(ctx, targetBox); err != nil {
		t.Fatal(err)
	}
	target := source.DeepCopy()
	target.Name, target.ResourceVersion = name, ""
	target.UID = types.UID(name + "-uid")
	target.Spec.SandboxRefs = []string{targetBox.Name}
	target.Spec.DefaultSandboxRef = targetBox.Name
	target.Status = api.TeamStatus{ObservedGeneration: 1, Conditions: []metav1.Condition{{
		Type: api.ConditionTeamAccepted, Status: metav1.ConditionTrue, ObservedGeneration: 1,
	}}}
	if err := f.kube.Create(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := f.kube.Status().Update(ctx, target); err != nil {
		t.Fatal(err)
	}
	return teamChangeTargetFixture{team: target, sandbox: targetBox, policy: policy}
}

func submitTeamChange(t *testing.T, f *nativeSandboxFixture, target teamChangeTargetFixture, requestID string) {
	t.Helper()
	ctx := context.Background()
	agent := f.current(t)
	agent.Spec.TeamChangeRequest = &api.TeamChangeRequest{
		RequestID: requestID, ExpectedUID: agent.UID, ExpectedResourceVersion: agent.ResourceVersion, TargetTeam: target.team.Name,
	}
	if err := f.kube.Update(ctx, agent); err != nil {
		t.Fatal(err)
	}
}

func authorizeCommittedTeamChange(t *testing.T, f *nativeSandboxFixture, target teamChangeTargetFixture) {
	t.Helper()
	ctx := context.Background()
	agent := f.current(t)
	if agent.Spec.Team != target.team.Name {
		return
	}
	if binding, err := sandboxbinding.AuthorizedBinding(agent); err == nil &&
		binding.TeamUID == target.team.UID && binding.SandboxUID == target.sandbox.UID && binding.AgentPolicyUID == target.policy.UID {
		return
	}
	record, err := sandboxbinding.Snapshot(agent, target.team, sandbox.Resources{
		Sandboxes:     map[string]*api.Sandbox{target.sandbox.Name: target.sandbox},
		AgentPolicies: map[string]*api.AgentPolicy{target.policy.Name: target.policy},
	})
	if err != nil {
		t.Fatal(err)
	}
	before := agent.DeepCopy()
	if agent.Annotations == nil {
		agent.Annotations = map[string]string{}
	}
	agent.Annotations[sandboxbinding.AuthorizationAnnotation] = record
	if err := f.kube.Patch(ctx, agent, client.MergeFrom(before)); err != nil {
		t.Fatal(err)
	}
}

func completeTeamChange(t *testing.T, f *nativeSandboxFixture, target teamChangeTargetFixture, sourcePod *corev1.Pod, expectPaused bool) {
	t.Helper()
	ctx := context.Background()
	acknowledgedSource, deletedSource, createdTargetPod := false, false, false
	for pass := 0; pass < 30; pass++ {
		authorizeCommittedTeamChange(t, f, target)
		if _, err := f.reconcile(); err != nil {
			t.Fatalf("reconcile Team change pass %d: %v", pass, err)
		}
		agent := f.current(t)
		state := agent.Status.TeamChange
		if state != nil && state.Phase == api.TeamChangeCompleted {
			return
		}
		if state != nil && state.Fenced && !acknowledgedSource {
			workload := f.workload(t)
			if workload.Spec.Replicas != nil && *workload.Spec.Replicas == 0 {
				workload.Status.ObservedGeneration = workload.Generation
				workload.Status.Replicas, workload.Status.ReadyReplicas, workload.Status.UpdatedReplicas = 0, 0, 0
				if err := f.kube.Status().Update(ctx, workload); err != nil {
					t.Fatal(err)
				}
				acknowledgedSource = true
			}
		}
		if sourcePod != nil && acknowledgedSource && !deletedSource {
			if err := f.kube.Delete(ctx, sourcePod); err != nil && !apierrors.IsNotFound(err) {
				t.Fatal(err)
			}
			deletedSource = true
			continue
		}
		if expectPaused || state == nil || state.Phase != api.TeamChangeStarting || createdTargetPod {
			continue
		}
		expected := reconciledExpectationForTest(t, f)
		for _, workload := range expected.Workloads {
			if workload.Kind != "StatefulSet" {
				continue
			}
			pod := reconciledPodForTest(t, f, workload)
			pod.UID = types.UID(fmt.Sprintf("team-change-%s-pod", target.team.Name))
			pod.Status.Phase = corev1.PodRunning
			pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
			if err := f.kube.Create(ctx, pod); err != nil {
				t.Fatal(err)
			}
			stateful := f.workload(t)
			stateful.Status.ObservedGeneration = stateful.Generation
			stateful.Status.Replicas, stateful.Status.ReadyReplicas, stateful.Status.UpdatedReplicas = 1, 1, 1
			if err := f.kube.Status().Update(ctx, stateful); err != nil {
				t.Fatal(err)
			}
			createdTargetPod = true
			break
		}
		if !createdTargetPod {
			t.Fatal("target Team change did not render a retained HOME workload")
		}
	}
	t.Fatalf("Team change did not complete: %+v", f.current(t).Status.TeamChange)
}

func assertCompletedTeamChangePreservesIdentity(t *testing.T, f *nativeSandboxFixture, target teamChangeTargetFixture,
	initial w.ReconcileExpectation, uid types.UID, keyID string, paused bool,
) {
	t.Helper()
	current := f.current(t)
	if current.Status.TeamChange == nil || current.Status.TeamChange.Phase != api.TeamChangeCompleted ||
		current.Spec.Team != target.team.Name || current.Spec.SandboxRef != target.sandbox.Name || current.UID != uid {
		t.Fatalf("Team change completion did not retain the expected binding and identity: phase=%+v team=%q sandbox=%q uid=%q",
			current.Status.TeamChange, current.Spec.Team, current.Spec.SandboxRef, current.UID)
	}
	updated := reconciledExpectationForTest(t, f)
	if updated.Home != initial.Home || updated.Namespace != initial.Namespace ||
		current.Status.AIGateway.KeyID != keyID || len(f.gateway.generated) != 1 {
		t.Fatal("Team change replaced retained HOME, namespace, or Gateway key identity")
	}
	for _, key := range f.gateway.keys {
		if paused != key.Blocked || ptr.Deref(key.Spend, 0) != 3.25 {
			t.Fatalf("Team change did not retain the Gateway key state: blocked=%t spend=%v", key.Blocked, key.Spend)
		}
	}
}

func markTeamChangeSourceRunning(t *testing.T, f *nativeSandboxFixture) {
	t.Helper()
	workload := f.workload(t)
	workload.Status.ObservedGeneration = workload.Generation
	workload.Status.Replicas, workload.Status.ReadyReplicas, workload.Status.UpdatedReplicas = 1, 1, 1
	if err := f.kube.Status().Update(context.Background(), workload); err != nil {
		t.Fatal(err)
	}
	f.reconcileOK(t)
	if current := f.current(t); current.Status.Phase != api.PhaseReady {
		t.Fatalf("fixture source did not reach running state: %+v", current.Status)
	}
}

func TestTeamChangeRunningSourceCompletesTwicePreservingIdentityHomeAndKey(t *testing.T) {
	f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, "openclaw", true, false, withReconciledCoordination(t))
	markTeamChangeSourceRunning(t, f)
	initial := reconciledExpectationForTest(t, f)
	agent := f.current(t)
	uid, keyID := agent.UID, agent.Status.AIGateway.KeyID
	for _, key := range f.gateway.keys {
		key.Spend = ptr.To(3.25)
	}

	targetA := createTeamChangeTarget(t, f, "team-change-target-a", false)
	submitTeamChange(t, f, targetA, "team-change-running-0001")
	completeTeamChange(t, f, targetA, nil, false)
	assertCompletedTeamChangePreservesIdentity(t, f, targetA, initial, uid, keyID, false)
	if f.current(t).Status.Phase != api.PhaseReady {
		t.Fatal("running source did not return to Ready after Team change")
	}

	// Completed transitions retain their fence for auditability, so this second
	// operation proves a new request ID can begin from that durable checkpoint.
	targetB := createTeamChangeTarget(t, f, "team-change-target-b", false)
	submitTeamChange(t, f, targetB, "team-change-running-0002")
	completeTeamChange(t, f, targetB, f.pod(t), false)
	assertCompletedTeamChangePreservesIdentity(t, f, targetB, initial, uid, keyID, false)
}

func TestTeamChangePausedSourceCompletesPreservingIdentityHomeAndKey(t *testing.T) {
	ctx := context.Background()
	f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, "openclaw", true, false, withReconciledCoordination(t))
	initial := reconciledExpectationForTest(t, f)
	agent := f.current(t)
	uid, keyID := agent.UID, agent.Status.AIGateway.KeyID
	for _, key := range f.gateway.keys {
		key.Spend = ptr.To(3.25)
	}
	agent.Spec.Suspended = true
	if err := f.kube.Update(ctx, agent); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		f.reconcileOK(t)
	}
	if current := f.current(t); current.Status.Phase != api.PhaseSuspended {
		t.Fatalf("fixture source did not reach suspended state: %+v", current.Status)
	}

	target := createTeamChangeTarget(t, f, "team-change-paused-target", false)
	submitTeamChange(t, f, target, "team-change-paused-0001")
	completeTeamChange(t, f, target, nil, true)
	for range 2 {
		f.reconcileOK(t)
	}
	assertCompletedTeamChangePreservesIdentity(t, f, target, initial, uid, keyID, true)
	if current := f.current(t); current.Status.Phase != api.PhaseSuspended || !current.Spec.Suspended {
		t.Fatalf("paused source was resumed by Team change: %+v", current)
	}
}

func TestTeamChangeRejectsTargetPolicyThatCannotHonorRetainedBudgetBeforeStopping(t *testing.T) {
	ctx := context.Background()
	f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, "openclaw", true, false, withReconciledCoordination(t),
		func(_ *AgentReconciler, kube client.Client) {
			agents := &api.AgentList{}
			if err := kube.List(ctx, agents); err != nil {
				t.Fatal(err)
			}
			if len(agents.Items) != 1 {
				t.Fatalf("fixture Agents=%d, want one", len(agents.Items))
			}
			agent := &agents.Items[0]
			agent.Spec.ModelAccess.MonthlyBudgetUSD = "2"
			if err := kube.Update(ctx, agent); err != nil {
				t.Fatal(err)
			}
		},
	)
	markTeamChangeSourceRunning(t, f)

	target := createTeamChangeTarget(t, f, "team-change-budget-limit", false)
	targetPolicy := target.policy.DeepCopy()
	targetPolicy.Name, targetPolicy.ResourceVersion = "team-change-budget-limit-policy", ""
	targetPolicy.UID = "team-change-budget-limit-policy-uid"
	targetPolicy.Spec.ModelAccess.GatewayBudget.MaxMonthlyUSD = "1"
	if err := f.kube.Create(ctx, targetPolicy); err != nil {
		t.Fatal(err)
	}
	target.team.Spec.AgentPolicyRef = targetPolicy.Name
	if err := f.kube.Update(ctx, target.team); err != nil {
		t.Fatal(err)
	}
	target.team.Status.ObservedGeneration = target.team.Generation
	target.team.Status.Conditions[0].ObservedGeneration = target.team.Generation
	if err := f.kube.Status().Update(ctx, target.team); err != nil {
		t.Fatal(err)
	}
	target.policy = targetPolicy

	agent := f.current(t)
	shared, copilot := true, false
	agent.Spec.TeamChangeRequest = &api.TeamChangeRequest{
		RequestID: "team-change-budget-limit-0001", ExpectedUID: agent.UID, ExpectedResourceVersion: agent.ResourceVersion,
		TargetTeam: target.team.Name, ModelAccess: &api.ModelAccessSpec{SharedModels: &shared, GitHubCopilot: &copilot},
	}
	if err := f.kube.Update(ctx, agent); err != nil {
		t.Fatal(err)
	}

	// First persist the operation and then run its preflight. Exercise the Team
	// change gate directly because this fixture already has a running native
	// single-start source.
	for range 2 {
		current := f.current(t)
		allowed, _, err := f.r.reconcileTeamChange(ctx, current)
		if err != nil || allowed {
			t.Fatalf("Team change preflight allowed=%t err=%v", allowed, err)
		}
	}
	current := f.current(t)
	state := current.Status.TeamChange
	if state == nil || state.Phase != api.TeamChangeFailed || state.Fenced || state.Stopped {
		t.Fatalf("target budget validation did not fail before stopping the source: %+v", state)
	}
	if state.FailureReason != "PreflightFailed" || !strings.Contains(state.FailureMessage, "gateway budget exceeds the policy maximum") {
		t.Fatalf("unexpected target budget validation failure: %+v", state)
	}
	if current.Spec.Team != agent.Spec.Team || current.Spec.SandboxRef != agent.Spec.SandboxRef || current.Spec.ModelAccess.MonthlyBudgetUSD != "2" {
		t.Fatalf("target budget validation changed the running source: %+v", current.Spec)
	}
}

func TestTeamChangeRejectsIncompatibleRetainedHomeBeforeStoppingSource(t *testing.T) {
	f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, "openclaw", true, false, withReconciledCoordination(t))
	markTeamChangeSourceRunning(t, f)
	initial := reconciledExpectationForTest(t, f)
	before := f.current(t)
	keyID := before.Status.AIGateway.KeyID
	target := createTeamChangeTarget(t, f, "team-change-incompatible-home", true)
	submitTeamChange(t, f, target, "team-change-incompatible-0001")
	for range 3 {
		f.reconcileOK(t)
	}
	current := f.current(t)
	if current.Status.TeamChange == nil || current.Status.TeamChange.Phase != api.TeamChangeFailed ||
		current.Status.TeamChange.FailureReason != "PreflightFailed" || current.Status.TeamChange.Fenced ||
		current.Spec.Team != before.Spec.Team || current.Spec.SandboxRef != before.Spec.SandboxRef ||
		current.Status.AIGateway.KeyID != keyID || current.Status.Phase != api.PhaseReady ||
		reconciledExpectationForTest(t, f).Home != initial.Home {
		t.Fatalf("incompatible HOME was not rejected before source stop: %+v", current.Status.TeamChange)
	}
}

func TestTeamChangeRejectsReplacedRetainedHomeBeforeStoppingSource(t *testing.T) {
	ctx := context.Background()
	f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, "openclaw", true, false, withReconciledCoordination(t))
	markTeamChangeSourceRunning(t, f)
	before := f.current(t)
	expected := reconciledExpectationForTest(t, f)
	home := &corev1.PersistentVolumeClaim{}
	if err := f.kube.Get(ctx, client.ObjectKey{Namespace: expected.Namespace.Name, Name: expected.Home.Name}, home); err != nil {
		t.Fatal(err)
	}
	replacement := home.DeepCopy()
	replacement.UID, replacement.ResourceVersion = "replacement-home-uid", ""
	replacement.Finalizers = nil
	home.Finalizers = nil
	if err := f.kube.Update(ctx, home); err != nil {
		t.Fatal(err)
	}
	if err := f.kube.Delete(ctx, home); err != nil {
		t.Fatal(err)
	}
	if err := f.kube.Create(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	target := createTeamChangeTarget(t, f, "team-change-replaced-home", false)
	submitTeamChange(t, f, target, "team-change-replaced-home-0001")
	for range 2 {
		f.reconcileOK(t)
	}
	current := f.current(t)
	if current.Status.TeamChange == nil || current.Status.TeamChange.Phase != api.TeamChangeFailed ||
		current.Status.TeamChange.FailureReason != "PreflightFailed" || current.Status.TeamChange.Fenced ||
		current.Spec.Team != before.Spec.Team || current.Spec.SandboxRef != before.Spec.SandboxRef ||
		current.Status.Phase != api.PhaseReady {
		t.Fatalf("same-name replacement HOME was not rejected before source stop: %+v", current.Status.TeamChange)
	}
}

func TestTeamChangeStartingSuspensionKeepsGatewayBlockedUntilTargetPodQuiesces(t *testing.T) {
	ctx := context.Background()
	f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, "openclaw", true, false, withReconciledCoordination(t))

	source := &api.Team{}
	if err := f.kube.Get(ctx, client.ObjectKey{Name: f.current(t).Spec.Team}, source); err != nil {
		t.Fatal(err)
	}
	target := source.DeepCopy()
	target.Name, target.UID, target.ResourceVersion = "team-change-target", types.UID("team-change-target-uid"), ""
	target.Status = api.TeamStatus{ObservedGeneration: 1, Conditions: []metav1.Condition{{
		Type: api.ConditionTeamAccepted, Status: metav1.ConditionTrue, ObservedGeneration: 1,
	}}}
	if err := f.kube.Create(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := f.kube.Status().Update(ctx, target); err != nil {
		t.Fatal(err)
	}

	f.projectGateway(t)
	agent := f.current(t)
	agent.Spec.TeamChangeRequest = &api.TeamChangeRequest{
		RequestID:               "team-change-starting-pause-001",
		ExpectedUID:             agent.UID,
		ExpectedResourceVersion: agent.ResourceVersion,
		TargetTeam:              target.Name,
	}
	if err := f.kube.Update(ctx, agent); err != nil {
		t.Fatal(err)
	}

	// Drive the real reconciler through validation, source quiescence, commit,
	// target Gateway projection and target reservation. The fake client has no
	// admission webhook, so materialize the admission-owned authorization
	// snapshot immediately after the controller's narrow protected commit.
	box := &api.Sandbox{}
	policy := &api.AgentPolicy{}
	if err := f.kube.Get(ctx, client.ObjectKey{Name: source.Spec.DefaultSandboxRef}, box); err != nil {
		t.Fatal(err)
	}
	if err := f.kube.Get(ctx, client.ObjectKey{Name: target.Spec.AgentPolicyRef}, policy); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 12; pass++ {
		agent = f.current(t)
		if agent.Spec.Team == target.Name {
			record, err := sandboxbinding.Snapshot(agent, target, sandbox.Resources{
				Sandboxes:     map[string]*api.Sandbox{box.Name: box},
				AgentPolicies: map[string]*api.AgentPolicy{policy.Name: policy},
			})
			if err != nil {
				t.Fatal(err)
			}
			before := agent.DeepCopy()
			agent.Annotations[sandboxbinding.AuthorizationAnnotation] = record
			if err := f.kube.Patch(ctx, agent, client.MergeFrom(before)); err != nil {
				t.Fatal(err)
			}
		}
		f.reconcileOK(t)
		agent = f.current(t)
		if agent.Status.TeamChange != nil && agent.Status.TeamChange.Phase == api.TeamChangeStarting {
			if _, found, err := f.r.SandboxNativeCoordination.ReadNative(ctx, agent.UID); err != nil {
				t.Fatal(err)
			} else if found {
				break
			}
		}
		if pass == 11 {
			t.Fatalf("Team change did not reach Starting with a target reservation: %+v", agent.Status.TeamChange)
		}
	}

	expected := reconciledExpectationForTest(t, f)
	var mainFound bool
	var pod *corev1.Pod
	for _, workload := range expected.Workloads {
		if workload.Kind == "StatefulSet" {
			pod = reconciledPodForTest(t, f, workload)
			mainFound = true
			break
		}
	}
	if !mainFound {
		t.Fatal("target expectation has no retained HOME workload")
	}
	pod.UID = "team-change-target-pod"
	pod.Status.Phase = corev1.PodRunning
	if err := f.kube.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	agent = f.current(t)
	agent.Spec.Suspended = true
	if err := f.kube.Update(ctx, agent); err != nil {
		t.Fatal(err)
	}

	result, err := f.reconcile()
	if err != nil || result.RequeueAfter == 0 {
		t.Fatalf("suspending Starting Agent did not wait for the live target Pod: result=%+v err=%v", result, err)
	}
	assertTeamChangeGatewayBlocked(t, f)
	if ticket, found, err := f.r.SandboxNativeCoordination.ReadTransition(ctx, agent.UID); err != nil || !found ||
		ticket.Transition.Phase != sandboxcoordination.Starting {
		t.Fatalf("live target Pod released Team transition: found=%t ticket=%+v err=%v", found, ticket, err)
	}

	if err := f.kube.Delete(ctx, pod); err != nil {
		t.Fatal(err)
	}
	f.reconcileOK(t)
	agent = f.current(t)
	if agent.Status.TeamChange == nil || agent.Status.TeamChange.Phase != api.TeamChangeCompleted {
		t.Fatalf("quiescent suspension did not complete Team transition cleanup: %+v", agent.Status.TeamChange)
	}
	if _, found, err := f.r.SandboxNativeCoordination.ReadTransition(ctx, agent.UID); err != nil || found {
		t.Fatalf("quiescent suspension retained Team transition: found=%t err=%v", found, err)
	}
	if _, found, err := f.r.SandboxNativeCoordination.ReadNative(ctx, agent.UID); err != nil || found {
		t.Fatalf("quiescent suspension retained target reservation: found=%t err=%v", found, err)
	}
	assertTeamChangeGatewayBlocked(t, f)
}

func assertTeamChangeGatewayBlocked(t *testing.T, f *nativeSandboxFixture) {
	t.Helper()
	secret := &corev1.Secret{}
	if err := f.kube.Get(context.Background(), client.ObjectKey{
		Namespace: instanceNamespace(f.agent.Name), Name: aiGatewayKeySecretName,
	}, secret); err != nil {
		t.Fatal(err)
	}
	key := f.gateway.keys[aigateway.Token(string(secret.Data[aiGatewayKeySecretField]))]
	if key == nil || !key.Blocked {
		t.Fatalf("Team-change suspension left the Gateway key usable: %+v", key)
	}
}
