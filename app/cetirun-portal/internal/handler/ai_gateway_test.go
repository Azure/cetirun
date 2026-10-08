// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/config"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
)

func gatewayConfig() config.Config {
	return config.Config{AIGatewayEnabled: true, AIGatewayCatalogNamespace: "cetirun-system"}
}

func gatewayTeam(name string) *cetirunv1alpha1.Team {
	return &cetirunv1alpha1.Team{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

// catalogConfigMap publishes the shared models of a deployment that offers
// GitHub Copilot.
func catalogConfigMap(models ...string) *corev1.ConfigMap {
	return publishedCatalog(true, models...)
}

func publishedCatalog(copilot bool, models ...string) *corev1.ConfigMap {
	return routingCatalog([]cetirunv1alpha1.AgentType{cetirunv1alpha1.AgentTypeOpenClaw, cetirunv1alpha1.AgentTypeHermes}, copilot, models...)
}

// routingCatalog publishes a catalog whose operator's default agent chart
// routes the given agent types; nil publishes an earlier operator's catalog,
// which does not list them.
func routingCatalog(agentTypes []cetirunv1alpha1.AgentType, copilot bool, models ...string) *corev1.ConfigMap {
	catalog := cetirunv1alpha1.AIGatewayCatalog{SharedModels: []cetirunv1alpha1.AIGatewayCatalogModel{}, GitHubCopilot: copilot, AgentTypes: agentTypes}
	for _, name := range models {
		catalog.SharedModels = append(catalog.SharedModels, cetirunv1alpha1.AIGatewayCatalogModel{Name: name, ContextWindow: 200000})
	}
	raw, _ := json.Marshal(catalog)
	if agentTypes == nil {
		raw, _ = json.Marshal(map[string]any{"sharedModels": catalog.SharedModels, "githubCopilot": copilot})
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: cetirunv1alpha1.AIGatewayCatalogConfigMap, Namespace: "cetirun-system"},
		Data:       map[string]string{cetirunv1alpha1.AIGatewayCatalogKey: string(raw)},
	}
}

func createAgent(t *testing.T, cfg config.Config, body string, objs ...client.Object) (*httptest.ResponseRecorder, client.Client) {
	t.Helper()
	fc := newFakeClient(objs...)
	h := &InstanceHandler{Kube: kube.NewClient(fc), Config: cfg}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances", strings.NewReader(body))
	req = withClaims(req, "alice", "Alice", "alice@example.com")
	w := httptest.NewRecorder()
	h.CreateInstance(w, req)
	return w, fc
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var resp ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error body %q: %v", w.Body.String(), err)
	}
	return resp.Error.Code
}

