// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
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
				platform.Topology.Gateway[0].Peer.PodSelector = map[string]string{"app.kubernetes.io/name": "cetirun-ai-gateway"}
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
