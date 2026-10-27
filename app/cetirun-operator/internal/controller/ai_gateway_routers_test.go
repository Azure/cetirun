// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
)

const (
	testSmallModel       = "gpt-6-nano"
	testRouterName       = "auto"
	testLegacyRouterTeam = "legacy-team"
)

// The small shared model's group and ID, in the form the Gateway chart
// renders for its name.
var (
	testSmallGroup      = aiGatewayModelAccessGroupPrefix + aiGatewayModelBinding(testSmallModel)
	testSmallDeployment = managedTestDeploymentID(testSmallModel, 1)
)

var fakeRouterRequests sync.Map // *fakeAIGateway -> []aigateway.RouterRequest

// AddRouter registers a router the way LiteLLM does for the operator: as a
// database deployment listed under its model name and groups. LiteLLM keeps
// one router per model name, so a second one is refused here.
func (f *fakeAIGateway) AddRouter(_ context.Context, req aigateway.RouterRequest) error {
	if _, ok := f.models[req.ID]; ok {
		return &aigateway.APIError{StatusCode: 500, Message: "Failed to add model to db"}
	}
	for _, model := range f.models {
		if model.ModelName == req.ModelName {
			return &aigateway.APIError{StatusCode: 400, Message: "Complexity-router deployment already exists"}
		}
	}
	f.models[req.ID] = aigateway.ModelRequest{ID: req.ID, ModelName: req.ModelName, AccessGroups: slices.Clone(req.AccessGroups)}
	f.added = append(f.added, req.ID)
	previous, _ := fakeRouterRequests.Load(f)
	requests, _ := previous.([]aigateway.RouterRequest)
	fakeRouterRequests.Store(f, append(requests, req))
	return nil
}

func routerRequests(f *fakeAIGateway) []aigateway.RouterRequest {
	value, _ := fakeRouterRequests.Load(f)
	requests, _ := value.([]aigateway.RouterRequest)
	return requests
}

func addSmallSharedModel(gateway *fakeAIGateway) {
	gateway.shared = append(gateway.shared, aigateway.Deployment{
		ID: testSmallDeployment, ModelName: testSmallModel, AccessGroups: []string{testSmallGroup},
		DatabaseSourceKnown: true, MaxInputTokens: 400000, MaxOutputTokens: 16384,
	})
}

func testRouter(name, simple, medium, complex string) api.TeamModelRouter {
	return api.TeamModelRouter{Name: name, Classifier: api.TeamModelRouterClassifier{Type: api.ModelRouterClassifierHeuristic},
		Tiers: api.TeamModelRouterTiers{Simple: simple, Medium: medium, Complex: complex}}
}

func routerTeam(routers ...api.TeamModelRouter) *api.Team {
	team := aiGatewayTestTeam(testAIGatewayTeam)
	team.Spec.ModelRouters = routers
	return team
}

func reconcileRouters(t *testing.T, r *AgentReconciler, kube client.Client, team string) *api.Team {
	t.Helper()
	routers := &TeamModelRouterReconciler{Client: kube, Agents: r}
	if _, err := routers.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: team}}); err != nil {
		t.Fatal(err)
	}
	current := &api.Team{}
	if err := kube.Get(context.Background(), types.NamespacedName{Name: team}, current); err != nil {
		t.Fatal(err)
	}
	return current
}

