// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestReconcileToolsMCPBrokerRecoversMissingNamespaceFromSteadyState(t *testing.T) {
	for _, phase := range []string{api.PhaseReady, api.PhaseSuspended} {
		t.Run(string(phase), func(t *testing.T) {
			instance := toolsMCPBrokerRecoveryAgent(phase)
			reconciler, kube := newToolsMCPBrokerRecoveryReconciler(t, instance)
			// The absent namespace also means the gateway Secret is absent. Seed
			// the exact empty hash so this verifies that recovery bypasses a
			// matching steady-state cache rather than merely handling a cache miss.
			setToolsMCPBrokerRecoveryHash(t, kube, instance, reconciler, "")

			if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(instance)}); err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if !reconciler.HelmClient.(*suspensionHelmClient).installCalled {
				t.Fatal("missing namespace recovery stopped before the full reconcile applied Helm")
			}
			if phase == api.PhaseSuspended {
				if got, ok := reconciler.HelmClient.(*suspensionHelmClient).lastValues["replicaCount"].(int); !ok || got != 0 {
					t.Fatalf("suspended recovery replicaCount = %v, want integer 0", reconciler.HelmClient.(*suspensionHelmClient).lastValues["replicaCount"])
				}
			}

			namespace := &corev1.Namespace{}
			if err := kube.Get(context.Background(), types.NamespacedName{Name: instanceNamespace(instance.Name)}, namespace); err != nil {
				t.Fatalf("get recovered namespace: %v", err)
			}
			if !toolsMCPBrokerOwned(namespace, instance) {
				t.Fatalf("recovered namespace is not owned by the Agent: %#v", namespace.OwnerReferences)
			}
			for _, object := range []client.Object{
				&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: toolsMCPBrokerRoleName, Namespace: namespace.Name}},
				&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: toolsMCPBrokerRoleBindingName, Namespace: namespace.Name}},
			} {
				if err := kube.Get(context.Background(), client.ObjectKeyFromObject(object), object); err != nil {
					t.Fatalf("get recovered Broker RBAC %T: %v", object, err)
				}
				if !toolsMCPBrokerOwned(object, instance) {
					t.Fatalf("recovered Broker RBAC %T is not owned by the Agent", object)
				}
			}
		})
	}
}

func TestReconcileToolsMCPBrokerRepairsOwnedNamespaceAtSteadyState(t *testing.T) {
	instance := toolsMCPBrokerRecoveryAgent(api.PhaseReady)
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: instanceNamespace(instance.Name), OwnerReferences: toolsMCPBrokerOwner(instance),
	}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: instance.Spec.GatewayTokenSecretRef, Namespace: namespace.Name,
	}, Data: map[string][]byte{"OPENCLAW_GATEWAY_TOKEN": []byte("synthetic-token"), "CETIRUN_AUTH_KEY": []byte("synthetic-key")}}
	reconciler, kube := newToolsMCPBrokerRecoveryReconciler(t, instance, namespace, secret)
	setToolsMCPBrokerRecoveryHash(t, kube, instance, reconciler, secretContentHash(secret.Data))

	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(instance)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if reconciler.HelmClient.(*suspensionHelmClient).installCalled {
		t.Fatal("steady-state Broker RBAC repair unexpectedly applied Helm")
	}
	for _, object := range []client.Object{
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: toolsMCPBrokerRoleName, Namespace: namespace.Name}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: toolsMCPBrokerRoleBindingName, Namespace: namespace.Name}},
	} {
		if err := kube.Get(context.Background(), client.ObjectKeyFromObject(object), object); err != nil {
			t.Fatalf("get repaired Broker RBAC %T: %v", object, err)
		}
		if !toolsMCPBrokerOwned(object, instance) {
			t.Fatalf("repaired Broker RBAC %T is not owned by the Agent", object)
		}
	}
}

