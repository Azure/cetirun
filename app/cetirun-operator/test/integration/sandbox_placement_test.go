// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package integration_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxcoordination"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxplacement"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxwriter"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestSandboxPlacementPreparation(t *testing.T) {
	h := newPreparationHarness(t)
	for _, agentType := range []string{"openclaw", "hermes"} {
		t.Run(agentType, func(t *testing.T) {
			f := h.newCase(t, agentType)
			f.reconcileConfiguration(t)
			f.reconcile(t)
			f.installer.beforeStage = nil
			before := f.assertPrepared(t)
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: agentType + "-worker"},
				Spec:       corev1.NodeSpec{ProviderID: "example://" + agentType + "-worker"},
			}
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
			registry := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "placement-" + agentType, Namespace: preparationStateNamespace},
				Data:       map[string]string{sandboxplacement.ConfigMapKey: string(raw)},
			}
			h.create(t, registry)
			installReader := func() {
				t.Helper()
				reader, err := sandboxplacement.NewHandler(h.client, sandboxplacement.Config{
					Namespace: registry.Namespace, ConfigMapName: registry.Name,
				})
				if err != nil {
					t.Fatal(err)
				}
				f.reconciler.SandboxPreparation.Placement = reader
			}
			installReader()
			f.reconcile(t)
			expected := f.assertPrepared(t)
			f.assertAdmissionHeld(t, expected)
			receipt, historical := f.readReceipt(t)
			if expected.Placement == nil || !equality.Semantic.DeepEqual(historical.Placement, expected.Placement) ||
				historical.Placement.Registry != (w.ObjectIdentity{Name: registry.Name, UID: registry.UID}) ||
				historical.Placement.Home != expected.Home || len(historical.Placement.Nodes) != 1 ||
				historical.Placement.Nodes[0].UID != node.UID {
				t.Fatal("API-persisted receipt did not bind the exact placement/HOME lifetime")
			}
			for _, object := range []*corev1.ConfigMap{registry, receipt} {
				if !slices.Contains(expected.Sources, w.SourceReference{
					APIVersion: "v1", Kind: "ConfigMap", Namespace: object.Namespace, Name: object.Name,
					UID: object.UID, ResourceVersion: object.ResourceVersion,
				}) {
					t.Fatal("expectation did not pin durable placement inputs")
				}
			}
			f.restart()
			installReader()
			f.reconcile(t)
			if f.installer.stageCalls != 0 || !equality.Semantic.DeepEqual(f.expectation(t).Placement, historical.Placement) {
				t.Fatal("restart lost or restaged the recorded assignment")
			}
			node.Labels = map[string]string{"integration.example.com/heartbeat": "changed"}
			h.update(t, node)
			f.reconcile(t)
			if f.installer.stageCalls != 0 {
				t.Fatal("Node metadata churn invalidated the lifetime binding")
			}
			currentReceipt, current := f.readReceipt(t)
			if currentReceipt.ResourceVersion != receipt.ResourceVersion || !equality.Semantic.DeepEqual(current.Placement, historical.Placement) {
				t.Fatal("metadata churn rewrote the durable receipt")
			}
			t.Run("execution-input-and-approval-rechecks", func(t *testing.T) {
				createPreparationRevision(t, f)
				f.reconcile(t)
				expected := f.assertPrepared(t)
				target := sandboxcoordination.EnrolledTarget{
					ExecutionIdentity: sandbox.ExecutionIdentity{
						Binding: expected.Binding, Revision: expected.Revision,
						PodUID: "synthetic-enrolled-pod", NodeUID: node.UID, EnforcementEpoch: "synthetic-epoch",
					},
					Writer: sandboxwriter.Writer{
						Workload: expected.Workload, Pod: w.ObjectIdentity{Name: expected.Workload.Name + "-0", UID: "synthetic-enrolled-pod"},
						Node: expected.Placement.Nodes[0], MachineID: "synthetic-physical-incarnation", Epoch: "synthetic-epoch",
						Storage: sandboxwriter.StorageIdentity{Volume: w.ObjectIdentity{Name: "example-pv", UID: "synthetic-pv-uid"},
							Driver: "csi.example.com", Handle: "synthetic-volume"},
					},
					WriterJournal: w.ObjectIdentity{
						Name: sandboxwriter.JournalName(sandboxwriter.Lifetime{Agent: expected.Agent, Namespace: expected.Namespace, Home: expected.Home}),
						UID:  "synthetic-writer-journal-uid",
					},
				}
				intent, err := f.reconciler.SandboxExecutionIntent(h.ctx, f.agent(t), target)
				if err != nil {
					t.Fatal(err)
				}
				fences, stages := f.source.fenceCalls, f.installer.stageCalls
				for _, object := range []client.Object{
					&api.Team{ObjectMeta: metav1.ObjectMeta{Name: f.fixture.Team.Name}},
					&api.Sandbox{ObjectMeta: metav1.ObjectMeta{Name: f.fixture.Sandbox.Name}},
					&api.AgentPolicy{ObjectMeta: metav1.ObjectMeta{Name: f.fixture.Policy.Name}},
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: intent.Namespace.Name}},
					&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: intent.Namespace.Name, Name: intent.Home.Name}},
				} {
					h.get(t, client.ObjectKeyFromObject(object), object)
					labels := object.GetLabels()
					if labels == nil {
						labels = map[string]string{}
					}
					labels["example.com/observation"] = "updated"
					object.SetLabels(labels)
					h.update(t, object)
					switch object := object.(type) {
					case *api.Team:
						object.Status.ObservedGeneration = object.Generation
					case *api.Sandbox:
						object.Status.ObservedGeneration = object.Generation
					case *api.AgentPolicy:
						object.Status.ObservedGeneration = object.Generation
					case *corev1.Namespace:
						object.Status.Phase = corev1.NamespaceActive
					case *corev1.PersistentVolumeClaim:
						object.Status.Phase = corev1.ClaimBound
					}
					if err := h.client.Status().Update(h.ctx, object); err != nil {
						t.Fatal(err)
					}
					if err := f.reconciler.CheckSandboxExecutionSources(h.ctx, intent); err != nil {
						t.Fatalf("%T observation metadata invalidated current inputs: %v", object, err)
					}
				}
				originalApproval := f.source.prerequisites.Revision
				f.source.fenceError = errors.New("synthetic writer remains active")
				defer func() {
					f.source.fenceError = nil
					f.source.prerequisites.Revision = originalApproval
				}()
				workload := &appsv1.StatefulSet{}
				h.get(t, client.ObjectKey{Namespace: intent.Namespace.Name, Name: intent.Workload.Name}, workload)
				originalRV := workload.ResourceVersion
				workload.Status.Replicas = 1
				workload.Status.ReadyReplicas = 1
				if err := h.client.Status().Update(h.ctx, workload); err != nil {
					t.Fatal(err)
				}
				if workload.ResourceVersion == originalRV {
					t.Fatal("API server did not advance the status observation version")
				}
				if err := f.reconciler.CheckSandboxExecutionSources(h.ctx, intent); err != nil {
					t.Fatal("status-only API update invalidated effective workload inputs:", err)
				}
				// No StatefulSet controller or agent container runs in envtest.
				workload.Spec.Replicas = ptr.To[int32](1)
				h.update(t, workload)
				h.get(t, client.ObjectKeyFromObject(f.spare), f.spare)
				h.get(t, client.ObjectKeyFromObject(f.unusedPolicy), f.unusedPolicy)
				f.spare.Spec.Storage.Scratch.SizeLimit = resource.MustParse("2Gi")
				f.unusedPolicy.Spec.Network.BlockedCIDRs = []string{"203.0.113.0/24"}
				h.update(t, f.spare)
				h.update(t, f.unusedPolicy)
				if err := f.reconciler.CheckSandboxExecutionSources(h.ctx, intent); err != nil {
					t.Fatal("controlled scale or unused configuration changes invalidated execution:", err)
				}
				f.source.prerequisites.Revision = "withdrawn-synthetic-approval"
				if err := f.reconciler.CheckSandboxExecutionSources(h.ctx, intent); err == nil {
					t.Fatal("withdrawn prerequisite approval was accepted")
				}
				if f.source.fenceCalls != fences || f.installer.stageCalls != stages {
					t.Fatal("execution rechecks quiesced or restaged the writer")
				}
				f.assertAdmissionHeld(t, expected)
			})
			if err := h.client.Delete(h.ctx, registry); err != nil {
				t.Fatal(err)
			}
			if _, err := f.reconciler.Reconcile(h.ctx, f.request()); err == nil {
				t.Fatal("registry loss reused a cached assignment")
			}
			_, retained := f.readReceipt(t)
			if !equality.Semantic.DeepEqual(retained.Placement, historical.Placement) {
				t.Fatal("registry loss discarded historical placement")
			}
			f.reconciler.SandboxPreparation.Placement = nil
			f.source.configurationError = fmt.Errorf("synthetic current configuration outage")
			f.source.fenceError = fmt.Errorf("synthetic historical fence remains pending")
			resolveCalls, snapshotCalls := f.source.resolveCalls, f.source.snapshotCalls
			if err := h.client.Delete(h.ctx, f.agent(t)); err != nil {
				t.Fatal(err)
			}
			if _, err := f.reconciler.Reconcile(h.ctx, f.request()); err == nil ||
				!equality.Semantic.DeepEqual(f.source.lastFence.Placement, historical.Placement) ||
				f.source.resolveCalls != resolveCalls || f.source.snapshotCalls != snapshotCalls {
				t.Fatal("cleanup did not use only its durable historical placement tuple")
			}
			namespace := &corev1.Namespace{}
			h.get(t, client.ObjectKey{Name: historical.Placement.Namespace.Name}, namespace)
			if !namespace.DeletionTimestamp.IsZero() {
				t.Fatal("pending historical fencing allowed namespace deletion")
			}
		})
	}
}
