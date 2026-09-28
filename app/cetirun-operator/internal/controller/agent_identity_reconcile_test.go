// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
)

type suspensionHelmClient struct {
	installCalled bool
	lastValues    map[string]any
}

func (f *suspensionHelmClient) Prepare(_ string, values map[string]any, chartVersion string) (*helm.PreparedRelease, error) {
	return &helm.PreparedRelease{Values: values, ChartVersion: chartVersion}, nil
}

func (f *suspensionHelmClient) Refresh(prepared *helm.PreparedRelease, values map[string]any) (*helm.PreparedRelease, error) {
	prepared.Values = values
	return prepared, nil
}

func (f *suspensionHelmClient) InstallOrUpgrade(_ string, _ string, prepared *helm.PreparedRelease) error {
	f.installCalled = true
	f.lastValues = prepared.Values
	return nil
}

func (*suspensionHelmClient) CustomApplied(string, string, *helm.PreparedRelease) (bool, error) {
	return false, nil
}

func (*suspensionHelmClient) Uninstall(string, string) error { return nil }

func TestReconcileSuspensionContinuesWhenAgentIdentityLookupFails(t *testing.T) {
	const name = "suspended-identity"
	instance := &cetirunv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			UID:        types.UID(testAgentIdentityInstanceUID),
			Generation: 2,
			Finalizers: []string{cetirunv1alpha1.Finalizer},
			Annotations: map[string]string{
				cetirunv1alpha1.AnnotationOwnerTenantID: testAgentIdentityTenantID,
				cetirunv1alpha1.AnnotationOwnerObjectID: testAgentIdentityOwnerObject,
			},
		},
		Spec: cetirunv1alpha1.AgentSpec{
			Agent: cetirunv1alpha1.HarnessSpec{Type: cetirunv1alpha1.AgentTypeOpenClaw},
			User: cetirunv1alpha1.UserSpec{
				Alias:       name,
				DisplayName: "Suspended Identity",
			},
			GatewayTokenSecretRef: "gateway-token",
			Helm: cetirunv1alpha1.HelmSpec{
				Version: "0.1.28",
				Values:  map[string]runtime.RawExtension{},
			},
			Suspended:     true,
			AgentIdentity: &cetirunv1alpha1.AgentIdentitySpec{Enabled: true},
		},
		Status: cetirunv1alpha1.AgentStatus{
			Phase:              cetirunv1alpha1.PhaseReady,
			ObservedGeneration: 1,
			AgentIdentity: &cetirunv1alpha1.AgentIdentityStatus{
				Phase:              cetirunv1alpha1.AgentIdentityPhaseReady,
				ClientID:           testAgentIdentityClientID,
				ObjectID:           testAgentIdentityObjectID,
				DisplayName:        testAgentIdentityDisplayName(),
				ObservedGeneration: 2,
			},
		},
	}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: instanceNamespace(name)}}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "gateway-token", Namespace: instanceNamespace(name)},
		Data: map[string][]byte{
			"OPENCLAW_GATEWAY_TOKEN": []byte("synthetic-token"),
			"CETIRUN_AUTH_KEY":       []byte("synthetic-auth-key"),
		},
	}
	virtualService := &istiov1beta1.VirtualService{
		ObjectMeta: metav1.ObjectMeta{Name: virtualServiceName(name), Namespace: instanceNamespace(name)},
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := cetirunv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := istiov1beta1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(instance, namespace, secret, virtualService).
		WithStatusSubresource(instance).
		Build()
	directory := &fakeAgentIdentityDirectory{getObjectErr: errors.New("synthetic Graph outage")}
	helmClient := &suspensionHelmClient{}
	reconciler := &AgentReconciler{
		Client:                         client,
		Scheme:                         scheme,
		EventRecorder:                  record.NewFakeRecorder(10),
		HelmClient:                     helmClient,
		OperatorNamespace:              "cetirun-system",
		OperatorSAName:                 "cetirun-operator-controller-manager",
		ChartVersion:                   "0.1.28",
		DomainSuffix:                   "example.com",
		AgentIdentityEnabled:           true,
		AgentIdentityTenantID:          testAgentIdentityTenantID,
		AgentIdentityBlueprintClientID: testAgentIdentityBlueprintID,
		agentIdentityGraphClientFactory: func() (agentIdentityDirectoryClient, error) {
			return directory, nil
		},
	}

	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name},
	})
	if err != nil {
		t.Fatalf("suspension returned an identity dependency error: %v", err)
	}
	if result.RequeueAfter != 30*time.Second {
		t.Fatalf("identity retry interval = %v, want 30s", result.RequeueAfter)
	}
	if !helmClient.installCalled || helmClient.lastValues["replicaCount"] != 0 {
		t.Fatalf("suspension did not apply replicaCount=0: called=%t values=%v", helmClient.installCalled, helmClient.lastValues)
	}
	remaining := &istiov1beta1.VirtualService{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: virtualService.Name, Namespace: virtualService.Namespace}, remaining); !apierrors.IsNotFound(err) {
		t.Fatalf("suspension did not remove ingress: %v", err)
	}
	updated := &cetirunv1alpha1.Agent{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: name}, updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != cetirunv1alpha1.PhaseSuspended {
		t.Fatalf("phase = %q, want Suspended", updated.Status.Phase)
	}
	if updated.Status.AgentIdentity == nil || updated.Status.AgentIdentity.Reason != "DirectoryLookupFailed" {
		t.Fatalf("identity failure was not retained: %#v", updated.Status.AgentIdentity)
	}
	condition := findStatusCondition(updated.Status.Conditions, cetirunv1alpha1.ConditionAgentIdentityReady)
	if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != "DirectoryLookupFailed" {
		t.Fatalf("identity condition was not retained: %#v", condition)
	}
}

func findStatusCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}
