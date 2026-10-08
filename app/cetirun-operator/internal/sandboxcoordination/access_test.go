// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxcoordination

import (
	"context"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
)

func accessIntent() Intent {
	intent := testIntent("access")
	service := source("v1", "Service", intent.Namespace.Name, intent.Workload.Name, "service-uid")
	gateway := source("networking.istio.io/v1beta1", "Gateway", "gateway-system", "gateway", "gateway-uid")
	ingressNamespace := source("v1", "Namespace", "", "gateway-workloads", "ingress-namespace-uid")
	intent.Access = &AccessBinding{
		Route:   w.ObjectIdentity{Name: "cetirun-" + intent.Agent.Name, UID: "route-uid"},
		Service: service, Gateway: gateway, Host: "agents.example.com", Port: 18789,
		IngressNamespaces: []w.SourceReference{ingressNamespace},
	}
	intent.Sources = append(intent.Sources, service, gateway, ingressNamespace)
	intent.SourceDigests[w.SourceKey(service)] = strings.Repeat("b", 64)
	intent.SourceDigests[w.SourceKey(gateway)] = strings.Repeat("c", 64)
	intent.SourceDigests[w.SourceKey(ingressNamespace)] = strings.Repeat("d", 64)
	return intent
}

func TestAccessIntentRequiresExactSourcePins(t *testing.T) {
	for name, mutate := range map[string]func(*Intent){
		"disabled ingress": func(i *Intent) { i.IngressMode = api.AgentPolicyIngressDisabled },
		"route UID":        func(i *Intent) { i.Access.Route.UID = "" },
		"route name":       func(i *Intent) { i.Access.Route.Name = "another" },
		"host":             func(i *Intent) { i.Access.Host = "*.example.com" },
		"port":             func(i *Intent) { i.Access.Port = 65536 },
		"service key":      func(i *Intent) { i.Access.Service.Namespace = "another" },
		"service UID":      func(i *Intent) { i.Access.Service.UID = "another" },
		"gateway key":      func(i *Intent) { i.Access.Gateway.Name = "another" },
		"gateway type":     func(i *Intent) { i.Access.Gateway.Kind = "Secret" },
		"gateway digest":   func(i *Intent) { delete(i.SourceDigests, w.SourceKey(i.Access.Gateway)) },
		"ingress UID":      func(i *Intent) { i.Access.IngressNamespaces[0].UID = "another" },
		"ingress name":     func(i *Intent) { i.Access.IngressNamespaces[0].Name = "*" },
		"ingress kind":     func(i *Intent) { i.Access.IngressNamespaces[0].Kind = "ConfigMap" },
		"ingress scope":    func(i *Intent) { i.Access.IngressNamespaces[0].Namespace = "another" },
		"ingress digest":   func(i *Intent) { delete(i.SourceDigests, w.SourceKey(i.Access.IngressNamespaces[0])) },
		"duplicate ingress": func(i *Intent) {
			i.Access.IngressNamespaces = append(i.Access.IngressNamespaces, i.Access.IngressNamespaces[0])
		},
	} {
		t.Run(name, func(t *testing.T) {
			intent := accessIntent()
			if err := intent.Validate(); err != nil {
				t.Fatal("valid access fixture was rejected:", err)
			}
			mutate(&intent)
			if err := intent.Validate(); err == nil {
				t.Fatal("invalid access binding was accepted")
			}
		})
	}
}

func TestAccessIntentPersistsAnIndependentBinding(t *testing.T) {
	ctx := context.Background()
	store, _ := fixture(t)
	intent := accessIntent()
	ticket, err := store.AcquireExecution(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	intent.Access.Host = "caller-mutation.example.com"
	intent.Access.IngressNamespaces[0].Name = "caller-mutation"
	original := ticket.DeepCopy()
	ticket.Execution.Intent.Access.Host = "returned-mutation.example.com"
	ticket.Execution.Intent.Access.IngressNamespaces[0].Name = "returned-mutation"
	stored, err := store.ReadExecution(ctx, original.Execution.Intent.Agent.UID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Execution.Intent.Access.Host != "agents.example.com" || original.Execution.Intent.Access.Host != "agents.example.com" {
		t.Fatal("caller mutation changed a persisted or copied binding")
	}
	if stored.Execution.Intent.Access.IngressNamespaces[0].Name != "gateway-workloads" ||
		original.Execution.Intent.Access.IngressNamespaces[0].Name != "gateway-workloads" {
		t.Fatal("caller mutation changed the persisted ingress consumer namespace")
	}
	if err := store.Check(ctx, ticket); err == nil {
		t.Fatal("mutated access target retained execution authority")
	}
	if err := store.Check(ctx, original); err != nil {
		t.Fatal("immutable original ticket changed:", err)
	}
}
