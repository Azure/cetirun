// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
)

const (
	// aiGatewayCatalogMaxAge is how old a catalog a reconcile accepts before
	// listing the Gateway's models again.
	aiGatewayCatalogMaxAge = time.Minute
	// aiGatewayCatalogInterval is how often the catalog is refreshed and
	// published in the background.
	aiGatewayCatalogInterval = 2 * time.Minute
)

// aiGatewayCatalogModel is one shared model with its limits; the Portal reads
// the same shape from the published catalog.
type aiGatewayCatalogModel = cetirunv1alpha1.AIGatewayCatalogModel

// aiGatewayCatalog is the operator's view of the Gateway's model
// deployments. The zero value knows nothing yet.
type aiGatewayCatalog struct {
	refreshMu sync.Mutex
	mu        sync.RWMutex
	known     bool
	// seeded marks shared models taken from the published catalog while the
	// Gateway could not be listed, such as at operator start during a
	// Gateway outage: Agents keep their model lists instead of losing the
	// limits and being restarted twice.
	seeded    bool
	fetchedAt time.Time
	sharedSet []aiGatewayCatalogModel
	ids       map[string]bool
	groups    map[string][]string
}

func (c *aiGatewayCatalog) age(now time.Time) time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.known {
		return time.Duration(1<<63 - 1)
	}
	return now.Sub(c.fetchedAt)
}

// update replaces the view and reports whether the shared models changed.
func (c *aiGatewayCatalog) update(deployments []aigateway.Deployment, now time.Time) (bool, []string) {
	var skipped []string
	ids := map[string]bool{}
	groups := map[string][]string{}
	byName := map[string]aiGatewayCatalogModel{}
	for _, deployment := range deployments {
		ids[deployment.ID] = true
		for _, group := range deployment.AccessGroups {
			groups[group] = append(groups[group], deployment.ID)
		}
		if !slices.Contains(deployment.AccessGroups, aiGatewaySharedAccessGroup) {
			continue
		}
		if !aiGatewaySharedModelUsable(deployment.ModelName) {
			skipped = append(skipped, deployment.ModelName)
			continue
		}
		model := byName[deployment.ModelName]
		model.Name = deployment.ModelName
		// Deployments behind one name are load-balanced; configure the
		// Agent with the smallest limits any of them has.
		model.ContextWindow = smallerPositive(model.ContextWindow, deployment.MaxInputTokens)
		model.MaxOutputTokens = smallerPositive(model.MaxOutputTokens, deployment.MaxOutputTokens)
		byName[deployment.ModelName] = model
	}
	shared := make([]aiGatewayCatalogModel, 0, len(byName))
	for _, model := range byName {
		shared = append(shared, model)
	}
	sort.Slice(shared, func(i, j int) bool { return shared[i].Name < shared[j].Name })
	c.mu.Lock()
	defer c.mu.Unlock()
	changed := !(c.known || c.seeded) || !slices.Equal(c.sharedSet, shared)
	c.known, c.seeded, c.fetchedAt, c.sharedSet, c.ids, c.groups = true, false, now, shared, ids, groups
	return changed, skipped
}

// seed takes shared models from the published catalog until the Gateway can
// be listed. It never replaces a listed view.
func (c *aiGatewayCatalog) seed(shared []aiGatewayCatalogModel) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.known || c.seeded {
		return
	}
	c.sharedSet, c.seeded = slices.Clone(shared), true
}

// aiGatewayModelNameLimit is the agent chart's limit, in characters, on a
// configured model's id and name (values.schema.json). A single longer value
// makes the Agent's whole model configuration invalid, so such names are left
// out rather than passed to Helm.
const aiGatewayModelNameLimit = 256

// aiGatewaySharedModelUsable reports whether a shared model name can be given
// to Agents: the agent chart rejects names with the provider prefix Agents
// use and names over its length limit, and the Copilot prefix belongs to the
// Agents' own Copilot routes.
func aiGatewaySharedModelUsable(name string) bool {
	if name == "" || strings.ContainsAny(name, " \t\r\n") || utf8.RuneCountInString(name) > aiGatewayModelNameLimit {
		return false
	}
	return !strings.HasPrefix(name, "openai/") && !strings.HasPrefix(name, aiGatewayCopilotModelPrefix)
}

func smallerPositive(current, next int64) int64 {
	if next <= 0 {
		return current
	}
	if current <= 0 || next < current {
		return next
	}
	return current
}

