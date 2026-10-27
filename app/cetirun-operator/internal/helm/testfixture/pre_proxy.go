// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package testfixture

import (
	"bytes"
	"crypto/sha256"
	"fmt"

	"helm.sh/helm/v3/pkg/chart"
)

// laterAdditions are chart changes made after the proxy hook, removed before
// the original bytes are checked.
var laterAdditions = map[string][]string{
	"agents/openclaw/config/native.tpl": {`{{- /* Portal sign-in: the ingress lets only the Agent's owner through, after
   their Portal sign-in, and passes their email from that sign-in; the Istio
   sidecar is the Agent's only proxy. OpenClaw then takes the owner from that
   header and approves their browsers without "openclaw devices approve". The
   gateway token becomes a password for clients on loopback, such as the CLI
   in the Terminal, as OpenClaw refuses a token in this mode. */}}
{{- if and (dig "portalSignIn" "enabled" false .Values.openclaw) (not .Values.sandboxWorkload) (not .TailscaleEnabled) }}
{{- $gateway := dig "gateway" (dict) $config }}
{{- if not (has "127.0.0.6" (dig "trustedProxies" (list) $gateway)) }}
{{- $_ := required "openclaw.portalSignIn needs 127.0.0.6, the Istio sidecar, in openclaw.config.gateway.trustedProxies" "" }}
{{- end }}
{{- $_ := set $gateway "auth" (dict
    "mode" "trusted-proxy"
    "trustedProxy" (dict
      "userHeader" "x-auth-request-email"
      "allowLoopback" true
      "deviceAutoApprove" (dict "enabled" true))) }}
{{- $_ := set $config "gateway" $gateway }}
{{- end }}
`},
	"values.schema.json": {`                "portalSignIn": {
                    "type": "object",
                    "description": "Sign the owner in to the Control UI with their Portal sign-in (OpenClaw trusted-proxy mode)",
                    "additionalProperties": false,
                    "properties": {
                        "enabled": {
                            "type": "boolean",
                            "default": false
                        }
                    }
                },
`},
}

