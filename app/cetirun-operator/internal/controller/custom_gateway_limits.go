// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"maps"
	"slices"
	"strings"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/agents"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
)

// Separate from the native catalog's best-effort display limits: unknown
// deployments invalidate a Custom SDK budget, including through router tiers.
// Snapshots are immutable after construction and contain no provider secrets.
type customGatewayLimitView struct {
	limits            map[string][2]int64
	complete          map[string]bool
	routers           map[string]aigateway.Router
	routerFingerprint string
}

func validCustomBudget(value [2]int64) bool {
	const maximum = int64(1<<53) - 1
	return value[0] > 0 && value[0] <= maximum && value[1] > 0 && value[1] <= maximum
}

func newCustomGatewayLimitView(deployments []aigateway.Deployment) *customGatewayLimitView {
	v := &customGatewayLimitView{limits: map[string][2]int64{}, complete: map[string]bool{}, routers: map[string]aigateway.Router{}}
	for _, deployment := range deployments {
		// Other owners' private routes must not change this Agent's budget.
		// Its own registered routes are folded in from its approved model view.
		if copilotRoute(deployment) {
			continue
		}
		name := deployment.ModelName
		value := [2]int64{deployment.MaxInputTokens, deployment.MaxOutputTokens}
		complete, exists := v.complete[name]
		v.complete[name] = (!exists || complete) && validCustomBudget(value)
		previous := v.limits[name]
		v.limits[name] = [2]int64{smallerPositive(previous[0], value[0]), smallerPositive(previous[1], value[1])}
		if deployment.Router != nil {
			v.routers[name] = mergeRouters(v.routers[name], *deployment.Router)
		}
	}
	v.routerFingerprint = aiGatewayRouterFingerprint(v.routers)
	return v
}

func sameCustomGatewayLimits(a, b *customGatewayLimitView) bool {
	return a != nil && b != nil && a.routerFingerprint == b.routerFingerprint &&
		maps.Equal(a.limits, b.limits) && maps.Equal(a.complete, b.complete)
}

func (c *aiGatewayCatalog) customLimitSnapshot() *customGatewayLimitView {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.known { // A seeded display catalog cannot prove deployment completeness.
		return nil
	}
	return c.customLimits
}

// resolve folds every possible answer target, with this Agent's private routes
// overlaid by Gateway name. A zero route means its limits are unknown, not that
// the route can be ignored. Classifier/compactor calls are not answer budgets.
func (v *customGatewayLimitView) resolve(routes map[string][2]int64) func(string) [2]int64 {
	memo, visiting := map[string][2]int64{}, map[string]bool{}
	var resolve func(string) [2]int64
	resolve = func(name string) [2]int64 {
		if value, found := memo[name]; found {
			return value
		}
		if visiting[name] || len(visiting) >= 256 {
			return [2]int64{}
		}
		visiting[name] = true
		defer delete(visiting, name)
		value, present := v.limits[name]
		if router, found := v.routers[name]; found {
			if router.Unverifiable != "" {
				return [2]int64{}
			}
			var targets int
			for _, model := range router.Models {
				if model.Use != aigateway.RouterTier && model.Use != aigateway.RouterDefault {
					continue
				}
				targets++
				target := resolve(model.Name)
				if !validCustomBudget(target) {
					return [2]int64{}
				}
				value = [2]int64{smallerPositive(value[0], target[0]), smallerPositive(value[1], target[1])}
			}
			if targets == 0 {
				return [2]int64{}
			}
		} else if present && !v.complete[name] {
			return [2]int64{}
		}
		if route, found := routes[name]; found {
			if !validCustomBudget(route) {
				return [2]int64{}
			}
			value = [2]int64{smallerPositive(value[0], route[0]), smallerPositive(value[1], route[1])}
		}
		if !validCustomBudget(value) {
			return [2]int64{}
		}
		memo[name] = value
		return value
	}
	return resolve
}

// aiGatewayCustomModels projects limits only for already-authorized names. The
// live key's discovery list remains the runtime's authority for availability.
func (r *AgentReconciler) aiGatewayCustomModels(instance *api.Agent) []agents.GatewayModel {
	view := r.aiGatewayCatalog.customLimitSnapshot()
	if view == nil {
		return nil
	}
	entries, _, _ := r.aiGatewayModelEntries(instance)
	if r.aiGatewayCatalog.customLimitSnapshot() != view {
		return nil // Do not mix authorization names and limits from different listings.
	}
	parsed, _ := parseCopilotModels(instance.Annotations[api.AnnotationCopilotModels])
	copilot := map[string][2]int64{}
	for _, model := range parsed {
		copilot[model.ID] = [2]int64{model.ContextWindow, model.MaxOutputTokens}
	}
	routes := map[string][2]int64{}
	for _, route := range r.aiGatewayConfiguredCopilotRoutes(instance) {
		value := copilot[route.Model]
		if previous, exists := routes[route.Name]; exists {
			if !validCustomBudget(previous) || !validCustomBudget(value) {
				value = [2]int64{}
			} else {
				value = [2]int64{min(previous[0], value[0]), min(previous[1], value[1])}
			}
		}
		routes[route.Name] = value
	}
	resolve := view.resolve(routes)
	aliases := map[string]string{}
	if status := instance.Status.AIGateway; status != nil {
		for _, router := range status.ModelRouters {
			if router.Granted && router.Alias != "" && strings.HasPrefix(router.Model, aiGatewayRouterPrefix) {
				aliases[router.Model] = router.Alias
			}
		}
	}
	models := make([]agents.GatewayModel, 0, len(entries))
	for _, entry := range entries {
		value := resolve(entry.ID)
		if validCustomBudget(value) {
			id := entry.ID
			// LiteLLM lists a key's alias instead of the internal Team-router
			// name. Replace it, keeping the same model count at the catalog cap.
			if alias := aliases[id]; alias != "" {
				id = alias
			}
			models = append(models, agents.GatewayModel{ID: id, Name: entry.Name, ContextWindow: value[0], MaxTokens: value[1]})
		}
	}
	slices.SortFunc(models, func(a, b agents.GatewayModel) int { return strings.Compare(a.ID, b.ID) })
	return models
}
