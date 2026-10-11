// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
)

func TestEmptySkillsBindingRequiresNativeConsistentInputs(t *testing.T) {
	bindings := Bindings{
		NativeSandbox: true, Model: ModelBinding{Gateway: true},
		Skills:         SkillsBinding{Empty: true, MountPath: "/home/vibe/.aks/skills"},
		ConfigTemplate: `{{ dig "skillsMode" "" .Values.sandboxWorkload }}`,
	}
	agent := ResolvedAgent{Type: "openclaw"}
	config, err := RenderConfig(agent, bindings)
	if err != nil || strings.TrimSpace(config) != "empty" {
		t.Fatal("typed empty skills were not projected into the native template:", err)
	}
	for name, mutate := range map[string]func(*Bindings){
		"non-native":     func(b *Bindings) { b.NativeSandbox = false },
		"no Gateway":     func(b *Bindings) { b.Model.Gateway = false },
		"shared storage": func(b *Bindings) { b.Skills.Shared = true },
		"packages":       func(b *Bindings) { b.Skills.NPM = []string{"example-package"} },
		"other path":     func(b *Bindings) { b.Skills.MountPath = "/other-skills" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := bindings
			mutate(&changed)
			if _, err := RenderConfig(agent, changed); err == nil {
				t.Fatal("inconsistent native empty-skills binding was accepted")
			}
		})
	}
	agent.Type = "hermes"
	if _, err := RenderConfig(agent, bindings); err == nil {
		t.Fatal("empty-skills binding was accepted outside native OpenClaw")
	}
	raw, err := json.Marshal(SkillsBinding{})
	if err != nil || strings.Contains(string(raw), `"Empty"`) {
		t.Fatal("legacy runtime binding serialized shape changed:", err)
	}
}

func TestNativeConfigAssetsUseRegisteredFunctions(t *testing.T) {
	for _, harness := range []string{"openclaw", "hermes"} {
		t.Run(harness, func(t *testing.T) {
			path := filepath.Join("..", "..", "..", "cetirun-chart", "agents", harness, "config", "native.tpl")
			asset, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := template.New(harness).Funcs(nativeTemplateFunctions()).Parse(string(asset)); err != nil {
				t.Fatal(err)
			}
		})
	}
}
