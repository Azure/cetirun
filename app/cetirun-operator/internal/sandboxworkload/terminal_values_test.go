// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxworkload_test

import (
	"fmt"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/ingressmtls"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload/testfixture"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
)

func TestNativeTerminalValuesPreserveChartConfiguration(t *testing.T) {
	configured := map[string]any{
		"enabled": true, "port": 7682, "shell": "/bin/bash", "basePath": "/native/term",
		"image":          map[string]any{"repository": "registry.example.test/cetirun-terminal", "digest": "sha256:" + strings.Repeat("a", 64)},
		"sessionSeconds": 1800,
	}
	mtlsConfigured := map[string]any{
		"enabled": true, "port": 7681, "shell": "/bin/bash", "basePath": "/native/term",
		"image":          map[string]any{"repository": "registry.example.test/cetirun-terminal", "digest": "sha256:" + strings.Repeat("a", 64)},
		"sessionSeconds": 1800,
	}
	for _, tc := range []struct {
		name, wantError string
		mtls, terminal  bool
		ttyd            map[string]any
		wantPresent     bool
		wantEnabled     bool
	}{
		{name: "plain omitted"},
		{name: "plain enabled", ttyd: configured, wantPresent: true, wantEnabled: true},
		{name: "plain disabled", ttyd: map[string]any{"enabled": false, "port": 7682, "shell": "/bin/bash", "basePath": "/native/term"}, wantPresent: true},
		{name: "mTLS omitted", mtls: true, terminal: true, wantPresent: true, wantEnabled: true},
		{name: "mTLS omitted disabled", mtls: true, wantPresent: true},
		{name: "mTLS enabled", mtls: true, terminal: true, ttyd: mtlsConfigured, wantPresent: true, wantEnabled: true},
		{name: "mTLS disabled", mtls: true, ttyd: map[string]any{"enabled": false, "port": 7682, "shell": "/bin/bash", "basePath": "/native/term"}, wantPresent: true},
		{name: "mTLS mismatch", mtls: true, terminal: true, ttyd: map[string]any{"enabled": false}, wantError: "mTLS terminal selection differs"},
		{name: "mTLS disabled mismatch", mtls: true, ttyd: map[string]any{"enabled": true}, wantError: "mTLS terminal selection differs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := nativeTerminalInput(t, tc.mtls, tc.terminal)
			bundle, err := w.Build(input)
			if err != nil {
				t.Fatal(err)
			}
			values := nativeTerminalValues(input)
			if tc.ttyd != nil {
				values["ttyd"] = tc.ttyd
			}
			out, err := bundle.Apply(values)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("Apply() error = %v, want %q", err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			settings, present := out["ttyd"].(map[string]any)
			if present != tc.wantPresent {
				t.Fatalf("ttyd present = %t, want %t; values=%#v", present, tc.wantPresent, out["ttyd"])
			}
			if !present {
				return
			}
			if settings["enabled"] != tc.wantEnabled {
				t.Fatalf("ttyd.enabled = %#v, want %t", settings["enabled"], tc.wantEnabled)
			}
			if tc.ttyd != nil && (fmt.Sprint(settings["port"]) != fmt.Sprint(tc.ttyd["port"]) || settings["shell"] != tc.ttyd["shell"] || settings["basePath"] != tc.ttyd["basePath"]) {
				t.Fatalf("native ttyd chart inputs changed: %#v", settings)
			}
		})
	}
}

func TestCustomTerminalRequiresTypedPlanAndMTLSRejectsCustom(t *testing.T) {
	input := customTerminalInput()
	bundle, err := w.Build(input)
	if err != nil {
		t.Fatal(err)
	}
	values := nativeTerminalValues(input)
	values["ttyd"] = map[string]any{"enabled": true}
	if _, err := bundle.Apply(values); err == nil || !strings.Contains(err.Error(), "terminal is unavailable") {
		t.Fatalf("Custom without typed terminal enabled ttyd: %v", err)
	}

	configureMTLSInput(t, &input, true)
	if _, err := w.Build(input); err == nil || !strings.Contains(err.Error(), "mTLS requires the common executor") {
		t.Fatalf("Custom mTLS controls were accepted: %v", err)
	}
}

func nativeTerminalInput(t *testing.T, mtls, terminal bool) w.Input {
	input := testfixture.New("openclaw").Input
	input.Reconciled = true
	input.Helpers.Desktop = false
	if mtls {
		configureMTLSInput(t, &input, terminal)
	}
	return input
}

