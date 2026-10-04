// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller_test

import (
	"strings"
	"testing"

	"github.com/Azure/CetiRun/app/cetirun-operator/internal/controller"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const sandboxSourcesConfigJSON = `{
	"enabled": true,
	"platformConfigMapName": "sandbox-platform",
	"homeClaimName": "home",
	"skillsClaimName": "skills",
	"admissionServiceAccount": "cetirun-sandbox-admission",
	"helpers": {"doctor": true, "logger": true, "desktop": false}
}`

func TestParseSandboxSourcesConfig(t *testing.T) {
	for _, test := range []struct {
		name, raw string
		enabled   bool
		wantError bool
	}{
		{name: "unset"},
		{name: "empty object", raw: "{}"},
		{name: "disabled", raw: `{"enabled":false}`},
		{name: "enabled", raw: sandboxSourcesConfigJSON, enabled: true},
		{name: "bootstrap enabled", raw: strings.Replace(sandboxSourcesConfigJSON, `"enabled": true`, `"enabled": true, "bootstrapOwnedPrerequisites": true`, 1), enabled: true},
		{name: "bootstrap requires sources", raw: `{"bootstrapOwnedPrerequisites":true}`, wantError: true},
		{name: "invalid bootstrap type", raw: `{"enabled":false,"bootstrapOwnedPrerequisites":"true"}`, wantError: true},
		{name: "malformed", raw: "{", wantError: true},
		{name: "null object", raw: "null", wantError: true},
		{name: "wrong object type", raw: "[]", wantError: true},
		{name: "wrong enabled type", raw: `{"enabled":"true"}`, wantError: true},
		{name: "duplicate enabled", raw: `{"enabled":true,"enabled":false}`, wantError: true},
		{name: "unknown setting", raw: `{"enabled":false,"allowExecution":true}`, wantError: true},
		{name: "unknown helper", raw: strings.Replace(sandboxSourcesConfigJSON, `"doctor"`, `"custom"`, 1), wantError: true},
		{name: "missing required inputs", raw: `{"enabled":true}`, wantError: true},
		{name: "empty platform name", raw: strings.Replace(sandboxSourcesConfigJSON, `"sandbox-platform"`, `""`, 1), wantError: true},
		{name: "invalid platform name", raw: strings.Replace(sandboxSourcesConfigJSON, `"sandbox-platform"`, `"Bad/Name"`, 1), wantError: true},
		{name: "invalid HOME name", raw: strings.Replace(sandboxSourcesConfigJSON, `"home"`, `"home "`, 1), wantError: true},
		{name: "invalid skills name", raw: strings.Replace(sandboxSourcesConfigJSON, `"skills"`, `"Skills"`, 1), wantError: true},
		{name: "same claims", raw: strings.Replace(sandboxSourcesConfigJSON, `"skills"`, `"home"`, 1), wantError: true},
		{name: "missing admission identity", raw: strings.Replace(sandboxSourcesConfigJSON, `"cetirun-sandbox-admission"`, `""`, 1), wantError: true},
		{name: "oversized config", raw: strings.Repeat(" ", 16*1024) + "{}", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, err := controller.ParseSandboxSourcesConfig(test.raw)
			if (err != nil) != test.wantError {
				t.Fatalf("error=%v, want error=%v", err, test.wantError)
			}
			if err == nil && config.Enabled != test.enabled {
				t.Fatalf("enabled=%v, want %v", config.Enabled, test.enabled)
			}
		})
	}
}

func TestSandboxSourcesFactoryDefaultsStayDisabled(t *testing.T) {
	prepare, err := controller.NewKubernetesSandboxPreparation(controller.SandboxSourcesConfig{}, nil, nil, "", "")
	if err != nil || prepare != nil {
		t.Fatalf("disabled sources installed dependencies: %+v %v", prepare, err)
	}
}

func TestSandboxSourcesFactoryUsesAuthoritativeReadersWithoutFencing(t *testing.T) {
	config, err := controller.ParseSandboxSourcesConfig(sandboxSourcesConfigJSON)
	if err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(testScheme()).Build()
	installer := &preparationInstaller{}
	prepare, err := controller.NewKubernetesSandboxPreparation(config, reader, installer, "platform-system", "0.1.1")
	if err != nil {
		t.Fatal(err)
	}
	source, ok := prepare.Configuration.(*controller.KubernetesSandboxConfigurationSource)
	if !ok || source.APIReader != reader || source.Namespace != "platform-system" || source.Name != config.PlatformConfigMapName {
		t.Fatal("configuration source was not bound to the selected operator namespace and reader")
	}
	prerequisites, ok := prepare.Prerequisites.(*controller.KubernetesSandboxPrerequisites)
	if !ok || prerequisites.APIReader != reader || prerequisites.HomeClaimName != config.HomeClaimName ||
		prerequisites.SkillsClaimName != config.SkillsClaimName || prerequisites.ChartVersion != "0.1.1" ||
		prerequisites.Helpers != (sandboxworkload.Helpers{Doctor: true, Logger: true}) {
		t.Fatal("prerequisite source was not bound to the approved native inputs")
	}
	if prerequisites.WriterExclusion != nil {
		t.Fatal("startup installed unqualified writer-exclusion evidence")
	}
	if prepare.Installer != installer || prepare.AdmissionServiceAccount != config.AdmissionServiceAccount {
		t.Fatal("factory changed the installer or admission identity")
	}
}

func TestSandboxSourcesFactoryRejectsMissingDependencies(t *testing.T) {
	config, err := controller.ParseSandboxSourcesConfig(sandboxSourcesConfigJSON)
	if err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(testScheme()).Build()
	installer := &preparationInstaller{}
	for _, test := range []struct {
		name                    string
		reader                  client.Reader
		installer               controller.SandboxHelmInstaller
		namespace, chartVersion string
	}{
		{"reader", nil, installer, "platform-system", "0.1.1"},
		{"installer", reader, nil, "platform-system", "0.1.1"},
		{"namespace", reader, installer, "", "0.1.1"},
		{"invalid namespace", reader, installer, "namespace.with.dots", "0.1.1"},
		{"chart version", reader, installer, "platform-system", ""},
		{"untrimmed version", reader, installer, "platform-system", " 0.1.1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			prepare, err := controller.NewKubernetesSandboxPreparation(config, test.reader, test.installer, test.namespace, test.chartVersion)
			if err == nil || prepare != nil {
				t.Fatalf("missing dependency was accepted: %+v %v", prepare, err)
			}
		})
	}
	config.HomeClaimName = config.SkillsClaimName
	if prepare, err := controller.NewKubernetesSandboxPreparation(config, reader, installer, "platform-system", "0.1.1"); err == nil || prepare != nil {
		t.Fatal("direct typed configuration bypassed startup validation")
	}
	config.HomeClaimName = "home"
	config.BootstrapOwnedPrerequisites = true
	for _, version := range []string{"latest", "v0.1.1", "0.1"} {
		if prepare, err := controller.NewKubernetesSandboxPreparation(config, reader, installer, "platform-system", version); err == nil || prepare != nil {
			t.Fatal("bootstrap accepted a non-semantic approved chart version")
		}
	}
}
