// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package agents

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"sigs.k8s.io/yaml"
)

// RenderConfig evaluates the same native asset used by standalone Helm. Only
// deterministic formatting/collection functions are exposed, with no lookup,
// environment access, network access, or nested template evaluation.
func RenderConfig(agent ResolvedAgent, bindings Bindings) (string, error) {
	renderer, err := template.New("native-config").Funcs(nativeTemplateFunctions()).Parse(bindings.ConfigTemplate)
	if err != nil {
		return "", fmt.Errorf("invalid native config asset")
	}
	context := nativeTemplateContext(agent, bindings)
	var output bytes.Buffer
	if err := renderer.Execute(&output, context); err != nil {
		// Native preferences can contain user data; don't echo template errors.
		return "", fmt.Errorf("native config does not match the selected profile")
	}
	return strings.TrimSpace(output.String()) + "\n", nil
}

func nativeTemplateFunctions() template.FuncMap {
	available := sprig.TxtFuncMap()
	funcs := template.FuncMap{}
	for _, name := range []string{
		"deepCopy", "default", "dict", "list", "dig", "set", "int", "replace",
		"append", "concat", "uniq", "has", "hasKey",
	} {
		funcs[name] = available[name]
	}
	funcs["toPrettyJson"] = func(value any) (string, error) {
		raw, err := json.MarshalIndent(value, "", "  ")
		return string(raw), err
	}
	funcs["toYaml"] = func(value any) (string, error) {
		raw, err := yaml.Marshal(value)
		return strings.TrimSuffix(string(raw), "\n"), err
	}
	funcs["required"] = func(message string, value string) (string, error) {
		if value == "" {
			return "", fmt.Errorf("%s", message)
		}
		return value, nil
	}
	return funcs
}

func nativeTemplateContext(agent ResolvedAgent, bindings Bindings) map[string]any {
	integrations := bindings.Integrations
	native := map[string]any{"config": bindings.NativeConfig}
	for key, value := range bindings.NativeOptions {
		native[key] = value
	}
	values := map[string]any{
		agent.Type: native,
		"secrets": map[string]any{
			"provider":           bindings.Model.Provider,
			"model":              bindings.Model.Name,
			"apiBase":            bindings.Model.BaseURL,
			"authMode":           bindings.Model.AuthMode,
			"existingSecret":     integrations.ExistingSecret,
			"braveApiKey":        integrations.Brave,
			"perplexityApiKey":   integrations.Perplexity,
			"telegramBotToken":   integrations.Telegram,
			"telegramTokenFile":  integrations.TelegramTokenFile,
			"slackBotToken":      integrations.Slack,
			"slackAppToken":      integrations.Slack,
			"discordBotToken":    integrations.Discord,
			"feishuAppId":        integrations.Feishu,
			"feishuAppSecret":    integrations.Feishu,
			"msteamsAppId":       integrations.TeamsApp,
			"msteamsTenantId":    integrations.TeamsTenant,
			"msteamsAppPassword": integrations.TeamsPassword,
		},
		"skills": map[string]any{
			"shared":    bindings.Skills.Shared,
			"mountPath": bindings.Skills.MountPath,
			"npm":       bindings.Skills.NPM,
		},
		"msteamsAuthType":    integrations.TeamsAuthType,
		"msteamsWebhookPort": integrations.TeamsPort,
	}
	return map[string]any{
		"Values":           values,
		"Chart":            map[string]any{"Version": bindings.ChartVersion},
		"SkillDirs":        NPMSkillDirs(bindings.Skills.NPM),
		"TailscaleEnabled": integrations.Tailscale,
	}
}

func NPMSkillDirs(packages []string) []string {
	dirs := make([]string, 0, len(packages))
	for _, spec := range packages {
		prefix := ""
		if strings.HasPrefix(spec, "@") {
			prefix, spec = "@", strings.TrimPrefix(spec, "@")
		}
		name, _, _ := strings.Cut(spec, "@")
		dirs = append(dirs, "/home/vibe/.cetirun/skills/npm/"+strings.ReplaceAll(prefix+name, "/", "__"))
	}
	return dirs
}

func Build(agent ResolvedAgent, bindings Bindings, descriptor Descriptor) (Contribution, error) {
	if err := Validate(agent, bindings, descriptor); err != nil {
		return Contribution{}, err
	}
	config, err := RenderConfig(agent, bindings)
	if err != nil {
		return Contribution{}, err
	}
	ports := []PortBinding{{Name: descriptor.PortName, Number: *agent.Port}}
	if descriptor.DashboardPort != 0 {
		ports = append(ports, PortBinding{Name: "dashboard", Number: descriptor.DashboardPort})
	}
	ports = append(ports, PortBinding{Name: "cetirun-auth", Number: 18790})
	return Contribution{
		Agent:       agent,
		ProfileID:   bindings.ProfileID,
		ConfigFiles: map[string]string{descriptor.ConfigFile: config},
		Ports:       ports,
		HTTP: &HTTPBinding{
			PortName:      descriptor.PortName,
			HealthPath:    descriptor.HealthPath,
			ReadinessPath: descriptor.ReadinessPath,
			StartupPath:   descriptor.StartupPath,
		},
	}, nil
}
