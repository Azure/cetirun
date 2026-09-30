// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

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
	// AIGatewayCatalogConfigMap is the operator-namespace ConfigMap listing
	// the shared models, for the Portal. It holds names and limits only.
	AIGatewayCatalogConfigMap = "cetirun-ai-gateway-catalog"
	// AIGatewayCatalogKey is its data key.
	AIGatewayCatalogKey = "catalog.json"
	// aiGatewayCatalogMaxAge is how old a catalog a reconcile accepts before
	// listing the Gateway's models again.
	aiGatewayCatalogMaxAge = time.Minute
	// aiGatewayCatalogInterval is how often the catalog is refreshed and
	// published in the background.
	aiGatewayCatalogInterval = 2 * time.Minute
)

// aiGatewayCatalogModel is one shared model: a model name with the limits
// the Gateway knows for it.
type aiGatewayCatalogModel struct {
	Name            string `json:"name"`
	ContextWindow   int64  `json:"contextWindow,omitempty"`
	MaxOutputTokens int64  `json:"maxOutputTokens,omitempty"`
}

// AIGatewayCatalog is what the Portal reads from the catalog ConfigMap.
type AIGatewayCatalog struct {
	SharedModels []aiGatewayCatalogModel `json:"sharedModels"`
}

// aiGatewayCatalog is the operator's view of the Gateway's model
// deployments. The zero value knows nothing yet.
type aiGatewayCatalog struct {
	refreshMu sync.Mutex
	mu        sync.RWMutex
	known     bool
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
func (c *aiGatewayCatalog) update(deployments []aigateway.Deployment, now time.Time) bool {
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
	changed := !c.known || !slices.Equal(c.sharedSet, shared)
	c.known, c.fetchedAt, c.sharedSet, c.ids, c.groups = true, now, shared, ids, groups
	return changed
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
	return slices.Clone(c.sharedSet), c.known
}

func (c *aiGatewayCatalog) sharedNames() ([]string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	names := make([]string, 0, len(c.sharedSet))
	for _, model := range c.sharedSet {
		names = append(names, model.Name)
	}
	return names, c.known
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
		return false
	}
	return catalog.update(deployments, time.Now())
}

func (r *AgentReconciler) refreshAIGatewayCatalog(ctx context.Context, client aiGatewayKeyClient) {
	if r.refreshAIGatewayCatalogWithin(ctx, client, aiGatewayCatalogMaxAge) {
		r.notifyAIGatewayCatalogChanged(ctx)
	}
}

// AIGatewayCatalogPublisher keeps the catalog fresh, publishes the shared
// models for the Portal and reconciles Agents when the shared models change.
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
	shared, known := r.aiGatewayCatalog.shared()
	if !known {
		return
	}
	if err := r.writeAIGatewayCatalog(ctx, shared); err != nil {
		logger.Error(err, "publishing the AI Gateway catalog failed")
	}
}

func (r *AgentReconciler) writeAIGatewayCatalog(ctx context.Context, shared []aiGatewayCatalogModel) error {
	namespace := r.OperatorNamespace
	if namespace == "" {
		namespace = "cetirun-system"
	}
	if shared == nil {
		shared = []aiGatewayCatalogModel{}
	}
	raw, err := json.Marshal(AIGatewayCatalog{SharedModels: shared})
	if err != nil {
		return err
	}
	key := types.NamespacedName{Namespace: namespace, Name: AIGatewayCatalogConfigMap}
	configMap := &corev1.ConfigMap{}
	err = r.Get(ctx, key, configMap)
	if apierrors.IsNotFound(err) {
		configMap = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Labels: map[string]string{
				"app.kubernetes.io/managed-by": "cetirun-operator", "app.kubernetes.io/component": "ai-gateway-catalog",
			}},
			Data: map[string]string{AIGatewayCatalogKey: string(raw)},
		}
		return r.Create(ctx, configMap)
	}
	if err != nil {
		return fmt.Errorf("get catalog ConfigMap: %w", err)
	}
	if configMap.Data[AIGatewayCatalogKey] == string(raw) {
		return nil
	}
	if configMap.Data == nil {
		configMap.Data = map[string]string{}
	}
	configMap.Data[AIGatewayCatalogKey] = string(raw)
	return r.Update(ctx, configMap)
}

// notifyAIGatewayCatalogChanged queues every Agent routed through the
// Gateway, so a new or removed shared model reaches Agent configurations
// without waiting for the periodic verification.
func (r *AgentReconciler) notifyAIGatewayCatalogChanged(ctx context.Context) {
	if r.aiGatewayEvents == nil {
		return
	}
	agents := &cetirunv1alpha1.AgentList{}
	if err := r.List(ctx, agents); err != nil {
		log.FromContext(ctx).Error(err, "listing Agents after an AI Gateway catalog change failed")
		return
	}
	for i := range agents.Items {
		agent := &agents.Items[i]
		if agent.Status.AIGateway == nil || !agent.Status.AIGateway.Routed || !agent.Spec.SharedModelsEnabled() {
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
