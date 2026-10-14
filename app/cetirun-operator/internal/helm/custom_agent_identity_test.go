// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"strings"
	"testing"

	"github.com/Azure/CetiRun/app/cetirun-operator/internal/runtimebindings"
)

func customIdentityInput() map[string]any {
	input := customInput(8080)
	b := input["customBindings"].(runtimebindings.Custom)
	b.AgentIdentity = &runtimebindings.AgentIdentity{BrokerURL: "http://cetirun-agent-broker.credentials.svc.cluster.local:8080"}
	input["customBindings"] = b
	return input
}

func TestCustomAgentIdentityUsesSharedToolsAndBoundBroker(t *testing.T) {
	for _, memory := range []bool{false, true} {
		input := customIdentityInput()
		if !memory {
			input["memoryOptions"] = MemoryOptions{Enabled: false}
		}
		prepared, err := PrepareChart(testChart(t), input)
		if err != nil {
			t.Fatal(err)
		}
		if err := prepared.ValidateCustom("cetirun-example", "openclaw-example"); err != nil {
			t.Fatal(err)
		}
		for _, expected := range []string{"CETIRUN_AGENT_BROKER_URL", "/usr/local/bin/cetirun-agent", "skills/azure/SKILL.md", "agent-identity-tools"} {
			if !strings.Contains(string(prepared.manifest), expected) {
				t.Fatalf("missing %s", expected)
			}
		}
		object(prepared.Values["agentIdentity"])["brokerURL"] = "http://other.credentials.svc.cluster.local:8080"
		if err := prepared.ValidateCustom("cetirun-example", "openclaw-example"); err == nil {
			t.Fatal("a changed Broker endpoint was accepted")
		}
	}
}

func TestCustomAgentIdentityRejectsUnsupportedInputs(t *testing.T) {
	for _, name := range []string{"old-chart", "external-broker", "no-network", "no-sidecar", "context-conflict", "raw-override"} {
		t.Run(name, func(t *testing.T) {
			input, chrt := customIdentityInput(), testChart(t)
			b := input["customBindings"].(runtimebindings.Custom)
			switch name {
			case "old-chart":
				delete(chrt.Metadata.Annotations, "cetirun.microsoft.com/custom-agent-identity")
			case "external-broker":
				b.AgentIdentity.BrokerURL = "https://broker.example.invalid"
			case "no-network":
				input["networkPolicy"] = map[string]any{"enabled": false}
			case "no-sidecar":
				input["sidecarScope"] = map[string]any{"enabled": false}
			case "context-conflict":
				b.Context.Files["owned-azure"] = "skills/azure/SKILL.md"
			case "raw-override":
				input["agentIdentity"] = map[string]any{"enabled": true}
			}
			input["customBindings"] = b
			if _, err := PrepareChart(chrt, input); err == nil {
				t.Fatal("unapproved Agent Identity configuration was accepted")
			}
		})
	}
}

func TestCustomAgentIdentityChangesWorkloadAndPreservesOptOut(t *testing.T) {
	plain, err := PrepareChart(testChart(t), customInput(8080))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain.manifest), "CETIRUN_AGENT_BROKER_URL") || strings.Contains(string(plain.manifest), "agent-identity-tools") {
		t.Fatal("identity tools appeared without an approved binding")
	}
	identity, err := PrepareChart(testChart(t), customIdentityInput())
	if err != nil {
		t.Fatal(err)
	}
	if plain.WorkloadHash == identity.WorkloadHash {
		t.Fatal("identity activation did not invalidate the workload")
	}
}
