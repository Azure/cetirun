// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package integration_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/controller"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Real API defaults, UID ownership and held admission; no Blob CSI, NFS,
// runtime or writer-exclusion implementation is exercised or qualified.
func TestSandboxBootstrapSkillsPublication(t *testing.T) {
	h := newPreparationHarness(t)
	for _, typ := range []string{"openclaw", "hermes"} {
		t.Run(typ, func(t *testing.T) {
			f := h.newCaseWithPrerequisites(t, typ, false)
			raw, err := json.Marshal(map[string]any{
				"version": "v1", "teams": map[string]any{f.fixture.Team.Name: map[string]any{
					"teamUID": f.fixture.Team.UID, "revision": "published-v1",
					"source": map[string]any{
						"resourceGroup": "skills-example", "storageAccount": "publishedskills",
						"containerName": "skills-v1", "storageClassName": "azureblob-nfs-premium", "storageSize": "1Gi",
					},
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			catalog := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: f.fixture.Agent.Name + "-skills", Namespace: preparationStateNamespace},
				Data:       map[string]string{controller.SandboxSkillsCatalogKey: string(raw)},
			}
			h.create(t, catalog)
			configureBootstrapSkillsCatalog(t, f, catalog.Name)
			f.reconcile(t) // Persist namespace ownership before Gateway key projection.
			if _, err := f.reconciler.Reconcile(h.ctx, f.request()); !errors.Is(err, sandbox.ErrObservationUnavailable) {
				t.Fatal("catalog bootstrap did not stop at unavailable qualified writer evidence:", err)
			}
			agent := f.agent(t)
			if f.installer.stageCalls != 0 || agent.Status.Sandbox != nil || agent.Status.AccessURL != "" ||
				!slices.Contains(agent.Finalizers, sandbox.HoldFinalizer) ||
				slices.Contains(agent.Finalizers, sandbox.PreparationFinalizer) ||
				!apimeta.IsStatusConditionFalse(agent.Status.Conditions, api.ConditionAgentConfigReady) {
				t.Fatal("real API persistence was confused with runnable qualification")
			}
			h.get(t, client.ObjectKeyFromObject(f.fixture.Namespace), f.fixture.Namespace)
			h.get(t, client.ObjectKeyFromObject(f.fixture.Input.Home), f.fixture.Input.Home)
			h.get(t, client.ObjectKeyFromObject(f.fixture.Input.Skills), f.fixture.Input.Skills)
			claim := f.fixture.Input.Skills
			volume := &corev1.PersistentVolume{}
			h.get(t, client.ObjectKey{Name: claim.Spec.VolumeName}, volume)
			if volume.Spec.ClaimRef.UID != claim.UID || volume.Spec.ClaimRef.Namespace != claim.Namespace ||
				volume.Spec.CSI == nil || !volume.Spec.CSI.ReadOnly ||
				volume.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain ||
				!slices.Contains(claim.Finalizers, sandbox.BootstrapFinalizer) ||
				!slices.Contains(volume.Finalizers, sandbox.BootstrapFinalizer) ||
				claim.Name == f.fixture.Input.Home.Name {
				t.Fatal("API-defaulted skills lost their exact static binding or lifecycle protection")
			}
			originalClaim, originalVolume := claim.DeepCopy(), volume.DeepCopy()
			if _, err := f.reconciler.Reconcile(h.ctx, f.request()); !errors.Is(err, sandbox.ErrObservationUnavailable) {
				t.Fatal("API-defaulted bindings failed immutable retry:", err)
			}
			h.get(t, client.ObjectKeyFromObject(claim), claim)
			h.get(t, client.ObjectKeyFromObject(volume), volume)
			if claim.ResourceVersion != originalClaim.ResourceVersion || volume.ResourceVersion != originalVolume.ResourceVersion {
				t.Fatal("immutable retry rewrote the publication bindings")
			}

			h.create(t, f.fixture.ServiceAccount)
			source := f.reconciler.SandboxPreparation.Prerequisites.(*controller.KubernetesSandboxPrerequisites)
			f.source.prerequisites, err = source.Resolve(h.ctx, f.agent(t), f.fixture.Team)
			if err != nil {
				t.Fatal(err)
			}
			source.WriterExclusion = f.source // Synthetic evidence permits only the held test path.
			f.reconcile(t)
			expected := f.assertPrepared(t)
			f.assertAdmissionHeld(t, expected)
			receipt := bootstrapCaseReceipt(t, f)
			for _, object := range []struct {
				kind string
				item client.Object
			}{{"ConfigMap", catalog}, {"ConfigMap", receipt}, {"PersistentVolume", volume}} {
				ref := preparationSource(t, expected, object.kind, object.item.GetNamespace(), object.item.GetName())
				if ref.UID != object.item.GetUID() || ref.ResourceVersion != object.item.GetResourceVersion() {
					t.Fatal("held admission lost a current publication dependency")
				}
			}
			if !expected.ExecutionHeld || expected.Skills != (w.ObjectIdentity{Name: claim.Name, UID: claim.UID}) {
				t.Fatal("publication handoff did not preserve the execution hold and exact claim")
			}
			var pods corev1.PodList
			if err := h.client.List(h.ctx, &pods, client.InNamespace(claim.Namespace)); err != nil || len(pods.Items) != 0 {
				t.Fatal("catalog handoff started a Pod:", err)
			}
			catalog.Data[controller.SandboxSkillsCatalogKey] = strings.ReplaceAll(catalog.Data[controller.SandboxSkillsCatalogKey], "published-v1", "published-v2")
			h.update(t, catalog)
			if _, err := f.reconciler.Reconcile(h.ctx, f.request()); !errors.Is(err, w.ErrStorageTransitionRequired) {
				t.Fatal("changed approval did not require explicit recovery:", err)
			}
			workload := &appsv1.StatefulSet{}
			h.get(t, client.ObjectKey{Namespace: claim.Namespace, Name: f.workloadName()}, workload)
			if workload.Spec.Replicas == nil || *workload.Spec.Replicas != 0 ||
				!apimeta.IsStatusConditionFalse(f.agent(t).Status.Conditions, api.ConditionAgentConfigReady) {
				t.Fatal("changed publication started or retained prepared status")
			}
			deletePublishedSkillsCase(t, f, catalog, volume)
		})
	}
}

func deletePublishedSkillsCase(t *testing.T, f *preparationCase, catalog *corev1.ConfigMap, volume *corev1.PersistentVolume) {
	t.Helper()
	h := f.h
	receipt := bootstrapCaseReceipt(t, f)
	agent := f.agent(t)
	claim := &corev1.PersistentVolumeClaim{}
	h.get(t, client.ObjectKeyFromObject(f.fixture.Input.Skills), claim)
	h.get(t, client.ObjectKeyFromObject(volume), volume)
	const retained = "example.com/publication-retention"
	volume.Finalizers = append(volume.Finalizers, retained)
	h.update(t, volume)
	if err := h.client.Delete(h.ctx, catalog); err != nil {
		t.Fatal(err)
	}
	fenceFailure := errors.New("synthetic publication cleanup fence is unavailable")
	f.source.fenceError = fenceFailure
	if err := h.client.Delete(h.ctx, agent, client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil {
		t.Fatal(err)
	}
	// envtest has no GC. Send the dependent DELETEs it can issue before the
	// controller's shutdown/fencing gates have completed.
	for _, object := range []client.Object{claim, volume} {
		if err := h.client.Delete(h.ctx, object); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.reconciler.Reconcile(h.ctx, f.request()); !errors.Is(err, fenceFailure) {
		t.Fatal("publication cleanup bypassed the existing preparation fence:", err)
	}
	for _, object := range []client.Object{claim, volume} {
		uid := object.GetUID()
		h.get(t, client.ObjectKeyFromObject(object), object)
		if object.GetUID() != uid || object.GetDeletionTimestamp().IsZero() ||
			!slices.Contains(object.GetFinalizers(), sandbox.BootstrapFinalizer) {
			t.Fatal("foreground deletion removed a publication binding before cleanup authorization")
		}
	}
	f.source.fenceError = nil
	f.reconcile(t)
	h.get(t, client.ObjectKeyFromObject(claim), claim)
	h.get(t, client.ObjectKeyFromObject(volume), volume)
	if slices.Contains(claim.Finalizers, sandbox.BootstrapFinalizer) ||
		!slices.Contains(volume.Finalizers, sandbox.BootstrapFinalizer) {
		t.Fatal("cleanup did not release the claim before the cluster-scoped PV")
	}
	f.completeNamespaceDeletion(t)
	if _, err := f.reconciler.Reconcile(h.ctx, f.request()); err == nil ||
		!strings.Contains(err.Error(), "PV deletion is incomplete") {
		t.Fatal("cleanup did not retain state for the independently retained PV:", err)
	}
	h.get(t, client.ObjectKeyFromObject(volume), volume)
	h.get(t, client.ObjectKeyFromObject(receipt), receipt)
	if slices.Contains(volume.Finalizers, sandbox.BootstrapFinalizer) ||
		!slices.Contains(volume.Finalizers, retained) ||
		!slices.Contains(volume.Finalizers, "kubernetes.io/pv-protection") ||
		volume.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain ||
		volume.Spec.CSI == nil || !volume.Spec.CSI.ReadOnly ||
		!slices.Contains(receipt.Finalizers, sandbox.BootstrapFinalizer) ||
		!slices.Contains(f.agent(t).Finalizers, sandbox.BootstrapFinalizer) {
		t.Fatal("cleanup changed data retention or dropped unresolved ownership protection")
	}
	// The PVC is absent. Simulate the missing PV protection controller, then
	// prove the operator still preserves the independent synthetic finalizer.
	volume.Finalizers = slices.DeleteFunc(volume.Finalizers, func(value string) bool {
		return value == "kubernetes.io/pv-protection"
	})
	h.update(t, volume)
	if _, err := f.reconciler.Reconcile(h.ctx, f.request()); err == nil ||
		!strings.Contains(err.Error(), "PV deletion is incomplete") {
		t.Fatal("cleanup ignored another controller's pending PV finalizer:", err)
	}
	h.get(t, client.ObjectKeyFromObject(volume), volume)
	if !slices.Equal(volume.Finalizers, []string{retained}) {
		t.Fatal("cleanup changed another controller's PV finalizer")
	}
	volume.Finalizers = nil // Completion by the synthetic finalizer owner, not the operator.
	h.update(t, volume)
	f.reconcile(t)
	for _, object := range []client.Object{claim, volume, receipt} {
		if err := h.client.Get(h.ctx, client.ObjectKeyFromObject(object), object); !apierrors.IsNotFound(err) {
			t.Fatalf("cleanup retained %T after confirmed binding deletion: %v", object, err)
		}
	}
	agent = f.agent(t)
	if !slices.Equal(agent.Finalizers, []string{metav1.FinalizerDeleteDependents}) {
		t.Fatal("cleanup removed GC's finalizer or retained its own completed obligations")
	}
	agent.Finalizers = nil // Simulate foreground GC after all recorded bindings are absent.
	h.update(t, agent)
	if err := h.client.Get(h.ctx, client.ObjectKeyFromObject(agent), &api.Agent{}); !apierrors.IsNotFound(err) {
		t.Fatal("foreground publication cleanup did not complete:", err)
	}
}
