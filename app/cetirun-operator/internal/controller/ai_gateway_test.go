// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"helm.sh/helm/v3/pkg/chart/loader"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"

	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
)

const (
	testAIGatewayUID   = "3f0b5a8e-1c2d-4e5f-8a9b-0c1d2e3f4a5b"
	testAIGatewayURL   = "http://cetirun-ai-gateway.cetirun-system.svc.cluster.local:4000"
	testAIGatewayTeam  = "platform-admins"
	testProxyImage     = "registry.example.com/cetirun-ai-gateway:0.1.1"
	testCopilotModels  = `[{"id":"gpt-5.5","name":"GPT-5.5","contextWindow":400000,"maxOutputTokens":128000},{"id":"claude-sonnet-4.5","name":"Claude Sonnet 4.5"},{"id":"grok-code-fast-1"},{"id":"bad/id"}]`
	testSharedModelOne = "gpt-6-sol"
)

// fakeAIGateway mirrors the LiteLLM behaviour the operator relies on: keys
// are stored by the SHA-256 of their value and aliases are unique; teams,
// users and memberships are created once; model IDs are unique.
type fakeAIGateway struct {
	keys           map[string]*aigateway.Key
	getErr         error
	deleteErr      error
	addModelErr    error
	generated      []aigateway.GenerateRequest
	updates        [][]string
	ownerUpdates   [][2]string
	deletedTokens  []string
	deletedAliases []string
	beforeGenerate func(aigateway.GenerateRequest)
	teams          map[string]bool
	users          map[string]bool
	members        map[string]bool
	models         map[string]aigateway.ModelRequest
	added          []string
	deletedModels  []string
	shared         []aigateway.Deployment
	priced         map[string]bool
	listCalls      int
	// blockUnpriced is off so that tests about other behaviour need no
	// prices; the tests about prices turn it on.
	blockUnpriced bool
	blockErr      error
}

func newFakeAIGateway() *fakeAIGateway {
	return &fakeAIGateway{
		keys: map[string]*aigateway.Key{}, teams: map[string]bool{}, users: map[string]bool{}, members: map[string]bool{},
		models: map[string]aigateway.ModelRequest{},
		shared: []aigateway.Deployment{
			{ID: "cfg-1", ModelName: testSharedModelOne, AccessGroups: []string{aiGatewaySharedAccessGroup}, MaxInputTokens: 1000000, MaxOutputTokens: 32768},
			{ID: "cfg-2", ModelName: "admin-only", AccessGroups: []string{"cetirun-admin"}},
		},
		priced: map[string]bool{"gpt-5.5": true, "claude-sonnet-4-5": true, "gpt-4.1": true},
	}
}

func (f *fakeAIGateway) GetKey(_ context.Context, token string) (*aigateway.Key, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	key, ok := f.keys[token]
	if !ok {
		return nil, nil
	}
	copied := *key
	copied.Models = slices.Clone(key.Models)
	return &copied, nil
}

func (f *fakeAIGateway) GenerateKey(_ context.Context, req aigateway.GenerateRequest) error {
	if f.beforeGenerate != nil {
		f.beforeGenerate(req)
	}
	for _, key := range f.keys {
		if key.Alias == req.Alias {
			return aigateway.ErrAliasExists
		}
	}
	f.generated = append(f.generated, req)
	budget := req.MaxBudget
	f.keys[aigateway.Token(req.Key)] = &aigateway.Key{
		Alias: req.Alias, Models: slices.Clone(req.Models), MaxBudget: &budget, BudgetDuration: req.BudgetDuration,
		TeamID: req.TeamID, UserID: req.UserID,
	}
	return nil
}

func (f *fakeAIGateway) UpdateKeyModels(_ context.Context, token string, models []string) error {
	key, ok := f.keys[token]
	if !ok {
		return &aigateway.APIError{StatusCode: 404, Message: "not found"}
	}
	key.Models = slices.Clone(models)
	f.updates = append(f.updates, slices.Clone(models))
	return nil
}

func (f *fakeAIGateway) UpdateKeyOwner(_ context.Context, token, teamID, userID string) error {
	key, ok := f.keys[token]
	if !ok {
		return &aigateway.APIError{StatusCode: 404, Message: "not found"}
	}
	if teamID != "" && !f.members[teamID+"/"+userID] {
		return &aigateway.APIError{StatusCode: 403, Message: "User is not a member of the team"}
	}
	key.TeamID, key.UserID = teamID, userID
	f.ownerUpdates = append(f.ownerUpdates, [2]string{teamID, userID})
	return nil
}

func (f *fakeAIGateway) DeleteKeys(_ context.Context, tokens []string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	for _, token := range tokens {
		f.deletedTokens = append(f.deletedTokens, token)
		delete(f.keys, token)
	}
	return nil
}

func (f *fakeAIGateway) DeleteKeyByAlias(_ context.Context, alias string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletedAliases = append(f.deletedAliases, alias)
	for token, key := range f.keys {
		if key.Alias == alias {
			delete(f.keys, token)
		}
	}
	return nil
}

func (f *fakeAIGateway) EnsureTeam(_ context.Context, teamID, _ string, _ map[string]string) error {
	f.teams[teamID] = true
	return nil
}

func (f *fakeAIGateway) EnsureUser(_ context.Context, userID string, _ map[string]string) error {
	f.users[userID] = true
	return nil
}

func (f *fakeAIGateway) EnsureTeamMember(_ context.Context, teamID, userID string) error {
	if !f.teams[teamID] {
		return &aigateway.APIError{StatusCode: 400, Message: "team does not exist"}
	}
	f.members[teamID+"/"+userID] = true
	return nil
}

func (f *fakeAIGateway) ListModels(context.Context) ([]aigateway.Deployment, error) {
	f.listCalls++
	deployments := slices.Clone(f.shared)
	for id, model := range f.models {
		deployments = append(deployments, aigateway.Deployment{
			ID: id, ModelName: model.ModelName, AccessGroups: model.AccessGroups, FromDatabase: true, Priced: true,
		})
	}
	return deployments, nil
}

func (f *fakeAIGateway) AddModel(_ context.Context, req aigateway.ModelRequest) error {
	if f.addModelErr != nil {
		return f.addModelErr
	}
	if _, ok := f.models[req.ID]; ok {
		return &aigateway.APIError{StatusCode: 500, Message: "Failed to add model to db"}
	}
	f.models[req.ID] = req
	f.added = append(f.added, req.ID)
	return nil
}

func (f *fakeAIGateway) DeleteModel(_ context.Context, id string) error {
	f.deletedModels = append(f.deletedModels, id)
	delete(f.models, id)
	return nil
}

func (f *fakeAIGateway) PricedModels(context.Context) (map[string]bool, error) {
	return f.priced, nil
}

func (f *fakeAIGateway) BlockUnpricedModels(context.Context) (bool, error) {
	if f.blockErr != nil {
		return false, f.blockErr
	}
	return f.blockUnpriced, nil
}

func (f *fakeAIGateway) modelNames() []string {
	var names []string
	for _, model := range f.models {
		names = append(names, model.ModelName)
	}
	slices.Sort(names)
	return names
}

func aiGatewayTestConfig() AIGatewayConfig {
	return AIGatewayConfig{
		Enabled: true, URL: testAIGatewayURL, MasterKeySecretName: "cetirun-ai-gateway-auth", MasterKeySecretKey: "master-key",
		DefaultBudget: 10, CopilotProxyImage: testProxyImage,
	}
}

func aiGatewayTestAgent(team string) *cetirunv1alpha1.Agent {
	return &cetirunv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alice", UID: types.UID(testAIGatewayUID), Generation: 1,
			Finalizers: []string{cetirunv1alpha1.Finalizer},
		},
		Spec: cetirunv1alpha1.AgentSpec{
			Agent:                 cetirunv1alpha1.HarnessSpec{Type: cetirunv1alpha1.AgentTypeOpenClaw},
			User:                  cetirunv1alpha1.UserSpec{Alias: "alice", DisplayName: "Alice"},
			Team:                  team,
			GatewayTokenSecretRef: "gateway-token",
			Helm:                  cetirunv1alpha1.HelmSpec{Version: "0.1.28", Values: map[string]runtime.RawExtension{}},
		},
	}
}

func modelAccess(shared, copilot bool) *cetirunv1alpha1.ModelAccessSpec {
	return &cetirunv1alpha1.ModelAccessSpec{SharedModels: &shared, GitHubCopilot: &copilot}
}

func aiGatewayTestTeam(name string) *cetirunv1alpha1.Team {
	return &cetirunv1alpha1.Team{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func aiGatewayTestReconciler(t *testing.T, objects ...client.Object) (*AgentReconciler, client.Client, *fakeAIGateway) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, cetirunv1alpha1.AddToScheme, istiov1beta1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).
		WithStatusSubresource(&cetirunv1alpha1.Agent{}).Build()
	gateway := newFakeAIGateway()
	r := &AgentReconciler{
		Client: kube, Scheme: scheme, EventRecorder: record.NewFakeRecorder(100),
		OperatorNamespace: "cetirun-system", ChartVersion: "0.1.28", DomainSuffix: "example.com", IstioRevision: "test",
		DefaultModel: "copilot-model",
		AIGateway:    aiGatewayTestConfig(),
		aiGatewayClientFactory: func(context.Context) (aiGatewayKeyClient, error) {
			return gateway, nil
		},
	}
	return r, kube, gateway
}

func aiGatewayNamespace() *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: instanceNamespace("alice")}}
}

func readSecret(t *testing.T, kube client.Client, name string) *corev1.Secret {
	t.Helper()
	secret := &corev1.Secret{}
	if err := kube.Get(context.Background(), types.NamespacedName{Namespace: instanceNamespace("alice"), Name: name}, secret); err != nil {
		t.Fatalf("read Secret %s: %v", name, err)
	}
	return secret
}

func readKeySecret(t *testing.T, kube client.Client) *corev1.Secret {
	t.Helper()
	return readSecret(t, kube, aiGatewayKeySecretName)
}

func keySecret(uid, value string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: aiGatewayKeySecretName, Namespace: instanceNamespace("alice"),
			Labels: map[string]string{aiGatewayAgentUIDLabel: uid},
		},
		Data: map[string][]byte{aiGatewayKeySecretField: []byte(value)},
	}
}

