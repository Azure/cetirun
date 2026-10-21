// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/log"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
)

// An Agent's GitHub Copilot model is registered under the name of the shared
// model it is the same model as, when the Agent is granted that shared model.
// The Agent then sees one model, and the Gateway's routing strategy spreads
// the Agent's requests for it over the shared deployments and the Agent's own
// Copilot route, failing over between them. The route stays in the Agent's
// own access group, so no other key reaches it. A Copilot model that matches
// no shared model the Agent is granted is registered under its own ID,
// unless a model the Gateway serves already has that name.

// copilotMergeView is what Copilot registration needs from a Gateway listing:
// the deployments behind each shared model name, and every name a deployment
// outside the Agents' Copilot groups serves.
type copilotMergeView struct {
	shared map[string][]aigateway.Deployment
	served map[string]bool
}

func newCopilotMergeView() copilotMergeView {
	return copilotMergeView{shared: map[string][]aigateway.Deployment{}, served: map[string]bool{}}
}

// serve records the name of a deployment that is not an Agent's Copilot route.
func (v copilotMergeView) serve(deployment aigateway.Deployment) {
	if !copilotRoute(deployment) {
		v.served[deployment.ModelName] = true
	}
}

// share records a deployment of a shared model Agents are offered.
func (v copilotMergeView) share(deployment aigateway.Deployment) {
	v.shared[deployment.ModelName] = append(v.shared[deployment.ModelName], deployment)
}

// copilotRoute reports whether a deployment is an Agent's Copilot route.
func copilotRoute(deployment aigateway.Deployment) bool {
	return slices.ContainsFunc(deployment.AccessGroups, func(group string) bool {
		return strings.HasPrefix(group, aiGatewayCopilotGroupPrefix)
	})
}

// copilotMergeSnapshot returns the view of the last listing, and false until
// the Gateway has been listed. The view is never modified after the listing.
func (c *aiGatewayCatalog) copilotMergeSnapshot() (copilotMergeView, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.copilotMerge, c.known && c.copilotMerge.shared != nil
}

// realModelKey reduces a model name to the form compared across the two
// sides: without the provider prefix (everything up to the last /), in lower
// case, with dots as dashes (claude-sonnet-4.5 and claude-sonnet-4-5).
func realModelKey(model string) string {
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(model)), ".", "-")
}

// datedModelID reports a model ID that carries a date version, such as
// gpt-4.1-2025-04-14, gpt-4-0613 or claude-3-5-sonnet-20241022: a dash
// separated part of four, six or eight digits. A dated Copilot model is a
// snapshot the shared deployments need not match, so it is never merged.
func datedModelID(id string) bool {
	for _, part := range strings.Split(id, "-") {
		if (len(part) == 4 || len(part) == 6 || len(part) == 8) && strings.Trim(part, "0123456789") == "" {
			return true
		}
	}
	return false
}

// sharedRealModel is the real model behind a shared model name: each
// deployment's model_info.base_model or, without one, its litellm_params.model
// when the price table prices that name. Empty when a deployment's real model
// is unknown or the deployments disagree: such a name never takes a Copilot
// model.
func sharedRealModel(deployments []aigateway.Deployment, priced map[string]bool) string {
	real := ""
	for _, deployment := range deployments {
		model := deployment.BaseModel
		if model == "" && priced[deployment.Model] {
			model = deployment.Model
		}
		key := realModelKey(model)
		if key == "" || real != "" && key != real {
			return ""
		}
		real = key
	}
	return real
}

// copilotRouting copies the shared deployments' routing fields to a Copilot
// route of the same name, so it competes with them on their terms: the lowest
// order any of them sets, and of those at that order the largest weight, rpm
// and tpm any of them sets. Without these a route could be dropped next to
// ordered deployments, or never chosen next to weighted ones.
func copilotRouting(deployments []aigateway.Deployment) aigateway.Routing {
	var out aigateway.Routing
	for _, deployment := range deployments {
		if order := deployment.Routing.Order; order != nil && (out.Order == nil || *order < *out.Order) {
			value := *order
			out.Order = &value
		}
	}
	for _, deployment := range deployments {
		routing := deployment.Routing
		if out.Order != nil && (routing.Order == nil || *routing.Order != *out.Order) {
			continue
		}
		if routing.Weight != nil && (out.Weight == nil || *routing.Weight > *out.Weight) {
			value := *routing.Weight
			out.Weight = &value
		}
		out.RPM = largerCount(out.RPM, routing.RPM)
		out.TPM = largerCount(out.TPM, routing.TPM)
	}
	return out
}

func largerCount(current, next *int64) *int64 {
	if next == nil || current != nil && *current >= *next {
		return current
	}
	value := *next
	return &value
}

