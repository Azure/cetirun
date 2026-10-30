// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"fmt"

	"github.com/Azure/CetiRun/app/cetirun-operator/internal/egresscapture"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/ingressmtls"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
)

func sandboxEgressSNIInputs(attempt *sandboxPreparationAttempt) error {
	if len(attempt.resolution.AgentPolicy.Network.BlockedFQDNs) == 0 {
		return nil
	}
	if !attempt.reconciled || attempt.agent.Spec.Agent.Port == nil ||
		*attempt.agent.Spec.Agent.Port == egresscapture.CapturePort ||
		*attempt.agent.Spec.Agent.Port == ingressmtls.AdminPort {
		return fmt.Errorf("%w: observed-SNI capture requires a nonconflicting common-executor application port", sandboxpolicy.ErrUnsupportedControl)
	}
	return nil
}
