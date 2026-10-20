// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
)

const (
	testGatewayRouter = "auto-shared"
	testWideModel     = "admin-wide"
	testWideGroup     = "cetirun-model-test-wide"
)

func tierModel(tier, name string) aigateway.RouterModel {
	return aigateway.RouterModel{Use: aigateway.RouterTier, Tier: tier, Name: name}
}

// gatewayRouter is a router of the Gateway chart's models as the catalog
// lists it: a static, priced deployment with its chart-derived ID and group.
func gatewayRouter(name string, models ...aigateway.RouterModel) aigateway.Deployment {
	return aigateway.Deployment{
		ID: aiGatewaySharedDeploymentIDPrefix + "test-" + name, ModelName: name, AccessGroups: []string{gatewayRouterGroup(name)},
		DatabaseSourceKnown: true, Priced: true, Router: &aigateway.Router{Models: models},
	}
}

func gatewayRouterGroup(name string) string { return aiGatewayModelAccessGroupPrefix + "test-" + name }

func wideSharedModel() aigateway.Deployment {
	return aigateway.Deployment{ID: "cetirun-shared-test-wide", ModelName: testWideModel, AccessGroups: []string{testWideGroup}, DatabaseSourceKnown: true}
}

// setRouter changes the configuration of a listed router; its deployment
// keeps its ID, group, name and limits.
func setRouter(t *testing.T, gateway *fakeAIGateway, name string, router aigateway.Router) {
	t.Helper()
	for i := range gateway.shared {
		if gateway.shared[i].ModelName == name {
			gateway.shared[i].Router = &router
			return
		}
	}
	t.Fatalf("no router %s", name)
}

func routerStatuses(agent *api.Agent) map[string]api.AIGatewayModelRouterStatus {
	statuses := map[string]api.AIGatewayModelRouterStatus{}
	for _, status := range agent.Status.AIGateway.ModelRouters {
		statuses[status.Name] = status
	}
	return statuses
}

// The rule for a router configured in the Gateway, on its own.
func TestGatewayRouterRefusalFollowsEveryModelTheRouterUses(t *testing.T) {
	routerOf := func(models ...aigateway.RouterModel) aigateway.Router { return aigateway.Router{Models: models} }
	view := aiGatewaySharedRouterView{known: true,
		shared:  map[string]bool{testSmallModel: true, testSharedModelOne: true, testWideModel: true, testGatewayRouter: true, "auto-other": true},
		routers: map[string]aigateway.Router{"auto-other": routerOf(tierModel("SIMPLE", testSmallModel))},
	}
	classifier := aigateway.RouterModel{Use: aigateway.RouterClassifier, Name: testWideModel}
	defaultModel := aigateway.RouterModel{Use: aigateway.RouterDefault, Name: testSharedModelOne}
	compactor := aigateway.RouterModel{Use: aigateway.RouterCompactor, Name: testWideModel}
	allowed := map[string]bool{testGatewayRouter: true, testSmallModel: true, testSharedModelOne: true}
	for name, tc := range map[string]struct {
		router  aigateway.Router
		allowed map[string]bool
		want    string
	}{
		"the policy allows every model": {routerOf(tierModel("SIMPLE", testSmallModel), tierModel("MEDIUM", testSharedModelOne), defaultModel), allowed, ""},
		"an unlisted classifier":        {routerOf(tierModel("SIMPLE", testSmallModel), classifier), allowed, ""},
		"a tier the policy does not allow": {routerOf(tierModel("SIMPLE", testSmallModel), tierModel("MEDIUM", testWideModel)), allowed,
			"tier medium uses admin-wide, which the AgentPolicy does not allow"},
		"a default model the policy does not allow": {routerOf(tierModel("SIMPLE", testSmallModel), aigateway.RouterModel{Use: aigateway.RouterDefault, Name: testWideModel}), allowed,
			"the default model is admin-wide, which the AgentPolicy does not allow"},
		"a compaction model the policy does not allow": {routerOf(tierModel("SIMPLE", testSmallModel), compactor), allowed,
			"context compaction uses admin-wide, which the AgentPolicy does not allow"},
		"a router the policy does not list": {routerOf(tierModel("SIMPLE", testSmallModel)), map[string]bool{testSmallModel: true}, modelRouterNotAllowed},
		"a tier missing from the Gateway": {routerOf(tierModel("SIMPLE", "missing-model")), allowed,
			"the router is misconfigured: tier simple names missing-model, which is not a shared model of the AI Gateway"},
		"a default model missing from the Gateway": {routerOf(tierModel("SIMPLE", testSmallModel), aigateway.RouterModel{Use: aigateway.RouterDefault, Name: "missing-model"}), allowed,
			"the router is misconfigured: the default model is missing-model, which is not a shared model of the AI Gateway"},
		"a classifier missing from the Gateway": {routerOf(tierModel("SIMPLE", testSmallModel), aigateway.RouterModel{Use: aigateway.RouterClassifier, Name: "missing-model"}), allowed,
			"the router is misconfigured: the classifier missing-model is not a shared model of the AI Gateway"},
		"a tier that is a router": {routerOf(tierModel("SIMPLE", "auto-other")), allowed,
			"the router is misconfigured: tier simple names auto-other, which is a router, not a model"},
		"an unverifiable router": {aigateway.Router{Models: []aigateway.RouterModel{tierModel("SIMPLE", testSmallModel)}, Unverifiable: "it runs routing plugins, code the operator cannot check"}, allowed,
			"the operator cannot check every model the router uses: it runs routing plugins, code the operator cannot check"},
		// An Agent without a policy may use every shared model: only a
		// misconfigured router is refused.
		"no policy, models outside any policy": {routerOf(tierModel("SIMPLE", testWideModel), compactor, defaultModel), nil, ""},
		"no policy, an unverifiable router":    {aigateway.Router{Models: []aigateway.RouterModel{tierModel("SIMPLE", testSmallModel)}, Unverifiable: "it runs routing plugins"}, nil, ""},
		"no policy, a tier missing from the Gateway": {routerOf(tierModel("SIMPLE", testSmallModel), tierModel("COMPLEX", "missing-model")), nil,
			"the router is misconfigured: tier complex names missing-model, which is not a shared model of the AI Gateway"},
	} {
		t.Run(name, func(t *testing.T) {
			view.routers[testGatewayRouter] = tc.router
			if got := view.refusal(testGatewayRouter, tc.allowed); got != tc.want {
				t.Fatalf("refusal = %q, want %q", got, tc.want)
			}
		})
	}
}