func mustKeyValue(t *testing.T) string {
	t.Helper()
	value, err := aigateway.NewKeyValue()
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func copilotGroup() string { return aiGatewayCopilotGroupPrefix + testAIGatewayUID }

func TestReconcileAIGatewayCreatesKeyAfterPersistingSecret(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	gateway.beforeGenerate = func(req aigateway.GenerateRequest) {
		if string(readKeySecret(t, kube).Data[aiGatewayKeySecretField]) != req.Key {
			t.Fatal("the key value must be persisted before the Gateway create request")
		}
	}
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	secret := readKeySecret(t, kube)
	value := string(secret.Data[aiGatewayKeySecretField])
	if !aigateway.ValidKeyValue(value) || secret.Labels[aiGatewayAgentUIDLabel] != testAIGatewayUID {
		t.Fatalf("unexpected key Secret labels=%v", secret.Labels)
	}
	if len(gateway.generated) != 1 {
		t.Fatalf("generated %d keys, want 1", len(gateway.generated))
	}
	req := gateway.generated[0]
	wantGroups := sortedCopy([]string{aiGatewaySharedAccessGroup, copilotGroup()})
	if req.Key != value || req.Alias != "cetirun-alice-"+testAIGatewayUID || !slices.Equal(req.Models, wantGroups) ||
		req.MaxBudget != 10 || req.BudgetDuration != "1mo" || req.Metadata["cetirun_agent_uid"] != testAIGatewayUID ||
		req.TeamID != aiGatewayTeamPrefix+testAIGatewayTeam || req.UserID != "alice" {
		t.Fatalf("unexpected create request %+v", req)
	}
	if !gateway.teams[aiGatewayTeamPrefix+testAIGatewayTeam] || !gateway.users["alice"] ||
		!gateway.members[aiGatewayTeamPrefix+testAIGatewayTeam+"/alice"] {
		t.Fatalf("the Gateway team, user and membership must exist before the key: %v %v %v", gateway.teams, gateway.users, gateway.members)
	}
	status := agent.Status.AIGateway
	if status.Phase != cetirunv1alpha1.AIGatewayPhaseReady || status.KeyID != aigateway.Token(value)[:aiGatewayKeyIDLength] ||
		!slices.Equal(status.AccessGroups, wantGroups) || !status.Routed || status.KeySecretName != aiGatewayKeySecretName ||
		status.Team != aiGatewayTeamPrefix+testAIGatewayTeam {
		t.Fatalf("unexpected status %+v", status)
	}
	if !slices.Equal(status.Models, []string{testSharedModelOne}) || status.DefaultModel != testSharedModelOne {
		t.Fatalf("the Agent must be configured with the shared models: %v default %q", status.Models, status.DefaultModel)
	}
	if strings.Contains(status.KeyID, value) || strings.Contains(status.KeyAlias, value) {
		t.Fatal("status must not contain the key value")
	}
	if !meta.IsStatusConditionTrue(agent.Status.Conditions, cetirunv1alpha1.ConditionAIGatewayReady) {
		t.Fatal("AIGatewayReady is not true")
	}

	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	if len(gateway.generated) != 1 || len(gateway.updates) != 0 || len(gateway.ownerUpdates) != 0 {
		t.Fatalf("a steady reconcile must not create or update: generated=%d updates=%d owner=%d",
			len(gateway.generated), len(gateway.updates), len(gateway.ownerUpdates))
	}
}

func TestReconcileAIGatewayGroupsFollowModelAccessSwitches(t *testing.T) {
	for name, tc := range map[string]struct {
		access *cetirunv1alpha1.ModelAccessSpec
		want   []string
	}{
		"both on by default": {nil, sortedCopy([]string{aiGatewaySharedAccessGroup, copilotGroup()})},
		"shared only":        {modelAccess(true, false), []string{aiGatewaySharedAccessGroup}},
		"copilot only":       {modelAccess(false, true), []string{copilotGroup()}},
		"neither":            {modelAccess(false, false), []string{aigateway.NoModels}},
	} {
		t.Run(name, func(t *testing.T) {
			agent := aiGatewayTestAgent(testAIGatewayTeam)
			agent.Spec.ModelAccess = tc.access
			r, _, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
			if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
				t.Fatal(err)
			}
			if got := gateway.generated[0].Models; !slices.Equal(got, tc.want) {
				t.Fatalf("key models %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReconcileAIGatewayWithoutProxyImageGrantsNoCopilot(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, _, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	r.AIGateway.CopilotProxyImage = ""
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	if got := gateway.generated[0].Models; !slices.Equal(got, []string{aiGatewaySharedAccessGroup}) {
		t.Fatalf("without a proxy image no Copilot group is granted, got %v", got)
	}
	if agent.Status.AIGateway.Copilot != nil {
		t.Fatal("no Copilot proxy without an image")
	}
}

func TestReconcileAIGatewayMovesKeyWhenTeamChanges(t *testing.T) {
	agent := aiGatewayTestAgent("team-a")
	r, _, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	agent.Spec.Team = "team-b"
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	if len(gateway.ownerUpdates) != 1 || gateway.ownerUpdates[0] != [2]string{aiGatewayTeamPrefix + "team-b", "alice"} {
		t.Fatalf("the key must move to the new team after the user joins it: %v", gateway.ownerUpdates)
	}
	for _, key := range gateway.keys {
		if key.MaxBudget == nil || *key.MaxBudget != 10 {
			t.Fatalf("the budget must be left as created, got %v", key.MaxBudget)
		}
	}
	if agent.Status.AIGateway.Team != aiGatewayTeamPrefix+"team-b" {
		t.Fatalf("status team %q", agent.Status.AIGateway.Team)
	}
	agent.Spec.Team = ""
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	if last := gateway.ownerUpdates[len(gateway.ownerUpdates)-1]; last != [2]string{"", "alice"} {
		t.Fatalf("an Agent without a Team leaves the key without a team: %v", last)
	}
}

func TestReconcileAIGatewayAdoptsKeyCreatedBeforeStatusWasLost(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	value := mustKeyValue(t)
	r, _, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace(), keySecret(testAIGatewayUID, value))
	budget := 25.0
	gateway.keys[aigateway.Token(value)] = &aigateway.Key{
		Alias: aiGatewayKeyAlias(agent), Models: sortedCopy([]string{aiGatewaySharedAccessGroup, copilotGroup()}), MaxBudget: &budget,
		TeamID: aiGatewayTeamPrefix + testAIGatewayTeam, UserID: "alice",
	}
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	if len(gateway.generated) != 0 || len(gateway.deletedAliases) != 0 || len(gateway.updates) != 0 {
		t.Fatal("an existing key for the persisted value must be adopted, not recreated")
	}
	if agent.Status.AIGateway.KeyID != aigateway.Token(value)[:aiGatewayKeyIDLength] {
		t.Fatal("adopted key is not recorded")
	}
}

// When the key Secret no longer holds the value of this Agent's existing key,
// replacing the key would reset its spend and budget (and unblock it), so the
// operator reports the state and leaves the key alone until an administrator
// restores the Secret or deletes the key.
func TestReconcileAIGatewayKeepsOwnKeyWhenItsValueIsLost(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	value := mustKeyValue(t)
	r, _, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace(), keySecret(testAIGatewayUID, value))
	budget := 25.0
	gateway.keys["token-of-a-value-no-longer-held"] = &aigateway.Key{
		Alias: aiGatewayKeyAlias(agent), Models: []string{aiGatewaySharedAccessGroup}, MaxBudget: &budget, Blocked: true,
	}
	for pass := 1; pass <= 2; pass++ {
		if err := r.reconcileAIGateway(context.Background(), agent, true); err == nil {
			t.Fatalf("pass %d: a lost key value must be reported", pass)
		}
	}
	status := agent.Status.AIGateway
	if status.Phase != cetirunv1alpha1.AIGatewayPhaseDegraded || status.Reason != aiGatewayReasonKeySecretLost {
		t.Fatalf("status = %s/%s", status.Phase, status.Reason)
	}
	if len(gateway.deletedAliases) != 0 || len(gateway.deletedTokens) != 0 || len(gateway.generated) != 0 {
		t.Fatalf("the existing key must be left alone: deleted=%v/%v generated=%d", gateway.deletedAliases, gateway.deletedTokens, len(gateway.generated))
	}
	if key, ok := gateway.keys["token-of-a-value-no-longer-held"]; !ok || !key.Blocked || *key.MaxBudget != budget {
		t.Fatal("the existing key and its budget state must survive")
	}
	events := r.EventRecorder.(*record.FakeRecorder).Events
	lost := 0
	for len(events) > 0 {
		if strings.Contains(<-events, "AIGatewayKeySecretLost") {
			lost++
		}
	}
	if lost != 1 {
		t.Fatalf("the lost value must be reported once, got %d events", lost)
	}

	// An administrator deletes the old key: the next pass creates this one.
	delete(gateway.keys, "token-of-a-value-no-longer-held")
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	if len(gateway.generated) != 1 || gateway.generated[0].Key != value || agent.Status.AIGateway.Phase != cetirunv1alpha1.AIGatewayPhaseReady {
		t.Fatalf("after the administrator's decision the key must be created: generated=%d phase=%s", len(gateway.generated), agent.Status.AIGateway.Phase)
	}
}

func TestReconcileAIGatewayReplacesSecretOfAnotherAgent(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	stale := mustKeyValue(t)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace(), keySecret("00000000-0000-0000-0000-000000000000", stale))
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	secret := readKeySecret(t, kube)
	if string(secret.Data[aiGatewayKeySecretField]) == stale || secret.Labels[aiGatewayAgentUIDLabel] != testAIGatewayUID {
		t.Fatal("a Secret written for another Agent UID must get a new value")
	}
	if gateway.generated[0].Key == stale {
		t.Fatal("the stale value must not be registered")
	}
}

func TestReconcileAIGatewayNeverAdoptsAnotherAlias(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	value := mustKeyValue(t)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace(), keySecret(testAIGatewayUID, value))
	foreign := &aigateway.Key{Alias: "someone-else", Models: []string{"model-x"}}
	gateway.keys[aigateway.Token(value)] = foreign
	err := r.reconcileAIGateway(context.Background(), agent, true)
	if err == nil || agent.Status.AIGateway.Reason != "KeyAliasMismatch" {
		t.Fatalf("want KeyAliasMismatch, got %v / %+v", err, agent.Status.AIGateway)
	}
	if string(readKeySecret(t, kube).Data[aiGatewayKeySecretField]) == value {
		t.Fatal("the Secret must get a new value")
	}
	if len(gateway.updates) != 0 || len(gateway.deletedTokens) != 0 || len(gateway.deletedAliases) != 0 ||
		!slices.Equal(foreign.Models, []string{"model-x"}) {
		t.Fatal("a key with another alias must not be changed or deleted")
	}
}

func TestReconcileAIGatewayOutageKeepsCheckpoint(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	gateway.getErr = errors.New("synthetic outage")
	err := r.reconcileAIGateway(context.Background(), agent, true)
	if err == nil || agent.Status.AIGateway.Phase != cetirunv1alpha1.AIGatewayPhaseDegraded || agent.Status.AIGateway.Reason != "GatewayUnavailable" {
		t.Fatalf("want Degraded/GatewayUnavailable, got %v %+v", err, agent.Status.AIGateway)
	}
	if agent.Status.AIGateway.Routed {
		t.Fatal("an Agent must not be routed before its key exists")
	}
	first := string(readKeySecret(t, kube).Data[aiGatewayKeySecretField])
	gateway.getErr = nil
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	if gateway.generated[0].Key != first {
		t.Fatal("the persisted value must be reused after the outage")
	}
}

func TestReconcileAIGatewayRequiresChartSupportForRouting(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, _, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	if err := r.reconcileAIGateway(context.Background(), agent, false); err != nil {
		t.Fatal(err)
	}
	condition := meta.FindStatusCondition(agent.Status.Conditions, cetirunv1alpha1.ConditionAIGatewayReady)
	if agent.Status.AIGateway.Routed || condition == nil || !strings.Contains(condition.Message, "chart version") {
		t.Fatalf("an Agent on a chart without AI Gateway support must not be routed: %+v %+v", agent.Status.AIGateway, condition)
	}
	if slices.Contains(gateway.generated[0].Models, copilotGroup()) || agent.Status.AIGateway.Copilot != nil {
		t.Fatal("an unrouted Agent gets no Copilot proxy")
	}
}

func TestReconcileAIGatewayHermesKeepsProvider(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	agent.Spec.Agent.Type = cetirunv1alpha1.AgentTypeHermes
	r, _, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	if len(gateway.generated) != 1 || agent.Status.AIGateway.Routed || agent.Status.AIGateway.Copilot != nil {
		t.Fatalf("Hermes gets a key but is not routed yet: generated=%d status=%+v", len(gateway.generated), agent.Status.AIGateway)
	}
	if !slices.Equal(gateway.generated[0].Models, []string{aiGatewaySharedAccessGroup}) {
		t.Fatalf("Hermes key models %v", gateway.generated[0].Models)
	}
}

func readyProxy(ready int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "openclaw-alice-copilot-proxy", Namespace: instanceNamespace("alice")},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: ready},
	}
}

func signIn(t *testing.T, kube client.Client, agent *cetirunv1alpha1.Agent, models string) {
	t.Helper()
	secret := readSecret(t, kube, cetirunv1alpha1.CopilotTokenSecretName)
	secret.Data = map[string][]byte{cetirunv1alpha1.CopilotTokenSecretKey: []byte("gho_synthetic")}
	if err := kube.Update(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	if agent.Annotations == nil {
		agent.Annotations = map[string]string{}
	}
	agent.Annotations[cetirunv1alpha1.AnnotationCopilotModels] = models
}

func TestReconcileAIGatewayCopilotSignInRegistersPricedModels(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	copilot := agent.Status.AIGateway.Copilot
	if copilot == nil || copilot.Phase != cetirunv1alpha1.AIGatewayCopilotPhaseSignInRequired || copilot.AccessGroup != copilotGroup() {
		t.Fatalf("before sign-in the proxy waits: %+v", copilot)
	}
	proxyKey := string(readSecret(t, kube, aiGatewayProxySecretName).Data[aiGatewayProxySecretField])
	if !aigateway.ValidKeyValue(proxyKey) {
		t.Fatal("the proxy master key Secret was not created")
	}
	if token := readSecret(t, kube, cetirunv1alpha1.CopilotTokenSecretName); len(token.Data) != 0 || token.Labels[aiGatewayAgentUIDLabel] != testAIGatewayUID {
		t.Fatalf("the token Secret must exist empty for the proxy to mount: %+v", token)
	}
	if agent.Status.AIGateway.DefaultModel != testSharedModelOne {
		t.Fatalf("before sign-in the shared model is the default, got %q", agent.Status.AIGateway.DefaultModel)
	}

	signIn(t, kube, agent, testCopilotModels)
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	if agent.Status.AIGateway.Copilot.Phase != cetirunv1alpha1.AIGatewayCopilotPhaseStarting || len(gateway.models) != 0 {
		t.Fatalf("routes wait for the proxy to be ready: %+v models=%d", agent.Status.AIGateway.Copilot, len(gateway.models))
	}

	if err := kube.Create(context.Background(), readyProxy(1)); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	copilot = agent.Status.AIGateway.Copilot
	if copilot.Phase != cetirunv1alpha1.AIGatewayCopilotPhaseReady {
		t.Fatalf("copilot status %+v", copilot)
	}
	wantNames := []string{"copilot/claude-sonnet-4.5", "copilot/gpt-5.5"}
	if !slices.Equal(copilot.Models, wantNames) || !slices.Equal(gateway.modelNames(), wantNames) {
		t.Fatalf("registered %v, gateway %v", copilot.Models, gateway.modelNames())
	}
	if !slices.Equal(copilot.Unpriced, []string{"grok-code-fast-1"}) {
		t.Fatalf("an unpriced model must be reported, not registered: %v", copilot.Unpriced)
	}
	for id, model := range gateway.models {
		if !strings.HasPrefix(id, copilotGroup()+"-") || !slices.Equal(model.AccessGroups, []string{copilotGroup()}) ||
			model.APIBase != "http://openclaw-alice-copilot-proxy.cetirun-alice.svc.cluster.local:4000" || model.APIKey != proxyKey ||
			model.Model != "litellm_proxy/"+strings.TrimPrefix(model.ModelName, "copilot/") {
			t.Fatalf("unexpected route %s: %+v", id, model)
		}
		if model.ModelName == "copilot/claude-sonnet-4.5" && model.BaseModel != "claude-sonnet-4-5" {
			t.Fatalf("Copilot's dotted Claude name must be priced by the dashed entry, got %q", model.BaseModel)
		}
	}
	wantModels := []string{testSharedModelOne, "copilot/claude-sonnet-4.5", "copilot/gpt-5.5"}
	if !slices.Equal(agent.Status.AIGateway.Models, wantModels) {
		t.Fatalf("agent models %v, want %v", agent.Status.AIGateway.Models, wantModels)
	}

	// Steady state: nothing is re-registered.
	added := len(gateway.added)
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	if len(gateway.added) != added {
		t.Fatal("a steady reconcile must not re-register routes")
	}

	// The account loses a model: its route goes.
	agent.Annotations[cetirunv1alpha1.AnnotationCopilotModels] = `[{"id":"gpt-5.5"}]`
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gateway.modelNames(), []string{"copilot/gpt-5.5"}) || !slices.Equal(agent.Status.AIGateway.Copilot.Models, []string{"copilot/gpt-5.5"}) {
		t.Fatalf("stale routes must be removed: gateway %v status %v", gateway.modelNames(), agent.Status.AIGateway.Copilot.Models)
	}

	// The Gateway lost a route: the catalog shows it gone and it comes back.
	for id := range gateway.models {
		delete(gateway.models, id)
	}
	r.aiGatewayCatalog = aiGatewayCatalog{}
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gateway.modelNames(), []string{"copilot/gpt-5.5"}) {
		t.Fatalf("a lost route must be re-added, gateway %v", gateway.modelNames())
	}

	// The owner turns Copilot off: routes, token and proxy key go.
	agent.Spec.ModelAccess = modelAccess(true, false)
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	if len(gateway.models) != 0 || agent.Status.AIGateway.Copilot != nil {
		t.Fatalf("turning Copilot off removes its routes: %v %+v", gateway.modelNames(), agent.Status.AIGateway.Copilot)
	}
	for _, name := range []string{cetirunv1alpha1.CopilotTokenSecretName, aiGatewayProxySecretName} {
		if err := kube.Get(context.Background(), types.NamespacedName{Namespace: instanceNamespace("alice"), Name: name}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
			t.Fatalf("Secret %s must be deleted: %v", name, err)
		}
	}
	if slices.Contains(gateway.generated[0].Models, copilotGroup()) && slices.Contains(lastKeyModels(gateway), copilotGroup()) {
		t.Fatal("the key must lose the Copilot group")
	}
}

