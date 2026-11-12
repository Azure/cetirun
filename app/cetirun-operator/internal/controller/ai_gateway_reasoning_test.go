// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
)

// reasoningDeployment is one deployment of a shared model of the Gateway
// chart's models, entry telling deployments of one name apart.
func reasoningDeployment(name string, entry int, reasoning bool) aigateway.Deployment {
	deployment := visionDeployment(name, entry, false)
	deployment.Reasoning = reasoning
	return deployment
}

// entryReasoning reports, by ID, whether the Agent is configured to set a
// reasoning effort for each of its models.
func entryReasoning(t *testing.T, r *AgentReconciler, agent *api.Agent) map[string]bool {
	t.Helper()
	entries, _, _ := r.aiGatewayModelEntries(agent)
	reasoning := map[string]bool{}
	for _, entry := range entries {
		reasoning[entry.ID] = entry.Reasoning
	}
	return reasoning
}

// A name reasons only when every deployment behind it does, and a router only
// when every model it sends requests to answer does, by the rule for images.
// The routers that send requests both to models that reason and to models
// that do not are listed apart.
func TestAIGatewayCatalogMarksTheModelsThatReason(t *testing.T) {
	classifier := aigateway.RouterModel{Use: aigateway.RouterClassifier, Name: "plain"}
	unverifiable := gatewayRouter("auto-unverifiable", tierModel("SIMPLE", "thinks"), tierModel("MEDIUM", "plain"))
	unverifiable.Router.Unverifiable = "it runs routing plugins, code the operator cannot check"
	// Its models all reason, but the operator cannot read all of its
	// configuration.
	unverifiableThinks := gatewayRouter("auto-unverifiable-thinks", tierModel("SIMPLE", "thinks"))
	unverifiableThinks.Router.Unverifiable = "it runs routing plugins, code the operator cannot check"
	claims := gatewayRouter("auto-claims", tierModel("SIMPLE", "plain"))
	claims.Reasoning = true
	listing := []aigateway.Deployment{
		reasoningDeployment("thinks", 1, true), reasoningDeployment("thinks", 2, true),
		reasoningDeployment("mixed", 1, true), reasoningDeployment("mixed", 2, false),
		reasoningDeployment("plain", 1, false),
		// A classifier answers no request.
		gatewayRouter("auto-thinks", tierModel("SIMPLE", "thinks"), tierModel("MEDIUM", "thinks"), classifier),
		gatewayRouter("auto-mixed-tier", tierModel("SIMPLE", "thinks"), tierModel("MEDIUM", "mixed")),
		gatewayRouter("auto-plain-default", tierModel("SIMPLE", "thinks"), aigateway.RouterModel{Use: aigateway.RouterDefault, Name: "plain"}),
		gatewayRouter("auto-plain-compactor", tierModel("SIMPLE", "thinks"), aigateway.RouterModel{Use: aigateway.RouterCompactor, Name: "plain"}),
		gatewayRouter("auto-nested", tierModel("SIMPLE", "auto-thinks")),
		gatewayRouter("auto-missing", tierModel("SIMPLE", "thinks"), tierModel("MEDIUM", "not-in-the-gateway")),
		gatewayRouter("auto-classifier-only", classifier),
		gatewayRouter("auto-plain", tierModel("SIMPLE", "plain")),
		unverifiable, unverifiableThinks, claims,
	}
	want := map[string]bool{
		"thinks": true, "mixed": false, "plain": false,
		"auto-thinks": true, "auto-mixed-tier": false, "auto-plain-default": false, "auto-plain-compactor": false,
		"auto-nested": false, "auto-missing": false, "auto-classifier-only": false, "auto-plain": false,
		"auto-unverifiable": false, "auto-unverifiable-thinks": false, "auto-claims": false,
	}
	wantMixed := []string{"auto-missing", "auto-mixed-tier", "auto-plain-compactor", "auto-plain-default"}
	for _, order := range []string{"listed", "reversed"} {
		ordered := slices.Clone(listing)
		if order == "reversed" {
			slices.Reverse(ordered)
		}
		var catalog aiGatewayCatalog
		catalog.update(ordered, false, time.Now())
		shared, _ := catalog.shared()
		got := map[string]bool{}
		for _, model := range shared {
			got[model.Name] = model.Reasoning
		}
		for name, reasoning := range want {
			if reasons, listed := got[name]; !listed || reasons != reasoning {
				t.Errorf("%s: %s reasoning = %v (listed %v), want %v", order, name, reasons, listed, reasoning)
			}
		}
		if mixed := catalog.reasoningMixedRouters(); !slices.Equal(mixed, wantMixed) {
			t.Errorf("%s: mixed routers = %v, want %v", order, mixed, wantMixed)
		}
		// The published catalog carries it, and a catalog seeded from that
		// keeps it.
		var seeded aiGatewayCatalog
		seeded.seed(catalog.sharedRouters().describe(shared), nil)
		seededShared, _ := seeded.shared()
		if len(seededShared) != len(want) {
			t.Fatalf("%s: seeded %+v", order, seededShared)
		}
		for _, model := range seededShared {
			if model.Reasoning != want[model.Name] {
				t.Errorf("%s: seeded %s reasoning = %v", order, model.Name, model.Reasoning)
			}
		}
	}
}

