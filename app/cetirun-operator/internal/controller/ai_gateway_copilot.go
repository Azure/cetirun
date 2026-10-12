// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
)

const (
	// aiGatewayCopilotGroupPrefix prefixes the access group of one Agent's
	// Copilot models. The Agent UID follows, so no other key is granted it
	// and a same-name replacement starts with an empty group.
	aiGatewayCopilotGroupPrefix = "cetirun-copilot-"
	// aiGatewayCopilotModelPrefix prefixes the names Copilot models get in the
	// Gateway, so they never share a name with an administrator's shared model.
	aiGatewayCopilotModelPrefix = cetirunv1alpha1.AIGatewayCopilotModelPrefix
	// aiGatewayProxySecretName holds the master key of the Agent's Copilot
	// proxy. The Gateway's routes to the proxy carry it.
	aiGatewayProxySecretName  = "ai-gateway-copilot-proxy"
	aiGatewayProxySecretField = "master-key"
	aiGatewayProxyPort        = 4000
	// aiGatewayMaxCopilotModels bounds how many models one account registers.
	aiGatewayMaxCopilotModels = 128
	aiGatewayPriceCacheTTL    = time.Hour
	// copilotSignInGrace is how long the operator waits for a sign-in whose
	// token is stored to record its models and complete, as long as the
	// Portal's lease on completing it. The Portal does both right after the
	// token.
	copilotSignInGrace = 2 * time.Minute
)

// copilotModelID is the form of Copilot model IDs the operator registers.
// It keeps IDs usable in Gateway model names and deployment IDs.
var copilotModelID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// copilotModelNameSuffix marks a Copilot model's name in the Agent's
// configuration.
const copilotModelNameSuffix = " (GitHub Copilot)"

// truncateRunes shortens s to at most n characters.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func copilotAccessGroup(instance *cetirunv1alpha1.Agent) string {
	return aiGatewayCopilotGroupPrefix + string(instance.UID)
}

// copilotProxyServiceName mirrors the agent chart's name for the proxy
// Service: the release name with a suffix, cut to a DNS label.
func copilotProxyServiceName(instance *cetirunv1alpha1.Agent) string {
	name := helmReleaseName(instance.Name) + "-copilot-proxy"
	if len(name) > 63 {
		name = name[:63]
	}
	return strings.TrimRight(name, "-")
}

func copilotProxyURL(instance *cetirunv1alpha1.Agent) string {
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", copilotProxyServiceName(instance), instanceNamespace(instance.Name), aiGatewayProxyPort)
}

