// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"fmt"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

const sessionLogPlugin = "cetirun-session-log"

// sessionLogRegistered reports whether the rendered native configuration loads
// and enables the CetiRun session log plugin.
func sessionLogRegistered(t *testing.T, typ string, native map[string]any) bool {
	t.Helper()
	contains := func(values []any, item string) bool {
		return slices.ContainsFunc(values, func(value any) bool { return text(value) == item })
	}
	if typ == "hermes" {
		return contains(array(object(object(native["hermes-config.yaml"])["plugins"])["enabled"]), sessionLogPlugin)
	}
	plugins := object(object(native["openclaw.json"])["plugins"])
	loaded := contains(array(object(plugins["load"])["paths"]), "/usr/local/lib/cetirun/openclaw-plugins/"+sessionLogPlugin)
	allowed := contains(array(plugins["allow"]), sessionLogPlugin)
	enabled := object(object(plugins["entries"])[sessionLogPlugin])["enabled"] == true
	if loaded != allowed || allowed != enabled {
		t.Fatalf("OpenClaw session log registration is partial: load %t, allow %t, enabled %t", loaded, allowed, enabled)
	}
	return enabled
}

func TestSessionLogPluginFollowsTheLogger(t *testing.T) {
	for _, typ := range []string{"openclaw", "hermes"} {
		for _, enabled := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s logger %t", typ, enabled), func(t *testing.T) {
				chrt := testChart(t)
				input := sampleInput(typ, "openai", false)
				input["logger"] = map[string]any{"enabled": enabled}
				prepared, err := PrepareChart(chrt, input)
				if err != nil {
					t.Fatal(err)
				}
				for mode, values := range map[string]map[string]any{"prepared": prepared.Values, "standalone": input} {
					if registered := sessionLogRegistered(t, typ, renderSnapshot(t, chrt, values).Native); registered != enabled {
						t.Fatalf("%s %s: session log plugin registered %t with logger.enabled %t", typ, mode, registered, enabled)
					}
				}
				logger := slices.ContainsFunc(registryRenderedTemplate(t, prepared).Spec.Containers, func(container corev1.Container) bool {
					return container.Name == "logger"
				})
				if logger != enabled {
					t.Fatal("the logger sidecar and the session log plugin disagree")
				}
			})
		}
	}
}

func TestReconciledSandboxSessionLogFollowsTheLogger(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("logger %t", enabled), func(t *testing.T) {
			bundle, input := reconciledSandboxInputs(t)
			input["logger"] = map[string]any{"enabled": enabled}
			prepared, err := PrepareSandboxChart(testChart(t), input, nil, bundle)
			if err != nil {
				t.Fatal(err)
			}
			if registered := sessionLogRegistered(t, "openclaw", renderSnapshot(t, testChart(t), prepared.Values).Native); registered != enabled {
				t.Fatalf("reconciled Sandbox session log plugin registered %t with logger.enabled %t", registered, enabled)
			}
		})
	}
}

func TestSessionLogKeepsExplicitlyDisabledOpenClawPluginsOff(t *testing.T) {
	for name, plugins := range map[string]map[string]any{
		"plugins": {"enabled": false},
		"plugin":  {"entries": map[string]any{sessionLogPlugin: map[string]any{"enabled": false}}},
	} {
		t.Run(name, func(t *testing.T) {
			chrt := testChart(t)
			input := sampleInput("openclaw", "openai", false)
			// Memory refuses disabled plugins on its own; this test is about the session log.
			input["memoryContext"] = map[string]any{"enabled": false}
			native, _ := input["openclaw"].(map[string]any)
			if native == nil {
				native = map[string]any{}
			}
			config, _ := native["config"].(map[string]any)
			if config == nil {
				config = map[string]any{}
			}
			config["plugins"] = plugins
			native["config"] = config
			input["openclaw"] = native
			prepared, err := PrepareChart(chrt, input)
			if err != nil {
				t.Fatal(err)
			}
			rendered := renderSnapshot(t, chrt, prepared.Values).Native
			loaded := array(object(object(object(rendered["openclaw.json"])["plugins"])["load"])["paths"])
			if slices.ContainsFunc(loaded, func(value any) bool {
				return text(value) == "/usr/local/lib/cetirun/openclaw-plugins/"+sessionLogPlugin
			}) {
				t.Fatal("the session log loaded a plugin that the configuration turned off")
			}
		})
	}
}
