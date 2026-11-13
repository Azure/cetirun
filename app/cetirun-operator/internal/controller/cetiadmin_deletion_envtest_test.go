// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package controller

import (
	"reflect"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/internal/registryauth"
	"github.com/go-logr/logr"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestCetiAdminHeldTargetWithdrawalAPIServer(t *testing.T) {
	ctrl.SetLogger(logr.Discard())
	h := newCetiAdminInstallationAPI(t, true)
	agent, namespace := h.target(t, "held-target", "engineering", api.AgentTypeCustom, true)
	h.customReceipt(t, agent, namespace)
	policy, policyBinding := retainedDeletionExecPolicy(agent, namespace)
	h.create(t, policy)
	h.create(t, policyBinding)
	h.converge(t)
	h.runtimeAccess(t, agent.Name)
	if err := h.operator.Get(h.ctx, client.ObjectKeyFromObject(agent), agent); err != nil {
		t.Fatal(err)
	}
	if agent.Annotations[registryauth.ExecAdminBindingAnnotation] == "" {
		t.Fatal("fixture never acquired exec binding")
	}
	before := agent.DeepCopy()
	agent.Finalizers = append(agent.Finalizers, sandbox.HoldFinalizer)
	if err := h.operator.Patch(h.ctx, agent, client.MergeFrom(before)); err != nil {
		t.Fatal(err)
	}
	agent.Status.Policy = &api.PolicyStatus{Phase: "Denied"}
	if err := h.operator.Status().Update(h.ctx, agent); err != nil {
		t.Fatal(err)
	}
	retained := agent.DeepCopy()
	if err := h.installer.RevokeInstallationAccess(h.ctx, h.state(t)); err != nil {
		t.Fatal(err)
	}
	if err := h.operator.Get(h.ctx, client.ObjectKeyFromObject(agent), agent); err != nil {
		t.Fatal(err)
	}
	if agent.Annotations[registryauth.ExecAdminBindingAnnotation] != "" ||
		!reflect.DeepEqual(agent.Spec, retained.Spec) || !reflect.DeepEqual(agent.Finalizers, retained.Finalizers) ||
		!reflect.DeepEqual(agent.Status.Policy, retained.Status.Policy) || agent.UID != retained.UID {
		t.Fatal("withdrawal must remove only the exec binding and preserve the held lifetime")
	}
	h.converge(t)
	if !h.state(t).CanRun() {
		t.Fatal("CetiAdmin did not recover after held-target withdrawal")
	}
}

func TestCetiAdminBoundAgentDeletionAPIServer(t *testing.T) {
	ctrl.SetLogger(logr.Discard())
	for _, name := range []string{"custom", "openclaw", "hermes", "held-helm-hermes"} {
		t.Run(name, func(t *testing.T) {
			kind := api.AgentType(name)
			if name == "held-helm-hermes" {
				kind = api.AgentTypeHermes
			}
			h := newCetiAdminInstallationAPI(t, true)
			if _, err := envtest.InstallCRDs(h.config, envtest.CRDInstallOptions{CRDs: []*apiextensionsv1.CustomResourceDefinition{
				nativeStartExternalCRD("networking.istio.io", "v1beta1", "VirtualService", "virtualservices"),
			}}); err != nil {
				t.Fatal(err)
			}
			agent, namespace := h.target(t, "retained", "engineering", kind, true)
			if name == "held-helm-hermes" {
				namespace.Labels["app.kubernetes.io/managed-by"] = "cetirun-operator"
				if err := h.operator.Update(h.ctx, namespace); err != nil {
					t.Fatal(err)
				}
				if err := h.admin.Delete(h.ctx, namespace); !apierrors.IsForbidden(err) {
					t.Fatalf("managed namespace deletion must remain Operator-only: %v", err)
				}
			}
			// Seed an already retained runtime. Neither its old chart nor its image
			// is fetched or upgraded by the candidate control-plane cleanup.
			agent.Spec.Suspended = true
			agent.Spec.Helm.Version = "0.1.0"
			agent.Spec.Agent.Image = ptr.To("registry.example.com/retained-runtime@sha256:" + strings.Repeat("a", 64))
			agent.Annotations = map[string]string{"cetirun.microsoft.com/native-auth-rotation-request-id": "retained-rotation-12345678"}
			if kind != api.AgentTypeCustom {
				h.create(t, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: agent.Spec.GatewayTokenSecretRef, Namespace: namespace.Name,
					Annotations: map[string]string{"cetirun.microsoft.com/native-auth-rotation-request-id": "retained-rotation-12345678"}},
					Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"CETIRUN_AUTH_KEY": []byte("synthetic-rotated-key")}})
			}
			agent.Finalizers = []string{api.Finalizer}
			if kind == api.AgentTypeCustom {
				agent.Finalizers = append(agent.Finalizers, customOwnershipFinalizer)
			}
			if err := h.operator.Update(h.ctx, agent); err != nil {
				t.Fatal(err)
			}
			agent.Status.Phase, agent.Status.ObservedGeneration = api.PhaseSuspended, agent.Generation
			if err := h.operator.Status().Update(h.ctx, agent); err != nil {
				t.Fatal(err)
			}
			if kind == api.AgentTypeCustom {
				h.customReceipt(t, agent, namespace)
				receipt := &corev1.ConfigMap{}
				if err := h.operator.Get(h.ctx, h.installer.Runtime.ownershipKey(agent), receipt); err != nil {
					t.Fatal(err)
				}
				receipt.Finalizers = []string{customOwnershipFinalizer}
				if err := h.operator.Update(h.ctx, receipt); err != nil {
					t.Fatal(err)
				}
			}
			owner := []metav1.OwnerReference{registryauth.Owner(agent)}
			home := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "custom-home", Namespace: namespace.Name, OwnerReferences: owner},
				Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}}
			h.create(t, home)
			workload := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: instanceServiceName(agent.Name), Namespace: namespace.Name,
				OwnerReferences: owner, Annotations: map[string]string{customAgentUID: string(agent.UID)}}, Spec: appsv1.StatefulSetSpec{
				Replicas: ptr.To(int32(0)), ServiceName: "retained", Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "retained"}},
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "retained"}}, Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "cetirun", Image: *agent.Spec.Agent.Image}},
				}},
			}}
			if name == "held-helm-hermes" {
				workload.OwnerReferences = nil
				workload.Labels = map[string]string{"app.kubernetes.io/managed-by": "Helm"}
				workload.Annotations = map[string]string{"meta.helm.sh/release-name": helmReleaseName(agent.Name), "meta.helm.sh/release-namespace": namespace.Name}
			}
			h.create(t, workload)
			policy, policyBinding := retainedDeletionExecPolicy(agent, namespace)
			h.create(t, policy)
			h.create(t, policyBinding)
			h.converge(t)
			h.runtimeAccess(t, agent.Name)
			if err := h.operator.Get(h.ctx, client.ObjectKeyFromObject(agent), agent); err != nil {
				t.Fatal(err)
			}
			if agent.Annotations[registryauth.ExecAdminBindingAnnotation] == "" {
				t.Fatal("retained fixture never acquired its Operator-managed binding")
			}
			if name == "held-helm-hermes" {
				before := agent.DeepCopy()
				agent.Finalizers = append(agent.Finalizers, sandbox.HoldFinalizer)
				if err := h.operator.Patch(h.ctx, agent, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
					t.Fatal(err)
				}
			}
			retained := agent.DeepCopy()
			machine := h.machine(t)
			h.assertPermission(t, machine, "create", "", "pods", "exec", namespace.Name, "runtime-pod", true)
			if err := machine.Delete(h.ctx, agent, client.Preconditions{UID: &agent.UID, ResourceVersion: &agent.ResourceVersion}); !apierrors.IsForbidden(err) {
				t.Fatalf("CetiAdmin runtime could delete the Agent: %v", err)
			}

			// An ordinary Portal-style service account has only Agent API rights,
			// and uses neither the Operator identity nor system:masters.
			h.create(t, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "portal-delete", Namespace: "operator-test"}})
			portal := h.newClient(t, h.accountConfig(t, "operator-test", "portal-delete", nil), h.operator.Scheme())
			h.create(t, &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "portal-delete"}, Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{api.GroupVersion.Group}, Resources: []string{"agents"}, Verbs: []string{"get", "delete", "patch"},
			}}})
			h.create(t, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "portal-delete"},
				RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "portal-delete"},
				Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Namespace: "operator-test", Name: "portal-delete"}}})
			h.assertPermission(t, portal, "delete", api.GroupVersion.Group, "agents", "", "", agent.Name, true)
			h.create(t, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "namespace-controller", Namespace: "operator-test"}})
			namespaceController := h.newClient(t, h.accountConfig(t, "operator-test", "namespace-controller", nil), h.operator.Scheme())
			h.create(t, &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "namespace-cleanup", Namespace: namespace.Name}, Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get", "list", "delete", "deletecollection"},
			}}})
			h.create(t, &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "namespace-cleanup", Namespace: namespace.Name},
				RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "namespace-cleanup"},
				Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Namespace: "operator-test", Name: "namespace-controller"}}})
			h.assertPermission(t, namespaceController, "deletecollection", "", "secrets", "", namespace.Name, "", true)
			for _, value := range []string{"", "replacement-binding"} {
				next := agent.DeepCopy()
				if value == "" {
					delete(next.Annotations, registryauth.ExecAdminBindingAnnotation)
				} else {
					next.Annotations[registryauth.ExecAdminBindingAnnotation] = value
				}
				err := portal.Patch(h.ctx, next, client.MergeFromWithOptions(agent, client.MergeFromWithOptimisticLock{}))
				if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "only the Operator") {
					t.Fatalf("Portal binding mutation did not reach protection: %v", err)
				}
			}
			installed := h.agent(t)
			if err := portal.Delete(h.ctx, installed); !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "Operator-owned") {
				t.Fatalf("DELETE bypassed installation protection: %v", err)
			}
			staleVersion := "1"
			if err := portal.Delete(h.ctx, agent, client.Preconditions{UID: &agent.UID, ResourceVersion: &staleVersion}); !apierrors.IsConflict(err) {
				t.Fatalf("DELETE accepted a stale resourceVersion: %v", err)
			}
			if err := portal.Delete(h.ctx, agent, client.Preconditions{UID: &agent.UID, ResourceVersion: &agent.ResourceVersion}); err != nil {
				t.Fatalf("ordinary authorized DELETE of a bound Agent failed: %v", err)
			}
			if err := h.operator.Get(h.ctx, client.ObjectKeyFromObject(agent), agent); err != nil || agent.DeletionTimestamp == nil {
				t.Fatalf("DELETE did not persist its timestamp: %v", err)
			}
			// Exercise withdrawal while the API still retains the deleting Agent,
			// before any namespace or finalizer garbage collection can hide it.
			h.runtimeAccess(t, agent.Name)
			if err := h.operator.Get(h.ctx, client.ObjectKeyFromObject(agent), agent); err != nil {
				t.Fatal(err)
			}
			if agent.Annotations[registryauth.ExecAdminBindingAnnotation] != "" || agent.UID != retained.UID ||
				!reflect.DeepEqual(agent.Spec, retained.Spec) || !reflect.DeepEqual(agent.Finalizers, retained.Finalizers) {
				t.Fatal("binding withdrawal changed retained identity, runtime pins, or cleanup finalizers")
			}
			if err := h.operator.Get(h.ctx, client.ObjectKeyFromObject(policy), policy); err != nil ||
				len(policy.Spec.MatchConditions) != 0 || policy.Annotations[cetiAdminExecReceipt] != "" {
				t.Fatalf("exec exception or receipt survived withdrawal: %v", err)
			}
			h.assertPermission(t, machine, "create", "", "pods", "exec", namespace.Name, "runtime-pod", false)
			version := agent.ResourceVersion
			h.runtimeAccess(t, agent.Name)
			if err := h.operator.Get(h.ctx, client.ObjectKeyFromObject(agent), agent); err != nil || agent.ResourceVersion != version {
				t.Fatalf("repeated withdrawal was not stable: %v", err)
			}

			// Use a fresh controller as after a control-plane upgrade. No Broker or
			// Gateway was selected for these synthetic retained runtimes.
			r := &AgentReconciler{Client: h.operator, APIReader: h.operator, Scheme: h.operator.Scheme(), CetiAdmin: h.installer,
				OperatorNamespace: h.source.Namespace, HelmClient: h.helm, EventRecorder: record.NewFakeRecorder(32)}
			request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(agent)}
			if _, err := r.Reconcile(h.ctx, request); err != nil {
				t.Fatalf("normal deletion reconciliation failed: %v", err)
			}
			if err := h.operator.Get(h.ctx, client.ObjectKeyFromObject(namespace), namespace); err != nil || namespace.DeletionTimestamp == nil {
				t.Fatalf("controller did not request namespace cleanup: %v", err)
			}
			if kind == api.AgentTypeCustom {
				if err := h.operator.Get(h.ctx, client.ObjectKeyFromObject(agent), agent); err != nil || len(agent.Finalizers) == 0 {
					t.Fatalf("Custom released finalizers before namespace cleanup: %v", err)
				}
			}
			completeRetainedDeletionNamespaceGC(t, h, namespace, namespaceController)
			if _, err := r.Reconcile(h.ctx, request); err != nil {
				t.Fatalf("normal finalizer cleanup failed: %v", err)
			}
			for _, object := range []client.Object{&api.Agent{}, &corev1.Namespace{}, &corev1.PersistentVolumeClaim{}, &appsv1.StatefulSet{}} {
				key := client.ObjectKeyFromObject(agent)
				switch object.(type) {
				case *corev1.Namespace:
					key = client.ObjectKeyFromObject(namespace)
				case *corev1.PersistentVolumeClaim:
					key = client.ObjectKeyFromObject(home)
				case *appsv1.StatefulSet:
					key = client.ObjectKeyFromObject(workload)
				}
				if err := h.admin.Get(h.ctx, key, object); !apierrors.IsNotFound(err) {
					t.Fatalf("cleanup retained %T %s: %v", object, key, err)
				}
			}
			if kind == api.AgentTypeCustom {
				receipt := &corev1.ConfigMap{}
				if err := h.admin.Get(h.ctx, r.ownershipKey(agent), receipt); err != nil || len(receipt.Finalizers) != 0 {
					t.Fatalf("Custom ownership finalizer survived cleanup: %v", err)
				}
			}
		})
	}
}

