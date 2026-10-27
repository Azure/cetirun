// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestSandboxTerminalInputRequiresPlatformPinnedImage(t *testing.T) {
	platform := map[string]any{"ttyd": map[string]any{
		"enabled": true, "port": 7681, "sessionSeconds": 1800,
		"image": map[string]any{"repository": "registry.example.test/cetirun-terminal", "digest": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
	}}
	terminal := sandboxTerminalInput(platform, nil, "cetirun-example")
	if terminal == nil || terminal.Image != "registry.example.test/cetirun-terminal@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" ||
		terminal.Port != 7681 || terminal.SessionSeconds != 1800 || terminal.ServiceAccountName != "cetirun-example-terminal" {
		t.Fatalf("platform terminal contract = %#v", terminal)
	}
	delete(platform["ttyd"].(map[string]any), "port")
	delete(platform["ttyd"].(map[string]any), "sessionSeconds")
	terminal = sandboxTerminalInput(platform, nil, "cetirun-example")
	if terminal == nil || terminal.Port != 7681 || terminal.SessionSeconds != 1800 {
		t.Fatalf("platform terminal defaults = %#v", terminal)
	}
	if got := sandboxTerminalInput(platform, map[string]any{"ttyd": map[string]any{"enabled": false}}, "cetirun-example"); got != nil {
		t.Fatalf("Agent terminal disable was ignored: %#v", got)
	}
	image := platform["ttyd"].(map[string]any)["image"].(map[string]any)
	image["digest"] = ""
	if got := sandboxTerminalInput(platform, nil, "cetirun-example"); got != nil {
		t.Fatalf("mutable platform image was accepted: %#v", got)
	}
}

func TestReconciledBuiltinSandboxIgnoresCustomTerminalPlatformInput(t *testing.T) {
	terminal := map[string]any{
		"enabled": true, "port": 7681, "sessionSeconds": 1800,
		"image": map[string]any{
			"repository": "registry.example.test/cetirun-terminal",
			"digest":     "sha256:" + strings.Repeat("a", 64),
		},
	}
	for _, harness := range []string{"openclaw", "hermes"} {
		t.Run(harness, func(t *testing.T) {
			f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, harness, true, false,
				func(r *AgentReconciler, _ client.Client) {
					r.CetiRunHelmValues = map[string]any{"ttyd": terminal}
				},
			)
			expected := reconciledExpectationForTest(t, f)
			if expected.Terminal != nil {
				t.Fatalf("%s common Sandbox retained the Custom terminal target: %#v", harness, expected.Terminal)
			}
			if f.workload(t).Name != instanceServiceName(f.agent.Name) {
				t.Fatalf("%s common Sandbox did not start its primary workload", harness)
			}
		})
	}
}
