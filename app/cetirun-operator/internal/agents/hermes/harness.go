// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package hermes

import "github.com/Azure/CetiRun/app/cetirun-operator/internal/agents"

type Harness struct{}

func (Harness) Describe() agents.Descriptor {
	return agents.Descriptor{
		Type: "hermes", ConfigFile: "hermes-config.yaml", PortName: "api",
		DefaultPort: 8642, DashboardPort: 9119, HealthPath: "/health", StartupPath: "/health",
		NativeSecretKey: "API_SERVER_KEY", EnvironmentFile: "hermes.env",
	}
}

func (h Harness) Validate(agent agents.ResolvedAgent, bindings agents.Bindings) error {
	return agents.Validate(agent, bindings, h.Describe())
}

func (h Harness) Build(agent agents.ResolvedAgent, bindings agents.Bindings) (agents.Contribution, error) {
	return agents.Build(agent, bindings, h.Describe())
}
