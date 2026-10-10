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
