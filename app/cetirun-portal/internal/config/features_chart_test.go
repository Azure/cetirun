// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package config

import (
	"os/exec"
	"strings"
	"testing"
)

func TestCustomChartKeepsAgentIdentitySeparate(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("Helm is required for the Portal chart rendering check")
	}
	args := []string{"template", "portal", "../../chart", "--set", "istio.host=portal.example.com",
		"--set-json", `features.allowedAgentTypes=["hermes","openclaw","custom"]`}
	output, err := exec.Command(helm, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("Custom chart render: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), `value: "hermes,openclaw,custom"`) {
		t.Fatal("Custom type was not enabled")
	}
	for _, absent := range []string{"name: AGENT_ADMISSION_RULES", "name: REQUIRE_VERIFIED_OWNER", "name: ENTRA_TENANT_ID", "name: PORTAL_OIDC_CLIENT_ID", "name: AGENT_TOOLS_BROKER_URL"} {
		if strings.Contains(string(output), absent) {
			t.Errorf("Custom enabled unrelated configuration: %s", absent)
		}
	}
	args = append(args, "--set", "features.agentIdentityEnabled=true")
	output, err = exec.Command(helm, args...).CombinedOutput()
	if err == nil || !strings.Contains(string(output), "tenantId is required") {
		t.Fatalf("Agent Identity accepted missing OIDC configuration: %v\n%s", err, output)
	}
	args = append(args, "--set", "agentIdentity.tenantId=11111111-1111-1111-1111-111111111111",
		"--set", "agentIdentity.portalClientId=33333333-3333-3333-3333-333333333333")
	output, err = exec.Command(helm, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("Agent Identity chart render: %v\n%s", err, output)
	}
	for _, expected := range []string{"name: ENTRA_TENANT_ID", "name: PORTAL_OIDC_CLIENT_ID", "name: PORTAL_ORIGIN"} {
		if !strings.Contains(string(output), expected) {
			t.Errorf("Agent Identity configuration is missing: %s", expected)
		}
	}
	args = []string{"template", "portal", "../../chart", "--set", "istio.host=portal.example.com", "--set", "features.agentToolsEnabled=true"}
	output, err = exec.Command(helm, args...).CombinedOutput()
	if err == nil || !strings.Contains(string(output), "tenantId is required") {
		t.Fatalf("Agent Tools accepted missing OIDC configuration: %v\n%s", err, output)
	}
	args = append(args,
		"--set", "agentIdentity.tenantId=11111111-1111-1111-1111-111111111111",
		"--set", "agentIdentity.portalClientId=33333333-3333-3333-3333-333333333333")
	output, err = exec.Command(helm, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("Agent Tools chart render: %v\n%s", err, output)
	}
	for _, expected := range []string{"name: FEATURE_AGENT_TOOLS_ENABLED", "name: AGENT_TOOLS_BROKER_URL", "name: ENTRA_TENANT_ID", "name: PORTAL_OIDC_CLIENT_ID"} {
		if !strings.Contains(string(output), expected) {
			t.Errorf("Agent Tools configuration is missing: %s", expected)
		}
	}
}

func TestConfigurationCoordinationChartWiring(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("Helm is required for the Portal chart rendering check")
	}
	base := []string{"template", "portal", "../../chart", "--set", "istio.host=portal.example.com"}
	partial := append(append([]string{}, base...), "--set", "configurationCoordination.name=configuration-state")
	output, err := exec.Command(helm, partial...).CombinedOutput()
	if err == nil || !strings.Contains(string(output), "requires namespace, name and uid together") {
		t.Fatalf("partial coordination authority was accepted: %v\n%s", err, output)
	}
	configured := append(append([]string{}, base...),
		"--set", "configurationCoordination.namespace=cetirun-system",
		"--set", "configurationCoordination.name=configuration-state",
		"--set", "configurationCoordination.uid=authority-uid")
	output, err = exec.Command(helm, configured...).CombinedOutput()
	if err != nil {
		t.Fatalf("coordination chart render: %v\n%s", err, output)
	}
	rendered := string(output)
	for _, expected := range []string{
		"name: CONFIGURATION_COORDINATION_NAMESPACE",
		"name: CONFIGURATION_COORDINATION_NAME",
		"name: CONFIGURATION_COORDINATION_UID",
		"name: cetirun-portal-configuration-coordination",
		`resourceNames: ["configuration-state"]`,
		`verbs: ["get", "update"]`,
	} {
		if !strings.Contains(rendered, expected) {
			t.Errorf("coordination chart output is missing %q", expected)
		}
	}
}

func TestAgentToolsChartRequiresVerifiedOwnerConfiguration(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("Helm is required for the Portal chart rendering check")
	}
	base := []string{"template", "portal", "../../chart", "--set", "istio.host=portal.example.com"}
	cases := []struct {
		name      string
		values    []string
		wantError string
	}{
		{"defaults", nil, ""},
		{"missing tenant", []string{"features.agentToolsEnabled=true"}, "tenantId is required"},
		{"missing client", []string{"features.agentToolsEnabled=true", "agentIdentity.tenantId=tenant"}, "portalClientId is required"},
		{"missing origin", []string{"features.agentToolsEnabled=true", "agentIdentity.tenantId=tenant", "agentIdentity.portalClientId=client", "istio.host="}, "istio.host is required"},
		{"tools only", []string{"features.agentToolsEnabled=true", "agentIdentity.tenantId=tenant", "agentIdentity.portalClientId=client", "istio.host=portal.example.com", "agentTools.runtimeTypes={hermes}"}, ""},
		{"empty runtimes", []string{"features.agentToolsEnabled=true", "agentIdentity.tenantId=tenant", "agentIdentity.portalClientId=client", "istio.host=portal.example.com", "agentTools.runtimeTypes=null"}, "runtimeTypes must not be empty"},
		{"empty broker", []string{"features.agentToolsEnabled=true", "agentIdentity.tenantId=tenant", "agentIdentity.portalClientId=client", "istio.host=portal.example.com", "agentTools.brokerURL="}, "brokerURL is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{}, base...)
			for _, value := range tc.values {
				args = append(args, "--set", value)
			}
			output, err := exec.Command(helm, args...).CombinedOutput()
			if tc.wantError != "" {
				if err == nil || !strings.Contains(string(output), tc.wantError) {
					t.Fatalf("expected %q, got %v: %s", tc.wantError, err, output)
				}
				return
			}
			if err != nil {
				t.Fatalf("render: %v: %s", err, output)
			}
			text := string(output)
			for _, env := range []string{"ENTRA_TENANT_ID", "PORTAL_OIDC_CLIENT_ID", "AGENT_TOOLS_BROKER_URL", "CONNECTIONS_MCP_RUNTIME_TYPES"} {
				if got := strings.Contains(text, "name: "+env); got != (tc.name == "tools only") {
					t.Errorf("unexpected %s configuration: present=%v", env, got)
				}
			}
			if strings.Contains(text, "name: AGENT_ID_BROKER_URL") {
				t.Error("Agent Tools implicitly enabled legacy authorization")
			}
		})
	}
}
