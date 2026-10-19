// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"hash/fnv"
	"maps"
	"slices"
	"testing"
	"time"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
)

func TestCopilotModelMatchingNormalizesNamesAndRejectsSnapshots(t *testing.T) {
	for model, want := range map[string]string{
		"azure/GPT-4.1": "gpt-4-1", "openai/gpt-5.5": "gpt-5-5", "claude-sonnet-4.5": "claude-sonnet-4-5",
		"vertex_ai/anthropic/claude-sonnet-4-5": "claude-sonnet-4-5", "": "",
	} {
		if got := realModelKey(model); got != want {
			t.Errorf("realModelKey(%q) = %q, want %q", model, got, want)
		}
	}
	for id, dated := range map[string]bool{
		"gpt-4.1-2025-04-14": true, "gpt-4-0613": true, "claude-3-5-sonnet-20241022": true, "gpt-4-1106-preview": true,
		"gpt-4.1": false, "claude-sonnet-4.5": false, "gemini-2.5-pro": false, "o4-mini": false, "grok-code-fast-1": false,
	} {
		if got := datedModelID(id); got != dated {
			t.Errorf("datedModelID(%q) = %v, want %v", id, got, dated)
		}
	}
	priced := map[string]bool{"azure/gpt-4.1": true}
	for name, test := range map[string]struct {
		deployments []aigateway.Deployment
		want        string
	}{
		"base model wins":           {[]aigateway.Deployment{{Model: "azure/my-deployment", BaseModel: "azure/gpt-4.1"}}, "gpt-4-1"},
		"priced provider model":     {[]aigateway.Deployment{{Model: "azure/gpt-4.1"}}, "gpt-4-1"},
		"unpriced model is unknown": {[]aigateway.Deployment{{Model: "azure/my-deployment"}}, ""},
		"deployments agree":         {[]aigateway.Deployment{{Model: "azure/gpt-4.1"}, {BaseModel: "openai/gpt-4.1"}}, "gpt-4-1"},
		"deployments disagree":      {[]aigateway.Deployment{{Model: "azure/gpt-4.1"}, {BaseModel: "gpt-4.1-mini"}}, ""},
		"one unknown":               {[]aigateway.Deployment{{Model: "azure/gpt-4.1"}, {Model: "azure/other"}}, ""},
	} {
		if got := sharedRealModel(test.deployments, priced); got != test.want {
			t.Errorf("%s: sharedRealModel = %q, want %q", name, got, test.want)
		}
	}
}

func TestCopilotRoutingCopiesThePrimaryLevel(t *testing.T) {
	order := func(v int64) *int64 { return &v }
	weight := func(v float64) *float64 { return &v }
	routing := copilotRouting([]aigateway.Deployment{
		{Routing: aigateway.Routing{Order: order(2), Weight: weight(9), RPM: order(900)}},
		{Routing: aigateway.Routing{Order: order(1), Weight: weight(2), TPM: order(1000)}},
		{Routing: aigateway.Routing{Order: order(1), Weight: weight(3)}},
	})
	if routing.String() != "order=1,weight=3,tpm=1000" {
		t.Fatalf("a route joins the primary level with its largest weight: %s", routing)
	}
	if unordered := copilotRouting([]aigateway.Deployment{{Routing: aigateway.Routing{RPM: order(60)}}, {}}); unordered.String() != "rpm=60" {
		t.Fatalf("without order every deployment counts: %s", unordered)
	}
	if none := copilotRouting([]aigateway.Deployment{{}, {}}); !none.IsZero() {
		t.Fatalf("deployments without routing fields give none: %s", none)
	}
}

// sharedDeployment is a chart-configured shared deployment: its ID and group
// take the form the Gateway chart renders for name, and suffix stands for its
// configuration, so entries of one name share the group.
func sharedDeployment(suffix, name string, mutate func(*aigateway.Deployment)) aigateway.Deployment {
	entry := fnv.New32a()
	_, _ = entry.Write([]byte(suffix))
	deployment := aigateway.Deployment{
		ID: managedTestDeploymentID(name, int(entry.Sum32())), ModelName: name, DatabaseSourceKnown: true, Priced: true,
		AccessGroups: []string{aiGatewaySharedAccessGroup, aiGatewayModelAccessGroupPrefix + aiGatewayModelBinding(name)},
	}
	if mutate != nil {
		mutate(&deployment)
	}
	return deployment
}

