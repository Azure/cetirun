// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package config

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

func TestLoadSandboxRuntimeProfiles(t *testing.T) {
	profile := SandboxRuntime{ChartVersion: "0.0.0-dev-test", Image: "registry.example.com/openclaw@sha256:" + strings.Repeat("a", 64), Port: 18789}
	raw, _ := json.Marshal(map[string]SandboxRuntime{"openclaw": profile})
	t.Setenv("SANDBOX_RUNTIME_PROFILES", string(raw))
	got, ok := Load().SandboxRuntimeFor(api.AgentTypeOpenClaw)
	if !ok || got != profile {
		t.Fatalf("approved profile missing: %+v", got)
	}
	for _, invalid := range []string{string(raw) + " trailing", `{"openclaw":{"unknown":true}}`, `{"openclaw":null}`, `{"custom":{}}`} {
		t.Setenv("SANDBOX_RUNTIME_PROFILES", invalid)
		if _, ok := Load().SandboxRuntimeFor(api.AgentTypeOpenClaw); ok {
			t.Fatal("invalid runtime configuration authorized defaults")
		}
	}
}

func TestSandboxRuntimeChartWiring(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("Helm is required for the Portal chart rendering check")
	}
	profiles := `{"openclaw":{"chartVersion":"1.2.3","image":"registry.example.com/openclaw@sha256:` + strings.Repeat("a", 64) + `","port":18789}}`
	out, err := exec.Command(helm, "template", "portal", "../../chart", "--set", "istio.host=portal.example.com", "--set-json", "sandboxRuntimeProfiles="+profiles).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "name: SANDBOX_RUNTIME_PROFILES") || !strings.Contains(string(out), `\"chartVersion\":\"1.2.3\"`) {
		t.Fatalf("runtime chart wiring failed: %v\n%s", err, out)
	}
}
