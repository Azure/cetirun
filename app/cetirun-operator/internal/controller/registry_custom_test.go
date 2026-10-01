// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller_test

import (
	"context"
	"reflect"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/internal/registryauth"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type registryScenarioClient struct{ client.Client }

func (c registryScenarioClient) Create(ctx context.Context, object client.Object, options ...client.CreateOption) error {
	if review, ok := object.(*authorizationv1.SubjectAccessReview); ok {
		review.Status.Denied = true
		return nil
	}
	if object.GetUID() == "" {
		object.SetUID(types.UID("created-" + object.GetName()))
	}
	return c.Client.Create(ctx, object, options...)
}

func TestCustomRegistryChangesNeverStopHealthyWriter(t *testing.T) {
	s := runningCustomReview(t)
	ctx := context.Background()
	s.reconciler.Client = registryScenarioClient{s.client}
	before := s.workload()
	approvals := s.provider.store.approvals
	agent := s.current()
	agent.Spec.Agent.RegistrySecretRef = &api.RegistrySecretRef{Name: "registry-source-example"}
	agent.Generation++
	if err := s.client.Update(ctx, agent); err != nil {
		t.Fatal(err)
	}
	s.provider.bindings.Generation = agent.Generation
	raw, _ := registryauth.Encode(*agent.Spec.Agent.Image, registryauth.Credentials{Username: "example", Password: "synthetic-token"})
	source := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: agent.Spec.Agent.RegistrySecretRef.Name, Namespace: registryauth.DefaultNamespace,
		UID: "source-uid", OwnerReferences: []metav1.OwnerReference{registryauth.Owner(agent)}, Labels: registryauth.Labels(agent.UID)},
		Type: corev1.SecretTypeDockerConfigJson, Data: map[string][]byte{corev1.DockerConfigJsonKey: raw}}
	if err := s.client.Create(ctx, source); err != nil {
		t.Fatal(err)
	}
	if err := s.step(); err != nil {
		t.Fatal(err)
	}
	if s.current().Status.Phase != api.PhaseReady || s.current().Status.RegistryCredentials == nil {
		t.Fatalf("Custom sync did not complete: %+v", s.current().Status.Conditions)
	}
	if s.provider.store.approvals != approvals || !reflect.DeepEqual(before, s.workload()) {
		t.Fatal("adding a pull reference stopped or replaced the writer")
	}
	hash := s.helm.lastValues["runtime"].(map[string]any)["workloadHash"]
	s.helm.installCalled = false
	_ = s.client.Get(ctx, client.ObjectKeyFromObject(source), source)
	source.Data[corev1.DockerConfigJsonKey], _ = registryauth.Encode(*agent.Spec.Agent.Image, registryauth.Credentials{Username: "example", Password: "rotated-token"})
	_ = s.client.Update(ctx, source)
	if err := s.step(); err != nil {
		t.Fatal(err)
	}
	if s.helm.installCalled || s.provider.store.approvals != approvals || s.helm.lastValues["runtime"].(map[string]any)["workloadHash"] != hash {
		t.Fatal("content rotation redeployed Custom")
	}
	agent = s.current()
	agent.Spec.Agent.RegistrySecretRef = nil
	agent.Generation++
	_ = s.client.Update(ctx, agent)
	s.provider.bindings.Generation = agent.Generation
	if err := s.step(); err != nil {
		t.Fatal(err)
	}
	if s.provider.store.approvals != approvals || s.current().Status.RegistryCredentials != nil || !reflect.DeepEqual(before, s.workload()) {
		t.Fatal("removal disturbed writer or retained credentials")
	}
}
