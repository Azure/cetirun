// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestConnectionsMCPBrokerStorageConfigRequiresCompleteValidCoordinates(t *testing.T) {
	for _, test := range []struct {
		name, namespace, serviceAccount string
		enabled                         bool
		wantErr                         bool
	}{
		{name: "disabled"},
		{name: "enabled", namespace: "cetirun-credentials", serviceAccount: "cetirun-agent-broker", enabled: true},
		{name: "namespace only", namespace: "cetirun-credentials", wantErr: true},
		{name: "service account only", serviceAccount: "cetirun-agent-broker", wantErr: true},
		{name: "invalid namespace", namespace: "Credentials", serviceAccount: "broker", wantErr: true},
		{name: "invalid service account", namespace: "credentials", serviceAccount: " broker", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			enabled, err := ParseConnectionsMCPBrokerStorageConfig(test.namespace, test.serviceAccount)
			if (err != nil) != test.wantErr || enabled != test.enabled {
				t.Fatalf("ParseConnectionsMCPBrokerStorageConfig() = %v, %v; want %v, error %v", enabled, err, test.enabled, test.wantErr)
			}
		})
	}
}

func TestEnsureConnectionsMCPBrokerRBACCreatesOwnedNarrowRoleIdempotently(t *testing.T) {
	ctx := context.Background()
	instance := connectionsMCPTestAgent()
	owners := connectionsMCPBrokerOwner(instance)
	controller := true
	owners[0].Controller = &controller
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: instanceNamespace(instance.Name), OwnerReferences: owners,
	}}
	reconciler, kube := connectionsMCPTestReconciler(t, namespace)
	reconciler.ConnectionsMCPBrokerNamespace = "cetirun-credentials"
	reconciler.ConnectionsMCPBrokerServiceAccount = "cetirun-agent-broker"
	if err := reconciler.ensureConnectionsMCPBrokerRBAC(ctx, instance); err != nil {
		t.Fatalf("initial ensureConnectionsMCPBrokerRBAC() error = %v", err)
	}
	role := &rbacv1.Role{}
	roleKey := types.NamespacedName{Namespace: namespace.Name, Name: connectionsMCPBrokerRoleName}
	if err := kube.Get(ctx, roleKey, role); err != nil {
		t.Fatal(err)
	}
	if !connectionsMCPBrokerOwned(role, instance) || len(role.Rules) != 1 || !reflect.DeepEqual(role.Rules[0].Verbs, []string{"get", "list", "create", "update", "delete"}) {
		t.Fatalf("Broker Role is not a narrow Agent-owned Secret role: %#v", role)
	}
	if !reflect.DeepEqual(role.Rules[0].Resources, []string{"secrets"}) || len(role.Rules[0].APIGroups) != 1 || role.Rules[0].APIGroups[0] != "" {
		t.Fatalf("Broker Role grants unexpected resources: %#v", role.Rules)
	}
	binding := &rbacv1.RoleBinding{}
	bindingKey := types.NamespacedName{Namespace: namespace.Name, Name: connectionsMCPBrokerRoleBindingName}
	if err := kube.Get(ctx, bindingKey, binding); err != nil {
		t.Fatal(err)
	}
	if !connectionsMCPBrokerOwned(binding, instance) || !reflect.DeepEqual(binding.Subjects, []rbacv1.Subject{{Kind: "ServiceAccount", Namespace: "cetirun-credentials", Name: "cetirun-agent-broker"}}) {
		t.Fatalf("Broker RoleBinding is not owned or targets the wrong ServiceAccount: %#v", binding)
	}
	resourceVersion := role.ResourceVersion + "/" + binding.ResourceVersion
	if err := reconciler.ensureConnectionsMCPBrokerRBAC(ctx, instance); err != nil {
		t.Fatalf("idempotent ensureConnectionsMCPBrokerRBAC() error = %v", err)
	}
	if err := kube.Get(ctx, roleKey, role); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, bindingKey, binding); err != nil {
		t.Fatal(err)
	}
	if resourceVersion != role.ResourceVersion+"/"+binding.ResourceVersion {
		t.Fatalf("idempotent Broker RBAC reconciliation updated objects: %q -> %q", resourceVersion, role.ResourceVersion+"/"+binding.ResourceVersion)
	}
}