// A model that starts or stops reasoning changes the catalog, which
// reconfigures the Agents.
func TestAIGatewayCatalogSeesAModelStartReasoning(t *testing.T) {
	var catalog aiGatewayCatalog
	catalog.update([]aigateway.Deployment{reasoningDeployment("thinks", 1, false)}, false, time.Now())
	if changed, _, _ := catalog.update([]aigateway.Deployment{reasoningDeployment("thinks", 1, false)}, false, time.Now()); changed {
		t.Fatal("the same listing changes nothing")
	}
	if !catalog.changedBy([]aigateway.Deployment{reasoningDeployment("thinks", 1, true)}, false) {
		t.Fatal("a model that starts reasoning must change the catalog")
	}
	if changed, _, _ := catalog.update([]aigateway.Deployment{reasoningDeployment("thinks", 1, true)}, false, time.Now()); !changed {
		t.Fatal("a model that starts reasoning must change the catalog")
	}
}

// The operator logs a router configured in the Gateway that sends requests
// both to models that reason and to models that do not once, and again only
// when that list changes.
func TestMixedReasoningRoutersAreLoggedOnChange(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, _, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	gateway.shared = append(gateway.shared, reasoningDeployment("thinks", 1, true), reasoningDeployment("plain", 1, false),
		gatewayRouter("auto-mixed", tierModel("SIMPLE", "thinks"), tierModel("MEDIUM", "plain")))
	var lines []string
	ctx := log.IntoContext(context.Background(), funcr.New(func(_, args string) { lines = append(lines, args) }, funcr.Options{}))
	logged := func() int {
		count := 0
		for _, line := range lines {
			if strings.Contains(line, "models that reason and to models that do not") {
				count++
			}
		}
		return count
	}
	r.refreshAIGatewayCatalogWithin(ctx, gateway, 0)
	r.refreshAIGatewayCatalogWithin(ctx, gateway, 0)
	if logged() != 1 || !strings.Contains(strings.Join(lines, "\n"), "auto-mixed") {
		t.Fatalf("logged %d times: %v", logged(), lines)
	}
	// Another router mixes models: the list changes.
	gateway.shared = append(gateway.shared, gatewayRouter("auto-mixed-2", tierModel("SIMPLE", "plain"), tierModel("MEDIUM", "thinks")))
	r.refreshAIGatewayCatalogWithin(ctx, gateway, 0)
	if logged() != 2 {
		t.Fatalf("logged %d times: %v", logged(), lines)
	}
}

