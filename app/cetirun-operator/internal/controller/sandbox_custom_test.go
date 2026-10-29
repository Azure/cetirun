// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"reflect"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/internal/chataccess"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func withCommonCustom(t *testing.T, mode string) func(*AgentReconciler, client.Client) {
	t.Helper()
	return func(r *AgentReconciler, kube client.Client) {
		ctx := context.Background()
		agent := &api.Agent{}
		if err := kube.Get(ctx, client.ObjectKey{Name: "example"}, agent); err != nil {
			t.Fatal(err)
		}
		agent.Spec.Agent.Type = api.AgentTypeCustom
		agent.Spec.Agent.Image = ptr.To("registry.example.com/custom-agent@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		agent.Spec.Agent.Port = ptr.To[int32](8080)
		agent.Spec.GatewayTokenSecretRef = ""
		if mode == "headless" {
			agent.Spec.Agent.Port = nil
		}
		agent.Spec.Agent.WebUI = mode == "web-ui"
		if err := kube.Update(ctx, agent); err != nil {
			t.Fatal(err)
		}
		policy := &api.AgentPolicy{}
		if err := kube.Get(ctx, client.ObjectKey{Name: "policy-example"}, policy); err != nil {
			t.Fatal(err)
		}
		policy.Spec.Harnesses.Allowed = []api.AgentType{api.AgentTypeCustom}
		if err := kube.Update(ctx, policy); err != nil {
			t.Fatal(err)
		}
		r.CustomBindings = NewKubernetesCustomBindings(r, CustomRuntimeConfig{
			Enabled: true, ApprovedImages: []string{*agent.Spec.Agent.Image}, GatewayImages: []string{*agent.Spec.Agent.Image},
			StorageClass: "unused-legacy-storage", StorageSize: "1Gi", AccessMode: "ReadWriteOnce", FSGroup: 1024,
			Context: map[string]string{"AGENTS.md": "unused legacy context", "skills/cetirun-memory/SKILL.md": "synthetic"},
		})
		r.CetiRunHelmValues = map[string]any{"ttyd": map[string]any{
			"enabled": false, "image": map[string]any{
				"repository": "registry.example.com/cetirun-terminal",
				"digest":     "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			},
		}}
		r.ChatAccess = chataccess.Config{Enabled: true, Callers: []chataccess.Caller{{
			Namespace: "portal-system", PodLabels: map[string]string{"app": "portal"},
		}}}
	}
}

func TestCommonCustomCreatesReadyWithoutLegacyBindings(t *testing.T) {
	for _, mode := range []string{"headless", "chat", "web-ui"} {
		for _, container := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/kata", true: "/container"}[container], func(t *testing.T) {
				options := []func(*AgentReconciler, client.Client){withCommonCustom(t, mode)}
				if container {
					options = append(options, withContainerRuntime(t))
				}

				f, _ := newUnreconciledSandboxRuntimeFixture(t, true, nil, true, "custom", true, false, options...)
				for range 5 {
					f.reconcileOK(t)
				}
				workload := f.workload(t)
				if len(workload.Spec.VolumeClaimTemplates) != 0 || !ptr.Deref(workload.Spec.Template.Spec.Containers[0].SecurityContext.RunAsNonRoot, false) {
					t.Fatal("Custom bypassed common retained storage or non-root constraints")
				}
				workload.Status.ObservedGeneration = workload.Generation
				workload.Status.Replicas, workload.Status.ReadyReplicas, workload.Status.UpdatedReplicas = 1, 1, 1
				if err := f.kube.Status().Update(context.Background(), workload); err != nil {
					t.Fatal(err)
				}
				f.reconcileOK(t)
				f.reconcileOK(t)
				current := f.current(t)
				if current.Status.Phase != api.PhaseReady || !isConditionTrue(current.Status.Conditions, api.ConditionPolicyApplied) {
					t.Fatalf("Custom did not reach current Ready: %+v", current.Status)
				}
				expected := reconciledExpectationForTest(t, f)
				if expected.Home.Name != f.r.SandboxPreparation.bootstrap.HomeClaimName {
					t.Fatal("Custom used its unrelated legacy HOME configuration")
				}
				stages, key := f.installer.stages, current.Status.AIGateway.KeyID
				before := workload.Spec.DeepCopy()
				f.reconcileOK(t)
				if f.installer.stages != stages || f.current(t).Status.AIGateway.KeyID != key ||
					!reflect.DeepEqual(f.workload(t).Spec, *before) {
					t.Fatal("unchanged Custom reconciliation replaced workload or accounting identity")
				}
			})
		}
	}
}

func TestCommonCustomRetainedLifecycle(t *testing.T) {
	for _, backend := range []string{"kata", "container"} {
		t.Run(backend, func(t *testing.T) {
			setup := []func(*AgentReconciler, client.Client){withCommonCustom(t, "headless")}
			if backend == "container" {
				setup = append(setup, withContainerRuntime(t))
			}
			t.Run("restart", func(t *testing.T) {
				testReconciledSandboxRetainedRestartForHarnesses(t, []string{"custom"}, setup...)
			})
			t.Run("suspend-resume", func(t *testing.T) {
				testReconciledSandboxSuspensionForHarnesses(t, []string{"custom"}, setup...)
			})
			t.Run("migration", func(t *testing.T) {
				testPlannedMigrationRetainsHOMEForHarnesses(t, []string{"custom"}, setup...)
			})
			t.Run("deletion", func(t *testing.T) {
				testReconciledSandboxDeletionForHarnesses(t, []string{"custom"}, setup...)
			})
		})
	}
}

func TestCommonCustomRejectsUnqualifiedInputsWithoutFallback(t *testing.T) {
	for name, change := range map[string]func(*AgentReconciler, client.Client){
		"no approval provider": func(r *AgentReconciler, _ client.Client) { r.CustomBindings = nil },
		"legacy-only executor": func(r *AgentReconciler, _ client.Client) { r.SandboxPreparation.NativeExecution = false },
		"image-owned model mode": func(r *AgentReconciler, _ client.Client) {
			r.CustomBindings = NewKubernetesCustomBindings(r, CustomRuntimeConfig{
				Enabled: true, AgentManagedModel: true,
				ApprovedImages: []string{"registry.example.com/custom-agent@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			})
		},
		"desktop": func(_ *AgentReconciler, kube client.Client) {
			agent := &api.Agent{}
			if err := kube.Get(context.Background(), client.ObjectKey{Name: "example"}, agent); err != nil {
				t.Fatal(err)
			}
			agent.Spec.Helm.Values = map[string]runtime.RawExtension{"desktop": {Raw: []byte(`{"enabled":true}`)}}
			if err := kube.Update(context.Background(), agent); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, _ := newUnreconciledSandboxRuntimeFixture(t, true, nil, true, "custom", true, false,
				withCommonCustom(t, "chat"), change)
			for range 3 {
				_, _ = f.reconcile()
			}
			f.assertNotReady(t)
			var workloads appsv1.StatefulSetList
			if err := f.kube.List(context.Background(), &workloads); err != nil || len(workloads.Items) != 0 {
				t.Fatal("unqualified Custom inputs created compute:", err)
			}
			if f.installer.stages != 0 {
				t.Fatal("unqualified Custom inputs fell back to another executor")
			}
		})
	}
}
