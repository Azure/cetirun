// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestPolicyHoldShowsWhyInReadyAndSandboxReady reproduces #556 item 1: when a
// policy holds a common-executor Agent, Ready and SandboxReady show the
// PolicyResolved message after the generic hold text. The reason stays
// SandboxHeld. The message is already on the Agent, so this shows nothing new.
func TestPolicyHoldShowsWhyInReadyAndSandboxReady(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		hold   func(*testing.T, *nativeSandboxFixture, *api.Team)
		reason string
	}{
		{"unavailable policy observation", func(t *testing.T, f *nativeSandboxFixture, team *api.Team) {
			policy := &api.AgentPolicy{}
			if err := f.kube.Get(ctx, client.ObjectKey{Name: team.Spec.AgentPolicyRef}, policy); err != nil {
				t.Fatal(err)
			}
			policy.Status.ObservedGeneration = policy.Generation
			apimeta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
				Type: api.ConditionAgentPolicyAccepted, Status: metav1.ConditionUnknown, ObservedGeneration: policy.Generation,
				Reason: "PrerequisitesUnavailable", Message: "Current platform support inventory is unavailable: a fixed reason.",
			})
			if err := f.kube.Status().Update(ctx, policy); err != nil {
				t.Fatal(err)
			}
		}, "DependencyUnavailable"},
		{"denied Sandbox", func(t *testing.T, f *nativeSandboxFixture, team *api.Team) {
			team.Spec.SandboxRefs = nil
			if err := f.kube.Update(ctx, team); err != nil {
				t.Fatal(err)
			}
		}, "PolicyDenied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := readyPolicyRateLimitFixture(t, "openclaw", true)
			team := &api.Team{}
			if err := f.kube.Get(ctx, client.ObjectKey{Name: f.current(t).Spec.Team}, team); err != nil {
				t.Fatal(err)
			}
			tc.hold(t, f, team)
			for range 3 {
				_, _ = f.reconcile()
			}
			agent := f.current(t)
			resolved := apimeta.FindStatusCondition(agent.Status.Conditions, api.ConditionPolicyResolved)
			if agent.Status.Phase == api.PhaseReady || resolved == nil || resolved.Status != metav1.ConditionFalse ||
				resolved.Reason != tc.reason || resolved.Message == "" {
				t.Fatalf("the policy did not hold the Agent: phase=%s condition=%+v", agent.Status.Phase, resolved)
			}
			want := reconciledSandboxHeldMessage + ": " + resolved.Message
			for _, kind := range []string{api.ConditionReady, api.ConditionSandboxReady} {
				condition := apimeta.FindStatusCondition(agent.Status.Conditions, kind)
				if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != "SandboxHeld" ||
					condition.Message != want {
					t.Fatalf("%s does not show why the policy holds the Agent: %+v", kind, condition)
				}
			}

			// A hold for another cause keeps the generic text, also while the
			// policy does not resolve.
			if _, err := f.r.holdReconciledSandbox(ctx, agent); err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{api.ConditionReady, api.ConditionSandboxReady} {
				if condition := apimeta.FindStatusCondition(f.current(t).Status.Conditions, kind); condition == nil ||
					condition.Reason != "SandboxHeld" || condition.Message != reconciledSandboxHeldMessage {
					t.Fatalf("another hold added the policy text to %s: %+v", kind, condition)
				}
			}
		})
	}
}

// TestPolicyHoldCauseIsCurrentAndBounded checks that only a current
// PolicyResolved failure is repeated, and that its text is bounded.
func TestPolicyHoldCauseIsCurrentAndBounded(t *testing.T) {
	agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Generation: 3}}
	agent.Status.Policy = &api.PolicyStatus{ObservedGeneration: 3, Phase: "Denied"}
	resolved := func(status metav1.ConditionStatus, message string) {
		agent.Status.Conditions = []metav1.Condition{{Type: api.ConditionPolicyResolved, Status: status, Reason: "PolicyDenied", Message: message}}
	}
	resolved(metav1.ConditionFalse, "skill \"example\" is not allowed by policy")
	if got := policyHoldCause(agent); got != "skill \"example\" is not allowed by policy" {
		t.Fatalf("the current policy failure is not repeated: %q", got)
	}
	resolved(metav1.ConditionFalse, strings.Repeat("M", 40000))
	if got := policyHoldCause(agent); utf8.RuneCountInString(got) != maxConditionSummary {
		t.Fatalf("the repeated policy text is not bounded: %d characters", utf8.RuneCountInString(got))
	}
	agent.Status.Policy.ObservedGeneration = 2
	if got := policyHoldCause(agent); got != "" {
		t.Fatalf("a policy failure of an earlier generation was repeated: %q", got)
	}
	agent.Status.Policy.ObservedGeneration = 3
	resolved(metav1.ConditionTrue, "The Team selection, requests, Sandbox, and effective AgentPolicy are valid")
	if got := policyHoldCause(agent); got != "" {
		t.Fatalf("a resolved policy was repeated as a cause: %q", got)
	}
	agent.Status.Policy, agent.Status.Conditions = nil, nil
	if got := policyHoldCause(agent); got != "" {
		t.Fatalf("an Agent without a policy observation has a cause: %q", got)
	}
}
