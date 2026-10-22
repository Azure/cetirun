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
	assets := map[string]*[]byte{"values.schema.json": &chrt.Schema}
	for _, files := range [][]*chart.File{chrt.Files, chrt.Templates} {
		for _, file := range files {
			assets[file.Name] = &file.Data
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
