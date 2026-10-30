// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Azure/CetiRun/app/cetirun-operator/internal/agents"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/agents/builtin"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// AgentRequest is assembled by the controller after user overrides are checked.
// It is removed before values are passed to Helm.
type AgentRequest struct {
	agents.Request
	Registry         string `json:"registry,omitempty"`
	UIPrefix         string `json:"uiPrefix,omitempty"`
	TailscaleEnabled bool   `json:"tailscaleEnabled,omitempty"`
	ReleaseName      string `json:"releaseName,omitempty"`
	PullSecretName   string `json:"pullSecretName,omitempty"`
}

type PreparedRelease struct {
	Values               map[string]any
	Agent                agents.ResolvedAgent
	ChartVersion         string
	PlanHash             string
	WorkloadHash         string
	basePlanHash         string
	releaseName          string
	manifestHash         string
	manifest             []byte
	sealedInput          string
	actionRevision       int
	custom               bool
	chart                *chart.Chart
	sidecarAPIVersion    string
	pdbAPIVersion        string
	homeRetention        bool
	apiValidated         bool
	sandboxBundle        *sandboxworkload.Bundle
	sandboxTemplate      *corev1.PodTemplateSpec
	sandboxReconcile     *sandboxworkload.ReconcileExpectation
	sandboxPreflight     func(context.Context) error
	sandboxRenderGuard   func(context.Context) error
	sandboxGuard         func(context.Context) error
	sandboxBindingCreate func(context.Context, unstructured.Unstructured) (func(context.Context) error, error)
}

// CacheKey includes actual rendered output as well as the consumed inputs.
func (p *PreparedRelease) CacheKey() string {
	return p.PlanHash + ":" + p.manifestHash
}

// SupportsValue reports whether the loaded chart declares a top-level value.
// The controller uses it before relying on a value that older pinned chart
// versions would silently ignore.
func (p *PreparedRelease) SupportsValue(name string) bool {
	return p.SupportsValuePath(name)
}

// SupportsValuePath reports whether the loaded chart declares a nested value,
// such as aiGateway.models.
func (p *PreparedRelease) SupportsValuePath(path ...string) bool {
	if p == nil || p.chart == nil {
		return false
	}
	return declaresValuePath(p.chart.Values, path)
}

// ChartValues are the values a chart version declares, for checking what it
// supports before an Agent uses it.
type ChartValues struct {
	values map[string]any
}

// NewChartValues describes the values a loaded chart declares.
func NewChartValues(chrt *chart.Chart) *ChartValues {
	if chrt == nil {
		return &ChartValues{}
	}
	return &ChartValues{values: chrt.Values}
}

// SupportsValuePath reports whether the chart declares a nested value.
func (v *ChartValues) SupportsValuePath(path ...string) bool {
	return v != nil && declaresValuePath(v.values, path)
}

func declaresValuePath(values map[string]any, path []string) bool {
	if len(path) == 0 {
		return false
	}
	current := values
	for i, name := range path {
		value, ok := current[name]
		if !ok {
			return false
		}
		if i == len(path)-1 {
			return true
		}
		next, ok := value.(map[string]any)
		if !ok {
			return false
		}
		current = next
	}
	return false
}

// signInHelperPort names the runtime port of the in-agent model sign-in
// helper. A built-in harness exposes it only when its chart carries the
// helper and the Agent's models do not come through the AI Gateway.
const signInHelperPort = "cetirun-auth"

// ServesSignInHelper reports whether the prepared workload exposes the
// in-agent model sign-in helper, which the /<name>/cetirun-auth/ route and
// the Portal's in-agent Copilot sign-in reach.
func (p *PreparedRelease) ServesSignInHelper() bool {
	if p == nil {
		return false
	}
	runtime, _ := p.Values["runtime"].(map[string]any)
	ports, _ := runtime["ports"].([]any)
	for _, item := range ports {
		if port, ok := item.(map[string]any); ok && port["name"] == signInHelperPort {
			return true
		}
	}
	return false
}

// Rebuild uses the same loaded chart after platform integration bindings have
// been resolved. It never pulls a second, potentially changed chart.
func (p *PreparedRelease) Rebuild(input map[string]any) (*PreparedRelease, error) {
	if p.chart == nil {
		return nil, fmt.Errorf("prepared release has no chart")
	}
	if p.sandboxBundle != nil {
		return nil, fmt.Errorf("Sandbox rebuild requires separately validated user inputs and a current bundle")
	}
	return PrepareChart(p.chart, input)
}

