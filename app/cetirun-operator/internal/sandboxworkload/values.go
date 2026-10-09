// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxworkload

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"
	strictjson "sigs.k8s.io/json"
)

// ValidateUserValues must run on the original per-Agent overrides, before
// merging any defaults. Protected input is rejected, not silently overwritten.
func ValidateUserValues(values map[string]any, authorizedSecrets []string) error {
	for key, value := range values {
		switch key {
		case "secrets":
			fields, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("secrets must be an object")
			}
			for field, raw := range fields {
				text, ok := raw.(string)
				if !ok || text == "" {
					return fmt.Errorf("secrets.%s must be a non-empty string", field)
				}
				switch field {
				case "existingSecret":
					if !slices.Contains(authorizedSecrets, text) {
						return fmt.Errorf("secrets.existingSecret is not authorized")
					}
				case "provider":
					if !slices.Contains([]string{"github_copilot", "openai", "anthropic"}, text) {
						return fmt.Errorf("unsupported secrets.provider")
					}
				case "model":
				default:
					return fmt.Errorf("secrets.%s is protected", field)
				}
			}
		case "extraEnv":
			raw, err := json.Marshal(value)
			if err != nil || string(raw) == "null" {
				return fmt.Errorf("extraEnv must be a typed environment list")
			}
			var env []corev1.EnvVar
			problems, err := strictjson.UnmarshalStrict(raw, &env)
			if err != nil || len(problems) != 0 {
				return fmt.Errorf("invalid extraEnv")
			}
			seen := map[string]bool{}
			for _, item := range env {
				if len(validation.IsEnvVarName(item.Name)) != 0 || seen[item.Name] || protectedEnvironment(item.Name) {
					return fmt.Errorf("extraEnv contains a protected or duplicate name")
				}
				seen[item.Name] = true
				if item.ValueFrom != nil {
					from := item.ValueFrom
					if item.Value != "" || from.SecretKeyRef == nil || from.ConfigMapKeyRef != nil ||
						from.FieldRef != nil || from.ResourceFieldRef != nil ||
						!slices.Contains(authorizedSecrets, from.SecretKeyRef.Name) || from.SecretKeyRef.Key == "" {
						return fmt.Errorf("extraEnv references an unauthorized source")
					}
				}
			}
		default:
			return fmt.Errorf("Sandbox Helm value %q is protected or unsupported", key)
		}
	}
	return nil
}

