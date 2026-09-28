// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"strings"
	"testing"

	"github.com/Azure/CetiRun/app/cetirun-operator/internal/runtimebindings"
	"helm.sh/helm/v3/pkg/chartutil"
)

func TestCustomImageManagedAuthentication(t *testing.T) {
	for _, port := range []any{nil, 8080} {
		input := customInput(port)
		b := input["customBindings"].(runtimebindings.Custom)
		b.AgentManagedModel = true
		b.Model = runtimebindings.ModelAccess{}
		b.Network.Gateway = runtimebindings.Peer{}
		b.Network.GatewayPort = 0
		input["customBindings"] = b
		input["skills"] = sharedSkillValues()
		p, err := PrepareChart(testChart(t), input)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(p.manifest), "OPENAI_API_KEY") || strings.Contains(string(p.manifest), "OPENAI_BASE_URL") {
			t.Fatal("image-owned authentication received a fabricated model binding")
		}
		objects := platformRender(t, p, chartutil.DefaultCapabilities)
		for _, kind := range []string{"NetworkPolicy", "ValidatingAdmissionPolicy", "PersistentVolume", "PersistentVolumeClaim"} {
			if objects[kind] == nil {
				t.Fatalf("image-owned authentication lost %s", kind)
			}
		}
		caps := chartutil.DefaultCapabilities.Copy()
		caps.APIVersions = append(caps.APIVersions, "networking.istio.io/v1/Sidecar")
		objects = platformRender(t, p, caps)
		egress := array(object(objects["Sidecar"]["spec"])["egress"])
		for _, host := range array(object(egress[0])["hosts"]) {
			if host == "/*" {
				t.Fatal("absent Gateway broadened mesh scope")
			}
		}
		b.Model = input["customBindings"].(runtimebindings.Custom).Model
		b.Model.Name = "unexpected-model-secret"
		input["customBindings"] = b
		if _, err := PrepareChart(testChart(t), input); err == nil {
			t.Fatal("mixed image-owned and platform credentials were accepted")
		}
	}
}
