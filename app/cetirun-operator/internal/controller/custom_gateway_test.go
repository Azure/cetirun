// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/runtimebindings"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func gatewayCustomFixture(t *testing.T) (*KubernetesCustomBindings, *api.Agent, *fakeAIGateway, runtimebindings.Custom) {
	t.Helper()
	p, instance := readyProvider(t)
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
	if !p.reconciler.aiGatewayRequired(instance) {
		t.Fatal("approved Custom image was not routed through the Gateway")
	}
	instance.Spec.Agent.Image = ptr.To("registry.example.com/unapproved@sha256:" + strings.Repeat("b", 64))
	if !p.reconciler.aiGatewayRequired(instance) {
		t.Fatal("Custom harness bypassed the Gateway requirement based on its image")
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
