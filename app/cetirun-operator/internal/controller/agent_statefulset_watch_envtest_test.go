// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package controller

import (
	"context"
	"os"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	istio "istio.io/client-go/pkg/apis/networking/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	controllerconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func TestAgentStatefulSetStatusWatchAPIServer(t *testing.T) {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		t.Fatal("KUBEBUILDER_ASSETS must name local envtest binaries")
	}
	environment := &envtest.Environment{
		BinaryAssetsDirectory: assets, UseExistingCluster: ptr.To(false),
		CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true,
		CRDs: []*apiextensionsv1.CustomResourceDefinition{
			nativeStartExternalCRD("networking.istio.io", "v1beta1", "VirtualService", "virtualservices"),
			nativeStartExternalCRD("networking.istio.io", "v1beta1", "DestinationRule", "destinationrules"),
		},
	}
	config, err := environment.Start()
	if err != nil {
		_ = environment.Stop()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{api.AddToScheme, appsv1.AddToScheme, corev1.AddToScheme, istio.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	mgr, err := ctrl.NewManager(config, ctrl.Options{Scheme: scheme,
		Controller: controllerconfig.Controller{SkipNameValidation: ptr.To(true)},
		Metrics:    metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0"})
	if err != nil {
		t.Fatal(err)
	}
	writer, err := client.NewWithWatch(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan client.ObjectKey, 10)
	r := &AgentReconciler{Scheme: scheme, Client: interceptor.NewClient(writer, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
			if _, ok := object.(*api.Agent); ok {
				requests <- key
				// No Agent exists: there can be no Agent event or timed retry.
				return apierrors.NewNotFound(schema.GroupResource{Group: api.GroupVersion.Group, Resource: "agents"}, key.Name)
			}
			return c.Get(ctx, key, object, options...)
		},
	})}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("controller manager did not stop")
		}
	})
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		t.Fatal("manager cache did not sync")
	}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "workload-watch"}}
	if err := writer.Create(ctx, namespace); err != nil {
		t.Fatal(err)
	}
	workload := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "main", Namespace: namespace.Name,
			OwnerReferences: []metav1.OwnerReference{w.AgentOwner("example", "synthetic-agent-uid")}},
		Spec: appsv1.StatefulSetSpec{Replicas: ptr.To[int32](0), ServiceName: "main",
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "example"}},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "example"}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "agent", Image: "registry.example.com/agent:test"}}}},
		},
	}
	waitForRequest := func() {
		t.Helper()
		select {
		case key := <-requests:
			if key != (client.ObjectKey{Name: "example"}) {
				t.Fatalf("StatefulSet enqueued the wrong cluster-scoped Agent: %v", key)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("StatefulSet event did not enqueue its Agent")
		}
	}
	if err := writer.Create(ctx, workload); err != nil {
		t.Fatal(err)
	}
	waitForRequest()
	workload.Status.ObservedGeneration = workload.Generation
	if err := writer.Status().Update(ctx, workload); err != nil {
		t.Fatal(err)
	}
	waitForRequest()
}