// A policy that lists a router configured in the Gateway grants it only with
// every model it uses; the key, the model list, the default model and the
// status follow the rule, beside the Team's own routers.
func TestPolicyBoundAgentGetsAGatewayRouterOnlyWithEveryModelItUses(t *testing.T) {
	team := routerTeam(testRouter(testRouterName, testSmallModel, testSharedModelOne, testSharedModelOne))
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	agent.Spec.Model = "auto-wide"
	r, kube, gateway := aiGatewayTestReconciler(t, agent, team, aiGatewayNamespace())
	addSmallSharedModel(gateway)
	gateway.shared = append(gateway.shared, wideSharedModel(),
		// The classifier is a shared model the policy does not list.
		gatewayRouter(testGatewayRouter, tierModel("SIMPLE", testSmallModel), tierModel("MEDIUM", testSharedModelOne),
			aigateway.RouterModel{Use: aigateway.RouterDefault, Name: testSharedModelOne},
			aigateway.RouterModel{Use: aigateway.RouterClassifier, Name: testWideModel}),
		gatewayRouter("auto-wide", tierModel("SIMPLE", testSmallModel), tierModel("MEDIUM", testWideModel)),
		gatewayRouter("auto-broken", tierModel("SIMPLE", "missing-model")),
		gatewayRouter("auto-unlisted", tierModel("SIMPLE", testSmallModel)),
	)
	unknown := gatewayRouter("auto-unknown", tierModel("SIMPLE", testSmallModel))
	unknown.Router.Unverifiable = "its complexity_router_config sets future_pool, a setting the operator does not know"
	gateway.shared = append(gateway.shared, unknown)
	allowTestPolicyModels(t, kube, testSmallModel, testRouterName, testGatewayRouter, "auto-wide", "auto-broken", "auto-unknown")
	reconcileRouters(t, r, kube, testAIGatewayTeam)
	ctx := context.Background()

	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	teamRouter := modelRouterGatewayName(testAIGatewayTeam, testRouterName)
	status := agent.Status.AIGateway
	wantGroups := sortedCopy([]string{testManagedGroup, testSmallGroup, gatewayRouterGroup(testGatewayRouter), copilotGroup(), teamRouter})
	if !slices.Equal(status.AccessGroups, wantGroups) || !slices.Equal(lastKeyModels(gateway), wantGroups) {
		t.Fatalf("groups %v key %v, want %v", status.AccessGroups, lastKeyModels(gateway), wantGroups)
	}
	// The Team's router first, then the Gateway's that the policy lists.
	names := []string{}
	for _, router := range status.ModelRouters {
		names = append(names, router.Name)
	}
	if !slices.Equal(names, []string{testRouterName, "auto-broken", testGatewayRouter, "auto-unknown", "auto-wide"}) {
		t.Fatalf("router statuses %+v", status.ModelRouters)
	}
	routers := routerStatuses(agent)
	if router := routers[testGatewayRouter]; !router.Granted || router.Model != testGatewayRouter || router.Reason != "" {
		t.Fatalf("%s = %+v", testGatewayRouter, router)
	}
	for name, reason := range map[string]string{
		"auto-wide":    "tier medium uses admin-wide, which the AgentPolicy does not allow",
		"auto-broken":  "the router is misconfigured: tier simple names missing-model, which is not a shared model of the AI Gateway",
		"auto-unknown": "the operator cannot check every model the router uses: its complexity_router_config sets future_pool, a setting the operator does not know",
	} {
		if router := routers[name]; router.Granted || router.Model != "" || router.Reason != reason {
			t.Errorf("%s = %+v, want the reason %q", name, router, reason)
		}
	}
	// The Agent names a refused router as its model: it is not configured
	// with it, and gets another default.
	for _, refused := range []string{"auto-wide", "auto-broken", "auto-unknown", "auto-unlisted"} {
		if slices.Contains(status.Models, refused) {
			t.Fatalf("models %v include the refused router %s", status.Models, refused)
		}
	}
	if !slices.Contains(status.Models, testGatewayRouter) || status.DefaultModel == "auto-wide" || status.DefaultModel == "" {
		t.Fatalf("models %v default %q", status.Models, status.DefaultModel)
	}
	// A granted router of the Gateway is configured once, as the shared
	// model it is; only the Team's router is configured as a router.
	entries, _, _ := r.aiGatewayModelEntries(agent)
	count := 0
	for _, entry := range entries {
		if entry.ID == testGatewayRouter {
			count++
		}
	}
	if count != 1 || len(aiGatewayRouterEntries(agent)) != 1 || aiGatewayRouterEntries(agent)[0].ID != teamRouter {
		t.Fatalf("entries %+v, router entries %+v", entries, aiGatewayRouterEntries(agent))
	}
	// Routers the policy lists stay resolvable, refused ones included.
	if _, err := r.resolvePolicy(ctx, agent); err != nil {
		t.Fatalf("a router the policy lists must not hold the Agent: %v", err)
	}

	// The policy allows the wide model: the next pass grants the router, which
	// the Agent asked for as its model.
	allowTestPolicyModels(t, kube, testWideModel)
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	if router := routerStatuses(agent)["auto-wide"]; !router.Granted || !slices.Contains(lastKeyModels(gateway), gatewayRouterGroup("auto-wide")) ||
		agent.Status.AIGateway.DefaultModel != "auto-wide" {
		t.Fatalf("auto-wide = %+v, key %v, default %q", router, lastKeyModels(gateway), agent.Status.AIGateway.DefaultModel)
	}
}

