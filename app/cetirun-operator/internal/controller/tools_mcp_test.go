// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	toolsMCPTestOperatorNamespace = "cetirun-system"
	toolsMCPTestSecretName        = "agent-tools-mcp-ca"
)

func TestToolsMCPRuntimeAllowlistDefaultsAndFailsClosed(t *testing.T) {
	defaults, err := ParseToolsMCPRuntimeTypes("")
	if err != nil || !reflect.DeepEqual(defaults, []string{"hermes", "openclaw"}) {
		t.Fatalf("default Tools MCP runtime types = %#v, %v", defaults, err)
	}
	parsed, err := ParseToolsMCPRuntimeTypes(" openclaw,hermes;openclaw ")
	if err != nil || !reflect.DeepEqual(parsed, []string{"hermes", "openclaw"}) {
		t.Fatalf("normalized Tools MCP runtime types = %#v, %v", parsed, err)
	}
	for _, raw := range []string{"custom", "hermes,unknown", ",;"} {
		if _, err := ParseToolsMCPRuntimeTypes(raw); err == nil {
			t.Fatalf("invalid Tools MCP allowlist %q was accepted", raw)
		}
	}
}

func TestToolsMCPBrokerStorageConfigRequiresCompleteValidCoordinates(t *testing.T) {
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
			enabled, err := ParseToolsMCPBrokerStorageConfig(test.namespace, test.serviceAccount)
			if (err != nil) != test.wantErr || enabled != test.enabled {
				t.Fatalf("ParseToolsMCPBrokerStorageConfig() = %v, %v; want %v, error %v", enabled, err, test.enabled, test.wantErr)
			}
		})
	}
}

func TestEnsureToolsMCPBrokerRBACCreatesOwnedNarrowRoleIdempotently(t *testing.T) {
	ctx := context.Background()
	instance := toolsMCPTestAgent()
	owners := toolsMCPBrokerOwner(instance)
	controller := true
	owners[0].Controller = &controller
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: instanceNamespace(instance.Name), OwnerReferences: owners,
	}}
	reconciler, kube := toolsMCPTestReconciler(t, namespace)
	reconciler.ToolsMCPBrokerNamespace = "cetirun-credentials"
	reconciler.ToolsMCPBrokerServiceAccount = "cetirun-agent-broker"
	if err := reconciler.ensureToolsMCPBrokerRBAC(ctx, instance); err != nil {
		t.Fatalf("initial ensureToolsMCPBrokerRBAC() error = %v", err)
	}
	role := &rbacv1.Role{}
	roleKey := types.NamespacedName{Namespace: namespace.Name, Name: toolsMCPBrokerRoleName}
	if err := kube.Get(ctx, roleKey, role); err != nil {
		t.Fatal(err)
	}
	if !toolsMCPBrokerOwned(role, instance) || len(role.Rules) != 1 || !reflect.DeepEqual(role.Rules[0].Verbs, []string{"get", "list", "create", "update", "delete"}) {
		t.Fatalf("Broker Role is not a narrow Agent-owned Secret role: %#v", role)
	}
	if !reflect.DeepEqual(role.Rules[0].Resources, []string{"secrets"}) || len(role.Rules[0].APIGroups) != 1 || role.Rules[0].APIGroups[0] != "" {
		t.Fatalf("Broker Role grants unexpected resources: %#v", role.Rules)
	}
	binding := &rbacv1.RoleBinding{}
	bindingKey := types.NamespacedName{Namespace: namespace.Name, Name: toolsMCPBrokerRoleBindingName}
	if err := kube.Get(ctx, bindingKey, binding); err != nil {
		t.Fatal(err)
	}
	if !toolsMCPBrokerOwned(binding, instance) || !reflect.DeepEqual(binding.Subjects, []rbacv1.Subject{{Kind: "ServiceAccount", Namespace: "cetirun-credentials", Name: "cetirun-agent-broker"}}) {
		t.Fatalf("Broker RoleBinding is not owned or targets the wrong ServiceAccount: %#v", binding)
	}
	resourceVersion := role.ResourceVersion + "/" + binding.ResourceVersion
	if err := reconciler.ensureToolsMCPBrokerRBAC(ctx, instance); err != nil {
		t.Fatalf("idempotent ensureToolsMCPBrokerRBAC() error = %v", err)
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

func TestEnsureToolsMCPBrokerRBACRefusesNamespaceAndObjectCollisions(t *testing.T) {
	ctx := context.Background()
	instance := toolsMCPTestAgent()
	foreign := toolsMCPTestAgent()
	foreign.Name, foreign.UID = "other", "other-agent"
	for _, test := range []struct {
		name    string
		objects []client.Object
	}{
		{name: "foreign namespace", objects: []client.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: instanceNamespace(instance.Name), OwnerReferences: toolsMCPBrokerOwner(foreign)}}}},
		{name: "foreign role", objects: []client.Object{
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: instanceNamespace(instance.Name), OwnerReferences: toolsMCPBrokerOwner(instance)}},
			&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: toolsMCPBrokerRoleName, Namespace: instanceNamespace(instance.Name), OwnerReferences: toolsMCPBrokerOwner(foreign)}},
		}},
		{name: "foreign binding", objects: []client.Object{
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: instanceNamespace(instance.Name), OwnerReferences: toolsMCPBrokerOwner(instance)}},
			&rbacv1.Role{
				ObjectMeta: metav1.ObjectMeta{Name: toolsMCPBrokerRoleName, Namespace: instanceNamespace(instance.Name), OwnerReferences: toolsMCPBrokerOwner(instance)},
				Rules:      []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get", "list", "create", "update", "delete"}}},
			},
			&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: toolsMCPBrokerRoleBindingName, Namespace: instanceNamespace(instance.Name), OwnerReferences: toolsMCPBrokerOwner(foreign)}},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			reconciler, _ := toolsMCPTestReconciler(t, test.objects...)
			reconciler.ToolsMCPBrokerNamespace = "cetirun-credentials"
			reconciler.ToolsMCPBrokerServiceAccount = "cetirun-agent-broker"
			if err := reconciler.ensureToolsMCPBrokerRBAC(ctx, instance); err == nil {
				t.Fatal("ensureToolsMCPBrokerRBAC() accepted a foreign resource")
			}
		})
	}
}

