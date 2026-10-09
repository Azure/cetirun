// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
	"k8s.io/utils/ptr"
)

// sandboxPolicyCompileInput keeps the desired-policy, preparation and running
// execution revisions on the same complete set of workload inputs. A field
// omitted by only one caller would otherwise cause a successfully applied
// Sandbox to be treated as a stale policy projection.
func sandboxPolicyCompileInput(agent *api.Agent, resolution sandbox.Resolution, namespace string,
	platform sandboxpolicy.Platform,
) sandboxpolicy.CompileInput {
	input := sandboxpolicy.CompileInput{Resolution: resolution, Namespace: namespace, Platform: platform}
	if agent != nil {
		input.AgentName = agent.Name
		input.WorkloadProbePort = ptr.Deref(agent.Spec.Agent.Port, 0)
	}
	return input
}