// An administrator changes a router's tiers: the Agents with shared models
// are queued at the next catalog listing, and their keys follow in place. The
// change itself touches neither the Agent's configuration hash nor the
// policy's Gateway revision, so nothing restarts or is fenced for it.
func TestGatewayRouterChangeReachesKeysInPlace(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	addSmallSharedModel(gateway)
	gateway.shared = append(gateway.shared, wideSharedModel(),
		gatewayRouter(testGatewayRouter, tierModel("SIMPLE", testSmallModel), tierModel("MEDIUM", testSharedModelOne)))
	allowTestPolicyModels(t, kube, testSmallModel, testGatewayRouter)
	ctx := context.Background()
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	if err := kube.Status().Update(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if !routerStatuses(agent)[testGatewayRouter].Granted {
		t.Fatalf("routers = %+v", agent.Status.AIGateway.ModelRouters)
	}
	revision := r.aiGatewayRevision(agent)
	resolution, err := r.resolvePolicy(ctx, agent)
	if err != nil {
		t.Fatal(err)
	}
	keys := len(gateway.generated)
	r.aiGatewayEvents = make(chan event.GenericEvent, 4)

	// The medium tier moves to a model the policy does not allow.
	setRouter(t, gateway, testGatewayRouter, aigateway.Router{Models: []aigateway.RouterModel{tierModel("SIMPLE", testSmallModel), tierModel("MEDIUM", testWideModel)}})
	r.publishAIGatewayCatalog(ctx, 0)
	select {
	case queued := <-r.aiGatewayEvents:
		if queued.Object.GetName() != agent.Name {
			t.Fatalf("queued %s", queued.Object.GetName())
		}
	default:
		t.Fatal("a router change must queue the Agents with shared models")
	}
	if got := r.aiGatewayRevision(agent); got != revision {
		t.Fatalf("a router change alone must not change the configuration hash:\n%s\n%s", revision, got)
	}
	if after, err := r.resolvePolicy(ctx, agent); err != nil || after.gatewayRevision != resolution.gatewayRevision {
		t.Fatalf("a router change must not change the policy's Gateway revision: %v %q %q", err, resolution.gatewayRevision, after.gatewayRevision)
	}

	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(lastKeyModels(gateway), gatewayRouterGroup(testGatewayRouter)) || slices.Contains(agent.Status.AIGateway.Models, testGatewayRouter) {
		t.Fatalf("the router must be withdrawn: key %v models %v", lastKeyModels(gateway), agent.Status.AIGateway.Models)
	}
	if router := routerStatuses(agent)[testGatewayRouter]; router.Granted || router.Reason != "tier medium uses admin-wide, which the AgentPolicy does not allow" {
		t.Fatalf("router = %+v", router)
	}
	for _, key := range gateway.keys {
		if key.Blocked {
			t.Fatal("the key must not be blocked for a router change")
		}
	}
	if len(gateway.generated) != keys || len(gateway.keys) != 1 {
		t.Fatalf("the key must be updated in place, not replaced: generated %d -> %d", keys, len(gateway.generated))
	}
}

// An Agent without a policy may use every shared model, so it gets every
// router of the Gateway but a misconfigured one. Its routers follow a change
// without the key verification, and without a configuration change unless
// its models change.
func TestLegacyAgentGetsEveryGatewayRouterButAMisconfiguredOne(t *testing.T) {
	team := &api.Team{ObjectMeta: metav1.ObjectMeta{Name: testLegacyRouterTeam, Generation: 1}}
	agent := aiGatewayTestAgent(testLegacyRouterTeam)
	agent.Spec.SandboxRef = ""
	agent.Status.Policy = nil
	agent.Spec.ModelAccess = modelAccess(true, false)
	agent.Spec.Model = "auto-broken"
	r, _, gateway := aiGatewayTestReconciler(t, agent, team, aiGatewayNamespace())
	addSmallSharedModel(gateway)
	unknown := gatewayRouter("auto-unknown", tierModel("SIMPLE", testSmallModel))
	unknown.Router.Unverifiable = "it runs routing plugins, code the operator cannot check"
	gateway.shared = append(gateway.shared, wideSharedModel(), unknown,
		gatewayRouter(testGatewayRouter, tierModel("SIMPLE", testSmallModel), tierModel("MEDIUM", testWideModel)),
		gatewayRouter("auto-broken", tierModel("SIMPLE", "missing-model")))
	ctx := context.Background()
	reconcile := func() {
		t.Helper()
		if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
			t.Fatal(err)
		}
	}
	reconcile()
	status := agent.Status.AIGateway
	wantGroups := sortedCopy([]string{aiGatewaySharedAccessGroup, testManagedGroup, testSmallGroup, testWideGroup,
		gatewayRouterGroup(testGatewayRouter), gatewayRouterGroup("auto-unknown")})
	if !slices.Equal(status.AccessGroups, wantGroups) || !slices.Equal(lastKeyModels(gateway), wantGroups) {
		t.Fatalf("groups %v key %v, want %v", status.AccessGroups, lastKeyModels(gateway), wantGroups)
	}
	if !slices.Contains(status.Models, testGatewayRouter) || !slices.Contains(status.Models, "auto-unknown") ||
		slices.Contains(status.Models, "auto-broken") || status.DefaultModel == "auto-broken" {
		t.Fatalf("models %v default %q", status.Models, status.DefaultModel)
	}
	if status.ModelRouters != nil {
		t.Fatalf("an Agent without a policy reports no router of the Gateway: %+v", status.ModelRouters)
	}
	if r.aiGatewayReconcileDue(agent, time.Now()) {
		t.Fatal("nothing is due right after a verification")
	}
	revision := r.aiGatewayRevision(agent)

	// A tier change the Agent may follow changes nothing for it.
	setRouter(t, gateway, testGatewayRouter, aigateway.Router{Models: []aigateway.RouterModel{tierModel("SIMPLE", testSmallModel), tierModel("MEDIUM", testSharedModelOne)}})
	if !r.refreshAIGatewayCatalogWithin(ctx, gateway, 0) {
		t.Fatal("a router change is a catalog change")
	}
	if r.aiGatewayReconcileDue(agent, time.Now()) || r.aiGatewayRevision(agent) != revision {
		t.Fatal("a router change that keeps the Agent's routers must neither reconcile nor reconfigure it")
	}

	// A tier the Gateway does not serve: the next reconcile of the Agent is
	// the full one, without a configuration change, and withdraws the router.
	setRouter(t, gateway, testGatewayRouter, aigateway.Router{Models: []aigateway.RouterModel{tierModel("SIMPLE", "missing-model")}})
	r.refreshAIGatewayCatalogWithin(ctx, gateway, 0)
	if !r.aiGatewayReconcileDue(agent, time.Now()) || r.aiGatewayRevision(agent) != revision {
		t.Fatal("a withdrawn router must make the full reconcile due, without a configuration change")
	}
	reconcile()
	if slices.Contains(lastKeyModels(gateway), gatewayRouterGroup(testGatewayRouter)) || slices.Contains(agent.Status.AIGateway.Models, testGatewayRouter) {
		t.Fatalf("key %v models %v", lastKeyModels(gateway), agent.Status.AIGateway.Models)
	}
	if r.aiGatewayReconcileDue(agent, time.Now()) {
		t.Fatal("nothing is due once the key follows the routers")
	}

	// The administrator fixes the router: it comes back the same way.
	setRouter(t, gateway, testGatewayRouter, aigateway.Router{Models: []aigateway.RouterModel{tierModel("SIMPLE", testSmallModel)}})
	r.refreshAIGatewayCatalogWithin(ctx, gateway, 0)
	if !r.aiGatewayReconcileDue(agent, time.Now()) {
		t.Fatal("a router the Agent may use again must make the full reconcile due")
	}
	reconcile()
	if !slices.Contains(lastKeyModels(gateway), gatewayRouterGroup(testGatewayRouter)) || !slices.Contains(agent.Status.AIGateway.Models, testGatewayRouter) {
		t.Fatalf("key %v models %v", lastKeyModels(gateway), agent.Status.AIGateway.Models)
	}
}

