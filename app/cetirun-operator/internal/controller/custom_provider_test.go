// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type providerTestClient struct{ client.Client }

type namespaceLagClient struct{ client.Client }

func (c namespaceLagClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, options ...client.GetOption) error {
	if _, ok := obj.(*corev1.Namespace); ok {
		return apierrors.NewNotFound(corev1.Resource("namespaces"), key.Name)
	}
	return c.Client.Get(ctx, key, obj, options...)
}

func (c providerTestClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if obj.GetUID() == "" {
		obj.SetUID(types.UID("synthetic-" + obj.GetName()))
	}
	return c.Client.Create(ctx, obj, opts...)
}

func providerFixture(t *testing.T) (*KubernetesCustomBindings, *api.Agent) {
	t.Helper()
	image := "registry.example.com/pi@sha256:" + strings.Repeat("a", 64)
	cfg := CustomRuntimeConfig{Enabled: true, AgentManagedModel: true, ApprovedImages: []string{image},
		StorageClass: "test-storage", StorageSize: "20Gi", AccessMode: "ReadWriteMany", FSGroup: 1024,
		Context: map[string]string{"AGENTS.md": "approved context", "skills/cetirun-memory/SKILL.md": "approved memory"}}
	data, _ := json.Marshal(cfg)
	parsed, err := ParseCustomRuntimeConfig(string(data))
	if err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{api.AddToScheme, corev1.AddToScheme, appsv1.AddToScheme, rbacv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	instance := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "pi-test", UID: "pi-agent-uid", Generation: 1, Finalizers: []string{api.Finalizer}},
		Spec: api.AgentSpec{Agent: api.HarnessSpec{Type: api.AgentTypeCustom, Image: &image},
			User: api.UserSpec{Alias: "owner", DisplayName: "Test"}, Helm: api.HelmSpec{Version: "0.1.1"}}}
	cl := providerTestClient{fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&api.Agent{}, &corev1.PersistentVolumeClaim{}).WithObjects(instance).Build()}
	r := &AgentReconciler{Client: cl, APIReader: cl, Scheme: scheme, OperatorNamespace: "cetirun-system",
		OperatorSAName: "operator-test", IstioRevision: "asm-test"}
	return NewKubernetesCustomBindings(r, parsed), instance
}