// parseCopilotModels reads the Portal's model list for the owner's Copilot
// account. Entries with an unusable ID are dropped; the rest are sorted and
// deduplicated so the result does not depend on the account's ordering.
func parseCopilotModels(annotation string) ([]cetirunv1alpha1.CopilotModel, error) {
	if strings.TrimSpace(annotation) == "" {
		return nil, nil
	}
	var models []cetirunv1alpha1.CopilotModel
	if err := json.Unmarshal([]byte(annotation), &models); err != nil {
		return nil, fmt.Errorf("the Copilot model list is not valid JSON")
	}
	seen := map[string]bool{}
	var out []cetirunv1alpha1.CopilotModel
	for _, model := range models {
		if !copilotModelID.MatchString(model.ID) || seen[model.ID] {
			continue
		}
		seen[model.ID] = true
		if model.ContextWindow < 0 {
			model.ContextWindow = 0
		}
		if model.MaxOutputTokens < 0 {
			model.MaxOutputTokens = 0
		}
		out = append(out, model)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > aiGatewayMaxCopilotModels {
		out = out[:aiGatewayMaxCopilotModels]
	}
	return out, nil
}

// copilotBaseModel picks the price-table entry for a Copilot model: its own
// name, the name with dots as dashes (Copilot writes claude-sonnet-4.5, the
// table claude-sonnet-4-5), then the same names under a provider prefix,
// github_copilot first. The Gateway refuses a route without a price, and a
// zero price would leave the key's budget unenforced, so a model without an
// entry is not registered.
func copilotBaseModel(id string, priced map[string]bool) (string, bool) {
	dashed := strings.ReplaceAll(id, ".", "-")
	for _, candidate := range []string{id, dashed, "github_copilot/" + id, "github_copilot/" + dashed} {
		if priced[candidate] {
			return candidate, true
		}
	}
	// Then the model vendor's own entry. Resellers' entries are not list
	// prices, so a model priced only by them is left unpriced.
	for _, vendor := range copilotModelVendors {
		for _, name := range []string{vendor + "/" + id, vendor + "/" + dashed} {
			if priced[name] {
				return name, true
			}
		}
	}
	return "", false
}

// copilotModelVendors are the price-table providers that are the vendors of
// the models GitHub Copilot serves, in the order they are tried.
var copilotModelVendors = []string{"openai", "anthropic", "gemini", "xai", "mistral", "deepseek"}

// aiGatewayPriceCache holds the Gateway's priced model names for an hour.
type aiGatewayPriceCache struct {
	mu     sync.Mutex
	at     time.Time
	priced map[string]bool
}

func (r *AgentReconciler) pricedModels(ctx context.Context, client aiGatewayKeyClient) (map[string]bool, error) {
	r.aiGatewayPrices.mu.Lock()
	defer r.aiGatewayPrices.mu.Unlock()
	if r.aiGatewayPrices.priced != nil && time.Since(r.aiGatewayPrices.at) < aiGatewayPriceCacheTTL {
		return r.aiGatewayPrices.priced, nil
	}
	priced, err := client.PricedModels(ctx)
	if err != nil {
		if r.aiGatewayPrices.priced != nil {
			return r.aiGatewayPrices.priced, nil
		}
		return nil, err
	}
	r.aiGatewayPrices.priced, r.aiGatewayPrices.at = priced, time.Now()
	return priced, nil
}

// copilotRegistration is one Copilot model route the Agent should have.
type copilotRegistration struct {
	request aigateway.ModelRequest
	model   cetirunv1alpha1.CopilotModel
}

// copilotRegistrationID names a route by the Agent UID, the model and a
// revision of everything the route carries, so a changed proxy address, key
// or price entry replaces the route instead of leaving a stale one.
func copilotRegistrationID(instance *cetirunv1alpha1.Agent, model, revision string) string {
	return fmt.Sprintf("%s%s-%s-%s", aiGatewayCopilotGroupPrefix, instance.UID, model, revision)
}

func copilotRevision(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])[:8]
}

