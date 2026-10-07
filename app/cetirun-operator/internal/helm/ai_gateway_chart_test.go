// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"encoding/json"
	"reflect"
	"slices"
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

// openClawInitContainers returns the init containers of the rendered workload.
func openClawInitContainers(t *testing.T, rendered map[string]string) []corev1.Container {
	t.Helper()
	sts := &appsv1.StatefulSet{}
	if err := yaml.Unmarshal([]byte(rendered["cetirun/templates/statefulset.yaml"]), sts); err != nil {
		t.Fatal(err)
	}
	return sts.Spec.Template.Spec.InitContainers
}

// openClawProvider returns the openai provider entry of the rendered OpenClaw
// configuration.
func openClawProvider(t *testing.T, rendered map[string]string) map[string]any {
	t.Helper()
	configMap := &corev1.ConfigMap{}
	if err := yaml.Unmarshal([]byte(rendered["cetirun/templates/configmap.yaml"]), configMap); err != nil {
		t.Fatal(err)
	}
	var config struct {
		Models struct {
			Providers map[string]map[string]any `json:"providers"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(configMap.Data["openclaw.json"]), &config); err != nil {
		t.Fatal(err)
	}
	return config.Models.Providers["openai"]
}

func TestAIGatewayChartWiring(t *testing.T) {
	rendered, err := renderAIGateway(t, aiGatewayInput("openclaw", "openai"))
	if err != nil {
		t.Fatal(err)
	}
	if value, ok := initGatewayMarker(t, rendered); !ok || value != "true" {
		t.Fatalf("init must know the Gateway owns the model list, got %q", value)
	}
	// The Gateway provider is bound to the virtual key: OpenClaw would
	// otherwise prefer a stored openai profile over OPENAI_API_KEY.
	provider := openClawProvider(t, rendered)
	wantKey := map[string]any{"source": "env", "provider": "default", "id": "OPENAI_API_KEY"}
	if provider["auth"] != "api-key" || !reflect.DeepEqual(provider["apiKey"], wantKey) {
		t.Fatalf("the Gateway provider must bind OPENAI_API_KEY, got auth=%v apiKey=%v", provider["auth"], provider["apiKey"])
	}
	// The sign-in the retired in-agent helper left in the home is removed
	// once, after doctor imported the auth profile files of earlier versions.
	containers := openClawInitContainers(t, rendered)
	var names []string
	var retire *corev1.Container
	for i := range containers {
		names = append(names, containers[i].Name)
		if containers[i].Name == "retire-copilot-sign-in" {
			retire = &containers[i]
		}
	}
	if doctor, retirement := slices.Index(names, "doctor"), slices.Index(names, "retire-copilot-sign-in"); doctor < 0 || retirement != doctor+1 {
		t.Fatalf("the retirement must run right after doctor, init containers %v", names)
	}
	if !slices.Equal(retire.Command, []string{"/bin/sh", "/etc/cetirun/retire-copilot-sign-in.sh"}) {
		t.Fatalf("retirement command %v", retire.Command)
	}
	if !strings.Contains(rendered["cetirun/templates/configmap.yaml"], "retire-copilot-sign-in.sh: |") {
		t.Fatal("the retirement script is not in the ConfigMap")
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
	// ...and its owner's choice of credentials.
	if provider := openClawProvider(t, rendered); provider["apiKey"] != nil || provider["auth"] != nil {
		t.Fatalf("a provider with its own endpoint must not be bound, got %v", provider)
	}
	// ...and OpenClaw's own choice of utility model, which that provider serves.
	if defaults := openClawAgentDefaults(t, rendered); defaults["utilityModel"] != nil {
		t.Fatalf("a provider with its own endpoint must keep OpenClaw's utility model, got %v", defaults["utilityModel"])
	}
	for _, container := range openClawInitContainers(t, rendered) {
		if container.Name == "retire-copilot-sign-in" {
			t.Fatal("without the Gateway the in-agent sign-in is the Agent's own and stays")
		}
	}
}

// openClawAgentDefaults returns agents.defaults of the rendered OpenClaw
// configuration.
func openClawAgentDefaults(t *testing.T, rendered map[string]string) map[string]any {
	t.Helper()
	configMap := &corev1.ConfigMap{}
	if err := yaml.Unmarshal([]byte(rendered["cetirun/templates/configmap.yaml"]), configMap); err != nil {
		t.Fatal(err)
	}
	var config struct {
		Agents struct {
			Defaults map[string]any `json:"defaults"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(configMap.Data["openclaw.json"]), &config); err != nil {
		t.Fatal(err)
	}
	return config.Agents.Defaults
}

// OpenClaw derives a utility model for titles and activity recaps from the
// primary model's provider: OpenAI's small model for the Gateway provider,
// which the Gateway does not offer. An Agent on the Gateway names its
// default model instead, with or without a model list.
func TestAIGatewayChartRoutesUtilityTasksToTheDefaultModel(t *testing.T) {
	listed := aiGatewayInput("openclaw", "openai")
	listed["aiGateway"].(map[string]any)["models"] = []any{
		map[string]any{"id": "gpt-6-sol", "name": "GPT-6 Sol", "contextWindow": 272000, "maxTokens": 128000},
		map[string]any{"id": "copilot/claude-haiku-4.5", "name": "Claude Haiku 4.5"},
	}
	for name, input := range map[string]map[string]any{"without a model list": aiGatewayInput("openclaw", "openai"), "with a model list": listed} {
		t.Run(name, func(t *testing.T) {
			rendered, err := renderAIGateway(t, input)
			if err != nil {
				t.Fatal(err)
			}
			defaults := openClawAgentDefaults(t, rendered)
			primary, _ := defaults["model"].(map[string]any)["primary"].(string)
			if primary == "" || defaults["utilityModel"] != primary {
				t.Fatalf("utilityModel = %v, want the default model %q", defaults["utilityModel"], primary)
			}
			allowed, _ := defaults["modelPolicy"].(map[string]any)["allow"].([]any)
			if !slices.Contains(allowed, any(primary)) {
				t.Fatalf("the utility model %q is not allowed: %v", primary, allowed)
			}
		})
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

func TestAIGatewayChartRestartsTheProxyForANewSignIn(t *testing.T) {
	render := func(generation any) (map[string]string, error) {
		input := aiGatewayInput("openclaw", "openai")
		proxy := map[string]any{
			"enabled": true, "image": "registry.example.com/cetirun-ai-gateway:1.0.0",
			"masterKeySecretName": "ai-gateway-copilot-proxy", "tokenSecretName": "github-copilot-token",
		}
		if generation != nil {
			proxy["signInGeneration"] = generation
		}
		input["aiGateway"].(map[string]any)["copilotProxy"] = proxy
		return renderAIGateway(t, input)
	}
	rendered, err := render("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	// The proxy keeps the Copilot token it exchanged: only a changed Pod
	// template, and with it an empty cache, makes a new sign-in take effect.
	if got := proxyTemplateAnnotations(t, rendered)["cetirun.microsoft.com/copilot-sign-in-generation"]; got != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("the proxy's Pod template must carry the sign-in generation, got %q", got)
	}
	rendered, err = render(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := proxyTemplateAnnotations(t, rendered)["cetirun.microsoft.com/copilot-sign-in-generation"]; ok {
		t.Fatal("no generation, no annotation")
	}
	if _, err := render("not a generation"); err == nil {
		t.Fatal("a generation that is not hexadecimal must be rejected")
	}
}

// The in-agent model sign-in helper is served exactly where the harness
// exposes its port: Hermes keeps it, and OpenClaw in a chart with the
// per-Agent Copilot proxy carries none, whether or not it uses the Gateway.
func TestPreparedReleaseServesSignInHelper(t *testing.T) {
	chrt, err := loader.Load("../../../cetirun-chart")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		input map[string]any
		want  bool
	}{
		{name: "Hermes", input: sampleInput("hermes", "github_copilot", false), want: true},
		{name: "OpenClaw without the Gateway", input: sampleInput("openclaw", "github_copilot", false), want: false},
		{name: "OpenClaw through the Gateway", input: aiGatewayInput("openclaw", "openai"), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepared, err := PrepareChart(chrt, tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if got := prepared.ServesSignInHelper(); got != tc.want {
				t.Fatalf("ServesSignInHelper() = %v, want %v", got, tc.want)
			}
		})
	}
	if (*PreparedRelease)(nil).ServesSignInHelper() {
		t.Fatal("no release serves no helper")
	}
}
