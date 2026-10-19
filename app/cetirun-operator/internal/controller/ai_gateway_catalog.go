// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
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
	// groups lists the deployment IDs of each access group.
	groups map[string][]string
	// modelGroups lists the trusted model groups for each shared model
	// identifier. Policy resolution uses these groups rather than a display
	// name or the broad legacy cetirun-shared group.
	modelGroups map[string][]string
	// fingerprint identifies which shared model names the managed access
	// groups bind. It is derived from modelGroups only, so a configuration
	// edit that keeps a model's name and binding leaves it unchanged.
	fingerprint string
	// allowsUnpriced is the Gateway's setting from the last listing. The
	// zero value is the Gateway's default, which refuses requests for
	// models without a price.
	allowsUnpriced bool
	// rollout holds back listings that change the view while the Gateway
	// is rolling out; see ai_gateway_rollout.go.
	rollout aiGatewayRollout
}

// changedBy reports whether a listing would change a view taken from an
// earlier listing. Without such a view it reports false: there is nothing
// to keep.
func (c *aiGatewayCatalog) changedBy(deployments []aigateway.Deployment, blockUnpriced bool) bool {
	c.mu.RLock()
	scratch := aiGatewayCatalog{known: c.known, sharedSet: c.sharedSet, fingerprint: c.fingerprint}
	c.mu.RUnlock()
	if !scratch.known {
		return false
	}
	changed, _, _ := scratch.update(deployments, blockUnpriced, time.Time{})
	return changed
}

func (c *aiGatewayCatalog) age(now time.Time) time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.known {
		return time.Duration(1<<63 - 1)
	}
	return now.Sub(c.fetchedAt)
}

// update replaces the view and reports whether the shared models changed. It
// also returns the shared names it leaves out: those Agents cannot use, and,
// while the Gateway blocks requests for unpriced models, those without a
// price. Like LiteLLM, it counts a name as priced when any deployment behind
// it is.
func (c *aiGatewayCatalog) update(deployments []aigateway.Deployment, blockUnpriced bool, now time.Time) (bool, []string, []string) {
	var skipped, unpriced []string
	ids := map[string]bool{}
	groups := map[string][]string{}
	priced := map[string]bool{}
	for _, deployment := range deployments {
		if deployment.Priced {
			priced[deployment.ModelName] = true
		}
	}
	trusted := trustedManagedModelGroups(deployments)
	byName := map[string]aiGatewayCatalogModel{}
	modelGroups := map[string][]string{}
	for _, deployment := range deployments {
		ids[deployment.ID] = true
		for _, group := range deployment.AccessGroups {
			groups[group] = append(groups[group], deployment.ID)
		}
		legacyShared := slices.Contains(deployment.AccessGroups, aiGatewaySharedAccessGroup)
		managedGroups := slices.DeleteFunc(managedModelGroups(deployment.AccessGroups), func(group string) bool {
			return !trusted[group]
		})
		policyBoundShared := len(managedGroups) > 0
		// Keep publishing legacy shared models while old catalog consumers are
		// present, but never resolve a Policy policy through their broad
		// group. Only a trusted model group below is authorization evidence.
		if !legacyShared && !policyBoundShared {
			continue
		}
		if !aiGatewaySharedModelUsable(deployment.ModelName) {
			skipped = append(skipped, deployment.ModelName)
			continue
		}
		if blockUnpriced && !priced[deployment.ModelName] {
			if !slices.Contains(unpriced, deployment.ModelName) {
				unpriced = append(unpriced, deployment.ModelName)
			}
			continue
		}
		model := byName[deployment.ModelName]
		model.Name = deployment.ModelName
		// Deployments behind one name are load-balanced; configure the
		// Agent with the smallest limits any of them has.
		model.ContextWindow = smallerPositive(model.ContextWindow, deployment.MaxInputTokens)
		model.MaxOutputTokens = smallerPositive(model.MaxOutputTokens, deployment.MaxOutputTokens)
		byName[deployment.ModelName] = model
		if policyBoundShared {
			modelGroups[deployment.ModelName] = append(modelGroups[deployment.ModelName], managedGroups...)
		}
	}
	shared := make([]aiGatewayCatalogModel, 0, len(byName))
	for _, model := range byName {
		shared = append(shared, model)
	}
	sort.Slice(shared, func(i, j int) bool { return shared[i].Name < shared[j].Name })
	slices.Sort(unpriced)
	for name, groups := range modelGroups {
		slices.Sort(groups)
		modelGroups[name] = slices.Compact(groups)
	}
	fingerprint := aiGatewayCatalogFingerprint(modelGroups)
	c.mu.Lock()
	defer c.mu.Unlock()
	changed := !(c.known || c.seeded) || !slices.Equal(c.sharedSet, shared) || c.fingerprint != fingerprint
	c.known, c.seeded, c.fetchedAt, c.sharedSet, c.ids, c.groups, c.modelGroups, c.fingerprint = true, false, now, shared, ids, groups, modelGroups, fingerprint
	c.allowsUnpriced = !blockUnpriced
	return changed, skipped, unpriced
}

