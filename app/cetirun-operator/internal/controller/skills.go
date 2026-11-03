// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"errors"
	"fmt"
	"slices"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
)

// errSkillMarketplaceOverride is the configuration error of an Agent whose
// skillOverrides add or remove a Skill Marketplace skill. The marketplace
// publishes each Team's selection, so the selection belongs on the Team.
var errSkillMarketplaceOverride = errors.New("skillOverrides cannot add or remove Skill Marketplace skills; select them on the Team")

// sandboxSkillsMode selects the empty skills source for a native Agent without
// a published catalog whose effective selection is empty, and the skills claim
// otherwise. With the Skill Marketplace (marketplace true), a Custom Agent
// drops marketplace skills (see customRuntimeValues), so they do not select
// the claim, which holds the subtree of every Team.
func sandboxSkillsMode(native bool, catalog string, marketplace bool, agent *api.Agent, team *api.Team) sandboxworkload.SkillsMode {
	selection := computeEffectiveSkills(team, agent.Spec.SkillOverrides.Add, agent.Spec.SkillOverrides.Remove)
	if marketplace && agent.Spec.Agent.Type == api.AgentTypeCustom {
		selection = slices.DeleteFunc(selection, api.IsSkillMarketplaceReference)
	}
	if native && catalog == "" && len(selection) == 0 {
		return sandboxworkload.SkillsModeEmpty
	}
	return ""
}

// applySkillValues keeps global, Team and per-Agent skill selection independent
// of the Agent runtime. Package materialization remains runtime-specific.
//
// With the Skill Marketplace (marketplace true), every entry without the npm:
// prefix names a marketplace skill, which the marketplace delivers for the
// Team: such entries never reach the reserved clawHub list, and an Agent's
// skillOverrides may not add or remove them. npm entries are unchanged.
func applySkillValues(values map[string]any, team *api.Team, overrides api.SkillOverrideSpec, storageAccount string, marketplace bool) error {
	if marketplace && (slices.ContainsFunc(overrides.Add, api.IsSkillMarketplaceReference) ||
		slices.ContainsFunc(overrides.Remove, api.IsSkillMarketplaceReference)) {
		return errSkillMarketplaceOverride
	}
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
		if marketplace {
			clawHub = nil
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
