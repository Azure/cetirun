// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// policyHistoryFixture is a native Agent whose AgentPolicy blocks a Pod CIDR.
// IP-bound DNS and Gateway peers can enforce this policy, and selector-bound
// peers cannot. The AgentPolicy controller reads the platform inventory
// through a source that fails on request.
type policyHistoryFixture struct {
	f          *nativeSandboxFixture
	policies   *AgentPolicyReconciler
	policyKey  client.ObjectKey
	generation int64
	// readFails makes the inventory read fail, and expired makes the
	// inventory return an expired snapshot.
	readFails, expired bool
	token, keyID       string
	generated          int
}

func newPolicyHistoryFixture(t *testing.T) *policyHistoryFixture {
	t.Helper()
	f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, "openclaw", true, false,
		blockPodCIDRForTest(t), withReconciledCoordination(t))
	h := &policyHistoryFixture{f: f}
	config := f.r.SandboxPreparation
	inventory := config.Configuration
	h.policies = &AgentPolicyReconciler{
		Client: f.kube, APIReader: f.kube, NativeExecution: true, Now: config.Now, MaxSnapshotAge: config.MaxSnapshotAge,
		Source: SandboxConfigurationSourceFunc(func(ctx context.Context) (SandboxConfigurationSnapshot, error) {
			if h.readFails {
				return SandboxConfigurationSnapshot{}, fmt.Errorf("%w: synthetic platform inventory read failure", sandboxpolicy.ErrUnavailable)
			}
			snapshot, err := inventory.Snapshot(ctx)
			if h.expired {
				snapshot.ObservedAt = snapshot.ObservedAt.Add(-time.Hour)
			}
			return snapshot, err
		}),
	}
	team := &api.Team{}
	if err := f.kube.Get(context.Background(), client.ObjectKey{Name: f.current(t).Spec.Team}, team); err != nil {
		t.Fatal(err)
	}
	h.policyKey = client.ObjectKey{Name: team.Spec.AgentPolicyRef}
	if h.generation = h.policy(t).Generation; h.generation <= 0 {
		t.Fatalf("the fixture AgentPolicy has generation %d", h.generation)
	}
	return h
}

func (h *policyHistoryFixture) policy(t *testing.T) *api.AgentPolicy {
	t.Helper()
	policy := &api.AgentPolicy{}
	if err := h.f.kube.Get(context.Background(), h.policyKey, policy); err != nil {
		t.Fatal(err)
	}
	return policy
}

// observe runs the AgentPolicy controller once. It checks the Accepted
// condition of the current generation and the acceptance history. An Unknown
// observation retries periodically, without a reconcile error.
func (h *policyHistoryFixture) observe(t *testing.T, status metav1.ConditionStatus, reason string, history int64) *metav1.Condition {
	t.Helper()
	result, err := h.policies.Reconcile(context.Background(), ctrl.Request{NamespacedName: h.policyKey})
	policy := h.policy(t)
	accepted := apimeta.FindStatusCondition(policy.Status.Conditions, api.ConditionAgentPolicyAccepted)
	if accepted == nil || accepted.Status != status || accepted.Reason != reason || accepted.ObservedGeneration != policy.Generation ||
		policy.Status.AcceptedGeneration != history || err != nil ||
		(result.RequeueAfter == configurationUnavailableRequeue) != (status == metav1.ConditionUnknown) {
		t.Fatalf("AgentPolicy generation %d: condition=%+v acceptedGeneration=%d result=%+v err=%v; want %s/%s and acceptedGeneration %d",
			policy.Generation, accepted, policy.Status.AcceptedGeneration, result, err, status, reason, history)
	}
	return accepted
}

// settle makes the Agent Ready and records its Gateway key.
func (h *policyHistoryFixture) settle(t *testing.T) {
	t.Helper()
	settleReadyForTest(t, h.f)
	agent := h.f.current(t)
	if agent.Status.AIGateway == nil || agent.Status.AIGateway.KeyID == "" || len(h.f.gateway.generated) == 0 {
		t.Fatalf("the ready Agent has no Gateway key: %+v", agent.Status.AIGateway)
	}
	h.token = aigateway.Token(h.f.gateway.generated[0].Key)
	h.keyID, h.generated = agent.Status.AIGateway.KeyID, len(h.f.gateway.generated)
}

