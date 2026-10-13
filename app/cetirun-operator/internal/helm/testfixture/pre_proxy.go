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
// proxy hook and verifies their exact pre-proxy bytes. A checksum changes only
// when an independently tested protected-chart change intentionally updates
// the same asset.
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
			checksum: "e412f87dec464b688827249a69164cd34e5ac4960d12ecbac0c6473fe875a494",
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
			checksum: "105e8fe605d6d3b258f12b898bbb5b2aefc7f415c43cf5289bc137accf8b6485",
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