func TestPlanModelRoutersBuildsTheRegistration(t *testing.T) {
	shared := []aiGatewayCatalogModel{{Name: testSmallModel}, {Name: testSharedModelOne}}
	team := routerTeam(testRouter(testRouterName, testSmallModel, testSharedModelOne, testSharedModelOne))
	team.Spec.ModelRouters = append(team.Spec.ModelRouters, api.TeamModelRouter{
		Name: "auto-llm", Classifier: api.TeamModelRouterClassifier{Type: api.ModelRouterClassifierLLM, Model: testSmallModel},
		Tiers: api.TeamModelRouterTiers{Simple: "copilot/fast-1", Medium: testSmallModel, Complex: testSharedModelOne, Reasoning: "copilot/deep-1"},
	})
	plans := planModelRouters(team, shared, nil, nil)
	if len(plans) != 2 || plans[0].problem != "" || plans[1].problem != "" {
		t.Fatalf("plans = %+v", plans)
	}
	plan := plans[0]
	gatewayName := modelRouterGatewayName(testAIGatewayTeam, testRouterName)
	if !strings.HasPrefix(gatewayName, aiGatewayRouterPrefix) || len(gatewayName) != len(aiGatewayRouterPrefix)+16 ||
		plan.gatewayName != gatewayName || modelRouterGatewayName("other-team", testRouterName) == gatewayName {
		t.Fatalf("gateway name %q, plan %q", gatewayName, plan.gatewayName)
	}
	request := plan.request
	if !strings.HasPrefix(request.ID, gatewayName+"-") || len(request.ID) != len(gatewayName)+9 ||
		request.ModelName != gatewayName || !slices.Equal(request.AccessGroups, []string{gatewayName}) ||
		request.DefaultModel != testSharedModelOne || request.InputCostPerToken <= 0 || request.OutputCostPerToken <= 0 {
		t.Fatalf("request = %+v", request)
	}
	body := aigateway.RouterBody(request)
	params := body["litellm_params"].(map[string]any)
	config := params["complexity_router_config"].(map[string]any)
	tiers := config["tiers"].(map[string]any)
	if params["model"] != aigateway.ComplexityRouterModel || config["classifier_type"] != "heuristic" ||
		tiers["SIMPLE"] != testSmallModel || tiers["MEDIUM"] != testSharedModelOne || tiers["COMPLEX"] != testSharedModelOne ||
		tiers["REASONING"] != testSharedModelOne || config["context_compaction"] != false ||
		config["max_tokens_from_tier_model"] != false || config["enable_context_window_escalation"] != true {
		t.Fatalf("config = %+v", config)
	}
	if _, ok := config["classifier_llm_config"]; ok {
		t.Fatal("a heuristic router names no classifier model")
	}
	markers := config["reminder_markers"].([]map[string]string)
	if len(markers) != 2 || markers[0]["open"] != "<<<BEGIN_OPENCLAW_INTERNAL_CONTEXT>>>" || markers[1]["close"] != "</system-reminder>" {
		t.Fatalf("reminder markers = %v", markers)
	}
	info := body["model_info"].(map[string]any)
	if _, ok := info["team_id"]; ok {
		t.Fatal("a team-scoped model needs a LiteLLM licence; the router must not set model_info.team_id")
	}
	if !slices.Equal(plan.shared, []string{testSmallModel, testSharedModelOne}) || len(plan.copilot) != 0 {
		t.Fatalf("models used = %v %v", plan.shared, plan.copilot)
	}

	llm := plans[1]
	llmConfig := aigateway.RouterBody(llm.request)["litellm_params"].(map[string]any)["complexity_router_config"].(map[string]any)
	if llmConfig["classifier_type"] != "llm" || llmConfig["classifier_llm_config"].(map[string]any)["model"] != testSmallModel ||
		!slices.Equal(llm.copilot, []string{"copilot/fast-1", "copilot/deep-1"}) || !slices.Equal(llm.shared, []string{testSmallModel, testSharedModelOne}) {
		t.Fatalf("llm router = %+v, config %v", llm, llmConfig)
	}

	// The ID follows the configuration; the model name does not.
	team.Spec.ModelRouters[0].Tiers.Complex = testSmallModel
	changed := planModelRouters(team, shared, nil, nil)[0]
	if changed.gatewayName != plan.gatewayName || changed.request.ID == plan.request.ID {
		t.Fatalf("a changed router keeps its name %q and gets a new ID, got %q %q", changed.gatewayName, changed.request.ID, plan.request.ID)
	}
	if again := planModelRouters(team, shared, nil, nil)[0]; again.request.ID != changed.request.ID {
		t.Fatal("the registration ID must be deterministic")
	}
}

