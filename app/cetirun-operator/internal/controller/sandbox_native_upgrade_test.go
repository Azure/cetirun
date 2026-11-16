// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	helmfixture "github.com/Azure/CetiRun/app/cetirun-operator/internal/helm/testfixture"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxbinding"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestNativeUpgradePreparesExistingAgentWithPinnedPreProxyChart(t *testing.T) {
	f := newNativeSandboxFixture(t, true)
	if err := helmfixture.RestorePreProxyContract(f.installer.chart); err != nil {
		t.Fatal(err)
	}
	original := f.current(t)
	if original.Spec.Helm.Version == "" || original.Annotations[sandboxbinding.AuthorizationAnnotation] == "" || f.installer.stages != 0 {
		t.Fatal("fixture must be an authorized, pinned Agent awaiting preparation")
	}
	receipt := &corev1.ConfigMap{}
	receiptKey := client.ObjectKey{Namespace: f.r.OperatorNamespace, Name: w.PreparationReceiptName(original.UID)}
	if err := f.kube.Get(context.Background(), receiptKey, receipt); !apierrors.IsNotFound(err) {
		t.Fatal("pending fixture unexpectedly has reusable preparation:", err)
	}
	f.r.ChartVersion = "999.0.0"
	f.prepare(t)
	current := f.current(t)
	if !reflect.DeepEqual(current.Spec, original.Spec) || current.UID != original.UID ||
		current.Annotations[sandboxbinding.AuthorizationAnnotation] != original.Annotations[sandboxbinding.AuthorizationAnnotation] ||
		f.installer.prepared.ChartVersion != original.Spec.Helm.Version {
		t.Fatal("upgrade changed the pending Agent's pin, intent or authorization")
	}
	if err := f.kube.Get(context.Background(), receiptKey, receipt); err != nil {
		t.Fatal(err)
	}
	recorded, err := w.DecodePreparationReceipt(receipt, w.ObjectIdentity{Name: original.Name, UID: original.UID})
	if err != nil || recorded.Prerequisites.ChartVersion != original.Spec.Helm.Version {
		t.Fatal("preparation did not retain the old chart pin:", err)
	}
	_, expected := f.expectation(t)
	if !expected.ExecutionHeld || expected.SinglePod || f.installer.stages != 1 {
		t.Fatal("old pinned chart did not complete guarded held preparation")
	}
	// The pinned chart predates the Gateway provider cetirun.
	f.assertGatewayProjection(t, expected, "openai")
	f.ready(t)
	if f.installer.stages != 1 {
		t.Fatal("completed pinned preparation was not reusable")
	}
}

func TestNativePinnedChartHoldsChangedInputs(t *testing.T) {
	// Old resource names are not migrated. Exercise the single-start boundary
	// with the pinned chart contract and the current resource naming instead.
	for _, change := range []string{"ingress peer", "runtime config", "gateway revision"} {
		t.Run(change, func(t *testing.T) {
			f := newNativeSandboxFixture(t, true)
			if err := helmfixture.RestorePreProxyContract(f.installer.chart); err != nil {
				t.Fatal(err)
			}
			f.ready(t)
			_, original := f.expectation(t)
			stages := f.installer.stages
			ctx := context.Background()
			if change == "ingress peer" {
				platform := &corev1.ConfigMap{}
				if err := f.kube.Get(ctx, client.ObjectKey{Namespace: f.r.OperatorNamespace, Name: "sandbox-platform"}, platform); err != nil {
					t.Fatal(err)
				}
				var document SandboxPlatformConfiguration
				if err := json.Unmarshal([]byte(platform.Data[SandboxPlatformConfigurationKey]), &document); err != nil {
					t.Fatal(err)
				}
				network := document.Platforms[api.SandboxKata]
				network.Topology.Ingress[0].Peer.IPs = []string{"10.96.1.61"}
				document.Platforms[api.SandboxKata] = network
				raw, err := json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
				platform.Data[SandboxPlatformConfigurationKey] = string(raw)
				if err := f.kube.Update(ctx, platform); err != nil {
					t.Fatal(err)
				}
			} else if change == "runtime config" {
				workload := f.workload(t)
				config := &corev1.ConfigMap{}
				if err := f.kube.Get(ctx, client.ObjectKey{Namespace: workload.Namespace, Name: workload.Name + "-config"}, config); err != nil {
					t.Fatal(err)
				}
				config.Data["unexpected-change"] = "synthetic"
				if err := f.kube.Update(ctx, config); err != nil {
					t.Fatal(err)
				}
			} else {
				agent := f.current(t)
				agent.Status.Policy.Enforcement.Gateway.DesiredRevision = "earlier-gateway-revision"
				agent.Status.AIGateway.PolicyRevision = "earlier-gateway-revision"
				if err := f.kube.Status().Update(ctx, agent); err != nil {
					t.Fatal(err)
				}
			}
			_, _ = f.reconcile()
			f.assertNotReady(t)
			if ptr.Deref(f.workload(t).Spec.Replicas, -1) != 0 || !slices.Contains(f.current(t).Finalizers, sandbox.HoldFinalizer) {
				t.Fatal("pinned chart retained execution after actual input drift")
			}
			route := &istiov1beta1.VirtualService{}
			if err := f.kube.Get(ctx, client.ObjectKey{Namespace: instanceNamespace(f.agent.Name), Name: virtualServiceName(f.agent.Name)}, route); !apierrors.IsNotFound(err) {
				t.Fatalf("changed inputs retained native access: %v", err)
			}
			receipt, _, err := f.r.readSandboxPreparationReceipt(ctx, f.current(t))
			if err != nil || receipt.Annotations[nativeStartAnnotation] != original.Revision {
				t.Fatalf("hold lost the original single-start marker: %v", err)
			}
			if _, err := f.r.startKubernetesSandbox(ctx, f.current(t)); err == nil || f.installer.stages != stages {
				t.Fatal("changed pinned lifetime automatically replayed")
			}
		})
	}
}
