// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"slices"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

// AIGatewayModelRouterResponse is one smart-routing model of the Agent's Team
// for the owner: its name, whether the Agent may use it and, if not, why. The
// router's configuration stays with administrators.
type AIGatewayModelRouterResponse struct {
	Name    string `json:"name"`
	Granted bool   `json:"granted"`
	Reason  string `json:"reason,omitempty"`
}

// teamModelRouterNames are the names of the Team's routers a member may pick:
// with a policy, those the policy's model list names.
func teamModelRouterNames(team *api.Team, policy *api.AgentPolicy) []string {
	names := []string{}
	if team == nil {
		return names
	}
	for _, router := range team.Spec.ModelRouters {
		if policy != nil && !slices.Contains(policy.Spec.ModelAccess.SharedModels.Allowed, router.Name) {
			continue
		}
		names = append(names, router.Name)
	}
	return names
}

// applyModelRouterNames reports the Agent's routers and names granted ones by
// their own names instead of the internal Gateway names the Agent sends, so
// the owner sees, and chooses as the default, the name the Team defined.
func applyModelRouterNames(resp *AIGatewayResponse, status *api.AIGatewayStatus) {
	names := map[string]string{}
	for _, router := range status.ModelRouters {
		resp.ModelRouters = append(resp.ModelRouters, AIGatewayModelRouterResponse{
			Name: router.Name, Granted: router.Granted, Reason: router.Reason,
		})
		if router.Granted && router.Model != "" {
			names[router.Model] = router.Name
		}
	}
	for i, model := range resp.Models {
		if name, ok := names[model]; ok {
			resp.Models[i] = name
		}
	}
	if name, ok := names[resp.DefaultModel]; ok {
		resp.DefaultModel = name
	}
}