// copilotPlacement is where one Copilot model is registered.
type copilotPlacement struct {
	// name is the Gateway model name.
	name string
	// shared reports that name is a shared model's: the route joins its
	// deployments and copies their routing fields. Otherwise the route is
	// the only one the Agent's key reaches under name and is never cooled
	// down: a 429 from Copilot reaches the Agent as it is.
	shared  bool
	routing aigateway.Routing
}

// aiGatewayEntitledShared returns the shared models the Agent may use, and
// whether they are known: none when the owner turned shared models off, those
// the Agent's grant reaches when it is policy-bound, and every shared model
// otherwise, but for a router configured in the Gateway that the Agent may not
// use (ai_gateway_shared_routers.go).
func (r *AgentReconciler) aiGatewayEntitledShared(instance *cetirunv1alpha1.Agent) ([]aiGatewayCatalogModel, bool) {
	if !instance.Spec.SharedModelsEnabled() {
		return nil, true
	}
	if !sandboxRequested(instance, nil) {
		models, known := r.aiGatewayCatalog.shared()
		// The key of an Agent without a policy reaches every shared model but
		// a router it may not use, which is left out here too.
		refused := r.aiGatewaySharedRouterGrant(instance, nil).refused
		return slices.DeleteFunc(models, func(model aiGatewayCatalogModel) bool { return refused[model.Name] }), known
	}
	var groups []string
	if status := instance.Status.AIGateway; status != nil {
		groups = status.AccessGroups
	}
	return r.aiGatewayCatalog.sharedForGroups(groups)
}

// placeCopilotModels decides each Copilot model's Gateway name. A model goes
// under the name of a shared model the Agent is granted whose real model is
// the same, the first such name in sort order when there are several. Any
// other model goes under its own ID. When a model the Gateway serves already
// has that ID, the Copilot model joins the name only if it is the same real
// model, which the Agent is not granted: the route stays in the Agent's own
// access group, so the Agent's key reaches only its own route under that
// name, and like any route it does not share, it is never cooled down.
// Otherwise it is skipped, as the name means another model. ok is false
// until the Gateway has been listed.
func (r *AgentReconciler) placeCopilotModels(instance *cetirunv1alpha1.Agent, models []cetirunv1alpha1.CopilotModel, priced map[string]bool) (map[string]copilotPlacement, []string, bool) {
	view, known := r.aiGatewayCatalog.copilotMergeSnapshot()
	entitledModels, entitledKnown := r.aiGatewayEntitledShared(instance)
	if !known || !entitledKnown {
		return nil, nil, false
	}
	entitled := map[string]bool{}
	for _, model := range entitledModels {
		entitled[model.Name] = true
	}
	byReal := map[string][]string{}
	for _, name := range slices.Sorted(maps.Keys(view.shared)) {
		if real := sharedRealModel(view.shared[name], priced); real != "" {
			byReal[real] = append(byReal[real], name)
		}
	}
	placements := map[string]copilotPlacement{}
	var skipped []string
	for _, model := range models {
		sameModel := !datedModelID(model.ID)
		if sameModel {
			if i := slices.IndexFunc(byReal[realModelKey(model.ID)], func(name string) bool { return entitled[name] }); i >= 0 {
				name := byReal[realModelKey(model.ID)][i]
				placements[model.ID] = copilotPlacement{name: name, shared: true, routing: copilotRouting(view.shared[name])}
				continue
			}
		}
		if view.served[model.ID] && !(sameModel && slices.Contains(byReal[realModelKey(model.ID)], model.ID)) {
			skipped = append(skipped, model.ID)
			continue
		}
		placements[model.ID] = copilotPlacement{name: model.ID}
	}
	slices.Sort(skipped)
	return placements, skipped, true
}

// copilotPlacementRevision covers what a placement puts on the route, so a
// route whose shared model appears, goes or changes its routing is replaced.
func copilotPlacementRevision(placement copilotPlacement) []string {
	return []string{placement.name, strconv.FormatBool(placement.shared), placement.routing.String()}
}

// copilotRoutes returns the Agent's registered Copilot routes. A status
// recorded before routes were listed has only copilot/<ID> names, each its
// own route; they are re-registered under their new names on the next full
// reconcile.
func copilotRoutes(copilot *cetirunv1alpha1.AIGatewayCopilotStatus) []cetirunv1alpha1.AIGatewayCopilotRoute {
	if copilot == nil {
		return nil
	}
	if len(copilot.Routes) > 0 {
		return copilot.Routes
	}
	var routes []cetirunv1alpha1.AIGatewayCopilotRoute
	for _, name := range copilot.Models {
		if id, ok := strings.CutPrefix(name, aiGatewayCopilotModelPrefix); ok && id != "" {
			routes = append(routes, cetirunv1alpha1.AIGatewayCopilotRoute{Model: id, Name: name})
		}
	}
	return routes
}