func TestPlanModelRoutersReportsMisconfiguration(t *testing.T) {
	// auto is a router configured in the Gateway, which is a shared model too.
	shared := []aiGatewayCatalogModel{{Name: testSmallModel}, {Name: testSharedModelOne}, {Name: "auto"}}
	routers := map[string]bool{"auto": true}
	for _, tc := range []struct {
		router api.TeamModelRouter
		want   string
	}{
		{testRouter(testRouterName, testSmallModel, "missing-model", testSharedModelOne), "tier medium names missing-model, which is not a shared model of the AI Gateway"},
		{testRouter(testRouterName, "copilot/bad/id", testSmallModel, testSharedModelOne), "tier simple names copilot/bad/id, which is not a GitHub Copilot model ID"},
		{testRouter(testSharedModelOne, testSmallModel, testSmallModel, testSmallModel), "the name gpt-6-sol is also a shared model"},
		{api.TeamModelRouter{Name: testRouterName, Classifier: api.TeamModelRouterClassifier{Type: api.ModelRouterClassifierLLM, Model: "copilot/fast-1"},
			Tiers: api.TeamModelRouterTiers{Simple: testSmallModel, Medium: testSmallModel, Complex: testSmallModel}}, "the classifier copilot/fast-1 is not a shared model"},
		{testRouter(testRouterName, testSmallModel, "other-router", testSmallModel), "tier medium names other-router"},
		// A router as the classifier would pick a model of its own on the
		// Agent's behalf, outside its key's checks (review); as a tier, it
		// picks one too.
		{api.TeamModelRouter{Name: testRouterName, Classifier: api.TeamModelRouterClassifier{Type: api.ModelRouterClassifierLLM, Model: "auto"},
			Tiers: api.TeamModelRouterTiers{Simple: testSmallModel, Medium: testSmallModel, Complex: testSmallModel}}, "the classifier auto is a router, not a model"},
		{testRouter(testRouterName, testSmallModel, "auto", testSmallModel), "tier medium names auto, which is a router, not a model"},
	} {
		plans := planModelRouters(routerTeam(tc.router), shared, routers, nil)
		if len(plans) != 1 || !strings.Contains(plans[0].problem, tc.want) || !strings.HasPrefix(plans[0].problem, "router "+tc.router.Name+": ") {
			t.Errorf("router %+v: problem %q, want %q", tc.router, plans[0].problem, tc.want)
		}
	}
}

func TestTeamModelRoutersRegisterReplaceAndRemove(t *testing.T) {
	team := routerTeam(testRouter(testRouterName, testSmallModel, testSharedModelOne, testSharedModelOne),
		testRouter("auto-bad", testSmallModel, "missing-model", testSharedModelOne))
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, team, aiGatewayNamespace())
	addSmallSharedModel(gateway)
	// Another operator-registered router no Team defines any more.
	gateway.models["cetirun-router-0000000000000000-deadbeef"] = aigateway.ModelRequest{
		ID: "cetirun-router-0000000000000000-deadbeef", ModelName: "cetirun-router-0000000000000000",
		AccessGroups: []string{"cetirun-router-0000000000000000"},
	}

	current := reconcileRouters(t, r, kube, testAIGatewayTeam)
	requests := routerRequests(gateway)
	gatewayName := modelRouterGatewayName(testAIGatewayTeam, testRouterName)
	if len(requests) != 1 || requests[0].ModelName != gatewayName {
		t.Fatalf("registered %+v, want only the valid router", requests)
	}
	if _, ok := gateway.models["cetirun-router-0000000000000000-deadbeef"]; ok {
		t.Fatal("a router no Team defines must be removed")
	}
	condition := apimeta.FindStatusCondition(current.Status.Conditions, api.ConditionTeamModelRoutersReady)
	if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != "RouterNotRegistered" ||
		!strings.Contains(condition.Message, "router auto-bad: tier medium names missing-model, which is not a shared model of the AI Gateway") {
		t.Fatalf("condition = %+v", condition)
	}
	if apimeta.FindStatusCondition(current.Status.Conditions, api.ConditionTeamAccepted) == nil {
		t.Fatal("the Team's other conditions must stay")
	}
	if len(current.Status.ModelRouters) != 2 || !current.Status.ModelRouters[0].Ready || current.Status.ModelRouters[0].GatewayModel != gatewayName ||
		current.Status.ModelRouters[1].Ready || current.Status.ModelRouters[1].Message == "" {
		t.Fatalf("router status = %+v", current.Status.ModelRouters)
	}

	// Steady state: nothing changes.
	added := len(gateway.added)
	reconcileRouters(t, r, kube, testAIGatewayTeam)
	if len(gateway.added) != added || len(gateway.deletedModels) != 1 {
		t.Fatalf("a steady pass must not touch the Gateway: added %v deleted %v", gateway.added, gateway.deletedModels)
	}

	// A changed router replaces its registration under the same name: the
	// old deployment goes first, since LiteLLM keeps one router per name.
	oldID := requests[0].ID
	current.Spec.ModelRouters = []api.TeamModelRouter{testRouter(testRouterName, testSmallModel, testSmallModel, testSharedModelOne)}
	if err := kube.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	current = reconcileRouters(t, r, kube, testAIGatewayTeam)
	requests = routerRequests(gateway)
	if len(requests) != 2 || requests[1].ModelName != gatewayName || requests[1].ID == oldID || !slices.Contains(gateway.deletedModels, oldID) {
		t.Fatalf("requests %+v deleted %v", requests, gateway.deletedModels)
	}
	if _, ok := gateway.models[oldID]; ok {
		t.Fatal("the old registration must be gone")
	}
	if condition := apimeta.FindStatusCondition(current.Status.Conditions, api.ConditionTeamModelRoutersReady); condition.Status != metav1.ConditionTrue {
		t.Fatalf("condition = %+v", condition)
	}

	// The Team drops its routers: the registration goes and the status clears.
	current.Spec.ModelRouters = nil
	if err := kube.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	current = reconcileRouters(t, r, kube, testAIGatewayTeam)
	if ids, _ := r.aiGatewayCatalog.routerDeployments(); len(ids) != 0 || len(gateway.models) != 0 {
		t.Fatalf("registrations left: %v %v", ids, gateway.models)
	}
	if current.Status.ModelRouters != nil || apimeta.FindStatusCondition(current.Status.Conditions, api.ConditionTeamModelRoutersReady) != nil {
		t.Fatalf("status = %+v", current.Status)
	}
}