func TestReconcileToolsMCPBrokerRBACRetainsOwnedGrantWhenRuntimeIsDisabled(t *testing.T) {
	ctx := context.Background()
	instance := toolsMCPTestAgent()
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: instanceNamespace(instance.Name), OwnerReferences: toolsMCPBrokerOwner(instance),
	}}
	reconciler, kube := toolsMCPTestReconciler(t, namespace)
	reconciler.ToolsMCPBrokerNamespace = "cetirun-credentials"
	reconciler.ToolsMCPBrokerServiceAccount = "cetirun-agent-broker"
	if err := reconciler.reconcileToolsMCPBrokerRBAC(ctx, instance, true); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.reconcileToolsMCPBrokerRBAC(ctx, instance, false); err != nil {
		t.Fatal(err)
	}
	key := types.NamespacedName{Namespace: namespace.Name, Name: toolsMCPBrokerRoleName}
	if err := kube.Get(ctx, key, &rbacv1.Role{}); err != nil {
		t.Fatalf("Broker Role was removed before Agent finalizer cleanup: %v", err)
	}
	if err := kube.Get(ctx, key, &rbacv1.RoleBinding{}); err != nil {
		t.Fatalf("Broker RoleBinding was removed before Agent finalizer cleanup: %v", err)
	}
}