// expectHeld reconciles the Agent and checks that it waits for the
// AgentPolicy: it is not Ready, it has no replica, and it keeps its Gateway
// key in the blocked state.
func (h *policyHistoryFixture) expectHeld(t *testing.T, policyReason string) {
	t.Helper()
	for range 2 {
		_, _ = h.f.reconcile()
	}
	agent := h.f.current(t)
	resolved := apimeta.FindStatusCondition(agent.Status.Conditions, api.ConditionPolicyResolved)
	if agent.Status.Phase == api.PhaseReady || resolved == nil || resolved.Reason != "DependencyUnavailable" ||
		!strings.Contains(resolved.Message, policyReason) {
		t.Fatalf("the Agent does not wait for the AgentPolicy: phase=%s condition=%+v", agent.Status.Phase, resolved)
	}
	if replicas := ptr.Deref(h.f.workload(t).Spec.Replicas, 1); replicas != 0 {
		t.Fatalf("the held Agent has %d replicas", replicas)
	}
	key, found := h.f.gateway.keys[h.token]
	blocked := found && key.Blocked
	if !blocked || agent.Status.AIGateway == nil || agent.Status.AIGateway.KeyID != h.keyID ||
		agent.Status.AIGateway.Phase == api.AIGatewayPhaseDeleting {
		t.Fatalf("the hold did not keep and block the Gateway key: found=%t blocked=%t status=%+v", found, blocked, agent.Status.AIGateway)
	}
}

// expectRecovered checks that the Agent runs again with the same Gateway key.
func (h *policyHistoryFixture) expectRecovered(t *testing.T) {
	t.Helper()
	settleReadyForTest(t, h.f)
	agent := h.f.current(t)
	key, found := h.f.gateway.keys[h.token]
	blocked := found && key.Blocked
	if !found || blocked || agent.Status.AIGateway == nil || agent.Status.AIGateway.KeyID != h.keyID ||
		len(h.f.gateway.generated) != h.generated {
		t.Fatalf("the Agent did not recover with its key: found=%t blocked=%t status=%+v generated=%d",
			found, blocked, agent.Status.AIGateway, len(h.f.gateway.generated))
	}
	for _, condition := range agent.Status.Conditions {
		if condition.Reason == "KeyRecordLost" {
			t.Fatalf("recovery reported a lost key record: %+v", condition)
		}
	}
}

// TestRegistrationIncompatibleSurvivesTransientObservations checks that an
// inventory read failure or an expired snapshot between two registration-only
// refusals does not make the second refusal definitive. A definitive refusal
// revokes the Gateway key, and after the registration is corrected the Agent
// then reports KeyRecordLost.
func TestRegistrationIncompatibleSurvivesTransientObservations(t *testing.T) {
	h := newPolicyHistoryFixture(t)
	h.observe(t, metav1.ConditionTrue, "PolicyAccepted", h.generation)
	h.settle(t)

	selectorEgressForTest(t, h.f, true)
	h.observe(t, metav1.ConditionFalse, policyRegistrationIncompatibleReason, h.generation)
	h.expectHeld(t, policyRegistrationIncompatibleReason)
	for _, transient := range []*bool{&h.readFails, &h.expired} {
		*transient = true
		h.observe(t, metav1.ConditionUnknown, "PrerequisitesUnavailable", h.generation)
		h.expectHeld(t, "PrerequisitesUnavailable")
		*transient = false
		h.observe(t, metav1.ConditionFalse, policyRegistrationIncompatibleReason, h.generation)
		h.expectHeld(t, policyRegistrationIncompatibleReason)
	}

	selectorEgressForTest(t, h.f, false)
	h.observe(t, metav1.ConditionTrue, "PolicyAccepted", h.generation)
	h.expectRecovered(t)
}

