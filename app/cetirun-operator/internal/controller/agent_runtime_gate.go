// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"fmt"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/agents/builtin"
)

// validateAgentRuntime prevents the existing chart path from ignoring fields
// whose runtime adapters have not been implemented yet.
func validateAgentRuntime(agent cetirunv1alpha1.HarnessSpec) error {
	if _, err := builtin.NewRegistry().Lookup(string(agent.Type)); err != nil {
		return err
	}
	if agent.RegistrySecretRef != nil {
		return fmt.Errorf("spec.agent.registrySecretRef runtime support is not implemented")
	}
	if agent.Port != nil && (*agent.Port < 1 || *agent.Port > 65535) {
		return fmt.Errorf("spec.agent.port must be between 1 and 65535")
	}
	if agent.Image != nil && *agent.Image == "" {
		return fmt.Errorf("spec.agent.image must not be empty")
	}
	if agent.Username != nil && *agent.Username != cetirunv1alpha1.DefaultAgentUsername {
		return fmt.Errorf("non-default spec.agent.username requires Agent runtime support that is not implemented")
	}
	return nil
}
