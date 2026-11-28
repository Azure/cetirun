// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
	strictjson "sigs.k8s.io/json"
)

// maxSkillMarketplaceConfigBytes bounds CETIRUN_SKILL_MARKETPLACE.
const maxSkillMarketplaceConfigBytes = 1024

// SkillMarketplaceConfig is the operator's Skill Marketplace configuration,
// read from CETIRUN_SKILL_MARKETPLACE. It is platform configuration, never a
// tenant override.
type SkillMarketplaceConfig struct {
	// Enabled makes every Team and Agent skill entry without the npm: prefix
	// a Skill Marketplace skill, which the marketplace delivers for the Team.
	Enabled bool `json:"enabled"`
	// Storage is the Blob NFS container of the Team releases. The operator
	// gives each Agent of a policy-bound Team a read-only claim of it. It is
	// required while the marketplace is enabled.
	Storage *SkillMarketplaceStorage `json:"storage,omitempty"`
}

// SkillMarketplaceStorage names the platform-owned Team release storage.
// It holds no credentials: NFS mounts authorize by network.
type SkillMarketplaceStorage struct {
	Backend          string `json:"backend,omitempty"`
	ResourceGroup    string `json:"resourceGroup"`
	StorageAccount   string `json:"storageAccount"`
	ContainerName    string `json:"containerName"`
	ShareName        string `json:"shareName,omitempty"`
	StorageClassName string `json:"storageClassName"`
}

// ParseSkillMarketplaceConfig reads the strict JSON object
// {"enabled": bool, "storage": {...}}. An empty value leaves the marketplace
// disabled.
func ParseSkillMarketplaceConfig(raw string) (SkillMarketplaceConfig, error) {
	var config SkillMarketplaceConfig
	if raw == "" {
		return config, nil
	}
	var problem error
	switch {
	case len(raw) > maxSkillMarketplaceConfigBytes:
		problem = fmt.Errorf("it is longer than %d bytes", maxSkillMarketplaceConfigBytes)
	case strings.TrimSpace(raw) == "null":
		problem = errors.New("null is not a JSON object")
	default:
		strict, err := strictjson.UnmarshalStrict([]byte(raw), &config)
		problem = errors.Join(append([]error{err}, strict...)...)
	}
	if problem == nil && config.Enabled {
		problem = config.Storage.validate()
	}
	if problem != nil {
		return SkillMarketplaceConfig{}, fmt.Errorf(
			`invalid CETIRUN_SKILL_MARKETPLACE configuration, which must be {"enabled": false} or {"enabled": true, "storage": {...}}: %w`, problem)
	}
	return config, nil
}

func (s *SkillMarketplaceStorage) validate() error {
	if s == nil {
		return errors.New("storage is required while the marketplace is enabled")
	}
	if s.Backend != "" && s.Backend != "blob" && s.Backend != "azure-files-nfs" {
		return errors.New("storage.backend must be blob or azure-files-nfs")
	}
	if s.Backend != "azure-files-nfs" && s.ShareName != "" {
		return errors.New("storage.shareName requires the Azure Files backend")
	}
	switch {
	case !sandboxResourceGroup.MatchString(s.ResourceGroup) || strings.HasSuffix(s.ResourceGroup, "."):
		return errors.New("storage.resourceGroup is not a resource group name")
	case !sandboxStorageAccount.MatchString(s.StorageAccount):
		return errors.New("storage.storageAccount must be 3-24 lowercase letters and digits")
	case s.Backend == "azure-files-nfs" && (s.ContainerName != "" || len(s.ShareName) < 3 || len(validation.IsDNS1123Label(s.ShareName)) != 0 || strings.Contains(s.ShareName, "--")):
		return errors.New("storage.shareName is required and storage.containerName must be empty for Azure Files")
	case s.Backend != "azure-files-nfs" && (len(s.ContainerName) < 3 || len(validation.IsDNS1123Label(s.ContainerName)) != 0 || strings.Contains(s.ContainerName, "--")):
		return errors.New("storage.containerName is not a Blob container name")
	case len(validation.IsDNS1123Subdomain(s.StorageClassName)) != 0:
		return errors.New("storage.storageClassName is not a StorageClass name")
	}
	return nil
}
