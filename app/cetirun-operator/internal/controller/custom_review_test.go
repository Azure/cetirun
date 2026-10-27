// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller_test

import (
	"context"
	"fmt"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/runtimebindings"
	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func runningCustomReview(t *testing.T) *completionScenario {
	t.Helper()
	s := newCompletionScenario(t)
	if err := s.step(); err != nil {
		t.Fatal(err)
	}
	s.observeApplied()
	if err := s.step(); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *completionScenario) workload() *appsv1.StatefulSet {
	s.t.Helper()
	workload := &appsv1.StatefulSet{}
	key := types.NamespacedName{Namespace: s.provider.bindings.Namespace.Name, Name: "openclaw-" + s.agent.Name + "-cetirun"}
	if err := s.client.Get(context.Background(), key, workload); err != nil {
		s.t.Fatal(err)
	}
	return workload
}

type reviewClient struct {
	client.Client
	podsForbidden bool
	noRoutes      bool
}

func (c reviewClient) List(ctx context.Context, obj client.ObjectList, options ...client.ListOption) error {
	if _, ok := obj.(*corev1.PodList); ok && c.podsForbidden {
		return apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", fmt.Errorf("synthetic denial"))
	}
	return c.Client.List(ctx, obj, options...)
}

func (c reviewClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, options ...client.GetOption) error {
	if _, ok := obj.(*istiov1beta1.VirtualService); ok && c.noRoutes {
		return &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "networking.istio.io", Kind: "VirtualService"}}
	}
	return c.Client.Get(ctx, key, obj, options...)
}

type observedProvider struct {
	*recoveryProvider
	deadline bool
	fail     bool
}

func (p *observedProvider) Resolve(ctx context.Context, agent *api.Agent, team *api.Team) (runtimebindings.Custom, error) {
	_, p.deadline = ctx.Deadline()
	if p.fail {
		return runtimebindings.Custom{}, fmt.Errorf("synthetic provider outage")
	}
	return p.recoveryProvider.Resolve(ctx, agent, team)
}