func TestReconcileToolsMCPBrokerRejectsUnverifiableNamespaceWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name      string
		namespace *corev1.Namespace
		getErr    error
	}{
		{
			name: "foreign namespace",
			namespace: &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name:   instanceNamespace("broker-recovery"),
				Labels: map[string]string{"test": "foreign"},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: api.GroupVersion.String(), Kind: "Agent", Name: "other", UID: "other-agent",
				}},
			}},
		},
		{
			name: "unowned legacy namespace",
			namespace: &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name: instanceNamespace("broker-recovery"), Labels: map[string]string{"test": "legacy"},
			}},
		},
		{
			name:   "forbidden namespace read",
			getErr: apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "cetirun-broker-recovery", errors.New("synthetic denial")),
		},
		{
			name:   "namespace read failure",
			getErr: fmt.Errorf("synthetic read failure"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			instance := toolsMCPBrokerRecoveryAgent(api.PhaseReady)
			objects := []client.Object{instance}
			if test.namespace != nil {
				objects = append(objects, test.namespace)
			}
			reconciler, kube := newToolsMCPBrokerRecoveryReconciler(t, objects...)
			setToolsMCPBrokerRecoveryHash(t, kube, instance, reconciler, "")
			if test.getErr != nil {
				reconciler.Client = &toolsMCPBrokerNamespaceGetErrorClient{Client: kube, err: test.getErr}
			}

			if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(instance)}); err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if reconciler.HelmClient.(*suspensionHelmClient).installCalled {
				t.Fatal("namespace ownership failure unexpectedly applied Helm")
			}
			recorder := reconciler.EventRecorder.(*record.FakeRecorder)
			select {
			case event := <-recorder.Events:
				if !strings.Contains(event, "Warning BrokerStorageAccessFailed") {
					t.Fatalf("unexpected namespace verification event: %q", event)
				}
			default:
				t.Fatal("namespace verification failure did not emit a warning event")
			}
			roleKey := types.NamespacedName{Namespace: instanceNamespace(instance.Name), Name: toolsMCPBrokerRoleName}
			if err := kube.Get(context.Background(), roleKey, &rbacv1.Role{}); !apierrors.IsNotFound(err) {
				t.Fatalf("Broker Role was created after refusing namespace: %v", err)
			}
			if test.namespace != nil {
				stored := &corev1.Namespace{}
				if err := kube.Get(context.Background(), client.ObjectKeyFromObject(test.namespace), stored); err != nil {
					t.Fatal(err)
				}
				if !equalBrokerRecoveryNamespace(test.namespace, stored) {
					t.Fatalf("namespace was adopted or changed: got %#v, want %#v", stored.ObjectMeta, test.namespace.ObjectMeta)
				}
			}
		})
	}
}

func TestReconcileToolsMCPBrokerRoleNotFoundDoesNotTriggerNamespaceRecovery(t *testing.T) {
	instance := toolsMCPBrokerRecoveryAgent(api.PhaseReady)
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: instanceNamespace(instance.Name), OwnerReferences: toolsMCPBrokerOwner(instance),
	}}
	reconciler, kube := newToolsMCPBrokerRecoveryReconciler(t, instance, namespace)
	setToolsMCPBrokerRecoveryHash(t, kube, instance, reconciler, "")
	roleReads := 0
	reconciler.Client = interceptor.NewClient(kube, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
			if _, isRole := object.(*rbacv1.Role); isRole && key.Name == toolsMCPBrokerRoleName {
				roleReads++
				// Simulate a cache that still cannot observe the newly created Role.
				return apierrors.NewNotFound(schema.GroupResource{Group: rbacv1.GroupName, Resource: "roles"}, key.Name)
			}
			return c.Get(ctx, key, object, options...)
		},
	})
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(instance)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if roleReads != 2 || reconciler.HelmClient.(*suspensionHelmClient).installCalled {
		t.Fatalf("Role NotFound entered the full reconcile: Role reads = %d", roleReads)
	}
	stored := &corev1.Namespace{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(namespace), stored); err != nil {
		t.Fatal(err)
	}
	if !equalBrokerRecoveryNamespace(namespace, stored) || stored.ResourceVersion != namespace.ResourceVersion {
		t.Fatal("Role NotFound modified the namespace")
	}
}

type toolsMCPBrokerNamespaceGetErrorClient struct {
	client.Client
	err error
}

func (c *toolsMCPBrokerNamespaceGetErrorClient) Get(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
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

func newToolsMCPBrokerRecoveryReconciler(t *testing.T, objects ...client.Object) (*AgentReconciler, client.WithWatch) {
	t.Helper()
	operatorScheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(operatorScheme))
	utilruntime.Must(api.AddToScheme(operatorScheme))
	utilruntime.Must(istiov1beta1.AddToScheme(operatorScheme))
	kube := fake.NewClientBuilder().WithScheme(operatorScheme).WithObjects(objects...).WithStatusSubresource(&api.Agent{}).Build()
	return &AgentReconciler{
		Client:                       kube,
		Scheme:                       operatorScheme,
		EventRecorder:                record.NewFakeRecorder(10),
		HelmClient:                   &suspensionHelmClient{},
		OperatorNamespace:            "cetirun-system",
		OperatorSAName:               "cetirun-operator-controller-manager",
		ToolsMCPBrokerNamespace:      "cetirun-credentials",
		ToolsMCPBrokerServiceAccount: "cetirun-agent-broker",
	}, kube
}

func toolsMCPBrokerRecoveryAgent(phase string) *api.Agent {
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

func setToolsMCPBrokerRecoveryHash(t *testing.T, kube client.Client, instance *api.Agent, reconciler *AgentReconciler, gatewaySecretHash string) {
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
