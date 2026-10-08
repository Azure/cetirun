// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package integration_test

import (
	"context"
	"encoding/json"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/controller"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxcoordination"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxplacement"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload/testfixture"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxwriter"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func (s *syntheticPreparationSource) ResolveRunning(ctx context.Context, agent *api.Agent, team *api.Team) (controller.SandboxPrerequisites, error) {
	return s.Resolve(ctx, agent, team)
}

// Only API defaulting, source pins and final-Pod admission are real here.
// Handoff/enrollment and platform approvals are synthetic; no scheduler,
// kubelet, container, runtime start gate or guest-external observer exists.
func TestSandboxRunningInputs(t *testing.T) {
	for _, typ := range []string{"openclaw", "hermes"} {
		t.Run(typ, func(t *testing.T) {
			h := newPreparationHarness(t, "running-placement")
			f := h.newCase(t, typ)
			f.reconcileConfiguration(t)
			f.reconcile(t)
			f.installer.beforeStage = nil
			before := f.assertPrepared(t)
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: typ + "-running-node"},
				Spec: corev1.NodeSpec{ProviderID: "example://" + typ + "-node"}}
			h.create(t, node)
			raw, err := json.Marshal(map[string]any{
				"version": sandboxplacement.RegistryVersion,
				"nodes": []any{map[string]any{
					"name": node.Name, "uid": node.UID, "providerID": node.Spec.ProviderID,
					"reservation": map[string]any{
						"agent": before.Agent, "namespace": before.Namespace, "statefulSet": before.Workload,
					},
				}},
				"platformDaemonSets": []any{},
			})
			if err != nil {
				t.Fatal(err)
			}
			registry := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
				Namespace: preparationStateNamespace, Name: "running-placement",
			}, Data: map[string]string{sandboxplacement.ConfigMapKey: string(raw)}}
			h.create(t, registry)
			placement, err := sandboxplacement.NewHandler(h.client, sandboxplacement.Config{
				Namespace: registry.Namespace, ConfigMapName: registry.Name,
			})
			if err != nil {
				t.Fatal(err)
			}
			f.reconciler.SandboxPreparation.Placement = placement
			createPreparationRevision(t, f)
			f.reconcile(t)
			expected := f.assertPrepared(t)
			target := sandboxcoordination.EnrolledTarget{
				ExecutionIdentity: sandbox.ExecutionIdentity{
					Binding: expected.Binding, Revision: expected.Revision,
					PodUID: "synthetic-enrollment", NodeUID: node.UID, EnforcementEpoch: "synthetic-epoch",
				},
				Writer: sandboxwriter.Writer{
					Workload: expected.Workload,
					Pod:      w.ObjectIdentity{Name: expected.Workload.Name + "-0", UID: "synthetic-enrollment"},
					Node:     expected.Placement.Nodes[0], MachineID: "synthetic-machine", Epoch: "synthetic-epoch",
					Storage: sandboxwriter.StorageIdentity{Volume: w.ObjectIdentity{Name: "example-volume", UID: "synthetic-volume"},
						Driver: "csi.example.com", Handle: "synthetic-handle"},
				},
				WriterJournal: w.ObjectIdentity{
					Name: sandboxwriter.JournalName(sandboxwriter.Lifetime{Agent: expected.Agent, Namespace: expected.Namespace, Home: expected.Home}),
					UID:  "synthetic-writer-journal",
				},
			}
			intent, err := f.reconciler.SandboxExecutionIntent(h.ctx, f.agent(t), target)
			if err != nil {
				t.Fatal(err)
			}
			f.assertAdmissionHeld(t, expected)
			if _, err := f.reconciler.SandboxRunningStatusInput(h.ctx, intent); err == nil {
				t.Fatal("held API state was accepted as running")
			}
			agent := f.agent(t)
			controllerutil.RemoveFinalizer(agent, sandbox.HoldFinalizer)
			h.update(t, agent)
			workload := &appsv1.StatefulSet{}
			h.get(t, client.ObjectKey{Namespace: intent.Namespace.Name, Name: intent.Workload.Name}, workload)
			workload.Spec.Replicas = ptr.To(int32(1))
			h.update(t, workload)
			agentSource, err := w.AgentSourceReference(agent)
			if err != nil {
				t.Fatal(err)
			}
			expected.ExecutionHeld = false
			for i := range expected.Sources {
				source := &expected.Sources[i]
				if source.IsAgent() {
					*source = agentSource
				}
				if source.Kind == "StatefulSet" {
					source.ResourceVersion = workload.ResourceVersion
				}
			}
			state := &corev1.ConfigMap{}
			h.get(t, client.ObjectKey{Namespace: intent.Preparation.Namespace, Name: intent.Preparation.Name}, state)
			raw, err = json.Marshal(expected)
			if err != nil {
				t.Fatal(err)
			}
			state.Data[w.PodExpectationKey] = string(raw)
			h.update(t, state)
			pod := testfixture.Pod(expected)
			pod.Spec.Overhead = nil
			h.create(t, pod)
			binding := &corev1.Binding{ObjectMeta: metav1.ObjectMeta{
				Name: pod.Name, Namespace: pod.Namespace, UID: pod.UID,
			}, Target: corev1.ObjectReference{Kind: "Node", Name: node.Name}}
			if err := h.kubernetes.CoreV1().Pods(pod.Namespace).Bind(h.ctx, binding, metav1.CreateOptions{}); err != nil {
				t.Fatal("synthetic scheduling of admitted Pod failed:", err)
			}
			h.get(t, client.ObjectKeyFromObject(pod), pod)
			// Test-only enrollment pins the API-issued Pod identity without
			// consulting any enforcement observation. This is not a start protocol.
			intent.Expected.PodUID, intent.Writer.Pod.UID = pod.UID, pod.UID
			input, err := f.reconciler.SandboxRunningStatusInput(h.ctx, intent)
			if err != nil {
				t.Fatal("actual defaulted API objects failed running input checks:", err)
			}
			if input.Observation != nil || input.Plan == nil || input.Plan.Binding != intent.Expected.Binding ||
				input.Plan.Revision != intent.Expected.Revision {
				t.Fatal("source checking invented evidence or lost the exact plan")
			}
			if err := f.reconciler.CheckSandboxExecutionSources(h.ctx, intent); err == nil {
				t.Fatal("held verification accepted running API objects")
			}
			bad := intent
			bad.Expected.PodUID, bad.Writer.Pod.UID = "replacement-pod", "replacement-pod"
			if _, err := f.reconciler.SandboxRunningStatusInput(h.ctx, bad); err == nil {
				t.Fatal("unrelated enrollment was inferred from the live Pod")
			}
			workload.Spec.Replicas = ptr.To(int32(0))
			h.update(t, workload)
			if _, err := f.reconciler.SandboxRunningStatusInput(h.ctx, intent); err == nil {
				t.Fatal("zero-replica intent retained running eligibility")
			}
			if current := f.agent(t); current.Status.AccessURL != "" ||
				current.Status.Sandbox != nil && current.Status.Sandbox.AppliedRevision != "" {
				t.Fatalf("read-only checks published readiness for %s", typ)
			}
		})
	}
}