// A failed removal is retried even when no Team wants a router any more, so
// a transient Gateway error cannot leave a stale registration (review).
func TestAFailedRouterRemovalIsRetried(t *testing.T) {
	team := routerTeam()
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, _, gateway := aiGatewayTestReconciler(t, agent, team, aiGatewayNamespace())
	gateway.models["cetirun-router-0000000000000000-deadbeef"] = aigateway.ModelRequest{
		ID: "cetirun-router-0000000000000000-deadbeef", ModelName: "cetirun-router-0000000000000000",
		AccessGroups: []string{"cetirun-router-0000000000000000"},
	}
	gateway.deleteModelErr = errors.New("synthetic Gateway outage")
	routers := &TeamModelRouterReconciler{Client: r.Client, Agents: r}
	result, err := routers.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: testAIGatewayTeam}})
	if err != nil {
		t.Fatal(err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("a failed removal must be retried, got %+v", result)
	}
	gateway.deleteModelErr = nil
	if _, err := routers.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: testAIGatewayTeam}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := gateway.models["cetirun-router-0000000000000000-deadbeef"]; ok {
		t.Fatal("the retry removes the stale registration")
	}
}
func TestPolicyBoundAgentGetsAllowedRouter(t *testing.T) {
	team := routerTeam(testRouter(testRouterName, testSmallModel, testSharedModelOne, testSharedModelOne),
		testRouter("auto-unlisted", testSmallModel, testSharedModelOne, testSharedModelOne),
		testRouter("auto-wide", testSmallModel, "admin-wide", testSharedModelOne))
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	agent.Spec.Model = testRouterName
	r, kube, gateway := aiGatewayTestReconciler(t, agent, team, aiGatewayNamespace())
	addSmallSharedModel(gateway)
	gateway.shared = append(gateway.shared, aigateway.Deployment{
		ID: managedTestDeploymentID("admin-wide", 2), ModelName: "admin-wide",
		AccessGroups: []string{aiGatewayModelAccessGroupPrefix + aiGatewayModelBinding("admin-wide")}, DatabaseSourceKnown: true,
	})
	allowTestPolicyModels(t, kube, testSmallModel, testRouterName, "auto-wide")
	reconcileRouters(t, r, kube, testAIGatewayTeam)

	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	gatewayName := modelRouterGatewayName(testAIGatewayTeam, testRouterName)
	status := agent.Status.AIGateway
	wantGroups := sortedCopy([]string{testManagedGroup, testSmallGroup, copilotGroup(), gatewayName})
	if !slices.Equal(status.AccessGroups, wantGroups) || !slices.Equal(lastKeyModels(gateway), wantGroups) {
		t.Fatalf("groups %v key %v, want %v", status.AccessGroups, lastKeyModels(gateway), wantGroups)
	}
	routers := map[string]api.AIGatewayModelRouterStatus{}
	for _, router := range status.ModelRouters {
		routers[router.Name] = router
	}
	if router := routers[testRouterName]; !router.Granted || router.Model != gatewayName || router.ContextWindow != 400000 || router.MaxTokens != 16384 {
		t.Fatalf("auto = %+v", router)
	}
	if router := routers["auto-unlisted"]; router.Granted || router.Reason != "the AgentPolicy does not allow this router" {
		t.Fatalf("auto-unlisted = %+v", router)
	}
	if router := routers["auto-wide"]; router.Granted || router.Reason != "tier medium uses admin-wide, which the AgentPolicy does not allow" {
		t.Fatalf("auto-wide = %+v", router)
	}
	// The Agent lists the router by its name, sends its internal name and
	// uses it as the default it asked for.
	if !slices.Contains(status.Models, gatewayName) || status.DefaultModel != gatewayName {
		t.Fatalf("models %v default %q", status.Models, status.DefaultModel)
	}
	entries, defaultModel, _ := r.aiGatewayModelEntries(agent)
	if defaultModel != gatewayName || !slices.ContainsFunc(entries, func(entry aiGatewayModelEntry) bool {
		return entry.ID == gatewayName && entry.Name == testRouterName && entry.ContextWindow == 400000
	}) {
		t.Fatalf("entries %+v default %q", entries, defaultModel)
	}
	if !strings.Contains(r.aiGatewayRevision(agent), "routers:"+gatewayName+"="+testRouterName) {
		t.Fatalf("the revision must follow the routers: %s", r.aiGatewayRevision(agent))
	}
	// A router name in the policy is not resolved as a shared model, and the
	// router's configuration is not part of the policy's Gateway revision.
	resolution, err := r.resolvePolicy(context.Background(), agent)
	if err != nil {
		t.Fatal(err)
	}
	before := resolution.gatewayRevision
	current := &api.Team{}
	if err := kube.Get(context.Background(), types.NamespacedName{Name: testAIGatewayTeam}, current); err != nil {
		t.Fatal(err)
	}
	current.Spec.ModelRouters[0].Tiers.Complex = testSmallModel
	if err := kube.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	current.Status.ObservedGeneration = current.Generation
	apimeta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{Type: api.ConditionTeamAccepted, Status: metav1.ConditionTrue, Reason: "Valid", ObservedGeneration: current.Generation})
	if err := kube.Status().Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	resolution, err = r.resolvePolicy(context.Background(), agent)
	if err != nil || resolution.gatewayRevision != before {
		t.Fatalf("a router change must not change the policy revision: %v %q %q", err, before, resolution.gatewayRevision)
	}

	// The policy withdraws the router: the key loses it at the next pass. (An
	// Agent that still names it as its model is refused as before.)
	agent.Spec.Model = ""
	policy := &api.AgentPolicy{}
	if err := kube.Get(context.Background(), types.NamespacedName{Name: testPolicyName}, policy); err != nil {
		t.Fatal(err)
	}
	policy.Spec.ModelAccess.SharedModels.Allowed = slices.DeleteFunc(policy.Spec.ModelAccess.SharedModels.Allowed, func(name string) bool { return name == testRouterName })
	if err := kube.Update(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	policy.Status.ObservedGeneration = policy.Generation
	if err := kube.Status().Update(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(lastKeyModels(gateway), gatewayName) || slices.Contains(agent.Status.AIGateway.Models, gatewayName) {
		t.Fatalf("the router must be withdrawn: key %v models %v", lastKeyModels(gateway), agent.Status.AIGateway.Models)
	}
}

func TestCopilotTierRouterFollowsTheOwnersSignIn(t *testing.T) {
	team := routerTeam(testRouter(testRouterName, "copilot/gpt-5.5", testSharedModelOne, "copilot/claude-sonnet-4.5"))
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, team, aiGatewayNamespace(), readyProxy(1))
	allowTestPolicyModels(t, kube, testRouterName)
	reconcileRouters(t, r, kube, testAIGatewayTeam)
	gatewayName := modelRouterGatewayName(testAIGatewayTeam, testRouterName)

	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	if router := agent.Status.AIGateway.ModelRouters[0]; router.Granted ||
		router.Reason != "tier simple uses the GitHub Copilot model gpt-5.5: sign in to GitHub Copilot with an account that offers it" {
		t.Fatalf("before sign-in: %+v", router)
	}

	// The sign-in registers the Copilot models, and the same pass grants the
	// router.
	signIn(t, kube, agent, testCopilotModels)
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	router := agent.Status.AIGateway.ModelRouters[0]
	if !router.Granted || !slices.Contains(lastKeyModels(gateway), gatewayName) || !slices.Contains(agent.Status.AIGateway.AccessGroups, gatewayName) {
		t.Fatalf("after sign-in: router %+v key %v", router, lastKeyModels(gateway))
	}
	// The smallest limits among the tiers: GPT-5.5's, which Copilot reports,
	// and the shared model's.
	if router.ContextWindow != 400000 || router.MaxTokens != 32768 {
		t.Fatalf("limits = %d %d", router.ContextWindow, router.MaxTokens)
	}
	if !slices.Contains(agent.Status.AIGateway.Models, gatewayName) {
		t.Fatalf("models = %v", agent.Status.AIGateway.Models)
	}

	// The account no longer offers a tier's model: the router goes.
	agent.Annotations[api.AnnotationCopilotModels] = `[{"id":"gpt-5.5"}]`
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	if router := agent.Status.AIGateway.ModelRouters[0]; router.Granted || slices.Contains(lastKeyModels(gateway), gatewayName) {
		t.Fatalf("after losing a model: router %+v key %v", router, lastKeyModels(gateway))
	}
}

