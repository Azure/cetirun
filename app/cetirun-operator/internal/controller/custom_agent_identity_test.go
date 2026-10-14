// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/azure"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/runtimebindings"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCustomAgentIdentityPersistsCanonicalBinding(t *testing.T) {
	instance := newAgentIdentityTestInstance()
	instance.Spec.Agent.Type = api.AgentTypeCustom
	scheme := agentIdentityTestScheme(t)
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.Agent{}).WithObjects(instance).Build()
	directory := &fakeAgentIdentityDirectory{createResult: &azure.ServicePrincipal{
		ID: testAgentIdentityObjectID, AppID: testAgentIdentityClientID,
		DisplayName: testAgentIdentityDisplayName(), AgentIdentityBlueprintID: testAgentIdentityBlueprintID,
	}}
	r := &AgentReconciler{
		Client: client, APIReader: client, Scheme: scheme, AgentIdentityEnabled: true,
		AgentIdentityTenantID: testAgentIdentityTenantID, AgentIdentityBlueprintClientID: testAgentIdentityBlueprintID,
		AgentIdentityBrokerURL:          "http://broker.credentials.svc.cluster.local:8080",
		agentIdentityGraphClientFactory: func() (agentIdentityDirectoryClient, error) { return directory, nil },
	}
	bindings := runtimebindings.Custom{}
	ready, err := r.bindCustomAgentIdentity(context.Background(), instance, &bindings)
	if err != nil || !ready || bindings.AgentIdentity == nil || directory.createCalls != 1 {
		t.Fatalf("ready=%v binding=%v creates=%d err=%v", ready, bindings.AgentIdentity, directory.createCalls, err)
	}
	stored := &api.Agent{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: instance.Name}, stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.AgentIdentity == nil || stored.Status.AgentIdentity.ClientID != testAgentIdentityClientID || stored.Status.AgentIdentity.Phase != api.AgentIdentityPhaseReady {
		t.Fatal("Broker cannot observe the canonical identity")
	}
	bindings = runtimebindings.Custom{}
	ready, err = r.bindCustomAgentIdentity(context.Background(), stored, &bindings)
	if err != nil || !ready || directory.createCalls != 1 || directory.getObjectCalls != 0 {
		t.Fatal("unchanged Custom identity bypassed the verification interval")
	}
}

func TestCustomAgentIdentityRejectsProviderInjectionAndDisabledDeployment(t *testing.T) {
	instance := newAgentIdentityTestInstance()
	instance.Spec.Agent.Type = api.AgentTypeCustom
	scheme := agentIdentityTestScheme(t)
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.Agent{}).WithObjects(instance).Build()
	r := &AgentReconciler{Client: client, APIReader: client, Scheme: scheme}
	bindings := runtimebindings.Custom{AgentIdentity: &runtimebindings.AgentIdentity{BrokerURL: "http://broker.credentials.svc.cluster.local:8080"}}
	if ready, err := r.bindCustomAgentIdentity(context.Background(), instance, &bindings); err == nil || ready {
		t.Fatal("general provider injected identity access")
	}
	bindings = runtimebindings.Custom{}
	if ready, err := r.bindCustomAgentIdentity(context.Background(), instance, &bindings); err != nil || ready || bindings.AgentIdentity != nil {
		t.Fatalf("disabled deployment supplied an identity binding: ready=%v err=%v", ready, err)
	}
}
