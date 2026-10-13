// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxbinding"
)

func TestResolvePolicyIgnoresSandboxReadiness(t *testing.T) {
	policy, team, box := acceptedPolicyFixture()
	team.Spec.Skills = []string{"allowed", "removed"}
	agent := authorizedAgent(t, team, policy, box)
	agent.Spec.SkillOverrides.Remove = []string{"removed"}
	agent.Spec.ModelAccess = &api.ModelAccessSpec{GitHubCopilot: boolPointer(true)}

	resolution, err := policyReconciler(t, policy, team).resolvePolicy(context.Background(), agent)
	if err != nil {
		t.Fatalf("resolve policy: %v", err)
	}
	if resolution.policy.Name != policy.Name {
		t.Fatalf("effective policy = %q, want %q", resolution.policy.Name, policy.Name)
	}
}

func TestResolvePolicyRejectsEffectiveSkillOutsideAllowlist(t *testing.T) {
	policy, team, box := acceptedPolicyFixture()
	team.Spec.Skills = []string{"denied"}
	agent := authorizedAgent(t, team, policy, box)

	if _, err := policyReconciler(t, policy, team).resolvePolicy(context.Background(), agent); err == nil {
		t.Fatal("expected policy denial for an unapproved skill")
	}
}

func TestResolvePolicyRequiresProtectedAuthorization(t *testing.T) {
	policy, team, _ := acceptedPolicyFixture()
	agent := &api.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", UID: types.UID("agent-uid")},
		Spec: api.AgentSpec{
			Team: team.Name, SandboxRef: "box", Agent: api.HarnessSpec{Type: api.AgentTypeOpenClaw},
		},
	}

	if _, err := policyReconciler(t, policy, team).resolvePolicy(context.Background(), agent); err == nil || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("Agent without protected authorization was resolved: %v", err)
	}
}

func TestResolvePolicyRejectsPolicyUIDReplacement(t *testing.T) {
	policy, team, box := acceptedPolicyFixture()
	agent := authorizedAgent(t, team, policy, box)
	replacement := policy.DeepCopy()
	replacement.UID = types.UID("replacement-policy-uid")

	if _, err := policyReconciler(t, replacement, team).resolvePolicy(context.Background(), agent); err == nil || !strings.Contains(err.Error(), "protected authorization") {
		t.Fatalf("replacement AgentPolicy was resolved without reauthorization: %v", err)
	}
}

func TestResolvePolicyRejectsTeamUIDReplacement(t *testing.T) {
	policy, team, box := acceptedPolicyFixture()
	agent := authorizedAgent(t, team, policy, box)
	replacement := team.DeepCopy()
	replacement.UID = types.UID("replacement-team-uid")

	if _, err := policyReconciler(t, policy, replacement).resolvePolicy(context.Background(), agent); err == nil || !strings.Contains(err.Error(), "protected authorization") {
		t.Fatalf("replacement Team was resolved without reauthorization: %v", err)
	}
}