func TestDisabledToolsMCPRuntimeStillReceivesBrokerCleanupRBAC(t *testing.T) {
	ctx := context.Background()
	instance := toolsMCPTestAgent()
	instance.Spec.Agent.Type = api.AgentTypeOpenClaw
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: instanceNamespace(instance.Name), OwnerReferences: toolsMCPBrokerOwner(instance),
	}}
	reconciler, kube := toolsMCPTestReconciler(t, namespace)
	reconciler.ToolsMCPRuntimeTypes = []string{"hermes"}
	reconciler.ToolsMCPBrokerNamespace = "cetirun-credentials"
	reconciler.ToolsMCPBrokerServiceAccount = "cetirun-agent-broker"

	enabled, err := reconciler.toolsMCPEnabledFor(instance)
	if err != nil || enabled {
		t.Fatalf("OpenClaw Tools MCP runtime gate = %v, %v", enabled, err)
	}
	if err := reconciler.reconcileToolsMCPBrokerRBAC(ctx, instance, !sandboxRequested(instance, nil)); err != nil {
		t.Fatalf("Broker cleanup RBAC for disabled runtime: %v", err)
	}
	roleKey := types.NamespacedName{Namespace: namespace.Name, Name: toolsMCPBrokerRoleName}
	role := &rbacv1.Role{}
	if err := kube.Get(ctx, roleKey, role); err != nil {
		t.Fatalf("get Broker cleanup Role: %v", err)
	}
	if len(role.Rules) != 1 || !reflect.DeepEqual(role.Rules[0], rbacv1.PolicyRule{
		APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get", "list", "create", "update", "delete"},
	}) {
		t.Fatalf("Broker cleanup Role grants = %#v", role.Rules)
	}
	binding := &rbacv1.RoleBinding{}
	if err := kube.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: toolsMCPBrokerRoleBindingName}, binding); err != nil {
		t.Fatalf("get Broker cleanup RoleBinding: %v", err)
	}
	wantSubject := []rbacv1.Subject{{Kind: "ServiceAccount", Namespace: "cetirun-credentials", Name: "cetirun-agent-broker"}}
	if !reflect.DeepEqual(binding.Subjects, wantSubject) {
		t.Fatalf("Broker cleanup RoleBinding subjects = %#v, want %#v", binding.Subjects, wantSubject)
	}
}