// routesByModel maps each registered Copilot model of the test Agent to its
// route in the Gateway.
func routesByModel(gateway *fakeAIGateway) map[string]aigateway.ModelRequest {
	routes := map[string]aigateway.ModelRequest{}
	for _, route := range gateway.models {
		routes[route.Model[len("litellm_proxy/"):]] = route
	}
	return routes
}

func TestReconcileAIGatewayCopilotJoinsTheSharedModelOfTheSameRealModel(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	agent.Spec.Model = "copilot/gpt-5.5"
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace(), readyProxy(1))
	one, two := int64(1), int64(2)
	gateway.shared = append(gateway.shared,
		sharedDeployment("team-gpt-a", "team-gpt", func(d *aigateway.Deployment) {
			d.BaseModel, d.MaxInputTokens, d.MaxOutputTokens = "openai/gpt-5.5", 1000000, 64000
			d.Routing = aigateway.Routing{Order: &one, Weight: ptrFloat(2)}
		}),
		sharedDeployment("team-gpt-b", "team-gpt", func(d *aigateway.Deployment) {
			d.BaseModel = "gpt-5.5"
			d.Routing = aigateway.Routing{Order: &two, Weight: ptrFloat(9)}
		}),
		// The Copilot model's own ID, but another model.
		sharedDeployment("claude-other", "claude-sonnet-4.5", func(d *aigateway.Deployment) { d.BaseModel = "anthropic/claude-opus-4" }),
		// The same model, but not granted to this Agent.
		sharedDeployment("gpt41", "gpt-4.1", func(d *aigateway.Deployment) { d.Model = "azure/gpt-4.1" }),
		// A snapshot is never merged, even into the same snapshot.
		sharedDeployment("team-4o", "team-4o", func(d *aigateway.Deployment) { d.BaseModel = "gpt-4o-2024-11-20" }),
	)
	gateway.priced["azure/gpt-4.1"], gateway.priced["gpt-4o-2024-11-20"] = true, true
	allowTestPolicyModels(t, kube, "team-gpt", "claude-sonnet-4.5", "team-4o")
	ctx := context.Background()
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	signIn(t, kube, agent, `[{"id":"gpt-5.5","name":"GPT-5.5","contextWindow":400000,"maxOutputTokens":128000},`+
		`{"id":"claude-sonnet-4.5"},{"id":"gpt-4.1"},{"id":"gpt-4o-2024-11-20","name":"GPT-4o"},{"id":"grok-code-fast-1"}]`)
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	copilot := agent.Status.AIGateway.Copilot
	if copilot.Phase != cetirunv1alpha1.AIGatewayCopilotPhaseReady {
		t.Fatalf("copilot status %+v", copilot)
	}
	routes := routesByModel(gateway)
	merged, own, joined := routes["gpt-5.5"], routes["gpt-4o-2024-11-20"], routes["gpt-4.1"]
	if len(routes) != 3 || merged.ModelName != "team-gpt" || merged.NoCooldown || merged.Routing.String() != "order=1,weight=2" ||
		!slices.Equal(merged.AccessGroups, []string{copilotGroup()}) || merged.BaseModel != "gpt-5.5" {
		t.Fatalf("the Copilot model joins the shared model at its primary level: %+v", routes)
	}
	if own.ModelName != "gpt-4o-2024-11-20" || !own.NoCooldown || !own.Routing.IsZero() {
		t.Fatalf("a dated model keeps its own name and is never cooled down: %+v", own)
	}
	for _, route := range routes {
		if !route.ProbeWithoutRetries {
			t.Fatalf("every Copilot route is probed without the provider client's retries: %+v", route)
		}
	}
	// The same model as a shared model the Agent is not granted, under that
	// name: only the Agent's own access group reaches the route, so the
	// Agent's key gets its own Copilot route there and never the shared
	// deployment; it shares nothing, so it is never cooled down.
	if joined.ModelName != "gpt-4.1" || !joined.NoCooldown || !joined.Routing.IsZero() ||
		!slices.Equal(joined.AccessGroups, []string{copilotGroup()}) {
		t.Fatalf("a Copilot model the Agent is not granted the shared model of keeps the name in its own group: %+v", joined)
	}
	wantRoutes := []cetirunv1alpha1.AIGatewayCopilotRoute{
		{Model: "gpt-4.1", Name: "gpt-4.1"}, {Model: "gpt-4o-2024-11-20", Name: "gpt-4o-2024-11-20"},
		{Model: "gpt-5.5", Name: "team-gpt", Shared: true},
	}
	if !slices.Equal(copilot.Routes, wantRoutes) || !slices.Equal(copilot.Models, []string{"gpt-4.1", "gpt-4o-2024-11-20", "team-gpt"}) ||
		!slices.Equal(copilot.Skipped, []string{"claude-sonnet-4.5"}) || !slices.Equal(copilot.Unpriced, []string{"grok-code-fast-1"}) {
		t.Fatalf("copilot status %+v", copilot)
	}
	// The Agent sees one entry per model: the shared model stands for the
	// Copilot model, with the smaller limits.
	wantModels := []string{"claude-sonnet-4.5", testSharedModelOne, "team-4o", "team-gpt", "gpt-4.1", "gpt-4o-2024-11-20"}
	if !slices.Equal(agent.Status.AIGateway.Models, wantModels) || agent.Status.AIGateway.DefaultModel != "team-gpt" {
		t.Fatalf("models %v default %q", agent.Status.AIGateway.Models, agent.Status.AIGateway.DefaultModel)
	}
	entries, _, _ := r.aiGatewayModelEntries(agent)
	for _, entry := range entries {
		if entry.ID == "team-gpt" && (entry.ContextWindow != 400000 || entry.MaxTokens != 64000 || entry.Name != "team-gpt") {
			t.Fatalf("a merged entry takes the smaller limits: %+v", entry)
		}
		if entry.ID == "gpt-4o-2024-11-20" && entry.Name != "gpt-4o-2024-11-20" {
			t.Fatalf("a Copilot entry is named like any other model: %+v", entry)
		}
	}
	key := gateway.keys[aigateway.Token(string(readSecret(t, kube, aiGatewayKeySecretName).Data[aiGatewayKeySecretField]))]
	wantAliases := map[string]string{"copilot/gpt-5.5": "team-gpt", "copilot/gpt-4o-2024-11-20": "gpt-4o-2024-11-20", "copilot/gpt-4.1": "gpt-4.1"}
	if !maps.Equal(key.Aliases, wantAliases) {
		t.Fatalf("earlier copilot/ names stay usable as key aliases: %v", key.Aliases)
	}
	// A route under a shared name changes neither the shared models nor the
	// policy's view of them.
	if r.refreshAIGatewayCatalogWithin(ctx, gateway, 0) {
		t.Fatal("a Copilot route sharing a name must not count as a catalog change")
	}

	// Steady state: nothing is registered again, the aliases are not rewritten.
	added, aliasUpdates := len(gateway.added), len(gateway.aliasUpdates)
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	if len(gateway.added) != added || len(gateway.aliasUpdates) != aliasUpdates {
		t.Fatal("a steady reconcile must not re-register routes or rewrite aliases")
	}

	// The shared deployments change their routing: the route follows.
	gateway.shared[2].Routing.Weight = ptrFloat(5)
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	if routes := routesByModel(gateway); routes["gpt-5.5"].Routing.String() != "order=1,weight=5" || routes["gpt-5.5"].ID == merged.ID ||
		!slices.Contains(gateway.deletedModels, merged.ID) {
		t.Fatalf("a changed routing replaces the route: %+v deleted %v", routes, gateway.deletedModels)
	}

	// A deployment default may name a Copilot model the earlier way too.
	agent.Spec.Model = ""
	r.AIGateway.DefaultModel = "copilot/gpt-4o-2024-11-20"
	if _, def, _ := r.aiGatewayModelEntries(agent); def != "gpt-4o-2024-11-20" {
		t.Fatalf("a copilot/<ID> deployment default names the model's route: %q", def)
	}
}

