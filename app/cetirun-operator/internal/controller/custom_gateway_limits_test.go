// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"fmt"
	"slices"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/agents"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
)

func TestCustomGatewayLimitGraph(t *testing.T) {
	leaf := limitedSharedModel("leaf", 128000, 8192)
	second := limitedSharedModel("leaf", 256000, 4096)
	second.ID = managedTestDeploymentID("leaf", 4)
	router := gatewayRouter("router", tierModel("simple", "leaf"))
	router.MaxInputTokens, router.MaxOutputTokens = 1000000, 16384
	nested := gatewayRouter("nested", tierModel("simple", "router"))
	missing := gatewayRouter("missing", tierModel("simple", "unknown"))
	cycleA := gatewayRouter("cycle-a", tierModel("simple", "cycle-b"))
	cycleB := gatewayRouter("cycle-b", tierModel("simple", "cycle-a"))
	listing := []aigateway.Deployment{leaf, second, router, nested, missing, cycleA, cycleB}
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%v", reverse), func(t *testing.T) {
			models := slices.Clone(listing)
			if reverse {
				slices.Reverse(models)
			}
			view := newCustomGatewayLimitView(models)
			resolve := view.resolve(nil)
			for _, name := range []string{"leaf", "router", "nested"} {
				if got := resolve(name); got != [2]int64{128000, 4096} {
					t.Fatalf("%s: %v", name, got)
				}
			}
			for _, name := range []string{"missing", "cycle-a", "cycle-b", "unknown"} {
				if validCustomBudget(resolve(name)) {
					t.Fatalf("unproven %s accepted", name)
				}
			}
			fold := view.resolve(map[string][2]int64{"leaf": {64000, 2048}})
			if got := fold("nested"); got != [2]int64{64000, 2048} {
				t.Fatal(got)
			}
			if validCustomBudget(view.resolve(map[string][2]int64{"leaf": {}})("nested")) {
				t.Fatal("an incomplete private route was ignored through a router")
			}
		})
	}
	second.MaxOutputTokens = 0
	view := newCustomGatewayLimitView([]aigateway.Deployment{leaf, second, router})
	if validCustomBudget(view.resolve(nil)("router")) {
		t.Fatal("unknown deployment was ignored")
	}
	router.Router.Unverifiable = "synthetic unknown routing"
	if validCustomBudget(newCustomGatewayLimitView([]aigateway.Deployment{leaf, router}).resolve(nil)("router")) {
		t.Fatal("unverifiable routing was accepted")
	}
}

func TestCustomGatewayLimitsIgnoreOtherOwnersCopilotRoutes(t *testing.T) {
	leaf := limitedSharedModel("leaf", 128000, 4096)
	other := aigateway.Deployment{ModelName: "leaf", AccessGroups: []string{aiGatewayCopilotGroupPrefix + "other-owner"}}
	view := newCustomGatewayLimitView([]aigateway.Deployment{leaf, other})
	if got := view.resolve(nil)("leaf"); got != [2]int64{128000, 4096} {
		t.Fatal(got)
	}
	// This Agent's incomplete route must still fail closed.
	if validCustomBudget(view.resolve(map[string][2]int64{"leaf": {}})("leaf")) {
		t.Fatal("own route was ignored")
	}
}

