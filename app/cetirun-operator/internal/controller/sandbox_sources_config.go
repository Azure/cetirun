// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"fmt"
	"strings"

	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxplacement"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"github.com/Masterminds/semver/v3"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	strictjson "sigs.k8s.io/json"
)

// SandboxSourcesConfig selects platform-owned inputs, not tenant overrides or
// caller-supplied execution evidence. The native adapter derives conservative
// ReadWriteOncePod writer-exclusion evidence from authoritative API reads.
type SandboxSourcesConfig struct {
	Enabled                     bool                    `json:"enabled"`
	BootstrapOwnedPrerequisites bool                    `json:"bootstrapOwnedPrerequisites"`
	PlatformConfigMapName       string                  `json:"platformConfigMapName"`
	HomeClaimName               string                  `json:"homeClaimName"`
	SkillsClaimName             string                  `json:"skillsClaimName"`
	SkillsCatalogConfigMapName  string                  `json:"skillsCatalogConfigMapName,omitempty"`
	PlacementConfigMapName      string                  `json:"placementConfigMapName,omitempty"`
	AdmissionServiceAccount     string                  `json:"admissionServiceAccount"`
	Helpers                     sandboxworkload.Helpers `json:"helpers"`
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
	if config.PlacementConfigMapName != "" &&
		(!config.Enabled || len(validation.IsDNS1123Subdomain(config.PlacementConfigMapName)) != 0 ||
			config.PlacementConfigMapName == config.PlatformConfigMapName ||
			config.PlacementConfigMapName == config.SkillsCatalogConfigMapName) {
		return fmt.Errorf("Sandbox placement requires enabled sources and a separate valid ConfigMap name")
	}
	if config.SkillsCatalogConfigMapName != "" &&
		(!config.Enabled || !config.BootstrapOwnedPrerequisites ||
			len(validation.IsDNS1123Subdomain(config.SkillsCatalogConfigMapName)) != 0 ||
			config.SkillsCatalogConfigMapName == config.PlatformConfigMapName) {
		return fmt.Errorf("Sandbox skills catalog requires enabled owned bootstrap and a separate valid ConfigMap name")
	}
	if !config.Enabled {
		if config.BootstrapOwnedPrerequisites {
			return fmt.Errorf("Sandbox bootstrap requires enabled Kubernetes sources")
		}
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

// NewKubernetesSandboxPreparation installs the native Kubernetes adapter. It
// supports only ReadWriteOncePod HOME volumes, a one-replica StatefulSet and
// controller-owned shutdown; it never force-deletes a Pod to manufacture a
// replacement writer.
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
	prerequisites := &KubernetesSandboxPrerequisites{
		APIReader: reader, HomeClaimName: config.HomeClaimName, SkillsClaimName: config.SkillsClaimName,
		OperatorNamespace: namespace, SkillsCatalogConfigMapName: config.SkillsCatalogConfigMapName,
		ChartVersion: chartVersion, Helpers: config.Helpers,
		WriterExclusion: &KubernetesRWOPWriterExclusion{APIReader: reader},
	}
	preparation := &SandboxPreparation{
		Configuration: &KubernetesSandboxConfigurationSource{
			APIReader: reader, Namespace: namespace, Name: config.PlatformConfigMapName,
		},
		Prerequisites: prerequisites,
		Installer:     installer, AdmissionServiceAccount: config.AdmissionServiceAccount,
	}
	if config.PlacementConfigMapName != "" {
		placement, err := sandboxplacement.NewHandler(reader, sandboxplacement.Config{
			Namespace: namespace, ConfigMapName: config.PlacementConfigMapName,
		})
		if err != nil {
			return nil, err
		}
		preparation.Placement = placement
	}
	if config.BootstrapOwnedPrerequisites {
		if _, err := semver.StrictNewVersion(chartVersion); err != nil {
			return nil, fmt.Errorf("Sandbox bootstrap requires an explicitly approved semantic chart version")
		}
		preparation.bootstrap = &sandboxBootstrapConfig{
			HomeClaimName: config.HomeClaimName, ChartVersion: chartVersion,
			SkillsClaimName: config.SkillsClaimName, SkillsCatalogConfigMapName: config.SkillsCatalogConfigMapName,
		}
	}
	return preparation, nil
}