// PrepareChart is also used by offline rendering tests. It has no Kubernetes or
// Helm actions; chart loading and applying the prepared result stay in Client.
func PrepareChart(chrt *chart.Chart, input map[string]any) (*PreparedRelease, error) {
	prepared, err := prepareChart(chrt, input, true)
	return prepared, configurationError(err)
}

func prepareChart(chrt *chart.Chart, input map[string]any, validateRender bool) (*PreparedRelease, error) {
	if input[sandboxworkload.ValuesKey] != nil {
		return nil, fmt.Errorf("sandboxWorkload is reserved for the protected Sandbox renderer")
	}
	if input["runtime"] != nil {
		return nil, fmt.Errorf("runtime is reserved for the platform")
	}
	return prepareRuntimeChart(chrt, input, validateRender)
}

func prepareRuntimeChart(chrt *chart.Chart, input map[string]any, validateRender bool) (*PreparedRelease, error) {
	var request AgentRequest
	requestJSON, err := json.Marshal(input["agentRequest"])
	if err != nil {
		return nil, fmt.Errorf("invalid agent runtime request")
	}
	if err := json.Unmarshal(requestJSON, &request); err != nil {
		return nil, fmt.Errorf("invalid agent runtime request")
	}
	if request.PullSecretName != "" && chrt.Metadata.Annotations["cetirun.microsoft.com/registry-credentials"] != "v1" {
		return nil, fmt.Errorf("the selected chart does not support managed registry credentials; upgrade the pinned chart first")
	}
	harness, err := builtin.NewRegistry().Lookup(request.Type)
	if err != nil {
		return nil, err
	}
	descriptor := harness.Describe()
	if descriptor.Type == "custom" && input[sandboxworkload.ValuesKey] == nil {
		return prepareCustomChart(chrt, input, request, validateRender)
	}
	memoryHome := "/home/vibe"
	if descriptor.Type == "custom" {
		memoryHome = text(object(object(input["runtime"])["agent"])["home"])
	}
	if memory, configured, err := resolveMemory(input, chrt, memoryHome); err != nil {
		return nil, err
	} else if configured {
		inputCopy := make(map[string]any, len(input))
		for key, value := range input {
			if key != "memoryOptions" && key != "memoryContext" {
				inputCopy[key] = value
			}
		}
		if declaresValuePath(chrt.Values, []string{"memoryContext"}) {
			inputCopy["memoryContext"] = memory.Values()
		}
		input = inputCopy
	}
	if descriptor.Type == "custom" {
		return prepareSandboxCustomChart(chrt, input)
	}
	profile, err := loadRuntimeProfile(chrt, descriptor, request.Registry)
	if err != nil {
		return nil, err
	}
	resolved, err := agents.Resolve(request.Request, profile)
	if err != nil {
		return nil, err
	}
	values, err := prepareRuntimeValues(chrt, input)
	if err != nil {
		return nil, err
	}
	if values["agentType"] != descriptor.Type {
		return nil, fmt.Errorf("agent type does not match runtime request")
	}
	secrets := object(values["secrets"])
	native := object(values[descriptor.Type])
	config := object(native["config"])
	if descriptor.PreserveUIPrefix {
		gateway := ensureObject(config, "gateway")
		gateway["port"] = *resolved.Port
		if request.UIPrefix != "" {
			ensureObject(gateway, "controlUi")["basePath"] = request.UIPrefix
		}
		ensureObject(values, "service")["port"] = *resolved.Port
		ensureObject(values, "service")["targetPort"] = *resolved.Port
	} else {
		ensureObject(native, "env")["API_SERVER_PORT"] = strconv.Itoa(int(*resolved.Port))
	}
	native["config"] = config
	values[descriptor.Type] = native
	skills := object(values["skills"])
	integrations := agents.Integrations{
		ExistingSecret:    present(secrets["existingSecret"]),
		Brave:             present(secrets["braveApiKey"]),
		Perplexity:        present(secrets["perplexityApiKey"]),
		Telegram:          present(secrets["telegramBotToken"]),
		TelegramTokenFile: text(secrets["telegramTokenFile"]),
		Slack:             present(secrets["slackBotToken"]) && present(secrets["slackAppToken"]),
		Discord:           present(secrets["discordBotToken"]),
		Feishu:            present(secrets["feishuAppId"]) && present(secrets["feishuAppSecret"]),
		TeamsApp:          present(secrets["msteamsAppId"]),
		TeamsTenant:       present(secrets["msteamsTenantId"]),
		TeamsPassword:     present(secrets["msteamsAppPassword"]),
		TeamsPort:         number(values["msteamsWebhookPort"]),
		TeamsAuthType:     text(values["msteamsAuthType"]),
		Tailscale:         request.TailscaleEnabled || present(secrets["tailscaleAuthKey"]),
	}
	provider := text(secrets["provider"])
	if provider == "" {
		provider = "github_copilot"
	}
	gateway := object(values["aiGateway"])
	var gatewayModels []agents.GatewayModel
	for _, item := range array(gateway["models"]) {
		model := object(item)
		gatewayModels = append(gatewayModels, agents.GatewayModel{
			ID: text(model["id"]), Name: text(model["name"]),
			ContextWindow: integer(model["contextWindow"]), MaxTokens: integer(model["maxTokens"]),
		})
	}
	bindings := agents.Bindings{
		MemoryEnabled:     (values[sandboxworkload.ValuesKey] == nil || object(values[sandboxworkload.ValuesKey])["reconciled"] == true) && object(values["memoryContext"])["enabled"] == true,
		DesktopEnabled:    object(values["desktop"])["enabled"] == true,
		ProfileID:         profile.ID,
		ChartVersion:      chrt.Metadata.Version,
		NativeConfig:      config,
		NativeOptions:     map[string]any{"acpx": native["acpx"], "plugins": native["plugins"], "portalSignIn": native["portalSignIn"]},
		NativeSandbox:     values[sandboxworkload.ValuesKey] != nil,
		ReconciledSandbox: object(values[sandboxworkload.ValuesKey])["reconciled"] == true,
		ConnectionsMCP:    agents.ConnectionsMCPBinding{Enabled: object(values["connectionsMCP"])["enabled"] == true, URL: text(object(values["connectionsMCP"])["url"])},
		Model: agents.ModelBinding{
			Provider:      provider,
			Name:          text(secrets["model"]),
			BaseURL:       text(secrets["apiBase"]),
			AuthMode:      text(secrets["authMode"]),
			Gateway:       gateway["enabled"] == true,
			GatewayModels: gatewayModels,
			GatewayKeyEnv: text(object(gateway["hermes"])["keyEnv"]),
		},
		Skills: agents.SkillsBinding{
			Shared:    skills["shared"] == true,
			Empty:     object(values[sandboxworkload.ValuesKey])["skillsMode"] == string(sandboxworkload.SkillsModeEmpty),
			MountPath: text(skills["mountPath"]),
		},
		Integrations:   integrations,
		ConfigTemplate: string(asset(chrt, "agents/"+descriptor.Type+"/config/native.tpl")),
		// OpenClaw in a chart with the per-Agent Copilot proxy signs in
		// through the Portal and carries no in-agent helper.
		NoSignInHelper: descriptor.Type == "openclaw" &&
			(&PreparedRelease{chart: chrt}).SupportsValuePath("aiGateway", "copilotProxy"),
	}
	if proxies, present := object(values[sandboxworkload.ValuesKey])["trustedProxyIPs"]; present {
		if err := decodeObject(proxies, &bindings.TrustedProxyIPs); err != nil || len(bindings.TrustedProxyIPs) == 0 {
			return nil, fmt.Errorf("native proxy trust requires an explicit compiler-selected IP list")
		}
	}
	// dig expects missing optional maps rather than explicit null.
	for key, value := range bindings.NativeOptions {
		if value == nil {
			delete(bindings.NativeOptions, key)
		}
	}
	for _, item := range array(skills["npm"]) {
		bindings.Skills.NPM = append(bindings.Skills.NPM, text(item))
	}
	if object(values["ttyd"])["enabled"] == true {
		bindings.ReservedPorts = append(bindings.ReservedPorts, agents.PortBinding{
			Name: "ttyd", Number: number(object(values["ttyd"])["port"]),
		})
	}
	if object(values["desktop"])["enabled"] == true {
		bindings.ReservedPorts = append(bindings.ReservedPorts,
			agents.PortBinding{Name: "vnc", Number: 5900},
			agents.PortBinding{Name: "desktop", Number: 6080},
			agents.PortBinding{Name: "browser", Number: 18800})
	}
	if integrations.TeamsPort > 0 {
		bindings.ReservedPorts = append(bindings.ReservedPorts, agents.PortBinding{Name: "teams", Number: integrations.TeamsPort})
	}
	contribution, err := harness.Build(resolved, bindings)
	if err != nil {
		return nil, err
	}
	contributionJSON, err := json.Marshal(contribution)
	if err != nil {
		return nil, err
	}
	var runtimeValues map[string]any
	if err := json.Unmarshal(contributionJSON, &runtimeValues); err != nil {
		return nil, err
	}
	runtimeValues["contractVersion"] = "v1"
	if request.PullSecretName != "" {
		runtimeValues["imagePullSecrets"] = []any{map[string]any{"name": request.PullSecretName}}
	}
	values["runtime"] = runtimeValues
	if err := chartutil.ValidateAgainstSchema(chrt, values); err != nil {
		return nil, fmt.Errorf("runtime values do not match the selected chart schema")
	}
	return &PreparedRelease{
		Values:       values,
		Agent:        resolved,
		ChartVersion: chrt.Metadata.Version,
		chart:        chrt,
	}, nil
}