// A route registered just before the operator stopped, before the status
// recorded it, is known only to the Gateway. Once the proxy key or the price
// table changes, its revision is obsolete, and it would keep serving the
// Agent's model name with the old key. A reconcile removes it as soon as the
// catalog has listed it; other Agents' routes stay.
func TestReconcileAIGatewayCopilotRemovesUnrecordedObsoleteRoutes(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace(), readyProxy(1))
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	signIn(t, kube, agent, `[{"id":"gpt-5.5"}]`)
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gateway.modelNames(), []string{"copilot/gpt-5.5"}) {
		t.Fatalf("setup: gateway %v", gateway.modelNames())
	}
	obsolete := copilotRegistrationID(agent, "gpt-5.5", "0badc0de")
	gateway.models[obsolete] = aigateway.ModelRequest{ID: obsolete, ModelName: "copilot/gpt-5.5", APIKey: "sk-obsolete", AccessGroups: []string{copilotGroup()}}
	other := aiGatewayCopilotGroupPrefix + "another-agent-uid-gpt-5.5-0badc0de"
	gateway.models[other] = aigateway.ModelRequest{ID: other, ModelName: "copilot/gpt-5.5", AccessGroups: []string{aiGatewayCopilotGroupPrefix + "another-agent-uid"}}
	if slices.Contains(agent.Status.AIGateway.Copilot.Registered, obsolete) {
		t.Fatal("setup: the obsolete route must not be recorded")
	}

	// After a restart the catalog has not listed the Gateway yet: nothing
	// unrecorded is touched, and the reconcile lists it.
	r.aiGatewayCatalog = aiGatewayCatalog{}
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := gateway.models[obsolete]; !ok {
		t.Fatal("an unlisted route cannot be judged yet")
	}
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := gateway.models[obsolete]; ok {
		t.Fatalf("the unrecorded obsolete route must be removed: %v", gateway.deletedModels)
	}
	if _, ok := gateway.models[other]; !ok {
		t.Fatal("another Agent's route must stay")
	}
	if names := gateway.modelNames(); !slices.Equal(names, []string{"copilot/gpt-5.5", "copilot/gpt-5.5"}) {
		t.Fatalf("this Agent's current route must stay: %v", names)
	}
	if copilot := agent.Status.AIGateway.Copilot; copilot.Phase != cetirunv1alpha1.AIGatewayCopilotPhaseReady ||
		slices.Contains(copilot.Registered, obsolete) || len(copilot.Registered) != 1 {
		t.Fatalf("copilot status %+v", copilot)
	}
}

func lastKeyModels(gateway *fakeAIGateway) []string {
	for _, key := range gateway.keys {
		return key.Models
	}
	return nil
}

func TestReconcileAIGatewayCopilotSignedInWithoutModelListIsDegraded(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, _ := aiGatewayTestReconciler(t, agent, aiGatewayNamespace(), readyProxy(1))
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	signIn(t, kube, agent, "")
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	if copilot := agent.Status.AIGateway.Copilot; copilot.Phase != cetirunv1alpha1.AIGatewayCopilotPhaseDegraded || copilot.Reason != "ModelListMissing" {
		t.Fatalf("copilot status %+v", copilot)
	}
}

func TestReconcileAIGatewayClearsTokenSecretOfAnotherAgent(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	stale := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: cetirunv1alpha1.CopilotTokenSecretName, Namespace: instanceNamespace("alice"),
			Labels: map[string]string{aiGatewayAgentUIDLabel: "00000000-0000-0000-0000-000000000000"}},
		Data: map[string][]byte{cetirunv1alpha1.CopilotTokenSecretKey: []byte("gho_previous_agent")},
	}
	r, kube, _ := aiGatewayTestReconciler(t, agent, aiGatewayNamespace(), stale)
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	secret := readSecret(t, kube, cetirunv1alpha1.CopilotTokenSecretName)
	if len(secret.Data) != 0 || secret.Labels[aiGatewayAgentUIDLabel] != testAIGatewayUID {
		t.Fatal("a same-name replacement must not inherit the previous Agent's sign-in")
	}
	if agent.Status.AIGateway.Copilot.Phase != cetirunv1alpha1.AIGatewayCopilotPhaseSignInRequired {
		t.Fatalf("copilot status %+v", agent.Status.AIGateway.Copilot)
	}
}

func TestAIGatewayModelEntriesChooseDefault(t *testing.T) {
	r, _, gateway := aiGatewayTestReconciler(t)
	r.refreshAIGatewayCatalogWithin(context.Background(), gateway, 0)
	agent := routedAgent()
	agent.Status.AIGateway.Copilot = &cetirunv1alpha1.AIGatewayCopilotStatus{Models: []string{"copilot/gpt-5.5"}}
	agent.Annotations = map[string]string{cetirunv1alpha1.AnnotationCopilotModels: testCopilotModels}

	entries, def, _ := r.aiGatewayModelEntries(agent)
	if def != testSharedModelOne || len(entries) != 2 || entries[1].ID != "copilot/gpt-5.5" ||
		entries[1].ContextWindow != 400000 || entries[1].MaxTokens != 128000 || entries[0].ContextWindow != 1000000 {
		t.Fatalf("entries %+v default %q", entries, def)
	}
	agent.Spec.Model = "copilot/gpt-5.5"
	if _, def, _ := r.aiGatewayModelEntries(agent); def != "copilot/gpt-5.5" {
		t.Fatalf("the Agent's own available choice wins, got %q", def)
	}
	agent.Spec.Model = "not-available"
	r.AIGateway.DefaultModel = "copilot/gpt-5.5"
	if _, def, _ := r.aiGatewayModelEntries(agent); def != "copilot/gpt-5.5" {
		t.Fatalf("an unavailable choice falls back to the deployment default, got %q", def)
	}
	agent.Spec.ModelAccess = modelAccess(false, true)
	agent.Status.AIGateway.Copilot.Models = nil
	if entries, def, _ := r.aiGatewayModelEntries(agent); def != aiGatewaySignInRequiredModel || len(entries) != 1 {
		t.Fatalf("a Copilot-only Agent before sign-in gets the placeholder: %+v %q", entries, def)
	}
	agent.Spec.ModelAccess = modelAccess(false, false)
	if _, def, _ := r.aiGatewayModelEntries(agent); def != aiGatewayNoModelsModel {
		t.Fatalf("an Agent without sources gets the no-models placeholder, got %q", def)
	}
}

// manySharedModels adds count shared models, model-000 onwards, to the Gateway.
func manySharedModels(gateway *fakeAIGateway, count int) {
	for i := 0; i < count; i++ {
		gateway.shared = append(gateway.shared, aigateway.Deployment{
			ID: fmt.Sprintf("many-%03d", i), ModelName: fmt.Sprintf("model-%03d", i), AccessGroups: []string{aiGatewaySharedAccessGroup},
		})
	}
}