func readyProvider(t *testing.T) (*KubernetesCustomBindings, *api.Agent) {
	t.Helper()
	p, instance := providerFixture(t)
	ctx := context.Background()
	if _, err := p.Resolve(ctx, instance, nil); err == nil {
		t.Fatal("unbound storage was approved")
	}
	if err := p.reconciler.Get(ctx, client.ObjectKeyFromObject(instance), instance); err != nil {
		t.Fatal(err)
	}
	pvc := &corev1.PersistentVolumeClaim{}
	key := types.NamespacedName{Namespace: instanceNamespace(instance.Name), Name: "data-" + instanceServiceName(instance.Name) + "-0"}
	if err := p.reconciler.Get(ctx, key, pvc); err != nil {
		t.Fatal(err)
	}
	pvc.Spec.VolumeName = "synthetic-bound-volume"
	if err := p.reconciler.Update(ctx, pvc); err != nil {
		t.Fatal(err)
	}
	pvc.Status.Phase = corev1.ClaimBound
	if err := p.reconciler.Status().Update(ctx, pvc); err != nil {
		t.Fatal(err)
	}
	b, err := p.Resolve(ctx, instance, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !b.AgentManagedModel || b.Model.Name != "" || b.Network.GatewayPort != 0 {
		t.Fatal("provider invented platform model access")
	}
	if err := p.reconciler.validateCustomObjects(ctx, instance, b); err != nil {
		t.Fatal(err)
	}
	secrets := &corev1.SecretList{}
	if err := p.reconciler.List(ctx, secrets); err != nil || len(secrets.Items) != 0 {
		t.Fatal("image-owned auth created a model Secret")
	}
	return p, instance
}

func TestKubernetesCustomProvisioning(t *testing.T) {
	p, instance := readyProvider(t)
	ctx := context.Background()
	first, err := p.Resolve(ctx, instance, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Resolve(ctx, instance, nil)
	if err != nil || first.Context.ObjectReference != second.Context.ObjectReference || first.Home != second.Home {
		t.Fatal("unchanged provisioning replaced HOME or context")
	}
}

func TestKubernetesCustomWaitsForObservedMeshLabels(t *testing.T) {
	p, instance := providerFixture(t)
	p.reconciler.Client = namespaceLagClient{Client: p.reconciler.Client}
	if _, err := p.Resolve(context.Background(), instance, nil); err == nil || !strings.Contains(err.Error(), "mesh preparation") {
		t.Fatalf("unobserved namespace mesh settings were accepted: %v", err)
	}
	claims := &corev1.PersistentVolumeClaimList{}
	if err := p.reconciler.List(context.Background(), claims); err != nil || len(claims.Items) != 0 {
		t.Fatal("provisioning advanced before namespace setup was observed")
	}
}

func TestKubernetesCustomRejectsUnapprovedAndForeignResources(t *testing.T) {
	p, instance := providerFixture(t)
	instance.Spec.Agent.Image = ptr.To("registry.example.com/other@sha256:" + strings.Repeat("b", 64))
	if _, err := p.Resolve(context.Background(), instance, nil); err == nil {
		t.Fatal("unapproved image accepted")
	}
	namespaces := &corev1.NamespaceList{}
	_ = p.reconciler.List(context.Background(), namespaces)
	if len(namespaces.Items) != 0 {
		t.Fatal("unapproved image created a namespace")
	}
	p, instance = providerFixture(t)
	foreign := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: instanceNamespace(instance.Name)}}
	if err := p.reconciler.Create(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Resolve(context.Background(), instance, nil); err == nil {
		t.Fatal("foreign namespace adopted")
	}
}

func TestKubernetesCustomDoesNotRecreateRetainedHome(t *testing.T) {
	p, instance := readyProvider(t)
	ctx := context.Background()
	receipt, err := p.reconciler.readCustomOwnership(ctx, instance)
	if err != nil {
		t.Fatal(err)
	}
	home := &corev1.PersistentVolumeClaim{}
	key := types.NamespacedName{Namespace: receipt.Namespace, Name: receipt.Home}
	if err := p.reconciler.Get(ctx, key, home); err != nil {
		t.Fatal(err)
	}
	if err := p.reconciler.Delete(ctx, home); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Resolve(ctx, instance, nil); err == nil {
		t.Fatal("missing retained HOME was recreated")
	}
	claims := &corev1.PersistentVolumeClaimList{}
	_ = p.reconciler.List(ctx, claims)
	if len(claims.Items) != 0 {
		t.Fatal("recovery allocated a replacement claim")
	}
}

func TestKubernetesCustomLeasePersistsAcrossRestart(t *testing.T) {
	p, instance := readyProvider(t)
	ctx := context.Background()
	b, _ := p.Resolve(ctx, instance, nil)
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: instanceServiceName(instance.Name), Namespace: b.Namespace.Name,
		Annotations:     map[string]string{customAgentUID: string(instance.UID), customHomeUID: b.Home.UID},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(instance, api.GroupVersion.WithKind("Agent"))}},
		Spec: appsv1.StatefulSetSpec{Replicas: ptr.To[int32](1), Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "cetirun", Image: *instance.Spec.Agent.Image}},
		}}}}
	if err := p.reconciler.Create(ctx, sts); err != nil {
		t.Fatal(err)
	}
	op := CustomApplyOperation{ID: "one", PlanKey: "plan:manifest", Generation: instance.Generation}
	lease, err := p.ApproveApply(ctx, instance, sts, op)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.reconciler.Get(ctx, client.ObjectKeyFromObject(sts), sts); err != nil || *sts.Spec.Replicas != 0 {
		t.Fatal("provider did not stop the old writer")
	}
	other := op
	other.ID = "two"
	if _, err := p.ApproveApply(ctx, instance, sts, other); err == nil {
		t.Fatal("concurrent operation acquired the same HOME")
	}
	restarted := NewKubernetesCustomBindings(p.reconciler, p.config)
	resumed, err := restarted.ResumeApply(ctx, instance, op)
	if err != nil {
		t.Fatal(err)
	}
	if err := resumed.Complete(ctx, CustomApplyUnknown); err == nil {
		t.Fatal("unknown apply result was acknowledged")
	}
	if err := resumed.Complete(ctx, CustomApplySucceeded); err != nil {
		t.Fatal(err)
	}
	if err := resumed.Complete(ctx, CustomApplySucceeded); err != nil {
		t.Fatal("completion was not idempotent")
	}
	if err := lease.Check(ctx); err == nil {
		t.Fatal("completed lease remained valid")
	}
}

func TestCustomRuntimeConfigRequiresExplicitApproval(t *testing.T) {
	p, _ := providerFixture(t)
	for _, edit := range []func(*CustomRuntimeConfig){
		func(c *CustomRuntimeConfig) { c.AgentManagedModel = false },
		func(c *CustomRuntimeConfig) { c.ApprovedImages = []string{"registry.example.com/pi:latest"} },
		func(c *CustomRuntimeConfig) { c.FSGroup = 0 },
		func(c *CustomRuntimeConfig) { c.Context["../private"] = "bad" },
	} {
		data, _ := json.Marshal(p.config)
		var cfg CustomRuntimeConfig
		_ = json.Unmarshal(data, &cfg)
		edit(&cfg)
		data, _ = json.Marshal(cfg)
		if _, err := ParseCustomRuntimeConfig(string(data)); err == nil {
			t.Fatal("invalid provisioning configuration accepted")
		}
	}
}
