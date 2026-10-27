// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package v1alpha1

import (
	"slices"
	"testing"

	"k8s.io/utils/ptr"
)

func TestPolicyBindingsAreRequired(t *testing.T) {
	agent := apiContractCRD(t, "agents").Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
	for _, field := range []string{"team", "sandboxRef"} {
		if !slices.Contains(agent.Required, field) {
			t.Errorf("Agent creation must require binding %s", field)
		}
	}
	team := apiContractCRD(t, "teams").Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
	for _, field := range []string{"agentPolicyRef", "sandboxRefs", "defaultSandboxRef"} {
		if !slices.Contains(team.Required, field) {
			t.Errorf("Team creation must require binding %s", field)
		}
	}
}

func TestOmittedModelSourceRequestsDefaultOff(t *testing.T) {
	for _, sandboxRef := range []string{"", "standard"} {
		spec := AgentSpec{SandboxRef: sandboxRef}
		if spec.SharedModelsEnabled() || spec.GitHubCopilotEnabled() {
			t.Fatalf("omitted model requests must be off for Sandbox %q", sandboxRef)
		}
		spec.ModelAccess = &ModelAccessSpec{SharedModels: ptr.To(false), GitHubCopilot: ptr.To(true)}
		if spec.SharedModelsEnabled() || !spec.GitHubCopilotEnabled() {
			t.Fatalf("explicit model requests were not preserved for Sandbox %q", sandboxRef)
		}
	}
}