func retainedDeletionExecPolicy(agent *api.Agent, namespace *corev1.Namespace) (*admissionv1.ValidatingAdmissionPolicy, *admissionv1.ValidatingAdmissionPolicyBinding) {
	metadata := metav1.ObjectMeta{Name: instanceServiceName(agent.Name) + "-" + namespace.Name + "-deny-exec",
		OwnerReferences: []metav1.OwnerReference{registryauth.Owner(agent)}, Annotations: map[string]string{
			"meta.helm.sh/release-name": helmReleaseName(agent.Name), "meta.helm.sh/release-namespace": namespace.Name,
		}}
	return &admissionv1.ValidatingAdmissionPolicy{ObjectMeta: metadata, Spec: admissionv1.ValidatingAdmissionPolicySpec{
		FailurePolicy: ptr.To(admissionv1.Fail), MatchConstraints: &admissionv1.MatchResources{
			NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": namespace.Name}},
			ResourceRules: []admissionv1.NamedRuleWithOperations{{RuleWithOperations: admissionv1.RuleWithOperations{
				Operations: []admissionv1.OperationType{admissionv1.Connect}, Rule: admissionv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"pods/exec"}},
			}}},
		}, Validations: []admissionv1.Validation{{Expression: "false", Message: "kubectl exec is not allowed on openclaw pods"}},
	}}, &admissionv1.ValidatingAdmissionPolicyBinding{ObjectMeta: *metadata.DeepCopy(), Spec: admissionv1.ValidatingAdmissionPolicyBindingSpec{
		PolicyName: metadata.Name, ValidationActions: []admissionv1.ValidationAction{admissionv1.Deny},
	}}
}