func TestEnsureToolsMCPBrokerCleanupRBACHandlesUpgradeDeletion(t *testing.T) {
	ctx := context.Background()
	instance := toolsMCPTestAgent()
	configure := func(reconciler *AgentReconciler) {
		reconciler.ToolsMCPBrokerNamespace = "cetirun-credentials"
		reconciler.ToolsMCPBrokerServiceAccount = "cetirun-agent-broker"
	}
	t.Run("live owned namespace creates grant", func(t *testing.T) {
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: instanceNamespace(instance.Name), OwnerReferences: toolsMCPBrokerOwner(instance),
		}}
		reconciler, kube := toolsMCPTestReconciler(t, namespace)
		configure(reconciler)
		if err := reconciler.ensureToolsMCPBrokerCleanupRBAC(ctx, instance); err != nil {
			t.Fatal(err)
		}
		if err := kube.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: toolsMCPBrokerRoleName}, &rbacv1.Role{}); err != nil {
			t.Fatalf("cleanup Role was not created before Broker cleanup: %v", err)
		}
	})
	t.Run("missing namespace is idempotent", func(t *testing.T) {
		reconciler, _ := toolsMCPTestReconciler(t)
		configure(reconciler)
		if err := reconciler.ensureToolsMCPBrokerCleanupRBAC(ctx, instance); err != nil {
			t.Fatalf("missing namespace blocked cleanup: %v", err)
		}
	})
	t.Run("terminating owned namespace only verifies", func(t *testing.T) {
		now := metav1.Now()
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: instanceNamespace(instance.Name), OwnerReferences: toolsMCPBrokerOwner(instance), DeletionTimestamp: &now,
			Finalizers: []string{"test.example.com/hold"},
		}}
		seed := &AgentReconciler{ToolsMCPBrokerNamespace: "cetirun-credentials", ToolsMCPBrokerServiceAccount: "cetirun-agent-broker"}
		role, binding := seed.desiredToolsMCPBrokerRBAC(instance)
		reconciler, kube := toolsMCPTestReconciler(t, namespace, role, binding)
		configure(reconciler)
		if err := reconciler.ensureToolsMCPBrokerCleanupRBAC(ctx, instance); err != nil {
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
	instance := toolsMCPTestAgent()
	instance.UID = "11111111-1111-1111-1111-111111111111"
	instance.Finalizers = []string{api.Finalizer}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: instanceNamespace(instance.Name), OwnerReferences: toolsMCPBrokerOwner(instance),
	}}
	reconciler, kube := toolsMCPTestReconciler(t, instance, namespace)
	reconciler.ToolsMCPBrokerNamespace = "cetirun-credentials"
	reconciler.ToolsMCPBrokerServiceAccount = "cetirun-agent-broker"
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
		if err := kube.Get(request.Context(), types.NamespacedName{Namespace: instanceNamespace(instance.Name), Name: toolsMCPBrokerRoleName}, role); err != nil {
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

func TestToolsMCPRuntimeGateOnlyEnablesAllowedRuntime(t *testing.T) {
	config := AgentIdentityHelmConfig{
		ToolsMCPURL:          "https://agent-tools-mcp.cetirun-tools.svc.cluster.local/mcp",
		ToolsMCPAudience:     "agent-tools-mcp",
		ToolsMCPCASecretName: "agent-tools-mcp-ca",
		ToolsMCPRuntimeTypes: []string{"hermes"},
	}
	for _, runtimeType := range []api.AgentType{api.AgentTypeHermes, api.AgentTypeOpenClaw} {
		t.Run(string(runtimeType), func(t *testing.T) {
			instance := newTestInstance("tools", "Tools", "", "gateway-token")
			instance.Spec.Agent.Type = runtimeType
			values, err := buildHelmValuesWithAgentIdentity(instance, nil, "example.com", "", nil, "", "", config)
			if err != nil {
				t.Fatal(err)
			}
			toolsMCP := values["toolsMCP"].(map[string]interface{})
			enabled, _ := toolsMCP["enabled"].(bool)
			if enabled != (runtimeType == api.AgentTypeHermes) {
				t.Fatalf("Tools MCP enabled for %s = %v", runtimeType, enabled)
			}
			if !enabled {
				if _, present := toolsMCP["caSecretName"]; present {
					t.Fatalf("disabled runtime received a Tools MCP CA projection: %#v", toolsMCP)
				}
				if toolsMCP["url"] != "" {
					t.Fatalf("disabled runtime received a Tools MCP endpoint: %#v", toolsMCP)
				}
			}
		})
	}
}

func TestToolsMCPRuntimeGateParticipatesInOperatorHash(t *testing.T) {
	instance := newTestInstance("tools", "Tools", "", "gateway-token")
	instance.Spec.Agent.Type = api.AgentTypeOpenClaw
	reconciler := &AgentReconciler{
		ToolsMCPURL:          "https://agent-tools-mcp.cetirun-tools.svc.cluster.local/mcp",
		ToolsMCPRuntimeTypes: []string{"hermes"},
	}
	disabled := reconciler.configHash(instance, nil, "", "")
	reconciler.ToolsMCPRuntimeTypes = []string{"hermes", "openclaw"}
	enabled := reconciler.configHash(instance, nil, "", "")
	if disabled == enabled {
		t.Fatal("enabling Tools MCP for this runtime did not invalidate the operator hash")
	}
	reconciler.ToolsMCPRuntimeTypes = []string{"openclaw", "hermes"}
	if reordered := reconciler.configHash(instance, nil, "", ""); reordered != enabled {
		t.Fatalf("allowlist ordering changed operator hash: %q != %q", reordered, enabled)
	}
}

func TestToolsMCPOnlyDeletionBlocksUntilGrantsAreRevoked(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			instance := toolsMCPTestAgent()
			instance.UID = "11111111-1111-1111-1111-111111111111"
			instance.Finalizers = []string{api.Finalizer}
			reconciler, kube := toolsMCPTestReconciler(t, instance)
			if err := istiov1beta1.AddToScheme(reconciler.Scheme); err != nil {
				t.Fatal(err)
			}
			reconciler.EventRecorder = record.NewFakeRecorder(10)
			reconciler.HelmClient = &suspensionHelmClient{}
			calls := 0
			broker := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls++
				if request.Method != http.MethodDelete || request.URL.Path != "/operator/v1/instances/11111111-1111-1111-1111-111111111111/authorization" {
					t.Error("unexpected cleanup request")
				}
				writer.WriteHeader(status)
			}))
			defer broker.Close()
			reconciler.AgentIdentityBrokerURL = broker.URL
			reconciler.AgentIdentityHTTPClient = broker.Client()
			result, err := reconciler.handleDelete(context.Background(), instance)
			if err != nil || calls != 1 {
				t.Fatalf("cleanup request missing: %v, calls %d", err, calls)
			}
			var current api.Agent
			if err := kube.Get(context.Background(), types.NamespacedName{Name: instance.Name}, &current); err != nil {
				t.Fatal(err)
			}
			if status == http.StatusServiceUnavailable && (len(current.Finalizers) == 0 || result.RequeueAfter == 0) {
				t.Fatal("failed grant revocation removed finalizer")
			}
			if status == http.StatusNoContent && len(current.Finalizers) != 0 {
				t.Fatal("successful cleanup retained finalizer")
			}
		})
	}
}

