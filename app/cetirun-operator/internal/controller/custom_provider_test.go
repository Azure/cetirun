// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/runtimebindings"
	"helm.sh/helm/v3/pkg/chart/loader"
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
	cfg := CustomRuntimeConfig{Enabled: true, ApprovedImages: []string{image}}
	data, _ := json.Marshal(cfg)
	parsed, err := ParseCustomRuntimeConfig(string(data))
	if err != nil {
		t.Fatal(err)
	}
	// Legacy-provider tests still exercise the durable ownership code. The
	// runtime configuration no longer accepts these fields: the Sandbox owns
	// storage and context in production.
	parsed.StorageClass, parsed.StorageSize, parsed.AccessMode, parsed.FSGroup = "test-storage", "20Gi", "ReadWriteMany", 1024
	parsed.Context = map[string]string{"AGENTS.md": "approved context", "skills/cetirun-memory/SKILL.md": "approved memory"}
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
		t.Fatal("namespace ownership was not recorded first")
	}
	if err := p.reconciler.Get(ctx, client.ObjectKeyFromObject(instance), instance); err != nil {
		t.Fatal(err)
	}
	b, err := p.Resolve(ctx, instance, nil)
	if err != nil {
		t.Fatal(err)
	}
	simulateChartHome(t, p, instance, b, true)
	b, err = p.Resolve(ctx, instance, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.AgentManagedModel || b.Model.Name != "" || b.Network.GatewayPort != 0 {
		t.Fatal("Custom provider did not require the platform Gateway path")
	}
	return p, instance
}

