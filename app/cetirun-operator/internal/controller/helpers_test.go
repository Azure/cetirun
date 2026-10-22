// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	istioapiv1beta1 "istio.io/api/networking/v1beta1"
	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

// TestOperatorConfigHash_Uniqueness verifies that different config combinations produce different hashes.
func TestOperatorConfigHash_Uniqueness(t *testing.T) {
	cases := []struct {
		domainSuffix string
		secretHash   string
	}{
		{"example.com", ""},
		{"example.net", ""},
		{"example.com", "abc123"},
		{"example.net", "abc123"},
		{"example.com", "xyz789"},
	}

	seen := map[string]struct{ domain, secret string }{}
	for _, c := range cases {
		h := operatorConfigHash(c.domainSuffix, c.secretHash, "", "", "")
		if len(h) != 16 {
			t.Errorf("operatorConfigHash(%q, %q) = %q, want 16 hex chars", c.domainSuffix, c.secretHash, h)
		}
		if prev, exists := seen[h]; exists {
			t.Errorf("hash collision: (%q,%q) and (%q,%q) both produce %q",
				prev.domain, prev.secret, c.domainSuffix, c.secretHash, h)
		}
		seen[h] = struct{ domain, secret string }{c.domainSuffix, c.secretHash}
	}
}

// TestOperatorConfigHash_Deterministic verifies the same inputs always produce the same hash.
func TestOperatorConfigHash_Deterministic(t *testing.T) {
	h1 := operatorConfigHash("example.com", "secret1", "", "", "")
	h2 := operatorConfigHash("example.com", "secret1", "", "", "")
	if h1 != h2 {
		t.Errorf("operatorConfigHash not deterministic: got %q then %q", h1, h2)
	}
}

func TestConfigHash_IncludesDefaultModelOnlyWhenRendered(t *testing.T) {
	reconciler := &AgentReconciler{DefaultModel: "gpt-6-astra"}
	instance := newTestInstance("alice", "Alice", "", "gateway-token")

	first := reconciler.configHash(instance, nil, "secret", "")
	reconciler.DefaultModel = "gpt-6-astra-next"
	if second := reconciler.configHash(instance, nil, "secret", ""); first == second {
		t.Error("deployment default change must affect an instance that uses it")
	}

	instance.Spec.Model = "gpt-5.4"
	first = reconciler.configHash(instance, nil, "secret", "")
	reconciler.DefaultModel = "gpt-6-astra-final"
	if second := reconciler.configHash(instance, nil, "secret", ""); first != second {
		t.Error("deployment default change must not affect an explicitly selected model")
	}

	instance.Spec.Model = ""
	instance.Spec.Helm.Values["secrets"] = runtime.RawExtension{
		Raw: []byte(`{"provider":"openai","model":"gpt-5.4"}`),
	}
	first = reconciler.configHash(instance, nil, "secret", "")
	reconciler.DefaultModel = "gpt-6-astra-latest"
	if second := reconciler.configHash(instance, nil, "secret", ""); first != second {
		t.Error("deployment default change must not affect a non-Copilot instance")
	}
}

func TestConfigHashInvalidatesPreRuntimeAgentIdentityWiring(t *testing.T) {
	reconciler := &AgentReconciler{
		AgentIdentityEnabled:           true,
		AgentIdentityTenantID:          "11111111-1111-1111-1111-111111111111",
		AgentIdentityBlueprintClientID: "22222222-2222-2222-2222-222222222222",
		AgentIdentityNamePrefix:        "cetirun",
		AgentIdentityBrokerURL:         "http://cetirun-agent-broker.cetirun-credentials.svc.cluster.local:8080",
	}
	instance := newTestInstance("alice", "Alice", "", "gateway-token")
	instance.Spec.AgentIdentity = &cetirunv1alpha1.AgentIdentitySpec{Enabled: true}
	legacy := operatorConfigHash(
		reconciler.DomainSuffix, "secret", reconciler.SkillsStorageAccount,
		hashCetiRunHelmValues(reconciler.CetiRunHelmValues), reconciler.ChartRegistry,
		reconciler.AzureSubscriptionID, reconciler.AzureResourceGroup,
		reconciler.AzureBotNamePrefix, reconciler.AzureTenantID,
		reconciler.AzureOIDCIssuerURL, reconciler.AgentIdentityTenantID,
		reconciler.AgentIdentityBlueprintClientID, reconciler.AgentIdentityNamePrefix,
		reconciler.AgentIdentityBrokerURL, "",
	)
	if current := reconciler.configHash(instance, nil, "secret", ""); current == legacy {
		t.Fatal("Agent Identity runtime revision must invalidate the pre-wiring skip cache")
	}
}

// newTestInstance returns a minimal Agent for use in tests.
func newTestInstance(alias, displayName, team, gatewaySecret string) *cetirunv1alpha1.Agent {
	return &cetirunv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: alias},
		Spec: cetirunv1alpha1.AgentSpec{
			Agent: cetirunv1alpha1.HarnessSpec{Type: cetirunv1alpha1.AgentTypeOpenClaw},
			User: cetirunv1alpha1.UserSpec{
				Alias:       alias,
				DisplayName: displayName,
			},
			Team:                  team,
			GatewayTokenSecretRef: gatewaySecret,
			Helm:                  cetirunv1alpha1.HelmSpec{Values: map[string]runtime.RawExtension{}},
		},
	}
}