func ptrFloat(value float64) *float64 { return &value }

func TestReconcileAIGatewayCopilotFollowsSharedModelsOfALegacyAgent(t *testing.T) {
	r, agent, gateway := legacyGatewayFixture(t)
	r.AIGateway.CopilotProxyImage = testProxyImage
	ctx := context.Background()
	if err := r.Create(ctx, readyProxy(1)); err != nil {
		t.Fatal(err)
	}
	base := slices.Clone(gateway.shared)
	shared := aigateway.Deployment{ID: "legacy-gpt", ModelName: "gpt-x", AccessGroups: []string{aiGatewaySharedAccessGroup},
		BaseModel: "gpt-5.5", Routing: aigateway.Routing{Weight: ptrFloat(3)}}
	gateway.shared = append(gateway.shared, shared)
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	signIn(t, r.Client, agent, `[{"id":"gpt-5.5"}]`)
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	route := routesByModel(gateway)["gpt-5.5"]
	if route.ModelName != "gpt-x" || route.Routing.String() != "weight=3" || route.NoCooldown {
		t.Fatalf("a legacy Agent's Copilot model joins every shared model of the same real model: %+v", route)
	}
	if slices.Contains(agent.Status.AIGateway.Models, "gpt-5.5") || !slices.Contains(agent.Status.AIGateway.Models, "gpt-x") {
		t.Fatalf("the Agent lists the model once: %v", agent.Status.AIGateway.Models)
	}

	// The shared model goes: the route takes the Copilot model's own name.
	gateway.shared = slices.Clone(base)
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	moved := routesByModel(gateway)["gpt-5.5"]
	if len(gateway.models) != 1 || moved.ModelName != "gpt-5.5" || !moved.NoCooldown || !slices.Contains(gateway.deletedModels, route.ID) {
		t.Fatalf("the route moves to the model's own name: %+v", gateway.models)
	}
	key := gateway.keys[aigateway.Token(string(readSecret(t, r.Client, aiGatewayKeySecretName).Data[aiGatewayKeySecretField]))]
	if !maps.Equal(key.Aliases, map[string]string{"copilot/gpt-5.5": "gpt-5.5"}) || !slices.Contains(agent.Status.AIGateway.Models, "gpt-5.5") {
		t.Fatalf("aliases %v models %v", key.Aliases, agent.Status.AIGateway.Models)
	}

	// With shared models off the Agent is granted none, so a model the shared
	// deployments also serve keeps its own name.
	gateway.shared = append(slices.Clone(base), shared)
	agent.Spec.ModelAccess = modelAccess(false, true)
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	if own := routesByModel(gateway)["gpt-5.5"]; own.ModelName != "gpt-5.5" || !own.NoCooldown {
		t.Fatalf("an Agent without shared models keeps the Copilot model's own name: %+v", own)
	}

	// Copilot off: routes and aliases go.
	agent.Spec.ModelAccess = modelAccess(true, false)
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	if len(gateway.models) != 0 || len(key.Aliases) != 0 {
		t.Fatalf("turning Copilot off removes routes and aliases: %v %v", gateway.modelNames(), key.Aliases)
	}
}