// A sign-in that grants a Team router in the same pass keeps the statuses of
// the Gateway's routers.
func TestTeamRouterRegrantKeepsTheGatewayRoutersStatus(t *testing.T) {
	team := routerTeam(testRouter(testRouterName, "copilot/gpt-5.5", testSharedModelOne, testSharedModelOne))
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, team, aiGatewayNamespace(), readyProxy(1))
	addSmallSharedModel(gateway)
	gateway.shared = append(gateway.shared, gatewayRouter(testGatewayRouter, tierModel("SIMPLE", testSmallModel)))
	allowTestPolicyModels(t, kube, testRouterName, testSmallModel, testGatewayRouter)
	reconcileRouters(t, r, kube, testAIGatewayTeam)
	ctx := context.Background()
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	signIn(t, kube, agent, testCopilotModels)
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	routers := routerStatuses(agent)
	if !routers[testRouterName].Granted || !routers[testGatewayRouter].Granted ||
		!slices.Contains(lastKeyModels(gateway), gatewayRouterGroup(testGatewayRouter)) {
		t.Fatalf("routers %+v key %v", agent.Status.AIGateway.ModelRouters, lastKeyModels(gateway))
	}
}

// status.modelRouters is keyed by name: a Team router named like a router of
// the Gateway, which makes it misconfigured, keeps its entry.
func TestGatewayRouterStatusesFollowTheTeams(t *testing.T) {
	team := []api.AIGatewayModelRouterStatus{{Name: "auto", Reason: "the router is misconfigured: the name auto is also a shared model"}}
	shared := []api.AIGatewayModelRouterStatus{{Name: "auto", Granted: true, Model: "auto"}, {Name: "auto-2", Granted: true, Model: "auto-2"}}
	statuses := withSharedRouterStatuses(team, shared)
	if len(statuses) != 2 || statuses[0] != team[0] || statuses[1] != shared[1] {
		t.Fatalf("statuses = %+v", statuses)
	}
	if withSharedRouterStatuses(nil, nil) != nil {
		t.Fatal("no routers report nothing")
	}
}