func TestBuildHelmValuesWithToolsMCPUsesControllerOwnedWorkloadIdentity(t *testing.T) {
	instance := newTestInstance("alice", "Alice", "", "gateway-token")
	instance.Name = "alice-hermes-v1"
	instance.Spec.AgentIdentity = &cetirunv1alpha1.AgentIdentitySpec{Enabled: true}
	instance.Status.AgentIdentity = &cetirunv1alpha1.AgentIdentityStatus{
		Phase:    cetirunv1alpha1.AgentIdentityPhaseReady,
		ClientID: "11111111-1111-1111-1111-111111111111",
		ObjectID: "22222222-2222-2222-2222-222222222222",
	}
	instance.Spec.Helm.Values["agentIdentity"] = runtime.RawExtension{Raw: []byte(`{"enabled":false,"brokerURL":"http://evil.default.svc.cluster.local:8080"}`)}
	instance.Spec.Helm.Values["serviceAccount"] = runtime.RawExtension{Raw: []byte(`{"create":false,"name":"attacker-selected","role":"cluster-admin","automountToken":true}`)}
	values, err := buildHelmValuesWithAgentIdentity(
		instance, nil, "example.com", "", nil, "", "gpt-5.4",
		AgentIdentityHelmConfig{
			Enabled:              true,
			ToolsMCPURL:          "https://agent-tools-mcp.cetirun-tools.svc.cluster.local/mcp",
			ToolsMCPAudience:     "agent-tools-mcp",
			ToolsMCPCASecretName: "agent-tools-mcp-ca",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	identity := values["agentIdentity"].(map[string]interface{})
	if !reflect.DeepEqual(identity, map[string]interface{}{"enabled": true}) {
		t.Fatalf("Agent Identity must not inject a Broker endpoint: %#v", identity)
	}
	toolsMCP := values["toolsMCP"].(map[string]interface{})
	wantToolsMCP := map[string]interface{}{
		"enabled":      true,
		"url":          "https://agent-tools-mcp.cetirun-tools.svc.cluster.local/mcp",
		"audience":     "agent-tools-mcp",
		"namespace":    "cetirun-tools",
		"caSecretName": "agent-tools-mcp-ca",
	}
	if !reflect.DeepEqual(toolsMCP, wantToolsMCP) {
		t.Fatalf("controller-owned Tools MCP values were not enforced: %#v", toolsMCP)
	}
	serviceAccount := values["serviceAccount"].(map[string]interface{})
	if serviceAccount["create"] != true ||
		serviceAccount["name"] != instanceServiceName(instance.Name) ||
		serviceAccount["role"] != "" ||
		serviceAccount["automountToken"] != false {
		t.Fatalf("controller-owned ServiceAccount values were not enforced: %#v", serviceAccount)
	}
	networkPolicy := values["networkPolicy"].(map[string]interface{})
	if _, found := networkPolicy["agentIdentityBrokerNamespace"]; found {
		t.Fatalf("Agent Broker egress must not be injected: %#v", networkPolicy)
	}
}

func TestInstanceServiceNameMatchesChartAndBrokerBoundary(t *testing.T) {
	for length, want := range map[int]string{
		46: "openclaw-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-cetirun",
		47: "openclaw-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-cetiru",
	} {
		got := instanceServiceName(strings.Repeat("a", length))
		if got != want || len(got) > 63 || strings.HasSuffix(got, "-") {
			t.Fatalf("instanceServiceName(%d chars) = %q", length, got)
		}
	}
}

func TestBuildHelmValuesRejectsToolsMCPOverride(t *testing.T) {
	instance := newTestInstance("alice", "Alice", "", "gateway-token")
	instance.Spec.Helm.Values["toolsMCP"] = runtime.RawExtension{Raw: []byte(`{"enabled":true,"url":"https://evil.default.svc.cluster.local/mcp"}`)}
	_, err := buildHelmValuesWithAgentIdentity(
		instance, nil, "example.com", "", nil, "", "gpt-5.4",
		AgentIdentityHelmConfig{
			ToolsMCPURL:          "https://agent-tools-mcp.cetirun-tools.svc.cluster.local/mcp",
			ToolsMCPCASecretName: "agent-tools-mcp-ca",
		},
	)
	if err == nil {
		t.Fatal("per-instance Tools MCP override was accepted")
	}
}

func TestBuildHelmValuesCannotEnableAgentIdentityOutsideReadyDeployment(t *testing.T) {
	instance := newTestInstance("alice", "Alice", "", "gateway-token")
	instance.Spec.AgentIdentity = &cetirunv1alpha1.AgentIdentitySpec{Enabled: true}
	instance.Status.AgentIdentity = &cetirunv1alpha1.AgentIdentityStatus{
		Phase: cetirunv1alpha1.AgentIdentityPhaseReady,
	}
	instance.Spec.Helm.Values["agentIdentity"] = runtime.RawExtension{Raw: []byte(`{"enabled":true,"brokerURL":"http://evil.default.svc.cluster.local:8080"}`)}
	for name, config := range map[string]AgentIdentityHelmConfig{
		"deployment disabled": {},
		"invalid Tools MCP URL": {
			Enabled:              true,
			ToolsMCPURL:          "https://tools.example.invalid/mcp",
			ToolsMCPCASecretName: "agent-tools-mcp-ca",
		},
	} {
		t.Run(name, func(t *testing.T) {
			values, err := buildHelmValuesWithAgentIdentity(
				instance, nil, "example.com", "", nil, "", "gpt-5.4", config,
			)
			if name == "invalid Tools MCP URL" {
				if err == nil {
					t.Fatal("invalid Tools MCP URL was accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			identity := values["agentIdentity"].(map[string]interface{})
			if !reflect.DeepEqual(identity, map[string]interface{}{"enabled": false}) {
				t.Fatalf("disabled deployment accepted Agent Identity override: %#v", identity)
			}
		})
	}
}

// TestBuildHelmValues_CopilotProvider verifies the configured deployment
// default is used when an instance does not select a model.
func TestBuildHelmValues_CopilotProvider(t *testing.T) {
	instance := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")
	values, err := buildHelmValues(instance, nil, "example.com", "", nil, "", "gpt-5.4")
	if err != nil {
		t.Fatalf("buildHelmValues error: %v", err)
	}
	secrets, ok := values["secrets"].(map[string]interface{})
	if !ok {
		t.Fatal("values[\"secrets\"] missing or wrong type")
	}
	if got := secrets["provider"]; got != "github_copilot" {
		t.Errorf("secrets.provider = %q, want %q", got, "github_copilot")
	}
	if got := secrets["model"]; got != "gpt-5.4" {
		t.Errorf("secrets.model = %q, want %q", got, "gpt-5.4")
	}
}

func TestBuildHelmValues_RejectsDirectProviderOverride(t *testing.T) {
	instance := newTestInstance("admin", "Admin", "", "gateway-token")
	instance.Spec.Model = "gpt-6-astra"
	instance.Spec.Helm.Values["secrets"] = runtime.RawExtension{
		Raw: []byte(`{"provider":"openai","model":"gpt-5.4"}`),
	}
	if _, err := buildHelmValues(instance, nil, "example.com", "", nil, "", "gpt-6-astra"); err == nil {
		t.Fatal("direct model provider override bypassed AI Gateway routing")
	}
}

func TestBuildHelmValues_AgentType(t *testing.T) {
	instance := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")
	instance.Spec.Agent.Type = cetirunv1alpha1.AgentTypeHermes
	values, err := buildHelmValues(instance, nil, "example.com", "", nil, "")
	if err != nil {
		t.Fatalf("buildHelmValues error: %v", err)
	}
	if got := values["agentType"]; got != string(cetirunv1alpha1.AgentTypeHermes) {
		t.Errorf("agentType = %q, want %q", got, cetirunv1alpha1.AgentTypeHermes)
	}
}

func TestBuildHelmValues_HomeStorage(t *testing.T) {
	for _, agentType := range []cetirunv1alpha1.AgentType{cetirunv1alpha1.AgentTypeHermes, cetirunv1alpha1.AgentTypeOpenClaw} {
		for _, test := range []struct {
			name     string
			global   map[string]interface{}
			local    string
			expected map[string]interface{}
		}{
			{"defaults", nil, "", map[string]interface{}{"storageClass": "default", "accessMode": "ReadWriteOnce", "size": "100Gi"}},
			{"operator override", map[string]interface{}{"persistence": map[string]interface{}{"size": "200Gi"}}, "",
				map[string]interface{}{"storageClass": "default", "accessMode": "ReadWriteOnce", "size": "200Gi"}},
			{"instance override", map[string]interface{}{"persistence": map[string]interface{}{"size": "200Gi"}},
				`{"storageClass":"approved-home","accessMode":"ReadWriteMany"}`,
				map[string]interface{}{"storageClass": "approved-home", "accessMode": "ReadWriteMany", "size": "200Gi"}},
		} {
			t.Run(string(agentType)+"/"+test.name, func(t *testing.T) {
				instance := newTestInstance("alice", "Alice", "", "gateway-token")
				instance.Spec.Agent.Type = agentType
				if test.local != "" {
					instance.Spec.Helm.Values["persistence"] = runtime.RawExtension{Raw: []byte(test.local)}
				}
				values, err := buildHelmValues(instance, nil, "example.com", "", test.global, "")
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(values["persistence"], test.expected) {
					t.Errorf("persistence = %v, want %v", values["persistence"], test.expected)
				}
			})
		}
	}
}

func TestBuildHelmValues_OpenClawControlUIBasePath(t *testing.T) {
	instance := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")
	instance.Name = "alice-work"
	instance.Spec.Helm.Values["openclaw"] = runtime.RawExtension{
		Raw: []byte(`{"config":{"gateway":{"controlUi":{"basePath":"/user-value"}}}}`),
	}

	values, err := buildHelmValues(instance, nil, "example.com", "", nil, "")
	if err != nil {
		t.Fatalf("buildHelmValues error: %v", err)
	}
	openclaw := values["openclaw"].(map[string]interface{})
	config := openclaw["config"].(map[string]interface{})
	gateway := config["gateway"].(map[string]interface{})
	controlUI := gateway["controlUi"].(map[string]interface{})
	if got := controlUI["basePath"]; got != "/ui/alice-work" {
		t.Errorf("controlUi.basePath = %q, want %q", got, "/ui/alice-work")
	}
}

func TestBuildHelmValues_HermesDoesNotSetOpenClawControlUIBasePath(t *testing.T) {
	instance := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")
	instance.Spec.Agent.Type = cetirunv1alpha1.AgentTypeHermes

	values, err := buildHelmValues(instance, nil, "example.com", "", nil, "")
	if err != nil {
		t.Fatalf("buildHelmValues error: %v", err)
	}
	if _, ok := values["openclaw"]; ok {
		t.Error("values[\"openclaw\"] should not be set for Hermes")
	}
}

// TestBuildHelmValues_NoApiKeyOrBase verifies that apiKey and apiBase are not set
// (GitHub Copilot uses OAuth device flow, not API keys).
func TestBuildHelmValues_NoApiKeyOrBase(t *testing.T) {
	instance := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")
	values, err := buildHelmValues(instance, nil, "example.com", "", nil, "")
	if err != nil {
		t.Fatalf("buildHelmValues error: %v", err)
	}
	secrets, ok := values["secrets"].(map[string]interface{})
	if !ok {
		t.Fatal("values[\"secrets\"] missing or wrong type")
	}
	if _, ok := secrets["apiKey"]; ok {
		t.Error("secrets[\"apiKey\"] should not be set for github_copilot provider")
	}
	if _, ok := secrets["apiBase"]; ok {
		t.Error("secrets[\"apiBase\"] should not be set for github_copilot provider")
	}
}

// TestBuildHelmValues_NoMemorySearchModel verifies that adding the Control UI
// base path does not set a memory-search model (the chart owns that setting).
func TestBuildHelmValues_NoMemorySearchModel(t *testing.T) {
	instance := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")
	values, err := buildHelmValues(instance, nil, "example.com", "", nil, "")
	if err != nil {
		t.Fatalf("buildHelmValues error: %v", err)
	}
	openclaw := values["openclaw"].(map[string]interface{})
	config := openclaw["config"].(map[string]interface{})
	if _, ok := config["memorySearch"]; ok {
		t.Error("openclaw.config.memorySearch should not be set; memory search is auto-configured by chart for copilot")
	}
}

// TestIsConditionTrue verifies the isConditionTrue helper.
func TestIsConditionTrue(t *testing.T) {
	tests := []struct {
		name       string
		conditions []metav1.Condition
		condType   string
		want       bool
	}{
		{
			name: "condition present with True",
			conditions: []metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionTrue},
			},
			condType: "Ready",
			want:     true,
		},
		{
			name: "condition present with False",
			conditions: []metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionFalse},
			},
			condType: "Ready",
			want:     false,
		},
		{
			name: "condition absent",
			conditions: []metav1.Condition{
				{Type: "Other", Status: metav1.ConditionTrue},
			},
			condType: "Ready",
			want:     false,
		},
		{
			name:       "empty slice",
			conditions: []metav1.Condition{},
			condType:   "Ready",
			want:       false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isConditionTrue(tc.conditions, tc.condType); got != tc.want {
				t.Errorf("isConditionTrue() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBuildHelmValues_NoTailscaleInHelmValues verifies that buildHelmValues never injects
// tailscaleAuthKey into Helm values.
func TestBuildHelmValues_NoTailscaleInHelmValues(t *testing.T) {
	instance := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")
	instance.Spec.Tailscale = &cetirunv1alpha1.TailscaleSpec{
		AuthKeySecretRef: "tailscale-secret",
	}

	values, err := buildHelmValues(instance, nil, "example.com", "", nil, "")
	if err != nil {
		t.Fatalf("buildHelmValues returned error: %v", err)
	}

	secrets, ok := values["secrets"].(map[string]interface{})
	if !ok {
		t.Fatal("values[\"secrets\"] missing or wrong type")
	}
	if _, ok := secrets["tailscaleAuthKey"]; ok {
		t.Error("secrets[\"tailscaleAuthKey\"] should not be in Helm values; auth key is written directly to the gateway Secret")
	}
}

// TestBuildHelmValues_UserIdentityPropagated verifies that spec.user fields are
// passed through as Helm user.* values for USER.md templating in the ConfigMap.
func TestBuildHelmValues_UserIdentityPropagated(t *testing.T) {
	instance := newTestInstance("example-user", "Example User", "other-team", "gateway-token")
	values, err := buildHelmValues(instance, nil, "example.com", "", nil, "")
	if err != nil {
		t.Fatalf("buildHelmValues returned error: %v", err)
	}

	user, ok := values["user"].(map[string]interface{})
	if !ok {
		t.Fatal("values[\"user\"] missing or wrong type")
	}
	if got := user["alias"]; got != "example-user" {
		t.Errorf("user.alias = %q, want %q", got, "example-user")
	}
	if got := user["displayName"]; got != "Example User" {
		t.Errorf("user.displayName = %q, want %q", got, "Example User")
	}
	if got := user["team"]; got != "other-team" {
		t.Errorf("user.team = %q, want %q", got, "other-team")
	}
	if got := user["baseDomain"]; got != "example.com" {
		t.Errorf("user.baseDomain = %q, want %q", got, "example.com")
	}
}

// TestBuildHelmValues_ProviderDefaults verifies that secrets.provider and
// secrets.model are set to configured GitHub Copilot defaults.
func TestBuildHelmValues_ProviderDefaults(t *testing.T) {
	instance := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")
	values, err := buildHelmValues(instance, nil, "example.com", "", nil, "", "gpt-5.4")
	if err != nil {
		t.Fatalf("buildHelmValues returned error: %v", err)
	}

	secrets, ok := values["secrets"].(map[string]interface{})
	if !ok {
		t.Fatal("values[\"secrets\"] missing or wrong type")
	}
	if got := secrets["provider"]; got != "github_copilot" {
		t.Errorf("secrets.provider = %q, want %q", got, "github_copilot")
	}
	if got := secrets["model"]; got != "gpt-5.4" {
		t.Errorf("secrets.model = %q, want %q", got, "gpt-5.4")
	}
}

// TestBuildHelmValues_NetworkPolicyIstioNamespaces verifies that AKS-specific
// Istio namespace values are set for the NetworkPolicy.
func TestBuildHelmValues_NetworkPolicyIstioNamespaces(t *testing.T) {
	instance := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")
	values, err := buildHelmValues(instance, nil, "example.com", "", nil, "")
	if err != nil {
		t.Fatalf("buildHelmValues returned error: %v", err)
	}

	np, ok := values["networkPolicy"].(map[string]interface{})
	if !ok {
		t.Fatal("values[\"networkPolicy\"] missing or wrong type")
	}
	if got := np["istioNamespace"]; got != "aks-istio-system" {
		t.Errorf("networkPolicy.istioNamespace = %q, want %q", got, "aks-istio-system")
	}
	if got := np["istioIngressNamespace"]; got != "aks-istio-ingress" {
		t.Errorf("networkPolicy.istioIngressNamespace = %q, want %q", got, "aks-istio-ingress")
	}
}

// TestBuildHelmValues_TailscaleServeEnabled verifies that when spec.Tailscale is set,
// tailscale.serve=true is injected into the Helm values.
func TestBuildHelmValues_TailscaleServeEnabled(t *testing.T) {
	instance := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")
	instance.Spec.Tailscale = &cetirunv1alpha1.TailscaleSpec{
		AuthKeySecretRef: "tailscale-secret",
	}

	values, err := buildHelmValues(instance, nil, "example.com", "", nil, "")
	if err != nil {
		t.Fatalf("buildHelmValues error: %v", err)
	}

	ts, ok := values["tailscale"].(map[string]interface{})
	if !ok {
		t.Fatal("values[\"tailscale\"] missing or wrong type")
	}
	if got := ts["serve"]; got != true {
		t.Errorf("tailscale.serve = %v, want true", got)
	}
}

// TestBuildHelmValues_NoTailscaleWhenNotConfigured verifies that when spec.Tailscale is nil,
// no tailscale key appears in the Helm values.
func TestBuildHelmValues_NoTailscaleWhenNotConfigured(t *testing.T) {
	instance := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")
	// spec.Tailscale is nil by default from newTestInstance

	values, err := buildHelmValues(instance, nil, "example.com", "", nil, "")
	if err != nil {
		t.Fatalf("buildHelmValues error: %v", err)
	}

	if _, ok := values["tailscale"]; ok {
		t.Error("values[\"tailscale\"] should not be set when spec.Tailscale is nil")
	}
}

// TestBuildHelmValues_TailscaleServeOverriddenByHelmValues verifies that user-supplied
// helm.values can deep-merge with the auto-injected tailscale.serve=true, e.g. to add
// additional tailscale-related Helm values alongside the operator default.
func TestBuildHelmValues_TailscaleServeOverriddenByHelmValues(t *testing.T) {
	instance := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")
	instance.Spec.Tailscale = &cetirunv1alpha1.TailscaleSpec{
		AuthKeySecretRef: "tailscale-secret",
	}
	// User override: set tailscale.hostname via helm.values
	instance.Spec.Helm.Values["tailscale"] = runtime.RawExtension{
		Raw: []byte(`{"hostname":"my-custom-host"}`),
	}

	values, err := buildHelmValues(instance, nil, "example.com", "", nil, "")
	if err != nil {
		t.Fatalf("buildHelmValues error: %v", err)
	}

	ts, ok := values["tailscale"].(map[string]interface{})
	if !ok {
		t.Fatal("values[\"tailscale\"] missing or wrong type")
	}
	// The operator default (serve=true) should be preserved after deep merge.
	if got := ts["serve"]; got != true {
		t.Errorf("tailscale.serve = %v, want true (should survive deep merge)", got)
	}
	// The user override should also be present.
	if got := ts["hostname"]; got != "my-custom-host" {
		t.Errorf("tailscale.hostname = %v, want %q", got, "my-custom-host")
	}
}

// assertWebhookDenyRoute asserts that route r is the non-Teams webhook deny route:
// an exact POST match on wantPath that returns a direct 404 with no upstream.
func assertWebhookDenyRoute(t *testing.T, r *istioapiv1beta1.HTTPRoute, wantPath string) {
	t.Helper()
	if exact := r.Match[0].GetUri().GetExact(); exact != wantPath {
		t.Errorf("deny route: URI exact = %q, want %q", exact, wantPath)
	}
	if method := r.Match[0].GetMethod().GetExact(); method != "POST" {
		t.Errorf("deny route: method = %q, want POST", method)
	}
	if r.DirectResponse == nil || r.DirectResponse.Status != 404 {
		t.Errorf("deny route: expected DirectResponse 404, got %+v", r.DirectResponse)
	}
	if len(r.Route) != 0 {
		t.Errorf("deny route: expected no upstream destination, got %d", len(r.Route))
	}
}

// TestBuildVirtualService_Routes verifies that buildVirtualService produces the
// expected HTTP routes for the OpenClaw branch:
// /ui/<instanceName> WebSocket Upgrade → OpenClaw Gateway (port 18789),
// /ui/<instanceName> HTTP → canonical trailing slash redirect,
// /<instanceName>/desktop → noVNC (port 6080, rewrite to /desktop),
// /<instanceName>/term → ttyd (port 7681, rewrite to /term),
// /<instanceName>/cetirun-auth/ → in-pod auth helper (port 18790, rewrite to /),
// /ui/<instanceName>/ → OpenClaw Gateway (port 18789, URI preserved for basePath).
func TestBuildVirtualService_Routes(t *testing.T) {
	instance := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")
	instance.Name = "alice"
	domainSuffix := "example.com"

	vs := buildVirtualService(instance, domainSuffix, DefaultIstioGateway)

	// VS must bind to the apex domain, not a subdomain
	if len(vs.Spec.Hosts) != 1 || vs.Spec.Hosts[0] != domainSuffix {
		t.Fatalf("VS hosts = %v, want [%q]", vs.Spec.Hosts, domainSuffix)
	}

	// Routes: 1 webhook deny route (non-Teams instance, prepended first)
	//       + 1 exact-path WebSocket route for the OpenClaw Control UI
	//       + 3 redirects (instance root, desktop, term without trailing slash)
	//       + 4 proxy routes (cetirun-auth, desktop, term, native Control UI)
	if len(vs.Spec.Http) != 9 {
		t.Fatalf("expected 9 HTTP routes, got %d", len(vs.Spec.Http))
	}

	// Route 0: webhook deny — POST /alice/api/messages → direct 404 (Teams disabled).
	assertWebhookDenyRoute(t, vs.Spec.Http[0], "/alice/api/messages")

	// Route 1: the Control UI removes the trailing slash when constructing its
	// WebSocket URL. The Upgrade request must reach the gateway rather than the
	// canonical HTTP redirect because browsers do not follow WebSocket redirects.
	r1 := vs.Spec.Http[1]
	if exact := r1.Match[0].GetUri().GetExact(); exact != "/ui/alice" {
		t.Errorf("route 1: URI exact = %q, want /ui/alice", exact)
	}
	if upgrade := r1.Match[0].Headers["upgrade"].GetExact(); upgrade != "websocket" {
		t.Errorf("route 1: Upgrade header match = %q, want websocket", upgrade)
	}
	if port := r1.Route[0].Destination.Port.Number; port != openclawPort {
		t.Errorf("route 1: destination port = %d, want %d", port, openclawPort)
	}

	// Routes 2-4: trailing-slash redirects
	for i, path := range []string{"/ui/alice", "/alice/desktop", "/alice/term"} {
		r := vs.Spec.Http[i+2]
		if exact := r.Match[0].GetUri().GetExact(); exact != path {
			t.Errorf("route %d: URI exact = %q, want %q", i+2, exact, path)
		}
		if r.Redirect == nil {
			t.Errorf("route %d: expected redirect", i+2)
		}
	}

	// Route 5: /alice/cetirun-auth/ → in-pod auth helper, rewrite to /
	r5 := vs.Spec.Http[5]
	if prefix := r5.Match[0].GetUri().GetPrefix(); prefix != "/alice/cetirun-auth/" {
		t.Errorf("route 5: cetirun-auth prefix = %q, want /alice/cetirun-auth/", prefix)
	}
	if r5.Rewrite == nil || r5.Rewrite.GetUri() != "/" {
		t.Errorf("route 5: rewrite = %v, want /", r5.Rewrite)
	}
	if port := r5.Route[0].Destination.Port.Number; port != cetirunAuthPort {
		t.Errorf("route 5: destination port = %d, want %d", port, cetirunAuthPort)
	}

	// Route 6: /alice/desktop → noVNC with rewrite to /desktop
	r6 := vs.Spec.Http[6]
	if prefix := r6.Match[0].GetUri().GetPrefix(); prefix != "/alice/desktop" {
		t.Errorf("route 6: URI prefix = %q, want %q", prefix, "/alice/desktop")
	}
	if r6.Rewrite == nil || r6.Rewrite.Uri != "/desktop" {
		t.Errorf("route 6: rewrite = %v, want /desktop", r6.Rewrite)
	}
	if port := r6.Route[0].Destination.Port.Number; port != novncPort {
		t.Errorf("route 6: destination port = %d, want %d", port, novncPort)
	}

	// Route 7: /alice/term → ttyd with rewrite to /term
	r7 := vs.Spec.Http[7]
	if prefix := r7.Match[0].GetUri().GetPrefix(); prefix != "/alice/term" {
		t.Errorf("route 7: URI prefix = %q, want %q", prefix, "/alice/term")
	}
	if r7.Rewrite == nil || r7.Rewrite.Uri != "/term" {
		t.Errorf("route 7: rewrite = %v, want /term", r7.Rewrite)
	}
	if port := r7.Route[0].Destination.Port.Number; port != ttydPort {
		t.Errorf("route 7: destination port = %d, want %d", port, ttydPort)
	}

	// Route 8: /ui/alice/ → OpenClaw Control UI. The URI remains prefixed because
	// the gateway is configured with controlUi.basePath=/ui/alice.
	r8 := vs.Spec.Http[8]
	if prefix := r8.Match[0].GetUri().GetPrefix(); prefix != "/ui/alice/" {
		t.Errorf("route 8: URI prefix = %q, want %q", prefix, "/ui/alice/")
	}
	if r8.Rewrite != nil {
		t.Errorf("route 8: rewrite = %v, want nil", r8.Rewrite)
	}
	if port := r8.Route[0].Destination.Port.Number; port != openclawPort {
		t.Errorf("route 8: destination port = %d, want %d", port, openclawPort)
	}
	if r8.Timeout == nil {
		t.Error("route 8: expected Timeout to be set")
	}
	if r8.Retries == nil {
		t.Error("route 8: expected Retries to be set")
	}
}

func TestBuildVirtualService_UsesConfiguredGateway(t *testing.T) {
	instance := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")
	configuredGateway := "custom-namespace/custom-gateway"

	vs := buildVirtualService(instance, "example.com", configuredGateway)
	if len(vs.Spec.Gateways) != 1 || vs.Spec.Gateways[0] != configuredGateway {
		t.Fatalf("VS gateways = %v, want [%q]", vs.Spec.Gateways, configuredGateway)
	}
}

func TestBuildVirtualService_HermesRoutes(t *testing.T) {
	instance := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")
	instance.Name = "alice"
	instance.Spec.Agent.Type = cetirunv1alpha1.AgentTypeHermes

	vs := buildVirtualService(instance, "example.com", DefaultIstioGateway)

	// 1 webhook deny route (non-Teams, prepended) + 3 redirects (root, desktop, term)
	// + 6 proxy routes (v1, health, cetirun-auth, desktop, term, catch-all dashboard)
	if len(vs.Spec.Http) != 10 {
		t.Fatalf("expected 10 HTTP routes, got %d", len(vs.Spec.Http))
	}

	// Route 0: webhook deny — POST /alice/api/messages → direct 404 (Teams disabled).
	assertWebhookDenyRoute(t, vs.Spec.Http[0], "/alice/api/messages")

	for i, path := range []string{"/ui/alice", "/alice/desktop", "/alice/term"} {
		r := vs.Spec.Http[i+1]
		if exact := r.Match[0].GetUri().GetExact(); exact != path {
			t.Errorf("route %d: URI exact = %q, want %q", i+1, exact, path)
		}
		if r.Redirect == nil {
			t.Errorf("route %d: expected redirect", i+1)
		}
	}
	// /v1 → Hermes API
	if port := vs.Spec.Http[4].Route[0].Destination.Port.Number; port != hermesAPIPort {
		t.Errorf("v1 route port = %d, want %d", port, hermesAPIPort)
	}
	// /health → Hermes API
	if port := vs.Spec.Http[5].Route[0].Destination.Port.Number; port != hermesAPIPort {
		t.Errorf("health route port = %d, want %d", port, hermesAPIPort)
	}
	// /cetirun-auth/ → in-pod auth helper service (rewritten to /)
	authRoute := vs.Spec.Http[6]
	if prefix := authRoute.Match[0].GetUri().GetPrefix(); prefix != "/alice/cetirun-auth/" {
		t.Errorf("cetirun-auth prefix = %q, want /alice/cetirun-auth/", prefix)
	}
	if rewrite := authRoute.Rewrite.GetUri(); rewrite != "/" {
		t.Errorf("cetirun-auth rewrite = %q, want /", rewrite)
	}
	if port := authRoute.Route[0].Destination.Port.Number; port != cetirunAuthPort {
		t.Errorf("cetirun-auth port = %d, want %d", port, cetirunAuthPort)
	}
	// /desktop → noVNC
	if port := vs.Spec.Http[7].Route[0].Destination.Port.Number; port != novncPort {
		t.Errorf("desktop route port = %d, want %d", port, novncPort)
	}
	// /term → ttyd
	if port := vs.Spec.Http[8].Route[0].Destination.Port.Number; port != ttydPort {
		t.Errorf("term route port = %d, want %d", port, ttydPort)
	}
	// catch-all → native Dashboard (must come AFTER /cetirun-auth so it doesn't swallow auth requests)
	catchAll := vs.Spec.Http[9]
	if prefix := catchAll.Match[0].GetUri().GetPrefix(); prefix != "/ui/alice/" {
		t.Errorf("catch-all prefix = %q, want /ui/alice/", prefix)
	}
	if rewrite := catchAll.Rewrite.GetUri(); rewrite != "/" {
		t.Errorf("catch-all rewrite = %q, want /", rewrite)
	}
	if port := catchAll.Route[0].Destination.Port.Number; port != hermesDashboardPort {
		t.Errorf("catch-all (dashboard) port = %d, want %d", port, hermesDashboardPort)
	}
	if got := catchAll.Headers.GetRequest().GetAdd()["x-forwarded-prefix"]; got != "/ui/alice" {
		t.Errorf("catch-all x-forwarded-prefix = %q, want /ui/alice", got)
	}
}

// TestSecretContentHash verifies determinism, uniqueness, and empty-data handling.
func TestSecretContentHash(t *testing.T) {
	t.Run("empty data returns empty string", func(t *testing.T) {
		if got := secretContentHash(nil); got != "" {
			t.Errorf("secretContentHash(nil) = %q, want empty", got)
		}
		if got := secretContentHash(map[string][]byte{}); got != "" {
			t.Errorf("secretContentHash(empty) = %q, want empty", got)
		}
	})

	t.Run("deterministic", func(t *testing.T) {
		data := map[string][]byte{"key1": []byte("val1"), "key2": []byte("val2")}
		h1 := secretContentHash(data)
		h2 := secretContentHash(data)
		if h1 != h2 {
			t.Errorf("not deterministic: %q vs %q", h1, h2)
		}
	})

	t.Run("different data produces different hash", func(t *testing.T) {
		d1 := map[string][]byte{"token": []byte("abc")}
		d2 := map[string][]byte{"token": []byte("xyz")}
		if secretContentHash(d1) == secretContentHash(d2) {
			t.Error("different data produced same hash")
		}
	})

	t.Run("key order does not matter", func(t *testing.T) {
		d1 := map[string][]byte{"a": []byte("1"), "b": []byte("2")}
		d2 := map[string][]byte{"b": []byte("2"), "a": []byte("1")}
		if secretContentHash(d1) != secretContentHash(d2) {
			t.Error("same data in different key order produced different hashes")
		}
	})
}

func TestBuildHelmValues_SkillsStorageAccountOverride(t *testing.T) {
	instance := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")

	values, err := buildHelmValues(instance, nil, "example.com", "cetirunskills", nil, "")
	if err != nil {
		t.Fatalf("buildHelmValues error: %v", err)
	}

	// Verify the skills storage account override is applied
	skills, ok := values["skills"].(map[string]interface{})
	if !ok {
		t.Fatal("skills key not found or not a map")
	}
	if sa := skills["storageAccount"]; sa != "cetirunskills" {
		t.Errorf("skills.storageAccount = %q, want %q", sa, "cetirunskills")
	}
}

func TestBuildHelmValues_NoSkillsStorageAccountWhenEmpty(t *testing.T) {
	instance := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")

	values, err := buildHelmValues(instance, nil, "example.com", "", nil, "")
	if err != nil {
		t.Fatalf("buildHelmValues error: %v", err)
	}

	// When empty, skills key should not be set (chart default applies)
	if _, ok := values["skills"]; ok {
		t.Error("values[\"skills\"] should not be set when skillsStorageAccount is empty")
	}
}

func TestBuildHelmValues_InstanceOverridesSkillsStorageAccount(t *testing.T) {
	instance := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")
	instance.Spec.Helm.Values = map[string]runtime.RawExtension{
		"skills": {Raw: []byte(`{"storageAccount":"custom-account"}`)},
	}

	values, err := buildHelmValues(instance, nil, "example.com", "cetirunskills", nil, "")
	if err != nil {
		t.Fatalf("buildHelmValues error: %v", err)
	}

	// Per-instance helm.values should take precedence over operator-level override
	skills, ok := values["skills"].(map[string]interface{})
	if !ok {
		t.Fatal("skills key not found or not a map")
	}
	if sa := skills["storageAccount"]; sa != "custom-account" {
		t.Errorf("skills.storageAccount = %q, want %q (instance override should win)", sa, "custom-account")
	}
}

// TestOperatorConfigHash_IncludesSkillsStorageAccount verifies that changing the
// skills storage account produces a different operatorConfigHash.
func TestOperatorConfigHash_IncludesSkillsStorageAccount(t *testing.T) {
	hEmpty := operatorConfigHash("example.com", "secret1", "", "", "")
	hA := operatorConfigHash("example.com", "secret1", "storageaccounta", "", "")
	hB := operatorConfigHash("example.com", "secret1", "storageaccountb", "", "")

	if hEmpty == hA {
		t.Errorf("hash with empty storageAccount (%q) should differ from hash with storageaccounta (%q)", hEmpty, hA)
	}
	if hA == hB {
		t.Errorf("hash with storageaccounta (%q) should differ from hash with storageaccountb (%q)", hA, hB)
	}

	// Deterministic: same value should produce the same hash.
	hA2 := operatorConfigHash("example.com", "secret1", "storageaccounta", "", "")
	if hA != hA2 {
		t.Errorf("operatorConfigHash not deterministic: %q vs %q", hA, hA2)
	}
}

// TestBuildHelmValues_CetiRunHelmValues verifies that operator-level helm values
// are merged between hardcoded defaults and per-instance overrides.
func TestBuildHelmValues_CetiRunHelmValues(t *testing.T) {
	instance := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")

	operatorValues := map[string]interface{}{
		"skills": map[string]interface{}{
			"shared":         true,
			"resourceGroup":  "my-rg",
			"storageAccount": "cetirunskills",
		},
	}

	values, err := buildHelmValues(instance, nil, "example.com", "", operatorValues, "")
	if err != nil {
		t.Fatalf("buildHelmValues error: %v", err)
	}

	skills, ok := values["skills"].(map[string]interface{})
	if !ok {
		t.Fatal("skills key not found or not a map")
	}
	if got := skills["shared"]; got != true {
		t.Errorf("skills.shared = %v, want true", got)
	}
	if got := skills["resourceGroup"]; got != "my-rg" {
		t.Errorf("skills.resourceGroup = %q, want %q", got, "my-rg")
	}
	if got := skills["storageAccount"]; got != "cetirunskills" {
		t.Errorf("skills.storageAccount = %q, want %q", got, "cetirunskills")
	}
}

// TestBuildHelmValues_CetiRunHelmValuesOverriddenByInstance verifies that
// per-instance spec.helm.values takes precedence over operator-level values.
func TestBuildHelmValues_CetiRunHelmValuesOverriddenByInstance(t *testing.T) {
	instance := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")
	instance.Spec.Helm.Values = map[string]runtime.RawExtension{
		"skills": {Raw: []byte(`{"storageAccount":"custom-account","shared":false}`)},
	}

	operatorValues := map[string]interface{}{
		"skills": map[string]interface{}{
			"shared":         true,
			"resourceGroup":  "my-rg",
			"storageAccount": "cetirunskills",
		},
	}

	values, err := buildHelmValues(instance, nil, "example.com", "", operatorValues, "")
	if err != nil {
		t.Fatalf("buildHelmValues error: %v", err)
	}

	skills, ok := values["skills"].(map[string]interface{})
	if !ok {
		t.Fatal("skills key not found or not a map")
	}
	// Per-instance override should win
	if got := skills["storageAccount"]; got != "custom-account" {
		t.Errorf("skills.storageAccount = %q, want %q (instance override should win)", got, "custom-account")
	}
	if got := skills["shared"]; got != false {
		t.Errorf("skills.shared = %v, want false (instance override should win)", got)
	}
	// resourceGroup from operator-level should persist since instance didn't override it
	if got := skills["resourceGroup"]; got != "my-rg" {
		t.Errorf("skills.resourceGroup = %q, want %q (operator value should persist)", got, "my-rg")
	}
}

// TestBuildHelmValues_SkillsStorageAccountOverridesCetiRunHelmValues verifies that
// the legacy SKILLS_STORAGE_ACCOUNT takes precedence over cetirunHelmValues.skills.storageAccount.
func TestBuildHelmValues_SkillsStorageAccountOverridesCetiRunHelmValues(t *testing.T) {
	instance := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")

	operatorValues := map[string]interface{}{
		"skills": map[string]interface{}{
			"shared":         true,
			"resourceGroup":  "my-rg",
			"storageAccount": "from-operator-values",
		},
	}

	values, err := buildHelmValues(instance, nil, "example.com", "from-env-var", operatorValues, "")
	if err != nil {
		t.Fatalf("buildHelmValues error: %v", err)
	}

	skills, ok := values["skills"].(map[string]interface{})
	if !ok {
		t.Fatal("skills key not found or not a map")
	}
	// SKILLS_STORAGE_ACCOUNT env var should override cetirunHelmValues
	if got := skills["storageAccount"]; got != "from-env-var" {
		t.Errorf("skills.storageAccount = %q, want %q (env var should win)", got, "from-env-var")
	}
	// Other keys from operator values should persist
	if got := skills["shared"]; got != true {
		t.Errorf("skills.shared = %v, want true", got)
	}
	if got := skills["resourceGroup"]; got != "my-rg" {
		t.Errorf("skills.resourceGroup = %q, want %q", got, "my-rg")
	}
}

// TestOperatorConfigHash_IncludesCetiRunHelmValues verifies that changing
// cetirunHelmValues produces a different operatorConfigHash.
func TestOperatorConfigHash_IncludesCetiRunHelmValues(t *testing.T) {
	hEmpty := operatorConfigHash("example.com", "secret1", "", "", "")
	hA := operatorConfigHash("example.com", "secret1", "", "hashA", "")
	hB := operatorConfigHash("example.com", "secret1", "", "hashB", "")

	if hEmpty == hA {
		t.Errorf("hash with empty helmValues (%q) should differ from hash with hashA (%q)", hEmpty, hA)
	}
	if hA == hB {
		t.Errorf("hash with hashA (%q) should differ from hash with hashB (%q)", hA, hB)
	}
}

// TestBuildHelmValues_ImageRepositoryFromChartRegistry verifies that when chartRegistry
// is set, agent image repositories are overridden to pull from the correct ACR.
func TestBuildHelmValues_ImageRepositoryFromChartRegistry(t *testing.T) {
	instance := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")
	values, err := buildHelmValues(instance, nil, "example.com", "", nil, "registry.example.com")
	if err != nil {
		t.Fatalf("buildHelmValues error: %v", err)
	}
	openclaw, ok := values["openclaw"].(map[string]interface{})
	if !ok {
		t.Fatal("values[\"openclaw\"] missing or wrong type")
	}
	image, ok := openclaw["image"].(map[string]interface{})
	if !ok {
		t.Fatal("values[\"openclaw\"].image missing or wrong type")
	}
	if got := image["repository"]; got != "registry.example.com/openclaw-gateway" {
		t.Errorf("openclaw.image.repository = %q, want %q", got, "registry.example.com/openclaw-gateway")
	}
	hermes, ok := values["hermes"].(map[string]interface{})
	if !ok {
		t.Fatal("values[\"hermes\"] missing or wrong type")
	}
	hermesImage, ok := hermes["image"].(map[string]interface{})
	if !ok {
		t.Fatal("values[\"hermes\"].image missing or wrong type")
	}
	if got := hermesImage["repository"]; got != "registry.example.com/hermes-gateway" {
		t.Errorf("hermes.image.repository = %q, want %q", got, "registry.example.com/hermes-gateway")
	}
}

// TestBuildHelmValues_ImageRepositoryNotSetWhenNoChartRegistry verifies that when
// chartRegistry is empty, image.repository is not overridden (uses chart default).
func TestBuildHelmValues_ImageRepositoryNotSetWhenNoChartRegistry(t *testing.T) {
	instance := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")
	values, err := buildHelmValues(instance, nil, "example.com", "", nil, "")
	if err != nil {
		t.Fatalf("buildHelmValues error: %v", err)
	}
	openclaw := values["openclaw"].(map[string]interface{})
	if _, ok := openclaw["image"]; ok {
		t.Error("openclaw.image should not be set when chartRegistry is empty")
	}
	if _, ok := values["hermes"]; ok {
		t.Error("values[\"hermes\"] should not be set when chartRegistry is empty")
	}
}

// TestBuildHelmValues_ImageRepositoryMergedWithTeams verifies that when both
// chartRegistry is set and Teams is enabled, image.repository is set without tagSuffix.
func TestBuildHelmValues_ImageRepositoryMergedWithTeams(t *testing.T) {
	instance := newTestInstance("bob", "Bob Builder", "aks-ai", "gateway-token")
	instance.Spec.MSTeams = &cetirunv1alpha1.MSTeamsSpec{
		Enabled: true,
		AppID:   "test-app-id",
	}
	values, err := buildHelmValues(instance, nil, "example.com", "", nil, "registry.example.com")
	if err != nil {
		t.Fatalf("buildHelmValues error: %v", err)
	}
	openclaw, ok := values["openclaw"].(map[string]interface{})
	if !ok {
		t.Fatal("values[\"openclaw\"] missing or wrong type")
	}
	image, ok := openclaw["image"].(map[string]interface{})
	if !ok {
		t.Fatal("values[\"openclaw\"].image missing or wrong type")
	}
	if got := image["repository"]; got != "registry.example.com/openclaw-gateway" {
		t.Errorf("openclaw.image.repository = %q, want %q", got, "registry.example.com/openclaw-gateway")
	}
	if _, ok := image["tagSuffix"]; ok {
		t.Error("image.tagSuffix should not be set (Teams uses the same image now)")
	}
}

func TestBuildVirtualService_MSTeamsRoute(t *testing.T) {
	instance := newTestInstance("bob", "Bob Builder", "aks-ai", "gateway-token")
	instance.Name = "bob"
	instance.Spec.MSTeams = &cetirunv1alpha1.MSTeamsSpec{
		Enabled:  true,
		AppID:    "00000000-0000-0000-0000-000000000001",
		TenantID: "00000000-0000-0000-0000-000000000002",
	}
	domainSuffix := "example.com"

	vs := buildVirtualService(instance, domainSuffix, DefaultIstioGateway)

	// Should have 9 routes: msteams webhook + WebSocket route + 3 redirects + cetirun-auth + 3 proxy routes.
	if len(vs.Spec.Http) != 9 {
		t.Fatalf("expected 9 HTTP routes, got %d", len(vs.Spec.Http))
	}

	// VS must bind to the apex domain
	if len(vs.Spec.Hosts) != 1 || vs.Spec.Hosts[0] != domainSuffix {
		t.Errorf("VS hosts = %v, want [%q]", vs.Spec.Hosts, domainSuffix)
	}

	// The first route (index 0) should be the msteams webhook route (prepended).
	r0 := vs.Spec.Http[0]
	if exact := r0.Match[0].GetUri().GetExact(); exact != "/bob/api/messages" {
		t.Errorf("msteams route: URI exact = %q, want %q", exact, "/bob/api/messages")
	}
	if method := r0.Match[0].GetMethod().GetExact(); method != "POST" {
		t.Errorf("msteams route: method = %q, want POST", method)
	}
	if port := r0.Route[0].Destination.Port.Number; port != msteamsWebhookPort {
		t.Errorf("msteams route: port = %d, want %d", port, msteamsWebhookPort)
	}
}

func TestBuildVirtualService_NoMSTeamsRoute(t *testing.T) {
	instance := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")
	instance.Name = "alice"
	vs := buildVirtualService(instance, "example.com", DefaultIstioGateway)

	// Non-Teams instance still gets a prepended webhook deny route (1 + 8 = 9).
	if len(vs.Spec.Http) != 9 {
		t.Fatalf("expected 9 HTTP routes (deny + base), got %d", len(vs.Spec.Http))
	}

	// Route 0 must be the deny route, not a proxy/redirect, so the static gateway POST
	// bypass cannot reach the catch-all for a non-Teams instance.
	assertWebhookDenyRoute(t, vs.Spec.Http[0], "/alice/api/messages")
}

// TestBuildVirtualService_WebhookRouteIsPerInstance verifies the webhook route (Teams
// or deny) is scoped to the instance's own path and lives only in that instance's
// VirtualService — never as a shared/global suffix rule that could shadow
// another instance. A Teams-enabled instance and a non-Teams instance must each carry
// their own exact-path route bound to their own apex VirtualService.
func TestBuildVirtualService_WebhookRouteIsPerInstance(t *testing.T) {
	teams := newTestInstance("bob", "Bob Builder", "aks-ai", "gateway-token")
	teams.Name = "bob"
	teams.Spec.MSTeams = &cetirunv1alpha1.MSTeamsSpec{Enabled: true}

	plain := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")
	plain.Name = "alice"

	teamsVS := buildVirtualService(teams, "example.com", DefaultIstioGateway)
	plainVS := buildVirtualService(plain, "example.com", DefaultIstioGateway)

	// Each VS binds only to the shared apex host but matches its own instance path.
	teamsR0 := teamsVS.Spec.Http[0]
	if exact := teamsR0.Match[0].GetUri().GetExact(); exact != "/bob/api/messages" {
		t.Errorf("teams webhook route matches %q, want /bob/api/messages", exact)
	}
	if teamsR0.DirectResponse != nil {
		t.Error("teams instance webhook route must forward, not direct-respond")
	}

	plainR0 := plainVS.Spec.Http[0]
	if exact := plainR0.Match[0].GetUri().GetExact(); exact != "/alice/api/messages" {
		t.Errorf("non-teams webhook route matches %q, want /alice/api/messages", exact)
	}
	if plainR0.DirectResponse == nil {
		t.Error("non-teams instance webhook route must direct-respond (deny)")
	}

	// No route may use a wildcard/global suffix match that would span instances.
	for _, vs := range []*istiov1beta1.VirtualService{teamsVS, plainVS} {
		for i, r := range vs.Spec.Http {
			if exact := r.Match[0].GetUri().GetExact(); strings.Contains(exact, "*") {
				t.Errorf("%s route %d uses wildcard exact match %q; webhook routes must be exact per-instance paths", vs.Name, i, exact)
			}
		}
	}
}

func TestBuildHelmValues_MSTeamsManagedIdentity(t *testing.T) {
	instance := newTestInstance("bob", "Bob Builder", "aks-ai", "gateway-token")
	instance.Spec.MSTeams = &cetirunv1alpha1.MSTeamsSpec{
		Enabled:  true,
		AppID:    "00000000-0000-0000-0000-000000000001",
		TenantID: "00000000-0000-0000-0000-000000000002",
	}

	vals, err := buildHelmValues(instance, nil, "example.com", "", nil, "")
	if err != nil {
		t.Fatalf("buildHelmValues: %v", err)
	}

	// Check msteamsWebhookPort.
	if port, ok := vals["msteamsWebhookPort"]; !ok || port != 3978 {
		t.Errorf("msteamsWebhookPort = %v, want 3978", port)
	}

	// Check azureWorkloadIdentity.
	awi, ok := vals["azureWorkloadIdentity"].(map[string]interface{})
	if !ok {
		t.Fatal("azureWorkloadIdentity not set")
	}
	if awi["enabled"] != true {
		t.Errorf("azureWorkloadIdentity.enabled = %v, want true", awi["enabled"])
	}
	if awi["clientId"] != "00000000-0000-0000-0000-000000000001" {
		t.Errorf("azureWorkloadIdentity.clientId = %v", awi["clientId"])
	}

	// Check msteamsAuthType Helm value (chart template converts this to env vars).
	if got := vals["msteamsAuthType"]; got != "managedIdentity" {
		t.Errorf("msteamsAuthType = %v, want managedIdentity", got)
	}
}

func TestBuildHelmValues_MSTeamsCertificate(t *testing.T) {
	// Certificate auth type has been removed; this test now verifies that
	// Teams with Enabled=true always uses managedIdentity.
	instance := newTestInstance("carol", "Carol Cert", "aks-ai", "gateway-token")
	instance.Spec.MSTeams = &cetirunv1alpha1.MSTeamsSpec{
		Enabled:  true,
		AppID:    "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		TenantID: "00000000-0000-0000-0000-000000000002",
	}

	vals, err := buildHelmValues(instance, nil, "example.com", "", nil, "")
	if err != nil {
		t.Fatalf("buildHelmValues: %v", err)
	}

	// Always managedIdentity now.
	if got := vals["msteamsAuthType"]; got != "managedIdentity" {
		t.Errorf("msteamsAuthType = %v, want managedIdentity", got)
	}

	// No extraVolumes or extraVolumeMounts for certs.
	if _, ok := vals["extraVolumes"]; ok {
		t.Error("extraVolumes should not be set (certificate auth removed)")
	}
	if _, ok := vals["extraVolumeMounts"]; ok {
		t.Error("extraVolumeMounts should not be set (certificate auth removed)")
	}
}

// TestBuildHelmValues_TeamsNoImageTagSuffix verifies that when MSTeams is enabled
// without a chartRegistry, no image override is injected (no tagSuffix, no repository).
func TestBuildHelmValues_TeamsNoImageTagSuffix(t *testing.T) {
	instance := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")
	instance.Spec.MSTeams = &cetirunv1alpha1.MSTeamsSpec{
		Enabled: true,
		AppID:   "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
	}

	values, err := buildHelmValues(instance, nil, "example.com", "", nil, "")
	if err != nil {
		t.Fatalf("buildHelmValues: %v", err)
	}

	// With no chartRegistry, Teams no longer triggers any image override
	// because the same upstream image now includes Teams support.
	if _, ok := values["image"]; ok {
		t.Error("values[\"image\"] should not be set when chartRegistry is empty (Teams merged upstream)")
	}
}

// TestBuildHelmValues_NoTeamsNoImageTag verifies that when MSTeams is not enabled,
// no image override is set (chart uses appVersion by default).
func TestBuildHelmValues_NoTeamsNoImageTag(t *testing.T) {
	instance := newTestInstance("alice", "Alice Smith", "example-team", "gateway-token")

	values, err := buildHelmValues(instance, nil, "example.com", "", nil, "")
	if err != nil {
		t.Fatalf("buildHelmValues: %v", err)
	}

	if _, ok := values["image"]; ok {
		t.Error("values[\"image\"] should not be set when Teams is not enabled")
	}
}

// TestBuildHelmValues_TeamsEmptyAppID verifies that when MSTeams is enabled but
// AppID is empty (auto-provisioned, not yet resolved), azureWorkloadIdentity is
// disabled to avoid deploying a ServiceAccount with an empty client-id annotation.
func TestBuildHelmValues_TeamsEmptyAppID(t *testing.T) {
	instance := newTestInstance("dave", "Dave Auto", "aks-ai", "gateway-token")
	instance.Spec.MSTeams = &cetirunv1alpha1.MSTeamsSpec{
		Enabled: true,
		AppID:   "", // auto-provisioned — not yet resolved
	}

	vals, err := buildHelmValues(instance, nil, "example.com", "", nil, "")
	if err != nil {
		t.Fatalf("buildHelmValues: %v", err)
	}

	awi, ok := vals["azureWorkloadIdentity"].(map[string]interface{})
	if !ok {
		t.Fatal("azureWorkloadIdentity not set")
	}
	if awi["enabled"] != false {
		t.Errorf("azureWorkloadIdentity.enabled = %v, want false (empty appId)", awi["enabled"])
	}
	if _, hasClientID := awi["clientId"]; hasClientID {
		t.Error("azureWorkloadIdentity.clientId should not be set when appId is empty")
	}
}

// TestBuildHelmValues_TeamsStatusAppID verifies that when spec.msteams.appId is empty
// but status.msteams.appId is populated (auto-provisioned, resolved), the status
// appId is used for azureWorkloadIdentity.
func TestBuildHelmValues_TeamsStatusAppID(t *testing.T) {
	instance := newTestInstance("eve", "Eve Auto", "aks-ai", "gateway-token")
	instance.Spec.MSTeams = &cetirunv1alpha1.MSTeamsSpec{
		Enabled: true,
		AppID:   "", // empty in spec
	}
	instance.Status.MSTeams = &cetirunv1alpha1.MSTeamsStatus{
		AppID: "resolved-app-id-from-status",
	}

	vals, err := buildHelmValues(instance, nil, "example.com", "", nil, "")
	if err != nil {
		t.Fatalf("buildHelmValues: %v", err)
	}

	awi, ok := vals["azureWorkloadIdentity"].(map[string]interface{})
	if !ok {
		t.Fatal("azureWorkloadIdentity not set")
	}
	if awi["enabled"] != true {
		t.Errorf("azureWorkloadIdentity.enabled = %v, want true (status appId populated)", awi["enabled"])
	}
	if awi["clientId"] != "resolved-app-id-from-status" {
		t.Errorf("azureWorkloadIdentity.clientId = %v, want resolved-app-id-from-status", awi["clientId"])
	}
}

// TestBuildHelmValues_SuspendedSetsReplicaCountZero verifies that when
// spec.suspended is true, replicaCount is set to 0.
func TestBuildHelmValues_SuspendedSetsReplicaCountZero(t *testing.T) {
	instance := newTestInstance("frank", "Frank Suspended", "example-team", "gateway-token")
	instance.Spec.Suspended = true

	vals, err := buildHelmValues(instance, nil, "example.com", "", nil, "")
	if err != nil {
		t.Fatalf("buildHelmValues: %v", err)
	}

	rc, ok := vals["replicaCount"]
	if !ok {
		t.Fatal("replicaCount not set when suspended=true")
	}
	if rc != 0 {
		t.Errorf("replicaCount = %v, want 0", rc)
	}
}

func TestInstanceKey(t *testing.T) {
	tests := []struct {
		instanceName string
		want         string
	}{
		{"alice-work", "alice-work"},
		{"bob-exp", "bob-exp"},
	}
	for _, tt := range tests {
		t.Run(tt.instanceName, func(t *testing.T) {
			if got := instanceKey(tt.instanceName); got != tt.want {
				t.Errorf("instanceKey(%q) = %q, want %q", tt.instanceName, got, tt.want)
			}
		})
	}
}

func TestAccessURLPathBased(t *testing.T) {
	got := accessURLPathBased("myinstance", "cetirun.example.com")
	want := "https://cetirun.example.com/ui/myinstance/"
	if got != want {
		t.Errorf("accessURLPathBased() = %q, want %q", got, want)
	}
}

func TestBuildVirtualServicePathBased(t *testing.T) {
	instance := &cetirunv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alice-work",
		},
		Spec: cetirunv1alpha1.AgentSpec{
			Agent: cetirunv1alpha1.HarnessSpec{Type: cetirunv1alpha1.AgentTypeOpenClaw},
			User:  cetirunv1alpha1.UserSpec{Alias: "alice"},
		},
	}
	domain := "cetirun.example.com"
	vs := buildVirtualService(instance, domain, DefaultIstioGateway)

	// VS must bind to the apex domain, not a subdomain
	if len(vs.Spec.Hosts) != 1 || vs.Spec.Hosts[0] != domain {
		t.Errorf("VS hosts = %v, want [%q]", vs.Spec.Hosts, domain)
	}

	// Must have exactly 9 HTTP routes: 1 webhook deny route (non-Teams, prepended),
	// 1 exact-path WebSocket route, 3 trailing-slash redirects, cetirun-auth, and 3 proxy routes.
	if len(vs.Spec.Http) != 9 {
		t.Fatalf("VS http routes = %d, want 9", len(vs.Spec.Http))
	}

	// Route 0: webhook deny — POST /alice-work/api/messages → direct 404.
	assertWebhookDenyRoute(t, vs.Spec.Http[0], "/alice-work/api/messages")

	// Route 1: /ui/alice-work with Upgrade: websocket routes directly to the gateway.
	r1 := vs.Spec.Http[1]
	if r1.Match[0].Uri.GetExact() != "/ui/alice-work" || r1.Match[0].Headers["upgrade"].GetExact() != "websocket" {
		t.Errorf("route[1] must match WebSocket upgrades for /ui/alice-work")
	}

	// Routes 2-4: trailing-slash redirects for the native UI, desktop, and terminal.
	for i, path := range []string{"/ui/alice-work", "/alice-work/desktop", "/alice-work/term"} {
		r := vs.Spec.Http[i+2]
		if r.Match[0].Uri.GetExact() != path {
			t.Errorf("redirect route[%d] exact = %q, want %q", i+2, r.Match[0].Uri.GetExact(), path)
		}
		if r.Redirect == nil {
			t.Errorf("redirect route[%d] missing Redirect", i+2)
		}
	}

	// Route 5: /alice-work/cetirun-auth/ → cetirun-auth helper
	r5 := vs.Spec.Http[5]
	if r5.Match[0].Uri.GetPrefix() != "/alice-work/cetirun-auth/" {
		t.Errorf("route[5] prefix = %q, want %q", r5.Match[0].Uri.GetPrefix(), "/alice-work/cetirun-auth/")
	}
	if r5.Route[0].Destination.Port.Number != cetirunAuthPort {
		t.Errorf("route[5] port = %d, want %d", r5.Route[0].Destination.Port.Number, cetirunAuthPort)
	}

	// Route 6: /alice-work/desktop → novnc
	r6 := vs.Spec.Http[6]
	if r6.Match[0].Uri.GetPrefix() != "/alice-work/desktop" {
		t.Errorf("route[6] prefix = %q, want %q", r6.Match[0].Uri.GetPrefix(), "/alice-work/desktop")
	}
	if r6.Route[0].Destination.Port.Number != novncPort {
		t.Errorf("route[6] port = %d, want %d", r6.Route[0].Destination.Port.Number, novncPort)
	}

	// Route 7: /alice-work/term → ttyd
	r7 := vs.Spec.Http[7]
	if r7.Match[0].Uri.GetPrefix() != "/alice-work/term" {
		t.Errorf("route[7] prefix = %q, want %q", r7.Match[0].Uri.GetPrefix(), "/alice-work/term")
	}
	if r7.Route[0].Destination.Port.Number != ttydPort {
		t.Errorf("route[7] port = %d, want %d", r7.Route[0].Destination.Port.Number, ttydPort)
	}

	// Route 8: /ui/alice-work/ → native OpenClaw Control UI, with the URI preserved.
	r8 := vs.Spec.Http[8]
	if r8.Match[0].Uri.GetPrefix() != "/ui/alice-work/" {
		t.Errorf("route[8] prefix = %q, want %q", r8.Match[0].Uri.GetPrefix(), "/ui/alice-work/")
	}
	if r8.Rewrite != nil {
		t.Errorf("route[8] rewrite = %v, want nil", r8.Rewrite)
	}
	if r8.Route[0].Destination.Port.Number != openclawPort {
		t.Errorf("route[8] port = %d, want %d", r8.Route[0].Destination.Port.Number, openclawPort)
	}
}

func TestInstanceNamespaceByName(t *testing.T) {
	got := instanceNamespace("alice-work")
	want := "cetirun-alice-work"
	if got != want {
		t.Errorf("instanceNamespace(%q) = %q, want %q", "alice-work", got, want)
	}
}

// TestBuildHelmValues_NotSuspendedOmitsReplicaCount verifies that when
// spec.suspended is false (default), replicaCount is not set by the
// operator, allowing the chart default (1) to apply.
func TestBuildHelmValues_NotSuspendedOmitsReplicaCount(t *testing.T) {
	instance := newTestInstance("grace", "Grace Active", "example-team", "gateway-token")
	// Suspended is false by default (zero value).

	vals, err := buildHelmValues(instance, nil, "example.com", "", nil, "")
	if err != nil {
		t.Fatalf("buildHelmValues: %v", err)
	}

	if _, ok := vals["replicaCount"]; ok {
		t.Error("replicaCount should not be set when suspended=false (let chart default apply)")
	}
}

func TestComputeEffectiveSkills(t *testing.T) {
	cases := []struct {
		name   string
		team   *cetirunv1alpha1.Team
		add    []string
		remove []string
		want   []string
	}{
		{
			name: "nil team no overrides",
			want: []string{},
		},
		{
			name: "team skills only",
			team: &cetirunv1alpha1.Team{
				Spec: cetirunv1alpha1.TeamSpec{Skills: []string{"npm:fetch", "@anthropic/fetch"}},
			},
			want: []string{"@anthropic/fetch", "npm:fetch"},
		},
		{
			name: "add without team",
			add:  []string{"npm:matrix"},
			want: []string{"npm:matrix"},
		},
		{
			name: "team + add + remove",
			team: &cetirunv1alpha1.Team{
				Spec: cetirunv1alpha1.TeamSpec{Skills: []string{"@anthropic/fetch", "npm:extra"}},
			},
			add:    []string{"npm:matrix"},
			remove: []string{"npm:extra"},
			want:   []string{"@anthropic/fetch", "npm:matrix"},
		},
		{
			name:   "remove nonexistent is no-op",
			add:    []string{"npm:matrix"},
			remove: []string{"npm:ghost"},
			want:   []string{"npm:matrix"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := computeEffectiveSkills(c.team, c.add, c.remove)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("computeEffectiveSkills() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestClassifySkills(t *testing.T) {
	npm, hub, err := classifySkills([]string{
		"npm:@openclaw/matrix",
		"@anthropic/fetch",
		"bare-tool",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(npm) != 1 || npm[0] != "@openclaw/matrix" {
		t.Errorf("unexpected npm: %v", npm)
	}
	if len(hub) != 2 {
		t.Errorf("unexpected clawHub: %v", hub)
	}
	// pvc: prefix is no longer supported
	if _, _, err := classifySkills([]string{"pvc:platform"}); err == nil {
		t.Error("expected error for pvc: prefix")
	}
	// empty npm: package should error
	if _, _, err := classifySkills([]string{"npm:"}); err == nil {
		t.Error("expected error for empty npm: name")
	}
}

func TestBuildHelmValues_WithTeam(t *testing.T) {
	inst := newTestInstance("alice", "Alice", "", "gateway-token")
	// A stale Copilot override must not replace the model configured for the
	// team's Anthropic provider.
	inst.Spec.Model = "gpt-5.4"
	inst.Spec.Helm.Values["secrets"] = runtime.RawExtension{
		Raw: []byte(`{"model":"claude-sonnet-4.5"}`),
	}
	team := &cetirunv1alpha1.Team{
		Spec: cetirunv1alpha1.TeamSpec{
			Defaults: cetirunv1alpha1.TeamDefaultsSpec{
				Provider: "anthropic",
			},
			MemberContext: cetirunv1alpha1.TeamContextSpec{
				Role:        "Platform Engineer",
				Description: []string{"Owns infra layer"},
			},
			Skills: []string{"npm:matrix", "@anthropic/fetch"},
			SkillStorage: &cetirunv1alpha1.SkillStorageSpec{
				ResourceGroup:  "rg-platform",
				StorageAccount: "platformskills",
				ContainerName:  "skills",
			},
		},
	}

	if _, err := buildHelmValues(inst, team, "example.com", "", nil, ""); err == nil {
		t.Fatal("team defaults must not inject a direct provider model")
	}

	vals, err := buildHelmValues(newTestInstance("alice", "Alice", "", "gateway-token"), team, "example.com", "", nil, "")
	if err != nil {
		t.Fatal(err)
	}

	user := vals["user"].(map[string]interface{})
	if user["role"] != "Platform Engineer" {
		t.Errorf("role = %v, want Platform Engineer", user["role"])
	}
	desc := user["description"].([]interface{})
	if len(desc) != 1 || desc[0] != "Owns infra layer" {
		t.Errorf("description = %v", desc)
	}

	skills := vals["skills"].(map[string]interface{})
	if skills["shared"] != true {
		t.Errorf("skills.shared = %v, want true", skills["shared"])
	}
	if skills["resourceGroup"] != "rg-platform" {
		t.Errorf("skills.resourceGroup = %v", skills["resourceGroup"])
	}
	if skills["storageAccount"] != "platformskills" {
		t.Errorf("skills.storageAccount = %v", skills["storageAccount"])
	}
	if skills["containerName"] != "skills" {
		t.Errorf("skills.containerName = %v", skills["containerName"])
	}
	npmPkgs := skills["npm"].([]interface{})
	if len(npmPkgs) != 1 || npmPkgs[0] != "matrix" {
		t.Errorf("npm = %v", npmPkgs)
	}
	hub := skills["clawHub"].([]interface{})
	if len(hub) != 1 || hub[0] != "@anthropic/fetch" {
		t.Errorf("clawHub = %v", hub)
	}
}

func TestBuildHelmValuesKeepsTeamContextAndSkillsIsolated(t *testing.T) {
	for _, test := range []struct {
		name, role, description, storageAccount, npmSkill, hubSkill string
	}{
		{"platform", "Platform Engineer", "Owns the platform", "platformskills", "npm:platform", "@platform/release"},
		{"research", "Research Engineer", "Owns experimentation", "researchskills", "npm:research", "@research/evaluate"},
	} {
		t.Run(test.name, func(t *testing.T) {
			agent := newTestInstance(test.name, test.name, "", "gateway-token")
			agent.Spec.Team = test.name
			team := &cetirunv1alpha1.Team{
				Spec: cetirunv1alpha1.TeamSpec{
					MemberContext: cetirunv1alpha1.TeamContextSpec{
						Role:        test.role,
						Description: []string{test.description},
					},
					Skills: []string{test.npmSkill, test.hubSkill},
					SkillStorage: &cetirunv1alpha1.SkillStorageSpec{
						ResourceGroup: "rg-" + test.name, StorageAccount: test.storageAccount, ContainerName: "skills",
					},
				},
			}

			values, err := buildHelmValues(agent, team, "example.com", "", nil, "")
			if err != nil {
				t.Fatal(err)
			}
			user := values["user"].(map[string]interface{})
			if user["role"] != test.role || !reflect.DeepEqual(user["description"], []interface{}{test.description}) {
				t.Fatalf("Team context crossed into %s Agent values: %#v", test.name, user)
			}
			skills := values["skills"].(map[string]interface{})
			if skills["storageAccount"] != test.storageAccount ||
				!reflect.DeepEqual(skills["npm"], []interface{}{test.npmSkill[4:]}) ||
				!reflect.DeepEqual(skills["clawHub"], []interface{}{test.hubSkill}) {
				t.Fatalf("Team skills crossed into %s Agent values: %#v", test.name, skills)
			}
		})
	}
}

func TestBuildHelmValues_NilTeam_Unchanged(t *testing.T) {
	inst := newTestInstance("alice", "Alice", "aks", "gateway-token")
	vals, err := buildHelmValues(inst, nil, "example.com", "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	secrets := vals["secrets"].(map[string]interface{})
	if secrets["provider"] != "github_copilot" {
		t.Errorf("provider = %v, want github_copilot", secrets["provider"])
	}
	user := vals["user"].(map[string]interface{})
	if _, ok := user["role"]; ok {
		t.Error("role should not be set when team is nil")
	}
}

func TestBuildHelmValues_SkillOverrides(t *testing.T) {
	inst := newTestInstance("alice", "Alice", "", "gw")
	inst.Spec.Team = "platform"
	inst.Spec.SkillOverrides = cetirunv1alpha1.SkillOverrideSpec{
		Add:    []string{"npm:extra"},
		Remove: []string{"npm:dropped"},
	}
	team := &cetirunv1alpha1.Team{
		Spec: cetirunv1alpha1.TeamSpec{
			Skills: []string{"npm:dropped", "@anthropic/fetch"},
		},
	}

	vals, err := buildHelmValues(inst, team, "", "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	skills := vals["skills"].(map[string]interface{})
	npmPkgs := skills["npm"].([]interface{})
	if len(npmPkgs) != 1 || npmPkgs[0] != "extra" {
		t.Errorf("unexpected npm after override: %v", npmPkgs)
	}
	hub := skills["clawHub"].([]interface{})
	if len(hub) != 1 || hub[0] != "@anthropic/fetch" {
		t.Errorf("unexpected clawHub after override: %v", hub)
	}
}

// TestBuildHelmValues_WithTeam_Hermes verifies that team defaults, memberContext
// and skills propagate into helm values even when the instance uses agentType=hermes.
// The chart consumes these values agent-agnostically (USER.md / TEAM.md / skills.external_dirs),
// so the operator must not branch on agentType when applying team data.
func TestBuildHelmValues_WithTeam_Hermes(t *testing.T) {
	inst := newTestInstance("bob", "Bob", "", "gateway-token")
	inst.Spec.Agent.Type = cetirunv1alpha1.AgentTypeHermes
	team := &cetirunv1alpha1.Team{
		Spec: cetirunv1alpha1.TeamSpec{
			Defaults: cetirunv1alpha1.TeamDefaultsSpec{
				Provider: "anthropic",
			},
			MemberContext: cetirunv1alpha1.TeamContextSpec{
				Role:        "SRE",
				Description: []string{"On-call rotation B", "Owns ingest pipeline"},
			},
			Skills: []string{"npm:matrix"},
			SkillStorage: &cetirunv1alpha1.SkillStorageSpec{
				ResourceGroup:  "rg-shared",
				StorageAccount: "sharedskills",
				ContainerName:  "skills",
				MountPath:      "/home/vibe/.aks/skills",
			},
		},
	}

	vals, err := buildHelmValues(inst, team, "example.com", "", nil, "")
	if err != nil {
		t.Fatal(err)
	}

	if vals["agentType"] != string(cetirunv1alpha1.AgentTypeHermes) {
		t.Errorf("agentType = %v, want hermes", vals["agentType"])
	}

	user := vals["user"].(map[string]interface{})
	if user["role"] != "SRE" {
		t.Errorf("role = %v, want SRE", user["role"])
	}
	desc := user["description"].([]interface{})
	if len(desc) != 2 || desc[0] != "On-call rotation B" {
		t.Errorf("description = %v", desc)
	}

	skills := vals["skills"].(map[string]interface{})
	if skills["shared"] != true {
		t.Errorf("skills.shared = %v, want true", skills["shared"])
	}
	if skills["storageAccount"] != "sharedskills" {
		t.Errorf("storageAccount = %v", skills["storageAccount"])
	}
	if skills["mountPath"] != "/home/vibe/.aks/skills" {
		t.Errorf("mountPath = %v", skills["mountPath"])
	}
	npmPkgs := skills["npm"].([]interface{})
	if len(npmPkgs) != 1 || npmPkgs[0] != "matrix" {
		t.Errorf("npm = %v", npmPkgs)
	}

	secrets := vals["secrets"].(map[string]interface{})
	if secrets["provider"] != "anthropic" {
		t.Errorf("provider = %v, want anthropic", secrets["provider"])
	}
}