// copilotModelName returns the Gateway name of the Agent's Copilot model with
// the given ID, or "" when it has no route.
func copilotModelName(copilot *cetirunv1alpha1.AIGatewayCopilotStatus, id string) string {
	for _, route := range copilotRoutes(copilot) {
		if route.Model == id {
			return route.Name
		}
	}
	return ""
}

// aiGatewayRequestedModel is the Agent's own choice of default model.
func aiGatewayRequestedModel(instance *cetirunv1alpha1.Agent) string {
	return aiGatewayModelName(instance, instance.Spec.Model)
}

// aiGatewayModelName returns the name the Agent uses for a model it is
// configured with. copilot/<ID>, the form Copilot models had before they took
// shared models' names or their own IDs, names the Copilot model's route.
func aiGatewayModelName(instance *cetirunv1alpha1.Agent, model string) string {
	model = strings.TrimSpace(model)
	if id, ok := strings.CutPrefix(model, aiGatewayCopilotModelPrefix); ok && instance.Status.AIGateway != nil {
		if name := copilotModelName(instance.Status.AIGateway.Copilot, id); name != "" {
			return name
		}
	}
	return model
}

// copilotDefaultModelAllowed reports whether a policy that allows GitHub
// Copilot accepts model as the Agent's default: copilot/<ID>, or a name in the
// form of a Copilot model ID. A Copilot model is named by its ID or by a
// shared model's name, and the account's models are known only after the
// owner signs in. The default grants nothing: a name the Agent's key does not
// reach is left out of its configuration, which then uses another default.
func copilotDefaultModelAllowed(model string) bool {
	return strings.HasPrefix(model, aiGatewayCopilotModelPrefix) || copilotModelID.MatchString(model)
}

// copilotKeyAliases are the key aliases that keep copilot/<ID> working for
// clients configured before Copilot models took their new names.
func copilotKeyAliases(copilot *cetirunv1alpha1.AIGatewayCopilotStatus) map[string]string {
	if copilot == nil {
		return nil
	}
	aliases := map[string]string{}
	for _, route := range copilot.Routes {
		aliases[aiGatewayCopilotModelPrefix+route.Model] = route.Name
	}
	return aliases
}

// aiGatewayKeyAliases is the key's alias map with the Copilot aliases set to
// the Agent's routes: other aliases, such as an administrator's, are kept;
// copilot/ aliases of models without a route are removed.
func aiGatewayKeyAliases(current map[string]string, copilot *cetirunv1alpha1.AIGatewayCopilotStatus) map[string]string {
	aliases := map[string]string{}
	for name, target := range current {
		if !strings.HasPrefix(name, aiGatewayCopilotModelPrefix) {
			aliases[name] = target
		}
	}
	maps.Copy(aliases, copilotKeyAliases(copilot))
	return aliases
}

// reconcileAIGatewayKeyAliases brings the key's Copilot aliases in line with
// the Agent's routes and reads them back. A failure is reported on the
// Copilot status; it never fails the key reconcile.
func (r *AgentReconciler) reconcileAIGatewayKeyAliases(ctx context.Context, client aiGatewayKeyClient, instance *cetirunv1alpha1.Agent, token string, key *aigateway.Key) {
	status := instance.Status.AIGateway
	var copilot *cetirunv1alpha1.AIGatewayCopilotStatus
	if status != nil {
		copilot = status.Copilot
	}
	var current map[string]string
	if key != nil {
		current = key.Aliases
	}
	wanted := aiGatewayKeyAliases(current, copilot)
	if maps.Equal(current, wanted) || len(current) == 0 && len(wanted) == 0 {
		return
	}
	err := client.UpdateKeyAliases(ctx, token, wanted)
	if err == nil {
		var readBack *aigateway.Key
		readBack, err = client.GetKey(ctx, token)
		if err == nil && (readBack == nil || !maps.Equal(readBack.Aliases, wanted) && !(len(readBack.Aliases) == 0 && len(wanted) == 0)) {
			err = fmt.Errorf("the AI Gateway key's model aliases do not match the Agent's Copilot models")
		}
	}
	if err != nil {
		log.FromContext(ctx).Error(err, "updating the AI Gateway key's Copilot model aliases failed", "instance", instance.Name)
		if copilot != nil {
			copilot.Phase = cetirunv1alpha1.AIGatewayCopilotPhaseDegraded
			copilot.Reason = "KeyAliasUpdateFailed"
		}
	}
}
