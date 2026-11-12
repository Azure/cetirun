// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const selectorBackendDetail = "blocked Pod CIDRs cannot be enforced on selector-bound service backends"

// blockPodCIDRForTest adds a blocked CIDR inside the Pod network. It does not
// contain a fixture peer IP, so IP-bound DNS and Gateway peers accept it.
func blockPodCIDRForTest(t *testing.T) func(*AgentReconciler, client.Client) {
	t.Helper()
	return func(_ *AgentReconciler, kube client.Client) {
		var teams api.TeamList
		if err := kube.List(context.Background(), &teams); err != nil || len(teams.Items) != 1 {
			t.Fatal("fixture requires one Team:", err)
		}
		policy := &api.AgentPolicy{}
		if err := kube.Get(context.Background(), client.ObjectKey{Name: teams.Items[0].Spec.AgentPolicyRef}, policy); err != nil {
			t.Fatal(err)
		}
		policy.Spec.Network.BlockedCIDRs = []string{"10.244.200.0/24"}
		if err := kube.Update(context.Background(), policy); err != nil {
			t.Fatal(err)
		}
	}
}

func selectorEgressForTest(t *testing.T, f *nativeSandboxFixture, selector bool) {
	t.Helper()
	updateContainerPlatformForTest(t, f.r, f.kube, func(document *SandboxPlatformConfiguration) {
		for backend, platform := range document.Platforms {
			if selector {
				platform.Topology.DNS[0].PodSelector = map[string]string{"k8s-app": "kube-dns"}
				platform.Topology.Gateway[0].Peer.PodSelector = map[string]string{
					"app.kubernetes.io/name": "cetirun-ai-gateway", sandboxpolicy.PeerLabel: sandboxpolicy.PeerAIGateway,
				}
			} else {
				platform.Topology.DNS[0].PodSelector = nil
				platform.Topology.Gateway[0].Peer.PodSelector = nil
			}
			document.Platforms[backend] = platform
		}
	})
}

func settleReadyForTest(t *testing.T, f *nativeSandboxFixture) {
	t.Helper()
	for range 6 {
		workload := f.workload(t)
		workload.Status.ObservedGeneration = workload.Generation
		workload.Status.ReadyReplicas, workload.Status.UpdatedReplicas = 1, 1
		if err := f.kube.Status().Update(context.Background(), workload); err != nil {
			t.Fatal(err)
		}
		_, _ = f.reconcile()
		if f.current(t).Status.Phase == api.PhaseReady {
			return
		}
	}
	t.Fatalf("Agent did not become ready: phase=%s conditions=%+v", f.current(t).Status.Phase, f.current(t).Status.Conditions)
}

