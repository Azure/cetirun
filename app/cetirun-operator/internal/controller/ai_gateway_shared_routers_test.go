// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/event"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
)

const (
	testGatewayRouter = "auto-shared"
	testWideModel     = "admin-wide"
)

// testWideGroup is the wide shared model's group in the form the Gateway
// chart renders for its name.
var testWideGroup = aiGatewayModelAccessGroupPrefix + aiGatewayModelBinding(testWideModel)

func tierModel(tier, name string) aigateway.RouterModel {
	return aigateway.RouterModel{Use: aigateway.RouterTier, Tier: tier, Name: name}
}

// gatewayRouter is a router of the Gateway chart's models as the catalog
// lists it: a static, priced deployment with the ID and group the chart
// derives from its name.
func gatewayRouter(name string, models ...aigateway.RouterModel) aigateway.Deployment {
	return aigateway.Deployment{
		ID: managedTestDeploymentID(name, 1), ModelName: name, AccessGroups: []string{gatewayRouterGroup(name)},
		DatabaseSourceKnown: true, Priced: true, Router: &aigateway.Router{Models: models},
	}
}

func gatewayRouterGroup(name string) string {
	return aiGatewayModelAccessGroupPrefix + aiGatewayModelBinding(name)
}

func wideSharedModel() aigateway.Deployment {
	return aigateway.Deployment{ID: managedTestDeploymentID(testWideModel, 2), ModelName: testWideModel, AccessGroups: []string{testWideGroup}, DatabaseSourceKnown: true}
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

// Without a model the Agent or the deployment names, the default is a model:
// a router of the Gateway listed first by name does not become the default.
func TestADefaultModelIsNotAGatewayRouterUnlessNothingElseIsOffered(t *testing.T) {
	team := &api.Team{ObjectMeta: metav1.ObjectMeta{Name: testLegacyRouterTeam, Generation: 1}}
	agent := aiGatewayTestAgent(testLegacyRouterTeam)
	agent.Spec.SandboxRef, agent.Status.Policy, agent.Spec.Model = "", nil, ""
	agent.Spec.ModelAccess = modelAccess(true, false)
	r, _, gateway := aiGatewayTestReconciler(t, agent, team, aiGatewayNamespace())
	r.AIGateway.DefaultModel = ""
	addSmallSharedModel(gateway)
	// "a-router" sorts before every model name.
	gateway.shared = append(gateway.shared, gatewayRouter("a-router", tierModel("SIMPLE", testSmallModel)))
	ctx := context.Background()
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	status := agent.Status.AIGateway
	if !slices.Contains(status.Models, "a-router") || status.DefaultModel == "a-router" || status.DefaultModel == "" {
		t.Fatalf("models %v default %q", status.Models, status.DefaultModel)
	}
	// The Agent asks for the router: it is the default.
	agent.Spec.Model = "a-router"
	if _, def, _ := r.aiGatewayModelEntries(agent); def != "a-router" {
		t.Fatalf("an Agent's own choice of a router is its default, got %q", def)
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
	if now, _ := catalog.shared(); !slices.EqualFunc(now, shared, sameCatalogModel) {
		t.Fatalf("the shared models must not change: %v -> %v", shared, now)
	}
	// Two router deployments behind one name, listed in either order, are
	// one router. They share the name's group, as the chart renders them.
	second := gatewayRouter(testGatewayRouter, tierModel("COMPLEX", testSmallModel))
	second.ID = managedTestDeploymentID(testGatewayRouter, 2)
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

const testDecisionModel = "decision-1"

// decisionModel is a decision model of the Gateway chart's models, such as
// Microsoft-Decision-1, which an auto router classifies requests with.
func decisionModel() aigateway.Deployment {
	return aigateway.Deployment{ID: managedTestDeploymentID(testDecisionModel, 1), ModelName: testDecisionModel,
		AccessGroups: []string{testDecisionGroup}, DatabaseSourceKnown: true, Priced: true, Mode: "evaluation"}
}

var testDecisionGroup = aiGatewayModelAccessGroupPrefix + aiGatewayModelBinding(testDecisionModel)

// A shared model that answers no conversation stays a shared model for the
// routers' rule and the keys, but no Agent is configured with it and the
// published catalog leaves it out.
func TestAIGatewayCatalogLeavesModelsThatAnswerNoConversationOutOfAgentsModels(t *testing.T) {
	var catalog aiGatewayCatalog
	small := aigateway.Deployment{ID: testSmallDeployment, ModelName: testSmallModel, AccessGroups: []string{testSmallGroup},
		DatabaseSourceKnown: true, Mode: "chat"}
	embedding := aigateway.Deployment{ID: "embed", ModelName: "embedder", AccessGroups: []string{aiGatewaySharedAccessGroup}, Mode: "embedding"}
	// Two deployments behind one name, one of them a chat model: the name
	// serves conversations.
	mixedChat := aigateway.Deployment{ID: "mixed-a", ModelName: "mixed", AccessGroups: []string{aiGatewaySharedAccessGroup}, Mode: "responses"}
	mixedOther := aigateway.Deployment{ID: "mixed-b", ModelName: "mixed", AccessGroups: []string{aiGatewaySharedAccessGroup}, Mode: "embedding"}
	// No mode, as for a router or a model the price table does not know.
	unknown := aigateway.Deployment{ID: "unknown", ModelName: "no-mode", AccessGroups: []string{aiGatewaySharedAccessGroup}}
	listing := []aigateway.Deployment{small, decisionModel(), embedding, mixedChat, mixedOther, unknown}
	if changed, _, _ := catalog.update(listing, false, time.Now()); !changed {
		t.Fatal("the first listing is a change")
	}
	shared, known := catalog.shared()
	names := make([]string, 0, len(shared))
	for _, model := range shared {
		names = append(names, model.Name)
	}
	if !known || !slices.Equal(names, []string{testSmallModel, "mixed", "no-mode"}) {
		t.Fatalf("shared = %v", names)
	}
	groups := []string{testSmallGroup, testDecisionGroup}
	if forGroups, _ := catalog.sharedForGroups(groups); len(forGroups) != 1 || forGroups[0].Name != testSmallModel {
		t.Fatalf("a policy that resolves the decision model's group must not configure it: %+v", forGroups)
	}
	// The decision model keeps its group and stays shared for the routers.
	if resolved, ok := catalog.resolvedGroups([]string{testDecisionModel}); !ok || len(resolved) != 1 {
		t.Fatalf("resolved = %v %v", resolved, ok)
	}
	if view := catalog.sharedRouters(); !view.shared[testDecisionModel] || !view.nonChat[testDecisionModel] || view.nonChat["mixed"] {
		t.Fatalf("view shared %v nonChat %v", view.shared, view.nonChat)
	}
	if !slices.Contains(catalog.legacySharedGroups(), testDecisionGroup) {
		t.Fatal("the key of an Agent without a policy keeps every shared model's group")
	}
	if changed, _, _ := catalog.update(listing, false, time.Now()); changed {
		t.Fatal("the same listing is no change")
	}
	// The administrator corrects the decision model's mode: a change, which
	// configures Agents with it.
	listing[1].Mode = "chat"
	if changed, _, _ := catalog.update(listing, false, time.Now()); !changed {
		t.Fatal("a model that now answers conversations is a catalog change")
	}
	if shared, _ := catalog.shared(); !slices.ContainsFunc(shared, func(model aiGatewayCatalogModel) bool { return model.Name == testDecisionModel }) {
		t.Fatalf("shared = %+v", shared)
	}
}

// A router may classify with a decision model, but not send it requests to
// answer.
func TestGatewayRouterMayClassifyWithAModelThatAnswersNoConversation(t *testing.T) {
	view := aiGatewaySharedRouterView{known: true,
		shared:  map[string]bool{testSmallModel: true, testDecisionModel: true, testGatewayRouter: true},
		nonChat: map[string]bool{testDecisionModel: true},
		routers: map[string]aigateway.Router{},
	}
	classifier := aigateway.RouterModel{Use: aigateway.RouterClassifier, Name: testDecisionModel}
	allowed := map[string]bool{testGatewayRouter: true, testSmallModel: true}
	for name, tc := range map[string]struct {
		router  aigateway.Router
		allowed map[string]bool
		want    string
	}{
		"policy, decision classifier":    {aigateway.Router{Models: []aigateway.RouterModel{tierModel("SIMPLE", testSmallModel), classifier}}, allowed, ""},
		"no policy, decision classifier": {aigateway.Router{Models: []aigateway.RouterModel{tierModel("SIMPLE", testSmallModel), classifier}}, nil, ""},
		"a decision model as a tier": {aigateway.Router{Models: []aigateway.RouterModel{tierModel("SIMPLE", testDecisionModel)}}, nil,
			"the router is misconfigured: tier simple names decision-1, which is not a chat model"},
		"a decision model as the default": {aigateway.Router{Models: []aigateway.RouterModel{tierModel("SIMPLE", testSmallModel),
			{Use: aigateway.RouterDefault, Name: testDecisionModel}}}, map[string]bool{testGatewayRouter: true, testSmallModel: true, testDecisionModel: true},
			"the router is misconfigured: the default model is decision-1, which is not a chat model"},
	} {
		t.Run(name, func(t *testing.T) {
			view.routers[testGatewayRouter] = tc.router
			if got := view.refusal(testGatewayRouter, tc.allowed); got != tc.want {
				t.Fatalf("refusal = %q, want %q", got, tc.want)
			}
		})
	}
}

// An Agent without a policy gets a router that classifies with a decision
// model, and its key the decision model's group, but neither its models, its
// default model nor the published catalog list the decision model, even when
// the Agent asks for it.
func TestLegacyAgentIsNotConfiguredWithADecisionModel(t *testing.T) {
	team := &api.Team{ObjectMeta: metav1.ObjectMeta{Name: testLegacyRouterTeam, Generation: 1}}
	agent := aiGatewayTestAgent(testLegacyRouterTeam)
	agent.Spec.SandboxRef = ""
	agent.Spec.Model = testDecisionModel
	agent.Status.Policy = nil
	agent.Spec.ModelAccess = modelAccess(true, false)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, team, aiGatewayNamespace())
	addSmallSharedModel(gateway)
	gateway.shared = append(gateway.shared, decisionModel(),
		gatewayRouter(testGatewayRouter, tierModel("SIMPLE", testSmallModel),
			aigateway.RouterModel{Use: aigateway.RouterClassifier, Name: testDecisionModel}))
	ctx := context.Background()
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	status := agent.Status.AIGateway
	if slices.Contains(status.Models, testDecisionModel) || status.DefaultModel == testDecisionModel || status.DefaultModel == "" {
		t.Fatalf("models %v default %q", status.Models, status.DefaultModel)
	}
	if !slices.Contains(status.Models, testGatewayRouter) || !slices.Contains(lastKeyModels(gateway), gatewayRouterGroup(testGatewayRouter)) {
		t.Fatalf("the router must be granted: models %v key %v", status.Models, lastKeyModels(gateway))
	}
	if !slices.Contains(lastKeyModels(gateway), testDecisionGroup) {
		t.Fatalf("the key keeps every shared model's group: %v", lastKeyModels(gateway))
	}
	entries, _, _ := r.aiGatewayModelEntries(agent)
	for _, entry := range entries {
		if entry.ID == testDecisionModel {
			t.Fatalf("entries %+v", entries)
		}
	}
	r.publishAIGatewayCatalog(ctx, 0)
	configMap := &corev1.ConfigMap{}
	if err := kube.Get(ctx, types.NamespacedName{Namespace: "cetirun-system", Name: api.AIGatewayCatalogConfigMap}, configMap); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(configMap.Data[api.AIGatewayCatalogKey], testDecisionModel) {
		t.Fatalf("the published catalog lists the decision model: %s", configMap.Data[api.AIGatewayCatalogKey])
	}
}

// The published catalog names the models each shared router uses, so the
// Portal offers a router only to Teams whose policy lists every one of them.
func TestPublishedCatalogNamesTheModelsOfEachSharedRouter(t *testing.T) {
	var catalog aiGatewayCatalog
	sol := aigateway.Deployment{ID: testManagedDeploymentID, ModelName: testSharedModelOne, AccessGroups: []string{testManagedGroup}, DatabaseSourceKnown: true}
	small := aigateway.Deployment{ID: testSmallDeployment, ModelName: testSmallModel, AccessGroups: []string{testSmallGroup}, DatabaseSourceKnown: true}
	classifier := tierModel("", testSmallModel)
	classifier.Use, classifier.Tier = aigateway.RouterClassifier, ""
	router := gatewayRouter(testGatewayRouter, tierModel("SIMPLE", testSmallModel), tierModel("MEDIUM", testSharedModelOne),
		tierModel("COMPLEX", testSharedModelOne), classifier)
	catalog.update([]aigateway.Deployment{sol, small, router}, false, time.Now())
	shared, _ := catalog.shared()
	described := catalog.sharedRouters().describe(shared)
	byName := map[string]api.AIGatewayCatalogModel{}
	for _, model := range described {
		byName[model.Name] = model
	}
	if got := byName[testGatewayRouter].RouterModels; !slices.Equal(got, slices.Sorted(slices.Values([]string{testSharedModelOne, testSmallModel}))) {
		t.Fatalf("router models = %v", got)
	}
	if byName[testGatewayRouter].RouterUnverifiable || byName[testSharedModelOne].RouterModels != nil {
		t.Fatalf("described = %+v", described)
	}
	unverifiable := router
	unverifiable.Router = &aigateway.Router{Models: router.Router.Models, Unverifiable: "it runs routing plugins"}
	catalog.update([]aigateway.Deployment{sol, small, unverifiable}, false, time.Now())
	shared, _ = catalog.shared()
	for _, model := range catalog.sharedRouters().describe(shared) {
		if model.Name == testGatewayRouter && !model.RouterUnverifiable {
			t.Fatal("an unverifiable router must say so")
		}
	}
	// The operator's own view never takes them from the published catalog.
	var seeded aiGatewayCatalog
	seeded.seed(described)
	if now, _ := seeded.shared(); len(now) == 0 || slices.ContainsFunc(now, func(m api.AIGatewayCatalogModel) bool { return m.RouterModels != nil }) {
		t.Fatalf("seeded = %+v", now)
	}
}

// The rollout hold compares a listing with the catalog the operator applies,
// whether each shared model answers conversations included (review).
func TestAIGatewayCatalogChangedBySeesADecisionModelBecomeAChatModel(t *testing.T) {
	var catalog aiGatewayCatalog
	decision := decisionModel()
	sol := aigateway.Deployment{ID: testManagedDeploymentID, ModelName: testSharedModelOne, AccessGroups: []string{testManagedGroup}, DatabaseSourceKnown: true}
	catalog.update([]aigateway.Deployment{sol, decision}, false, time.Now())
	if catalog.changedBy([]aigateway.Deployment{sol, decision}, false) {
		t.Fatal("the same listing is no change")
	}
	decision.Mode = "chat"
	if !catalog.changedBy([]aigateway.Deployment{sol, decision}, false) {
		t.Fatal("a model that now answers conversations is a change")
	}
}