func TestDisabledToolsMCPRuntimeStillCleansLegacyGrantsOnDelete(t *testing.T) {
	instance := toolsMCPTestAgent()
	instance.UID = "11111111-1111-1111-1111-111111111111"
	instance.Spec.Agent.Type = api.AgentTypeOpenClaw
	instance.Finalizers = []string{api.Finalizer}
	reconciler, _ := toolsMCPTestReconciler(t, instance)
	reconciler.ToolsMCPRuntimeTypes = []string{"hermes"}
	if enabled, err := reconciler.toolsMCPEnabledFor(instance); err != nil || enabled {
		t.Fatalf("OpenClaw runtime gate = %v, %v", enabled, err)
	}
	if err := istiov1beta1.AddToScheme(reconciler.Scheme); err != nil {
		t.Fatal(err)
	}
	reconciler.EventRecorder = record.NewFakeRecorder(10)
	reconciler.HelmClient = &suspensionHelmClient{}
	calls := 0
	broker := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer broker.Close()
	reconciler.AgentIdentityBrokerURL = broker.URL
	reconciler.AgentIdentityHTTPClient = broker.Client()
	if _, err := reconciler.handleDelete(context.Background(), instance); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("disabled runtime skipped legacy grant cleanup; calls = %d", calls)
	}
}

func TestAgentGrantCleanupRejectsRedirects(t *testing.T) {
	redirectedCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		redirectedCalls++
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	broker := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer broker.Close()
	reconciler := &AgentReconciler{AgentIdentityBrokerURL: broker.URL, AgentIdentityHTTPClient: broker.Client()}
	err := reconciler.deleteAgentAuthorization(context.Background(), "11111111-1111-1111-1111-111111111111")
	if err == nil || redirectedCalls != 0 {
		t.Fatalf("redirected cleanup = %v, target calls %d", err, redirectedCalls)
	}
}

func TestEnsureToolsMCPCACreatesOwnerBoundPublicCASecret(t *testing.T) {
	ctx := context.Background()
	ca := toolsMCPTestCA(t, 1)
	source := toolsMCPTestSource(ca)
	reconciler, kube := toolsMCPTestReconciler(t, source)
	instance := toolsMCPTestAgent()

	if err := reconciler.ensureToolsMCPCA(ctx, instance); err != nil {
		t.Fatalf("ensureToolsMCPCA() error = %v", err)
	}
	target := &corev1.Secret{}
	key := types.NamespacedName{Namespace: instanceNamespace(instance.Name), Name: toolsMCPTestSecretName}
	if err := kube.Get(ctx, key, target); err != nil {
		t.Fatalf("get copied CA Secret: %v", err)
	}
	if len(target.Data) != 1 || string(target.Data["ca.crt"]) != string(ca) {
		t.Fatalf("target data = %#v, want only ca.crt", target.Data)
	}
	if len(target.OwnerReferences) != 1 || target.OwnerReferences[0].UID != instance.UID || target.OwnerReferences[0].Kind != "Agent" || target.OwnerReferences[0].Name != instance.Name {
		t.Fatalf("target owner references = %#v", target.OwnerReferences)
	}
}

func TestEnsureToolsMCPCARotationUpdatesSecretAndHash(t *testing.T) {
	ctx := context.Background()
	firstCA, secondCA := toolsMCPTestCA(t, 1), toolsMCPTestCA(t, 2)
	reconciler, kube := toolsMCPTestReconciler(t, toolsMCPTestSource(firstCA))
	instance := toolsMCPTestAgent()
	if err := reconciler.ensureToolsMCPCA(ctx, instance); err != nil {
		t.Fatalf("initial ensureToolsMCPCA() error = %v", err)
	}
	firstHash, err := reconciler.toolsMCPCAHash(ctx)
	if err != nil || firstHash == "" {
		t.Fatalf("initial toolsMCPCAHash() = %q, %v", firstHash, err)
	}
	source := &corev1.Secret{}
	sourceKey := types.NamespacedName{Namespace: toolsMCPTestOperatorNamespace, Name: toolsMCPTestSecretName}
	if err := kube.Get(ctx, sourceKey, source); err != nil {
		t.Fatal(err)
	}
	source.Data = map[string][]byte{"ca.crt": secondCA, "tls.key": []byte("source-private-key")}
	if err := kube.Update(ctx, source); err != nil {
		t.Fatal(err)
	}
	secondHash, err := reconciler.toolsMCPCAHash(ctx)
	if err != nil || secondHash == "" || secondHash == firstHash {
		t.Fatalf("rotated toolsMCPCAHash() = %q, %v; first = %q", secondHash, err, firstHash)
	}
	if err := reconciler.ensureToolsMCPCA(ctx, instance); err != nil {
		t.Fatalf("rotated ensureToolsMCPCA() error = %v", err)
	}
	target := &corev1.Secret{}
	if err := kube.Get(ctx, types.NamespacedName{Namespace: instanceNamespace(instance.Name), Name: toolsMCPTestSecretName}, target); err != nil {
		t.Fatal(err)
	}
	if len(target.Data) != 1 || string(target.Data["ca.crt"]) != string(secondCA) {
		t.Fatalf("rotated target data = %#v", target.Data)
	}
}

