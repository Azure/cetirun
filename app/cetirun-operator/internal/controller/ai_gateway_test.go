// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/chart/loader"
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

	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
)

const (
	testAIGatewayUID  = "3f0b5a8e-1c2d-4e5f-8a9b-0c1d2e3f4a5b"
	testAIGatewayURL  = "http://cetirun-ai-gateway.cetirun-system.svc.cluster.local:4000"
	testAIGatewayTeam = "platform-admins"
)

// fakeAIGateway mirrors the LiteLLM behaviour the operator relies on: keys
// are stored by the SHA-256 of their value and aliases are unique.
type fakeAIGateway struct {
	keys           map[string]*aigateway.Key
	getErr         error
	deleteErr      error
	generated      []aigateway.GenerateRequest
	updates        [][]string
	deletedTokens  []string
	deletedAliases []string
	beforeGenerate func(aigateway.GenerateRequest)
}

func newFakeAIGateway() *fakeAIGateway {
	return &fakeAIGateway{keys: map[string]*aigateway.Key{}}
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

func aiGatewayTestConfig() AIGatewayConfig {
	return AIGatewayConfig{
		Enabled: true, URL: testAIGatewayURL, MasterKeySecretName: "cetirun-ai-gateway-auth", MasterKeySecretKey: "master-key",
		DefaultBudget: 10, DefaultModel: "shared-model", SharedModelTeams: []string{testAIGatewayTeam},
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

func readKeySecret(t *testing.T, kube client.Client) *corev1.Secret {
	t.Helper()
	secret := &corev1.Secret{}
	if err := kube.Get(context.Background(), types.NamespacedName{Namespace: instanceNamespace("alice"), Name: aiGatewayKeySecretName}, secret); err != nil {
		t.Fatalf("read key Secret: %v", err)
	}
	return secret
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

func TestReconcileAIGatewayCreatesKeyAfterPersistingSecret(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	gateway.beforeGenerate = func(req aigateway.GenerateRequest) {
		if string(readKeySecret(t, kube).Data[aiGatewayKeySecretField]) != req.Key {
			t.Fatal("the key value must be persisted before the Gateway create request")
		}
	}
	if err := r.reconcileAIGateway(context.Background(), agent, aiGatewayTestTeam(testAIGatewayTeam), true); err != nil {
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
	if req.Key != value || req.Alias != "cetirun-alice-"+testAIGatewayUID || !slices.Equal(req.Models, []string{aiGatewaySharedAccessGroup}) ||
		req.MaxBudget != 10 || req.BudgetDuration != "1mo" || req.Metadata["cetirun_agent_uid"] != testAIGatewayUID {
		t.Fatalf("unexpected create request %+v", req)
	}
	status := agent.Status.AIGateway
	if status.Phase != cetirunv1alpha1.AIGatewayPhaseReady || status.KeyID != aigateway.Token(value)[:aiGatewayKeyIDLength] ||
		!slices.Equal(status.AccessGroups, []string{aiGatewaySharedAccessGroup}) || !status.Routed || status.KeySecretName != aiGatewayKeySecretName {
		t.Fatalf("unexpected status %+v", status)
	}
	if strings.Contains(status.KeyID, value) || strings.Contains(status.KeyAlias, value) {
		t.Fatal("status must not contain the key value")
	}
	if !meta.IsStatusConditionTrue(agent.Status.Conditions, cetirunv1alpha1.ConditionAIGatewayReady) {
		t.Fatal("AIGatewayReady is not true")
	}

	if err := r.reconcileAIGateway(context.Background(), agent, aiGatewayTestTeam(testAIGatewayTeam), true); err != nil {
		t.Fatal(err)
	}
	if len(gateway.generated) != 1 || len(gateway.updates) != 0 {
		t.Fatalf("a steady reconcile must not create or update: generated=%d updates=%d", len(gateway.generated), len(gateway.updates))
	}
}

func TestReconcileAIGatewayKeyWithoutSharedTeamGrantsNoModel(t *testing.T) {
	agent := aiGatewayTestAgent("other-team")
	r, _, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	if err := r.reconcileAIGateway(context.Background(), agent, aiGatewayTestTeam("other-team"), true); err != nil {
		t.Fatal(err)
	}
	if got := gateway.generated[0].Models; !slices.Equal(got, []string{aigateway.NoModels}) {
		t.Fatalf("a key without access groups must carry the no-model entry, got %v", got)
	}
	if agent.Status.AIGateway.Routed || len(agent.Status.AIGateway.AccessGroups) != 0 {
		t.Fatalf("an Agent without shared access must not be routed: %+v", agent.Status.AIGateway)
	}
}

func TestReconcileAIGatewayAlignsAccessGroupsWithoutTouchingBudget(t *testing.T) {
	agent := aiGatewayTestAgent("other-team")
	r, _, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	if err := r.reconcileAIGateway(context.Background(), agent, aiGatewayTestTeam("other-team"), true); err != nil {
		t.Fatal(err)
	}
	agent.Spec.Team = testAIGatewayTeam
	if err := r.reconcileAIGateway(context.Background(), agent, aiGatewayTestTeam(testAIGatewayTeam), true); err != nil {
		t.Fatal(err)
	}
	if len(gateway.updates) != 1 || !slices.Equal(gateway.updates[0], []string{aiGatewaySharedAccessGroup}) || len(gateway.generated) != 1 {
		t.Fatalf("access groups were not aligned in place: updates=%v generated=%d", gateway.updates, len(gateway.generated))
	}
	for _, key := range gateway.keys {
		if key.MaxBudget == nil || *key.MaxBudget != 10 {
			t.Fatalf("the budget must be left as created, got %v", key.MaxBudget)
		}
	}
	if !agent.Status.AIGateway.Routed {
		t.Fatal("moving into a shared-model Team must route the Agent")
	}
}

func TestReconcileAIGatewayAdoptsKeyCreatedBeforeStatusWasLost(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	value := mustKeyValue(t)
	r, _, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace(), keySecret(testAIGatewayUID, value))
	budget := 25.0
	gateway.keys[aigateway.Token(value)] = &aigateway.Key{Alias: aiGatewayKeyAlias(agent), Models: []string{aiGatewaySharedAccessGroup}, MaxBudget: &budget}
	if err := r.reconcileAIGateway(context.Background(), agent, aiGatewayTestTeam(testAIGatewayTeam), true); err != nil {
		t.Fatal(err)
	}
	if len(gateway.generated) != 0 || len(gateway.deletedAliases) != 0 {
		t.Fatal("an existing key for the persisted value must be adopted, not recreated")
	}
	if agent.Status.AIGateway.KeyID != aigateway.Token(value)[:aiGatewayKeyIDLength] {
		t.Fatal("adopted key is not recorded")
	}
}

func TestReconcileAIGatewayReplacesOwnKeyForLostValue(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	value := mustKeyValue(t)
	r, _, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace(), keySecret(testAIGatewayUID, value))
	gateway.keys["token-of-a-value-no-longer-held"] = &aigateway.Key{Alias: aiGatewayKeyAlias(agent), Models: []string{aiGatewaySharedAccessGroup}}
	if err := r.reconcileAIGateway(context.Background(), agent, aiGatewayTestTeam(testAIGatewayTeam), true); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gateway.deletedAliases, []string{aiGatewayKeyAlias(agent)}) || len(gateway.generated) != 1 || gateway.generated[0].Key != value {
		t.Fatalf("the orphaned key for this Agent must be replaced: deleted=%v generated=%d", gateway.deletedAliases, len(gateway.generated))
	}
	if _, ok := gateway.keys["token-of-a-value-no-longer-held"]; ok {
		t.Fatal("the orphaned key is still present")
	}
}

func TestReconcileAIGatewayReplacesSecretOfAnotherAgent(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	stale := mustKeyValue(t)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace(), keySecret("00000000-0000-0000-0000-000000000000", stale))
	if err := r.reconcileAIGateway(context.Background(), agent, aiGatewayTestTeam(testAIGatewayTeam), true); err != nil {
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
	err := r.reconcileAIGateway(context.Background(), agent, aiGatewayTestTeam(testAIGatewayTeam), true)
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
	err := r.reconcileAIGateway(context.Background(), agent, aiGatewayTestTeam(testAIGatewayTeam), true)
	if err == nil || agent.Status.AIGateway.Phase != cetirunv1alpha1.AIGatewayPhaseDegraded || agent.Status.AIGateway.Reason != "GatewayUnavailable" {
		t.Fatalf("want Degraded/GatewayUnavailable, got %v %+v", err, agent.Status.AIGateway)
	}
	if agent.Status.AIGateway.Routed {
		t.Fatal("an Agent must not be routed before its key exists")
	}
	first := string(readKeySecret(t, kube).Data[aiGatewayKeySecretField])
	gateway.getErr = nil
	if err := r.reconcileAIGateway(context.Background(), agent, aiGatewayTestTeam(testAIGatewayTeam), true); err != nil {
		t.Fatal(err)
	}
	if gateway.generated[0].Key != first {
		t.Fatal("the persisted value must be reused after the outage")
	}
}

func TestReconcileAIGatewayRequiresChartSupportForRouting(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, _, _ := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	if err := r.reconcileAIGateway(context.Background(), agent, aiGatewayTestTeam(testAIGatewayTeam), false); err != nil {
		t.Fatal(err)
	}
	condition := meta.FindStatusCondition(agent.Status.Conditions, cetirunv1alpha1.ConditionAIGatewayReady)
	if agent.Status.AIGateway.Routed || condition == nil || !strings.Contains(condition.Message, "chart version") {
		t.Fatalf("an Agent on a chart without AI Gateway support must not be routed: %+v %+v", agent.Status.AIGateway, condition)
	}
}

func TestReconcileAIGatewayHermesKeepsProvider(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	agent.Spec.Agent.Type = cetirunv1alpha1.AgentTypeHermes
	r, _, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	if err := r.reconcileAIGateway(context.Background(), agent, aiGatewayTestTeam(testAIGatewayTeam), true); err != nil {
		t.Fatal(err)
	}
	if len(gateway.generated) != 1 || agent.Status.AIGateway.Routed {
		t.Fatalf("Hermes gets a key but is not routed yet: generated=%d status=%+v", len(gateway.generated), agent.Status.AIGateway)
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
	}
	return agent
}

func TestApplyAIGatewayValuesOverridesProviderForRoutedAgent(t *testing.T) {
	r := &AgentReconciler{AIGateway: aiGatewayTestConfig()}
	values := map[string]any{
		"secrets":       map[string]any{"existingSecret": "gateway-token", "provider": "anthropic", "model": "user-choice"},
		"networkPolicy": map[string]any{"istioNamespace": "aks-istio-system"},
	}
	if err := r.applyAIGatewayValues(values, routedAgent()); err != nil {
		t.Fatal(err)
	}
	secrets := values["secrets"].(map[string]any)
	if secrets["provider"] != "openai" || secrets["apiBase"] != testAIGatewayURL+"/v1" || secrets["model"] != "shared-model" ||
		secrets["authMode"] != "api_key" || secrets["existingSecret"] != "gateway-token" {
		t.Fatalf("unexpected secrets values %v", secrets)
	}
	gateway := values["aiGateway"].(map[string]any)
	if gateway["enabled"] != true || gateway["keySecretName"] != aiGatewayKeySecretName {
		t.Fatalf("unexpected aiGateway values %v", gateway)
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
			if values["secrets"].(map[string]any)["provider"] != "github_copilot" || values["aiGateway"].(map[string]any)["enabled"] != false {
				t.Fatalf("unrouted Agent values changed: %v", values)
			}
		})
	}
}