// A Copilot route reasons when the price table entry it is billed as says so
// and the Copilot proxy passes a reasoning effort on for the model, which it
// does for Claude and GPT models only. A shared model that an Agent's Copilot
// route joins reasons for that Agent only when the route does too.
func TestCopilotModelsReasonAsThePriceTableAndTheProxySay(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace(), readyProxy(1))
	gateway.shared = append(gateway.shared, sharedDeployment("team-gpt-a", "team-gpt", func(d *aigateway.Deployment) {
		d.BaseModel, d.Reasoning = "openai/gpt-5.5", true
	}))
	gateway.priced["gemini-3.8-flash"] = true
	gateway.reasoning = map[string]bool{"claude-sonnet-4-5": true, "gemini-3.8-flash": true}
	allowTestPolicyModels(t, kube, "team-gpt")
	ctx := context.Background()
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	if reasoning := entryReasoning(t, r, agent); !reasoning["team-gpt"] {
		t.Fatalf("before sign-in: %v", reasoning)
	}
	signIn(t, kube, agent, `[{"id":"gpt-5.5"},{"id":"claude-sonnet-4.5"},{"id":"gemini-3.8-flash"},{"id":"gpt-4.1"}]`)
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	routes := map[string]api.AIGatewayCopilotRoute{}
	for _, route := range agent.Status.AIGateway.Copilot.Routes {
		routes[route.Model] = route
	}
	// The price table says Gemini reasons, but the proxy refuses its effort.
	if len(routes) != 4 || !routes["claude-sonnet-4.5"].Reasoning || routes["gemini-3.8-flash"].Reasoning ||
		routes["gpt-5.5"].Reasoning || routes["gpt-4.1"].Reasoning || routes["gpt-5.5"].Name != "team-gpt" {
		t.Fatalf("routes = %+v", routes)
	}
	reasoning := entryReasoning(t, r, agent)
	if !reasoning["claude-sonnet-4.5"] || reasoning["gemini-3.8-flash"] || reasoning["team-gpt"] || reasoning["gpt-4.1"] {
		t.Fatalf("entries: %v", reasoning)
	}
	before, added := r.aiGatewayRevision(agent), len(gateway.added)

	// The price table now says GPT-5.5 reasons. Once the cached table
	// expires, the next pass records it and the Agent is reconfigured; the
	// routes keep their IDs.
	gateway.reasoning["gpt-5.5"] = true
	r.aiGatewayPrices.table = aigateway.PriceTable{}
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	if reasoning := entryReasoning(t, r, agent); !reasoning["team-gpt"] {
		t.Fatalf("after: %v", reasoning)
	}
	if r.aiGatewayRevision(agent) == before {
		t.Fatal("a route that starts reasoning must reconfigure the Agent")
	}
	if len(gateway.added) != added {
		t.Fatalf("no route is registered again: %v", gateway.added[added:])
	}
}

// The Copilot proxy passes a reasoning effort on for Claude and GPT models
// whose price-table entry reasons.
func TestCopilotRouteReasonsForClaudeAndGPTOnly(t *testing.T) {
	table := aigateway.PriceTable{Reasoning: map[string]bool{
		"claude-sonnet-4-5": true, "gpt-6-sol": true, "github_copilot/gemini-3.8-flash": true, "github_copilot/grok-4.7": true,
	}}
	for _, test := range []struct {
		id, base string
		want     bool
	}{
		{"claude-sonnet-4.5", "claude-sonnet-4-5", true},
		{"gpt-6-sol", "gpt-6-sol", true},
		{"gemini-3.8-flash", "github_copilot/gemini-3.8-flash", false},
		{"grok-4.7", "github_copilot/grok-4.7", false},
		// The entry does not say that the model reasons.
		{"gpt-4.1", "gpt-4.1", false},
	} {
		if got := copilotRouteReasons(test.id, test.base, table); got != test.want {
			t.Errorf("%s: reasons = %v, want %v", test.id, got, test.want)
		}
	}
}

