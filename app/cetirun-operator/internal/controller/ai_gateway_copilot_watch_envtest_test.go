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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// The watch uses a real manager and API server; the fixture supplies the
// Agent and Gateway state without a second catalog-publishing goroutine.
type copilotWatchManager struct{ manager.Manager }

func (m copilotWatchManager) Add(r manager.Runnable) error {
	if _, ok := r.(*AIGatewayCatalogPublisher); ok {
		return nil
	}
	return m.Manager.Add(r)
}

func TestCopilotProxyWatchAPIServer(t *testing.T) {
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
	f, _ := newDeployedCopilotAgent(t, false)
	mgr, err := ctrl.NewManager(config, ctrl.Options{Scheme: f.r.Scheme,
		Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0"})
	if err != nil {
		t.Fatal(err)
	}
	kube := mgr.GetAPIReader()
	writer, err := client.New(config, client.Options{Scheme: f.r.Scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	proxy := readyProxy(0)
	proxy.Labels = map[string]string{"app.kubernetes.io/component": "copilot-proxy"}
	proxy.Spec = appsv1.DeploymentSpec{Replicas: ptr.To[int32](1),
		Selector: &metav1.LabelSelector{MatchLabels: proxy.Labels},
		Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: proxy.Labels},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "proxy", Image: testProxyImage}}}},
	}
	if err := writer.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: proxy.Namespace}}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Create(ctx, proxy); err != nil {
		t.Fatal(err)
	}
	proxyKey := client.ObjectKeyFromObject(proxy)
	f.r.Client = interceptor.NewClient(f.r.Client.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
			if _, ok := object.(*appsv1.Deployment); ok && key == proxyKey {
				return kube.Get(ctx, key, object, options...)
			}
			return c.Get(ctx, key, object, options...)
		},
	})
	f.r.APIReader = f.r.Client
	if err := f.r.SetupWithManager(copilotWatchManager{mgr}); err != nil {
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
		t.Fatal("manager cache did not sync")
	}
	waitForPhase := func(phase string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if f.agent(t).Status.AIGateway.Copilot.Phase == phase {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("Deployment watch did not reconcile Copilot to %s", phase)
	}
	// No Agent exists in the API server: only the unowned Deployment event
	// can enqueue it. Recovery must arrive before the 15-second retry.
	waitForPhase(api.AIGatewayCopilotPhaseStarting)
	if err := writer.Get(ctx, proxyKey, proxy); err != nil {
		t.Fatal(err)
	}
	proxy.Status.Replicas = 1
	proxy.Status.ReadyReplicas = 1
	if err := writer.Status().Update(ctx, proxy); err != nil {
		t.Fatal(err)
	}
	waitForPhase(api.AIGatewayCopilotPhaseReady)
	if err := writer.Delete(ctx, proxy); err != nil {
		t.Fatal(err)
	}
	waitForPhase(api.AIGatewayCopilotPhaseStarting)
}
