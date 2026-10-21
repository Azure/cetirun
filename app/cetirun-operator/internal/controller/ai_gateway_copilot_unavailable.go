// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
)

// GitHub Copilot models an owner's account no longer gets. The model list the
// Portal records at sign-in is not refreshed, and GitHub stops serving some of
// those models to the integration Agents use: each request for one, and each
// health check probe of its route, gets a 400 that says so. The routes would
// stay in the Agent's model list, fail every request, and be probed on the
// owner's Copilot allowance every few minutes. When several consecutive probes
// of a route got one of these refusals, the operator removes the model: its
// route is deleted and the Agent no longer offers it. Only these two refusals
// count; a rate limit, a server error or a timeout says nothing about the
// model. A new sign-in records a fresh list and brings the model back. A day
// after its removal the model is registered and probed again: it stays if
// GitHub serves it again, and is removed for another day if the probes after
// its return are refused again.

const (
	// copilotUnavailableProbes is how many consecutive probes must refuse a
	// model before it is removed.
	copilotUnavailableProbes = 3
	// copilotUnavailableRetry is how long a removed model stays out before
	// its route is registered and probed again.
	copilotUnavailableRetry = 24 * time.Hour
)

// copilotPermanentRefusals are GitHub's answers for a model the account's
// integration cannot use, as the Copilot proxy reports them.
var copilotPermanentRefusals = []string{
	"The requested model is not available for integrator",
	"The requested model is not supported",
}

// copilotPermanentRefusal reports whether a probe's error is one of GitHub's
// refusals of the model itself.
func copilotPermanentRefusal(message string) bool {
	return slices.ContainsFunc(copilotPermanentRefusals, func(refusal string) bool { return strings.Contains(message, refusal) })
}

// copilotRouteHealth is what the stored probes of one Copilot route say.
type copilotRouteHealth struct {
	// refusedSince is the time of the oldest of the latest
	// copilotUnavailableProbes probes when each of them got a permanent
	// refusal, and zero otherwise.
	refusedSince time.Time
	// healthyAt is the time of the latest probe when it succeeded.
	healthyAt time.Time
}

// copilotRoutesHealth reads the stored probes, newest first or in any order,
// of every Copilot route. A route with fewer than copilotUnavailableProbes
// probes stored is not judged refused.
func copilotRoutesHealth(checks []aigateway.HealthCheck) map[string]copilotRouteHealth {
	byRoute := map[string][]aigateway.HealthCheck{}
	for _, check := range checks {
		if strings.HasPrefix(check.DeploymentID, aiGatewayCopilotGroupPrefix) {
			byRoute[check.DeploymentID] = append(byRoute[check.DeploymentID], check)
		}
	}
	health := map[string]copilotRouteHealth{}
	for id, routeChecks := range byRoute {
		slices.SortStableFunc(routeChecks, func(a, b aigateway.HealthCheck) int { return b.CheckedAt.Compare(a.CheckedAt) })
		// Two replicas may store a probe of the same cycle; count each time
		// once.
		routeChecks = slices.CompactFunc(routeChecks, func(a, b aigateway.HealthCheck) bool { return a.CheckedAt.Equal(b.CheckedAt) })
		var route copilotRouteHealth
		if routeChecks[0].Healthy {
			route.healthyAt = routeChecks[0].CheckedAt
		}
		if len(routeChecks) >= copilotUnavailableProbes && !slices.ContainsFunc(routeChecks[:copilotUnavailableProbes],
			func(check aigateway.HealthCheck) bool { return check.Healthy || !copilotPermanentRefusal(check.Error) }) {
			route.refusedSince = routeChecks[copilotUnavailableProbes-1].CheckedAt
		}
		health[id] = route
	}
	return health
}

// refreshCopilotHealth reads the Gateway's stored probes of the Copilot
// routes and queues the Agents that hold a route found refused. A failed read
// keeps the last result.
func (r *AgentReconciler) refreshCopilotHealth(ctx context.Context, client aiGatewayKeyClient) {
	catalog := &r.aiGatewayCatalog
	catalog.mu.RLock()
	routes := false
	for id := range catalog.ids {
		routes = routes || strings.HasPrefix(id, aiGatewayCopilotGroupPrefix)
	}
	catalog.mu.RUnlock()
	if !routes {
		catalog.setCopilotHealth(nil)
		return
	}
	// The most LiteLLM returns: a few probe cycles of every deployment.
	checks, err := client.HealthHistory(ctx, aigateway.HealthHistoryLimit)
	if err != nil {
		log.FromContext(ctx).Info("reading the AI Gateway's health checks failed; keeping the last result", "error", err.Error())
		return
	}
	if !catalog.setCopilotHealth(copilotRoutesHealth(checks)) {
		return
	}
	r.notifyAIGatewayAgents(ctx, "GitHub refusing Copilot models", func(agent *cetirunv1alpha1.Agent) bool {
		status := agent.Status.AIGateway
		if status == nil || status.Copilot == nil || !agent.DeletionTimestamp.IsZero() {
			return false
		}
		return slices.ContainsFunc(status.Copilot.Registered, func(id string) bool {
			return !catalog.copilotHealth(id).refusedSince.IsZero()
		})
	})
}