// Envtest has no namespace/volume controllers. Only simulate their deletion of
// dependents after the Operator requested namespace deletion; never strip any
// CetiRun finalizer. This is API cleanup evidence, not physical CSI reclamation.
func completeRetainedDeletionNamespaceGC(t *testing.T, h *cetiAdminInstallationAPI, namespace *corev1.Namespace, namespaceController client.Client) {
	t.Helper()
	if namespace.DeletionTimestamp == nil || len(namespace.Finalizers) != 0 {
		t.Fatal("namespace cleanup was not released by the Operator")
	}
	// Secret cleanup uses ordinary controller credentials, not the Operator or admin.
	if err := namespaceController.DeleteAllOf(h.ctx, &corev1.Secret{}, client.InNamespace(namespace.Name)); err != nil {
		t.Fatalf("namespace controller could not delete retained native auth receipts: %v", err)
	}
	secrets := &corev1.SecretList{}
	if err := namespaceController.List(h.ctx, secrets, client.InNamespace(namespace.Name)); err != nil || len(secrets.Items) != 0 {
		t.Fatalf("namespace still contains Secrets after cleanup: count=%d error=%v", len(secrets.Items), err)
	}
	for _, list := range []client.ObjectList{&appsv1.StatefulSetList{}, &corev1.PersistentVolumeClaimList{}, &corev1.ServiceAccountList{}, &rbacv1.RoleList{}, &rbacv1.RoleBindingList{}} {
		if err := h.admin.List(h.ctx, list, client.InNamespace(namespace.Name)); err != nil {
			t.Fatal(err)
		}
		if err := apimeta.EachListItem(list, func(item runtime.Object) error {
			object := item.(client.Object)
			for _, finalizer := range object.GetFinalizers() {
				if finalizer != "kubernetes.io/pvc-protection" {
					t.Fatalf("dependent retains a non-Kubernetes finalizer: %s %s", object.GetName(), finalizer)
				}
			}
			if len(object.GetFinalizers()) != 0 {
				before := object.DeepCopyObject().(client.Object)
				object.SetFinalizers(nil)
				if err := h.admin.Patch(h.ctx, object, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
					return err
				}
			}
			return h.admin.Delete(h.ctx, object, client.Preconditions{UID: ptr.To(object.GetUID()), ResourceVersion: ptr.To(object.GetResourceVersion())})
		}); err != nil {
			t.Fatal(err)
		}
	}
	namespace.Spec.Finalizers = nil
	if err := h.admin.SubResource("finalize").Update(h.ctx, namespace); err != nil {
		t.Fatal(err)
	}
}
