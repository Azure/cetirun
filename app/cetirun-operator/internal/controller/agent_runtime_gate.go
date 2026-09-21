// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"fmt"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

// validateAgentRuntime prevents the existing chart path from ignoring fields
// whose runtime adapters have not been implemented yet.
func validateAgentRuntime(agent cetirunv1alpha1.HarnessSpec) error {
	switch agent.Type {
	case cetirunv1alpha1.AgentTypeOpenClaw, cetirunv1alpha1.AgentTypeHermes:
	case cetirunv1alpha1.AgentTypeCustom:
		return fmt.Errorf("custom Agent runtime support is not implemented")
	default:
		return fmt.Errorf("spec.agent.type must explicitly select a supported Agent type")
	}
	if agent.Image != nil || agent.Port != nil || agent.RegistrySecretRef != nil {
		return fmt.Errorf("spec.agent image, port and registrySecretRef require Agent runtime support that is not implemented")
	}
	if agent.Username != nil && *agent.Username != cetirunv1alpha1.DefaultAgentUsername {
		return fmt.Errorf("non-default spec.agent.username requires Agent runtime support that is not implemented")
	}
	return nil
}