// aiGatewayModelBindingLength is the number of hexadecimal characters the
// Gateway chart keeps of each SHA-256 in a model binding.
const aiGatewayModelBindingLength = 16

// aiGatewayModelBinding is the binding the Gateway chart derives from a model
// name: the first 16 hexadecimal characters of the SHA-256 of the name, as
// Helm's sha256sum prints them. The model group is cetirun-model-<binding>.
func aiGatewayModelBinding(modelName string) string {
	sum := sha256.Sum256([]byte(modelName))
	return hex.EncodeToString(sum[:])[:aiGatewayModelBindingLength]
}

// trustedManagedModelGroups returns the chart-derived model groups whose
// every member agrees with the chart's binding: a group cetirun-model-<g> is
// trusted only when each deployment carrying it is configured statically
// (not stored in the Gateway database, with a known source), has the unique
// deployment ID cetirun-shared-<g>-<16 lowercase hexadecimal characters>, and
// serves the one model name whose SHA-256 begins with g. Several members are
// the load-balanced entries of that name. Every deployment is considered,
// database models included, so a console or database copy of a reserved
// group invalidates the whole group rather than leaving its configured
// members trusted.
func trustedManagedModelGroups(deployments []aigateway.Deployment) map[string]bool {
	idMembers := map[string]int{}
	members := map[string][]aigateway.Deployment{}
	for _, deployment := range deployments {
		if deployment.ID != "" {
			idMembers[deployment.ID]++
		}
		for _, group := range managedModelGroups(deployment.AccessGroups) {
			members[group] = append(members[group], deployment)
		}
	}
	trusted := map[string]bool{}
	for group, carriers := range members {
		binding := strings.TrimPrefix(group, aiGatewayModelAccessGroupPrefix)
		name := carriers[0].ModelName
		if !lowercaseHex(binding, aiGatewayModelBindingLength) || aiGatewayModelBinding(name) != binding {
			continue
		}
		if !slices.ContainsFunc(carriers, func(deployment aigateway.Deployment) bool {
			return !deployment.DatabaseSourceKnown || deployment.FromDatabase || deployment.ModelName != name ||
				idMembers[deployment.ID] != 1 || !aiGatewayManagedDeploymentID(deployment.ID, binding)
		}) {
			trusted[group] = true
		}
	}
	return trusted
}

// aiGatewayManagedDeploymentID reports whether id is a deployment ID the
// Gateway chart renders for the binding: cetirun-shared-<binding>-<hash of
// the complete model entry>. The operator cannot recompute the entry hash; it
// only keeps distinct entries of one name apart.
func aiGatewayManagedDeploymentID(id, binding string) bool {
	configuration, ok := strings.CutPrefix(id, aiGatewaySharedDeploymentIDPrefix+binding+"-")
	return ok && lowercaseHex(configuration, aiGatewayModelBindingLength)
}

func lowercaseHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func managedModelGroups(groups []string) []string {
	managed := make([]string, 0, len(groups))
	for _, group := range groups {
		if strings.HasPrefix(group, aiGatewayModelAccessGroupPrefix) && len(group) > len(aiGatewayModelAccessGroupPrefix) {
			managed = append(managed, group)
		}
	}
	slices.Sort(managed)
	return slices.Compact(managed)
}

