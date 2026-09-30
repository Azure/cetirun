// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
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
}

type PreparedRelease struct {
	Values            map[string]any
	Agent             agents.ResolvedAgent
	ChartVersion      string
	PlanHash          string
	WorkloadHash      string
	basePlanHash      string
	releaseName       string
	manifestHash      string
	manifest          []byte
	sealedInput       string
	actionRevision    int
	custom            bool
	chart             *chart.Chart
	sidecarAPIVersion string
	pdbAPIVersion     string
	homeRetention     bool
	apiValidated      bool
	sandboxBundle     *sandboxworkload.Bundle
	sandboxTemplate   *corev1.PodTemplateSpec
}

// CacheKey includes actual rendered output as well as the consumed inputs.
func (p *PreparedRelease) CacheKey() string {
	return p.PlanHash + ":" + p.manifestHash
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
	return prepareRuntimeChart(chrt, input, validateRender)
}

func prepareRuntimeChart(chrt *chart.Chart, input map[string]any, validateRender bool) (*PreparedRelease, error) {
	if input["runtime"] != nil {
		return nil, fmt.Errorf("runtime is reserved for the platform")
	}
	var request AgentRequest
	requestJSON, err := json.Marshal(input["agentRequest"])
	if err != nil {
		return nil, fmt.Errorf("invalid agent runtime request")
	}
	if err := json.Unmarshal(requestJSON, &request); err != nil {
		return nil, fmt.Errorf("invalid agent runtime request")
	}
	harness, err := builtin.NewRegistry().Lookup(request.Type)
	if err != nil {
		return nil, err
	}
	descriptor := harness.Describe()
	if descriptor.Type == "custom" {
		return prepareCustomChart(chrt, input, request, validateRender)
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
	bindings := agents.Bindings{
		ProfileID:     profile.ID,
		ChartVersion:  chrt.Metadata.Version,
		NativeConfig:  config,
		NativeOptions: map[string]any{"acpx": native["acpx"], "plugins": native["plugins"]},
		Model: agents.ModelBinding{
			Provider: provider,
			Name:     text(secrets["model"]),
			BaseURL:  text(secrets["apiBase"]),
			AuthMode: text(secrets["authMode"]),
		},
		Skills: agents.SkillsBinding{
			Shared:    skills["shared"] == true,
			MountPath: text(skills["mountPath"]),
		},
		Integrations:   integrations,
		ConfigTemplate: string(asset(chrt, "agents/"+descriptor.Type+"/config/native.tpl")),
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

func array(value any) []any {
	result, _ := value.([]any)
	return result
}
