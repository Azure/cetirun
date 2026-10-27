// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller_test

import (
	"context"
	"sync/atomic"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/internal/registryauth"
	appsv1 "k8s.io/api/apps/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type registryScenarioClient struct {
	client.Client
	reviews, inventories atomic.Int64
	allow                atomic.Bool
}

func (c *registryScenarioClient) Create(ctx context.Context, object client.Object, options ...client.CreateOption) error {
	if review, ok := object.(*authorizationv1.SubjectAccessReview); ok {
		c.reviews.Add(1)
		review.Status.Allowed = c.allow.Load()
		review.Status.Denied = !review.Status.Allowed
		return nil
	}
	if object.GetUID() == "" {
		object.SetUID(types.UID("created-" + object.GetName()))
	}
	return c.Client.Create(ctx, object, options...)
}

func (c *registryScenarioClient) List(ctx context.Context, list client.ObjectList, options ...client.ListOption) error {
	resolved := &client.ListOptions{}
	for _, option := range options {
		option.ApplyToList(resolved)
	}
	if resolved.Namespace == "" {
		switch list.(type) {
		case *api.AgentList, *corev1.PodList, *appsv1.StatefulSetList:
			c.inventories.Add(1)
		}
	}
	return c.Client.List(ctx, list, options...)
}

func attachCustomRegistry(t *testing.T, s *completionScenario) (*registryScenarioClient, *corev1.Secret) {
	t.Helper()
	ctx := context.Background()
	c := &registryScenarioClient{Client: s.client}
	s.reconciler.Client = c
	agent := s.current()
	agent.Spec.Agent.RegistrySecretRef = &api.RegistrySecretRef{Name: "registry-source-example"}
	agent.Generation++
	if err := s.client.Update(ctx, agent); err != nil {
		t.Fatal(err)
	}
	s.provider.bindings.Generation = agent.Generation
	raw, err := registryauth.Encode(*agent.Spec.Agent.Image, registryauth.Credentials{Username: "example", Password: "synthetic-token"})
	if err != nil {
		t.Fatal(err)
	}
	source := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: agent.Spec.Agent.RegistrySecretRef.Name, Namespace: registryauth.DefaultNamespace,
		UID: "source-uid", OwnerReferences: []metav1.OwnerReference{registryauth.Owner(agent)}, Labels: registryauth.Labels(agent.UID)},
		Type: corev1.SecretTypeDockerConfigJson, Data: map[string][]byte{corev1.DockerConfigJsonKey: raw}}
	if err := s.client.Create(ctx, source); err != nil {
		t.Fatal(err)
	}
	if err := s.step(); err != nil {
		t.Fatal(err)
	}
	return c, source
}
