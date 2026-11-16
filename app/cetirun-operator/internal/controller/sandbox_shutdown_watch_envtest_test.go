// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package controller

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func TestSandboxStatefulSetShutdownWatchAPIServer(t *testing.T) {
	environment := &envtest.Environment{
		BinaryAssetsDirectory: os.Getenv("KUBEBUILDER_ASSETS"), UseExistingCluster: ptr.To(false),
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
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, api.AddToScheme, istiov1beta1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	mgr, err := ctrl.NewManager(config, ctrl.Options{Scheme: scheme,
		Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0"})
	if err != nil {
		t.Fatal(err)
	}
	writer, err := client.NewWithWatch(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := writer.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shutdown-watch"}}); err != nil {
		t.Fatal(err)
	}
	workload := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: "shutdown-watch",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: api.GroupVersion.String(), Kind: "Agent",
				Name: "shutdown-watch-agent", UID: "shutdown-watch-agent-uid", Controller: ptr.To(true)}}},
		Spec: appsv1.StatefulSetSpec{
			Replicas: ptr.To[int32](0), ServiceName: "workload",
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "shutdown-watch"}},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "shutdown-watch"}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "registry.example.com/probe:fixture"}}}},
		},
	}
	if err := writer.Create(ctx, workload); err != nil {
		t.Fatal(err)
	}
	var reconciles atomic.Int32
	r := &AgentReconciler{Scheme: scheme, OperatorNamespace: "shutdown-watch",
		Client: interceptor.NewClient(writer, interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
				if _, ok := object.(*api.Agent); ok && key.Name == "shutdown-watch-agent" {
					reconciles.Add(1)
					return apierrors.NewNotFound(api.GroupVersion.WithResource("agents").GroupResource(), key.Name)
				}
				return c.Get(ctx, key, object, options...)
			},
		}),
	}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("controller manager did not stop")
		}
	})
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		t.Fatal("controller cache did not synchronize")
	}
	waitFor := func(previous int32) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if reconciles.Load() > previous {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("StatefulSet event did not wake its Agent")
	}
	waitFor(0)
	previous := reconciles.Load()
	workload.Status.ObservedGeneration = workload.Generation
	if err := writer.Status().Update(ctx, workload); err != nil {
		t.Fatal(err)
	}
	// No Agent exists or requeues: only the workload's status event can wake it.
	waitFor(previous)
}
