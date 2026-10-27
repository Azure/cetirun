// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"io"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	kubefake "helm.sh/helm/v3/pkg/kube/fake"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
	"sigs.k8s.io/yaml"
)

func customActionConfig() *action.Configuration {
	return &action.Configuration{
		Releases:     storage.Init(driver.NewMemory()),
		KubeClient:   &kubefake.PrintingKubeClient{Out: io.Discard},
		Capabilities: chartutil.DefaultCapabilities.Copy(),
		Log:          func(string, ...any) {},
	}
}

func platformRender(t *testing.T, p *PreparedRelease, capabilities *chartutil.Capabilities) map[string]map[string]any {
	t.Helper()
	values, err := chartutil.ToRenderValues(p.chart, p.Values, chartutil.ReleaseOptions{
		Name: p.releaseName, Namespace: "cetirun-example", IsInstall: true, Revision: 1,
	}, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := engine.Render(p.chart, values)
	if err != nil {
		t.Fatal(err)
	}
	objects := map[string]map[string]any{}
	for name, content := range rendered {
		if strings.HasSuffix(name, "NOTES.txt") {
			continue
		}
		for _, document := range strings.Split(content, "\n---") {
			var obj map[string]any
			if err := yaml.Unmarshal([]byte(document), &obj); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if len(obj) != 0 {
				objects[text(obj["kind"])] = obj
			}
		}
	}
	return objects
}

func sharedSkillValues() map[string]any {
	return map[string]any{
		"shared": true, "resourceGroup": "test-group", "storageAccount": "testskills",
		"containerName": "approved-skills", "storageClassName": "azureblob-nfs-premium",
		"storageSize": "100Gi", "protocol": "nfs", "npm": []any{},
	}
}