// TestRegistrationIncompatiblePolicyHoldsAgentWithoutRevokingItsKey reproduces
// #490: an AgentPolicy that IP-bound peers accept is refused after the
// registration moves DNS and Gateway peers to Pod selectors.
func TestRegistrationIncompatiblePolicyHoldsAgentWithoutRevokingItsKey(t *testing.T) {
	ctx := context.Background()
	f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, "openclaw", true, false,
		blockPodCIDRForTest(t), withReconciledCoordination(t))
	config := f.r.SandboxPreparation
	policyController := &AgentPolicyReconciler{
		Client: f.kube, APIReader: f.kube, NativeExecution: true,
		Source: config.Configuration, Now: config.Now, MaxSnapshotAge: config.MaxSnapshotAge,
	}
	team := &api.Team{}
	if err := f.kube.Get(ctx, client.ObjectKey{Name: f.current(t).Spec.Team}, team); err != nil {
		t.Fatal(err)
	}
	policyKey := client.ObjectKey{Name: team.Spec.AgentPolicyRef}
	acceptance := func() *metav1.Condition {
		t.Helper()
		if _, err := policyController.Reconcile(ctx, ctrl.Request{NamespacedName: policyKey}); err != nil {
			t.Fatal("assess policy:", err)
		}
		policy := &api.AgentPolicy{}
		if err := f.kube.Get(ctx, policyKey, policy); err != nil {
			t.Fatal(err)
		}
		return apimeta.FindStatusCondition(policy.Status.Conditions, api.ConditionAgentPolicyAccepted)
	}
	if accepted := acceptance(); accepted == nil || accepted.Status != metav1.ConditionTrue {
		t.Fatalf("IP-bound peers must accept the blocked Pod CIDR: %+v", accepted)
	}
	settleReadyForTest(t, f)
	before := f.current(t)
	token := aigateway.Token(f.gateway.generated[0].Key)
	generated := len(f.gateway.generated)

	selectorEgressForTest(t, f, true)
	accepted := acceptance()
	if accepted == nil || accepted.Status != metav1.ConditionFalse || accepted.Reason != policyRegistrationIncompatibleReason ||
		!strings.Contains(accepted.Message, selectorBackendDetail) || strings.Contains(accepted.Message, "10.244.200") {
		t.Fatalf("registration incompatibility was not reported with its fixed reason: %+v", accepted)
	}
	teams := &TeamReconciler{Client: f.kube, APIReader: f.kube, NativeExecution: true,
		Source: config.Configuration, Now: config.Now, MaxSnapshotAge: config.MaxSnapshotAge}
	if _, err := teams.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(team)}); err != nil {
		t.Fatal("assess Team:", err)
	}
	if err := f.kube.Get(ctx, client.ObjectKeyFromObject(team), team); err != nil {
		t.Fatal(err)
	}
	if report := team.Status.Compatibility; report == nil || report.State != api.CompatibilityIncompatible ||
		len(report.Sandboxes) != 1 || report.Sandboxes[0].Reason != "PolicyNotAccepted" ||
		!strings.Contains(report.Message, policyRegistrationIncompatibleReason) {
		t.Fatalf("Team compatibility did not report the refused policy: %+v", report)
	}
	for range 2 {
		_, _ = f.reconcile()
	}
	held := f.current(t)
	resolved := apimeta.FindStatusCondition(held.Status.Conditions, api.ConditionPolicyResolved)
	if held.Status.Phase == api.PhaseReady || resolved == nil || resolved.Reason != "DependencyUnavailable" ||
		!strings.Contains(resolved.Message, policyRegistrationIncompatibleReason) || !strings.Contains(resolved.Message, selectorBackendDetail) {
		t.Fatalf("Agent did not take the dependency hold: phase=%s condition=%+v", held.Status.Phase, resolved)
	}
	if _, found := f.gateway.keys[token]; !found || held.Status.AIGateway == nil ||
		held.Status.AIGateway.Phase == api.AIGatewayPhaseDeleting || held.Status.AIGateway.KeyID != before.Status.AIGateway.KeyID {
		t.Fatalf("registration incompatibility revoked the key: status=%+v", held.Status.AIGateway)
	}

	selectorEgressForTest(t, f, false)
	if accepted := acceptance(); accepted == nil || accepted.Status != metav1.ConditionTrue {
		t.Fatalf("restored registration did not accept the policy: %+v", accepted)
	}
	settleReadyForTest(t, f)
	recovered := f.current(t)
	if recovered.Status.AIGateway == nil || recovered.Status.AIGateway.KeyID != before.Status.AIGateway.KeyID ||
		len(f.gateway.generated) != generated {
		t.Fatalf("Agent did not recover with its key: status=%+v generated=%d", recovered.Status.AIGateway, len(f.gateway.generated))
	}
	for _, condition := range recovered.Status.Conditions {
		if condition.Reason == "KeyRecordLost" {
			t.Fatalf("recovery reported a lost key record: %+v", condition)
		}
	}

	// A new policy generation is assessed on its own: the same refusal is then
	// a definitive denial, not a registration change.
	selectorEgressForTest(t, f, true)
	policy := &api.AgentPolicy{}
	if err := f.kube.Get(ctx, policyKey, policy); err != nil {
		t.Fatal(err)
	}
	policy.Generation++
	if err := f.kube.Update(ctx, policy); err != nil {
		t.Fatal(err)
	}
	if accepted := acceptance(); accepted == nil || accepted.Status != metav1.ConditionFalse || accepted.Reason != "UnsupportedControl" ||
		!strings.Contains(accepted.Message, selectorBackendDetail) {
		t.Fatalf("a changed policy generation was not refused definitively: %+v", accepted)
	}
}

func TestPolicyDenialReportsKeyRevocationOnce(t *testing.T) {
	ctx := context.Background()
	f := readyPolicyRateLimitFixture(t, "openclaw", true)
	recorder := record.NewFakeRecorder(1000)
	f.r.EventRecorder = recorder
	team := &api.Team{}
	if err := f.kube.Get(ctx, types.NamespacedName{Name: f.current(t).Spec.Team}, team); err != nil {
		t.Fatal(err)
	}
	team.Spec.SandboxRefs = nil
	if err := f.kube.Update(ctx, team); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := f.reconcile(); err != nil {
			t.Fatal(err)
		}
	}
	if phase := f.current(t).Status.AIGateway.Phase; phase != api.AIGatewayPhaseDeleting {
		t.Fatalf("denial did not revoke the key: phase=%s", phase)
	}
	revoked := 0
	for len(recorder.Events) != 0 {
		if strings.Contains(<-recorder.Events, "AIGatewayKeyRevoked") {
			revoked++
		}
	}
	if revoked != 1 {
		t.Fatalf("AIGatewayKeyRevoked events = %d, want 1", revoked)
	}
}