func (c *aiGatewayCatalog) shared() ([]aiGatewayCatalogModel, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return slices.Clone(c.sharedSet), c.known || c.seeded
}

// has reports whether a deployment ID exists, and whether the catalog knows.
func (c *aiGatewayCatalog) has(id string) (bool, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ids[id], c.known
}

func (c *aiGatewayCatalog) groupDeployments(group string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return slices.Clone(c.groups[group])
}

// noteAdded and noteDeleted keep a known view in step with the operator's
// own changes, so a view listed before a registration does not report the
// new route as lost.
func (c *aiGatewayCatalog) noteAdded(id string, groups []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.known {
		return
	}
	c.ids[id] = true
	for _, group := range groups {
		if !slices.Contains(c.groups[group], id) {
			c.groups[group] = append(c.groups[group], id)
		}
	}
}

func (c *aiGatewayCatalog) noteDeleted(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.known {
		return
	}
	delete(c.ids, id)
	for group, ids := range c.groups {
		c.groups[group] = slices.DeleteFunc(slices.Clone(ids), func(value string) bool { return value == id })
	}
}

// refreshAIGatewayCatalog lists the Gateway's models when the catalog is
// older than maxAge. A failure keeps the previous view.
func (r *AgentReconciler) refreshAIGatewayCatalogWithin(ctx context.Context, client aiGatewayKeyClient, maxAge time.Duration) bool {
	catalog := &r.aiGatewayCatalog
	catalog.refreshMu.Lock()
	defer catalog.refreshMu.Unlock()
	if catalog.age(time.Now()) < maxAge {
		return false
	}
	deployments, err := client.ListModels(ctx)
	if err != nil {
		log.FromContext(ctx).Error(err, "listing AI Gateway models failed; keeping the previous catalog")
		r.seedAIGatewayCatalog(ctx)
		return false
	}
	changed, skipped := catalog.update(deployments, time.Now())
	if len(skipped) > 0 {
		log.FromContext(ctx).Info("shared models with names Agents cannot use are left out", "models", skipped)
	}
	return changed
}

// seedAIGatewayCatalog loads the last published shared models, once, when the
// Gateway cannot be listed and nothing is known yet.
func (r *AgentReconciler) seedAIGatewayCatalog(ctx context.Context) {
	if _, known := r.aiGatewayCatalog.shared(); known {
		return
	}
	configMap := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: r.catalogNamespace(), Name: cetirunv1alpha1.AIGatewayCatalogConfigMap}
	if err := r.Get(ctx, key, configMap); err != nil {
		return
	}
	var catalog cetirunv1alpha1.AIGatewayCatalog
	if err := json.Unmarshal([]byte(configMap.Data[cetirunv1alpha1.AIGatewayCatalogKey]), &catalog); err != nil {
		return
	}
	r.aiGatewayCatalog.seed(catalog.SharedModels)
}

func (r *AgentReconciler) catalogNamespace() string {
	if r.OperatorNamespace == "" {
		return "cetirun-system"
	}
	return r.OperatorNamespace
}

func (r *AgentReconciler) refreshAIGatewayCatalog(ctx context.Context, client aiGatewayKeyClient) {
	if r.refreshAIGatewayCatalogWithin(ctx, client, aiGatewayCatalogMaxAge) {
		r.notifyAIGatewayCatalogChanged(ctx)
	}
}

// AIGatewayCatalogPublisher keeps the catalog fresh, publishes the shared
// models for the Portal and reconciles Agents when the shared models change
// or their Copilot routes go missing.
type AIGatewayCatalogPublisher struct {
	Reconciler *AgentReconciler
	Interval   time.Duration
}

// NeedLeaderElection runs the publisher only on the leader, next to the
// reconciler that consumes the catalog.
func (p *AIGatewayCatalogPublisher) NeedLeaderElection() bool { return true }

