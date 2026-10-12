// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

func TestSandboxGatewayNamespaceRequiresExactAgentOwnership(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "example", UID: types.UID("agent-uid")}}
	kube := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &AgentReconciler{Client: kube}
	if err := r.ensureSandboxGatewayNamespace(ctx, agent); err != nil {
		t.Fatal(err)
	}
	namespace := &corev1.Namespace{}
	if err := kube.Get(ctx, types.NamespacedName{Name: instanceNamespace(agent.Name)}, namespace); err != nil {
		t.Fatal(err)
	}
	if !hasAgentOwner(namespace, agent) || namespace.Labels["app.kubernetes.io/managed-by"] != "cetirun-operator" {
		t.Fatalf("namespace is not bound to the exact Agent: %+v", namespace.ObjectMeta)
	}
	if err := r.ensureSandboxGatewayNamespace(ctx, agent); err != nil {
		t.Fatalf("idempotent ownership check failed: %v", err)
	}

	foreign := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: instanceNamespace("foreign")}}
	foreignKube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(foreign).Build()
	foreignAgent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "foreign", UID: types.UID("foreign-agent-uid")}}
	if err := (&AgentReconciler{Client: foreignKube}).ensureSandboxGatewayNamespace(ctx, foreignAgent); err == nil {
		t.Fatal("same-name foreign namespace was adopted")
	}
}

func TestResolvePolicyIgnoresSandboxReadiness(t *testing.T) {
	policy := acceptedPolicy()
	team := &api.Team{ObjectMeta: metav1.ObjectMeta{Name: "team", UID: types.UID("team-uid")}, Spec: api.TeamSpec{
		AgentPolicyRef: policy.Name, SandboxRefs: []string{"unready"}, DefaultSandboxRef: "unready",
		Skills: []string{"allowed", "removed"},
	}}
	agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "agent", UID: types.UID("agent-uid")}, Spec: api.AgentSpec{
		Team: "team", SandboxRef: "unready", Agent: api.HarnessSpec{Type: api.AgentTypeOpenClaw},
		SkillOverrides: api.SkillOverrideSpec{Remove: []string{"removed"}},
		ModelAccess:    &api.ModelAccessSpec{GitHubCopilot: boolPointer(true)},
	}}
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	r := &AgentReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy, team, agent).Build()}
	resolution, err := r.resolvePolicy(context.Background(), agent)
	if err != nil {
		t.Fatalf("resolve policy: %v", err)
	}
	if resolution.policy.Name != policy.Name {
		t.Fatalf("effective policy = %q, want %q", resolution.policy.Name, policy.Name)
	}
}

func TestResolvePolicyRejectsEffectiveSkillOutsideAllowlist(t *testing.T) {
	policy := acceptedPolicy()
	team := &api.Team{ObjectMeta: metav1.ObjectMeta{Name: "team", UID: types.UID("team-uid")}, Spec: api.TeamSpec{AgentPolicyRef: policy.Name, SandboxRefs: []string{"box"}, DefaultSandboxRef: "box", Skills: []string{"denied"}}}
	agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "agent", UID: types.UID("agent-uid")}, Spec: api.AgentSpec{Team: team.Name, SandboxRef: "box", Agent: api.HarnessSpec{Type: api.AgentTypeOpenClaw}}}
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	r := &AgentReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy, team).Build()}
	if _, err := r.resolvePolicy(context.Background(), agent); err == nil {
		t.Fatal("expected policy denial for an unapproved skill")
	}
}

func TestResolvePolicyUsesCurrentTeamPolicyWithoutSandboxAuthorization(t *testing.T) {
	policy := acceptedPolicy()
	team := &api.Team{ObjectMeta: metav1.ObjectMeta{Name: "team", UID: types.UID("team-uid")}, Spec: api.TeamSpec{
		AgentPolicyRef: policy.Name, SandboxRefs: []string{"box"}, DefaultSandboxRef: "box",
	}}
	agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "agent", UID: types.UID("agent-uid")}, Spec: api.AgentSpec{
		Team: team.Name, SandboxRef: "box", Agent: api.HarnessSpec{Type: api.AgentTypeOpenClaw},
		ModelAccess: &api.ModelAccessSpec{GitHubCopilot: boolPointer(true)},
	}}

	replacement := policy.DeepCopy()
	replacement.UID = types.UID("replacement-policy-uid")
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	r := &AgentReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(replacement, team, agent).Build()}
	resolution, err := r.resolvePolicy(context.Background(), agent)
	if err != nil {
		t.Fatalf("resolve policy without Sandbox authorization: %v", err)
	}
	if resolution.policy.UID != replacement.UID {
		t.Fatalf("resolved AgentPolicy UID = %q, want current UID %q", resolution.policy.UID, replacement.UID)
	}
}

func TestResolvePolicyRejectsTerminatingReferences(t *testing.T) {
	for _, terminating := range []string{"Team", "AgentPolicy"} {
		t.Run(terminating, func(t *testing.T) {
			policy := acceptedPolicy()
			team := &api.Team{ObjectMeta: metav1.ObjectMeta{Name: "team"}, Spec: api.TeamSpec{AgentPolicyRef: policy.Name, SandboxRefs: []string{"box"}, DefaultSandboxRef: "box"}}
			if terminating == "Team" {
				now := metav1.Now()
				team.DeletionTimestamp = &now
				team.Finalizers = []string{"test"}
			} else {
				now := metav1.Now()
				policy.DeletionTimestamp = &now
				policy.Finalizers = []string{"test"}
			}
			agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "agent"}, Spec: api.AgentSpec{Team: team.Name, Agent: api.HarnessSpec{Type: api.AgentTypeOpenClaw}}}
			scheme := runtime.NewScheme()
			if err := api.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			r := &AgentReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy, team).Build()}
			if _, err := r.resolvePolicy(context.Background(), agent); err == nil || !strings.Contains(err.Error(), "terminating") {
				t.Fatalf("terminating %s was resolved: %v", terminating, err)
			}
		})
	}
}

func acceptedPolicy() *api.AgentPolicy {
	return &api.AgentPolicy{ObjectMeta: metav1.ObjectMeta{Name: "policy", UID: types.UID("policy-uid"), Generation: 1}, Spec: api.AgentPolicySpec{
		Harnesses: api.AgentPolicyHarnesses{Allowed: []api.AgentType{api.AgentTypeOpenClaw}},
		Skills:    api.AgentPolicySkills{Allowed: []string{"allowed"}},
		ModelAccess: api.AgentPolicyModelAccess{
			Copilot:       api.AgentPolicyCopilotAccess{Allowed: true},
			GatewayBudget: &api.AgentPolicyGatewayBudget{DefaultMonthlyUSD: "1", MaxMonthlyUSD: "2"},
		},
	}, Status: api.AgentPolicyStatus{ObservedGeneration: 1, Conditions: []metav1.Condition{{Type: api.ConditionAgentPolicyAccepted, Status: metav1.ConditionTrue, ObservedGeneration: 1}}}}
}

func boolPointer(value bool) *bool { return &value }
