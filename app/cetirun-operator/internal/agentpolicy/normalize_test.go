// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package agentpolicy

import (
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

func TestNormalizeCanonicalizesComponentNeutralPolicy(t *testing.T) {
	policy, errs := Normalize(api.AgentPolicySpec{
		Harnesses: api.AgentPolicyHarnesses{Allowed: []api.AgentType{api.AgentTypeOpenClaw, api.AgentTypeHermes, api.AgentTypeOpenClaw}},
		Skills:    api.AgentPolicySkills{Allowed: []string{"writer", "reader", "writer"}},
		ModelAccess: api.AgentPolicyModelAccess{
			Copilot:       api.AgentPolicyCopilotAccess{Allowed: true},
			SharedModels:  api.AgentPolicySharedModelsAccess{Allowed: []string{"model-b", "model-a", "model-a"}},
			GatewayBudget: &api.AgentPolicyGatewayBudget{DefaultMonthlyUSD: "10.50", MaxMonthlyUSD: "20"},
		},
	})
	if len(errs) != 0 {
		t.Fatalf("Normalize() errors = %v", errs)
	}
	if got, want := policy.Harnesses.Allowed, []api.AgentType{api.AgentTypeHermes, api.AgentTypeOpenClaw}; !equalAgentTypes(got, want) {
		t.Fatalf("harnesses = %v, want %v", got, want)
	}
	if got, want := policy.Skills.Allowed, []string{"reader", "writer"}; !equalStrings(got, want) {
		t.Fatalf("skills = %v, want %v", got, want)
	}
	if got, want := policy.ModelAccess.SharedModels.Allowed, []string{"model-a", "model-b"}; !equalStrings(got, want) {
		t.Fatalf("models = %v, want %v", got, want)
	}
}

func TestNormalizeRequiresBudgetOnlyForModelAccess(t *testing.T) {
	_, errs := Normalize(api.AgentPolicySpec{Harnesses: api.AgentPolicyHarnesses{Allowed: []api.AgentType{api.AgentTypeOpenClaw}}})
	if len(errs) != 0 {
		t.Fatalf("policy without models errors = %v", errs)
	}
	_, errs = Normalize(api.AgentPolicySpec{Harnesses: api.AgentPolicyHarnesses{Allowed: []api.AgentType{api.AgentTypeOpenClaw}}, ModelAccess: api.AgentPolicyModelAccess{Copilot: api.AgentPolicyCopilotAccess{Allowed: true}}})
	if len(errs) == 0 {
		t.Fatal("model access without budget was accepted")
	}
}

func equalAgentTypes(left, right []api.AgentType) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
