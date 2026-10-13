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
// proxy hook and verifies their exact bytes at public revision 3a9e4de5423a64f40a945c1d2941cba3d691cdbe.
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
			addition: `{{- if and .Values.sandboxWorkload (hasKey .Values.sandboxWorkload "trustedProxyIPs") }}
{{- $gateway := dig "gateway" (dict) $config }}
{{- $_ := set $gateway "trustedProxies" (.Values.sandboxWorkload.trustedProxyIPs | default list) }}
{{- $_ := set $config "gateway" $gateway }}
{{- end }}
`,
			checksum: "b48917b34fe70814e3de7b9e67337f667db980d77f04e96621f550e6a0e5a05f",
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
			checksum: "f82f8be07eda05d20260def6b7f6e7b3cf8879cd07eda3220c9c222617e67234",
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
