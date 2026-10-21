// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
)

// The refusals as LiteLLM stores them for a probe of a Copilot route: the
// proxy's error, wrapped by the Gateway.
const (
	testNotForIntegrator = `litellm.BadRequestError: Error code: 400 - {'error': {'message': 'litellm.BadRequestError: Github_copilotException - {"error":{"message":"The requested model is not available for integrator \\"vscode-chat\\". Available models: [gpt-4.1]","code":"model_not_supported"}}', 'code': '400'}}`
	testNotSupported     = `litellm.BadRequestError: Error code: 400 - {'error': {'message': "litellm.BadRequestError: Github_copilotException - The requested model is not supported.", 'code': '400'}}`
	testRateLimited      = `litellm.RateLimitError: Error code: 429 - {'error': {'message': 'Github_copilotException - too many requests'}}`
)

func probes(id string, at time.Time, errs ...string) []aigateway.HealthCheck {
	checks := make([]aigateway.HealthCheck, 0, len(errs))
	for i, err := range errs {
		checks = append(checks, aigateway.HealthCheck{DeploymentID: id, Healthy: err == "", Error: err,
			CheckedAt: at.Add(-time.Duration(i) * 5 * time.Minute)})
	}
	return checks
}

func TestCopilotRoutesHealthCountsOnlyConsecutivePermanentRefusals(t *testing.T) {
	now := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	route := func(name string) string { return aiGatewayCopilotGroupPrefix + "uid-" + name + "-0123abcd" }
	var checks []aigateway.HealthCheck
	checks = append(checks, probes(route("refused"), now, testNotForIntegrator, testNotSupported, testNotForIntegrator, "")...)
	checks = append(checks, probes(route("two"), now, testNotForIntegrator, testNotForIntegrator)...)
	checks = append(checks, probes(route("rate-limited"), now, testNotForIntegrator, testRateLimited, testNotForIntegrator)...)
	checks = append(checks, probes(route("recovered"), now, "", testNotForIntegrator, testNotForIntegrator, testNotForIntegrator)...)
	checks = append(checks, probes("cetirun-shared-0123", now, testNotSupported, testNotSupported, testNotSupported)...)
	// A second replica stored the same probes: each time counts once.
	dup := probes(route("duplicated"), now, testNotSupported, testNotSupported)
	checks = append(checks, append(dup, dup...)...)
	// Listed in any order.
	slices.Reverse(checks)
	health := copilotRoutesHealth(checks)
	if got := health[route("refused")].refusedSince; !got.Equal(now.Add(-10 * time.Minute)) {
		t.Fatalf("refused since %v", got)
	}
	for _, name := range []string{"two", "rate-limited", "recovered", "duplicated"} {
		if !health[route(name)].refusedSince.IsZero() {
			t.Errorf("%s must not be judged refused", name)
		}
	}
	if !health[route("recovered")].healthyAt.Equal(now) || !health[route("refused")].healthyAt.IsZero() {
		t.Fatalf("healthy at: %+v", health)
	}
	if _, ok := health["cetirun-shared-0123"]; ok {
		t.Fatal("only Copilot routes are judged")
	}
}

func TestCopilotRouteModelReadsTheModelOfTheAgentsRoutes(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	for id, want := range map[string]string{
		copilotRegistrationID(agent, "gpt-5.5", "0123abcd"):          "gpt-5.5",
		copilotRegistrationID(agent, "claude-opus-4.8", "deadbeef"):  "claude-opus-4.8",
		copilotRegistrationID(agent, "a-b-c-d", "0123abcd"):          "a-b-c-d",
		aiGatewayCopilotGroupPrefix + "other-uid-gpt-5.5-0123abcd":   "",
		aiGatewayCopilotGroupPrefix + testAIGatewayUID + "-x":        "",
		aiGatewayCopilotGroupPrefix + testAIGatewayUID + "-gpt5abcd": "",
	} {
		got, ok := copilotRouteModel(agent, id)
		if got != want || ok != (want != "") {
			t.Errorf("copilotRouteModel(%q) = %q, %v; want %q", id, got, ok, want)
		}
	}
}