// TestRegistrationIncompatibleAfterUnknownObservation covers the orders in
// which an Unknown observation replaces Accepted=True before the
// registration becomes valid but incompatible: an inventory read failure, a
// registration that cannot be used, and an acceptance that only the condition
// of an operator without the acceptance history shows.
func TestRegistrationIncompatibleAfterUnknownObservation(t *testing.T) {
	readFailure := func(_ *testing.T, h *policyHistoryFixture) { h.readFails = true }
	for _, tc := range []struct {
		name string
		// observed is false when only the seeded condition shows the
		// acceptance, as an earlier operator wrote it.
		observed bool
		unknown  func(*testing.T, *policyHistoryFixture)
		message  string
	}{
		{"inventory read failure", true, readFailure, "Current platform support inventory is unavailable"},
		{"unusable registration", true, func(t *testing.T, h *policyHistoryFixture) {
			updateContainerPlatformForTest(t, h.f.r, h.f.kube, func(document *SandboxPlatformConfiguration) {
				for backend, platform := range document.Platforms {
					platform.Topology.Gateway[0].Peer.PodSelector = map[string]string{"app.kubernetes.io/name": "cetirun-ai-gateway"}
					document.Platforms[backend] = platform
				}
			})
		}, "a selector-bound AI Gateway peer must select the protected " + sandboxpolicy.PeerLabel + " Pod label"},
		{"acceptance from an operator without the history", false, readFailure, "Current platform support inventory is unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPolicyHistoryFixture(t)
			if tc.observed {
				h.observe(t, metav1.ConditionTrue, "PolicyAccepted", h.generation)
			} else if policy := h.policy(t); policy.Status.AcceptedGeneration != 0 || !policyAcceptedBefore(policy) {
				t.Fatalf("the fixture does not start with a condition-only acceptance: %+v", policy.Status)
			}
			h.settle(t)

			tc.unknown(t, h)
			unknown := h.observe(t, metav1.ConditionUnknown, "PrerequisitesUnavailable", h.generation)
			if !strings.Contains(unknown.Message, tc.message) {
				t.Fatalf("the Unknown observation has another cause: %+v", unknown)
			}
			h.expectHeld(t, "PrerequisitesUnavailable")
			h.readFails = false
			selectorEgressForTest(t, h.f, true)
			h.observe(t, metav1.ConditionFalse, policyRegistrationIncompatibleReason, h.generation)
			h.expectHeld(t, policyRegistrationIncompatibleReason)

			selectorEgressForTest(t, h.f, false)
			h.observe(t, metav1.ConditionTrue, "PolicyAccepted", h.generation)
			h.expectRecovered(t)
		})
	}
}

// TestNewPolicyGenerationStartsWithoutAcceptanceHistory checks that a spec
// edit starts a new history. The refusal of an incompatible new generation is
// definitive, also after a transient observation, and the Agent loses its
// key. An accepted new generation then keeps its own history.
func TestNewPolicyGenerationStartsWithoutAcceptanceHistory(t *testing.T) {
	h := newPolicyHistoryFixture(t)
	previous := h.generation
	h.observe(t, metav1.ConditionTrue, "PolicyAccepted", previous)
	h.settle(t)
	selectorEgressForTest(t, h.f, true)
	h.observe(t, metav1.ConditionFalse, policyRegistrationIncompatibleReason, previous)

	policy := h.policy(t)
	policy.Spec.Network.BlockedCIDRs = append(policy.Spec.Network.BlockedCIDRs, "10.244.201.0/24")
	policy.Generation++
	if err := h.f.kube.Update(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	h.observe(t, metav1.ConditionFalse, "UnsupportedControl", previous)
	h.readFails = true
	h.observe(t, metav1.ConditionUnknown, "PrerequisitesUnavailable", previous)
	h.readFails = false
	h.observe(t, metav1.ConditionFalse, "UnsupportedControl", previous)
	for range 3 {
		_, _ = h.f.reconcile()
	}
	agent := h.f.current(t)
	resolved := apimeta.FindStatusCondition(agent.Status.Conditions, api.ConditionPolicyResolved)
	if resolved == nil || resolved.Reason != "PolicyDenied" || agent.Status.AIGateway == nil ||
		agent.Status.AIGateway.Phase != api.AIGatewayPhaseDeleting {
		t.Fatalf("the refused new generation did not deny the Agent: condition=%+v status=%+v", resolved, agent.Status.AIGateway)
	}

	selectorEgressForTest(t, h.f, false)
	h.observe(t, metav1.ConditionTrue, "PolicyAccepted", previous+1)
	selectorEgressForTest(t, h.f, true)
	h.readFails = true
	h.observe(t, metav1.ConditionUnknown, "PrerequisitesUnavailable", previous+1)
	h.readFails = false
	h.observe(t, metav1.ConditionFalse, policyRegistrationIncompatibleReason, previous+1)
}

// TestPolicyAcceptanceHistoryIsNotPermission checks that an Agent runs only
// with Accepted=True for the current generation. The acceptance history of
// that generation alone never lets it run.
func TestPolicyAcceptanceHistoryIsNotPermission(t *testing.T) {
	ctx := context.Background()
	r := &AgentReconciler{}
	policyWith := func(observed int64, condition *metav1.Condition) *api.AgentPolicy {
		policy := &api.AgentPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "example-policy", UID: "example-policy-uid", Generation: 3},
			Status:     api.AgentPolicyStatus{ObservedGeneration: observed, AcceptedGeneration: 3},
		}
		if condition != nil {
			condition.Type = api.ConditionAgentPolicyAccepted
			policy.Status.Conditions = []metav1.Condition{*condition}
		}
		return policy
	}
	for _, tc := range []struct {
		name       string
		policy     *api.AgentPolicy
		dependency bool
	}{
		{"no observation", policyWith(3, nil), true},
		{"pending generation", policyWith(2, &metav1.Condition{Status: metav1.ConditionTrue, ObservedGeneration: 2, Reason: "PolicyAccepted"}), true},
		{"earlier observation", policyWith(3, &metav1.Condition{Status: metav1.ConditionTrue, ObservedGeneration: 2, Reason: "PolicyAccepted"}), true},
		{"unavailable", policyWith(3, &metav1.Condition{Status: metav1.ConditionUnknown, ObservedGeneration: 3, Reason: "PrerequisitesUnavailable"}), true},
		{"registration incompatible", policyWith(3, &metav1.Condition{Status: metav1.ConditionFalse, ObservedGeneration: 3, Reason: policyRegistrationIncompatibleReason}), true},
		{"refused", policyWith(3, &metav1.Condition{Status: metav1.ConditionFalse, ObservedGeneration: 3, Reason: "UnsupportedControl"}), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := r.verifyPolicyAcceptance(ctx, tc.policy); err == nil || isPolicyDependencyError(err) != tc.dependency {
				t.Fatalf("the acceptance history changed the Agent decision: %v", err)
			}
		})
	}
	accepted := policyWith(3, &metav1.Condition{Status: metav1.ConditionTrue, ObservedGeneration: 3, Reason: "PolicyAccepted"})
	if err := r.verifyPolicyAcceptance(ctx, accepted); err != nil {
		t.Fatalf("Accepted=True for the current generation did not permit the Agent: %v", err)
	}
}