func TestResolvePolicyEnforcesAgentIdentity(t *testing.T) {
	tests := []struct {
		name     string
		identity api.AgentPolicyAgentIdentity
		request  *api.AgentIdentitySpec
		want     string
	}{
		{
			name: "required but omitted", identity: api.AgentPolicyAgentIdentity{Required: true, Allowed: true, CredentialServices: []string{"azure-resource-manager"}},
			want: "requires Agent Identity",
		},
		{
			name: "requested but denied", identity: api.AgentPolicyAgentIdentity{}, request: &api.AgentIdentitySpec{Enabled: true},
			want: "not permitted",
		},
		{
			name: "requested with supported service", identity: api.AgentPolicyAgentIdentity{Allowed: true, CredentialServices: []string{"azure-resource-manager"}},
			request: &api.AgentIdentitySpec{Enabled: true},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy, team, box := acceptedPolicyFixture()
			policy.Spec.AgentIdentity = test.identity
			agent := authorizedAgent(t, team, policy, box)
			agent.Spec.AgentIdentity = test.request
			_, err := policyReconciler(t, policy, team).resolvePolicy(context.Background(), agent)
			if test.want == "" && err != nil {
				t.Fatalf("resolve policy: %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("resolve policy error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestResolvePolicyRejectsTerminatingReferences(t *testing.T) {
	for _, terminating := range []string{"Team", "AgentPolicy"} {
		t.Run(terminating, func(t *testing.T) {
			policy, team, box := acceptedPolicyFixture()
			agent := authorizedAgent(t, team, policy, box)
			now := metav1.Now()
			if terminating == "Team" {
				team.DeletionTimestamp = &now
				team.Finalizers = []string{"test"}
			} else {
				policy.DeletionTimestamp = &now
				policy.Finalizers = []string{"test"}
			}
			if _, err := policyReconciler(t, policy, team).resolvePolicy(context.Background(), agent); err == nil || !strings.Contains(err.Error(), "terminating") {
				t.Fatalf("terminating %s was resolved: %v", terminating, err)
			}
		})
	}
}

func policyReconciler(t *testing.T, objects ...runtime.Object) *AgentReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return &AgentReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()}
}

func acceptedPolicyFixture() (*api.AgentPolicy, *api.Team, *api.Sandbox) {
	policy := &api.AgentPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", UID: types.UID("policy-uid"), Generation: 1},
		Spec: api.AgentPolicySpec{
			Harnesses: api.AgentPolicyHarnesses{Allowed: []api.AgentType{api.AgentTypeOpenClaw}},
			Skills:    api.AgentPolicySkills{Allowed: []string{"allowed"}},
			ModelAccess: api.AgentPolicyModelAccess{
				Copilot:       api.AgentPolicyCopilotAccess{Allowed: true},
				GatewayBudget: &api.AgentPolicyGatewayBudget{DefaultMonthlyUSD: "1", MaxMonthlyUSD: "2"},
			},
		},
		Status: api.AgentPolicyStatus{
			ObservedGeneration: 1,
			Conditions: []metav1.Condition{{
				Type: api.ConditionAgentPolicyAccepted, Status: metav1.ConditionTrue, ObservedGeneration: 1,
			}},
		},
	}
	team := &api.Team{
		ObjectMeta: metav1.ObjectMeta{Name: "team", UID: types.UID("team-uid"), Generation: 1},
		Spec: api.TeamSpec{
			AgentPolicyRef: policy.Name, SandboxRefs: []string{"box"}, DefaultSandboxRef: "box",
		},
		Status: api.TeamStatus{
			ObservedGeneration: 1,
			Conditions: []metav1.Condition{{
				Type: api.ConditionTeamAccepted, Status: metav1.ConditionTrue, ObservedGeneration: 1,
			}},
		},
	}
	box := &api.Sandbox{ObjectMeta: metav1.ObjectMeta{Name: "box", UID: types.UID("sandbox-uid")}}
	return policy, team, box
}

func authorizedAgent(t *testing.T, team *api.Team, policy *api.AgentPolicy, box *api.Sandbox) *api.Agent {
	t.Helper()
	agent := &api.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", UID: types.UID("agent-uid"), Annotations: map[string]string{}},
		Spec: api.AgentSpec{
			Team: team.Name, SandboxRef: box.Name, Agent: api.HarnessSpec{Type: api.AgentTypeOpenClaw},
		},
	}
	snapshot, err := sandboxbinding.Snapshot(agent, team, sandbox.Resources{
		Sandboxes: map[string]*api.Sandbox{box.Name: box}, AgentPolicies: map[string]*api.AgentPolicy{policy.Name: policy},
	})
	if err != nil {
		t.Fatalf("create protected authorization: %v", err)
	}
	agent.Annotations[sandboxbinding.AuthorizationAnnotation] = snapshot
	return agent
}

func boolPointer(value bool) *bool { return &value }
