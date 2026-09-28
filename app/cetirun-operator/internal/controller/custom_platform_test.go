// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller_test

import (
	"context"
	"testing"

	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

func TestCustomDesktopAccessAndSuspension(t *testing.T) {
	s := newCompletionScenario(t)
	s.reconciler.DomainSuffix = "platform.example.com"
	if err := s.step(); err != nil {
		t.Fatal(err)
	}
	if got := s.current().Status.AccessURL; got != "" {
		t.Fatalf("Custom repurposed the native UI access URL: %q", got)
	}
	route := &istiov1beta1.VirtualService{}
	key := types.NamespacedName{Namespace: "cetirun-completion", Name: "cetirun-completion"}
	if err := s.client.Get(context.Background(), key, route); err != nil {
		t.Fatal(err)
	}
	if len(route.OwnerReferences) != 1 || route.OwnerReferences[0].UID != s.agent.UID {
		t.Fatal("desktop route lost Agent ownership")
	}
	for _, rule := range route.Spec.Http {
		for _, destination := range rule.Route {
			if destination.Destination.Port.Number != 6080 {
				t.Fatal("Custom published an Agent, native auth or ttyd endpoint")
			}
		}
	}
	current := s.current()
	current.Spec.Suspended = true
	if err := s.client.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if err := s.step(); err != nil {
		t.Fatal(err)
	}
	if err := s.client.Get(context.Background(), key, route); !apierrors.IsNotFound(err) {
		t.Fatalf("suspension retained desktop route: %v", err)
	}
}

func TestCustomDesktopCanBeDisabledWithoutLosingControls(t *testing.T) {
	s := newCompletionScenario(t)
	current := s.current()
	current.Spec.Helm.Values = map[string]runtime.RawExtension{
		"desktop": {Raw: []byte(`{"enabled":false}`)},
	}
	if err := s.client.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if err := s.step(); err != nil {
		t.Fatal(err)
	}
	if s.current().Status.AccessURL != "" {
		t.Fatal("disabled desktop received an access URL")
	}
	if s.helm.lastValues["execRestriction"].(map[string]any)["enabled"] != true {
		t.Fatal("disabling desktop disabled a platform security control")
	}
}