// reconcileAIGatewayCopilot keeps the Agent's Copilot proxy credentials and
// model routes in line with the owner's sign-in. The proxy reads the token
// Secret, which the Portal writes; the Portal also records the account's
// models on the Agent. Each model becomes a Gateway route to this Agent's
// proxy in the Agent's own access group. Problems are reported on the
// Copilot status; they never fail the key reconcile.
func (r *AgentReconciler) reconcileAIGatewayCopilot(ctx context.Context, client aiGatewayKeyClient, instance *cetirunv1alpha1.Agent, active bool) {
	logger := log.FromContext(ctx)
	status := instance.Status.AIGateway
	if !active {
		if status.Copilot == nil {
			return
		}
		if err := r.deleteCopilotRegistrations(ctx, client, instance); err != nil {
			status.Copilot.Phase = cetirunv1alpha1.AIGatewayCopilotPhaseDegraded
			status.Copilot.Reason = "RouteRemovalFailed"
			logger.Error(err, "removing Copilot model routes failed", "instance", instance.Name)
			return
		}
		for _, name := range []string{cetirunv1alpha1.CopilotTokenSecretName, aiGatewayProxySecretName} {
			if err := r.deleteAgentSecret(ctx, instance, name); err != nil {
				logger.Error(err, "removing Copilot Secret failed", "instance", instance.Name, "secret", name)
				return
			}
		}
		status.Copilot = nil
		return
	}
	if status.Copilot == nil {
		status.Copilot = &cetirunv1alpha1.AIGatewayCopilotStatus{}
	}
	copilot := status.Copilot
	copilot.AccessGroup = copilotAccessGroup(instance)
	degraded := func(reason string, err error) {
		copilot.Phase = cetirunv1alpha1.AIGatewayCopilotPhaseDegraded
		copilot.Reason = reason
		logger.Error(err, "GitHub Copilot proxy reconcile failed", "instance", instance.Name, "reason", reason)
	}

	proxyKey, err := r.ensureAgentOwnedValue(ctx, instance, aiGatewayProxySecretName, aiGatewayProxySecretField, false, nil)
	if err != nil {
		degraded("ProxySecretFailed", err)
		return
	}
	// A lost or malformed key Secret gets a new key, which the routes below
	// carry at once; the ID restarts the proxy with it.
	copilot.ProxyKeyID = aigateway.Token(proxyKey)[:aiGatewayKeyIDLength]
	signIn, err := r.ensureCopilotTokenSecret(ctx, instance)
	if err != nil {
		degraded("TokenSecretFailed", err)
		return
	}
	signedIn := signIn.signedIn
	mismatched := false
	var wanted []cetirunv1alpha1.CopilotModel
	if signedIn {
		if recorded := instance.Annotations[cetirunv1alpha1.AnnotationCopilotSignInGeneration]; recorded != signIn.generation || (signIn.generation != "" && !signIn.completed) {
			// The models on the Agent were recorded for another sign-in than
			// the token the proxy holds, or the Portal has not completed that
			// sign-in. A completing sign-in records its models just after its
			// token and then completes: wait for that, leaving the routes as
			// they are. Past that, the models do not belong to the stored
			// token, or its sign-in was cancelled, replaced or ended before it
			// completed, and they are not used. A token stored before
			// generations has none, nor have its models, and is used as before.
			if !signIn.storedAt.IsZero() && time.Since(signIn.storedAt) < copilotSignInGrace {
				copilot.Phase = cetirunv1alpha1.AIGatewayCopilotPhaseStarting
				copilot.Reason = "SignInCompleting"
				return
			}
			mismatched = true
		} else {
			wanted, err = parseCopilotModels(instance.Annotations[cetirunv1alpha1.AnnotationCopilotModels])
			if err != nil {
				degraded("ModelListInvalid", err)
				return
			}
		}
	}

	desired := map[string]copilotRegistration{}
	var unpriced []string
	if len(wanted) > 0 {
		priced, err := r.pricedModels(ctx, client)
		if err != nil {
			degraded("PriceTableUnavailable", err)
			return
		}
		proxyURL := copilotProxyURL(instance)
		for _, model := range wanted {
			base, ok := copilotBaseModel(model.ID, priced)
			if !ok {
				unpriced = append(unpriced, model.ID)
				continue
			}
			id := copilotRegistrationID(instance, model.ID, copilotRevision(proxyURL, proxyKey, base))
			desired[id] = copilotRegistration{model: model, request: aigateway.ModelRequest{
				ID: id, ModelName: aiGatewayCopilotModelPrefix + model.ID, Model: "litellm_proxy/" + model.ID,
				APIBase: proxyURL, APIKey: proxyKey, AccessGroups: []string{copilot.AccessGroup}, BaseModel: base,
			}}
		}
	}

	// Remove routes no longer wanted, then add the missing ones. A route the
	// Gateway lost is re-added when the catalog shows it gone. New routes wait
	// for the proxy to be ready: it starts serving only once it has read the
	// sign-in, and a route to a proxy that is not up fails the Agent's calls.
	proxyReady := len(desired) == 0 || r.copilotProxyReady(ctx, instance)
	var registered []string
	var failed error
	pending := false
	// The routes to remove are those of the Agent's group the status records
	// and, once the catalog has listed the Gateway, those it lists there: a
	// route registered just before the operator stopped, before the status
	// recorded it, is known only to the Gateway, and once the proxy key or
	// the price table changes its revision would keep serving the Agent's
	// model names with what it was registered with.
	stale := map[string]bool{}
	for _, id := range copilot.Registered {
		stale[id] = true
	}
	if listed, known := r.aiGatewayCatalog.groupDeployments(copilot.AccessGroup); known {
		for _, id := range listed {
			stale[id] = true
		}
	}
	staleIDs := make([]string, 0, len(stale))
	for id := range stale {
		if _, ok := desired[id]; !ok {
			staleIDs = append(staleIDs, id)
		}
	}
	slices.Sort(staleIDs)
	for _, id := range staleIDs {
		if err := client.DeleteModel(ctx, id); err != nil {
			failed = err
			registered = append(registered, id)
			continue
		}
		r.aiGatewayCatalog.noteDeleted(id)
	}
	ids := make([]string, 0, len(desired))
	for id := range desired {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var names []string
	for _, id := range ids {
		registration := desired[id]
		present, known := r.aiGatewayCatalog.has(id)
		if slices.Contains(copilot.Registered, id) && (!known || present) {
			registered = append(registered, id)
			names = append(names, registration.request.ModelName)
			continue
		}
		if !proxyReady {
			pending = true
			continue
		}
		err := client.AddModel(ctx, registration.request)
		if err != nil {
			// An earlier add may have succeeded without being recorded.
			// Replace it rather than guess.
			if deleteErr := client.DeleteModel(ctx, id); deleteErr == nil {
				err = client.AddModel(ctx, registration.request)
			}
		}
		if err != nil {
			failed = err
			continue
		}
		r.aiGatewayCatalog.noteAdded(id, registration.request.AccessGroups)
		registered = append(registered, id)
		names = append(names, registration.request.ModelName)
	}
	slices.Sort(registered)
	slices.Sort(names)
	slices.Sort(unpriced)
	copilot.Registered = registered
	copilot.Models = names
	copilot.Unpriced = unpriced
	switch {
	case failed != nil:
		degraded("RouteRegistrationFailed", failed)
	case !signedIn:
		copilot.Phase = cetirunv1alpha1.AIGatewayCopilotPhaseSignInRequired
		copilot.Reason = ""
	case mismatched:
		copilot.Phase = cetirunv1alpha1.AIGatewayCopilotPhaseDegraded
		copilot.Reason = "SignInIncomplete"
	case len(wanted) == 0:
		copilot.Phase = cetirunv1alpha1.AIGatewayCopilotPhaseDegraded
		copilot.Reason = "ModelListMissing"
	case pending:
		copilot.Phase = cetirunv1alpha1.AIGatewayCopilotPhaseStarting
		copilot.Reason = "ProxyStarting"
	default:
		copilot.Phase = cetirunv1alpha1.AIGatewayCopilotPhaseReady
		copilot.Reason = ""
	}
}

// copilotProxyReady reports whether the Agent's Copilot proxy has a ready
// replica, that is, it read the sign-in and serves requests.
func (r *AgentReconciler) copilotProxyReady(ctx context.Context, instance *cetirunv1alpha1.Agent) bool {
	deployment := &appsv1.Deployment{}
	key := types.NamespacedName{Namespace: instanceNamespace(instance.Name), Name: copilotProxyServiceName(instance)}
	if err := r.Get(ctx, key, deployment); err != nil {
		return false
	}
	return deployment.Status.ReadyReplicas > 0
}

// deleteCopilotRegistrations removes the Agent's Copilot routes: those its
// status records and every route the Gateway lists in the Agent's access
// group. A route registered just before the operator stopped, before the
// status recorded it, exists only in the Gateway, and the operator's catalog
// may not have been listed yet; so the Gateway is listed here, and a failed
// listing is an error that keeps what depends on the cleanup, such as the
// Agent's finalizer.
func (r *AgentReconciler) deleteCopilotRegistrations(ctx context.Context, client aiGatewayKeyClient, instance *cetirunv1alpha1.Agent) error {
	deployments, err := client.ListModels(ctx)
	if err != nil {
		return fmt.Errorf("list AI Gateway models: %w", err)
	}
	ids := map[string]bool{}
	if status := instance.Status.AIGateway; status != nil && status.Copilot != nil {
		for _, id := range status.Copilot.Registered {
			ids[id] = true
		}
	}
	group := copilotAccessGroup(instance)
	for _, deployment := range deployments {
		if slices.Contains(deployment.AccessGroups, group) {
			ids[deployment.ID] = true
		}
	}
	var remaining []string
	var firstErr error
	for id := range ids {
		if err := client.DeleteModel(ctx, id); err != nil {
			remaining = append(remaining, id)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		r.aiGatewayCatalog.noteDeleted(id)
	}
	if status := instance.Status.AIGateway; status != nil && status.Copilot != nil {
		slices.Sort(remaining)
		status.Copilot.Registered = remaining
		if firstErr == nil {
			status.Copilot.Models = nil
		}
	}
	return firstErr
}

// copilotSignIn is what the token Secret records of the owner's sign-in.
type copilotSignIn struct {
	signedIn bool
	// generation identifies the sign-in whose token is stored; the models on
	// the Agent carry the same value. Empty for a token stored before
	// generations were recorded.
	generation string
	// completed reports that the Portal completed that sign-in, under its
	// fence: one cancelled, replaced or ended first never is.
	completed bool
	storedAt  time.Time
}

// ensureCopilotTokenSecret creates the empty token Secret the proxy mounts,
// so the proxy starts and waits for the owner's sign-in. It reports whether
// the Portal has written a token, for which sign-in, and whether the Portal
// completed that sign-in. A Secret left by an earlier Agent of the same name
// is cleared: its token belongs to that Agent's sign-in.
func (r *AgentReconciler) ensureCopilotTokenSecret(ctx context.Context, instance *cetirunv1alpha1.Agent) (copilotSignIn, error) {
	key := types.NamespacedName{Namespace: instanceNamespace(instance.Name), Name: cetirunv1alpha1.CopilotTokenSecretName}
	secret := &corev1.Secret{}
	// Read past the cache: the Portal writes the token just before the
	// Agent annotation that wakes this reconcile, and a lagging cache would
	// record the sign-in as missing.
	err := r.customReader().Get(ctx, key, secret)
	if apierrors.IsNotFound(err) {
		secret = &corev1.Secret{Type: corev1.SecretTypeOpaque}
		secret.Name, secret.Namespace = key.Name, key.Namespace
		labelAgentSecret(secret, instance)
		if err := r.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
			return copilotSignIn{}, fmt.Errorf("create Copilot token Secret: %w", err)
		}
		return copilotSignIn{}, nil
	}
	if err != nil {
		return copilotSignIn{}, fmt.Errorf("get Copilot token Secret: %w", err)
	}
	if secret.Labels[aiGatewayAgentUIDLabel] != string(instance.UID) {
		labelAgentSecret(secret, instance)
		secret.Data = nil
		delete(secret.Annotations, cetirunv1alpha1.AnnotationCopilotSignInGeneration)
		delete(secret.Annotations, cetirunv1alpha1.AnnotationCopilotSignInCompleted)
		delete(secret.Annotations, cetirunv1alpha1.AnnotationCopilotTokenStoredAt)
		if err := r.Update(ctx, secret); err != nil {
			return copilotSignIn{}, fmt.Errorf("reset Copilot token Secret: %w", err)
		}
		return copilotSignIn{}, nil
	}
	generation := secret.Annotations[cetirunv1alpha1.AnnotationCopilotSignInGeneration]
	signIn := copilotSignIn{
		signedIn:   strings.TrimSpace(string(secret.Data[cetirunv1alpha1.CopilotTokenSecretKey])) != "",
		generation: generation,
		completed:  generation != "" && secret.Annotations[cetirunv1alpha1.AnnotationCopilotSignInCompleted] == generation,
	}
	if storedAt, err := time.Parse(time.RFC3339, secret.Annotations[cetirunv1alpha1.AnnotationCopilotTokenStoredAt]); err == nil {
		signIn.storedAt = storedAt
	}
	return signIn, nil
}

func (r *AgentReconciler) deleteAgentSecret(ctx context.Context, instance *cetirunv1alpha1.Agent, name string) error {
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Namespace: instanceNamespace(instance.Name), Name: name}, secret)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if secret.Labels[aiGatewayAgentUIDLabel] != string(instance.UID) {
		return nil
	}
	if err := r.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// aiGatewayModelEntry is one model in the Agent's configuration.
