// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"google.golang.org/protobuf/proto"
	"helm.sh/helm/v3/pkg/chart/loader"
	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type oldNativeUpgradeState struct {
	SourceCommit  string                    `json:"sourceCommit"`
	Description   string                    `json:"description"`
	ObjectsSHA256 string                    `json:"objectsSHA256"`
	Revision      string                    `json:"revision"`
	StateName     string                    `json:"stateName"`
	ReceiptName   string                    `json:"receiptName"`
	StageDigest   string                    `json:"stageDigest"`
	GatewayKeys   map[string]*aigateway.Key `json:"gatewayKeys"`
	Objects       []json.RawMessage         `json:"objects"`
}

func oldNativeUpgradeFixture(t *testing.T) (*nativeSandboxFixture, oldNativeUpgradeState) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "native-upgrade", "pre-proxy.json"))
	if err != nil {
		t.Fatal(err)
	}
	entropy := bytes.Repeat([]byte{0x42}, 32)
	raw = []byte(strings.NewReplacer(
		"@SYNTHETIC_GATEWAY_KEY_DATA@", base64.StdEncoding.EncodeToString([]byte("sk-"+base64.RawURLEncoding.EncodeToString(entropy))),
		"@SYNTHETIC_RUNTIME_KEY_DATA@", base64.StdEncoding.EncodeToString([]byte(base64.StdEncoding.EncodeToString(entropy))),
		"@EMPTY_RENDER_CHECKSUM@", fmt.Sprintf("%x", sha256.Sum256([]byte("\n"))),
	).Replace(string(raw)))
	var state oldNativeUpgradeState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	objectsJSON, err := json.Marshal(state.Objects)
	if err != nil {
		t.Fatal(err)
	}
	if state.SourceCommit != "3a9e4de5423a64f40a945c1d2941cba3d691cdbe" ||
		fmt.Sprintf("%x", sha256.Sum256(objectsJSON)) != state.ObjectsSHA256 {
		t.Fatal("old controller fixture provenance or hydrated object bytes changed")
	}
	r, _, gateway := aiGatewayTestReconciler(t)
	cilium := schema.GroupVersion{Group: "cilium.io", Version: "v2"}
	r.Scheme.AddKnownTypeWithName(cilium.WithKind("CiliumNetworkPolicy"), &unstructured.Unstructured{})
	r.Scheme.AddKnownTypeWithName(cilium.WithKind("CiliumNetworkPolicyList"), &unstructured.UnstructuredList{})
	decoder := serializer.NewCodecFactory(r.Scheme).UniversalDeserializer()
	objects := make([]runtime.Object, 0, len(state.Objects))
	var agent *api.Agent
	for _, raw := range state.Objects {
		object, _, err := decoder.Decode(raw, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if value, ok := object.(*api.Agent); ok {
			agent = value
		}
		objects = append(objects, object)
	}
	if agent == nil || agent.Status.Phase != api.PhaseReady || agent.Status.Sandbox == nil ||
		agent.Status.Sandbox.AppliedRevision != state.Revision {
		t.Fatal("fixture is not an old running native Agent")
	}
	kube := fake.NewClientBuilder().WithScheme(r.Scheme).WithRuntimeObjects(objects...).
		WithStatusSubresource(&api.Agent{}, &api.Team{}, &api.AgentPolicy{}).Build()
	r.Client, r.APIReader = kube, kube
	r.OperatorSAName, r.ChartRegistry, r.ChartVersion = "cetirun-operator", "registry.example.com", agent.Spec.Helm.Version
	r.DefaultModel, r.IstioGateway = testSharedModelOne, "platform/ingress"
	gateway.keys = state.GatewayKeys
	chrt, err := loader.Load(filepath.Join("..", "..", "..", "cetirun-chart"))
	if err != nil {
		t.Fatal(err)
	}
	installer := &nativeSandboxInstaller{kube: kube, chart: chrt, digest: state.StageDigest}
	config, err := ParseSandboxSourcesConfig(nativeSandboxSourcesJSON)
	if err != nil {
		t.Fatal(err)
	}
	config.NativeExecution = true
	r.SandboxPreparation, err = NewKubernetesSandboxPreparation(config, kube, installer, r.OperatorNamespace, r.ChartVersion)
	if err != nil {
		t.Fatal(err)
	}
	return &nativeSandboxFixture{r: r, kube: kube, gateway: gateway, installer: installer, agent: agent}, state
}