func protectedEnvironment(name string) bool {
	for _, prefix := range []string{"CETIRUN_", "OPENCLAW_", "HERMES_", "AZURE_", "KUBERNETES_", "LD_", "DYLD_", "NODE_", "PYTHON", "GIT_", "NPM_", "TAILSCALE_", "TS_", "TTYD_", "MSTEAMS_"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return slices.Contains([]string{"HOME", "PATH", "SHELL", "ENV", "BASH_ENV", "KUBECONFIG", "API_SERVER_ENABLED", "API_SERVER_HOST",
		"API_SERVER_PORT", "API_SERVER_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "BRAVE_API_KEY", "PERPLEXITY_API_KEY"}, name)
}

func (b *Bundle) SecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		RunAsUser: ptr.To(int64(1024)), RunAsGroup: ptr.To(int64(1024)), RunAsNonRoot: ptr.To(true),
		AllowPrivilegeEscalation: ptr.To(false), Privileged: ptr.To(false),
		ReadOnlyRootFilesystem: ptr.To(*b.input.Resolution.AgentPolicy.Security.ReadOnlyRootFilesystem),
		Capabilities:           &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile:         &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

func (b *Bundle) PodSecurityContext() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{
		RunAsUser: ptr.To(int64(1024)), RunAsGroup: ptr.To(int64(1024)), RunAsNonRoot: ptr.To(true),
		FSGroup: ptr.To(int64(1024)), FSGroupChangePolicy: ptr.To(corev1.FSGroupChangeOnRootMismatch),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

func (b *Bundle) DNSConfig() *corev1.PodDNSConfig {
	return &corev1.PodDNSConfig{Nameservers: slices.Clone(b.input.Controls.DNSIPs)}
}

// Apply replaces the complete protected values bundle after callers have
// validated and merged user input. It does not mutate the caller's maps.
func (b *Bundle) Apply(values map[string]any) (map[string]any, error) {
	out, err := clone(values)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	secrets, _ := out["secrets"].(map[string]any)
	if !slices.Contains(b.input.AuthorizedSecrets, textValue(secrets["existingSecret"])) {
		return nil, fmt.Errorf("native runtime Secret is not authorized")
	}
	gateway, _ := out["aiGateway"].(map[string]any)
	if key := b.input.GatewayKey; key != nil {
		if gateway["enabled"] != true || textValue(gateway["keySecretName"]) != key.Name ||
			textValue(gateway["keySecretKey"]) != "api-key" || key.Name == textValue(secrets["existingSecret"]) {
			return nil, fmt.Errorf("native Gateway values do not match the separately authorized key")
		}
	} else if gateway["enabled"] == true {
		return nil, fmt.Errorf("native Gateway requires a typed authorized key projection")
	}
	security, _ := jsonValue(b.SecurityContext())
	podSecurity, _ := jsonValue(b.PodSecurityContext())
	dnsConfig, _ := jsonValue(b.DNSConfig())
	agent, _ := jsonValue(b.agentResources())
	helper, _ := jsonValue(b.helperResources())
	podResources, _ := jsonValue(b.accounting.Pod)
	controls, _ := jsonValue(b.input.Controls.Objects)
	volumes, _ := jsonValue(b.input.Controls.Volumes)
	mounts, _ := jsonValue(b.input.Controls.VolumeMounts)
	env, _ := jsonValue(b.input.Controls.Environment)
	owners, _ := jsonValue([]metav1.OwnerReference{AgentOwner(b.input.AgentName, b.input.Resolution.Binding.AgentUID)})
	labels, _ := jsonValue(b.input.Controls.PodLabels)
	overlay := map[string]any{
		"agentType": b.input.AgentType, "fullnameOverride": b.input.WorkloadName,
		"replicaCount": 0, "runtimeClassName": b.input.RuntimeClass.Name,
		"podSecurityContext": podSecurity, "securityContext": security, "resources": agent,
		"podAnnotations": map[string]any{
			"sidecar.istio.io/inject": "false", RevisionAnnotation: b.input.Controls.Revision,
			AgentUIDAnnotation: string(b.input.Resolution.Binding.AgentUID), KindAnnotation: AgentWorkload,
		},
		"sidecarScope":  map[string]any{"enabled": false, "additionalEgressHosts": []any{}},
		"networkPolicy": map[string]any{"enabled": true},
		"serviceAccount": map[string]any{
			"create": false, "name": b.input.Controls.ServiceAccountName, "role": "", "automountToken": false, "annotations": map[string]any{},
		},
		"azureWorkloadIdentity": map[string]any{"enabled": false, "clientId": ""},
		"agentIdentity":         map[string]any{"enabled": false, "brokerURL": ""},
		"persistence": map[string]any{
			"enabled": true, "existingClaim": b.input.Home.Name, "useStatefulSetVolumeClaim": false, "retainPolicy": "Retain",
			"storageClass": *b.input.Home.Spec.StorageClassName, "accessMode": string(b.input.Home.Spec.AccessModes[0]),
			"size": b.input.Resolution.Sandbox.Storage.Home.Size.String(), "annotations": map[string]any{},
		},
		"skills": map[string]any{
			"shared": true, "existingClaim": b.input.Skills.Name, "mountPath": SkillsPath,
			"npm": []any{}, "clawHub": []any{},
		},
		"doctor":                    map[string]any{"enabled": b.input.Helpers.Doctor},
		"initContainers":            []any{},
		"sidecars":                  []any{},
		"extraVolumes":              []any{},
		"extraVolumeMounts":         []any{},
		"extraEnvFrom":              []any{},
		"imagePullSecrets":          []any{},
		"nodeSelector":              map[string]any{},
		"affinity":                  map[string]any{},
		"tolerations":               []any{},
		"topologySpreadConstraints": []any{},
		"autoscaling":               map[string]any{"enabled": false},
		"ingress":                   map[string]any{"enabled": false},
		// Protected namespaces use the operator's CONNECT guard instead of
		// the legacy per-release exec policy and its caller exemption.
		"execRestriction":     map[string]any{"enabled": false},
		"podDisruptionBudget": map[string]any{"enabled": false},
		"tailscale":           map[string]any{"enabled": false, "serve": false},
		ValuesKey: map[string]any{
			"contractVersion": ContractVersion, "executionHeld": true,
			"podLabels":    labels,
			"dnsConfig":    dnsConfig,
			"podResources": podResources, "helperResources": helper, "securityContext": security,
			"scratchSizeLimit": b.input.Resolution.Sandbox.Storage.Scratch.SizeLimit.String(),
			"ownerReferences":  owners, "identityVolumes": nonNilList(volumes),
			"identityMounts": nonNilList(mounts), "identityEnvironment": nonNilList(env),
			"objects": nonNilList(controls),
		},
	}
	if scheduling := b.input.RuntimeClass.Scheduling; scheduling != nil {
		overlay["nodeSelector"], _ = jsonValue(scheduling.NodeSelector)
		overlay["tolerations"], _ = jsonValue(scheduling.Tolerations)
		if overlay["nodeSelector"] == nil {
			overlay["nodeSelector"] = map[string]any{}
		}
		overlay["tolerations"] = nonNilList(overlay["tolerations"])
	}
	for key, value := range overlay {
		out[key] = value
	}
	for name, enabled := range map[string]bool{"logger": b.input.Helpers.Logger, "desktop": b.input.Helpers.Desktop} {
		settings, _ := out[name].(map[string]any)
		if settings == nil {
			settings = map[string]any{}
		}
		settings["enabled"], settings["resources"] = enabled, helper
		out[name] = settings
	}
	secrets["tailscaleAuthKey"] = ""
	if b.input.AgentType == "openclaw" {
		native, _ := out["openclaw"].(map[string]any)
		if native == nil {
			native = map[string]any{}
		}
		native["doctor"] = map[string]any{"enabled": b.input.Helpers.Doctor}
		if !b.input.Helpers.Desktop {
			config, _ := native["config"].(map[string]any)
			if config == nil {
				config = map[string]any{}
			}
			config["browser"] = map[string]any{"enabled": false, "attachOnly": false}
			native["config"] = config
		}
		out["openclaw"] = native
	}
	return out, nil
}

func jsonValue(value any) (any, error) {
	raw, err := json.Marshal(value)
	var out any
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}

func nonNilList(value any) any {
	if value == nil {
		return []any{}
	}
	return value
}

func textValue(value any) string {
	valueString, _ := value.(string)
	return valueString
}