// setCopilotHealth replaces what the probes say and reports whether a route
// is refused now that was not before.
func (c *aiGatewayCatalog) setCopilotHealth(health map[string]copilotRouteHealth) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	newly := false
	for id, route := range health {
		if !route.refusedSince.IsZero() && c.copilotRoutes[id].refusedSince.IsZero() {
			newly = true
		}
	}
	c.copilotRoutes = health
	return newly
}

// copilotHealth is what the latest probes say about a Copilot route.
func (c *aiGatewayCatalog) copilotHealth(id string) copilotRouteHealth {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.copilotRoutes[id]
}

// copilotRouteModel is the Copilot model ID of one of the Agent's route IDs,
// which copilotRegistrationID builds from the Agent UID, the model ID and an
// 8-character revision.
func copilotRouteModel(instance *cetirunv1alpha1.Agent, id string) (string, bool) {
	prefix := aiGatewayCopilotGroupPrefix + string(instance.UID) + "-"
	if !strings.HasPrefix(id, prefix) || len(id) < len(prefix)+10 || id[len(id)-9] != '-' {
		return "", false
	}
	return id[len(prefix) : len(id)-9], true
}

// copilotUnavailable reports whether a recorded model is left out now: it
// was removed less than copilotUnavailableRetry ago.
func copilotUnavailable(model cetirunv1alpha1.AIGatewayCopilotUnavailableModel, now time.Time) bool {
	return now.Sub(model.Since.Time) < copilotUnavailableRetry
}

// copilotUnavailableModels returns the Agent's unavailable Copilot models.
// A model is recorded when GitHub refused the latest probes of its
// registered route; for one recorded before, only probes after its retry
// count. A record is dropped when the owner signs in again, when the model
// list no longer names the model, and when, after the retry, the route's
// latest probe succeeds. Records past their retry stay otherwise, so the old
// refusals stored before the removal cannot remove the model again.
func (r *AgentReconciler) copilotUnavailableModels(instance *cetirunv1alpha1.Agent, copilot *cetirunv1alpha1.AIGatewayCopilotStatus,
	wanted []cetirunv1alpha1.CopilotModel, now time.Time) []cetirunv1alpha1.AIGatewayCopilotUnavailableModel {
	named := map[string]bool{}
	for _, model := range wanted {
		named[model.ID] = true
	}
	var signedInAt time.Time
	if value := instance.Annotations[cetirunv1alpha1.AnnotationCopilotSignedInAt]; value != "" {
		signedInAt, _ = time.Parse(time.RFC3339Nano, value)
	}
	routes := map[string]copilotRouteHealth{}
	for _, id := range copilot.Registered {
		if model, ok := copilotRouteModel(instance, id); ok {
			routes[model] = r.aiGatewayCatalog.copilotHealth(id)
		}
	}
	unavailable := map[string]cetirunv1alpha1.AIGatewayCopilotUnavailableModel{}
	for _, model := range copilot.Unavailable {
		retried := model.Since.Add(copilotUnavailableRetry)
		switch {
		case !named[model.Model], signedInAt.After(model.Since.Time):
		case !copilotUnavailable(model, now) && routes[model.Model].healthyAt.After(retried):
			// GitHub serves the model again.
		default:
			unavailable[model.Model] = model
		}
	}
	for model, route := range routes {
		// Refusals stored before the latest sign-in were for the account's
		// earlier list; the new sign-in gets its own probes.
		if !named[model] || route.refusedSince.IsZero() || !route.refusedSince.After(signedInAt) {
			continue
		}
		previous, recorded := unavailable[model]
		if recorded && (copilotUnavailable(previous, now) || !route.refusedSince.After(previous.Since.Add(copilotUnavailableRetry))) {
			continue
		}
		unavailable[model] = cetirunv1alpha1.AIGatewayCopilotUnavailableModel{Model: model, Since: metav1.NewTime(now)}
	}
	models := make([]cetirunv1alpha1.AIGatewayCopilotUnavailableModel, 0, len(unavailable))
	for _, model := range unavailable {
		models = append(models, model)
	}
	slices.SortFunc(models, func(a, b cetirunv1alpha1.AIGatewayCopilotUnavailableModel) int {
		return strings.Compare(a.Model, b.Model)
	})
	if len(models) == 0 {
		return nil
	}
	return models
}

// copilotUnavailableDue reports whether the Agent's Copilot models need the
// full reconcile: GitHub refused a model whose route is still registered, or a
// removed model is due to be tried again. Without it, an Agent queued because
// of a refusal would take the lightweight pass and keep the route until its
// next key verification.
func (r *AgentReconciler) copilotUnavailableDue(instance *cetirunv1alpha1.Agent, now time.Time) bool {
	status := instance.Status.AIGateway
	if status == nil || status.Copilot == nil || !instance.DeletionTimestamp.IsZero() {
		return false
	}
	if slices.ContainsFunc(status.Copilot.Registered, func(id string) bool {
		return !r.aiGatewayCatalog.copilotHealth(id).refusedSince.IsZero()
	}) {
		return true
	}
	return slices.ContainsFunc(status.Copilot.Unavailable, func(model cetirunv1alpha1.AIGatewayCopilotUnavailableModel) bool {
		return !copilotUnavailable(model, now) && !slices.ContainsFunc(status.Copilot.Routes, func(route cetirunv1alpha1.AIGatewayCopilotRoute) bool {
			return route.Model == model.Model
		})
	})
}
