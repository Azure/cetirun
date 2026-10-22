// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package testfixture

import (
	"bytes"
	"crypto/sha256"
	"fmt"

	"helm.sh/helm/v3/pkg/chart"
)

// RestorePreProxyContract reconstructs the three chart assets changed by the
// proxy hook and verifies their exact bytes at public revision 6414494154151b2daf74d39d1e989ebeb7ea5020.
func RestorePreProxyContract(chrt *chart.Chart) error {
	assets := map[string]*[]byte{"values.schema.json": &chrt.Schema}
	for _, files := range [][]*chart.File{chrt.Files, chrt.Templates} {
		for _, file := range files {
			assets[file.Name] = &file.Data
		}
	}
	// The current chart also contains native MCP support. Remove only those
	// additions before reconstructing and checking the fixed pre-proxy revision.
	for name, addition := range map[string]string{
		"agents/openclaw/config/native.tpl": `{{- if .Values.toolsMCP.enabled }}
{{- $mcp := dig "mcp" (dict) $config }}
{{- $servers := dig "servers" (dict) $mcp }}
{{- $_ := set $servers "agent-tools" (dict "url" .Values.toolsMCP.url "transport" "streamable-http" "sslVerify" true) }}
{{- $_ := set $mcp "servers" $servers }}
{{- $_ := set $config "mcp" $mcp }}
{{- else }}
{{- $servers := dig "mcp" "servers" (dict) $config }}
{{- $_ := unset $servers "agent-tools" }}
{{- end }}
`,
		"templates/_sandbox.tpl": ".Values.toolsMCP.enabled ",
	} {
		data, exists := assets[name]
		if !exists || bytes.Count(*data, []byte(addition)) != 1 {
			return fmt.Errorf("pre-proxy fixture requires the exact MCP addition in %s", name)
		}
		*data = bytes.Replace(*data, []byte(addition), nil, 1)
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
                    "description": "Exact compiler-selected ingress peer IPs; not tenant proxy trust",
                    "items": {
                        "type": "string",
                        "minLength": 1,
                        "anyOf": [{"format": "ipv4"}, {"format": "ipv6"}]
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
		if fmt.Sprintf("%x", sha256.Sum256(original)) != change.checksum {
			return fmt.Errorf("pre-proxy fixture %s differs from the original chart asset", name)
		}
		*data = original
	}
	return nil
}