// A copilot/<ID> tier of a Copilot model that joins a shared model is
// registered as that shared model, which the Agent's own Copilot route joins:
// the router reaches it. An Agent whose policy does not allow that shared
// model is refused the router (review of #263).
func TestCopilotTierOfAMergedModelIsRegisteredAsTheSharedModel(t *testing.T) {
	team := routerTeam(testRouter(testRouterName, "copilot/gpt-5.5", testSharedModelOne, "copilot/gpt-5.5"))
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, team, aiGatewayNamespace(), readyProxy(1))
	gateway.shared = append(gateway.shared, sharedDeployment("team-gpt-a", "team-gpt", func(d *aigateway.Deployment) {
		d.BaseModel, d.MaxInputTokens, d.MaxOutputTokens = "openai/gpt-5.5", 1000000, 64000
	}))
	allowTestPolicyModels(t, kube, testRouterName, "team-gpt")
	ctx := context.Background()
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	current := reconcileRouters(t, r, kube, testAIGatewayTeam)
	// The Team reports the models as registered, for the Portal's offer.
	if status := current.Status.ModelRouters[0]; !slices.Equal(status.SharedModels, []string{testSharedModelOne, "team-gpt"}) || len(status.CopilotModels) != 0 {
		t.Fatalf("router status = %+v", status)
	}
	requests := routerRequests(gateway)
	if len(requests) != 1 {
		t.Fatalf("router requests = %+v", requests)
	}
	tiers := aigateway.RouterBody(requests[0])["litellm_params"].(map[string]any)["complexity_router_config"].(map[string]any)["tiers"].(map[string]any)
	if tiers["SIMPLE"] != "team-gpt" || tiers["COMPLEX"] != "team-gpt" || tiers["MEDIUM"] != testSharedModelOne {
		t.Fatalf("tiers = %v", tiers)
	}
	signIn(t, kube, agent, `[{"id":"gpt-5.5","name":"GPT-5.5","contextWindow":400000,"maxOutputTokens":128000}]`)
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	joined := false
	for _, route := range gateway.models {
		joined = joined || route.Model == "litellm_proxy/gpt-5.5" && route.ModelName == "team-gpt"
	}
	if !joined {
		t.Fatalf("the Agent's Copilot route joins the shared model: %+v", gateway.models)
	}
	gatewayName := modelRouterGatewayName(testAIGatewayTeam, testRouterName)
	router := agent.Status.AIGateway.ModelRouters[0]
	if !router.Granted || !slices.Contains(lastKeyModels(gateway), gatewayName) {
		t.Fatalf("granted router: %+v key %v", router, lastKeyModels(gateway))
	}
	// The smaller limits of the shared model (1,000,000) and the owner's
	// Copilot model (400,000), which both answer the tier (review).
	if router.ContextWindow != 400000 {
		t.Fatalf("limits = %d %d", router.ContextWindow, router.MaxTokens)
	}

	// The policy no longer allows the shared model: the router goes.
	policy := &api.AgentPolicy{}
	if err := kube.Get(ctx, types.NamespacedName{Name: testPolicyName}, policy); err != nil {
		t.Fatal(err)
	}
	policy.Spec.ModelAccess.SharedModels.Allowed = slices.DeleteFunc(policy.Spec.ModelAccess.SharedModels.Allowed,
		func(model string) bool { return model == "team-gpt" })
	if err := kube.Update(ctx, policy); err != nil {
		t.Fatal(err)
	}
	policy.Status.ObservedGeneration = policy.Generation
	if err := kube.Status().Update(ctx, policy); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	router = agent.Status.AIGateway.ModelRouters[0]
	if router.Granted || router.Reason != "tier simple uses the GitHub Copilot model gpt-5.5, which the AI Gateway serves as the shared model team-gpt, and the AgentPolicy does not allow team-gpt" {
		t.Fatalf("refused router: %+v", router)
	}
}

