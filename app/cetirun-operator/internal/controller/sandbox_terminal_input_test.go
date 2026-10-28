// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/internal/chataccess"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
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

func TestDesiredCustomSandboxIngressesMatchHarnessAndTerminalInputs(t *testing.T) {
	r := &AgentReconciler{ChatAccess: chataccess.Config{Enabled: true, Callers: []chataccess.Caller{{
		Namespace: "cetirun-portal", PodLabels: map[string]string{"app": "portal"},
	}}}}
	terminal := map[string]any{"enabled": true, "port": 7681, "sessionSeconds": 1800, "image": map[string]any{
		"repository": "registry.example.test/cetirun-terminal", "digest": "sha256:" + strings.Repeat("a", 64),
	}}
	for _, tc := range []struct {
		name                     string
		agent                    *api.Agent
		user                     map[string]any
		wantCustom, wantTerminal bool
		wantWebUI                bool
	}{
		{name: "chat HTTP without terminal", agent: &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "chat"}, Spec: api.AgentSpec{Agent: api.HarnessSpec{Type: api.AgentTypeCustom, Port: ptr.To[int32](8080)}}}, user: map[string]any{"ttyd": map[string]any{"enabled": false}}, wantCustom: true},
		{name: "web UI", agent: &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "ui"}, Spec: api.AgentSpec{Agent: api.HarnessSpec{Type: api.AgentTypeCustom, Port: ptr.To[int32](8081), WebUI: true}}}, user: map[string]any{"ttyd": map[string]any{"enabled": false}}, wantCustom: true, wantWebUI: true},
		{name: "headless terminal", agent: &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "headless"}, Spec: api.AgentSpec{Agent: api.HarnessSpec{Type: api.AgentTypeCustom}}}, wantTerminal: true},
		{name: "native", agent: &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "native"}, Spec: api.AgentSpec{Agent: api.HarnessSpec{Type: api.AgentTypeOpenClaw, Port: ptr.To[int32](18789)}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			custom, isolated := r.desiredCustomSandboxIngresses(tc.agent, map[string]any{"ttyd": terminal}, tc.user)
			if (custom != nil) != tc.wantCustom || (isolated != nil) != tc.wantTerminal {
				t.Fatalf("ingress contracts = custom:%#v terminal:%#v", custom, isolated)
			}
			if custom != nil && (custom.WebUI != tc.wantWebUI || custom.Port != *tc.agent.Spec.Agent.Port ||
				(!custom.WebUI && len(custom.ChatCallers) != 1) || (custom.WebUI && len(custom.ChatCallers) != 0)) {
				t.Fatalf("Custom ingress = %#v", custom)
			}
			if isolated != nil && isolated.Port != 7681 {
				t.Fatalf("terminal ingress = %#v", isolated)
			}
		})
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
