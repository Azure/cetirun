// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package integration_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxplacement"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