// TestUnusableRegistrationShowsItsReason reproduces a held native Agent whose
// only status was "AgentPolicy acceptance observation is unavailable": the
// selector-bound Gateway peer of an earlier registration does not select the
// protected label, so the registration cannot be used.
func TestUnusableRegistrationShowsItsReason(t *testing.T) {
	const canary = "value-canary-5d1f"
	reason := "invalid Sandbox platform declaration: a selector-bound AI Gateway peer must select the protected " +
		sandboxpolicy.PeerLabel + " Pod label"
	ctx := context.Background()
	f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, "openclaw", true, false, withReconciledCoordination(t))
	config := f.r.SandboxPreparation
	policyController := &AgentPolicyReconciler{
		Client: f.kube, APIReader: f.kube, NativeExecution: true,
		Source: config.Configuration, Now: config.Now, MaxSnapshotAge: config.MaxSnapshotAge,
	}
	team := &api.Team{}
	if err := f.kube.Get(ctx, client.ObjectKey{Name: f.current(t).Spec.Team}, team); err != nil {
		t.Fatal(err)
	}
	policyKey := client.ObjectKey{Name: team.Spec.AgentPolicyRef}
	policy := &api.AgentPolicy{}
	assess := func() (*metav1.Condition, error) {
		t.Helper()
		_, err := policyController.Reconcile(ctx, ctrl.Request{NamespacedName: policyKey})
		if getErr := f.kube.Get(ctx, policyKey, policy); getErr != nil {
			t.Fatal(getErr)
		}
		return apimeta.FindStatusCondition(policy.Status.Conditions, api.ConditionAgentPolicyAccepted), err
	}
	if accepted, err := assess(); err != nil || accepted == nil || accepted.Status != metav1.ConditionTrue {
		t.Fatalf("the fixture policy was not accepted: %+v, %v", accepted, err)
	}
	settleReadyForTest(t, f)
	before := f.current(t)
	gatewaySelector := func(selector map[string]string) {
		updateContainerPlatformForTest(t, f.r, f.kube, func(document *SandboxPlatformConfiguration) {
			for backend, platform := range document.Platforms {
				platform.Topology.Gateway[0].Peer.PodSelector = selector
				document.Platforms[backend] = platform
			}
		})
	}

	gatewaySelector(map[string]string{"app.kubernetes.io/instance": canary})
	accepted, err := assess()
	if !errors.Is(err, sandboxpolicy.ErrUnsupportedControl) || !strings.Contains(err.Error(), reason) || strings.Contains(err.Error(), canary) {
		t.Fatalf("the AgentPolicy reconcile error does not carry only the fixed reason: %v", err)
	}
	if accepted == nil || accepted.Status != metav1.ConditionUnknown || accepted.Reason != "PrerequisitesUnavailable" ||
		accepted.Message != "Current platform support inventory is unavailable: "+reason+"." {
		t.Fatalf("the AgentPolicy does not show why the registration cannot be used: %+v", accepted)
	}
	for range 2 {
		_, _ = f.reconcile()
	}
	held := f.current(t)
	resolved := apimeta.FindStatusCondition(held.Status.Conditions, api.ConditionPolicyResolved)
	want := fmt.Sprintf("AgentPolicy acceptance observation is unavailable for generation %d: PrerequisitesUnavailable: %s",
		policy.Generation, accepted.Message)
	if held.Status.Phase == api.PhaseReady || resolved == nil || resolved.Reason != "DependencyUnavailable" || resolved.Message != want {
		t.Fatalf("the held Agent does not show why: phase=%s condition=%+v", held.Status.Phase, resolved)
	}
	for _, condition := range held.Status.Conditions {
		if strings.Contains(condition.Message, canary) {
			t.Fatalf("an Agent condition quoted the registration: %+v", condition)
		}
	}
	if held.Status.AIGateway == nil || held.Status.AIGateway.KeyID != before.Status.AIGateway.KeyID ||
		held.Status.AIGateway.Phase == api.AIGatewayPhaseDeleting {
		t.Fatalf("an unusable registration revoked the key: %+v", held.Status.AIGateway)
	}

	gatewaySelector(nil)
	if accepted, err := assess(); err != nil || accepted == nil || accepted.Status != metav1.ConditionTrue {
		t.Fatalf("the corrected registration did not accept the policy: %+v, %v", accepted, err)
	}
	settleReadyForTest(t, f)
	if recovered := f.current(t); recovered.Status.AIGateway == nil || recovered.Status.AIGateway.KeyID != before.Status.AIGateway.KeyID {
		t.Fatalf("the Agent did not recover with its key: %+v", recovered.Status.AIGateway)
	}
}