// A Team's router reasons only when every tier does, and the Team lists the
// tier models that do not.
func TestTeamRouterReasonsOnlyWhenEveryTierDoes(t *testing.T) {
	team := routerTeam(testRouter(testRouterName, testSmallModel, testSharedModelOne, testSharedModelOne),
		testRouter("auto-thinks", testSmallModel, testSmallModel, testSmallModel))
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, team, aiGatewayNamespace())
	addSmallSharedModel(gateway)
	small := len(gateway.shared) - 1
	gateway.shared[small].Reasoning = true
	allowTestPolicyModels(t, kube, testRouterName, "auto-thinks", testSmallModel)
	current := reconcileRouters(t, r, kube, testAIGatewayTeam)
	nonReasoning := map[string][]string{}
	for _, status := range current.Status.ModelRouters {
		nonReasoning[status.Name] = status.NonReasoningModels
	}
	if !slices.Equal(nonReasoning[testRouterName], []string{testSharedModelOne}) || nonReasoning["auto-thinks"] != nil {
		t.Fatalf("Team status: %v", nonReasoning)
	}
	ctx := context.Background()
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	statuses := routerStatuses(agent)
	if !statuses["auto-thinks"].Granted || !statuses["auto-thinks"].Reasoning || !statuses[testRouterName].Granted || statuses[testRouterName].Reasoning {
		t.Fatalf("routers = %+v", statuses)
	}
	thinks, mixed := modelRouterGatewayName(testAIGatewayTeam, "auto-thinks"), modelRouterGatewayName(testAIGatewayTeam, testRouterName)
	if reasoning := entryReasoning(t, r, agent); !reasoning[thinks] || reasoning[mixed] {
		t.Fatalf("entries: %v", reasoning)
	}
	before := r.aiGatewayRevision(agent)

	// The shared model of the medium and complex tiers starts reasoning.
	gateway.shared[0].Reasoning = true
	r.refreshAIGatewayCatalogWithin(ctx, gateway, 0)
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	if !routerStatuses(agent)[testRouterName].Reasoning || !entryReasoning(t, r, agent)[mixed] {
		t.Fatalf("after: %+v", routerStatuses(agent))
	}
	if after := r.aiGatewayRevision(agent); after == before || !strings.Contains(after, mixed+"=auto:400000:16384:reasoning") {
		t.Fatalf("revision %s", after)
	}
	current = reconcileRouters(t, r, kube, testAIGatewayTeam)
	for _, status := range current.Status.ModelRouters {
		if status.NonReasoningModels != nil {
			t.Fatalf("Team status after: %+v", current.Status.ModelRouters)
		}
	}
}

// The rule for a Team router on its own: every tier, as the Gateway name it
// is registered as, must reason, the shared deployments of that name and the
// Agent's own Copilot routes under it alike.
func TestModelRouterReasoningNeedsEveryTier(t *testing.T) {
	shared := []aiGatewayCatalogModel{{Name: "thinks", Reasoning: true}, {Name: "plain"}, {Name: "joined", Reasoning: true}}
	plan := func(simple, medium, complex string) modelRouterPlan {
		return modelRouterPlan{router: testRouter(testRouterName, simple, medium, complex), tierNames: map[string]string{
			"copilot/gpt-5.5": "joined", "copilot/own-1": "own-1", "copilot/plain-own": "plain-own",
		}}
	}
	routes := []api.AIGatewayCopilotRoute{
		{Model: "gpt-5.5", Name: "joined", Shared: true, Reasoning: true},
		{Model: "own-1", Name: "own-1", Reasoning: true},
		{Model: "plain-own", Name: "plain-own"},
	}
	for name, test := range map[string]struct {
		plan   modelRouterPlan
		routes []api.AIGatewayCopilotRoute
		want   bool
	}{
		"every shared tier reasons":                 {plan("thinks", "thinks", "thinks"), nil, true},
		"a shared tier does not":                    {plan("thinks", "plain", "thinks"), nil, false},
		"a Copilot tier under its ID reasons":       {plan("thinks", "copilot/own-1", "thinks"), routes, true},
		"a Copilot tier under its ID does not":      {plan("thinks", "copilot/plain-own", "thinks"), routes, false},
		"a joined tier whose route reasons":         {plan("copilot/gpt-5.5", "thinks", "thinks"), routes, true},
		"a joined tier whose route does not reason": {plan("copilot/gpt-5.5", "thinks", "thinks"), []api.AIGatewayCopilotRoute{{Model: "gpt-5.5", Name: "joined", Shared: true}}, false},
		"a tier that is neither":                    {plan("thinks", "unknown", "thinks"), routes, false},
		"a Copilot tier not there":                  {plan("thinks", "copilot/own-1", "thinks"), nil, false},
	} {
		if got := modelRouterReasoning(test.plan, shared, test.routes); got != test.want {
			t.Errorf("%s: reasoning = %v, want %v", name, got, test.want)
		}
	}
}