func simulateChartHome(t *testing.T, p *KubernetesCustomBindings, instance *api.Agent, b runtimebindings.Custom, bound bool) *corev1.PersistentVolumeClaim {
	t.Helper()
	// The fake client has no StatefulSet or CSI controller; simulate their output.
	home := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: b.Home.Name, Namespace: b.Namespace.Name,
		Annotations: map[string]string{customAgentUID: string(instance.UID)}}, Spec: b.Home.Claim.PVCSpec()}
	if bound {
		home.Spec.VolumeName = "synthetic-bound-volume"
	}
	if err := p.reconciler.Create(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	home.Status.Phase = corev1.ClaimPending
	if bound {
		home.Status.Phase = corev1.ClaimBound
	}
	if err := p.reconciler.Status().Update(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestKubernetesCustomProvisioning(t *testing.T) {
	p, instance := readyProvider(t)
	ctx := context.Background()
	first, err := p.Resolve(ctx, instance, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Resolve(ctx, instance, nil)
	if err != nil || first.Context.ObjectReference != second.Context.ObjectReference || !reflect.DeepEqual(first.Home, second.Home) {
		t.Fatal("unchanged provisioning replaced HOME or context")
	}
}

func pendingHomeProvider(t *testing.T) (*KubernetesCustomBindings, *api.Agent) {
	t.Helper()
	p, instance := providerFixture(t)
	chart, err := loader.Load("../../chart")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(chart.Values["customRuntime"])
	if err != nil {
		t.Fatal(err)
	}
	var defaults CustomRuntimeConfig
	if err := json.Unmarshal(raw, &defaults); err != nil {
		t.Fatal(err)
	}
	if defaults.Enabled || defaults.StorageClass != "default" || defaults.StorageSize != "100Gi" || defaults.AccessMode != "ReadWriteOnce" {
		t.Fatalf("unexpected Custom HOME defaults: %+v", defaults)
	}
	defaults.Enabled, defaults.ApprovedImages = true, p.config.ApprovedImages
	p = NewKubernetesCustomBindings(p.reconciler, defaults)
	ctx := context.Background()
	if _, err := p.Resolve(ctx, instance, nil); err == nil || !strings.Contains(err.Error(), "ownership recorded") {
		t.Fatalf("first resolve must persist ownership before approving HOME: %v", err)
	}
	if err := p.reconciler.Get(ctx, client.ObjectKeyFromObject(instance), instance); err != nil {
		t.Fatal(err)
	}
	return p, instance
}

func TestKubernetesCustomRejectsDifferentStorageProfile(t *testing.T) {
	p, instance := readyProvider(t)
	ctx := context.Background()
	before, err := p.Resolve(ctx, instance, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := p.config
	cfg.StorageClass, cfg.StorageSize, cfg.AccessMode = "default", "100Gi", "ReadWriteOnce"
	updated := NewKubernetesCustomBindings(p.reconciler, cfg)
	if _, err := updated.Resolve(ctx, instance, nil); err == nil {
		t.Fatal("HOME with a different storage profile was accepted")
	}
	home := &corev1.PersistentVolumeClaim{}
	if err := p.reconciler.Get(ctx, types.NamespacedName{Namespace: before.Namespace.Name, Name: before.Home.Name}, home); err != nil {
		t.Fatal(err)
	}
	if !before.Home.Claim.Matches(home) {
		t.Fatal("rejected storage profile modified HOME")
	}
	if err := p.reconciler.Delete(ctx, home); err != nil {
		t.Fatal(err)
	}
	if _, err := updated.Resolve(ctx, instance, nil); err == nil {
		t.Fatal("missing HOME permitted an outdated storage profile")
	}
	if err := p.reconciler.Get(ctx, client.ObjectKeyFromObject(home), home); !apierrors.IsNotFound(err) {
		t.Fatal("rejected storage profile created a replacement PVC")
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

func TestKubernetesCustomLeavesMissingChartHomeToStatefulSet(t *testing.T) {
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
	if bindings, err := p.Resolve(ctx, instance, nil); err != nil || bindings.Home.Claim == nil {
		t.Fatalf("missing chart-owned HOME lost its claim template: %v", err)
	}
	claims := &corev1.PersistentVolumeClaimList{}
	_ = p.reconciler.List(ctx, claims)
	if len(claims.Items) != 0 {
		t.Fatal("recovery allocated a replacement claim")
	}
}

func TestKubernetesCustomRejectsOwnershipWithoutClaim(t *testing.T) {
	p, instance := providerFixture(t)
	receipt := customOwnership{AgentName: instance.Name, AgentUID: instance.UID,
		Namespace: instanceNamespace(instance.Name), NamespaceUID: "namespace-uid",
		Release: helmReleaseName(instance.Name), Home: "data-" + instanceServiceName(instance.Name) + "-0"}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	key := p.reconciler.ownershipKey(instance)
	record := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace,
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(instance, api.GroupVersion.WithKind("Agent"))}},
		Immutable: ptr.To(true), Data: map[string]string{"ownership": string(raw)}}
	if err := p.reconciler.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Resolve(context.Background(), instance, nil); err == nil {
		t.Fatal("ownership without a HOME claim template was accepted")
	}
}

func TestKubernetesCustomLeasePersistsAcrossRestart(t *testing.T) {
	p, instance := readyProvider(t)
	ctx := context.Background()
	b, _ := p.Resolve(ctx, instance, nil)
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: instanceServiceName(instance.Name), Namespace: b.Namespace.Name,
		Annotations:     map[string]string{customAgentUID: string(instance.UID)},
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

func TestKubernetesCustomImageUpgradeRetainsHome(t *testing.T) {
	for _, name := range []string{"unapproved", "approved"} {
		t.Run(name, func(t *testing.T) {
			p, instance := readyProvider(t)
			ctx := context.Background()
			bindings, err := p.Resolve(ctx, instance, nil)
			if err != nil {
				t.Fatal(err)
			}
			home := &corev1.PersistentVolumeClaim{}
			homeKey := client.ObjectKey{Namespace: bindings.Namespace.Name, Name: bindings.Home.Name}
			if err := p.reconciler.Get(ctx, homeKey, home); err != nil {
				t.Fatal(err)
			}
			originalHome := home.DeepCopy()
			oldImage := *instance.Spec.Agent.Image
			workload := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
				Name: instanceServiceName(instance.Name), Namespace: bindings.Namespace.Name,
				Annotations:     map[string]string{customAgentUID: string(instance.UID)},
				OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(instance, api.GroupVersion.WithKind("Agent"))},
			}, Spec: appsv1.StatefulSetSpec{Replicas: ptr.To[int32](1),
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "cetirun", Image: oldImage}},
				}},
			}}
			if err := p.reconciler.Create(ctx, workload); err != nil {
				t.Fatal(err)
			}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: workload.Name + "-0", Namespace: workload.Namespace},
				Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: home.Name},
				}}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
			if err := p.reconciler.Create(ctx, pod); err != nil {
				t.Fatal(err)
			}
			newImage := "registry.example.com/pi@sha256:" + strings.Repeat("b", 64)
			instance.Spec.Agent.Image, instance.Generation = ptr.To(newImage), instance.Generation+1
			if err := p.reconciler.Update(ctx, instance); err != nil {
				t.Fatal(err)
			}
			if name == "approved" {
				cfg := p.config
				cfg.ApprovedImages = []string{oldImage, newImage}
				p = NewKubernetesCustomBindings(p.reconciler, cfg)
			}
			operation := CustomApplyOperation{ID: "image-upgrade", PlanKey: "new-image:manifest", Generation: instance.Generation}
			lease, err := p.ApproveApply(ctx, instance, workload, operation)
			if lease != nil || err == nil {
				t.Fatal("image upgrade was approved before the old Pod exited")
			}
			if err := p.reconciler.Get(ctx, client.ObjectKeyFromObject(workload), workload); err != nil {
				t.Fatal(err)
			}
			if name == "unapproved" {
				if *workload.Spec.Replicas != 1 {
					t.Fatal("unapproved image stopped the running workload")
				}
				return
			}
			if *workload.Spec.Replicas != 0 || workload.Spec.Template.Spec.Containers[0].Image != oldImage {
				t.Fatal("approved upgrade did not stop the old workload before changing its image")
			}
			if err := p.reconciler.Delete(ctx, pod); err != nil {
				t.Fatal(err)
			}
			lease, err = p.ApproveApply(ctx, instance, workload, operation)
			if err != nil || lease == nil {
				t.Fatalf("approved image upgrade remained blocked after Pod exit: %v", err)
			}
			if err := lease.Check(ctx); err != nil {
				t.Fatal(err)
			}
			next, err := p.Resolve(ctx, instance, nil)
			if err != nil || next.ApprovedImage != newImage || !reflect.DeepEqual(next.Home, bindings.Home) {
				t.Fatalf("image upgrade changed the HOME binding: %v", err)
			}
			if err := p.reconciler.Get(ctx, homeKey, home); err != nil || !reflect.DeepEqual(home, originalHome) {
				t.Fatalf("image upgrade changed the retained PVC: %v", err)
			}
			if err := lease.Complete(ctx, CustomApplySucceeded); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCustomRuntimeConfigRequiresExplicitApproval(t *testing.T) {
	image := "registry.example.com/pi@sha256:" + strings.Repeat("a", 64)
	valid := map[string]any{"enabled": true, "approvedImages": []string{image}}
	data, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCustomRuntimeConfig(string(data)); err != nil {
		t.Fatalf("valid immutable image approval was rejected: %v", err)
	}
	for _, invalid := range []map[string]any{
		{"enabled": true},
		{"enabled": true, "approvedImages": []string{"registry.example.com/pi:latest"}},
		{"enabled": true, "approvedImages": []string{image}, "agentManagedModel": true},
		{"enabled": true, "approvedImages": []string{image}, "gatewayImages": []string{image}},
		{"enabled": true, "approvedImages": []string{image}, "storageClass": "legacy"},
	} {
		data, err := json.Marshal(invalid)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseCustomRuntimeConfig(string(data)); err == nil {
			t.Fatalf("unsupported Custom runtime configuration was accepted: %s", data)
		}
	}
}

func TestDisabledCustomRuntimeCannotAuthorizeRetainedImages(t *testing.T) {
	image := "registry.example.com/custom@sha256:" + strings.Repeat("a", 64)
	raw, _ := json.Marshal(CustomRuntimeConfig{Enabled: false, ApprovedImages: []string{image}})
	config, err := ParseCustomRuntimeConfig(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(config.ExecutionImages()) != 0 {
		t.Fatal("disabled runtime retained execution authority")
	}
	if _, err := ParseCustomRuntimeConfig(`{"enabled":false,"approvedImages":["registry.example.com/custom:latest"]}`); err == nil {
		t.Fatal("disabled configuration bypassed immutable image validation")
	}
	config.Enabled = true
	allowed := config.ExecutionImages()
	allowed[0] = "changed"
	if config.ApprovedImages[0] != image {
		t.Fatal("execution allowlist aliases configuration")
	}
}