func TestAIGatewayCatalogComparesRoutersOutsideTheManagedFingerprint(t *testing.T) {
	var catalog aiGatewayCatalog
	sol := aigateway.Deployment{ID: testManagedDeploymentID, ModelName: testSharedModelOne, AccessGroups: []string{testManagedGroup}, DatabaseSourceKnown: true}
	small := aigateway.Deployment{ID: testSmallDeployment, ModelName: testSmallModel, AccessGroups: []string{testSmallGroup}, DatabaseSourceKnown: true}
	router := gatewayRouter(testGatewayRouter, tierModel("SIMPLE", testSmallModel), tierModel("MEDIUM", testSharedModelOne))
	// A router the operator registered for a Team, which is not a shared
	// model, and one in no group.
	teamRouter := aigateway.Deployment{ID: "cetirun-router-0123456789abcdef-01234567", ModelName: "cetirun-router-0123456789abcdef",
		AccessGroups: []string{"cetirun-router-0123456789abcdef"}, FromDatabase: true, DatabaseSourceKnown: true, Router: &aigateway.Router{}}
	console := aigateway.Deployment{ID: "console", ModelName: "console-router", Router: &aigateway.Router{}}
	if changed, _, _ := catalog.update([]aigateway.Deployment{sol, small, router, teamRouter, console}, false, time.Now()); !changed {
		t.Fatal("the first listing is a change")
	}
	view := catalog.sharedRouters()
	if !view.known || len(view.routers) != 1 || !slices.Equal(view.groups[testGatewayRouter], []string{gatewayRouterGroup(testGatewayRouter)}) {
		t.Fatalf("view = %+v", view)
	}
	managed, _ := catalog.managedFingerprint()
	shared, _ := catalog.shared()
	if changed, _, _ := catalog.update([]aigateway.Deployment{sol, small, router, teamRouter, console}, false, time.Now()); changed {
		t.Fatal("the same listing is no change")
	}
	changedRouter := router
	changedRouter.Router = &aigateway.Router{Models: []aigateway.RouterModel{tierModel("SIMPLE", testSharedModelOne)}}
	if changed, _, _ := catalog.update([]aigateway.Deployment{sol, small, changedRouter, teamRouter, console}, false, time.Now()); !changed {
		t.Fatal("a router's new tiers are a catalog change")
	}
	if fingerprint, _ := catalog.managedFingerprint(); fingerprint != managed {
		t.Fatal("a router's configuration must stay out of the managed fingerprint, which policies' Gateway revisions use")
	}
	if now, _ := catalog.shared(); !slices.Equal(now, shared) {
		t.Fatalf("the shared models must not change: %v -> %v", shared, now)
	}
	// Two router deployments behind one name, listed in either order, are
	// one router.
	second := gatewayRouter(testGatewayRouter, tierModel("COMPLEX", testSmallModel))
	second.ID, second.AccessGroups = second.ID+"-b", []string{gatewayRouterGroup(testGatewayRouter) + "-b"}
	second.Router.Unverifiable = "it runs routing plugins"
	var forward, backward aiGatewayCatalog
	forward.update([]aigateway.Deployment{sol, small, router, second}, false, time.Now())
	backward.update([]aigateway.Deployment{second, router, small, sol}, false, time.Now())
	if forward.routerFingerprint != backward.routerFingerprint || forward.routerFingerprint == "" {
		t.Fatal("the router fingerprint must not depend on the listing order")
	}
	merged := forward.sharedRouters().routers[testGatewayRouter]
	if len(merged.Models) != 3 || !strings.Contains(merged.Unverifiable, "routing plugins") {
		t.Fatalf("merged router = %+v", merged)
	}
	// Without routers there is no router fingerprint: a Gateway without them
	// changes nothing.
	var plain aiGatewayCatalog
	plain.update([]aigateway.Deployment{sol, small}, false, time.Now())
	if plain.routerFingerprint != "" {
		t.Fatal("no routers, no fingerprint")
	}
}