func TestNativeUpgradePreservesExistingRuntimeAndReceipt(t *testing.T) {
	f, legacy := oldNativeUpgradeFixture(t)
	ctx := context.Background()
	beforeAgent, beforePod, beforeWorkload := f.current(t), f.pod(t), f.workload(t)
	beforeState, expected := f.expectation(t)
	beforeReceipt, _, err := f.r.readSandboxPreparationReceipt(ctx, beforeAgent)
	if err != nil {
		t.Fatal(err)
	}
	if beforeState.Name != legacy.StateName || beforeReceipt.Name != legacy.ReceiptName ||
		beforeReceipt.Annotations[nativeStartAnnotation] != legacy.Revision ||
		expected.Revision != legacy.Revision || expected.ExecutionHeld || expected.PodUID != beforePod.UID {
		t.Fatal("old execution receipt or one-shot boundary is incomplete")
	}
	oldConfig := &corev1.ConfigMap{}
	if err := f.kube.Get(ctx, client.ObjectKey{Namespace: beforeWorkload.Namespace, Name: beforeWorkload.Name + "-config"}, oldConfig); err != nil {
		t.Fatal(err)
	}
	var runtimeConfig struct {
		Gateway struct {
			TrustedProxies []string `json:"trustedProxies"`
		} `json:"gateway"`
	}
	if err := json.Unmarshal([]byte(oldConfig.Data["openclaw.json"]), &runtimeConfig); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(runtimeConfig.Gateway.TrustedProxies, []string{"127.0.0.6"}) {
		t.Fatal("upgrade regression did not retain the old runtime configuration")
	}
	route := &istiov1beta1.VirtualService{}
	routeKey := client.ObjectKey{Namespace: beforeWorkload.Namespace, Name: virtualServiceName(beforeAgent.Name)}
	if err := f.kube.Get(ctx, routeKey, route); err != nil {
		t.Fatal(err)
	}
	beforeRoute := route.DeepCopy()
	for range 2 {
		f.reconcileOK(t)
		f.assertReady(t)
		agent := f.current(t)
		if agent.UID != beforeAgent.UID || !reflect.DeepEqual(agent.Spec, beforeAgent.Spec) ||
			slices.Contains(agent.Finalizers, sandbox.HoldFinalizer) ||
			agent.Status.Sandbox.AppliedRevision != legacy.Revision ||
			agent.Status.Policy.Enforcement.Sandbox.DesiredRevision != legacy.Revision ||
			agent.Status.AccessURL != beforeAgent.Status.AccessURL {
			t.Fatal("operator upgrade changed intent, held the lifetime or changed its effective revision")
		}
		if !reflect.DeepEqual(f.workload(t), beforeWorkload) || !reflect.DeepEqual(f.pod(t), beforePod) {
			t.Fatal("operator upgrade changed or replaced the existing runtime")
		}
		state, _ := f.expectation(t)
		receipt, _, err := f.r.readSandboxPreparationReceipt(ctx, agent)
		if err != nil || !reflect.DeepEqual(state, beforeState) || !reflect.DeepEqual(receipt, beforeReceipt) {
			t.Fatalf("operator upgrade rewrote the old expectation or single-start receipt: %v", err)
		}
		currentConfig := &corev1.ConfigMap{}
		if err := f.kube.Get(ctx, client.ObjectKeyFromObject(oldConfig), currentConfig); err != nil || !reflect.DeepEqual(currentConfig, oldConfig) {
			t.Fatalf("operator upgrade silently migrated runtime configuration: %v", err)
		}
		if err := f.kube.Get(ctx, routeKey, route); err != nil ||
			!reflect.DeepEqual(route.ObjectMeta, beforeRoute.ObjectMeta) || !proto.Equal(&route.Spec, &beforeRoute.Spec) {
			t.Fatalf("operator upgrade withdrew or changed the existing route: %v", err)
		}
	}
	if f.installer.stages != 0 || len(f.gateway.generated) != 0 || len(f.gateway.deletedTokens) != 0 || len(f.gateway.deletedAliases) != 0 {
		t.Fatal("operator upgrade restaged the workload or replaced/revoked its Gateway grant")
	}
	for _, key := range f.gateway.keys {
		if key.Blocked {
			t.Fatal("operator upgrade blocked the existing Gateway grant")
		}
	}
}

func TestNativeUpgradeStillHoldsChangedInputs(t *testing.T) {
	for _, change := range []string{"ingress peer", "runtime config"} {
		t.Run(change, func(t *testing.T) {
			f, legacy := oldNativeUpgradeFixture(t)
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
			} else {
				workload := f.workload(t)
				config := &corev1.ConfigMap{}
				if err := f.kube.Get(ctx, client.ObjectKey{Namespace: workload.Namespace, Name: workload.Name + "-config"}, config); err != nil {
					t.Fatal(err)
				}
				config.Data["unexpected-change"] = "synthetic"
				if err := f.kube.Update(ctx, config); err != nil {
					t.Fatal(err)
				}
			}
			_, _ = f.reconcile()
			f.assertNotReady(t)
			if ptr.Deref(f.workload(t).Spec.Replicas, -1) != 0 || !slices.Contains(f.current(t).Finalizers, sandbox.HoldFinalizer) {
				t.Fatal("upgrade compatibility retained execution after actual input drift")
			}
			route := &istiov1beta1.VirtualService{}
			if err := f.kube.Get(ctx, client.ObjectKey{Namespace: instanceNamespace(f.agent.Name), Name: virtualServiceName(f.agent.Name)}, route); !apierrors.IsNotFound(err) {
				t.Fatalf("changed inputs retained native access: %v", err)
			}
			receipt, _, err := f.r.readSandboxPreparationReceipt(ctx, f.current(t))
			if err != nil || receipt.Annotations[nativeStartAnnotation] != legacy.Revision {
				t.Fatalf("hold lost the original single-start marker: %v", err)
			}
			if _, err := f.r.startKubernetesSandbox(ctx, f.current(t)); err == nil || f.installer.stages != 0 {
				t.Fatal("changed legacy lifetime automatically replayed")
			}
		})
	}
}