// RestorePreProxyContract reconstructs the three chart assets changed by the
// proxy hook and verifies their exact bytes at public revision 6414494154151b2daf74d39d1e989ebeb7ea5020.
func RestorePreProxyContract(chrt *chart.Chart) error {
	if startup, ok := chrt.Values["startupProbe"].(map[string]any); ok {
		startup["failureThreshold"] = 30
	}
	if gateway, ok := chrt.Values["aiGateway"].(map[string]any); ok {
		if proxy, ok := gateway["copilotProxy"].(map[string]any); ok {
			delete(proxy, "replicaCount")
		}
	}
	assets := map[string]*[]byte{"values.schema.json": &chrt.Schema}
	for _, files := range [][]*chart.File{chrt.Files, chrt.Templates} {
		for _, file := range files {
			assets[file.Name] = &file.Data
		}
	}
	// The current chart also contains native MCP support. Remove only those
	// additions before reconstructing and checking the fixed pre-proxy revision.
	for name, addition := range map[string]string{
		"agents/openclaw/config/native.tpl": `{{- if .Values.connectionsMCP.enabled }}
{{- $mcp := dig "mcp" (dict) $config }}
{{- $servers := dig "servers" (dict) $mcp }}
{{- $_ := set $servers "cetirun-connections" (dict "url" .Values.connectionsMCP.url "transport" "streamable-http" "sslVerify" true) }}
{{- $_ := set $mcp "servers" $servers }}
{{- $_ := set $config "mcp" $mcp }}
{{- else }}
{{- $servers := dig "mcp" "servers" (dict) $config }}
{{- $_ := unset $servers "cetirun-connections" }}
{{- end }}
`,
		"templates/_sandbox.tpl": ".Values.connectionsMCP.enabled ",
	} {
		data, exists := assets[name]
		if !exists || bytes.Count(*data, []byte(addition)) != 1 {
			return fmt.Errorf("pre-proxy fixture requires the exact MCP addition in %s", name)
		}
		*data = bytes.Replace(*data, []byte(addition), nil, 1)
	}
	// Reverse later source changes before verifying the immutable old fixture.
	for name, replacements := range map[string][][2]string{
		"agents/openclaw/config/native.tpl": {
			{`{{- $contextSkills := and (or (not .Values.sandboxWorkload) (dig "reconciled" false (.Values.sandboxWorkload | default dict))) (dig "enabled" false (.Values.memoryContext | default dict)) }}`,
				`{{- $contextSkills := and (not .Values.sandboxWorkload) (dig "enabled" false (.Values.memoryContext | default dict)) }}`},
			{"{{- if $contextSkills }}\n{{- $plugins :=", "{{- if and (not .Values.sandboxWorkload) (dig \"enabled\" false (.Values.memoryContext | default dict)) }}\n{{- $plugins :="},
		},
		"values.schema.json": {
			{`"type": ["integer", "null"],
                    "minimum": 1,
                    "default": null,
                    "description": "Null selects 60 for common-executor OpenClaw Sandboxes, otherwise 30; explicit positive integers are preserved"`,
				`"type": "integer",
                    "default": 30`},
			{"                        \"replicaCount\": {\"type\": [\"integer\", \"null\"], \"minimum\": 0, \"maximum\": 1, \"default\": null},\n", ""},
			{`"executionHeld": {"type": "boolean"},
                "reconciled": {"const": true}`, `"executionHeld": {"const": true}`},
			{`"agentType": {"enum": ["openclaw", "hermes"]}`, `"agentType": {"const": "openclaw"}`},
			{`"sandboxWorkload": {
                        "anyOf": [
                            {"required": ["podFinalizers"]},
                            {"required": ["reconciled"], "properties": {"reconciled": {"const": true}}}
                        ]
                    }`, `"sandboxWorkload": {"required": ["podFinalizers"]}`},
		},
		"templates/_sandbox.tpl": {
			{`{{- $reconciled := $s.reconciled | default false -}}
{{- if or (ne $s.contractVersion "v1") (and (not $reconciled) (or (ne $s.executionHeld true) (ne (int .Values.replicaCount) 0))) (and $reconciled (or $s.executionHeld (gt (int .Values.replicaCount) 1) (lt (int .Values.replicaCount) 0))) -}}`,
				`{{- if or (ne $s.contractVersion "v1") (ne $s.executionHeld true) (ne (int .Values.replicaCount) 0) -}}`},
			{`(and (not $reconciled) (ne (include "cetirun.agent.type" .) "openclaw"))`, `(ne (include "cetirun.agent.type" .) "openclaw")`},
			{`(and (not $reconciled) (ne (toJson ($s.podFinalizers | default list)) (toJson (list "cetirun.microsoft.com/native-pod-lifetime"))))`,
				`(ne (toJson ($s.podFinalizers | default list)) (toJson (list "cetirun.microsoft.com/native-pod-lifetime")))`},
			{`(and (not $reconciled) (or .Values.initContainers .Values.sidecars .Values.extraVolumes .Values.extraVolumeMounts .Values.extraEnvFrom))`,
				`.Values.initContainers .Values.sidecars .Values.extraVolumes .Values.extraVolumeMounts .Values.extraEnvFrom`},
		},
	} {
		data := assets[name]
		if data == nil {
			return fmt.Errorf("missing historical chart input %s", name)
		}
		for _, replacement := range replacements {
			*data = bytes.ReplaceAll(*data, []byte(replacement[0]), []byte(replacement[1]))
		}
	}
	for name, change := range map[string]struct {
		addition string
		checksum string
	}{
		"agents/openclaw/config/native.tpl": {
			// Remove later browser defaulting too; retain the original checksum.
			addition: `{{- /* Use the desktop's headed browser when available; otherwise let OpenClaw
   launch a headless browser. Explicit boolean preferences take precedence. */}}
{{- $browser := dig "browser" (dict) $config | default dict }}
{{- if eq (index $browser "attachOnly") nil }}
{{- $_ := set $browser "attachOnly" .Values.desktop.enabled }}
{{- end }}
{{- if eq (index $browser "headless") nil }}
{{- $_ := set $browser "headless" (not .Values.desktop.enabled) }}
{{- end }}
{{- $_ := set $config "browser" $browser }}
{{- if and .Values.sandboxWorkload (hasKey .Values.sandboxWorkload "trustedProxyIPs") }}
{{- $gateway := dig "gateway" (dict) $config }}
{{- $_ := set $gateway "trustedProxies" (.Values.sandboxWorkload.trustedProxyIPs | default list) }}
{{- $_ := set $config "gateway" $gateway }}
{{- end }}
`,
			checksum: "8bf84a1d564eba85382a71e29f9e2c3261e70c0b7ca4acf95e520377fdbf8abf",
		},
		"templates/_sandbox.tpl": {
			addition: `{{- if hasKey $s "trustedProxyIPs" -}}
{{- if not (kindIs "slice" $s.trustedProxyIPs) -}}
{{- fail "Sandbox trustedProxyIPs must be a compiler-selected list" -}}
{{- end -}}
{{- range $s.trustedProxyIPs -}}
{{- if or (not (kindIs "string" .)) (empty .) -}}
{{- fail "Sandbox trustedProxyIPs must contain nonempty compiler-selected IPs" -}}
{{- end -}}
{{- end -}}
{{- end -}}
`,
			checksum: "3eb73fb04375e6fdcb9280e966565028f40338d73136c5b319047f584ff6b090",
		},
		"values.schema.json": {
			addition: `                "trustedProxyIPs": {
                    "type": "array",
                    "uniqueItems": true,
                    "description": "Compiler-selected ingress IPs or registered Pod CIDRs; not tenant proxy trust",
                    "items": {
                        "type": "string",
                        "minLength": 1,
                        "anyOf": [
                            {"format": "ipv4"},
                            {"format": "ipv6"},
                            {"pattern": "^([0-9]{1,3}\\.){3}[0-9]{1,3}/([1-9]|[12][0-9]|3[0-2])$"},
                            {"pattern": "^[0-9a-fA-F]*:[0-9a-fA-F:]+/([1-9]|[1-9][0-9]|1[01][0-9]|12[0-8])$"}
                        ]
                    }
                },
`,
			checksum: "8bdf6a6367048be1d1d818061cf0a268181b0e82324c1834259f2b069320aaaa",
		},
	} {
		data, exists := assets[name]
		if !exists || bytes.Count(*data, []byte(change.addition)) != 1 {
			return fmt.Errorf("pre-proxy fixture requires the exact added hook in %s", name)
		}
		original := bytes.Replace(*data, []byte(change.addition), nil, 1)
		for _, later := range laterAdditions[name] {
			if bytes.Count(original, []byte(later)) != 1 {
				return fmt.Errorf("pre-proxy fixture requires the later addition in %s", name)
			}
			original = bytes.Replace(original, []byte(later), nil, 1)
		}
		if fmt.Sprintf("%x", sha256.Sum256(original)) != change.checksum {
			return fmt.Errorf("pre-proxy fixture %s differs from the original chart asset", name)
		}
		*data = original
	}
	return nil
}