func TestPlaceCopilotModelsPicksTheFirstGrantedSharedName(t *testing.T) {
	r, _, _ := aiGatewayTestReconciler(t)
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	agent.Spec.SandboxRef, agent.Spec.ModelAccess, agent.Status.Policy = "", nil, nil
	deployments := []aigateway.Deployment{
		{ID: "b", ModelName: "b-gpt", AccessGroups: []string{aiGatewaySharedAccessGroup}, BaseModel: "gpt-5.5"},
		{ID: "a", ModelName: "a-gpt", AccessGroups: []string{aiGatewaySharedAccessGroup}, BaseModel: "openai/gpt-5.5"},
	}
	models := []cetirunv1alpha1.CopilotModel{{ID: "gpt-5.5"}}
	if _, _, ok := r.placeCopilotModels(agent, models, nil); ok {
		t.Fatal("placements need a listed catalog")
	}
	r.aiGatewayCatalog.update(deployments, false, time.Now())
	placements, skipped, ok := r.placeCopilotModels(agent, models, nil)
	if !ok || len(skipped) != 0 || placements["gpt-5.5"].name != "a-gpt" || !placements["gpt-5.5"].shared {
		t.Fatalf("several matching shared names: the first in sort order wins: %+v %v", placements, skipped)
	}
}

