// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/agents"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/runtimebindings"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func gatewayCustomFixture(t *testing.T) (*KubernetesCustomBindings, *api.Agent, *fakeAIGateway, runtimebindings.Custom) {
	t.Helper()
	p, instance := readyProvider(t)
	p.config.GatewayImages = []string{*instance.Spec.Agent.Image}
	r := p.reconciler
	r.CustomBindings = p
	r.EventRecorder = record.NewFakeRecorder(100)
	r.AIGateway = AIGatewayConfig{Enabled: true, URL: testAIGatewayURL, MasterKeySecretName: "gateway-auth", DefaultBudget: 10, CopilotProxyImage: testProxyImage}
	gateway := newFakeAIGateway()
	r.aiGatewayClientFactory = func(context.Context) (aiGatewayKeyClient, error) { return gateway, nil }
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "cetirun-ai-gateway", Namespace: "cetirun-system"}, Spec: corev1.ServiceSpec{
		Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.0.0.12", Selector: map[string]string{"app.kubernetes.io/name": "cetirun-ai-gateway", "app.kubernetes.io/component": "gateway"},
		Ports: []corev1.ServicePort{{Name: "http", Port: 4000, Protocol: corev1.ProtocolTCP}},
	}}
	if err := r.Create(context.Background(), service); err != nil {
		t.Fatal(err)
	}
	b, err := p.Resolve(context.Background(), instance, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p, instance, gateway, b
}

func TestCustomGatewayExplicitImageSelection(t *testing.T) {
	p, instance := readyProvider(t)
	p.reconciler.CustomBindings = p
	p.reconciler.AIGateway.Enabled = true
	if p.reconciler.aiGatewayRequired(instance) {
		t.Fatal("platform enablement migrated an image-owned Agent")
	}
	p.config.GatewayImages = []string{*instance.Spec.Agent.Image}
	p.config.AgentManagedModel = false
	raw, _ := json.Marshal(p.config)
	if _, err := ParseCustomRuntimeConfig(string(raw)); err != nil {
		t.Fatal(err)
	}
	if !p.reconciler.aiGatewayRequired(instance) {
		t.Fatal("approved Gateway image not selected")
	}
	p.config.GatewayImages = append(p.config.GatewayImages, "registry.example.com/unapproved@sha256:"+strings.Repeat("b", 64))
	raw, _ = json.Marshal(p.config)
	if _, err := ParseCustomRuntimeConfig(string(raw)); err == nil {
		t.Fatal("unapproved Gateway image accepted")
	}
}

func TestCustomGatewayKeyBindingAndCopilot(t *testing.T) {
	p, instance, gateway, b := gatewayCustomFixture(t)
	ctx := context.Background()
	if err := p.reconciler.bindCustomGateway(ctx, instance, &b); err != nil {
		t.Fatal(err)
	}
	resolved, err := agents.ResolveCustom(agents.Request{Type: "custom", Image: *instance.Spec.Agent.Image})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Validate(resolved); err != nil {
		t.Fatal(err)
	}
	if _, err := p.reconciler.validateCustomObjects(ctx, instance, b); err != nil {
		t.Fatal(err)
	}
	if b.AgentManagedModel || b.Model.Name != aiGatewayKeySecretName || b.Model.BaseURL != testAIGatewayURL+"/v1" || b.Network.GatewayPort != 4000 || b.Copilot == nil {
		t.Fatalf("missing Custom Gateway binding: %#v", b)
	}
	if !instance.Status.AIGateway.Routed || instance.Status.AIGateway.Copilot.Phase != api.AIGatewayCopilotPhaseSignInRequired {
		t.Fatal("unauthed Copilot readiness was not kept separate")
	}
	if len(gateway.generated) != 1 {
		t.Fatal("unexpected key creation count")
	}
	serialized, _ := json.Marshal(b)
	if strings.Contains(string(serialized), gateway.generated[0].Key) {
		t.Fatal("binding contains a key value")
	}
	first := b.Model
	if err := p.reconciler.bindCustomGateway(ctx, instance, &b); err != nil {
		t.Fatal(err)
	}
	if first != b.Model || len(gateway.generated) != 1 {
		t.Fatal("steady reconciliation changed the key binding")
	}
	if err := p.reconciler.deleteAIGatewayKey(ctx, instance); err != nil {
		t.Fatal(err)
	}
	if len(gateway.keys) != 0 || len(gateway.deletedAliases) != 1 {
		t.Fatal("Custom key was not revoked")
	}
}

func TestCustomGatewayNeverFallsBack(t *testing.T) {
	for _, scenario := range []string{"disabled", "outage", "foreign-secret", "foreign-service"} {
		t.Run(scenario, func(t *testing.T) {
			p, instance, gateway, b := gatewayCustomFixture(t)
			ctx := context.Background()
			switch scenario {
			case "disabled":
				p.reconciler.AIGateway.Enabled = false
			case "outage":
				gateway.getErr = fmt.Errorf("synthetic unavailable")
			case "foreign-secret":
				secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: aiGatewayKeySecretName, Namespace: b.Namespace.Name, Labels: map[string]string{aiGatewayAgentUIDLabel: "other-uid"}}, Data: map[string][]byte{aiGatewayKeySecretField: []byte("sk-dummy")}}
				if err := p.reconciler.Create(ctx, secret); err != nil {
					t.Fatal(err)
				}
			case "foreign-service":
				service := &corev1.Service{}
				if err := p.reconciler.Get(ctx, types.NamespacedName{Namespace: "cetirun-system", Name: "cetirun-ai-gateway"}, service); err != nil {
					t.Fatal(err)
				}
				service.Spec.Selector = map[string]string{"app": "other"}
				if err := p.reconciler.Update(ctx, service); err != nil {
					t.Fatal(err)
				}
			}
			if err := p.reconciler.bindCustomGateway(ctx, instance, &b); err == nil {
				t.Fatal("invalid Gateway binding accepted")
			}
			if b.AgentManagedModel || b.Model.Name != "" || len(gateway.generated) != 0 {
				t.Fatal("failure fell back or issued a usable binding")
			}
			stored := &api.Agent{}
			if err := p.reconciler.Get(ctx, client.ObjectKeyFromObject(instance), stored); err != nil {
				t.Fatal(err)
			}
			if scenario == "disabled" && (stored.Status.AIGateway == nil || stored.Status.AIGateway.Reason != aiGatewayReasonDisabled) {
				t.Fatal("Gateway failure was not persisted")
			}
		})
	}
}
