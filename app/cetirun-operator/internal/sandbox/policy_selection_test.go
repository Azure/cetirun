// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandbox

import (
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

func TestCustomTeamPolicyDoesNotSelectNativeSandbox(t *testing.T) {
	team := &api.Team{Spec: api.TeamSpec{
		AgentPolicyRef: "normal", SandboxRefs: []string{"normal"}, DefaultSandboxRef: "normal",
	}}
	custom := &api.Agent{Spec: api.AgentSpec{Agent: api.HarnessSpec{Type: api.AgentTypeCustom}, Team: "engineering"}}
	if Requested(custom, team) {
		t.Fatal("Custom Agent inherited native Sandbox intent from its Team")
	}

	builtIn := custom.DeepCopy()
	builtIn.Spec.Agent.Type = api.AgentTypeHermes
	if !Requested(builtIn, team) {
		t.Fatal("built-in Agent did not inherit its Team's native Sandbox intent")
	}
}
