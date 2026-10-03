// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"fmt"
	"strings"

	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	strictjson "sigs.k8s.io/json"
)

// SandboxSourcesConfig selects platform-owned inputs, not tenant overrides or
// execution evidence. It does not configure a writer-exclusion implementation.
type SandboxSourcesConfig struct {
	Enabled                 bool                    `json:"enabled"`
	PlatformConfigMapName   string                  `json:"platformConfigMapName"`
	HomeClaimName           string                  `json:"homeClaimName"`
	SkillsClaimName         string                  `json:"skillsClaimName"`
	AdmissionServiceAccount string                  `json:"admissionServiceAccount"`
	Helpers                 sandboxworkload.Helpers `json:"helpers"`
}

func ParseSandboxSourcesConfig(raw string) (SandboxSourcesConfig, error) {
	var config SandboxSourcesConfig
	if raw == "" {
		return config, nil
	}
	if len(raw) > 16*1024 || strings.TrimSpace(raw) == "null" {
		return config, fmt.Errorf("invalid CETIRUN_SANDBOX_SOURCES configuration")
	}
	problems, err := strictjson.UnmarshalStrict([]byte(raw), &config)
	if err != nil || len(problems) != 0 {
		return config, fmt.Errorf("invalid CETIRUN_SANDBOX_SOURCES configuration")
	}
	return config, config.validate()
}

func (config SandboxSourcesConfig) validate() error {
	if !config.Enabled {
		return nil
	}
	for _, field := range []struct{ name, value string }{
		{"platformConfigMapName", config.PlatformConfigMapName},
		{"homeClaimName", config.HomeClaimName},
		{"skillsClaimName", config.SkillsClaimName},
		{"admissionServiceAccount", config.AdmissionServiceAccount},
	} {
		if len(validation.IsDNS1123Subdomain(field.value)) != 0 {
			return fmt.Errorf("Sandbox sources require a valid %s", field.name)
		}
	}
	if config.HomeClaimName == config.SkillsClaimName {
		return fmt.Errorf("Sandbox HOME and skills must use distinct claims")
	}
	return nil
}

// NewKubernetesSandboxPreparation installs read-only sources for held
// preparation. A qualified writer-exclusion adapter must be supplied separately;
// the nil default never treats Kubernetes object state as fencing evidence.
func NewKubernetesSandboxPreparation(config SandboxSourcesConfig, reader client.Reader,
	installer SandboxHelmInstaller, namespace, chartVersion string,
) (*SandboxPreparation, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	if !config.Enabled {
		return nil, nil
	}
	if reader == nil || installer == nil || len(validation.IsDNS1123Label(namespace)) != 0 ||
		chartVersion == "" || strings.TrimSpace(chartVersion) != chartVersion {
		return nil, fmt.Errorf("Sandbox sources require an authoritative reader, installer, operator namespace and approved chart version")
	}
	return &SandboxPreparation{
		Configuration: &KubernetesSandboxConfigurationSource{
			APIReader: reader, Namespace: namespace, Name: config.PlatformConfigMapName,
		},
		Prerequisites: &KubernetesSandboxPrerequisites{
			APIReader: reader, HomeClaimName: config.HomeClaimName, SkillsClaimName: config.SkillsClaimName,
			ChartVersion: chartVersion, Helpers: config.Helpers,
		},
		Installer: installer, AdmissionServiceAccount: config.AdmissionServiceAccount,
	}, nil
}