func TestAIGatewayRevision(t *testing.T) {
	disabled := &AgentReconciler{}
	if got := disabled.aiGatewayRevision(aiGatewayTestAgent(testAIGatewayTeam), nil); got != "" {
		t.Fatalf("a deployment without the feature must keep its hash, got %q", got)
	}
	r := &AgentReconciler{AIGateway: aiGatewayTestConfig()}
	agent := routedAgent()
	team := aiGatewayTestTeam(testAIGatewayTeam)
	routed := r.aiGatewayRevision(agent, team)
	agent.Status.AIGateway.KeyID = "ba9876543210"
	if rotated := r.aiGatewayRevision(agent, team); rotated == routed {
		t.Fatal("a new key must change the revision")
	}
	if r.aiGatewayRevision(agent, team) == r.aiGatewayRevision(agent, aiGatewayTestTeam("other-team")) {
		t.Fatal("a change in shared-model eligibility must change the revision")
	}
}

func TestDeleteAIGatewayKeyRevokesByValueAndAlias(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	value := mustKeyValue(t)
	agent.Status.AIGateway = &cetirunv1alpha1.AIGatewayStatus{Phase: cetirunv1alpha1.AIGatewayPhaseReady, KeyAlias: aiGatewayKeyAlias(agent)}
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace(), keySecret(testAIGatewayUID, value))
	gateway.keys[aigateway.Token(value)] = &aigateway.Key{Alias: "renamed-by-an-administrator"}
	if err := r.deleteAIGatewayKey(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gateway.deletedTokens, []string{aigateway.Token(value)}) || !slices.Equal(gateway.deletedAliases, []string{aiGatewayKeyAlias(agent)}) {
		t.Fatalf("revocation must use the key and the alias: tokens=%v aliases=%v", gateway.deletedTokens, gateway.deletedAliases)
	}
	if len(gateway.keys) != 0 {
		t.Fatal("the key survived revocation")
	}
	persisted := &cetirunv1alpha1.Agent{}
	if err := kube.Get(context.Background(), types.NamespacedName{Name: agent.Name}, persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Status.AIGateway == nil || persisted.Status.AIGateway.Phase != cetirunv1alpha1.AIGatewayPhaseDeleting {
		t.Fatal("the deletion gate was not persisted before revocation")
	}
}