// Start implements manager.Runnable.
func (p *AIGatewayCatalogPublisher) Start(ctx context.Context) error {
	interval := p.Interval
	if interval <= 0 {
		interval = aiGatewayCatalogInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		p.Reconciler.publishAIGatewayCatalog(ctx, interval/2)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (r *AgentReconciler) publishAIGatewayCatalog(ctx context.Context, maxAge time.Duration) {
	logger := log.FromContext(ctx).WithName("ai-gateway-catalog")
	if !r.AIGateway.Enabled {
		return
	}
	client, err := r.aiGatewayClient(ctx)
	if err != nil {
		logger.Error(err, "AI Gateway client unavailable")
		return
	}
	if r.refreshAIGatewayCatalogWithin(ctx, client, maxAge) {
		r.notifyAIGatewayCatalogChanged(ctx)
	}
	r.notifyAIGatewayRoutesLost(ctx)
	shared, known := r.aiGatewayCatalog.shared()
	if !known {
		return
	}
	if err := r.writeAIGatewayCatalog(ctx, shared); err != nil {
		logger.Error(err, "publishing the AI Gateway catalog failed")
	}
}

func (r *AgentReconciler) writeAIGatewayCatalog(ctx context.Context, shared []aiGatewayCatalogModel) error {
	namespace := r.catalogNamespace()
	if shared == nil {
		shared = []aiGatewayCatalogModel{}
	}
	raw, err := json.Marshal(cetirunv1alpha1.AIGatewayCatalog{SharedModels: shared})
	if err != nil {
		return err
	}
	key := types.NamespacedName{Namespace: namespace, Name: cetirunv1alpha1.AIGatewayCatalogConfigMap}
	configMap := &corev1.ConfigMap{}
	err = r.Get(ctx, key, configMap)
	if apierrors.IsNotFound(err) {
		configMap = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Labels: map[string]string{
				"app.kubernetes.io/managed-by": "cetirun-operator", "app.kubernetes.io/component": "ai-gateway-catalog",
			}},
			Data: map[string]string{cetirunv1alpha1.AIGatewayCatalogKey: string(raw)},
		}
		return r.Create(ctx, configMap)
	}
	if err != nil {
		return fmt.Errorf("get catalog ConfigMap: %w", err)
	}
	if configMap.Data[cetirunv1alpha1.AIGatewayCatalogKey] == string(raw) {
		return nil
	}
	if configMap.Data == nil {
		configMap.Data = map[string]string{}
	}
	configMap.Data[cetirunv1alpha1.AIGatewayCatalogKey] = string(raw)
	return r.Update(ctx, configMap)
}

// notifyAIGatewayCatalogChanged queues every Agent routed through the
// Gateway, so a new or removed shared model reaches Agent configurations
// without waiting for the periodic verification.
func (r *AgentReconciler) notifyAIGatewayCatalogChanged(ctx context.Context) {
	r.notifyAIGatewayAgents(ctx, "an AI Gateway catalog change", func(agent *cetirunv1alpha1.Agent) bool {
		return agent.Status.AIGateway != nil && agent.Status.AIGateway.Routed && agent.Spec.SharedModelsEnabled()
	})
}

// notifyAIGatewayRoutesLost queues every Agent whose recorded Copilot routes
// the Gateway no longer has, such as after it lost its data. Its reconcile
// adds them again, and creates the key again if that is gone too. Without
// this, an Agent without shared models hears of the loss from no catalog
// change and waits for its periodic verification.
func (r *AgentReconciler) notifyAIGatewayRoutesLost(ctx context.Context) {
	r.notifyAIGatewayAgents(ctx, "a check for lost Copilot routes", r.copilotRoutesLost)
}

// copilotRoutesLost reports whether a listed catalog lacks one of the Agent's
// recorded Copilot routes. Routes of a deleted Agent are on their way out.
func (r *AgentReconciler) copilotRoutesLost(agent *cetirunv1alpha1.Agent) bool {
	status := agent.Status.AIGateway
	if status == nil || status.Copilot == nil || !agent.DeletionTimestamp.IsZero() {
		return false
	}
	for _, id := range status.Copilot.Registered {
		if present, known := r.aiGatewayCatalog.has(id); known && !present {
			return true
		}
	}
	return false
}

// notifyAIGatewayAgents queues the Agents pick selects.
func (r *AgentReconciler) notifyAIGatewayAgents(ctx context.Context, after string, pick func(*cetirunv1alpha1.Agent) bool) {
	if r.aiGatewayEvents == nil {
		return
	}
	agents := &cetirunv1alpha1.AgentList{}
	if err := r.List(ctx, agents); err != nil {
		log.FromContext(ctx).Error(err, "listing Agents failed", "after", after)
		return
	}
	for i := range agents.Items {
		agent := &agents.Items[i]
		if !pick(agent) {
			continue
		}
		select {
		case r.aiGatewayEvents <- event.GenericEvent{Object: client.Object(agent)}:
		default:
			// A full queue drops the nudge; the periodic verification
			// reconciles the Agent anyway.
		}
	}
}