func TestCopilotDefaultModelNamesAndAliases(t *testing.T) {
	agent := routedAgent()
	agent.Spec.Model = "copilot/gpt-5.5"
	agent.Status.AIGateway.Copilot = &cetirunv1alpha1.AIGatewayCopilotStatus{Models: []string{"copilot/gpt-5.5"}}
	if got := aiGatewayRequestedModel(agent); got != "copilot/gpt-5.5" {
		t.Fatalf("a status recorded before routes keeps the earlier name: %q", got)
	}
	agent.Status.AIGateway.Copilot = &cetirunv1alpha1.AIGatewayCopilotStatus{
		Models: []string{"team-gpt"}, Routes: []cetirunv1alpha1.AIGatewayCopilotRoute{{Model: "gpt-5.5", Name: "team-gpt", Shared: true}},
	}
	if got := aiGatewayRequestedModel(agent); got != "team-gpt" {
		t.Fatalf("copilot/<ID> names the model's route: %q", got)
	}
	agent.Spec.Model = "copilot/unknown"
	if got := aiGatewayRequestedModel(agent); got != "copilot/unknown" {
		t.Fatalf("an unknown Copilot model is left as it is: %q", got)
	}
	aliases := aiGatewayKeyAliases(map[string]string{"copilot/gone": "gone", "admin-alias": "team-gpt"}, agent.Status.AIGateway.Copilot)
	if !maps.Equal(aliases, map[string]string{"admin-alias": "team-gpt", "copilot/gpt-5.5": "team-gpt"}) {
		t.Fatalf("Copilot aliases follow the routes, others stay: %v", aliases)
	}
	for model, allowed := range map[string]bool{"copilot/gpt-5.5": true, "gpt-5.5": true, "team-gpt": true, "has space": false, "a/b": false} {
		if got := copilotDefaultModelAllowed(model); got != allowed {
			t.Errorf("copilotDefaultModelAllowed(%q) = %v", model, got)
		}
	}
	policy := aiGatewayTestPolicy()
	policyAgent := aiGatewayTestAgent(testAIGatewayTeam)
	policyAgent.Spec.Model = "gpt-5.5"
	if _, _, err := effectivePolicyModels(policyAgent, policy); err != nil {
		t.Fatalf("a Copilot model's own name is a valid default under a policy allowing Copilot: %v", err)
	}
	policyAgent.Spec.ModelAccess = modelAccess(true, false)
	if _, _, err := effectivePolicyModels(policyAgent, policy); err == nil {
		t.Fatal("without Copilot a default outside the policy's shared models is refused")
	}
}

// A Copilot route under a shared model's name is priced by its base model. It
// must not make an unpriced shared model look priced, nor become a shared
// model itself, nor change the policy's view of the shared models.
func TestAIGatewayCatalogIgnoresCopilotRoutesUnderSharedNames(t *testing.T) {
	var catalog aiGatewayCatalog
	shared := sharedDeployment("plain", "plain-model", nil)
	shared.Priced = false
	route := aigateway.Deployment{ID: "cetirun-copilot-uid-x-1", ModelName: "plain-model", AccessGroups: []string{"cetirun-copilot-uid"},
		FromDatabase: true, DatabaseSourceKnown: true, Priced: true, BaseModel: "gpt-5.5", MaxInputTokens: 10}
	_, _, unpriced := catalog.update([]aigateway.Deployment{shared, route}, true, time.Now())
	if models, _ := catalog.shared(); len(models) != 0 || !slices.Equal(unpriced, []string{"plain-model"}) {
		t.Fatalf("an unpriced shared model stays out while the Gateway blocks unpriced models: %+v %v", models, unpriced)
	}
	shared.Priced = true
	catalog.update([]aigateway.Deployment{shared}, true, time.Now())
	fingerprint, _ := catalog.managedFingerprint()
	if changed, _, _ := catalog.update([]aigateway.Deployment{shared, route}, true, time.Now()); changed {
		t.Fatal("a Copilot route joining a shared name is no catalog change")
	}
	models, _ := catalog.shared()
	view, _ := catalog.copilotMergeSnapshot()
	after, _ := catalog.managedFingerprint()
	if len(models) != 1 || models[0].ContextWindow != 0 || after != fingerprint || len(view.shared["plain-model"]) != 1 || !view.served["plain-model"] {
		t.Fatalf("the route is not part of the shared model: %+v view %+v", models, view.shared)
	}
}

// Without a catalog, an Agent keeps the shared models last configured; its
// Copilot models under their own IDs are not taken for shared ones.
func TestAIGatewayModelEntriesWithoutCatalogKeepCopilotModelsApart(t *testing.T) {
	r, _, _ := aiGatewayTestReconciler(t)
	agent := routedAgent()
	agent.Spec.SandboxRef, agent.Status.Policy = "", nil
	agent.Status.AIGateway.Models = []string{testSharedModelOne, "team-gpt", "gpt-4o"}
	agent.Status.AIGateway.Copilot = &cetirunv1alpha1.AIGatewayCopilotStatus{
		Models: []string{"gpt-4o", "team-gpt"},
		Routes: []cetirunv1alpha1.AIGatewayCopilotRoute{{Model: "gpt-4o", Name: "gpt-4o"}, {Model: "gpt-5.5", Name: "team-gpt", Shared: true}},
	}
	entries, _, _ := r.aiGatewayModelEntries(agent)
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.ID+"="+entry.Name)
	}
	want := []string{testSharedModelOne + "=" + testSharedModelOne, "team-gpt=team-gpt", "gpt-4o=gpt-4o"}
	if !slices.Equal(got, want) {
		t.Fatalf("entries %v, want %v", got, want)
	}
}
