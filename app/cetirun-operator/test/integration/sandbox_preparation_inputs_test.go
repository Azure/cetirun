// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package integration_test

import (
	"context"
	"encoding/json"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func createPreparationRevision(t *testing.T, f *preparationCase) *appsv1.ControllerRevision {
	t.Helper()
	workload := &appsv1.StatefulSet{}
	f.h.get(t, client.ObjectKey{Namespace: f.fixture.Namespace.Name, Name: f.workloadName()}, workload)
	template, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&workload.Spec.Template)
	if err != nil {
		t.Fatal(err)
	}
	template["$patch"] = "replace"
	raw, err := json.Marshal(map[string]any{"spec": map[string]any{"template": template}})
	if err != nil {
		t.Fatal(err)
	}
	revision := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: workload.Namespace, Name: workload.Name + "-revision",
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(workload, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))},
		},
		Data: runtime.RawExtension{Raw: raw}, Revision: 1,
	}
	f.h.create(t, revision)
	workload.Status.UpdateRevision, workload.Status.ObservedGeneration = revision.Name, workload.Generation
	if err := f.h.client.Status().Update(f.h.ctx, workload); err != nil {
		t.Fatal(err)
	}
	return revision
}

// Platform approvals, writer exclusion and the ControllerRevision producer are
// synthetic. Object identities, CAS failures and held Pod admission use the API.
func TestSandboxPreparationStartInputs(t *testing.T) {
	h := newPreparationHarness(t)
	for _, typ := range []string{"openclaw", "hermes"} {
		t.Run(typ, func(t *testing.T) {
			f := h.newCase(t, typ)
			f.reconcileConfiguration(t)
			restart := func() {
				f.restart()
				f.reconciler.ChartRegistry = "registry.example.com"
			}
			restart()
			f.reconcile(t)
			expected := f.assertPrepared(t)
			journal, _ := f.readJournal(t)
			pin := preparationSource(t, expected, "ConfigMap", journal.Namespace, journal.Name)
			if pin.UID != journal.UID || pin.ResourceVersion != journal.ResourceVersion || expected.ControllerRevision != "" {
				t.Fatal("held expectation did not pin the actual grant journal or fabricated a controller revision")
			}
			receipt, _ := f.readReceipt(t)
			restart()
			f.reconcile(t)
			unchanged, _ := f.readReceipt(t)
			if unchanged.UID != receipt.UID || unchanged.ResourceVersion != receipt.ResourceVersion || f.installer.stageCalls != 0 {
				t.Fatal("restart changed the v1 cleanup receipt or repeated held staging")
			}
			revision := createPreparationRevision(t, f)
			f.reconcile(t)
			expected = f.assertPrepared(t)
			pin = preparationSource(t, expected, "ControllerRevision", revision.Namespace, revision.Name)
			if pin.UID != revision.UID || pin.ResourceVersion != revision.ResourceVersion || expected.ControllerRevision != revision.Name {
				t.Fatal("held expectation did not bind the actual owned ControllerRevision")
			}
			f.assertAdmissionHeld(t, expected)

			agent := f.agent(t)
			image := "registry.example.com/" + typ + "-gateway:" + h.chart.Metadata.AppVersion
			agent.Spec.Agent.Image = &image
			h.update(t, agent)
			f.source.prerequisites.AgentGeneration = agent.Generation
			f.reconcile(t)
			expected = f.assertPrepared(t)
			if expected.Template.Spec.Containers[0].Image != image {
				t.Fatal("explicit qualified native image was not retained")
			}
			f.assertAdmissionHeld(t, expected)

			for _, failure := range []string{"expectation-conflict", "journal-changed-after-publication"} {
				t.Run(failure, func(t *testing.T) {
					team := &api.Team{}
					h.get(t, client.ObjectKeyFromObject(f.fixture.Team), team)
					team.Labels = map[string]string{"integration.example.com/refresh": failure}
					h.update(t, team)
					injected := false
					key := client.ObjectKey{
						Namespace: preparationStateNamespace, Name: w.PodExpectationName(f.fixture.Namespace.Name, f.fixture.Namespace.UID),
					}
					f.reconciler.Client = &heldInputPatchClient{
						Client: h.client,
						patch: func(ctx context.Context, object client.Object, patch client.Patch, opts ...client.PatchOption) error {
							if client.ObjectKeyFromObject(object) != key || injected {
								return h.client.Patch(ctx, object, patch, opts...)
							}
							injected = true
							if failure == "expectation-conflict" {
								current := &corev1.ConfigMap{}
								h.get(t, key, current)
								base := current.DeepCopy()
								current.Labels = map[string]string{"integration.example.com/concurrent": "true"}
								if err := h.client.Patch(ctx, current, client.MergeFrom(base)); err != nil {
									return err
								}
								return h.client.Patch(ctx, object, patch, opts...)
							}
							if err := h.client.Patch(ctx, object, patch, opts...); err != nil {
								return err
							}
							current := &corev1.ConfigMap{}
							h.get(t, client.ObjectKey{
								Namespace: preparationStateNamespace, Name: sandboxpolicy.GrantJournalName(f.fixture.Agent.UID),
							}, current)
							base := current.DeepCopy()
							current.Labels["integration.example.com/concurrent"] = "true"
							return h.client.Patch(ctx, current, client.MergeFrom(base))
						},
					}
					_, err := f.reconciler.Reconcile(h.ctx, f.request())
					if err == nil || !injected || (failure == "expectation-conflict" && !apierrors.IsConflict(err)) {
						t.Fatalf("publication race did not fail closed: %v", err)
					}
					f.assertHeld(t)
					f.assertRevoked(t)
					f.readReceipt(t)
					restart()
					f.reconcile(t)
					f.assertAdmissionHeld(t, f.assertPrepared(t))
				})
			}
			agent = f.agent(t)
			agent.Spec.Suspended = true
			h.update(t, agent)
			f.reconcile(t)
			f.assertHeld(t)
			f.assertRevoked(t)
			f.readReceipt(t)
		})
	}
}

type heldInputPatchClient struct {
	client.Client
	patch func(context.Context, client.Object, client.Patch, ...client.PatchOption) error
}

func (c *heldInputPatchClient) Patch(ctx context.Context, object client.Object, patch client.Patch, opts ...client.PatchOption) error {
	return c.patch(ctx, object, patch, opts...)
}