// The Team's hint names the tiers that do not reason: a shared tier by the
// catalog, and a copilot/<ID> tier by the price table and the Copilot proxy,
// only when the price table has an entry for the model. A tier that joins a
// shared model counts by both.
func TestTeamRouterStatusNamesTheTiersThatDoNotReason(t *testing.T) {
	shared := []aiGatewayCatalogModel{{Name: "thinks", Reasoning: true}, {Name: "plain"}, {Name: "joined", Reasoning: true}}
	table := &aigateway.PriceTable{
		Priced:    map[string]bool{"claude-sonnet-4-5": true, "claude-opus-5": true, "gemini-3.8-flash": true, "gpt-4.1": true, "gpt-5.5": true},
		Reasoning: map[string]bool{"claude-sonnet-4-5": true, "claude-opus-5": true, "gemini-3.8-flash": true, "gpt-5.5": true},
	}
	plan := func(simple, medium, complex, reasoning string) modelRouterPlan {
		router := testRouter(testRouterName, simple, medium, complex)
		router.Tiers.Reasoning = reasoning
		return modelRouterPlan{router: router, tierNames: map[string]string{
			"copilot/claude-sonnet-4.5": "claude-sonnet-4.5", "copilot/gemini-3.8-flash": "gemini-3.8-flash",
			"copilot/unpriced-1": "unpriced-1", "copilot/gpt-5.5": "joined", "copilot/gpt-4.1": "joined",
			"copilot/claude-opus-5": "plain",
		}}
	}
	for name, test := range map[string]struct {
		plan  modelRouterPlan
		table *aigateway.PriceTable
		want  []string
	}{
		"every tier reasons":       {plan("thinks", "thinks", "copilot/claude-sonnet-4.5", "copilot/gpt-5.5"), table, nil},
		"shared tiers that do not": {plan("plain", "thinks", "plain", ""), table, []string{"plain"}},
		"a Copilot tier that does not": {plan("thinks", "copilot/gemini-3.8-flash", "thinks", "plain"), table,
			[]string{"copilot/gemini-3.8-flash", "plain"}},
		"no price table":       {plan("thinks", "copilot/gemini-3.8-flash", "thinks", ""), nil, nil},
		"no price-table entry": {plan("thinks", "copilot/unpriced-1", "thinks", ""), table, nil},
		"a joined tier whose Copilot model does not reason": {plan("copilot/gpt-4.1", "thinks", "thinks", ""), table,
			[]string{"copilot/gpt-4.1"}},
		"a joined tier whose shared model does not reason": {plan("thinks", "copilot/claude-opus-5", "thinks", ""), table,
			[]string{"copilot/claude-opus-5"}},
	} {
		if got := test.plan.nonReasoningModels(shared, test.table); !slices.Equal(got, test.want) {
			t.Errorf("%s: %v, want %v", name, got, test.want)
		}
	}
}