func TestAIGatewayModelEntriesKeepTheConfigurationWithinTheChartLimit(t *testing.T) {
	r, _, gateway := aiGatewayTestReconciler(t)
	manySharedModels(gateway, 300)
	r.refreshAIGatewayCatalogWithin(context.Background(), gateway, 0)
	agent := routedAgent()
	copilot := []string{"copilot/claude-sonnet-4.5", "copilot/gpt-5.5"}
	agent.Status.AIGateway.Copilot = &cetirunv1alpha1.AIGatewayCopilotStatus{Models: copilot}
	agent.Spec.Model = "model-299"
	r.AIGateway.DefaultModel = "model-298"

	entries, def, omitted := r.aiGatewayModelEntries(agent)
	if len(entries) != aiGatewayMaxModels {
		t.Fatalf("%d entries, want the limit %d", len(entries), aiGatewayMaxModels)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	// The owner's Copilot models all stay; the shared models fill the rest,
	// the Agent's own choice and the deployment default first, then by name.
	for _, want := range append([]string{"model-299", "model-298", "model-000", testSharedModelOne}, copilot...) {
		if !slices.Contains(ids, want) {
			t.Errorf("the configuration lacks %s", want)
		}
	}
	if def != "model-299" {
		t.Fatalf("the Agent's own choice stays its default, got %q", def)
	}
	if want := 301 + len(copilot) - aiGatewayMaxModels; len(omitted) != want {
		t.Fatalf("%d shared models left out, want %d", len(omitted), want)
	}
	for _, name := range omitted {
		if slices.Contains(ids, name) || name == "model-299" || name == "model-298" {
			t.Fatalf("%s is both listed and left out", name)
		}
	}
	if omitted[0] != "model-251" || omitted[len(omitted)-1] != "model-297" {
		t.Fatalf("the shared models past the limit in name order are left out, got %s..%s", omitted[0], omitted[len(omitted)-1])
	}
}

func TestAIGatewayMaxModelsMatchesTheAgentChart(t *testing.T) {
	raw, err := os.ReadFile("../../../cetirun-chart/values.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties struct {
			AIGateway struct {
				Properties struct {
					Models struct {
						MaxItems int `json:"maxItems"`
					} `json:"models"`
				} `json:"properties"`
			} `json:"aiGateway"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if got := schema.Properties.AIGateway.Properties.Models.MaxItems; got != aiGatewayMaxModels {
		t.Fatalf("the chart accepts %d models, the operator lists up to %d", got, aiGatewayMaxModels)
	}
}

// More shared models than the chart accepts must not block the Agent.
func TestReconcileInstallsAnAgentWithMoreSharedModelsThanTheChartAccepts(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayTestTeam(testAIGatewayTeam))
	manySharedModels(gateway, 300)
	chart := &chartHelm{}
	r.HelmClient = chart
	recorder := record.NewFakeRecorder(100)
	r.EventRecorder = recorder
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: agent.Name}}
	for i := 0; i < 3 && chart.installs == 0; i++ {
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}
	current := &cetirunv1alpha1.Agent{}
	if err := kube.Get(context.Background(), req.NamespacedName, current); err != nil {
		t.Fatal(err)
	}
	if chart.installs == 0 {
		t.Fatalf("the Agent was never installed: %+v", current.Status.Conditions)
	}
	if models := chart.lastValues["aiGateway"].(map[string]any)["models"].([]any); len(models) != aiGatewayMaxModels {
		t.Fatalf("%d models configured, want %d", len(models), aiGatewayMaxModels)
	}
	limited := false
	for len(recorder.Events) > 0 {
		if event := <-recorder.Events; strings.Contains(event, "AIGatewayModelsLimited") && strings.Contains(event, "45 shared models") {
			limited = true
		}
	}
	if !limited {
		t.Fatal("the Agent must report the shared models left out")
	}
}

func TestAIGatewayClientReadsMasterKeySecret(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, _ := aiGatewayTestReconciler(t, agent)
	r.aiGatewayClientFactory = nil
	if _, err := r.aiGatewayClient(context.Background()); err == nil || !strings.Contains(err.Error(), "master key Secret") {
		t.Fatalf("missing master key Secret must be reported, got %v", err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "cetirun-ai-gateway-auth", Namespace: "cetirun-system"},
		Data:       map[string][]byte{"master-key": []byte(" ")},
	}
	if err := kube.Create(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	if _, err := r.aiGatewayClient(context.Background()); err == nil {
		t.Fatal("an empty master key must be rejected")
	}
	secret.Data["master-key"] = []byte("sk-synthetic-master")
	if err := kube.Update(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	if client, err := r.aiGatewayClient(context.Background()); err != nil || client == nil {
		t.Fatalf("client from a valid master key Secret: %v", err)
	}
}

func routedAgent() *cetirunv1alpha1.Agent {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	agent.Status.AIGateway = &cetirunv1alpha1.AIGatewayStatus{
		Phase: cetirunv1alpha1.AIGatewayPhaseReady, KeyID: "0123456789ab", KeySecretName: aiGatewayKeySecretName, Routed: true,
		Models: []string{testSharedModelOne}, DefaultModel: testSharedModelOne,
	}
	return agent
}

func TestApplyAIGatewayValuesOverridesProviderForRoutedAgent(t *testing.T) {
	r, _, gateway := aiGatewayTestReconciler(t)
	r.refreshAIGatewayCatalogWithin(context.Background(), gateway, 0)
	agent := routedAgent()
	agent.Status.AIGateway.Copilot = &cetirunv1alpha1.AIGatewayCopilotStatus{Phase: cetirunv1alpha1.AIGatewayCopilotPhaseReady, Models: []string{"copilot/gpt-5.5"}}
	values := map[string]any{
		"secrets":       map[string]any{"existingSecret": "gateway-token", "provider": "anthropic", "model": "user-choice"},
		"networkPolicy": map[string]any{"istioNamespace": "aks-istio-system"},
	}
	if err := r.applyAIGatewayValues(values, agent); err != nil {
		t.Fatal(err)
	}
	secrets := values["secrets"].(map[string]any)
	if secrets["provider"] != "openai" || secrets["apiBase"] != testAIGatewayURL+"/v1" || secrets["model"] != testSharedModelOne ||
		secrets["authMode"] != "api_key" || secrets["existingSecret"] != "gateway-token" {
		t.Fatalf("unexpected secrets values %v", secrets)
	}
	gatewayValues := values["aiGateway"].(map[string]any)
	if gatewayValues["enabled"] != true || gatewayValues["keySecretName"] != aiGatewayKeySecretName {
		t.Fatalf("unexpected aiGateway values %v", gatewayValues)
	}
	models := gatewayValues["models"].([]any)
	if len(models) != 2 || models[0].(map[string]any)["id"] != testSharedModelOne || models[1].(map[string]any)["id"] != "copilot/gpt-5.5" {
		t.Fatalf("unexpected models %v", models)
	}
	proxy := gatewayValues["copilotProxy"].(map[string]any)
	if proxy["enabled"] != true || proxy["image"] != testProxyImage || proxy["tokenSecretName"] != cetirunv1alpha1.CopilotTokenSecretName ||
		proxy["masterKeySecretName"] != aiGatewayProxySecretName {
		t.Fatalf("unexpected proxy values %v", proxy)
	}
	policy := values["networkPolicy"].(map[string]any)
	if policy["gatewayNamespace"] != "cetirun-system" || policy["gatewayPort"] != 4000 || policy["istioNamespace"] != "aks-istio-system" {
		t.Fatalf("unexpected networkPolicy values %v", policy)
	}
	if values["podAnnotations"].(map[string]any)[aiGatewayKeyIDPodAnnotation] != "0123456789ab" {
		t.Fatal("a key change must roll the Pod")
	}
}

func TestApplyAIGatewayValuesLeavesUnroutedAgentAlone(t *testing.T) {
	for name, mutate := range map[string]func(*AgentReconciler, *cetirunv1alpha1.Agent){
		"not routed":       func(_ *AgentReconciler, a *cetirunv1alpha1.Agent) { a.Status.AIGateway.Routed = false },
		"feature disabled": func(r *AgentReconciler, _ *cetirunv1alpha1.Agent) { r.AIGateway.Enabled = false },
		"no key":           func(_ *AgentReconciler, a *cetirunv1alpha1.Agent) { a.Status.AIGateway.KeyID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			r := &AgentReconciler{AIGateway: aiGatewayTestConfig()}
			agent := routedAgent()
			mutate(r, agent)
			values := map[string]any{"secrets": map[string]any{"provider": "github_copilot", "model": "copilot-model"}}
			if err := r.applyAIGatewayValues(values, agent); err != nil {
				t.Fatal(err)
			}
			gatewayValues := values["aiGateway"].(map[string]any)
			if values["secrets"].(map[string]any)["provider"] != "github_copilot" || gatewayValues["enabled"] != false {
				t.Fatalf("unrouted Agent values changed: %v", values)
			}
			if _, ok := gatewayValues["models"]; ok {
				t.Fatal("charts without model support must not receive the model list")
			}
		})
	}
}

func TestAIGatewayRevision(t *testing.T) {
	disabled := &AgentReconciler{}
	if got := disabled.aiGatewayRevision(aiGatewayTestAgent(testAIGatewayTeam)); got != "" {
		t.Fatalf("a deployment without the feature must keep its hash, got %q", got)
	}
	r, _, gateway := aiGatewayTestReconciler(t)
	agent := routedAgent()
	routed := r.aiGatewayRevision(agent)
	agent.Status.AIGateway.KeyID = "ba9876543210"
	rotated := r.aiGatewayRevision(agent)
	if rotated == routed {
		t.Fatal("a new key must change the revision")
	}
	agent.Annotations = map[string]string{cetirunv1alpha1.AnnotationCopilotModels: `[{"id":"gpt-5.5"}]`}
	signedIn := r.aiGatewayRevision(agent)
	if signedIn == rotated {
		t.Fatal("a new Copilot model list must change the revision, so the next reconcile registers it")
	}
	r.refreshAIGatewayCatalogWithin(context.Background(), gateway, 0)
	withCatalog := r.aiGatewayRevision(agent)
	gateway.shared = append(gateway.shared, aigateway.Deployment{ID: "cfg-3", ModelName: "new-shared", AccessGroups: []string{aiGatewaySharedAccessGroup}})
	r.refreshAIGatewayCatalogWithin(context.Background(), gateway, 0)
	if r.aiGatewayRevision(agent) == withCatalog {
		t.Fatal("a new shared model must change the revision")
	}
	withNewModel := r.aiGatewayRevision(agent)
	gateway.shared[0].MaxInputTokens /= 2
	r.refreshAIGatewayCatalogWithin(context.Background(), gateway, 0)
	if r.aiGatewayRevision(agent) == withNewModel {
		t.Fatal("new limits for a shared model must change the revision, so they reach the Agent")
	}
	withNewLimits := r.aiGatewayRevision(agent)
	gateway.shared[0].MaxOutputTokens /= 2
	r.refreshAIGatewayCatalogWithin(context.Background(), gateway, 0)
	if r.aiGatewayRevision(agent) == withNewLimits {
		t.Fatal("a new output limit for a shared model must change the revision")
	}
	agent.Spec.ModelAccess = modelAccess(true, false)
	if r.aiGatewayRevision(agent) == withCatalog {
		t.Fatal("turning Copilot off must change the revision")
	}
}

func TestDeleteAIGatewayKeyRevokesKeyAndCopilotRoutes(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	value := mustKeyValue(t)
	agent.Status.AIGateway = &cetirunv1alpha1.AIGatewayStatus{
		Phase: cetirunv1alpha1.AIGatewayPhaseReady, KeyAlias: aiGatewayKeyAlias(agent),
		Copilot: &cetirunv1alpha1.AIGatewayCopilotStatus{Registered: []string{"recorded-route"}},
	}
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace(), keySecret(testAIGatewayUID, value))
	gateway.keys[aigateway.Token(value)] = &aigateway.Key{Alias: "renamed-by-an-administrator"}
	gateway.models["recorded-route"] = aigateway.ModelRequest{ID: "recorded-route", ModelName: "copilot/a", AccessGroups: []string{copilotGroup()}}
	gateway.models["unrecorded-route"] = aigateway.ModelRequest{ID: "unrecorded-route", ModelName: "copilot/b", AccessGroups: []string{copilotGroup()}}
	r.refreshAIGatewayCatalogWithin(context.Background(), gateway, 0)
	if err := r.deleteAIGatewayKey(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gateway.deletedTokens, []string{aigateway.Token(value)}) || !slices.Equal(gateway.deletedAliases, []string{aiGatewayKeyAlias(agent)}) {
		t.Fatalf("revocation must use the key and the alias: tokens=%v aliases=%v", gateway.deletedTokens, gateway.deletedAliases)
	}
	if len(gateway.keys) != 0 || len(gateway.models) != 0 {
		t.Fatalf("the key or a Copilot route survived: keys=%d routes=%v", len(gateway.keys), gateway.modelNames())
	}
	persisted := &cetirunv1alpha1.Agent{}
	if err := kube.Get(context.Background(), types.NamespacedName{Name: agent.Name}, persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Status.AIGateway == nil || persisted.Status.AIGateway.Phase != cetirunv1alpha1.AIGatewayPhaseDeleting {
		t.Fatal("the deletion gate was not persisted before revocation")
	}
}

// A route registered just before the operator stopped is in neither the
// status nor, right after a restart, the operator's catalog. Deletion lists
// the Gateway itself, and holds the Agent while it cannot.
func TestDeleteAIGatewayKeyRemovesCopilotRoutesTheStatusMissed(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	agent.Status.AIGateway = &cetirunv1alpha1.AIGatewayStatus{Phase: cetirunv1alpha1.AIGatewayPhaseReady, KeyAlias: aiGatewayKeyAlias(agent)}
	r, _, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	gateway.models["unrecorded-route"] = aigateway.ModelRequest{ID: "unrecorded-route", ModelName: "copilot/a", AccessGroups: []string{copilotGroup()}}
	gateway.models["other-agent-route"] = aigateway.ModelRequest{ID: "other-agent-route", ModelName: "copilot/a", AccessGroups: []string{"cetirun-copilot-another-agent"}}
	if _, known := r.aiGatewayCatalog.has("unrecorded-route"); known {
		t.Fatal("the test needs a catalog that was never listed")
	}
	if err := r.deleteAIGatewayKey(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gateway.deletedModels, []string{"unrecorded-route"}) {
		t.Fatalf("deleted routes %v, want only this Agent's unrecorded route", gateway.deletedModels)
	}

	held := aiGatewayTestAgent(testAIGatewayTeam)
	held.Status.AIGateway = &cetirunv1alpha1.AIGatewayStatus{Phase: cetirunv1alpha1.AIGatewayPhaseReady}
	r2, _, _ := aiGatewayTestReconciler(t, held, aiGatewayNamespace())
	failing := &failingListGateway{fakeAIGateway: newFakeAIGateway()}
	r2.aiGatewayClientFactory = func(context.Context) (aiGatewayKeyClient, error) { return failing, nil }
	if err := r2.deleteAIGatewayKey(context.Background(), held); err == nil {
		t.Fatal("deletion must wait while the Gateway cannot be listed")
	}
}

func TestDeleteAIGatewayKeyBlocksOnFailureAndProceedsOnlyWithoutAGateway(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	agent.Status.AIGateway = &cetirunv1alpha1.AIGatewayStatus{Phase: cetirunv1alpha1.AIGatewayPhaseReady}
	r, _, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	gateway.deleteErr = errors.New("synthetic outage")
	if err := r.deleteAIGatewayKey(context.Background(), agent); err == nil {
		t.Fatal("a failed revocation must block deletion")
	}

	// Nothing proves that an Agent without status never had a key, so with a
	// Gateway configured its alias is revoked, which is idempotent.
	unused := aiGatewayTestAgent(testAIGatewayTeam)
	r2, _, gateway2 := aiGatewayTestReconciler(t, unused)
	r2.AIGateway.Enabled = false
	if err := r2.deleteAIGatewayKey(context.Background(), unused); err != nil ||
		!slices.Equal(gateway2.deletedAliases, []string{aiGatewayKeyAlias(unused)}) {
		t.Fatalf("an Agent without status must still have its alias revoked: %v %v", err, gateway2.deletedAliases)
	}

	unconfigured := aiGatewayTestAgent(testAIGatewayTeam)
	unconfigured.Status.AIGateway = &cetirunv1alpha1.AIGatewayStatus{Phase: cetirunv1alpha1.AIGatewayPhaseReady}
	r3, _, _ := aiGatewayTestReconciler(t, unconfigured)
	r3.AIGateway = AIGatewayConfig{}
	if err := r3.deleteAIGatewayKey(context.Background(), unconfigured); err != nil {
		t.Fatalf("without any Gateway configuration deletion must proceed: %v", err)
	}
	select {
	case event := <-r3.EventRecorder.(*record.FakeRecorder).Events:
		if !strings.Contains(event, "AIGatewayKeyNotRevoked") {
			t.Fatalf("unexpected event %q", event)
		}
	default:
		t.Fatal("an unrevoked key must be reported")
	}
	// An Agent that never recorded a key has nothing to report.
	never := aiGatewayTestAgent(testAIGatewayTeam)
	r4, _, _ := aiGatewayTestReconciler(t, never)
	r4.AIGateway = AIGatewayConfig{}
	if err := r4.deleteAIGatewayKey(context.Background(), never); err != nil {
		t.Fatalf("without any Gateway configuration deletion must proceed: %v", err)
	}
	select {
	case event := <-r4.EventRecorder.(*record.FakeRecorder).Events:
		t.Fatalf("unexpected event %q", event)
	default:
	}
}

// The operator writes the key Secret and creates the Gateway key before it
// saves the Agent's status. A deletion that finds no status must still
// revoke that key while a Gateway is configured, even after the deployment
// turned the feature off: the namespace deletion removes the only record.
func TestDeleteAIGatewayKeyRevokesAKeyCreatedBeforeItsStatus(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	value := mustKeyValue(t)
	r, _, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace(), keySecret(testAIGatewayUID, value))
	r.AIGateway.Enabled = false
	gateway.keys[aigateway.Token(value)] = &aigateway.Key{Alias: aiGatewayKeyAlias(agent)}
	if err := r.deleteAIGatewayKey(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gateway.deletedTokens, []string{aigateway.Token(value)}) || len(gateway.keys) != 0 {
		t.Fatalf("the key must be revoked: tokens=%v keys=%d", gateway.deletedTokens, len(gateway.keys))
	}

	// A Secret of another Agent under the same name is not this Agent's key:
	// only this Agent's own alias is revoked.
	other := aiGatewayTestAgent(testAIGatewayTeam)
	r2, _, gateway2 := aiGatewayTestReconciler(t, other, aiGatewayNamespace(), keySecret("uid-of-another-agent", mustKeyValue(t)))
	r2.AIGateway.Enabled = false
	if err := r2.deleteAIGatewayKey(context.Background(), other); err != nil || len(gateway2.deletedTokens) != 0 ||
		!slices.Equal(gateway2.deletedAliases, []string{aiGatewayKeyAlias(other)}) {
		t.Fatalf("another Agent's Secret must not be revoked: %v %v %v", err, gateway2.deletedAliases, gateway2.deletedTokens)
	}

	// An Agent that was never provisioned has no namespace: finalizing it
	// must not need Secret access, which a restricted controller lacks. Its
	// alias is still revoked, which needs no Secret.
	sparse := aiGatewayTestAgent(testAIGatewayTeam)
	r3, _, gateway3 := aiGatewayTestReconciler(t, sparse)
	r3.AIGateway.Enabled = false
	r3.Client = interceptor.NewClient(r3.Client.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Secret); ok {
				return apierrors.NewForbidden(corev1.Resource("secrets"), key.Name, errors.New("no Secret access"))
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	if err := r3.deleteAIGatewayKey(context.Background(), sparse); err != nil ||
		!slices.Equal(gateway3.deletedAliases, []string{aiGatewayKeyAlias(sparse)}) {
		t.Fatalf("an Agent without a namespace must be finalized without reading Secrets: %v %v", err, gateway3.deletedAliases)
	}
}

// Neither the status nor the key Secret proves that the Gateway holds no key:
// the operator creates the key before it saves the status, and the Secret can
// be lost. The UID-derived alias is enough to revoke it, with key creation
// turned off too.
func TestDeleteAIGatewayKeyRevokesByAliasWithoutStatusOrSecret(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, _, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	r.AIGateway.Enabled = false
	gateway.keys[aigateway.Token(mustKeyValue(t))] = &aigateway.Key{Alias: aiGatewayKeyAlias(agent)}
	if err := r.deleteAIGatewayKey(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	if len(gateway.keys) != 0 || !slices.Equal(gateway.deletedAliases, []string{aiGatewayKeyAlias(agent)}) {
		t.Fatalf("the key must be revoked by its alias: keys=%d aliases=%v", len(gateway.keys), gateway.deletedAliases)
	}
}

func TestParseAIGatewayConfig(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	cfg, err := ParseAIGatewayConfig(env(nil))
	if err != nil || cfg.Enabled || cfg.configured() {
		t.Fatalf("empty configuration: %+v %v", cfg, err)
	}
	valid := map[string]string{
		"AI_GATEWAY_ENABLED": "true", "AI_GATEWAY_URL": testAIGatewayURL + "/", "AI_GATEWAY_MASTER_KEY_SECRET": "cetirun-ai-gateway-auth",
		"AI_GATEWAY_DEFAULT_BUDGET": "12.5", "AI_GATEWAY_DEFAULT_MODEL": "shared-model",
		"AI_GATEWAY_COPILOT_PROXY_IMAGE": "registry.example.com/cetirun-ai-gateway:0.1.1@sha256:" + strings.Repeat("a", 64),
	}
	cfg, err = ParseAIGatewayConfig(env(valid))
	if err != nil || !cfg.Enabled || cfg.URL != testAIGatewayURL || cfg.MasterKeySecretKey != "master-key" || cfg.DefaultBudget != 12.5 ||
		!strings.HasPrefix(cfg.CopilotProxyImage, "registry.example.com/") {
		t.Fatalf("valid configuration: %+v %v", cfg, err)
	}
	for name, override := range map[string][2]string{
		"external URL":       {"AI_GATEWAY_URL", "http://gateway.example.com:4000"},
		"https URL":          {"AI_GATEWAY_URL", "https://cetirun-ai-gateway.cetirun-system.svc.cluster.local:4000"},
		"URL with path":      {"AI_GATEWAY_URL", testAIGatewayURL + "/v1"},
		"URL without port":   {"AI_GATEWAY_URL", "http://cetirun-ai-gateway.cetirun-system.svc.cluster.local"},
		"no Secret":          {"AI_GATEWAY_MASTER_KEY_SECRET", ""},
		"no budget":          {"AI_GATEWAY_DEFAULT_BUDGET", ""},
		"negative budget":    {"AI_GATEWAY_DEFAULT_BUDGET", "-1"},
		"untagged image":     {"AI_GATEWAY_COPILOT_PROXY_IMAGE", "registry.example.com/cetirun-ai-gateway"},
		"image with a space": {"AI_GATEWAY_COPILOT_PROXY_IMAGE", "registry.example.com/x:1 --privileged"},
		"bad digest":         {"AI_GATEWAY_COPILOT_PROXY_IMAGE", "registry.example.com/x@sha256:123"},
		// NaN passes every comparison, so a key created with it would get no
		// budget at all; an infinite budget is no cap either.
		"NaN budget":      {"AI_GATEWAY_DEFAULT_BUDGET", "NaN"},
		"infinite budget": {"AI_GATEWAY_DEFAULT_BUDGET", "+Inf"},
		"Infinity budget": {"AI_GATEWAY_DEFAULT_BUDGET", "Infinity"},
	} {
		values := map[string]string{}
		for key, value := range valid {
			values[key] = value
		}
		values[override[0]] = override[1]
		if _, err := ParseAIGatewayConfig(env(values)); err == nil {
			t.Errorf("%s: invalid configuration accepted", name)
		}
	}
	registryPort := map[string]string{}
	for key, value := range valid {
		registryPort[key] = value
	}
	registryPort["AI_GATEWAY_COPILOT_PROXY_IMAGE"] = "registry.example.com:5000/cetirun-ai-gateway:0.1.1"
	if _, err := ParseAIGatewayConfig(env(registryPort)); err != nil {
		t.Errorf("a registry with a port is valid: %v", err)
	}
}

func TestCopilotBaseModel(t *testing.T) {
	priced := map[string]bool{"gpt-5.5": true, "claude-sonnet-4-5": true, "github_copilot/gpt-9": true,
		"xai/grok-code-fast-1": true, "vertex_ai/grok-code-fast-1": true, "aihubmix/grok-code-fast-1": true,
		"openrouter/x-ai/grok-9": true, "aihubmix/grok-9": true, "gemini/gemini-9-flash": true}
	for id, want := range map[string]string{
		"gpt-5.5": "gpt-5.5", "claude-sonnet-4.5": "claude-sonnet-4-5", "gpt-9": "github_copilot/gpt-9",
		// The vendor's own entry, never a reseller's, whatever sorts first.
		"grok-code-fast-1": "xai/grok-code-fast-1", "gemini-9-flash": "gemini/gemini-9-flash",
		// Priced only by resellers: not a list price.
		"grok-9": "", "unknown-1": "",
	} {
		got, ok := copilotBaseModel(id, priced)
		if got != want || ok != (want != "") {
			t.Errorf("%s: got %q %v, want %q", id, got, ok, want)
		}
	}
}

func TestParseCopilotModels(t *testing.T) {
	models, err := parseCopilotModels(`[{"id":"b"},{"id":"a","contextWindow":-5},{"id":"a"},{"id":"../x"},{"id":""}]`)
	if err != nil || len(models) != 2 || models[0].ID != "a" || models[0].ContextWindow != 0 || models[1].ID != "b" {
		t.Fatalf("models %+v err %v", models, err)
	}
	if _, err := parseCopilotModels("not json"); err == nil {
		t.Fatal("invalid JSON must be reported")
	}
	var many []cetirunv1alpha1.CopilotModel
	for i := 0; i < aiGatewayMaxCopilotModels+10; i++ {
		many = append(many, cetirunv1alpha1.CopilotModel{ID: fmt.Sprintf("m%03d", i)})
	}
	raw, _ := json.Marshal(many)
	if models, _ := parseCopilotModels(string(raw)); len(models) != aiGatewayMaxCopilotModels {
		t.Fatalf("the list must be bounded, got %d", len(models))
	}
}

func TestAIGatewayCatalogPublishesSharedModels(t *testing.T) {
	r, kube, gateway := aiGatewayTestReconciler(t)
	gateway.shared = append(gateway.shared,
		aigateway.Deployment{ID: "cfg-3", ModelName: testSharedModelOne, AccessGroups: []string{aiGatewaySharedAccessGroup}, MaxInputTokens: 200000},
	)
	r.publishAIGatewayCatalog(context.Background(), 0)
	configMap := &corev1.ConfigMap{}
	if err := kube.Get(context.Background(), types.NamespacedName{Namespace: "cetirun-system", Name: cetirunv1alpha1.AIGatewayCatalogConfigMap}, configMap); err != nil {
		t.Fatal(err)
	}
	var catalog cetirunv1alpha1.AIGatewayCatalog
	if err := json.Unmarshal([]byte(configMap.Data[cetirunv1alpha1.AIGatewayCatalogKey]), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.SharedModels) != 1 || catalog.SharedModels[0].Name != testSharedModelOne || catalog.SharedModels[0].ContextWindow != 200000 {
		t.Fatalf("the catalog lists each shared name once with the smallest limits: %+v", catalog)
	}
	resourceVersion := configMap.ResourceVersion
	r.publishAIGatewayCatalog(context.Background(), 0)
	if err := kube.Get(context.Background(), types.NamespacedName{Namespace: "cetirun-system", Name: cetirunv1alpha1.AIGatewayCatalogConfigMap}, configMap); err != nil {
		t.Fatal(err)
	}
	if configMap.ResourceVersion != resourceVersion {
		t.Fatal("an unchanged catalog must not be rewritten")
	}
}

func TestAIGatewayCatalogQueuesAgentsWhoseCopilotRoutesAreGone(t *testing.T) {
	copilotOnly := func(name, uid string) (*cetirunv1alpha1.Agent, string) {
		agent := routedAgent()
		agent.Name, agent.UID = name, types.UID(uid)
		shared, copilot := false, true
		agent.Spec.ModelAccess = &cetirunv1alpha1.ModelAccessSpec{SharedModels: &shared, GitHubCopilot: &copilot}
		route := copilotRegistrationID(agent, "gpt-5.5", "0123abcd")
		agent.Status.AIGateway.Models = []string{"copilot/gpt-5.5"}
		agent.Status.AIGateway.DefaultModel = "copilot/gpt-5.5"
		agent.Status.AIGateway.Copilot = &cetirunv1alpha1.AIGatewayCopilotStatus{
			Phase: cetirunv1alpha1.AIGatewayCopilotPhaseReady, AccessGroup: copilotAccessGroup(agent),
			Registered: []string{route}, Models: []string{"copilot/gpt-5.5"},
		}
		return agent, route
	}
	lost, lostRoute := copilotOnly("lost", "uid-lost")
	kept, keptRoute := copilotOnly("kept", "uid-kept")
	r, _, gateway := aiGatewayTestReconciler(t, lost, kept, routedAgent())
	gateway.models[keptRoute] = aigateway.ModelRequest{ID: keptRoute, ModelName: "copilot/gpt-5.5", AccessGroups: []string{copilotAccessGroup(kept)}}
	r.aiGatewayEvents = make(chan event.GenericEvent, 8)
	queued := func() []string {
		var names []string
		for {
			select {
			case queuedEvent := <-r.aiGatewayEvents:
				names = append(names, queuedEvent.Object.GetName())
			default:
				slices.Sort(names)
				return names
			}
		}
	}

	// The first listing is a catalog change, which queues the Agent with
	// shared models, and it shows that one route is gone.
	r.publishAIGatewayCatalog(context.Background(), 0)
	if got := queued(); !slices.Equal(got, []string{"alice", "lost"}) {
		t.Fatalf("first pass queued %v, want the shared-model Agent and the Agent that lost its route", got)
	}
	// The catalog is unchanged now: only the Agent without its route is
	// queued, again on every pass until the route is back.
	r.publishAIGatewayCatalog(context.Background(), 0)
	if got := queued(); !slices.Equal(got, []string{"lost"}) {
		t.Fatalf("second pass queued %v, want only the Agent that lost its route", got)
	}
	gateway.models[lostRoute] = aigateway.ModelRequest{ID: lostRoute, ModelName: "copilot/gpt-5.5", AccessGroups: []string{copilotAccessGroup(lost)}}
	r.publishAIGatewayCatalog(context.Background(), 0)
	if got := queued(); len(got) != 0 {
		t.Fatalf("queued %v once the route was back", got)
	}
}

// chartHelm renders the real agent chart so the test covers chart support
// detection and the rendered wiring, not just the values map.
type chartHelm struct {
	installs   int
	lastValues map[string]any
	uninstalls int
}

func (h *chartHelm) Prepare(_ string, values map[string]any, chartVersion string) (*helm.PreparedRelease, error) {
	chrt, err := loader.Load("../../../cetirun-chart")
	if err != nil {
		return nil, err
	}
	chrt.Metadata.Version = chartVersion
	return helm.PrepareChart(chrt, values)
}

func (h *chartHelm) Refresh(prepared *helm.PreparedRelease, values map[string]any) (*helm.PreparedRelease, error) {
	return prepared.Rebuild(values)
}

func (h *chartHelm) InstallOrUpgrade(_, _ string, prepared *helm.PreparedRelease) error {
	h.installs++
	h.lastValues = prepared.Values
	return nil
}

func (*chartHelm) CustomApplied(string, string, *helm.PreparedRelease) (bool, error) {
	return false, nil
}

func (h *chartHelm) Uninstall(string, string) error {
	h.uninstalls++
	return nil
}

// hasSignInHelperRoute reports whether the Agent's VirtualService routes to
// an in-agent sign-in helper.
func hasSignInHelperRoute(agent *cetirunv1alpha1.Agent) bool {
	for _, route := range buildVirtualService(agent, "example.com", "").Spec.Http {
		for _, match := range route.Match {
			if strings.Contains(match.GetUri().GetPrefix(), "cetirun-auth") {
				return true
			}
		}
	}
	return false
}

// With the Gateway disabled, an OpenClaw chart that carries no in-agent
// sign-in helper must not get a route to one, and the Agent must report why
// it has no model sign-in.
func TestReconcileWithoutTheGatewayRoutesToNoMissingSignInHelper(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayTestTeam(testAIGatewayTeam))
	r.AIGateway.Enabled = false
	chart := &chartHelm{}
	r.HelmClient = chart
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: agent.Name}}
	for i := 0; i < 3 && chart.installs == 0; i++ {
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}
	if chart.installs == 0 {
		t.Fatal("the Agent was never installed")
	}
	if len(gateway.generated) != 0 {
		t.Fatal("a disabled Gateway issues no key")
	}
	for _, port := range chart.lastValues["runtime"].(map[string]any)["ports"].([]any) {
		if port.(map[string]any)["name"] == "cetirun-auth" {
			t.Fatal("this OpenClaw chart carries no in-agent sign-in helper")
		}
	}
	current := &cetirunv1alpha1.Agent{}
	if err := kube.Get(context.Background(), req.NamespacedName, current); err != nil {
		t.Fatal(err)
	}
	if current.Status.SignInHelper == nil || *current.Status.SignInHelper {
		t.Fatalf("recorded sign-in helper %v, want false", current.Status.SignInHelper)
	}
	if hasSignInHelperRoute(current) {
		t.Fatal("the VirtualService must not route to a helper the workload does not serve")
	}
	condition := meta.FindStatusCondition(current.Status.Conditions, cetirunv1alpha1.ConditionAIGatewayReady)
	if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != aiGatewayReasonDisabled {
		t.Fatalf("the Agent must report that its models need the Gateway, got %+v", condition)
	}
}

// The route follows what the operator recorded at the last render; an Agent
// not yet recorded keeps the route it had, which ran wherever the Gateway
// did not replace the helper.
func TestSignInHelperRouteFollowsTheRecordedWorkload(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	if !hasSignInHelperRoute(agent) {
		t.Fatal("an unrouted Agent not yet recorded keeps its helper route")
	}
	agent.Status.AIGateway = &cetirunv1alpha1.AIGatewayStatus{Routed: true}
	if hasSignInHelperRoute(agent) {
		t.Fatal("a routed Agent not yet recorded has no helper route")
	}
	agent.Status.AIGateway = nil
	without, with := false, true
	agent.Status.SignInHelper = &without
	if hasSignInHelperRoute(agent) {
		t.Fatal("no route to a helper the workload does not serve")
	}
	agent.Status.SignInHelper = &with
	if !hasSignInHelperRoute(agent) {
		t.Fatal("a workload serving the helper keeps its route")
	}
}

func TestReconcileRoutesAgentWithProxyAndRevokesOnDelete(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayTestTeam(testAIGatewayTeam))
	chart := &chartHelm{}
	r.HelmClient = chart
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: agent.Name}}
	for i := 0; i < 3 && chart.installs == 0; i++ {
		if _, err := r.Reconcile(context.Background(), req); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}
	if chart.installs == 0 {
		t.Fatal("the Agent was never installed")
	}
	if len(gateway.generated) != 1 {
		t.Fatalf("generated %d keys, want 1", len(gateway.generated))
	}
	secrets := chart.lastValues["secrets"].(map[string]any)
	if secrets["provider"] != "openai" || secrets["apiBase"] != testAIGatewayURL+"/v1" || secrets["model"] != testSharedModelOne {
		t.Fatalf("installed values are not routed through the Gateway: %v", secrets)
	}
	gatewayValues := chart.lastValues["aiGateway"].(map[string]any)
	if gatewayValues["enabled"] != true || gatewayValues["copilotProxy"].(map[string]any)["enabled"] != true {
		t.Fatalf("chart wiring for the key and proxy is off: %v", gatewayValues)
	}
	runtimeValues := chart.lastValues["runtime"].(map[string]any)
	for _, port := range runtimeValues["ports"].([]any) {
		if port.(map[string]any)["name"] == "cetirun-auth" {
			t.Fatal("an Agent on the Gateway carries no in-agent sign-in helper port")
		}
	}
	config := runtimeValues["configFiles"].(map[string]any)["openclaw.json"].(string)
	if !strings.Contains(config, `"openai/`+testSharedModelOne+`"`) || !strings.Contains(config, `"contextWindow": 1000000`) {
		t.Fatalf("the native configuration lacks the Gateway models:\n%s", config)
	}
	current := &cetirunv1alpha1.Agent{}
	if err := kube.Get(context.Background(), req.NamespacedName, current); err != nil {
		t.Fatal(err)
	}
	if current.Status.AIGateway == nil || !current.Status.AIGateway.Routed || current.Status.AIGateway.Phase != cetirunv1alpha1.AIGatewayPhaseReady {
		t.Fatalf("persisted status %+v", current.Status.AIGateway)
	}
	if current.Status.SignInHelper == nil || *current.Status.SignInHelper {
		t.Fatalf("an Agent on the Gateway serves no in-agent sign-in helper, recorded %v", current.Status.SignInHelper)
	}
	vs := buildVirtualService(current, "example.com", "")
	for _, route := range vs.Spec.Http {
		for _, match := range route.Match {
			if prefix := match.GetUri().GetPrefix(); strings.Contains(prefix, "cetirun-auth") {
				t.Fatalf("an Agent on the Gateway must not route to an in-agent helper: %s", prefix)
			}
		}
	}

	if err := kube.Delete(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(gateway.keys) != 0 || !slices.Contains(gateway.deletedAliases, aiGatewayKeyAlias(agent)) {
		t.Fatalf("the key was not revoked on delete: keys=%d aliases=%v", len(gateway.keys), gateway.deletedAliases)
	}
	if chart.uninstalls != 1 {
		t.Fatal("the release was not uninstalled after revocation")
	}
	if err := kube.Get(context.Background(), req.NamespacedName, current); !apierrors.IsNotFound(err) {
		t.Fatalf("the Agent was not released after revocation: %v", err)
	}
}

func TestReconcileDoesNotStartRoutedAgentWithoutKey(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, _, gateway := aiGatewayTestReconciler(t, agent, aiGatewayTestTeam(testAIGatewayTeam))
	gateway.getErr = errors.New("synthetic outage")
	chart := &chartHelm{}
	r.HelmClient = chart
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: agent.Name}}
	var result ctrl.Result
	for i := 0; i < 3; i++ {
		var err error
		if result, err = r.Reconcile(context.Background(), req); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}
	if chart.installs != 0 || result.RequeueAfter != 30*time.Second {
		t.Fatalf("a routed Agent must wait for its key: installs=%d result=%+v", chart.installs, result)
	}
}

func TestAIGatewayVerificationTimeMovesOnlyWhenDue(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, _, _ := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	first := agent.Status.AIGateway.LastVerifiedTime
	if first == nil {
		t.Fatal("the first pass records the verification")
	}
	// A pass that was not due, such as one while the Agent is still
	// provisioning, must not change the status: a changed status re-queues
	// the Agent at once.
	earlier := metav1.NewTime(time.Now().Add(-time.Minute).Truncate(time.Second))
	agent.Status.AIGateway.LastVerifiedTime = &earlier
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	if !agent.Status.AIGateway.LastVerifiedTime.Equal(&earlier) {
		t.Fatalf("verification time moved on a pass that was not due: %v", agent.Status.AIGateway.LastVerifiedTime)
	}
	stale := metav1.NewTime(time.Now().Add(-2 * aiGatewayVerificationInterval))
	agent.Status.AIGateway.LastVerifiedTime = &stale
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	if !agent.Status.AIGateway.LastVerifiedTime.After(stale.Time) {
		t.Fatal("a due verification must record its time")
	}
}

// An Agent queued because the Gateway lost its Copilot routes must not take
// the lightweight pass, which skips the Gateway until the next verification.
func TestLostCopilotRouteMakesTheFullReconcileDue(t *testing.T) {
	agent := routedAgent()
	now := metav1.Now()
	route := copilotRegistrationID(agent, "gpt-5.5", "0123abcd")
	agent.Status.AIGateway.LastVerifiedTime = &now
	agent.Status.AIGateway.Copilot = &cetirunv1alpha1.AIGatewayCopilotStatus{
		Phase: cetirunv1alpha1.AIGatewayCopilotPhaseReady, AccessGroup: copilotAccessGroup(agent),
		Registered: []string{route}, Models: []string{"copilot/gpt-5.5"},
	}
	r, _, gateway := aiGatewayTestReconciler(t, agent)
	if r.aiGatewayReconcileDue(agent, time.Now()) {
		t.Fatal("a fresh verification is not due while nothing is known about the routes")
	}
	gateway.models[route] = aigateway.ModelRequest{ID: route, ModelName: "copilot/gpt-5.5", AccessGroups: []string{copilotAccessGroup(agent)}}
	r.refreshAIGatewayCatalogWithin(context.Background(), gateway, 0)
	if r.aiGatewayReconcileDue(agent, time.Now()) {
		t.Fatal("an Agent whose routes the Gateway has waits for its verification")
	}
	delete(gateway.models, route)
	r.refreshAIGatewayCatalogWithin(context.Background(), gateway, 0)
	if !r.aiGatewayReconcileDue(agent, time.Now()) {
		t.Fatal("an Agent whose route the Gateway lost must get the full reconcile now")
	}
	r.AIGateway.Enabled = false
	if r.aiGatewayReconcileDue(agent, time.Now()) {
		t.Fatal("nothing is due with the Gateway off")
	}
}

// The whole path of a lost route: the catalog refresh shows it gone, and the
// Agent's next reconcile adds it back although its verification is not due.
func TestReconcileRestoresLostCopilotRoutesBeforeTheVerification(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayTestTeam(testAIGatewayTeam))
	r.HelmClient = &chartHelm{}
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: agent.Name}}
	current := func() *cetirunv1alpha1.Agent {
		t.Helper()
		got := &cetirunv1alpha1.Agent{}
		if err := kube.Get(ctx, req.NamespacedName, got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	reconcile := func(times int) {
		t.Helper()
		for i := 0; i < times; i++ {
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatalf("reconcile: %v", err)
			}
		}
	}
	reconcile(3)
	signedIn := current()
	signIn(t, kube, signedIn, testCopilotModels)
	if err := kube.Update(ctx, signedIn); err != nil {
		t.Fatal(err)
	}
	if err := kube.Create(ctx, readyProxy(1)); err != nil {
		t.Fatal(err)
	}
	reconcile(3)
	routes := len(gateway.models)
	if routes == 0 {
		t.Fatalf("no Copilot routes registered: %+v", current().Status.AIGateway.Copilot)
	}
	// Only a Ready Agent qualifies for the lightweight pass; the fake Helm
	// client starts no Pods, so mark it Ready as the workload would.
	ready := current()
	ready.Status.Phase = cetirunv1alpha1.PhaseReady
	if err := kube.Status().Update(ctx, ready); err != nil {
		t.Fatal(err)
	}
	reconcile(1)
	verified := current().Status.AIGateway.LastVerifiedTime

	// The Gateway loses the routes, and the catalog refresh sees it.
	gateway.models = map[string]aigateway.ModelRequest{}
	r.refreshAIGatewayCatalogWithin(ctx, gateway, 0)
	reconcile(1)
	if len(gateway.models) != routes {
		t.Fatalf("the reconcile restored %d of %d routes", len(gateway.models), routes)
	}
	if after := current().Status.AIGateway.LastVerifiedTime; !after.Equal(verified) {
		t.Fatalf("restoring the routes is not a verification, yet its time moved: %v -> %v", verified, after)
	}
}

// A lost proxy key Secret gets a new key, which the Gateway's routes carry at
// once. The running proxy read the old key when it started, so the Agent's
// chart must restart it.
func TestReconcileRestartsTheCopilotProxyWithANewMasterKey(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayTestTeam(testAIGatewayTeam))
	chart := &chartHelm{}
	r.HelmClient = chart
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: agent.Name}}
	current := func() *cetirunv1alpha1.Agent {
		t.Helper()
		got := &cetirunv1alpha1.Agent{}
		if err := kube.Get(ctx, req.NamespacedName, got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	reconcile := func(times int) {
		t.Helper()
		for i := 0; i < times; i++ {
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatalf("reconcile: %v", err)
			}
		}
	}
	proxyKeyID := func() any {
		t.Helper()
		return chart.lastValues["aiGateway"].(map[string]any)["copilotProxy"].(map[string]any)[aiGatewayChartProxyKeyValue]
	}
	reconcile(3)
	signedIn := current()
	signIn(t, kube, signedIn, testCopilotModels)
	if err := kube.Update(ctx, signedIn); err != nil {
		t.Fatal(err)
	}
	if err := kube.Create(ctx, readyProxy(1)); err != nil {
		t.Fatal(err)
	}
	reconcile(3)
	before := current().Status.AIGateway.Copilot.ProxyKeyID
	if before == "" || proxyKeyID() != before || len(gateway.models) == 0 {
		t.Fatalf("the chart must get the key ID once the routes exist: status %q values %v routes %d", before, proxyKeyID(), len(gateway.models))
	}
	installs := chart.installs

	// The proxy's key Secret is lost; the next verification finds it.
	if err := kube.Delete(ctx, readSecret(t, kube, aiGatewayProxySecretName)); err != nil {
		t.Fatal(err)
	}
	due := current()
	stale := metav1.NewTime(time.Now().Add(-2 * aiGatewayVerificationInterval))
	due.Status.AIGateway.LastVerifiedTime = &stale
	if err := kube.Status().Update(ctx, due); err != nil {
		t.Fatal(err)
	}
	reconcile(1)
	after := current().Status.AIGateway.Copilot.ProxyKeyID
	if after == "" || after == before {
		t.Fatalf("a new key must get a new ID: %q -> %q", before, after)
	}
	if chart.installs == installs || proxyKeyID() != after {
		t.Fatalf("the proxy must be restarted with the new key: installs %d -> %d, values carry %v", installs, chart.installs, proxyKeyID())
	}
	key := string(readSecret(t, kube, aiGatewayProxySecretName).Data[aiGatewayProxySecretField])
	if aigateway.Token(key)[:aiGatewayKeyIDLength] != after {
		t.Fatal("the ID must identify the key in the Secret")
	}
	for id, route := range gateway.models {
		if route.APIKey != key {
			t.Fatalf("route %s still carries the old key", id)
		}
	}
}

// New limits for a shared model reach the Agent's configuration although the
// model keeps its name.
func TestReconcileRollsTheAgentWhenASharedModelsLimitsChange(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayTestTeam(testAIGatewayTeam))
	chart := &chartHelm{}
	r.HelmClient = chart
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: agent.Name}}
	reconcile := func(times int) {
		t.Helper()
		for i := 0; i < times; i++ {
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatalf("reconcile: %v", err)
			}
		}
	}
	contextWindow := func() int64 {
		t.Helper()
		for _, item := range chart.lastValues["aiGateway"].(map[string]any)["models"].([]any) {
			if model := item.(map[string]any); model["id"] == testSharedModelOne {
				// Helm's coalescing may turn the number into a float.
				switch value := model["contextWindow"].(type) {
				case int64:
					return value
				case float64:
					return int64(value)
				}
				t.Fatalf("context window %v (%T)", model["contextWindow"], model["contextWindow"])
			}
		}
		t.Fatal("the shared model is not configured")
		return 0
	}
	reconcile(3)
	// Only a Ready Agent qualifies for the lightweight pass; the fake Helm
	// client starts no Pods, so mark it Ready as the workload would.
	ready := &cetirunv1alpha1.Agent{}
	if err := kube.Get(ctx, req.NamespacedName, ready); err != nil {
		t.Fatal(err)
	}
	ready.Status.Phase = cetirunv1alpha1.PhaseReady
	if err := kube.Status().Update(ctx, ready); err != nil {
		t.Fatal(err)
	}
	reconcile(1)
	if got := contextWindow(); got != 1000000 {
		t.Fatalf("context window %d, want the catalog's", got)
	}
	installs := chart.installs

	// An administrator lowers the model's context window in the Gateway.
	gateway.shared[0].MaxInputTokens = 400000
	r.refreshAIGatewayCatalogWithin(ctx, gateway, 0)
	reconcile(1)
	if chart.installs == installs || contextWindow() != 400000 {
		t.Fatalf("the new limit must reach the Agent: installs %d -> %d, context window %d", installs, chart.installs, contextWindow())
	}
}

func TestAIGatewayCopilotPendingKeepsReconciling(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	now := metav1.Now()
	agent.Status.AIGateway = &cetirunv1alpha1.AIGatewayStatus{Phase: cetirunv1alpha1.AIGatewayPhaseReady, LastVerifiedTime: &now}
	if aiGatewayVerificationDue(agent, true, time.Now()) {
		t.Fatal("a fresh key verification is not due")
	}
	for phase, want := range map[string]bool{
		cetirunv1alpha1.AIGatewayCopilotPhaseSignInRequired: false,
		cetirunv1alpha1.AIGatewayCopilotPhaseStarting:       true,
		cetirunv1alpha1.AIGatewayCopilotPhaseDegraded:       true,
		cetirunv1alpha1.AIGatewayCopilotPhaseReady:          false,
	} {
		agent.Status.AIGateway.Copilot = &cetirunv1alpha1.AIGatewayCopilotStatus{Phase: phase}
		if got := aiGatewayCopilotPending(agent, true); got != want {
			t.Errorf("phase %s: pending=%v, want %v", phase, got, want)
		}
		if aiGatewayCopilotPending(agent, false) {
			t.Errorf("phase %s: nothing is pending with the Gateway off", phase)
		}
	}
}

// Changing only the deployment's default model must reach the Agents whose
// default it decides, rather than wait for the next key verification, and
// must leave Agents with their own usable model alone.
func TestAIGatewayRevisionFollowsTheDeploymentDefaultModel(t *testing.T) {
	r, _, _ := aiGatewayTestReconciler(t)
	revisionWith := func(agent *cetirunv1alpha1.Agent, defaultModel string) string {
		r.AIGateway.DefaultModel = defaultModel
		return r.aiGatewayRevision(agent)
	}
	unnamed := routedAgent()
	if revisionWith(unnamed, "model-a") == revisionWith(unnamed, "model-b") {
		t.Fatal("an Agent that names no model must follow the deployment default")
	}
	unavailable := routedAgent()
	unavailable.Spec.Model = "no-longer-offered"
	if revisionWith(unavailable, "model-a") == revisionWith(unavailable, "model-b") {
		t.Fatal("an Agent whose own model is unavailable must follow the deployment default")
	}
	chosen := routedAgent()
	chosen.Spec.Model = testSharedModelOne
	if revisionWith(chosen, "model-a") != revisionWith(chosen, "model-b") {
		t.Fatal("an Agent with its own usable model must not be reconciled for the deployment default")
	}
}

func TestAIGatewayRevisionFollowsEverySignIn(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, _, _ := aiGatewayTestReconciler(t, agent)
	agent.Annotations = map[string]string{cetirunv1alpha1.AnnotationCopilotModels: testCopilotModels}
	before := r.aiGatewayRevision(agent)
	agent.Annotations[cetirunv1alpha1.AnnotationCopilotSignedInAt] = "2026-10-01T08:00:00Z"
	first := r.aiGatewayRevision(agent)
	agent.Annotations[cetirunv1alpha1.AnnotationCopilotSignedInAt] = "2026-10-01T08:05:00Z"
	second := r.aiGatewayRevision(agent)
	if before == first || first == second {
		t.Fatal("a sign-in with an unchanged model list must still change the revision")
	}
}

// A Copilot model list is used only with the token of its sign-in: the
// Portal records the same generation with both. Models of another sign-in are
// awaited while a sign-in completes, then no longer used, and the routes go,
// so the proxy is never asked for models its token's account may not have.
func TestReconcileAIGatewayCopilotUsesModelsOnlyWithTheirSignIn(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace(), readyProxy(1))
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	storeToken := func(generation string, storedAt time.Time) {
		t.Helper()
		secret := readSecret(t, kube, cetirunv1alpha1.CopilotTokenSecretName)
		secret.Data = map[string][]byte{cetirunv1alpha1.CopilotTokenSecretKey: []byte("gho_" + generation)}
		secret.Annotations = map[string]string{
			cetirunv1alpha1.AnnotationCopilotSignInGeneration: generation,
			cetirunv1alpha1.AnnotationCopilotTokenStoredAt:    storedAt.UTC().Format(time.RFC3339),
		}
		if err := kube.Update(context.Background(), secret); err != nil {
			t.Fatal(err)
		}
	}
	recordModels := func(generation, models string) {
		agent.Annotations = map[string]string{
			cetirunv1alpha1.AnnotationCopilotModels:           models,
			cetirunv1alpha1.AnnotationCopilotSignInGeneration: generation,
		}
	}
	reconcile := func() *cetirunv1alpha1.AIGatewayCopilotStatus {
		t.Helper()
		if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
			t.Fatal(err)
		}
		return agent.Status.AIGateway.Copilot
	}

	storeToken("first", time.Now())
	recordModels("first", `[{"id":"gpt-5.5"}]`)
	if copilot := reconcile(); copilot.Phase != cetirunv1alpha1.AIGatewayCopilotPhaseReady || !slices.Equal(gateway.modelNames(), []string{"copilot/gpt-5.5"}) {
		t.Fatalf("a consistent sign-in registers its models: %+v %v", copilot, gateway.modelNames())
	}

	// A newer sign-in stored its token; its models follow. The routes stay
	// as they are meanwhile.
	storeToken("second", time.Now())
	if copilot := reconcile(); copilot.Phase != cetirunv1alpha1.AIGatewayCopilotPhaseStarting || copilot.Reason != "SignInCompleting" ||
		!slices.Equal(gateway.modelNames(), []string{"copilot/gpt-5.5"}) {
		t.Fatalf("models of the previous sign-in are awaited, not dropped yet: %+v %v", copilot, gateway.modelNames())
	}

	// They never came: the models on the Agent belong to another sign-in
	// than the stored token, and are not used.
	storeToken("second", time.Now().Add(-copilotSignInGrace-time.Minute))
	if copilot := reconcile(); copilot.Phase != cetirunv1alpha1.AIGatewayCopilotPhaseDegraded || copilot.Reason != "SignInIncomplete" ||
		len(gateway.models) != 0 || len(copilot.Models) != 0 {
		t.Fatalf("models of another sign-in must not be used: %+v %v", copilot, gateway.modelNames())
	}

	// The second sign-in's models arrive.
	recordModels("second", `[{"id":"claude-sonnet-4.5"}]`)
	if copilot := reconcile(); copilot.Phase != cetirunv1alpha1.AIGatewayCopilotPhaseReady ||
		!slices.Equal(gateway.modelNames(), []string{"copilot/claude-sonnet-4.5"}) {
		t.Fatalf("the models of the stored sign-in are registered: %+v %v", copilot, gateway.modelNames())
	}
}

func TestCopilotTokenIsReadPastTheCache(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, _ := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	if signIn, err := r.ensureCopilotTokenSecret(context.Background(), agent); err != nil || signIn.signedIn {
		t.Fatalf("first pass creates an empty Secret: signIn=%+v err=%v", signIn, err)
	}
	// The cache still shows the empty Secret; the API server has the token.
	fresh := readSecret(t, kube, cetirunv1alpha1.CopilotTokenSecretName).DeepCopy()
	fresh.Data = map[string][]byte{cetirunv1alpha1.CopilotTokenSecretKey: []byte("gho_synthetic")}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	r.APIReader = fake.NewClientBuilder().WithScheme(scheme).WithObjects(fresh).Build()
	if signIn, err := r.ensureCopilotTokenSecret(context.Background(), agent); err != nil || !signIn.signedIn {
		t.Fatalf("the token written by the Portal must be seen: signIn=%+v err=%v", signIn, err)
	}
}

func TestAIGatewayCatalogLeavesOutUnusableSharedNames(t *testing.T) {
	var catalog aiGatewayCatalog
	atLimit := strings.Repeat("m", aiGatewayModelNameLimit)
	// The chart counts characters, not bytes.
	multiByteAtLimit := strings.Repeat("é", aiGatewayModelNameLimit)
	changed, skipped, _ := catalog.update([]aigateway.Deployment{
		{ID: "a", ModelName: "gpt-6", AccessGroups: []string{aiGatewaySharedAccessGroup}},
		{ID: "b", ModelName: "openai/*", AccessGroups: []string{aiGatewaySharedAccessGroup}},
		{ID: "c", ModelName: "copilot/gpt-5.5", AccessGroups: []string{aiGatewaySharedAccessGroup}},
		{ID: "d", ModelName: "has space", AccessGroups: []string{aiGatewaySharedAccessGroup}},
		{ID: "e", ModelName: atLimit, AccessGroups: []string{aiGatewaySharedAccessGroup}},
		{ID: "f", ModelName: multiByteAtLimit, AccessGroups: []string{aiGatewaySharedAccessGroup}},
		{ID: "g", ModelName: atLimit + "m", AccessGroups: []string{aiGatewaySharedAccessGroup}},
	}, false, time.Now())
	shared, _ := catalog.shared()
	if !changed || len(shared) != 3 || shared[0].Name != "gpt-6" || shared[1].Name != atLimit ||
		shared[2].Name != multiByteAtLimit || len(skipped) != 4 || !slices.Contains(skipped, atLimit+"m") {
		t.Fatalf("shared=%d %v skipped=%d changed=%v", len(shared), shared, len(skipped), changed)
	}
}

func catalogNames(models []aiGatewayCatalogModel) []string {
	names := make([]string, 0, len(models))
	for _, model := range models {
		names = append(names, model.Name)
	}
	return names
}

// The Gateway refuses every request for an unpriced model while it blocks
// them, so the catalog does not offer such a model then, and offers it
// otherwise. Like LiteLLM, a name counts as priced when any deployment
// behind it is.
func TestAIGatewayCatalogLeavesOutUnpricedSharedModelsWhileTheGatewayBlocksThem(t *testing.T) {
	deployments := []aigateway.Deployment{
		{ID: "a", ModelName: "priced", AccessGroups: []string{aiGatewaySharedAccessGroup}, Priced: true},
		{ID: "b", ModelName: "unpriced", AccessGroups: []string{aiGatewaySharedAccessGroup}},
		{ID: "c", ModelName: "unpriced", AccessGroups: []string{aiGatewaySharedAccessGroup}},
		{ID: "d", ModelName: "mixed", AccessGroups: []string{aiGatewaySharedAccessGroup}},
		{ID: "e", ModelName: "mixed", AccessGroups: []string{"cetirun-admin"}, Priced: true},
	}
	var blocking aiGatewayCatalog
	_, _, unpriced := blocking.update(deployments, true, time.Now())
	shared, _ := blocking.shared()
	if names := catalogNames(shared); !slices.Equal(names, []string{"mixed", "priced"}) || !slices.Equal(unpriced, []string{"unpriced"}) {
		t.Fatalf("while the Gateway blocks unpriced models: shared %v, left out %v", names, unpriced)
	}
	var allowing aiGatewayCatalog
	_, _, unpriced = allowing.update(deployments, false, time.Now())
	shared, _ = allowing.shared()
	if names := catalogNames(shared); !slices.Equal(names, []string{"mixed", "priced", "unpriced"}) || len(unpriced) != 0 {
		t.Fatalf("while the Gateway serves unpriced models: shared %v, left out %v", names, unpriced)
	}
}

// An administrator can change the setting in the Gateway's console at any
// time, so every listing reads it again.
func TestAIGatewayCatalogFollowsTheGatewaysUnpricedSetting(t *testing.T) {
	r, _, gateway := aiGatewayTestReconciler(t)
	gateway.shared[0].Priced = true
	gateway.shared = append(gateway.shared, aigateway.Deployment{ID: "cfg-3", ModelName: "unpriced", AccessGroups: []string{aiGatewaySharedAccessGroup}})
	ctx := context.Background()
	names := func() []string {
		shared, _ := r.aiGatewayCatalog.shared()
		return catalogNames(shared)
	}
	pricedOnly, both := []string{testSharedModelOne}, []string{testSharedModelOne, "unpriced"}

	// Before the setting was ever read, the Gateway's default holds.
	gateway.blockErr = errors.New("not reported")
	r.refreshAIGatewayCatalogWithin(ctx, gateway, 0)
	if got := names(); !slices.Equal(got, pricedOnly) {
		t.Fatalf("before the first read: %v, want %v", got, pricedOnly)
	}
	gateway.blockErr, gateway.blockUnpriced = nil, false
	if !r.refreshAIGatewayCatalogWithin(ctx, gateway, 0) || !slices.Equal(names(), both) {
		t.Fatalf("once the Gateway serves unpriced models the catalog must change to %v, got %v", both, names())
	}
	gateway.blockErr = errors.New("unavailable")
	if r.refreshAIGatewayCatalogWithin(ctx, gateway, 0) || !slices.Equal(names(), both) {
		t.Fatalf("a failed read keeps the last setting: %v", names())
	}
	gateway.blockErr, gateway.blockUnpriced = nil, true
	if !r.refreshAIGatewayCatalogWithin(ctx, gateway, 0) || !slices.Equal(names(), pricedOnly) {
		t.Fatalf("once the Gateway blocks unpriced models again the catalog must change to %v, got %v", pricedOnly, names())
	}
}

func TestAIGatewayCatalogPublishesWhetherGitHubCopilotIsOffered(t *testing.T) {
	for _, image := range []string{testProxyImage, ""} {
		r, kube, _ := aiGatewayTestReconciler(t)
		r.AIGateway.CopilotProxyImage = image
		r.publishAIGatewayCatalog(context.Background(), 0)
		configMap := &corev1.ConfigMap{}
		if err := kube.Get(context.Background(), types.NamespacedName{Namespace: "cetirun-system", Name: cetirunv1alpha1.AIGatewayCatalogConfigMap}, configMap); err != nil {
			t.Fatal(err)
		}
		var catalog cetirunv1alpha1.AIGatewayCatalog
		if err := json.Unmarshal([]byte(configMap.Data[cetirunv1alpha1.AIGatewayCatalogKey]), &catalog); err != nil {
			t.Fatal(err)
		}
		if catalog.GitHubCopilot != (image != "") || len(catalog.SharedModels) != 1 {
			t.Fatalf("proxy image %q: published %+v", image, catalog)
		}
	}
}

// A Copilot model's configured name carries GitHub's display name, which the
// operator does not control: it must stay within the chart's limit too.
func TestAIGatewayModelEntriesKeepCopilotNamesWithinTheChartLimit(t *testing.T) {
	r, _, gateway := aiGatewayTestReconciler(t)
	r.refreshAIGatewayCatalogWithin(context.Background(), gateway, 0)
	agent := routedAgent()
	raw, err := json.Marshal([]cetirunv1alpha1.CopilotModel{{ID: "gpt-5.5", Name: strings.Repeat("n", 300)}})
	if err != nil {
		t.Fatal(err)
	}
	agent.Annotations = map[string]string{cetirunv1alpha1.AnnotationCopilotModels: string(raw)}
	agent.Status.AIGateway.Copilot = &cetirunv1alpha1.AIGatewayCopilotStatus{Models: []string{"copilot/gpt-5.5"}}
	entries, _, _ := r.aiGatewayModelEntries(agent)
	found := false
	for _, entry := range entries {
		if utf8.RuneCountInString(entry.ID) > aiGatewayModelNameLimit || utf8.RuneCountInString(entry.Name) > aiGatewayModelNameLimit {
			t.Fatalf("entry %q exceeds the chart's limit: name has %d characters", entry.ID, utf8.RuneCountInString(entry.Name))
		}
		if entry.ID == "copilot/gpt-5.5" {
			found = strings.HasSuffix(entry.Name, copilotModelNameSuffix)
		}
	}
	if !found {
		t.Fatalf("the Copilot model must stay listed with its marked name: %+v", entries)
	}
}

func TestAIGatewayCatalogSeedsFromThePublishedCatalogDuringAnOutage(t *testing.T) {
	published := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: cetirunv1alpha1.AIGatewayCatalogConfigMap, Namespace: "cetirun-system"},
		Data: map[string]string{cetirunv1alpha1.AIGatewayCatalogKey: `{"sharedModels":[{"name":"` + testSharedModelOne +
			`","contextWindow":1000000,"maxOutputTokens":32768}]}`},
	}
	r, _, _ := aiGatewayTestReconciler(t, published)
	failing := &failingListGateway{fakeAIGateway: newFakeAIGateway()}
	if r.refreshAIGatewayCatalogWithin(context.Background(), failing, 0) {
		t.Fatal("a failed listing reports no change")
	}
	shared, known := r.aiGatewayCatalog.shared()
	if !known || len(shared) != 1 || shared[0].ContextWindow != 1000000 {
		t.Fatalf("the published catalog stands in, with its limits: %+v known=%v", shared, known)
	}
	if _, known := r.aiGatewayCatalog.has("cfg-1"); known {
		t.Fatal("a seeded catalog does not claim to know the Gateway's deployments")
	}
	// The first real listing with the same shared models is not a change,
	// so Agents are not re-rendered when the Gateway comes back.
	if r.refreshAIGatewayCatalogWithin(context.Background(), failing.fakeAIGateway, 0) {
		t.Fatal("recovery with unchanged shared models must not count as a change")
	}
}

type failingListGateway struct{ *fakeAIGateway }

func (f *failingListGateway) ListModels(context.Context) ([]aigateway.Deployment, error) {
	return nil, errors.New("gateway unavailable")
}

// Turning key creation off removes no key the Gateway holds: while a Gateway
// is configured, a failed revocation holds the deletion either way. Removing
// the Gateway configuration is how a deployment abandons revocation.
func TestDeleteAIGatewayKeyHoldsDeletionWhileAConfiguredGatewayFails(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	agent.Status.AIGateway = &cetirunv1alpha1.AIGatewayStatus{Phase: cetirunv1alpha1.AIGatewayPhaseReady}
	r, _, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	gateway.deleteErr = errors.New("synthetic outage")
	for _, enabled := range []bool{false, true} {
		r.AIGateway.Enabled = enabled
		if err := r.deleteAIGatewayKey(context.Background(), agent); err == nil {
			t.Fatalf("enabled=%v: a failed revocation must hold the deletion while the Gateway is configured", enabled)
		}
	}
	r.AIGateway = AIGatewayConfig{}
	if err := r.deleteAIGatewayKey(context.Background(), agent); err != nil {
		t.Fatalf("without a Gateway configured deletion must proceed: %v", err)
	}
}

func TestCopilotResponsesModelsReachTheProxy(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	agent.Annotations = map[string]string{cetirunv1alpha1.AnnotationCopilotModels: `[
		{"id":"gpt-5.5","responsesOnly":true},{"id":"claude-sonnet-4.5"},{"id":"gpt-6-sol","responsesOnly":true}]`}
	agent.Status.AIGateway = &cetirunv1alpha1.AIGatewayStatus{Copilot: &cetirunv1alpha1.AIGatewayCopilotStatus{
		// gpt-6-sol is not registered, for example for lack of a price.
		Models: []string{"copilot/claude-sonnet-4.5", "copilot/gpt-5.5"},
	}}
	r, _, _ := aiGatewayTestReconciler(t, agent)
	values := map[string]any{"aiGateway": map[string]any{"copilotProxy": map[string]any{"enabled": true}}}
	r.applyCopilotResponsesModels(values, agent)
	got := values["aiGateway"].(map[string]any)["copilotProxy"].(map[string]any)["responsesModels"]
	if !slices.Equal(got.([]any), []any{"gpt-5.5"}) {
		t.Fatalf("responsesModels = %v, want the registered responses-only models", got)
	}
	off := map[string]any{"aiGateway": map[string]any{"copilotProxy": map[string]any{"enabled": false}}}
	r.applyCopilotResponsesModels(off, agent)
	if _, set := off["aiGateway"].(map[string]any)["copilotProxy"].(map[string]any)["responsesModels"]; set {
		t.Fatal("an Agent without a proxy gets no proxy models")
	}
}

func TestReconcileAIGatewayRecreatesOwnerWhenTheGatewayLostItsData(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, _, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	team := aiGatewayTeamPrefix + testAIGatewayTeam
	if !gateway.teams[team] || !gateway.users["alice"] || !gateway.members[team+"/alice"] {
		t.Fatalf("first pass must create the owner: teams=%v users=%v members=%v", gateway.teams, gateway.users, gateway.members)
	}
	// The Gateway's database is replaced: keys, teams and users are gone,
	// while the operator still remembers having created the owner.
	gateway.keys = map[string]*aigateway.Key{}
	gateway.teams, gateway.users, gateway.members = map[string]bool{}, map[string]bool{}, map[string]bool{}
	stale := metav1.NewTime(time.Now().Add(-2 * aiGatewayVerificationInterval))
	agent.Status.AIGateway.LastVerifiedTime = &stale
	if err := r.reconcileAIGateway(context.Background(), agent, true); err != nil {
		t.Fatal(err)
	}
	if len(gateway.keys) != 1 {
		t.Fatalf("the key must be created again, got %d keys", len(gateway.keys))
	}
	if !gateway.teams[team] || !gateway.users["alice"] || !gateway.members[team+"/alice"] {
		t.Fatalf("the owner must be created again with the key: teams=%v users=%v members=%v", gateway.teams, gateway.users, gateway.members)
	}
}
