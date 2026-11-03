// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"errors"
	"fmt"
	"strings"

	strictjson "sigs.k8s.io/json"
)

// maxSkillMarketplaceConfigBytes bounds CETIRUN_SKILL_MARKETPLACE.
const maxSkillMarketplaceConfigBytes = 1024

// SkillMarketplaceConfig is the operator's Skill Marketplace switch, read from
// CETIRUN_SKILL_MARKETPLACE. It is platform configuration, never a tenant
// override.
type SkillMarketplaceConfig struct {
	// Enabled makes every Team and Agent skill entry without the npm: prefix
	// a Skill Marketplace skill, which the marketplace delivers for the Team.
	Enabled bool `json:"enabled"`
}

// ParseSkillMarketplaceConfig reads the strict JSON object {"enabled": bool}.
// An empty value leaves the marketplace disabled.
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
	if problem != nil {
		return SkillMarketplaceConfig{}, fmt.Errorf(
			`invalid CETIRUN_SKILL_MARKETPLACE configuration, which must be {"enabled": true} or {"enabled": false}: %w`, problem)
	}
	return config, nil
}