// aiGatewayCatalogFingerprint identifies the trusted model groups of each
// shared model name, and nothing else: no deployment IDs and no
// configuration hashes. It is part of the Gateway policy revision, so only a
// change in which names are bound, or in a name's groups, re-projects
// policy-bound Agents. Editing a configured model's endpoint, provider
// parameters, price or other metadata changes its deployment ID but neither
// its name nor its group, so it leaves the fingerprint unchanged. (A model
// that loses its price while the Gateway blocks unpriced models leaves the
// shared models, which removes its name.)
//
// That is safe because an AgentPolicy authorizes exact catalog model names
// (AgentPolicySharedModelsAccess.Allowed), not deployments: any deployment
// the chart configures under an allowed name is within the grant. A model
// group is derived from the name alone, so it is stable for as long as the
// name exists and can never stand for another name, and a group counts only
// while every deployment carrying it is static chart configuration of that
// name (trustedManagedModelGroups). A console or database deployment that
// copies a group still invalidates it, which removes the name from this
// fingerprint and from policy resolution.
func aiGatewayCatalogFingerprint(modelGroups map[string][]string) string {
	// encoding/json writes map keys in sorted order, and update sorts each
	// name's groups, so listing order cannot change the fingerprint.
	sum, _ := json.Marshal(struct {
		Version     string              `json:"version"`
		ModelGroups map[string][]string `json:"modelGroups"`
	}{Version: "model-name-bindings-v1", ModelGroups: modelGroups})
	digest := sha256.Sum256(sum)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// blocksUnpriced reports the Gateway's setting from the last listing, or its
// default before the first one.
func (c *aiGatewayCatalog) blocksUnpriced() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return !c.allowsUnpriced
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
// use and names over its length limit, and the Gateway chart reserves the
// cetirun- and copilot/ prefixes in any letter case, copilot/ for the Agents'
// own Copilot routes. The console does not check them.
func aiGatewaySharedModelUsable(name string) bool {
	if name == "" || strings.ContainsAny(name, " \t\r\n") || utf8.RuneCountInString(name) > aiGatewayModelNameLimit {
		return false
	}
	lower := strings.ToLower(name)
	return !strings.HasPrefix(name, "openai/") && !strings.HasPrefix(lower, "cetirun-") &&
		!strings.HasPrefix(lower, aiGatewayCopilotModelPrefix)
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

// legacySharedGroups includes every trusted configured shared model; a legacy
// Agent has no AgentPolicy allowlist. Call only after a live catalog refresh.
func (c *aiGatewayCatalog) legacySharedGroups() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var groups []string
	for _, model := range c.sharedSet {
		groups = append(groups, c.modelGroups[model.Name]...)
	}
	slices.Sort(groups)
	return slices.Compact(groups)
}

// managedFingerprint is empty until a live Gateway listing establishes the
// concrete access groups. Policy uses it in its expected Gateway
// projection revision, and a key reconcile records it as the Agent's
// pricingRevision; callers must not treat a seeded display catalog as
// authorization evidence.
func (c *aiGatewayCatalog) managedFingerprint() (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.fingerprint, c.known
}

// resolvedGroups returns the trusted model groups for the exact policy model
// identifiers. It is false until a live catalog has been listed; a seeded
// display-only catalog is intentionally insufficient evidence for a Policy
// grant.
func (c *aiGatewayCatalog) resolvedGroups(models []string) ([]string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.known {
		return nil, false
	}
	groups := make([]string, 0, len(models))
	for _, model := range models {
		entries := c.modelGroups[model]
		if len(entries) == 0 {
			return nil, false
		}
		groups = append(groups, entries...)
	}
	slices.Sort(groups)
	return slices.Compact(groups), true
}

// sharedForGroups returns the published model entries that are reachable by
// the supplied collision-safe groups. It is used when rendering a policy-bound
// workload so an allowed display name cannot accidentally reintroduce another
// policy's model into the harness configuration.
func (c *aiGatewayCatalog) sharedForGroups(groups []string) ([]aiGatewayCatalogModel, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.known {
		return nil, false
	}
	allowed := make(map[string]bool, len(groups))
	for _, group := range groups {
		allowed[group] = true
	}
	shared := make([]aiGatewayCatalogModel, 0, len(c.sharedSet))
	for _, model := range c.sharedSet {
		if slices.ContainsFunc(c.modelGroups[model.Name], func(group string) bool { return allowed[group] }) {
			shared = append(shared, model)
		}
	}
	return shared, true
}

// has reports whether a deployment ID exists, and whether the catalog knows.
func (c *aiGatewayCatalog) has(id string) (bool, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ids[id], c.known
}

// groupDeployments returns the deployment IDs of an access group, and whether
// the catalog knows them.
func (c *aiGatewayCatalog) groupDeployments(group string) ([]string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return slices.Clone(c.groups[group]), c.known
}

// noteAdded and noteDeleted keep a known view in step with the operator's
// own changes, so a view listed before a registration does not report the
// new route as lost, nor a removed route as still there.
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
// older than maxAge. A failed listing keeps the previous view; a failure to
// read whether the Gateway blocks unpriced models keeps the last setting.
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
	blockUnpriced, err := client.BlockUnpricedModels(ctx)
	if err != nil {
		blockUnpriced = catalog.blocksUnpriced()
		log.FromContext(ctx).Info("reading whether the AI Gateway blocks unpriced models failed; keeping the last setting",
			"blocks", blockUnpriced, "error", err.Error())
	}
	if r.holdAIGatewayCatalog(ctx, deployments, blockUnpriced, time.Now()) {
		return false
	}
	changed, skipped, unpriced := catalog.update(deployments, blockUnpriced, time.Now())
	if len(skipped) > 0 {
		log.FromContext(ctx).Info("shared models with names Agents cannot use are left out", "models", skipped)
	}
	if len(unpriced) > 0 {
		log.FromContext(ctx).Info("shared models without a price are left out while the AI Gateway blocks unpriced models", "models", unpriced)
	}
	return changed
}

