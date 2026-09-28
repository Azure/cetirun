// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"fmt"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

// applySkillValues keeps global, Team and per-Agent skill selection independent
// of the Agent runtime. Package materialization remains runtime-specific.
func applySkillValues(values map[string]any, team *api.Team, overrides api.SkillOverrideSpec, storageAccount string) error {
	if team != nil && team.Spec.SkillStorage != nil {
		s := team.Spec.SkillStorage
		storage := map[string]any{
			"shared": true, "resourceGroup": s.ResourceGroup,
			"storageAccount": s.StorageAccount, "containerName": s.ContainerName,
		}
		for key, value := range map[string]string{
			"storageClassName": s.StorageClassName, "storageSize": s.StorageSize, "mountPath": s.MountPath,
		} {
			if value != "" {
				storage[key] = value
			}
		}
		values["skills"] = deepMerge(values["skills"], storage)
	}
	effective := computeEffectiveSkills(team, overrides.Add, overrides.Remove)
	if len(effective) > 0 {
		npm, clawHub, err := classifySkills(effective)
		if err != nil {
			return fmt.Errorf("classify skills: %w", err)
		}
		skills := map[string]any{}
		if len(npm) > 0 {
			skills["npm"] = stringsToInterfaces(npm)
		}
		if len(clawHub) > 0 {
			skills["clawHub"] = stringsToInterfaces(clawHub)
		}
		if len(skills) > 0 {
			values["skills"] = deepMerge(values["skills"], skills)
		}
	}
	if storageAccount != "" {
		values["skills"] = deepMerge(values["skills"], map[string]any{"storageAccount": storageAccount})
	}
	return nil
}
