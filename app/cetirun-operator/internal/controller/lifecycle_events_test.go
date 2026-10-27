// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
)

var lifecycleEventReasons = []string{"InstanceCreating", "IngressConfigured", "InstanceReady", "InstanceSuspended"}

// lifecycleEvents drains the recorder and counts the lifecycle events by
// reason, with the message of the last IngressConfigured event.
func lifecycleEvents(recorder *record.FakeRecorder) (map[string]int, string) {
	counts := map[string]int{}
	ingress := ""
	for {
		select {
		case event := <-recorder.Events:
			for _, reason := range lifecycleEventReasons {
				if strings.Contains(event, " "+reason+" ") {
					counts[reason]++
					if reason == "IngressConfigured" {
						ingress = event
					}
				}
			}
		default:
			return counts, ingress
		}
	}
}

func TestEnsureVirtualServiceReportsWhatItChanged(t *testing.T) {
	agent := aiGatewayTestAgent("events-team")
	agent.Spec.SandboxRef = ""
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, cetirunv1alpha1.AddToScheme, istiov1beta1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent).Build()
	r := &AgentReconciler{Client: kube, Scheme: scheme, DomainSuffix: "example.com"}
	ctx := context.Background()
	key := types.NamespacedName{Namespace: instanceNamespace(agent.Name), Name: virtualServiceName(agent.Name)}

	if result, err := r.ensureVirtualService(ctx, agent); err != nil || result != controllerutil.OperationResultCreated {
		t.Fatalf("first ensure = %q, %v; want created", result, err)
	}
	created := &istiov1beta1.VirtualService{}
	if err := kube.Get(ctx, key, created); err != nil {
		t.Fatal(err)
	}
	if result, err := r.ensureVirtualService(ctx, agent); err != nil || result != controllerutil.OperationResultNone {
		t.Fatalf("repeated ensure = %q, %v; want unchanged", result, err)
	}
	unchanged := &istiov1beta1.VirtualService{}
	if err := kube.Get(ctx, key, unchanged); err != nil || unchanged.ResourceVersion != created.ResourceVersion {
		t.Fatalf("an unchanged route was written again: %v", err)
	}
	r.IstioGateway = "other-namespace/other-gateway"
	if result, err := r.ensureVirtualService(ctx, agent); err != nil || result != controllerutil.OperationResultUpdated {
		t.Fatalf("changed ensure = %q, %v; want updated", result, err)
	}
}

// preparationHelm renders the chart like chartHelm, but its Prepare can be
// made to fail, as it does when the chart cannot be downloaded.
type preparationHelm struct {
	chartHelm
	failPrepare bool
}

func (h *preparationHelm) Prepare(namespace string, values map[string]any, chartVersion string) (*helm.PreparedRelease, error) {
	if h.failPrepare {
		return nil, errors.New("synthetic chart download failure")
	}
	return h.chartHelm.Prepare(namespace, values, chartVersion)
}
