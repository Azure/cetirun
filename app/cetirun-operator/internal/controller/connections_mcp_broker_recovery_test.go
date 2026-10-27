// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"reflect"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type connectionsMCPBrokerNamespaceGetErrorClient struct {
	client.Client
	err error
}

func (c *connectionsMCPBrokerNamespaceGetErrorClient) Get(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
	if _, isNamespace := object.(*corev1.Namespace); isNamespace && key.Name == "cetirun-broker-recovery" {
		return c.err
	}
	return c.Client.Get(ctx, key, object, options...)
}

func equalBrokerRecoveryNamespace(want, got *corev1.Namespace) bool {
	return string(want.UID) == string(got.UID) &&
		reflect.DeepEqual(want.Labels, got.Labels) &&
		reflect.DeepEqual(want.OwnerReferences, got.OwnerReferences)
}

func newConnectionsMCPBrokerRecoveryReconciler(t *testing.T, objects ...client.Object) (*AgentReconciler, client.WithWatch) {
	t.Helper()
	operatorScheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(operatorScheme))
	utilruntime.Must(api.AddToScheme(operatorScheme))
	utilruntime.Must(istiov1beta1.AddToScheme(operatorScheme))
	kube := fake.NewClientBuilder().WithScheme(operatorScheme).WithObjects(objects...).WithStatusSubresource(&api.Agent{}).Build()
	return &AgentReconciler{
		Client:                             kube,
		Scheme:                             operatorScheme,
		EventRecorder:                      record.NewFakeRecorder(10),
		HelmClient:                         &suspensionHelmClient{},
		OperatorNamespace:                  "cetirun-system",
		OperatorSAName:                     "cetirun-operator-controller-manager",
		ConnectionsMCPBrokerNamespace:      "cetirun-credentials",
		ConnectionsMCPBrokerServiceAccount: "cetirun-agent-broker",
	}, kube
}

func connectionsMCPBrokerRecoveryAgent(phase string) *api.Agent {
	return &api.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name: "broker-recovery", UID: "broker-recovery-agent", Generation: 3,
			Finalizers: []string{api.Finalizer},
		},
		Spec: api.AgentSpec{
			Agent: api.HarnessSpec{
				Type: api.AgentTypeHermes, Image: ptr.To("registry.example.com/hermes:1"), Port: ptr.To[int32](18789),
			},
			User:                  api.UserSpec{Alias: "broker-recovery", DisplayName: "Broker Recovery"},
			GatewayTokenSecretRef: "gateway-token",
			Helm:                  api.HelmSpec{Version: "0.1.28", Values: map[string]runtime.RawExtension{}},
			Suspended:             phase == api.PhaseSuspended,
		},
		Status: api.AgentStatus{Phase: phase, ObservedGeneration: 3},
	}
}

func setConnectionsMCPBrokerRecoveryHash(t *testing.T, kube client.Client, instance *api.Agent, reconciler *AgentReconciler, gatewaySecretHash string) {
	t.Helper()
	stored := &api.Agent{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(instance), stored); err != nil {
		t.Fatal(err)
	}
	stored.Status.ObservedOperatorHash = reconciler.configHash(stored, nil, gatewaySecretHash, "")
	if err := kube.Status().Update(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	instance.Status.ObservedOperatorHash = stored.Status.ObservedOperatorHash
}