// A Copilot model GitHub no longer serves to the account is removed after
// three refused probes, brought back by a new sign-in, retried a day later,
// and removed again only by refusals after the retry.
func TestReconcileAIGatewayCopilotRemovesModelsGitHubRefuses(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace(), readyProxy(1))
	ctx := context.Background()
	r.aiGatewayEvents = make(chan event.GenericEvent, 8)
	reconcile := func() {
		t.Helper()
		if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
			t.Fatal(err)
		}
	}
	reconcile()
	signIn(t, kube, agent, testCopilotModels)
	reconcile()
	routeOf := func(name string) string {
		for id, model := range gateway.models {
			if model.ModelName == name {
				return id
			}
		}
		return ""
	}
	gpt := routeOf("gpt-5.5")
	if gpt == "" || !slices.Contains(agent.Status.AIGateway.Models, "gpt-5.5") {
		t.Fatalf("routes %v models %v", gateway.modelNames(), agent.Status.AIGateway.Models)
	}

	// Two refusals and a rate limit: nothing happens.
	now := time.Now()
	gateway.health = append(probes(gpt, now, testNotForIntegrator, testRateLimited, testNotForIntegrator),
		probes(routeOf("claude-sonnet-4.5"), now, "", "", "")...)
	r.refreshCopilotHealth(ctx, gateway)
	reconcile()
	if routeOf("gpt-5.5") == "" || agent.Status.AIGateway.Copilot.Unavailable != nil {
		t.Fatalf("a rate limit says nothing about the model: %+v", agent.Status.AIGateway.Copilot)
	}

	// Three refusals in a row: the Agent is queued, the route goes and the
	// Agent no longer offers the model; the other model stays.
	// The queue reads the Agents' stored status.
	if err := kube.Status().Update(ctx, agent.DeepCopy()); err != nil {
		t.Fatal(err)
	}
	gateway.health = append(probes(gpt, now, testNotForIntegrator, testNotSupported, testNotForIntegrator),
		probes(routeOf("claude-sonnet-4.5"), now, "", "", "")...)
	r.refreshCopilotHealth(ctx, gateway)
	select {
	case queued := <-r.aiGatewayEvents:
		if queued.Object.GetName() != agent.Name {
			t.Fatalf("queued %s", queued.Object.GetName())
		}
	default:
		t.Fatal("the Agent holding the refused route must be queued")
	}
	reconcile()
	copilot := agent.Status.AIGateway.Copilot
	if routeOf("gpt-5.5") != "" || slices.Contains(agent.Status.AIGateway.Models, "gpt-5.5") ||
		len(copilot.Unavailable) != 1 || copilot.Unavailable[0].Model != "gpt-5.5" || routeOf("claude-sonnet-4.5") == "" {
		t.Fatalf("routes %v models %v copilot %+v", gateway.modelNames(), agent.Status.AIGateway.Models, copilot)
	}
	if copilot.Phase != cetirunv1alpha1.AIGatewayCopilotPhaseReady {
		t.Fatalf("a removed model does not degrade the proxy: %+v", copilot)
	}
	// Steady: it stays out without new probes, and nothing is re-added.
	added := len(gateway.added)
	reconcile()
	if routeOf("gpt-5.5") != "" || len(gateway.added) != added {
		t.Fatal("a removed model must stay out")
	}

	// A day later it is registered and probed again. The refusals stored
	// before its removal do not remove it again.
	agent.Status.AIGateway.Copilot.Unavailable[0].Since = metav1.NewTime(now.Add(-copilotUnavailableRetry - time.Minute))
	reconcile()
	if routeOf("gpt-5.5") != gpt || len(agent.Status.AIGateway.Copilot.Unavailable) != 1 {
		t.Fatalf("the model must be tried again: routes %v copilot %+v", gateway.modelNames(), agent.Status.AIGateway.Copilot)
	}
	r.refreshCopilotHealth(ctx, gateway)
	reconcile()
	if routeOf("gpt-5.5") != gpt {
		t.Fatal("refusals from before the retry must not remove the model again")
	}
	// Refused again after the retry: out for another day.
	later := time.Now().Add(15 * time.Minute)
	gateway.health = probes(gpt, later, testNotForIntegrator, testNotForIntegrator, testNotForIntegrator)
	r.refreshCopilotHealth(ctx, gateway)
	reconcile()
	if routeOf("gpt-5.5") != "" || !copilotUnavailable(agent.Status.AIGateway.Copilot.Unavailable[0], time.Now()) {
		t.Fatalf("refused after the retry: %+v", agent.Status.AIGateway.Copilot)
	}

	// A new sign-in brings it back at once.
	agent.Annotations[cetirunv1alpha1.AnnotationCopilotSignedInAt] = time.Now().Add(20 * time.Minute).UTC().Format(time.RFC3339Nano)
	reconcile()
	if routeOf("gpt-5.5") == "" || agent.Status.AIGateway.Copilot.Unavailable != nil {
		t.Fatalf("a new sign-in must bring the model back: routes %v copilot %+v", gateway.modelNames(), agent.Status.AIGateway.Copilot)
	}
	// The refusals stored before that sign-in do not remove it.
	r.refreshCopilotHealth(ctx, gateway)
	reconcile()
	if routeOf("gpt-5.5") == "" {
		t.Fatal("refusals from before the sign-in must not remove the model")
	}
}