// A copilot/<ID> tier registered under the ID is refused to an Agent whose
// key reaches that Copilot model only under another name.
func TestCopilotTierUnderItsIDNeedsTheAgentsRouteUnderTheID(t *testing.T) {
	plan := modelRouterPlan{
		router:    testRouter(testRouterName, "copilot/gpt-5.5", testSharedModelOne, testSharedModelOne),
		tierNames: map[string]string{"copilot/gpt-5.5": "gpt-5.5"},
	}
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, _, _ := aiGatewayTestReconciler(t, agent)
	reason := r.modelRouterRefusal(agent, plan, nil, true, map[string]string{"copilot/gpt-5.5": "team-gpt"})
	if reason != "tier simple uses the GitHub Copilot model gpt-5.5, which this Agent reaches only as team-gpt" {
		t.Fatalf("reason = %q", reason)
	}
}

// A Team's router that classifies with a router of the Gateway is never
// registered or granted: the classifier call is made on the Agent's behalf
// without its key's checks, and the nested router would send the request to
// a model of its own choice (review).
func TestTeamRouterClassifyingWithAGatewayRouterIsRefused(t *testing.T) {
	team := routerTeam(api.TeamModelRouter{Name: testRouterName,
		Classifier: api.TeamModelRouterClassifier{Type: api.ModelRouterClassifierLLM, Model: testGatewayRouter},
		Tiers:      api.TeamModelRouterTiers{Simple: testSmallModel, Medium: testSmallModel, Complex: testSmallModel}})
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, team, aiGatewayNamespace())
	addSmallSharedModel(gateway)
	gateway.shared = append(gateway.shared, wideSharedModel(), gatewayRouter(testGatewayRouter, tierModel("SIMPLE", testWideModel)))
	allowTestPolicyModels(t, kube, testSmallModel, testRouterName)
	current := reconcileRouters(t, r, kube, testAIGatewayTeam)
	if len(routerRequests(gateway)) != 0 {
		t.Fatalf("registered %+v", routerRequests(gateway))
	}
	want := "router " + testRouterName + ": the classifier " + testGatewayRouter + " is a router, not a model"
	if status := current.Status.ModelRouters; len(status) != 1 || status[0].Ready || status[0].Message != want {
		t.Fatalf("Team router status = %+v", status)
	}
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	router := routerStatuses(agent)[testRouterName]
	if router.Granted || !strings.Contains(router.Reason, "the classifier "+testGatewayRouter+" is a router, not a model") {
		t.Fatalf("Agent's router = %+v", router)
	}
	if slices.Contains(lastKeyModels(gateway), modelRouterGatewayName(testAIGatewayTeam, testRouterName)) {
		t.Fatal("the key must not get the router")
	}
}