func TestCreateInstanceAIGatewayRequiresExistingTeam(t *testing.T) {
	w, _ := createAgent(t, gatewayConfig(), `{"agent":{"type":"openclaw"}}`, gatewayTeam("aks"))
	if w.Code != http.StatusBadRequest || errorCode(t, w) != "team_required" {
		t.Fatalf("no team: status %d body %s", w.Code, w.Body.String())
	}
	w, _ = createAgent(t, gatewayConfig(), `{"agent":{"type":"openclaw"},"team":"nope"}`, gatewayTeam("aks"))
	if w.Code != http.StatusBadRequest || errorCode(t, w) != "unknown_team" {
		t.Fatalf("unknown team: status %d body %s", w.Code, w.Body.String())
	}
	// Without the Gateway, a Team stays optional.
	w, _ = createAgent(t, config.Config{}, `{"agent":{"type":"openclaw"}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("feature off: status %d body %s", w.Code, w.Body.String())
	}
	// A Custom Agent gets a key too, so it needs a Team as well. It is not
	// routed, so the model switches do not apply to it.
	custom := gatewayConfig()
	custom.AllowedAgentTypes = []string{"openclaw", "custom"}
	customAgent := `"agent":{"type":"custom","image":"` + managementImage + `","username":"assistant"}`
	w, _ = createAgent(t, custom, `{`+customAgent+`}`, gatewayTeam("aks"))
	if w.Code != http.StatusBadRequest || errorCode(t, w) != "team_required" {
		t.Fatalf("custom without team: status %d body %s", w.Code, w.Body.String())
	}
	w, fc := createAgent(t, custom, `{`+customAgent+`,"team":"aks","modelAccess":{"githubCopilot":false}}`, gatewayTeam("aks"))
	if w.Code != http.StatusCreated {
		t.Fatalf("custom with team: status %d body %s", w.Code, w.Body.String())
	}
	agent := &cetirunv1alpha1.Agent{}
	if err := fc.Get(context.Background(), client.ObjectKey{Name: "alice"}, agent); err != nil {
		t.Fatal(err)
	}
	if agent.Spec.Team != "aks" || agent.Spec.ModelAccess != nil {
		t.Fatalf("custom: team %q modelAccess %+v, want aks and unset", agent.Spec.Team, agent.Spec.ModelAccess)
	}
}

func TestCreateInstanceAIGatewayModelAccessRules(t *testing.T) {
	const openclaw = `{"agent":{"type":"openclaw"},"team":"aks"`
	const hermes = `{"agent":{"type":"hermes"},"team":"aks"}`
	onlyOpenClaw := []cetirunv1alpha1.AgentType{cetirunv1alpha1.AgentTypeOpenClaw}
	cases := []struct {
		name        string
		body        string
		catalog     *corev1.ConfigMap
		wantStatus  int
		wantAccess  *[2]bool
		wantErrCode string
		wantErrText string
	}{
		{name: "defaults", body: `{"agent":{"type":"openclaw"},"team":" aks "}`, catalog: catalogConfigMap("gpt-6"), wantStatus: http.StatusCreated},
		{name: "both on", body: openclaw + `,"modelAccess":{"sharedModels":true,"githubCopilot":true}}`,
			catalog: catalogConfigMap(), wantStatus: http.StatusCreated, wantAccess: &[2]bool{true, true}},
		{name: "both off", body: openclaw + `,"modelAccess":{"sharedModels":false,"githubCopilot":false}}`,
			catalog: catalogConfigMap("gpt-6"), wantStatus: http.StatusBadRequest, wantErrCode: "invalid_model_access"},
		{name: "shared off", body: openclaw + `,"modelAccess":{"sharedModels":false}}`,
			catalog: catalogConfigMap(), wantStatus: http.StatusCreated, wantAccess: &[2]bool{false, true}},
		{name: "copilot off without shared models", body: openclaw + `,"modelAccess":{"githubCopilot":false}}`,
			catalog: catalogConfigMap(), wantStatus: http.StatusBadRequest, wantErrCode: "invalid_model_access"},
		{name: "copilot off with shared models", body: openclaw + `,"modelAccess":{"githubCopilot":false}}`,
			catalog: catalogConfigMap("gpt-6"), wantStatus: http.StatusCreated, wantAccess: &[2]bool{true, false}},
		// The catalog says which sources exist; until it is published the
		// Portal cannot tell whether the Agent would have a model.
		{name: "unpublished catalog", body: openclaw + `}`, wantStatus: http.StatusServiceUnavailable, wantErrCode: "model_sources_unknown"},
		{name: "copilot off with an unpublished catalog", body: openclaw + `,"modelAccess":{"githubCopilot":false}}`,
			wantStatus: http.StatusServiceUnavailable, wantErrCode: "model_sources_unknown"},
		// A deployment without the Copilot proxy offers only shared models.
		{name: "no copilot, shared models", body: openclaw + `}`, catalog: publishedCatalog(false, "gpt-6"), wantStatus: http.StatusCreated},
		{name: "no copilot, shared off", body: openclaw + `,"modelAccess":{"sharedModels":false}}`,
			catalog: publishedCatalog(false, "gpt-6"), wantStatus: http.StatusBadRequest, wantErrCode: "invalid_model_access"},
		{name: "no copilot, no shared models", body: openclaw + `}`, catalog: publishedCatalog(false),
			wantStatus: http.StatusConflict, wantErrCode: "no_model_source"},
		{name: "no copilot, no shared models, copilot asked for", body: openclaw + `,"modelAccess":{"sharedModels":false,"githubCopilot":true}}`,
			catalog: publishedCatalog(false), wantStatus: http.StatusConflict, wantErrCode: "no_model_source"},
		// Hermes takes its models from the Gateway too, under the same rules.
		{name: "hermes copilot off with shared models", body: `{"agent":{"type":"hermes"},"team":"aks","modelAccess":{"githubCopilot":false}}`,
			catalog: catalogConfigMap("gpt-6"), wantStatus: http.StatusCreated, wantAccess: &[2]bool{true, false}},
		{name: "hermes copilot off without shared models", body: `{"agent":{"type":"hermes"},"team":"aks","modelAccess":{"githubCopilot":false}}`,
			catalog: catalogConfigMap(), wantStatus: http.StatusBadRequest, wantErrCode: "invalid_model_access"},
		{name: "hermes with an unpublished catalog", body: `{"agent":{"type":"hermes"},"team":"aks"}`,
			wantStatus: http.StatusServiceUnavailable, wantErrCode: "model_sources_unknown"},
		// Only the agent types the operator's default agent chart routes
		// through the Gateway take its models: the operator would leave
		// another one off the Gateway, with the Gateway's model names on the
		// agent's own provider.
		{name: "hermes on a chart that routes only OpenClaw", body: hermes, catalog: routingCatalog(onlyOpenClaw, true, "gpt-6"),
			wantStatus: http.StatusConflict, wantErrCode: "agent_chart_unsupported", wantErrText: "does not route Hermes agents"},
		{name: "hermes with an earlier operator's catalog", body: hermes, catalog: routingCatalog(nil, true, "gpt-6"),
			wantStatus: http.StatusConflict, wantErrCode: "agent_chart_unsupported"},
		{name: "openclaw with an earlier operator's catalog", body: openclaw + `}`, catalog: routingCatalog(nil, true, "gpt-6"),
			wantStatus: http.StatusCreated},
		{name: "openclaw on a chart older than the Gateway", body: openclaw + `}`, catalog: routingCatalog([]cetirunv1alpha1.AgentType{}, true, "gpt-6"),
			wantStatus: http.StatusConflict, wantErrCode: "agent_chart_unsupported", wantErrText: "upgrade the agent chart"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs := []client.Object{gatewayTeam("aks")}
			if tc.catalog != nil {
				objs = append(objs, tc.catalog)
			}
			w, fc := createAgent(t, gatewayConfig(), tc.body, objs...)
			if w.Code != tc.wantStatus {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.wantStatus, w.Body.String())
			}
			if tc.wantErrCode != "" {
				if code := errorCode(t, w); code != tc.wantErrCode {
					t.Fatalf("error code %q, want %q", code, tc.wantErrCode)
				}
				if !strings.Contains(w.Body.String(), tc.wantErrText) {
					t.Fatalf("error %s, want it to say %q", w.Body.String(), tc.wantErrText)
				}
				return
			}
			agent := &cetirunv1alpha1.Agent{}
			if err := fc.Get(context.Background(), client.ObjectKey{Name: "alice"}, agent); err != nil {
				t.Fatal(err)
			}
			if agent.Spec.Team != "aks" || agent.Spec.User.Team != "aks" {
				t.Fatalf("team = %q/%q, want aks", agent.Spec.Team, agent.Spec.User.Team)
			}
			switch {
			case tc.wantAccess == nil && agent.Spec.ModelAccess != nil:
				t.Fatalf("modelAccess = %+v, want unset", agent.Spec.ModelAccess)
			case tc.wantAccess != nil:
				access := agent.Spec.ModelAccess
				if access == nil || access.SharedModels == nil || access.GitHubCopilot == nil ||
					*access.SharedModels != tc.wantAccess[0] || *access.GitHubCopilot != tc.wantAccess[1] {
					t.Fatalf("modelAccess = %+v, want %v", access, *tc.wantAccess)
				}
			}
		})
	}
}

func routedAgent() *cetirunv1alpha1.Agent {
	agent := sampleInstance()
	agent.Name = "alice-gw"
	agent.UID = "uid-1"
	agent.Spec.GatewayTokenSecretRef = "gateway-token"
	agent.Status.Namespace = "cetirun-alice-gw"
	agent.Status.AIGateway = &cetirunv1alpha1.AIGatewayStatus{
		Phase: "Ready", Routed: true, KeyID: "abc", KeySecretName: "ai-gateway-virtual-key",
		Models:       []string{"gpt-6", cetirunv1alpha1.AIGatewaySignInRequiredModel},
		DefaultModel: "gpt-6",
		Copilot:      &cetirunv1alpha1.AIGatewayCopilotStatus{Phase: "SignInRequired", AccessGroup: "copilot-uid-1"},
	}
	return agent
}

func gatewayTokenSecret(namespace string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "gateway-token", Namespace: namespace},
		Data:       map[string][]byte{"OPENCLAW_GATEWAY_TOKEN": []byte("tok"), "CETIRUN_AUTH_KEY": []byte("helper-key")},
	}
}

func TestInstanceResponseDescribesAIGateway(t *testing.T) {
	routed := routedAgent()
	fc := newFakeClient(routed, gatewayTokenSecret("cetirun-alice-gw"))
	h := &InstanceHandler{Kube: kube.NewClient(fc), Config: gatewayConfig()}
	resp, err := h.toInstanceResponse(context.Background(), routed)
	if err != nil {
		t.Fatal(err)
	}
	gw := resp.AIGateway
	if gw == nil || !gw.Routed || !gw.SharedModels || !gw.GitHubCopilot {
		t.Fatalf("aiGateway = %+v", gw)
	}
	if strings.Join(gw.Models, ",") != "gpt-6" || gw.DefaultModel != "gpt-6" {
		t.Fatalf("models = %v default %q; placeholders must be hidden", gw.Models, gw.DefaultModel)
	}
	if gw.Copilot == nil || gw.Copilot.Phase != "SignInRequired" || gw.Copilot.Models == nil {
		t.Fatalf("copilot = %+v", gw.Copilot)
	}
	if resp.CetiRunAuthKey != "" {
		t.Fatal("a routed Agent has no in-agent sign-in helper; its key must not be returned")
	}

	// A placeholder default is not reported as a model.
	onlyPlaceholder := routedAgent()
	onlyPlaceholder.Status.AIGateway.Models = []string{cetirunv1alpha1.AIGatewayNoModelsModel}
	onlyPlaceholder.Status.AIGateway.DefaultModel = cetirunv1alpha1.AIGatewayNoModelsModel
	if gw := aiGatewayResponse(onlyPlaceholder); len(gw.Models) != 0 || gw.DefaultModel != "" {
		t.Fatalf("placeholder-only response = %+v", gw)
	}

	unrouted := sampleInstance()
	unrouted.Spec.GatewayTokenSecretRef = "gateway-token"
	fc = newFakeClient(unrouted, gatewayTokenSecret("cetirun-alice"))
	h = &InstanceHandler{Kube: kube.NewClient(fc), Config: gatewayConfig()}
	resp, err = h.toInstanceResponse(context.Background(), unrouted)
	if err != nil {
		t.Fatal(err)
	}
	if resp.AIGateway != nil || resp.CetiRunAuthKey != "helper-key" {
		t.Fatalf("unrouted: aiGateway %+v, helper key %q", resp.AIGateway, resp.CetiRunAuthKey)
	}
}

// The Portal offers the in-agent sign-in only where the operator recorded
// that the workload serves the helper: an OpenClaw chart without one gets no
// helper key, even when the Agent is not routed through the Gateway.
func TestInstanceResponseOffersTheInAgentSignInOnlyWithItsHelper(t *testing.T) {
	helperKeyOf := func(agent *cetirunv1alpha1.Agent) string {
		t.Helper()
		h := &InstanceHandler{Kube: kube.NewClient(newFakeClient(agent, gatewayTokenSecret("cetirun-alice"))), Config: gatewayConfig()}
		resp, err := h.toInstanceResponse(context.Background(), agent)
		if err != nil {
			t.Fatal(err)
		}
		return resp.CetiRunAuthKey
	}
	without, with := false, true
	agent := sampleInstance()
	agent.Spec.GatewayTokenSecretRef = "gateway-token"
	agent.Status.SignInHelper = &without
	if key := helperKeyOf(agent); key != "" {
		t.Fatalf("a workload without the helper must not get its key, got %q", key)
	}
	agent.Status.SignInHelper = &with
	if key := helperKeyOf(agent); key != "helper-key" {
		t.Fatalf("a workload serving the helper gets its key, got %q", key)
	}
}

func TestGetAIGatewayCatalog(t *testing.T) {
	get := func(cfg config.Config, objs ...client.Object) *httptest.ResponseRecorder {
		h := &InstanceHandler{Kube: kube.NewClient(newFakeClient(objs...)), Config: cfg}
		req := withClaims(httptest.NewRequest(http.MethodGet, "/api/v1/ai-gateway/catalog", nil), "alice", "Alice", "alice@example.com")
		w := httptest.NewRecorder()
		h.GetAIGatewayCatalog(w, req)
		return w
	}
	if w := get(config.Config{}); w.Code != http.StatusNotFound {
		t.Fatalf("feature off: status %d", w.Code)
	}
	var resp AIGatewayCatalogResponse
	w := get(gatewayConfig())
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil || w.Code != http.StatusOK || resp.Known || resp.SharedModels == nil {
		t.Fatalf("unpublished: status %d resp %+v err %v", w.Code, resp, err)
	}
	w = get(gatewayConfig(), catalogConfigMap("gpt-6", "gpt-6-mini"))
	if err := json.NewDecoder(bytes.NewReader(w.Body.Bytes())).Decode(&resp); err != nil || !resp.Known || len(resp.SharedModels) != 2 ||
		resp.SharedModels[0].Name != "gpt-6" || resp.SharedModels[0].ContextWindow != 200000 || !resp.GitHubCopilot {
		t.Fatalf("published: resp %+v err %v", resp, err)
	}
	resp = AIGatewayCatalogResponse{}
	w = get(gatewayConfig(), publishedCatalog(false, "gpt-6"))
	if err := json.NewDecoder(bytes.NewReader(w.Body.Bytes())).Decode(&resp); err != nil || !resp.Known || resp.GitHubCopilot {
		t.Fatalf("published without GitHub Copilot: resp %+v err %v", resp, err)
	}
	if !slices.Equal(resp.AgentTypes, []cetirunv1alpha1.AgentType{cetirunv1alpha1.AgentTypeOpenClaw, cetirunv1alpha1.AgentTypeHermes}) {
		t.Fatalf("the published agent types must reach the form, got %v", resp.AgentTypes)
	}
	// A catalog from an earlier operator lists no agent types: it routes
	// only OpenClaw.
	resp = AIGatewayCatalogResponse{}
	w = get(gatewayConfig(), routingCatalog(nil, true, "gpt-6"))
	if err := json.NewDecoder(bytes.NewReader(w.Body.Bytes())).Decode(&resp); err != nil ||
		!slices.Equal(resp.AgentTypes, []cetirunv1alpha1.AgentType{cetirunv1alpha1.AgentTypeOpenClaw}) {
		t.Fatalf("earlier operator: resp %+v err %v", resp, err)
	}
}