func configureMTLSInput(t *testing.T, input *w.Input, terminal bool) {
	t.Helper()
	input.Resolution.Capabilities.AuthenticatedIngress, input.Resolution.Capabilities.MutualTLS = true, true
	input.Controls.IngressMode, input.Resolution.AgentPolicy.Network.Ingress.Mode = api.AgentPolicyIngressAuthenticated, api.AgentPolicyIngressAuthenticated
	input.Controls.TrustedProxyIPs = []string{"192.0.2.10"}
	input.Resolution.AgentPolicy.Network.Ingress.RequireMTLS = true
	input.Controls.IngressMTLS = terminalMTLSPlan(*input, terminal)
	resources, err := sandboxpolicy.IngressMTLSResources(sandboxpolicy.CompileInput{
		AgentName: input.AgentName, Namespace: input.Namespace.Name, Resolution: *input.Resolution,
	}, *input.Controls.IngressMTLS, input.Controls.Revision)
	if err != nil {
		t.Fatal(err)
	}
	input.Controls.Objects = append(input.Controls.Objects, resources...)
}

func customTerminalInput() w.Input {
	input := testfixture.New("openclaw").Input
	input.AgentType, input.Reconciled = "custom", true
	input.Helpers.Doctor = false
	input.Resolution.AgentPolicy.Harnesses.Allowed = append(input.Resolution.AgentPolicy.Harnesses.Allowed, api.AgentTypeCustom)
	input.GatewayAPIBase = "https://gateway.example.test/v1"
	input.CustomAgent = &w.CustomAgent{
		Image: "registry.example.test/worker@sha256:" + strings.Repeat("b", 64), Username: "worker", Home: "/home/worker",
		Port: ptr.To[int32](8080),
		Environment: []w.CustomEnvironment{
			{Name: "HOME", Value: "/home/worker"}, {Name: "OPENAI_BASE_URL", Value: input.GatewayAPIBase},
			{Name: "OPENAI_API_KEY", CredentialSlot: "model"}, {Name: "PORT", Value: "8080"},
		},
		Ports: []w.CustomPort{{Name: "http", Number: 8080}},
		HTTP:  &w.CustomHTTP{PortName: "http", HealthPath: "/healthz", ReadinessPath: "/healthz", StartupPath: "/healthz", ChatPath: "/v1/chat/completions", ModelsPath: "/v1/models"},
	}
	input.Context = &w.Context{ObjectIdentity: w.ObjectIdentity{Name: "custom-context-example", UID: "context-uid"}, Files: map[string]string{"AGENTS.md": "AGENTS.md"}}
	input.LoopbackProbe = &w.LoopbackProbe{Image: "registry.example.test/cetirun-terminal@sha256:" + strings.Repeat("c", 64)}
	return input
}

func nativeTerminalValues(input w.Input) map[string]any {
	return map[string]any{
		"secrets":   map[string]any{"existingSecret": "runtime-secret"},
		"aiGateway": map[string]any{"enabled": true, "keySecretName": input.GatewayKey.Name, "keySecretKey": "api-key"},
	}
}

func terminalMTLSPlan(input w.Input, terminal bool) *ingressmtls.Plan {
	serviceHost := input.WorkloadName + "." + input.Namespace.Name + ".svc.cluster.local"
	ref := func(namespace, name string) ingressmtls.Reference {
		return ingressmtls.Reference{Namespace: namespace, Name: name, UID: types.UID(name + "-uid"), ResourceVersion: "1"}
	}
	return &ingressmtls.Plan{
		Binding: ingressmtls.Binding{
			AgentUID: input.Resolution.Binding.AgentUID, Namespace: input.Namespace.Name, Harness: input.AgentType,
			Port: 18789, Terminal: terminal, ServerSecret: ref(input.Namespace.Name, "agent-ingress-tls"),
			ClientSecret: ref("platform", "gateway-agent-example"), Gateway: ref("platform", "ingress"),
			ClientURI: "spiffe://example.test/gateway/" + string(input.Resolution.Binding.AgentUID),
		},
		Image:      "registry.example.test/envoy@sha256:" + strings.Repeat("d", 64),
		ServerName: "agent.example.test", ServiceHost: serviceHost,
	}
}
