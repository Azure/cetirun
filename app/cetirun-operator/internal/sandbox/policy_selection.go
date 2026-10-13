// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandbox

import (
	"slices"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
)

const (
	HoldFinalizer        = "cetirun.microsoft.com/sandbox-hold"
	PreparationFinalizer = "cetirun.microsoft.com/sandbox-preparation"
	BootstrapFinalizer   = "cetirun.microsoft.com/sandbox-bootstrap"
)

// Requested detects native Sandbox intent, not a valid or enforced binding.
// A Custom Agent does not acquire Sandbox intent merely because its optional
// Team has a policy assignment. Durable Sandbox state is still recognized so
// that an invalid or interrupted transition remains fail-closed.
func Requested(agent *api.Agent, team *api.Team) bool {
	if agent != nil && (slices.Contains(agent.Finalizers, HoldFinalizer) ||
		slices.Contains(agent.Finalizers, PreparationFinalizer) || slices.Contains(agent.Finalizers, BootstrapFinalizer) ||
		agent.Spec.SandboxRef != "" ||
		agent.Status.Sandbox != nil || meta.FindStatusCondition(agent.Status.Conditions, api.ConditionSandboxReady) != nil) {
		return true
	}
	if agent != nil && agent.Spec.Agent.Type == api.AgentTypeCustom {
		return false
	}
	return TeamPolicyConfigured(team)
}

func TeamPolicyConfigured(team *api.Team) bool {
	return team != nil && (team.Spec.AgentPolicyRef != "" ||
		len(team.Spec.SandboxRefs) != 0 || team.Spec.DefaultSandboxRef != "")
}