// A tier written as a shared model's name reaches the owner's Copilot route
// merged under that name too, and so does a router of the Gateway with such a
// tier: their limits include the route's, as the Agent's entry for the
// shared model does (review).
func TestRouterLimitsIncludeCopilotRoutesMergedUnderASharedTier(t *testing.T) {
	team := routerTeam(testRouter(testRouterName, "team-gpt", "team-gpt", "team-gpt"))
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, team, aiGatewayNamespace(), readyProxy(1))
	chartRouter := gatewayRouter("auto-gpt", tierModel("SIMPLE", "team-gpt"))
	chartRouter.MaxInputTokens, chartRouter.MaxOutputTokens = 1000000, 64000
	gateway.shared = append(gateway.shared, sharedDeployment("team-gpt-a", "team-gpt", func(d *aigateway.Deployment) {
		d.BaseModel, d.MaxInputTokens, d.MaxOutputTokens = "openai/gpt-5.5", 1000000, 64000
	}), chartRouter)
	allowTestPolicyModels(t, kube, testRouterName, "team-gpt", "auto-gpt")
	ctx := context.Background()
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	reconcileRouters(t, r, kube, testAIGatewayTeam)
	signIn(t, kube, agent, `[{"id":"gpt-5.5","name":"GPT-5.5","contextWindow":400000,"maxOutputTokens":128000}]`)
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	if router := routerStatuses(agent)[testRouterName]; !router.Granted || router.ContextWindow != 400000 || router.MaxTokens != 64000 {
		t.Fatalf("Team router = %+v", router)
	}
	entries, _, _ := r.aiGatewayModelEntries(agent)
	seen := 0
	for _, entry := range entries {
		if entry.ID == "team-gpt" || entry.ID == "auto-gpt" {
			seen++
			if entry.ContextWindow != 400000 || entry.MaxTokens != 64000 {
				t.Fatalf("%s entry = %+v", entry.ID, entry)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("entries = %+v", entries)
	}
}

// A router of the Gateway whose tier moves to a shared model the owner's
// smaller Copilot model is merged into lowers the Agent's limits for it with
// nothing else of the Agent changing; the configuration hash must change too,
// or an Agent already deployed keeps the larger limits (review).
func TestGatewayRouterLimitsLoweredByACopilotRouteChangeTheRevision(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace(), readyProxy(1))
	chartRouter := gatewayRouter("auto-gpt", tierModel("SIMPLE", testSharedModelOne))
	chartRouter.MaxInputTokens, chartRouter.MaxOutputTokens = 1000000, 64000
	gateway.shared = append(gateway.shared, sharedDeployment("team-gpt-a", "team-gpt", func(d *aigateway.Deployment) {
		d.BaseModel, d.MaxInputTokens, d.MaxOutputTokens = "openai/gpt-5.5", 1000000, 64000
	}), chartRouter)
	allowTestPolicyModels(t, kube, "team-gpt", "auto-gpt")
	ctx := context.Background()
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	signIn(t, kube, agent, `[{"id":"gpt-5.5","name":"GPT-5.5","contextWindow":400000,"maxOutputTokens":128000}]`)
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	entryLimits := func() [2]int64 {
		entries, _, _ := r.aiGatewayModelEntries(agent)
		for _, entry := range entries {
			if entry.ID == "auto-gpt" {
				return [2]int64{entry.ContextWindow, entry.MaxTokens}
			}
		}
		t.Fatalf("no auto-gpt entry in %+v", entries)
		return [2]int64{}
	}
	if got := entryLimits(); got != [2]int64{1000000, 64000} {
		t.Fatalf("before: %v", got)
	}
	before := r.aiGatewayRevision(agent)

	setRouter(t, gateway, "auto-gpt", aigateway.Router{Models: []aigateway.RouterModel{tierModel("SIMPLE", "team-gpt")}})
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	if got := entryLimits(); got != [2]int64{400000, 64000} {
		t.Fatalf("after: %v", got)
	}
	if after := r.aiGatewayRevision(agent); after == before {
		t.Fatal("the configuration hash must change with the router's lowered limits")
	}
}
func TestRouterNamesDoNotBreakPolicyResolution(t *testing.T) {
	team := routerTeam(testRouter(testRouterName, testSharedModelOne, testSharedModelOne, testSharedModelOne))
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, _ := aiGatewayTestReconciler(t, agent, team, aiGatewayNamespace())
	allowTestPolicyModels(t, kube, testRouterName)
	if _, err := r.resolvePolicy(context.Background(), agent); err != nil {
		t.Fatalf("a router name in the policy must not hold the Agent: %v", err)
	}
	// A name no Team defines is still an unresolvable shared model.
	allowTestPolicyModels(t, kube, "no-such-model")
	if _, err := r.resolvePolicy(context.Background(), agent); err == nil || !isPolicyDependencyError(err) {
		t.Fatalf("an unknown model must still be refused: %v", err)
	}
}