// The Team's reconcile reads the Gateway's price table for a copilot/<ID>
// tier, and leaves such a tier out of the hint while the table cannot be
// read: the router is ready either way.
func TestTeamRouterStatusReadsThePriceTableForCopilotTiers(t *testing.T) {
	team := routerTeam(testRouter(testRouterName, "copilot/claude-sonnet-4.5", testSmallModel, "copilot/gpt-4.1"))
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, team, aiGatewayNamespace())
	addSmallSharedModel(gateway)
	gateway.shared[len(gateway.shared)-1].Reasoning = true
	gateway.reasoning = map[string]bool{"claude-sonnet-4-5": true}
	gateway.priceErr = errors.New("the price table cannot be read")
	if status := reconcileRouters(t, r, kube, testAIGatewayTeam).Status.ModelRouters[0]; !status.Ready || status.NonReasoningModels != nil {
		t.Fatalf("without the price table: %+v", status)
	}
	gateway.priceErr = nil
	if status := reconcileRouters(t, r, kube, testAIGatewayTeam).Status.ModelRouters[0]; !status.Ready ||
		!slices.Equal(status.NonReasoningModels, []string{"copilot/gpt-4.1"}) {
		t.Fatalf("with the price table: %+v", status)
	}
}

// A router configured in the Gateway that reasons does not for an Agent
// whose own Copilot route under one of its models does not.
func TestGatewayRouterDoesNotReasonWhenTheAgentsCopilotRouteUnderATierDoesNot(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace(), readyProxy(1))
	gateway.shared = append(gateway.shared, sharedDeployment("team-gpt-a", "team-gpt", func(d *aigateway.Deployment) {
		d.BaseModel, d.Reasoning, d.MaxInputTokens, d.MaxOutputTokens = "openai/gpt-5.5", true, 1000000, 64000
	}), gatewayRouter("auto-gpt", tierModel("SIMPLE", "team-gpt")))
	allowTestPolicyModels(t, kube, "team-gpt", "auto-gpt")
	ctx := context.Background()
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	if reasoning := entryReasoning(t, r, agent); !reasoning["auto-gpt"] || !reasoning["team-gpt"] {
		t.Fatalf("before sign-in: %v", reasoning)
	}
	if router := routerStatuses(agent)["auto-gpt"]; !router.Granted || !router.Reasoning {
		t.Fatalf("router = %+v", router)
	}
	if strings.Contains(r.aiGatewayRevision(agent), "gateway-router-reasoning") {
		t.Fatal("nothing lowers the router's reasoning yet")
	}

	// The owner's GPT-5.5, which the price table does not say reasons, joins
	// the router's tier model.
	signIn(t, kube, agent, `[{"id":"gpt-5.5","contextWindow":400000}]`)
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	if reasoning := entryReasoning(t, r, agent); reasoning["auto-gpt"] || reasoning["team-gpt"] {
		t.Fatalf("after sign-in: %v", reasoning)
	}
	if router := routerStatuses(agent)["auto-gpt"]; !router.Granted || router.Reasoning {
		t.Fatalf("router = %+v", router)
	}
	if revision := r.aiGatewayRevision(agent); !strings.Contains(revision, "gateway-router-reasoning|auto-gpt") {
		t.Fatalf("revision %s", revision)
	}
}

// Whether a model reasons is part of the configuration hash.
func TestAIGatewayRevisionFollowsTheModelsThatReason(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, _, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	ctx := context.Background()
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	before := r.aiGatewayRevision(agent)
	if strings.Contains(before, "|reasoning|") {
		t.Fatalf("no model reasons yet: %s", before)
	}
	gateway.shared[0].Reasoning = true
	r.refreshAIGatewayCatalogWithin(ctx, gateway, 0)
	after := r.aiGatewayRevision(agent)
	if after == before || !strings.Contains(after, "|reasoning|"+testSharedModelOne) {
		t.Fatalf("revision %s", after)
	}
	r.refreshAIGatewayCatalogWithin(ctx, gateway, 0)
	if r.aiGatewayRevision(agent) != after {
		t.Fatal("the same listing must keep the revision")
	}
}