// TestPolicyAcceptedBeforeUsesAcceptanceHistory checks which status shows an
// earlier acceptance of the current generation.
func TestPolicyAcceptedBeforeUsesAcceptanceHistory(t *testing.T) {
	condition := func(status metav1.ConditionStatus, generation int64, reason string) []metav1.Condition {
		return []metav1.Condition{{Type: api.ConditionAgentPolicyAccepted, Status: status, ObservedGeneration: generation, Reason: reason}}
	}
	for _, tc := range []struct {
		name   string
		status api.AgentPolicyStatus
		want   bool
	}{
		{"no status", api.AgentPolicyStatus{}, false},
		{"history after an Unknown observation", api.AgentPolicyStatus{ObservedGeneration: 4, AcceptedGeneration: 4,
			Conditions: condition(metav1.ConditionUnknown, 4, "PrerequisitesUnavailable")}, true},
		{"history after a definitive refusal", api.AgentPolicyStatus{ObservedGeneration: 4, AcceptedGeneration: 4,
			Conditions: condition(metav1.ConditionFalse, 4, "UnsupportedControl")}, true},
		{"history of an earlier generation", api.AgentPolicyStatus{ObservedGeneration: 4, AcceptedGeneration: 3,
			Conditions: condition(metav1.ConditionUnknown, 4, "PrerequisitesUnavailable")}, false},
		{"condition-only acceptance", api.AgentPolicyStatus{ObservedGeneration: 4,
			Conditions: condition(metav1.ConditionTrue, 4, "PolicyAccepted")}, true},
		{"condition-only registration refusal", api.AgentPolicyStatus{ObservedGeneration: 4,
			Conditions: condition(metav1.ConditionFalse, 4, policyRegistrationIncompatibleReason)}, true},
		{"condition-only acceptance of an earlier generation", api.AgentPolicyStatus{ObservedGeneration: 3,
			Conditions: condition(metav1.ConditionTrue, 3, "PolicyAccepted")}, false},
		{"Unknown without history", api.AgentPolicyStatus{ObservedGeneration: 4,
			Conditions: condition(metav1.ConditionUnknown, 4, "PrerequisitesUnavailable")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := &api.AgentPolicy{ObjectMeta: metav1.ObjectMeta{Name: "example-policy", Generation: 4}, Status: tc.status}
			if tc.name == "no status" {
				policy.Generation = 0
			}
			if got := policyAcceptedBefore(policy); got != tc.want {
				t.Fatalf("policyAcceptedBefore = %t, want %t", got, tc.want)
			}
		})
	}
}