func loadRuntimeProfile(chrt *chart.Chart, descriptor agents.Descriptor, registry string) (agents.Profile, error) {
	profilePath := "agents/" + descriptor.Type + "/profile.yaml"
	var definition struct {
		ContractVersion string   `json:"contractVersion"`
		NativeVersion   string   `json:"nativeVersion"`
		Port            int32    `json:"port"`
		QualifiedImages []string `json:"qualifiedImages"`
	}
	if err := yaml.UnmarshalStrict(asset(chrt, profilePath), &definition); err != nil ||
		definition.ContractVersion != "v1" || definition.NativeVersion == "" || definition.Port != descriptor.DefaultPort {
		return agents.Profile{}, fmt.Errorf("chart lacks a supported %s runtime profile", descriptor.Type)
	}

	baseImage := object(object(chrt.Values[descriptor.Type])["image"])
	repository := text(baseImage["repository"])
	if registry != "" {
		repository = registry + "/" + descriptor.Type + "-gateway"
	}
	tag := text(baseImage["tag"])
	if tag == "" {
		tag = chrt.Metadata.AppVersion
	}
	if repository == "" || tag == "" || tag == "latest" {
		return agents.Profile{}, fmt.Errorf("built-in profile requires a versioned default image")
	}
	image := repository + ":" + tag
	if digest := text(baseImage["digest"]); digest != "" {
		image += "@" + digest
	}
	return agents.Profile{
		ID:              chrt.Metadata.Version + "/" + descriptor.Type + "/" + definition.NativeVersion,
		Image:           image,
		Port:            definition.Port,
		QualifiedImages: definition.QualifiedImages,
	}, nil
}