// TestPolicyDependencyShowsTheObservedCondition checks the Agent text when the
// Team or AgentPolicy condition is not usable for the current generation.
func TestPolicyDependencyShowsTheObservedCondition(t *testing.T) {
	ctx := context.Background()
	policyFor := func(generation, observed int64, condition metav1.Condition) *api.AgentPolicy {
		condition.Type = api.ConditionAgentPolicyAccepted
		return &api.AgentPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "example-policy", UID: "example-policy-uid", Generation: generation},
			Spec:       api.AgentPolicySpec{Harnesses: api.AgentPolicyHarnesses{Allowed: []api.AgentType{api.AgentTypeOpenClaw}}},
			Status:     api.AgentPolicyStatus{ObservedGeneration: observed, Conditions: []metav1.Condition{condition}},
		}
	}
	unavailable := metav1.Condition{Status: metav1.ConditionUnknown, ObservedGeneration: 3, Reason: "PrerequisitesUnavailable",
		Message: "Current platform support inventory is unavailable: a fixed reason."}
	refused := metav1.Condition{Status: metav1.ConditionFalse, ObservedGeneration: 2, Reason: "UnsupportedControl",
		Message: "A requested control has no supported, qualified implementation."}
	long := unavailable
	long.Message = strings.Repeat("M", 40000)
	r := &AgentReconciler{}
	for _, tc := range []struct {
		name   string
		policy *api.AgentPolicy
		want   string
	}{
		{"unknown", policyFor(3, 3, unavailable),
			"AgentPolicy acceptance observation is unavailable for generation 3: PrerequisitesUnavailable: Current platform support inventory is unavailable: a fixed reason."},
		{"another generation", policyFor(3, 3, refused),
			"AgentPolicy acceptance observation is unavailable for generation 3; the last observation is for generation 2: UnsupportedControl: A requested control has no supported, qualified implementation."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := r.verifyPolicyAcceptance(ctx, tc.policy); !isPolicyDependencyError(err) || err.Error() != tc.want {
				t.Fatalf("unexpected dependency: %v", err)
			}
		})
	}
	if err := r.verifyPolicyAcceptance(ctx, policyFor(3, 3, long)); !isPolicyDependencyError(err) ||
		utf8.RuneCountInString(err.Error()) > maxConditionSummary+128 {
		t.Fatalf("the dependency text is not bounded: %d characters", utf8.RuneCountInString(err.Error()))
	}

	// A pending generation is assessed again; the Agent shows that result.
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	r.SandboxPreparation = &SandboxPreparation{NativeExecution: true, Now: func() time.Time { return now },
		Configuration: SandboxConfigurationSourceFunc(func(context.Context) (SandboxConfigurationSnapshot, error) {
			return SandboxConfigurationSnapshot{ObservedAt: now}, nil
		})}
	accepted := metav1.Condition{Status: metav1.ConditionTrue, ObservedGeneration: 2, Reason: "PolicyAccepted", Message: "Accepted."}
	want := "AgentPolicy observation is pending for generation 3: UnsupportedControl: A requested control has no supported, qualified implementation."
	if err := r.verifyPolicyAcceptance(ctx, policyFor(3, 2, accepted)); !isPolicyDependencyError(err) || err.Error() != want {
		t.Fatalf("the pending generation does not show its assessment: %v", err)
	}

	team := &api.Team{ObjectMeta: metav1.ObjectMeta{Name: "engineering", Generation: 4}, Status: api.TeamStatus{
		ObservedGeneration: 4, Conditions: []metav1.Condition{{Type: api.ConditionTeamAccepted, Status: metav1.ConditionUnknown,
			ObservedGeneration: 4, Reason: "DependenciesUnavailable", Message: "Team Policy or Sandbox bindings cannot currently be verified."}},
	}}
	want = "Team acceptance observation is unavailable for generation 4: DependenciesUnavailable: Team Policy or Sandbox bindings cannot currently be verified."
	if err := r.verifyTeamAcceptance(ctx, team); !isPolicyDependencyError(err) || err.Error() != want {
		t.Fatalf("the Team dependency does not show its condition: %v", err)
	}
}