type aiGatewayModelEntry struct {
	ID            string
	Name          string
	ContextWindow int64
	MaxTokens     int64
}

// aiGatewayMaxModels is the most models an Agent's configuration lists, the
// agent chart's limit on aiGateway.models.
const aiGatewayMaxModels = 256

// aiGatewayModelEntries lists the models the Agent is configured with and its
// default: the shared models when the owner kept them, from the Gateway
// catalog, then the Agent's registered Copilot models. The default is the
// Agent's own choice when available, then the deployment default, then the
// first shared model, then the first Copilot model. An Agent without models
// gets a placeholder its configuration requires; the Portal explains it.
//
// The configuration lists at most aiGatewayMaxModels models. The owner's
// Copilot models, of which registration keeps at most
// aiGatewayMaxCopilotModels, all stay; shared models fill the rest in name
// order, the Agent's own choice and the deployment default first. The names
// of the shared models left out are returned. The Agent's key still reaches
// them; only its configuration does not list them.
func (r *AgentReconciler) aiGatewayModelEntries(instance *cetirunv1alpha1.Agent) ([]aiGatewayModelEntry, string, []string) {
	var sharedEntries, copilotEntries []aiGatewayModelEntry
	status := instance.Status.AIGateway
	if instance.Spec.SharedModelsEnabled() {
		groups := []string(nil)
		if status != nil {
			groups = status.AccessGroups
		}
		if models, known := r.aiGatewayCatalog.sharedForGroups(groups); known {
			for _, model := range models {
				sharedEntries = append(sharedEntries, aiGatewayModelEntry{
					ID: model.Name, Name: model.Name, ContextWindow: model.ContextWindow, MaxTokens: model.MaxOutputTokens,
				})
			}
		} else if status != nil {
			// Without a catalog, keep the shared models last configured.
			for _, name := range status.Models {
				if !strings.HasPrefix(name, aiGatewayCopilotModelPrefix) && name != aiGatewayNoModelsModel {
					sharedEntries = append(sharedEntries, aiGatewayModelEntry{ID: name, Name: name})
				}
			}
		}
	}
	if r.aiGatewayCopilotWanted(instance) && status != nil && status.Copilot != nil {
		limits := map[string]cetirunv1alpha1.CopilotModel{}
		if models, err := parseCopilotModels(instance.Annotations[cetirunv1alpha1.AnnotationCopilotModels]); err == nil {
			for _, model := range models {
				limits[aiGatewayCopilotModelPrefix+model.ID] = model
			}
		}
		for _, name := range status.Copilot.Models {
			if len(copilotEntries) == aiGatewayMaxCopilotModels {
				break
			}
			model := limits[name]
			display := model.Name
			if display == "" {
				display = strings.TrimPrefix(name, aiGatewayCopilotModelPrefix)
			}
			// The ID is bounded by copilotModelID; the display name comes
			// from GitHub, so keep the configured name within the chart's
			// limit.
			display = truncateRunes(display, aiGatewayModelNameLimit-utf8.RuneCountInString(copilotModelNameSuffix))
			copilotEntries = append(copilotEntries, aiGatewayModelEntry{
				ID: name, Name: display + copilotModelNameSuffix, ContextWindow: model.ContextWindow, MaxTokens: model.MaxOutputTokens,
			})
		}
	}
	sharedEntries, omitted := keepSharedModels(sharedEntries, aiGatewayMaxModels-len(copilotEntries),
		strings.TrimSpace(instance.Spec.Model), r.AIGateway.DefaultModel)
	var shared, copilot []string
	for _, entry := range sharedEntries {
		shared = append(shared, entry.ID)
	}
	for _, entry := range copilotEntries {
		copilot = append(copilot, entry.ID)
	}
	entries := append(sharedEntries, copilotEntries...)
	available := func(name string) bool {
		return name != "" && (slices.Contains(shared, name) || slices.Contains(copilot, name))
	}
	switch {
	case available(strings.TrimSpace(instance.Spec.Model)):
		return entries, strings.TrimSpace(instance.Spec.Model), omitted
	case available(r.AIGateway.DefaultModel):
		return entries, r.AIGateway.DefaultModel, omitted
	case len(shared) > 0:
		return entries, shared[0], omitted
	case len(copilot) > 0:
		return entries, copilot[0], omitted
	}
	placeholder := aiGatewayNoModelsModel
	if r.aiGatewayCopilotWanted(instance) {
		placeholder = aiGatewaySignInRequiredModel
	}
	return append(entries, aiGatewayModelEntry{ID: placeholder, Name: placeholder}), placeholder, omitted
}

// keepSharedModels keeps at most room of the shared models: the preferred
// ones first, then the others in their order. The kept models stay in their
// order; the names of the others are returned.
func keepSharedModels(models []aiGatewayModelEntry, room int, preferred ...string) ([]aiGatewayModelEntry, []string) {
	if len(models) <= room {
		return models, nil
	}
	keep := map[string]bool{}
	for _, name := range preferred {
		if len(keep) < room && slices.ContainsFunc(models, func(model aiGatewayModelEntry) bool { return model.ID == name }) {
			keep[name] = true
		}
	}
	for _, model := range models {
		if len(keep) >= room {
			break
		}
		keep[model.ID] = true
	}
	var kept []aiGatewayModelEntry
	var omitted []string
	for _, model := range models {
		if keep[model.ID] {
			kept = append(kept, model)
		} else {
			omitted = append(omitted, model.ID)
		}
	}
	return kept, omitted
}