func prepareRuntimeValues(chrt *chart.Chart, input map[string]any) (map[string]any, error) {
	coalesced, err := chartutil.CoalesceValues(chrt, input)
	if err != nil {
		return nil, fmt.Errorf("coalesce runtime inputs: %w", err)
	}
	if err := chartutil.ValidateAgainstSchema(chrt, coalesced); err != nil {
		return nil, fmt.Errorf("runtime inputs do not match the selected chart schema")
	}
	delete(coalesced, "agentRequest")

	// JSON round-tripping isolates the nested maps from caller-owned values.
	data, err := json.Marshal(coalesced)
	if err != nil {
		return nil, fmt.Errorf("invalid chart values")
	}
	var values map[string]any
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, err
	}
	return values, nil
}

func asset(chrt *chart.Chart, name string) []byte {
	for _, file := range chrt.Files {
		if file.Name == name {
			return file.Data
		}
	}
	return nil
}

func object(value any) map[string]any {
	result, _ := value.(map[string]any)
	if result == nil {
		return map[string]any{}
	}
	return result
}

func ensureObject(parent map[string]any, key string) map[string]any {
	result := object(parent[key])
	parent[key] = result
	return result
}

func text(value any) string {
	result, _ := value.(string)
	return result
}

func present(value any) bool { return text(value) != "" }

func number(value any) int32 {
	n, _ := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(value)), 10, 32)
	return int32(n)
}

// integer reads a whole number that may have been round-tripped through
// JSON as a float, where fmt would print large values in exponent form.
func integer(value any) int64 {
	switch v := value.(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return 0
}

func array(value any) []any {
	result, _ := value.([]any)
	return result
}
