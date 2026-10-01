// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

func aiGatewayInput(typ, provider string) map[string]any {
	input := sampleInput(typ, provider, false)
	input["aiGateway"] = map[string]any{"enabled": true, "keySecretName": "ai-gateway-virtual-key", "keySecretKey": "api-key"}
	input["networkPolicy"] = map[string]any{"gatewayNamespace": "cetirun-system", "gatewayPort": 4000}
	return input
}

func renderAIGateway(t *testing.T, input map[string]any) (map[string]string, error) {
	t.Helper()
	chrt, err := loader.Load("../../../cetirun-chart")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareChart(chrt, input)
	if err != nil {
		return nil, err
	}
	caps := chartutil.DefaultCapabilities.Copy()
	caps.APIVersions = append(caps.APIVersions, "networking.istio.io/v1/Sidecar")
	values, err := chartutil.ToRenderValues(chrt, prepared.Values, chartutil.ReleaseOptions{
		Name: "openclaw-example", Namespace: "cetirun-example", IsInstall: true, Revision: 1,
	}, caps)
	if err != nil {
		t.Fatal(err)
	}
	return engine.Render(chrt, values)
}

func openAIKeyEnv(t *testing.T, rendered map[string]string) *corev1.EnvVar {
	t.Helper()
	sts := &appsv1.StatefulSet{}
	if err := yaml.Unmarshal([]byte(rendered["cetirun/templates/statefulset.yaml"]), sts); err != nil {
		t.Fatal(err)
	}
	for _, container := range sts.Spec.Template.Spec.Containers {
		for i := range container.Env {
			if container.Env[i].Name == "OPENAI_API_KEY" {
				return &container.Env[i]
			}
		}
	}
	return nil
}

func TestAIGatewayChartWiring(t *testing.T) {
	rendered, err := renderAIGateway(t, aiGatewayInput("openclaw", "openai"))
	if err != nil {
		t.Fatal(err)
	}
	env := openAIKeyEnv(t, rendered)
	if env == nil || env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
		t.Fatal("OPENAI_API_KEY is not read from a Secret")
	}
	ref := env.ValueFrom.SecretKeyRef
	if ref.Name != "ai-gateway-virtual-key" || ref.Key != "api-key" || (ref.Optional != nil && *ref.Optional) {
		t.Fatalf("OPENAI_API_KEY must come from the required virtual key Secret, got %+v", ref)
	}
	policy := rendered["cetirun/templates/networkpolicy.yaml"]
	for _, want := range []string{"kubernetes.io/metadata.name: cetirun-system", "app.kubernetes.io/name: cetirun-ai-gateway",
		"app.kubernetes.io/component: gateway", "port: 4000"} {
		if !strings.Contains(policy, want) {
			t.Errorf("NetworkPolicy lacks %q", want)
		}
	}
	if !strings.Contains(rendered["cetirun/templates/sidecar.yaml"], `"cetirun-system/*"`) {
		t.Error("Sidecar egress scope lacks the Gateway namespace")
	}
}

func TestAIGatewayChartDisabledKeepsProviderKey(t *testing.T) {
	rendered, err := renderAIGateway(t, sampleInput("openclaw", "openai", false))
	if err != nil {
		t.Fatal(err)
	}
	env := openAIKeyEnv(t, rendered)
	if env == nil || env.ValueFrom.SecretKeyRef.Key != "MODEL_API_KEY" || env.ValueFrom.SecretKeyRef.Name != "test-secret" {
		t.Fatalf("disabled Gateway must keep MODEL_API_KEY from the existing Secret, got %+v", env)
	}
	if strings.Contains(rendered["cetirun/templates/networkpolicy.yaml"], "cetirun-ai-gateway") ||
		strings.Contains(rendered["cetirun/templates/sidecar.yaml"], "cetirun-system/*") {
		t.Fatal("disabled Gateway must not open egress")
	}
}

func TestAIGatewayChartRejectsUnsupportedWiring(t *testing.T) {
	cases := map[string]struct {
		mutate func(map[string]any)
		want   string
	}{
		"hermes": {func(in map[string]any) {
			in["agentType"] = "hermes"
			in["agentRequest"] = map[string]any{"type": "hermes"}
		}, "supported only for OpenClaw"},
		"copilot provider": {func(in map[string]any) { in["secrets"].(map[string]any)["provider"] = "github_copilot" }, "secrets.provider=openai"},
		"missing Secret":   {func(in map[string]any) { in["aiGateway"].(map[string]any)["keySecretName"] = "" }, "keySecretName"},
		"no NetworkPolicy": {func(in map[string]any) { in["networkPolicy"].(map[string]any)["enabled"] = false }, "networkPolicy.enabled"},
		"reserved extraEnv": {func(in map[string]any) {
			in["extraEnv"] = []any{map[string]any{"name": "OPENAI_BASE_URL", "value": "https://example.com"}}
		}, "reserved"},
		"no Sidecar scoping": {func(in map[string]any) { in["sidecarScope"] = map[string]any{"enabled": false} }, "sidecarScope.enabled"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			input := aiGatewayInput("openclaw", "openai")
			tc.mutate(input)
			_, err := renderAIGateway(t, input)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want rejection mentioning %q, got %v", tc.want, err)
			}
		})
	}
}

func TestAIGatewayChartBridgesResponsesOnlyCopilotModels(t *testing.T) {
	input := aiGatewayInput("openclaw", "openai")
	input["aiGateway"].(map[string]any)["copilotProxy"] = map[string]any{
		"enabled": true, "image": "registry.example.com/cetirun-ai-gateway:1.0.0",
		"masterKeySecretName": "ai-gateway-copilot-proxy", "tokenSecretName": "github-copilot-token",
		"responsesModels": []any{"gpt-5.5"},
	}
	rendered, err := renderAIGateway(t, input)
	if err != nil {
		t.Fatal(err)
	}
	proxy := rendered["cetirun/templates/copilot-proxy.yaml"]
	for _, want := range []string{
		`- model_name: "gpt-5.5"`, `model: "github_copilot/gpt-5.5"`, "mode: responses",
		`- model_name: "*"`, `model: "github_copilot/*"`,
	} {
		if !strings.Contains(proxy, want) {
			t.Errorf("proxy configuration lacks %q", want)
		}
	}
	if strings.Index(proxy, `"gpt-5.5"`) > strings.Index(proxy, `model_name: "*"`) {
		t.Error("the named model must come before the wildcard")
	}
	input["aiGateway"].(map[string]any)["copilotProxy"].(map[string]any)["responsesModels"] = []any{"../etc"}
	if _, err := renderAIGateway(t, input); err == nil {
		t.Fatal("a model ID that is not a Copilot model ID must be rejected")
	}
}