// After its retry, a model whose probes succeed again is no longer recorded.
func TestReconcileAIGatewayCopilotForgetsAModelGitHubServesAgain(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace(), readyProxy(1))
	ctx := context.Background()
	for range 2 {
		if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
			t.Fatal(err)
		}
		if agent.Status.AIGateway.Copilot.Registered == nil {
			signIn(t, kube, agent, testCopilotModels)
		}
	}
	since := time.Now().Add(-copilotUnavailableRetry - time.Hour)
	agent.Status.AIGateway.Copilot.Unavailable = []cetirunv1alpha1.AIGatewayCopilotUnavailableModel{
		{Model: "gpt-5.5", Since: metav1.NewTime(since)},
		// The account's list no longer names it: the record goes.
		{Model: "gone-model", Since: metav1.NewTime(time.Now())},
	}
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	var gpt string
	for id, model := range gateway.models {
		if model.ModelName == "gpt-5.5" {
			gpt = id
		}
	}
	if gpt == "" || len(agent.Status.AIGateway.Copilot.Unavailable) != 1 {
		t.Fatalf("retried: routes %v copilot %+v", gateway.modelNames(), agent.Status.AIGateway.Copilot)
	}
	gateway.health = probes(gpt, time.Now(), "", testNotForIntegrator, testNotForIntegrator)
	r.refreshCopilotHealth(ctx, gateway)
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		t.Fatal(err)
	}
	if agent.Status.AIGateway.Copilot.Unavailable != nil {
		t.Fatalf("a model served again must be forgotten: %+v", agent.Status.AIGateway.Copilot.Unavailable)
	}
}

// A failed read of the health checks keeps what the last read said.
func TestRefreshCopilotHealthKeepsTheLastResultOnFailure(t *testing.T) {
	r := &AgentReconciler{}
	r.aiGatewayCatalog.update([]aigateway.Deployment{{ID: aiGatewayCopilotGroupPrefix + "uid-gpt-0123abcd", ModelName: "gpt"}}, false, time.Now())
	gateway := newFakeAIGateway()
	gateway.health = probes(aiGatewayCopilotGroupPrefix+"uid-gpt-0123abcd", time.Now(), testNotSupported, testNotSupported, testNotSupported)
	r.refreshCopilotHealth(context.Background(), gateway)
	gateway.healthErr = &aigateway.APIError{StatusCode: 500, Message: "synthetic"}
	r.refreshCopilotHealth(context.Background(), gateway)
	if r.aiGatewayCatalog.copilotHealth(aiGatewayCopilotGroupPrefix + "uid-gpt-0123abcd").refusedSince.IsZero() {
		t.Fatal("a failed read must keep the last result")
	}
}