func TestRemovedRouterQueuesTheAgentsGrantedIt(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	agent.Status.AIGateway = &api.AIGatewayStatus{ModelRouters: []api.AIGatewayModelRouterStatus{
		{Name: "gone", Model: "cetirun-router-0000000000000000", Granted: true},
	}}
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	if err := kube.Status().Update(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	gateway.models["cetirun-router-0000000000000000-deadbeef"] = aigateway.ModelRequest{
		ID: "cetirun-router-0000000000000000-deadbeef", ModelName: "cetirun-router-0000000000000000",
		AccessGroups: []string{"cetirun-router-0000000000000000"},
	}
	r.aiGatewayEvents = make(chan event.GenericEvent, 4)
	reconcileRouters(t, r, kube, testAIGatewayTeam)
	if _, ok := gateway.models["cetirun-router-0000000000000000-deadbeef"]; ok {
		t.Fatal("the stale registration must be removed")
	}
	select {
	case queued := <-r.aiGatewayEvents:
		if queued.Object.GetName() != agent.Name {
			t.Fatalf("queued %s", queued.Object.GetName())
		}
	default:
		t.Fatal("the Agent granted the removed router must be queued")
	}
}

func TestTeamModelRoutersReportADisabledGateway(t *testing.T) {
	team := routerTeam(testRouter(testRouterName, testSmallModel, testSharedModelOne, testSharedModelOne))
	r, kube, gateway := aiGatewayTestReconciler(t, team)
	r.AIGateway.Enabled = false
	current := reconcileRouters(t, r, kube, testAIGatewayTeam)
	condition := apimeta.FindStatusCondition(current.Status.Conditions, api.ConditionTeamModelRoutersReady)
	if condition == nil || condition.Status != metav1.ConditionUnknown || !strings.Contains(condition.Message, "not enabled") ||
		len(current.Status.ModelRouters) != 1 || current.Status.ModelRouters[0].Ready || len(routerRequests(gateway)) != 0 {
		t.Fatalf("condition %+v routers %+v", condition, current.Status.ModelRouters)
	}
}
