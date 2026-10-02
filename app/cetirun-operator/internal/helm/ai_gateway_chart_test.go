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

// initGatewayMarker returns the value of CETIRUN_AI_GATEWAY on the OpenClaw
// init container, which tells init that the Gateway owns the model list.
func initGatewayMarker(t *testing.T, rendered map[string]string) (string, bool) {
	t.Helper()
	sts := &appsv1.StatefulSet{}
	if err := yaml.Unmarshal([]byte(rendered["cetirun/templates/statefulset.yaml"]), sts); err != nil {
		t.Fatal(err)
	}
	for _, container := range sts.Spec.Template.Spec.InitContainers {
		if container.Name != "init-home-data" {
			continue
		}
		for _, env := range container.Env {
			if env.Name == "CETIRUN_AI_GATEWAY" {
				return env.Value, true
			}
		}
		return "", false
	}
	t.Fatal("no init-home-data init container")
	return "", false
}

func TestAIGatewayChartWiring(t *testing.T) {
	rendered, err := renderAIGateway(t, aiGatewayInput("openclaw", "openai"))
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := initGatewayMarker(t, rendered); !ok || value != "true" {
		t.Fatalf("init must know the Gateway owns the model list, got %q", value)
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
	// An openai provider with its own endpoint keeps its saved model list.
	if value, ok := initGatewayMarker(t, rendered); ok {
		t.Fatalf("init must not treat the model list as the Gateway's, got CETIRUN_AI_GATEWAY=%q", value)
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

// proxyTemplateAnnotations returns the annotations of the Copilot proxy's Pod
// template.
func proxyTemplateAnnotations(t *testing.T, rendered map[string]string) map[string]string {
	t.Helper()
	for _, document := range strings.Split(rendered["cetirun/templates/copilot-proxy.yaml"], "\n---") {
		if !strings.Contains(document, "kind: Deployment") {
			continue
		}
		deployment := &appsv1.Deployment{}
		if err := yaml.Unmarshal([]byte(document), deployment); err != nil {
			t.Fatal(err)
		}
		return deployment.Spec.Template.Annotations
	}
	t.Fatal("no proxy Deployment rendered")
	return nil
}

func TestAIGatewayChartRestartsTheProxyForANewMasterKey(t *testing.T) {
	render := func(keyID any) (map[string]string, error) {
		input := aiGatewayInput("openclaw", "openai")
		proxy := map[string]any{
			"enabled": true, "image": "registry.example.com/cetirun-ai-gateway:1.0.0",
			"masterKeySecretName": "ai-gateway-copilot-proxy", "tokenSecretName": "github-copilot-token",
		}
		if keyID != nil {
			proxy["masterKeyId"] = keyID
		}
		input["aiGateway"].(map[string]any)["copilotProxy"] = proxy
		return renderAIGateway(t, input)
	}
	rendered, err := render("0123456789ab")
	if err != nil {
		t.Fatal(err)
	}
	// The key reaches the proxy as an environment variable: only a changed
	// Pod template restarts it with a new one.
	if got := proxyTemplateAnnotations(t, rendered)["cetirun.microsoft.com/copilot-proxy-key-id"]; got != "0123456789ab" {
		t.Fatalf("the proxy's Pod template must carry the key ID, got %q", got)
	}
	rendered, err = render(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := proxyTemplateAnnotations(t, rendered)["cetirun.microsoft.com/copilot-proxy-key-id"]; ok {
		t.Fatal("no key ID, no annotation")
	}
	if _, err := render("not-a-hash"); err == nil {
		t.Fatal("a key ID that is not a hash prefix must be rejected")
	}
}
