// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

// TestPlatformRegistrationChangeQueuesEveryPolicyAndSandbox checks the watch of
// the platform registration: a change of the registration ConfigMap queues all
// AgentPolicies and all Sandboxes. Another ConfigMap, or a reconciler without a
// registration, queues nothing.
func TestPlatformRegistrationChangeQueuesEveryPolicyAndSandbox(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{api.AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&api.AgentPolicy{ObjectMeta: metav1.ObjectMeta{Name: "policy-a"}},
		&api.AgentPolicy{ObjectMeta: metav1.ObjectMeta{Name: "policy-b"}},
		&api.Sandbox{ObjectMeta: metav1.ObjectMeta{Name: "sandbox-a"}},
		&api.Sandbox{ObjectMeta: metav1.ObjectMeta{Name: "sandbox-b"}},
	).Build()
	registration := types.NamespacedName{Namespace: "cetirun-system", Name: "sandbox-platform"}
	configMap := func(namespace, name string) *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	}
	requests := func(names ...string) []reconcile.Request {
		var requests []reconcile.Request
		for _, name := range names {
			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: name}})
		}
		return requests
	}
	sorted := func(requests []reconcile.Request) []reconcile.Request {
		return slices.SortedFunc(slices.Values(requests), func(a, b reconcile.Request) int {
			return strings.Compare(a.Name, b.Name)
		})
	}
	ctx := context.Background()
	for _, tc := range []struct {
		kind    string
		enqueue func(types.NamespacedName, client.Client) func(context.Context, client.Object) []reconcile.Request
		want    []reconcile.Request
	}{
		{"AgentPolicy", func(key types.NamespacedName, kube client.Client) func(context.Context, client.Object) []reconcile.Request {
			return (&AgentPolicyReconciler{Client: kube, PlatformRegistration: key}).enqueueForPlatformRegistration
		}, requests("policy-a", "policy-b")},
		{"Sandbox", func(key types.NamespacedName, kube client.Client) func(context.Context, client.Object) []reconcile.Request {
			return (&SandboxReconciler{Client: kube, PlatformRegistration: key}).enqueueForPlatformRegistration
		}, requests("sandbox-a", "sandbox-b")},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			enqueue := tc.enqueue(registration, kube)
			if got := sorted(enqueue(ctx, configMap(registration.Namespace, registration.Name))); !slices.Equal(got, tc.want) {
				t.Fatalf("the registration change queued %v, want %v", got, tc.want)
			}
			for _, other := range []*corev1.ConfigMap{
				configMap(registration.Namespace, "skills-catalog"), configMap("other-namespace", registration.Name),
			} {
				if got := enqueue(ctx, other); len(got) != 0 {
					t.Fatalf("the ConfigMap %s/%s queued %v", other.Namespace, other.Name, got)
				}
			}
			if got := tc.enqueue(types.NamespacedName{}, kube)(ctx, configMap(registration.Namespace, registration.Name)); len(got) != 0 {
				t.Fatalf("a reconciler without a registration queued %v", got)
			}
			broken := interceptor.NewClient(kube, interceptor.Funcs{
				List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
					return errors.New("synthetic list failure")
				},
			})
			if got := tc.enqueue(registration, broken)(ctx, configMap(registration.Namespace, registration.Name)); len(got) != 0 {
				t.Fatalf("a failed list queued %v", got)
			}
		})
	}
}

// TestSandboxPlatformRegistrationNamesTheSourceConfigMap checks the key that
// the operator watches: the ConfigMap of a Kubernetes source, and nothing for
// another source.
func TestSandboxPlatformRegistrationNamesTheSourceConfigMap(t *testing.T) {
	source := &KubernetesSandboxConfigurationSource{Namespace: "platform-system", Name: "sandbox-platform"}
	if got := SandboxPlatformRegistration(source); got != (types.NamespacedName{Namespace: "platform-system", Name: "sandbox-platform"}) {
		t.Fatalf("SandboxPlatformRegistration = %v", got)
	}
	var none *KubernetesSandboxConfigurationSource
	for _, other := range []SandboxConfigurationSource{
		nil, none, SandboxConfigurationSourceFunc(func(context.Context) (SandboxConfigurationSnapshot, error) {
			return SandboxConfigurationSnapshot{}, nil
		}),
	} {
		if got := SandboxPlatformRegistration(other); got != (types.NamespacedName{}) {
			t.Fatalf("a source without a ConfigMap named %v", got)
		}
	}
}
