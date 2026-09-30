// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxworkload

import (
	"fmt"

	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
)

// ControlsFromPlan only adapts the compiler's desired output. It does not repeat
// policy compilation, authorize grants, or establish enforcement readiness.
func ControlsFromPlan(plan sandboxpolicy.Plan) (Controls, error) {
	if plan.Identity.AutomountServiceAccountToken {
		return Controls{}, fmt.Errorf("compiler identity cannot use an implicit default token")
	}
	return clone(Controls{
		Revision: plan.Revision, ResolutionRevision: plan.ResolutionRevision, Binding: plan.Binding,
		DNSIPs: plan.Network.DNSIPs, IngressMode: plan.Network.IngressMode,
		ServiceAccountName: plan.Identity.ServiceAccountName, Volumes: plan.Identity.Volumes,
		VolumeMounts: plan.Identity.VolumeMounts, Environment: plan.Identity.Env, Objects: plan.Resources,
		PodLabels: map[string]string{sandboxpolicy.AgentUIDLabel: string(plan.Binding.AgentUID)},
	})
}