func TestEnsureConnectionsMCPBrokerRBACRefusesNamespaceAndObjectCollisions(t *testing.T) {
	ctx := context.Background()
	instance := connectionsMCPTestAgent()
	foreign := connectionsMCPTestAgent()
	foreign.Name, foreign.UID = "other", "other-agent"
	for _, test := range []struct {
		name    string
		objects []client.Object
	}{
		{name: "foreign namespace", objects: []client.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: instanceNamespace(instance.Name), OwnerReferences: connectionsMCPBrokerOwner(foreign)}}}},
		{name: "foreign role", objects: []client.Object{
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: instanceNamespace(instance.Name), OwnerReferences: connectionsMCPBrokerOwner(instance)}},
			&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: connectionsMCPBrokerRoleName, Namespace: instanceNamespace(instance.Name), OwnerReferences: connectionsMCPBrokerOwner(foreign)}},
		}},
		{name: "foreign binding", objects: []client.Object{
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: instanceNamespace(instance.Name), OwnerReferences: connectionsMCPBrokerOwner(instance)}},
			&rbacv1.Role{
				ObjectMeta: metav1.ObjectMeta{Name: connectionsMCPBrokerRoleName, Namespace: instanceNamespace(instance.Name), OwnerReferences: connectionsMCPBrokerOwner(instance)},
				Rules:      []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get", "list", "create", "update", "delete"}}},
			},
			&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: connectionsMCPBrokerRoleBindingName, Namespace: instanceNamespace(instance.Name), OwnerReferences: connectionsMCPBrokerOwner(foreign)}},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			reconciler, _ := connectionsMCPTestReconciler(t, test.objects...)
			reconciler.ConnectionsMCPBrokerNamespace = "cetirun-credentials"
			reconciler.ConnectionsMCPBrokerServiceAccount = "cetirun-agent-broker"
			if err := reconciler.ensureConnectionsMCPBrokerRBAC(ctx, instance); err == nil {
				t.Fatal("ensureConnectionsMCPBrokerRBAC() accepted a foreign resource")
			}
		})
	}
}

func TestReconcileConnectionsMCPBrokerRBACRetainsOwnedGrantWhenRuntimeIsDisabled(t *testing.T) {
	ctx := context.Background()
	instance := connectionsMCPTestAgent()
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: instanceNamespace(instance.Name), OwnerReferences: connectionsMCPBrokerOwner(instance),
	}}
	reconciler, kube := connectionsMCPTestReconciler(t, namespace)
	reconciler.ConnectionsMCPBrokerNamespace = "cetirun-credentials"
	reconciler.ConnectionsMCPBrokerServiceAccount = "cetirun-agent-broker"
	if err := reconciler.reconcileConnectionsMCPBrokerRBAC(ctx, instance, true); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.reconcileConnectionsMCPBrokerRBAC(ctx, instance, false); err != nil {
		t.Fatal(err)
	}
	key := types.NamespacedName{Namespace: namespace.Name, Name: connectionsMCPBrokerRoleName}
	if err := kube.Get(ctx, key, &rbacv1.Role{}); err != nil {
		t.Fatalf("Broker Role was removed before Agent finalizer cleanup: %v", err)
	}
	if err := kube.Get(ctx, key, &rbacv1.RoleBinding{}); err != nil {
		t.Fatalf("Broker RoleBinding was removed before Agent finalizer cleanup: %v", err)
	}
}