func TestDeleteAIGatewayKeyBlocksOnFailureAndSkipsWhenUnused(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	agent.Status.AIGateway = &cetirunv1alpha1.AIGatewayStatus{Phase: cetirunv1alpha1.AIGatewayPhaseReady}
	r, _, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	gateway.deleteErr = errors.New("synthetic outage")
	if err := r.deleteAIGatewayKey(context.Background(), agent); err == nil {
		t.Fatal("a failed revocation must block deletion")
	}

	unused := aiGatewayTestAgent(testAIGatewayTeam)
	r2, _, gateway2 := aiGatewayTestReconciler(t, unused)
	r2.AIGateway.Enabled = false
	if err := r2.deleteAIGatewayKey(context.Background(), unused); err != nil || len(gateway2.deletedAliases) != 0 {
		t.Fatalf("an Agent that never had a key must not call the Gateway: %v %v", err, gateway2.deletedAliases)
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
		"AI_GATEWAY_SHARED_MODEL_TEAMS": " platform-admins, ,platform-admins,sre ",
	}
	cfg, err = ParseAIGatewayConfig(env(valid))
	if err != nil || !cfg.Enabled || cfg.URL != testAIGatewayURL || cfg.MasterKeySecretKey != "master-key" || cfg.DefaultBudget != 12.5 ||
		!slices.Equal(cfg.SharedModelTeams, []string{"platform-admins", "sre"}) {
		t.Fatalf("valid configuration: %+v %v", cfg, err)
	}
	for name, override := range map[string][2]string{
		"external URL":     {"AI_GATEWAY_URL", "http://gateway.example.com:4000"},
		"https URL":        {"AI_GATEWAY_URL", "https://cetirun-ai-gateway.cetirun-system.svc.cluster.local:4000"},
		"URL with path":    {"AI_GATEWAY_URL", testAIGatewayURL + "/v1"},
		"URL without port": {"AI_GATEWAY_URL", "http://cetirun-ai-gateway.cetirun-system.svc.cluster.local"},
		"no Secret":        {"AI_GATEWAY_MASTER_KEY_SECRET", ""},
		"no budget":        {"AI_GATEWAY_DEFAULT_BUDGET", ""},
		"negative budget":  {"AI_GATEWAY_DEFAULT_BUDGET", "-1"},
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

func TestReconcileRoutesSharedTeamAgentAndRevokesOnDelete(t *testing.T) {
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
	if secrets["provider"] != "openai" || secrets["apiBase"] != testAIGatewayURL+"/v1" || secrets["model"] != "shared-model" {
		t.Fatalf("installed values are not routed through the Gateway: %v", secrets)
	}
	if chart.lastValues["aiGateway"].(map[string]any)["enabled"] != true {
		t.Fatal("chart wiring for the key is off")
	}
	current := &cetirunv1alpha1.Agent{}
	if err := kube.Get(context.Background(), req.NamespacedName, current); err != nil {
		t.Fatal(err)
	}
	if current.Status.AIGateway == nil || !current.Status.AIGateway.Routed || current.Status.AIGateway.Phase != cetirunv1alpha1.AIGatewayPhaseReady {
		t.Fatalf("persisted status %+v", current.Status.AIGateway)
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