func TestEnsureToolsMCPCARejectsUnownedTarget(t *testing.T) {
	ctx := context.Background()
	instance := toolsMCPTestAgent()
	target := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: toolsMCPTestSecretName, Namespace: instanceNamespace(instance.Name), OwnerReferences: []metav1.OwnerReference{{APIVersion: api.GroupVersion.String(), Kind: "Agent", Name: "other", UID: "other-agent"}}},
		Data:       map[string][]byte{"ca.crt": []byte("unowned")},
	}
	reconciler, kube := toolsMCPTestReconciler(t, toolsMCPTestSource(toolsMCPTestCA(t, 1)), target)
	if err := reconciler.ensureToolsMCPCA(ctx, instance); err == nil {
		t.Fatal("ensureToolsMCPCA() accepted an unowned target")
	}
	stored := &corev1.Secret{}
	if err := kube.Get(ctx, types.NamespacedName{Namespace: target.Namespace, Name: target.Name}, stored); err != nil {
		t.Fatal(err)
	}
	if string(stored.Data["ca.crt"]) != "unowned" {
		t.Fatalf("unowned target was modified: %#v", stored.Data)
	}
}

func TestToolsMCPCARejectsMissingAndInvalidSources(t *testing.T) {
	tests := []struct {
		name   string
		source *corev1.Secret
	}{
		{name: "missing source"},
		{name: "missing CA", source: toolsMCPTestSource(nil)},
		{name: "invalid PEM", source: toolsMCPTestSource([]byte("not a certificate"))},
		{name: "private key material", source: toolsMCPTestSource(append(toolsMCPTestCA(t, 1), []byte("-----BEGIN PRIVATE KEY-----")...))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			objects := []client.Object{}
			if test.source != nil {
				objects = append(objects, test.source)
			}
			reconciler, kube := toolsMCPTestReconciler(t, objects...)
			if _, err := reconciler.toolsMCPCA(context.Background()); err == nil {
				t.Fatal("toolsMCPCA() accepted an unavailable or invalid source")
			}
			if err := reconciler.ensureToolsMCPCA(context.Background(), toolsMCPTestAgent()); err == nil {
				t.Fatal("ensureToolsMCPCA() accepted an unavailable or invalid source")
			}
			target := &corev1.Secret{}
			key := types.NamespacedName{Namespace: instanceNamespace(toolsMCPTestAgent().Name), Name: toolsMCPTestSecretName}
			if err := kube.Get(context.Background(), key, target); err == nil {
				t.Fatal("target Secret was created from an unavailable or invalid source")
			}
		})
	}
}

func toolsMCPTestReconciler(t *testing.T, objects ...client.Object) (*AgentReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(api.AddToScheme(scheme))
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	return &AgentReconciler{Client: kube, Scheme: scheme, OperatorNamespace: toolsMCPTestOperatorNamespace, ToolsMCPURL: "https://agent-tools-mcp.example.com/mcp", ToolsMCPCASecretName: toolsMCPTestSecretName}, kube
}

func toolsMCPTestSource(ca []byte) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: toolsMCPTestSecretName, Namespace: toolsMCPTestOperatorNamespace}, Data: map[string][]byte{"ca.crt": ca, "tls.key": []byte("source-private-key")}}
}

func toolsMCPTestAgent() *api.Agent {
	return &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "tools-mcp", UID: "tools-mcp-agent"}}
}

func toolsMCPTestCA(t *testing.T, serial int64) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "tools-mcp-test-ca"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