// refreshAIGatewayCatalogForGrant obtains a fresh, live catalog for a
// Policy grant. Unlike the background refresh, it never falls back to a
// cached or seeded catalog: doing so could authorize a group which has been
// deleted, or which a console or database copy has since invalidated. Every
// policy model must be resolved against the exact current Gateway response.
func (r *AgentReconciler) refreshAIGatewayCatalogForGrant(ctx context.Context, client aiGatewayKeyClient) error {
	catalog := &r.aiGatewayCatalog
	catalog.refreshMu.Lock()
	defer catalog.refreshMu.Unlock()
	deployments, err := client.ListModels(ctx)
	if err != nil {
		return fmt.Errorf("list AI Gateway models for Policy grant: %w", err)
	}
	blockUnpriced, err := client.BlockUnpricedModels(ctx)
	if err != nil {
		return fmt.Errorf("read AI Gateway pricing policy for Policy grant: %w", err)
	}
	if r.holdAIGatewayCatalog(ctx, deployments, blockUnpriced, time.Now()) {
		// While the Gateway rolls out, the grant uses the view of the last
		// listing adopted, which was live; the hold is bounded.
		return nil
	}
	changed, skipped, unpriced := catalog.update(deployments, blockUnpriced, time.Now())
	if len(skipped) > 0 {
		log.FromContext(ctx).Info("shared models with names Agents cannot use are left out", "models", skipped)
	}
	if len(unpriced) > 0 {
		log.FromContext(ctx).Info("shared models without a price are left out while the AI Gateway blocks unpriced models", "models", unpriced)
	}
	if changed {
		r.notifyAIGatewayCatalogChanged(ctx)
	}
	return nil
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

// AIGatewayCatalogPublisher keeps the catalog fresh, publishes the model
// sources for the Portal and reconciles Agents when the shared models change
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
	maxAge := interval / 2
	for {
		p.Reconciler.publishAIGatewayCatalog(ctx, maxAge)
		maxAge = interval / 2
		var recheck <-chan time.Time
		if p.Reconciler.aiGatewayCatalog.rollout.waiting() {
			recheck = time.After(aiGatewayRolloutRecheck)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-recheck:
			// A change held during a Gateway rollout is listed again soon,
			// whatever the age of the catalog.
			maxAge = 0
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
	catalog := cetirunv1alpha1.AIGatewayCatalog{SharedModels: shared, GitHubCopilot: r.aiGatewayCopilotOffered()}
	key := types.NamespacedName{Namespace: namespace, Name: cetirunv1alpha1.AIGatewayCatalogConfigMap}
	configMap := &corev1.ConfigMap{}
	err := r.Get(ctx, key, configMap)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("get catalog ConfigMap: %w", err)
	}
	published := err == nil
	var previous cetirunv1alpha1.AIGatewayCatalog
	if published {
		_ = json.Unmarshal([]byte(configMap.Data[cetirunv1alpha1.AIGatewayCatalogKey]), &previous)
	}
	catalog.Routing = r.aiGatewayRouting(ctx, previous.Routing)
	raw, err := json.Marshal(catalog)
	if err != nil {
		return err
	}
	if !published {
		configMap = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Labels: map[string]string{
				"app.kubernetes.io/managed-by": "cetirun-operator", "app.kubernetes.io/component": "ai-gateway-catalog",
			}},
			Data: map[string]string{cetirunv1alpha1.AIGatewayCatalogKey: string(raw)},
		}
		return r.Create(ctx, configMap)
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

// chartInspector names and reads the values a chart version declares without
// preparing a release. The Helm client implements it; a read ends when ctx is
// done or after the client's own time limit.
type chartInspector interface {
	ChartIdentity(version string) string
	InspectChart(ctx context.Context, version string) (*helm.ChartValues, error)
}

// aiGatewayChartRouting caches the agent types the default agent chart routes
// through the Gateway, with the chart they were read from.
type aiGatewayChartRouting struct {
	mu       sync.Mutex
	known    bool
	identity string
	types    []cetirunv1alpha1.AgentType
}

// aiGatewayRouting reports what the default agent chart routes through the
// Gateway, decided as for each Agent's own chart (aiGatewayChartSupports),
// with the chart's identity. The chart is read once per identity, within the
// publisher's context: a registry that stalls, or the publisher stopping,
// ends the read. While the chart cannot be read, the routing published before
// is kept only when it is for the same chart; otherwise the agent types are
// unknown (null), so that no Agent is created on the Gateway against a chart
// that may not route it. Nil when the Helm client cannot read charts.
func (r *AgentReconciler) aiGatewayRouting(ctx context.Context, previous *cetirunv1alpha1.AIGatewayRouting) *cetirunv1alpha1.AIGatewayRouting {
	inspector, ok := r.HelmClient.(chartInspector)
	if !ok {
		return nil
	}
	identity := inspector.ChartIdentity(r.ChartVersion)
	routing := &r.aiGatewayChartRouting
	routing.mu.Lock()
	defer routing.mu.Unlock()
	if routing.known && routing.identity == identity {
		return &cetirunv1alpha1.AIGatewayRouting{Chart: identity, AgentTypes: slices.Clone(routing.types)}
	}
	values, err := inspector.InspectChart(ctx, r.ChartVersion)
	if err != nil {
		if previous != nil && previous.Chart == identity && previous.AgentTypes != nil {
			log.FromContext(ctx).Error(err, "reading the default agent chart failed; the catalog keeps the agent types it published for it",
				"chart", identity)
			return &cetirunv1alpha1.AIGatewayRouting{Chart: identity, AgentTypes: slices.Clone(previous.AgentTypes)}
		}
		log.FromContext(ctx).Error(err, "reading the default agent chart failed; the catalog lists its agent types as unknown",
			"chart", identity)
		return &cetirunv1alpha1.AIGatewayRouting{Chart: identity}
	}
	routed := []cetirunv1alpha1.AgentType{}
	for _, agentType := range []cetirunv1alpha1.AgentType{
		cetirunv1alpha1.AgentTypeOpenClaw, cetirunv1alpha1.AgentTypeHermes, cetirunv1alpha1.AgentTypeCustom,
	} {
		if aiGatewayHarnessSupported(agentType) && aiGatewayChartSupports(values, agentType) {
			routed = append(routed, agentType)
		}
	}
	routing.known, routing.identity, routing.types = true, identity, routed
	return &cetirunv1alpha1.AIGatewayRouting{Chart: identity, AgentTypes: slices.Clone(routed)}
}

// notifyAIGatewayCatalogChanged queues every Agent routed through the
// Gateway, so a new or removed shared model reaches Agent configurations
// without waiting for the periodic verification.
func (r *AgentReconciler) notifyAIGatewayCatalogChanged(ctx context.Context) {
	r.notifyAIGatewayAgents(ctx, "an AI Gateway catalog change", func(agent *cetirunv1alpha1.Agent) bool {
		return agent.Status.AIGateway != nil && agent.Status.AIGateway.Routed && agent.Spec.SharedModelsEnabled()
	})
}

// aiGatewayBindingsChanged reports whether the live catalog binds shared
// model names to other groups than when the Agent's key was last granted:
// its recorded pricingRevision is an earlier catalog fingerprint. That happens
// when a shared model name is added or removed, or once after an upgrade
// that changes how bindings are derived. The key then needs its groups now,
// not at the next verification. Configuration edits that keep the names, and
// therefore the fingerprint, do not make it due. A queued Agent with this
// change takes the full reconcile, which updates the key in place; its
// configuration hash, and so its workload, changes only when the shared
// models listed for it change.
func (r *AgentReconciler) aiGatewayBindingsChanged(instance *cetirunv1alpha1.Agent) bool {
	status := instance.Status.AIGateway
	if status == nil || status.KeyID == "" || !instance.Spec.SharedModelsEnabled() {
		return false
	}
	fingerprint, known := r.aiGatewayCatalog.managedFingerprint()
	return known && status.PricingRevision != fingerprint
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