// The models that reason reach a chart that takes them, and no other: an
// older chart would refuse the value.
func TestReasoningModelsReachOnlyAChartThatTakesThem(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	addSmallSharedModel(gateway)
	small := len(gateway.shared) - 1
	gateway.shared[small].Reasoning = true
	allowTestPolicyModels(t, kube, testSmallModel)
	ctx := context.Background()
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	values := map[string]any{aiGatewayChartValue: map[string]any{}}
	r.applyAIGatewayReasoningModels(values, agent, declaredValues{{aiGatewayChartValue, aiGatewayChartModelsValue},
		{aiGatewayChartValue, aiGatewayChartVisionValue}})
	if _, set := values[aiGatewayChartValue].(map[string]any)[aiGatewayChartReasoningValue]; set {
		t.Fatal("a chart that does not declare the value must not get it")
	}
	takes := declaredValues{{aiGatewayChartValue, aiGatewayChartModelsValue}, {aiGatewayChartValue, aiGatewayChartReasoningValue}}
	r.applyAIGatewayReasoningModels(values, agent, takes)
	if got := values[aiGatewayChartValue].(map[string]any)[aiGatewayChartReasoningValue]; !reflect.DeepEqual(got, []any{testSmallModel}) {
		t.Fatalf("reasoning models = %#v", got)
	}
	// No model reasons: the list is empty.
	gateway.shared[small].Reasoning = false
	r.refreshAIGatewayCatalogWithin(ctx, gateway, 0)
	r.applyAIGatewayReasoningModels(values, agent, takes)
	if got := values[aiGatewayChartValue].(map[string]any)[aiGatewayChartReasoningValue]; !reflect.DeepEqual(got, []any{}) {
		t.Fatalf("reasoning models = %#v", got)
	}
}

// A full reconcile configures the Agent with the models that reason, through
// the chart's own renderer: OpenClaw offers reasoning levels for them and
// sends the effort; Hermes' configuration does not change.
func TestReconcileConfiguresTheModelsThatReason(t *testing.T) {
	for _, kind := range []api.AgentType{api.AgentTypeOpenClaw, api.AgentTypeHermes} {
		t.Run(string(kind), func(t *testing.T) {
			r, agent, gateway := legacyGatewayFixture(t)
			agent.Spec.Agent.Type = kind
			ctx := context.Background()
			if err := r.Update(ctx, agent); err != nil {
				t.Fatal(err)
			}
			gateway.shared[0].Reasoning = true
			chart := &chartHelm{}
			r.HelmClient = chart
			req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(agent)}
			for i := 0; i < 4 && chart.installs == 0; i++ {
				if _, err := r.Reconcile(ctx, req); err != nil {
					t.Fatalf("reconcile %d: %v", i, err)
				}
			}
			if chart.installs == 0 {
				t.Fatal("the Agent was never installed")
			}
			gatewayValues := chart.lastValues["aiGateway"].(map[string]any)
			if got := gatewayValues["reasoningModels"]; !reflect.DeepEqual(got, []any{testSharedModelOne}) {
				t.Fatalf("reasoning models = %#v", got)
			}
			files := chart.lastValues["runtime"].(map[string]any)["configFiles"].(map[string]any)
			switch kind {
			case api.AgentTypeOpenClaw:
				config := files["openclaw.json"].(string)
				if !strings.Contains(config, `"reasoning": true`) || !strings.Contains(config, `"supportsReasoningEffort": true`) {
					t.Fatalf("OpenClaw configuration:\n%s", config)
				}
			case api.AgentTypeHermes:
				if config := files["hermes-config.yaml"].(string); strings.Contains(config, "reasoning") {
					t.Fatalf("Hermes configuration:\n%s", config)
				}
			}
		})
	}
}
