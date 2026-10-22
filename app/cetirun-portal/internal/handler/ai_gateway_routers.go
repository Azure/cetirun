// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"slices"
	"strings"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

// AIGatewayModelRouterResponse is one smart-routing model of the Agent, its
// Team's or one configured in the Gateway, for the owner: its name, whether
// the Agent may use it and, if not, why. The router's configuration stays
// with administrators.
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
		if policy != nil && !teamRouterAllowed(router, policy) {
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

// sharedModelAllowed reports whether the policy lets its Agents use the
// shared model. A router configured in the Gateway also needs every model it
// sends requests to answer, as the operator grants it; one whose models the
// operator cannot read completely is refused, as there.
func sharedModelAllowed(model api.AIGatewayCatalogModel, policy *api.AgentPolicy) bool {
	allowed := policy.Spec.ModelAccess.SharedModels.Allowed
	if !slices.Contains(allowed, model.Name) || model.RouterUnverifiable {
		return false
	}
	for _, used := range model.RouterModels {
		if !slices.Contains(allowed, used) {
			return false
		}
	}
	return true
}

// teamRouterAllowed reports whether the policy lets its Agents use the
// Team's router: the policy lists the router and each shared model of its
// tiers, and allows GitHub Copilot when a tier uses a GitHub Copilot model,
// as the operator grants it. Whether the owner's GitHub account offers such a
// model is known only after sign-in.
func teamRouterAllowed(router api.TeamModelRouter, policy *api.AgentPolicy) bool {
	access := policy.Spec.ModelAccess
	if !slices.Contains(access.SharedModels.Allowed, router.Name) {
		return false
	}
	for _, model := range router.Tiers.Models() {
		if model == "" {
			continue
		}
		if strings.HasPrefix(model, api.AIGatewayCopilotModelPrefix) {
			if !access.Copilot.Allowed {
				return false
			}
		} else if !slices.Contains(access.SharedModels.Allowed, model) {
			return false
		}
	}
	return true
}
