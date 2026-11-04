// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"reflect"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/yaml"
)

func TestMemoryOptionsAndHashDoNotFollowPlatformChartVersion(t *testing.T) {
	r := &AgentReconciler{MemoryEnabled: true, MemoryRegistry: "registry.example.com/staging", ChartVersion: "1.2.0"}
	instance := &api.Agent{}
	instance.Spec.Helm.Version = "1.1.0"
	before, hash := r.memoryOptions(instance.Spec.Agent.Type), r.configHash(instance, nil, "", "")
	r.ChartVersion = "1.3.0"
	if r.memoryOptions(instance.Spec.Agent.Type) != before || r.configHash(instance, nil, "", "") != hash {
		t.Fatal("platform default upgrade changed a pinned Agent's Memory configuration")
	}
	r.MemoryImage = "registry.example.com/memory:2.0.0"
	if r.configHash(instance, nil, "", "") == hash || r.memoryOptions(instance.Spec.Agent.Type).Image != r.MemoryImage {
		t.Fatal("explicit asset override must invalidate the runtime configuration")
	}
	r.MemoryImage, r.MemoryEnabled = "", false
	if r.memoryOptions(instance.Spec.Agent.Type).Enabled || r.configHash(instance, nil, "", "") == hash {
		t.Fatal("disabling Memory must invalidate the enabled configuration")
	}
}