func TestEnsureConnectionsMCPBrokerCleanupRBACHandlesNamespaceLifecycle(t *testing.T) {
	ctx := context.Background()
	instance := connectionsMCPTestAgent()
	configure := func(reconciler *AgentReconciler) {
		reconciler.ConnectionsMCPBrokerNamespace = "cetirun-credentials"
		reconciler.ConnectionsMCPBrokerServiceAccount = "cetirun-agent-broker"
	}
	for _, name := range []string{"unowned namespace", "previous Agent UID"} {
		t.Run(name, func(t *testing.T) {
			namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: instanceNamespace(instance.Name)}}
			if name == "previous Agent UID" {
				previous := instance.DeepCopy()
				previous.UID = "previous-agent-uid"
				namespace.OwnerReferences = connectionsMCPBrokerOwner(previous)
			}
			reconciler, kube := connectionsMCPTestReconciler(t, namespace)
			configure(reconciler)
			if err := reconciler.ensureConnectionsMCPBrokerCleanupRBAC(ctx, instance); err == nil {
				t.Fatal("cleanup accepted a namespace without current Agent ownership")
			}
			current := &corev1.Namespace{}
			if err := kube.Get(ctx, types.NamespacedName{Name: namespace.Name}, current); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(current.OwnerReferences, namespace.OwnerReferences) || current.ResourceVersion != namespace.ResourceVersion {
				t.Fatal("cleanup adopted or modified the namespace")
			}
			key := types.NamespacedName{Namespace: namespace.Name, Name: connectionsMCPBrokerRoleName}
			for _, object := range []client.Object{&rbacv1.Role{}, &rbacv1.RoleBinding{}} {
				if err := kube.Get(ctx, key, object); !apierrors.IsNotFound(err) {
					t.Fatalf("cleanup created %T without namespace ownership: %v", object, err)
				}
			}
		})
	}
	t.Run("live owned namespace creates grant", func(t *testing.T) {
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: instanceNamespace(instance.Name), OwnerReferences: connectionsMCPBrokerOwner(instance),
		}}
		reconciler, kube := connectionsMCPTestReconciler(t, namespace)
		configure(reconciler)
		if err := reconciler.ensureConnectionsMCPBrokerCleanupRBAC(ctx, instance); err != nil {
			t.Fatal(err)
		}
		if err := kube.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: connectionsMCPBrokerRoleName}, &rbacv1.Role{}); err != nil {
			t.Fatalf("cleanup Role was not created before Broker cleanup: %v", err)
		}
	})
	t.Run("missing namespace is idempotent", func(t *testing.T) {
		reconciler, _ := connectionsMCPTestReconciler(t)
		configure(reconciler)
		if err := reconciler.ensureConnectionsMCPBrokerCleanupRBAC(ctx, instance); err != nil {
			t.Fatalf("missing namespace blocked cleanup: %v", err)
		}
	})
	t.Run("terminating owned namespace only verifies", func(t *testing.T) {
		now := metav1.Now()
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: instanceNamespace(instance.Name), OwnerReferences: connectionsMCPBrokerOwner(instance), DeletionTimestamp: &now,
			Finalizers: []string{"test.example.com/hold"},
		}}
		seed := &AgentReconciler{ConnectionsMCPBrokerNamespace: "cetirun-credentials", ConnectionsMCPBrokerServiceAccount: "cetirun-agent-broker"}
		role, binding := seed.desiredConnectionsMCPBrokerRBAC(instance)
		reconciler, kube := connectionsMCPTestReconciler(t, namespace, role, binding)
		configure(reconciler)
		if err := reconciler.ensureConnectionsMCPBrokerCleanupRBAC(ctx, instance); err != nil {
			t.Fatal(err)
		}
		got := &rbacv1.Role{}
		if err := kube.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: role.Name}, got); err != nil {
			t.Fatal(err)
		}
		if got.ResourceVersion != role.ResourceVersion {
			t.Fatalf("terminating namespace Role was modified: %q -> %q", role.ResourceVersion, got.ResourceVersion)
		}
		gotBinding := &rbacv1.RoleBinding{}
		if err := kube.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: binding.Name}, gotBinding); err != nil {
			t.Fatal(err)
		}
		if gotBinding.ResourceVersion != binding.ResourceVersion {
			t.Fatalf("terminating namespace RoleBinding was modified: %q -> %q", binding.ResourceVersion, gotBinding.ResourceVersion)
		}
	})
}

func TestHandleDeleteCreatesBrokerCleanupRBACBeforeAuthorizationRevocation(t *testing.T) {
	instance := connectionsMCPTestAgent()
	instance.UID = "11111111-1111-1111-1111-111111111111"
	instance.Finalizers = []string{api.Finalizer}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: instanceNamespace(instance.Name), OwnerReferences: connectionsMCPBrokerOwner(instance),
	}}
	reconciler, kube := connectionsMCPTestReconciler(t, instance, namespace)
	reconciler.ConnectionsMCPBrokerNamespace = "cetirun-credentials"
	reconciler.ConnectionsMCPBrokerServiceAccount = "cetirun-agent-broker"
	if err := istiov1beta1.AddToScheme(reconciler.Scheme); err != nil {
		t.Fatal(err)
	}
	reconciler.EventRecorder = record.NewFakeRecorder(10)
	reconciler.HelmClient = &suspensionHelmClient{}
	calls := 0
	broker := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		if request.Method != http.MethodDelete {
			t.Errorf("Broker cleanup method = %s", request.Method)
		}
		role := &rbacv1.Role{}
		if err := kube.Get(request.Context(), types.NamespacedName{Namespace: instanceNamespace(instance.Name), Name: connectionsMCPBrokerRoleName}, role); err != nil {
			t.Errorf("Broker was called before cleanup Role existed: %v", err)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer broker.Close()
	reconciler.AgentIdentityBrokerURL = broker.URL
	reconciler.AgentIdentityHTTPClient = broker.Client()
	if _, err := reconciler.handleDelete(context.Background(), instance); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("Broker authorization cleanup calls = %d, want 1", calls)
	}
}

func connectionsMCPTestReconciler(t *testing.T, objects ...client.Object) (*AgentReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(api.AddToScheme(scheme))
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	return &AgentReconciler{Client: kube, Scheme: scheme, OperatorNamespace: "cetirun-system"}, kube
}

func connectionsMCPTestAgent() *api.Agent {
	return &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "connections-mcp", UID: "connections-mcp-agent"}}
}