func TestCustomGatewayLimitsProjectGrantedTeamAliasAtCatalogCapacity(t *testing.T) {
	instance := aiGatewayTestAgent("synthetic-team")
	instance.Spec.Agent.Type = api.AgentTypeCustom
	instance.Spec.ModelAccess = modelAccess(true, false)
	instance.Status.AIGateway = &api.AIGatewayStatus{}
	routerName := aiGatewayRouterPrefix + "synthetic"
	instance.Status.AIGateway.ModelRouters = []api.AIGatewayModelRouterStatus{{
		Name: "z-team-auto", Model: routerName, Alias: "z-team-auto", Granted: true,
		// These legacy display limits must not stand in for deployment proof.
		ContextWindow: 1000000, MaxTokens: 16384,
	}}
	listing := make([]aigateway.Deployment, 0, aiGatewayMaxModels)
	for i := 0; i < aiGatewayMaxModels-1; i++ {
		name := fmt.Sprintf("model-%03d", i)
		listing = append(listing, limitedSharedModel(name, 128000, 4096))
		instance.Status.AIGateway.AccessGroups = append(instance.Status.AIGateway.AccessGroups, gatewayRouterGroup(name))
	}
	// Team routers are not shared catalog entries; the key's granted alias
	// reaches the internal name recorded in this deployment graph.
	listing = append(listing, aigateway.Deployment{ModelName: routerName, Router: &aigateway.Router{
		Models: []aigateway.RouterModel{tierModel("simple", "model-000")},
	}})
	r := &AgentReconciler{}
	r.aiGatewayCatalog.update(listing, false, time.Now())
	models := r.aiGatewayCustomModels(instance)
	if len(models) != aiGatewayMaxModels {
		t.Fatalf("got %d entries, want %d", len(models), aiGatewayMaxModels)
	}
	if slices.ContainsFunc(models, func(m agents.GatewayModel) bool { return m.ID == routerName }) {
		t.Fatal("hidden internal name consumed an additional catalog slot")
	}
	alias := slices.IndexFunc(models, func(m agents.GatewayModel) bool { return m.ID == "z-team-auto" })
	if alias < 0 || models[alias].ContextWindow != 128000 || models[alias].MaxTokens != 4096 {
		t.Fatal("granted alias did not receive target limits")
	}
	listing[0].MaxOutputTokens = 0
	r.aiGatewayCatalog.update(listing, false, time.Now())
	if slices.ContainsFunc(r.aiGatewayCustomModels(instance), func(m agents.GatewayModel) bool { return m.ID == "z-team-auto" }) {
		t.Fatal("positive status limits concealed an unknown Team router target")
	}
	instance.Status.AIGateway.ModelRouters[0].Granted = false
	listing[0].MaxOutputTokens = 4096
	r.aiGatewayCatalog.update(listing, false, time.Now())
	if slices.ContainsFunc(r.aiGatewayCustomModels(instance), func(m agents.GatewayModel) bool { return m.ID == "z-team-auto" }) {
		t.Fatal("limits expanded the router grant")
	}
}

func TestCustomGatewayLimitsCompletenessChangesRevisionWithoutChangingNativeMinima(t *testing.T) {
	instance := aiGatewayTestAgent("synthetic-team")
	instance.Spec.Agent.Type = api.AgentTypeCustom
	instance.Spec.ModelAccess = modelAccess(true, false)
	instance.Status.AIGateway = &api.AIGatewayStatus{AccessGroups: []string{gatewayRouterGroup("leaf")}}
	r := &AgentReconciler{}
	listing := []aigateway.Deployment{limitedSharedModel("leaf", 128000, 4096)}
	r.aiGatewayCatalog.update(listing, false, time.Now())
	before := r.aiGatewayRevision(instance)
	if changed, _, _ := r.aiGatewayCatalog.update(listing, false, time.Now()); changed {
		t.Fatal("stable catalog changed")
	}
	if r.aiGatewayRevision(instance) != before {
		t.Fatal("stable limits changed revision")
	}
	unknown := limitedSharedModel("leaf", 0, 0)
	unknown.ID = managedTestDeploymentID("leaf", 4)
	listing = append(listing, unknown)
	if changed, _, _ := r.aiGatewayCatalog.update(listing, false, time.Now()); !changed {
		t.Fatal("completeness change was not enqueued")
	}
	if r.aiGatewayRevision(instance) == before || len(r.aiGatewayCustomModels(instance)) != 0 {
		t.Fatal("incomplete limits were kept by the steady-reconcile revision")
	}
	shared, _ := r.aiGatewayCatalog.shared()
	if len(shared) != 1 || shared[0].ContextWindow != 128000 || shared[0].MaxOutputTokens != 4096 {
		t.Fatal("native display behavior changed")
	}
	r.aiGatewayCatalog.known, r.aiGatewayCatalog.seeded = false, true
	if len(r.aiGatewayCustomModels(instance)) != 0 {
		t.Fatal("seeded display catalog was treated as deployment proof")
	}
}