func TestHermesMemoryImageSelection(t *testing.T) {
	for _, test := range []struct {
		name    string
		kind    api.AgentType
		enabled bool
		shared  string
		hermes  string
		want    helm.MemoryOptions
	}{
		{"shared fallback", api.AgentTypeHermes, true, "shared:1", "", helm.MemoryOptions{Enabled: true, Image: "shared:1"}},
		{"chart fallback", api.AgentTypeHermes, true, "", "", helm.MemoryOptions{Enabled: true}},
		{"Hermes override", api.AgentTypeHermes, true, "shared:1", "hermes:2", helm.MemoryOptions{Enabled: true, Image: "hermes:2"}},
		{"OpenClaw shared image", api.AgentTypeOpenClaw, true, "shared:1", "hermes:2", helm.MemoryOptions{Enabled: true, Image: "shared:1"}},
		{"Custom shared image", api.AgentTypeCustom, true, "shared:1", "hermes:2", helm.MemoryOptions{Enabled: true, Image: "shared:1"}},
		{"Hermes explicit image enables assets", api.AgentTypeHermes, false, "", "hermes:2", helm.MemoryOptions{Enabled: true, Image: "hermes:2"}},
		{"Hermes image leaves OpenClaw disabled", api.AgentTypeOpenClaw, false, "", "hermes:2", helm.MemoryOptions{}},
		{"Hermes image leaves Custom disabled", api.AgentTypeCustom, false, "", "hermes:2", helm.MemoryOptions{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := &AgentReconciler{MemoryEnabled: test.enabled, MemoryImage: test.shared, HermesMemoryImage: test.hermes, MemoryRegistry: "registry.example.com"}
			test.want.Registry = r.MemoryRegistry
			if got := r.memoryOptions(test.kind); got != test.want {
				t.Fatalf("memory options = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestHermesMemoryImageOnlyChangesSelectedHarness(t *testing.T) {
	for _, kind := range []api.AgentType{api.AgentTypeHermes, api.AgentTypeOpenClaw, api.AgentTypeCustom} {
		t.Run(string(kind), func(t *testing.T) {
			r := &AgentReconciler{MemoryEnabled: true, MemoryImage: "registry.example.com/memory:1.0.0"}
			instance := &api.Agent{}
			instance.Spec.Agent.Type = kind
			original := instance.DeepCopy()
			hashes := func() [2]string {
				t.Helper()
				maintenance, err := r.maintenanceIntentHash(context.Background(), instance)
				if err != nil {
					t.Fatal(err)
				}
				return [2]string{r.configHash(instance, nil, "", ""), maintenance}
			}
			beforeOptions, beforeHashes := r.memoryOptions(kind), hashes()
			r.HermesMemoryImage = "registry.example.com/memory:2.0.0"
			afterOptions, afterHashes := r.memoryOptions(kind), hashes()
			wantChanged := kind == api.AgentTypeHermes
			if (beforeOptions != afterOptions) != wantChanged {
				t.Fatal("Hermes override changed the wrong harness image selection")
			}
			for i := range beforeHashes {
				if (beforeHashes[i] != afterHashes[i]) != wantChanged {
					t.Fatal("Hermes override changed the wrong restart or maintenance hash")
				}
			}
			if !reflect.DeepEqual(instance, original) {
				t.Fatal("Memory selection changed the Agent spec or metadata")
			}
			r.HermesMemoryImage = ""
			if r.memoryOptions(kind) != beforeOptions || hashes() != beforeHashes {
				t.Fatal("removing the override did not restore shared selection and hashes")
			}
			r.HermesMemoryImage = "registry.example.com/memory:2.0.0"
			r.MemoryImage = "registry.example.com/memory:3.0.0"
			sharedHashes := hashes()
			sharedChanged := kind != api.AgentTypeHermes
			for i := range afterHashes {
				if (sharedHashes[i] != afterHashes[i]) != sharedChanged {
					t.Fatal("shared image change invalidated a Hermes override or missed another harness")
				}
			}
			instance.Spec.RestartRequestID = "owner-requested-restart"
			restartedHashes := hashes()
			for i := range sharedHashes {
				if restartedHashes[i] == sharedHashes[i] {
					t.Fatal("Memory selection masked the requested restart")
				}
			}
		})
	}
}

func TestHermesMemoryImageRenderIsolation(t *testing.T) {
	for _, kind := range []api.AgentType{api.AgentTypeHermes, api.AgentTypeOpenClaw} {
		t.Run(string(kind), func(t *testing.T) {
			r := &AgentReconciler{MemoryEnabled: true, MemoryImage: "registry.example.com/memory:1.0.0"}
			before := renderMemoryWorkload(t, kind, r.memoryOptions(kind))
			r.HermesMemoryImage = "registry.example.com/memory:2.0.0"
			after := renderMemoryWorkload(t, kind, r.memoryOptions(kind))
			if kind == api.AgentTypeHermes {
				found := false
				for i, container := range before.Template.Spec.InitContainers {
					if container.Name == "memory-assets" {
						before.Template.Spec.InitContainers[i].Image = r.HermesMemoryImage
						found = true
					}
				}
				if !found {
					t.Fatal("Memory initializer missing from rendered workload")
				}
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatal("Memory override changed fields beyond the Hermes initializer image")
			}
		})
	}
}

func renderMemoryWorkload(t *testing.T, kind api.AgentType, options helm.MemoryOptions) appsv1.StatefulSetSpec {
	t.Helper()
	chart, err := loader.Load("../../../cetirun-chart")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := helm.PrepareChart(chart, map[string]any{
		"agentType": string(kind), "agentRequest": map[string]any{"type": string(kind)},
		"secrets":       map[string]any{"provider": "openai", "existingSecret": "native-auth", "model": "synthetic-model", "apiBase": "http://gateway.example.com/v1"},
		"aiGateway":     map[string]any{"enabled": true, "keySecretName": "model-key", "keySecretKey": "api-key"},
		"hermes":        map[string]any{"env": map[string]any{"HERMES_WEBAPP_PUBLIC_URL": "https://agents.example.com/ui/example"}},
		"memoryOptions": options,
	})
	if err != nil {
		t.Fatal(err)
	}
	values, err := chartutil.ToRenderValues(chart, prepared.Values, chartutil.ReleaseOptions{Name: "example", Namespace: "example", IsInstall: true}, chartutil.DefaultCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := engine.Render(chart, values)
	if err != nil {
		t.Fatal(err)
	}
	var workload appsv1.StatefulSet
	if err := yaml.Unmarshal([]byte(rendered["cetirun/templates/statefulset.yaml"]), &workload); err != nil {
		t.Fatal(err)
	}
	return workload.Spec
}
