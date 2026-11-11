// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package controller

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxcoordination"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestNativeOptionalIdentityDoesNotRestartUncoordinatedAgentsAPIServer(t *testing.T) {
	for _, kind := range []string{"openclaw", "hermes"} {
		t.Run(kind, func(t *testing.T) {
			h := readyReconciledPolicyIdentityHarness(t,
				newNativeStartAPIHarnessConfigured(t, true, false, corev1.ReadWriteOncePod, kind, nil))
			f := h.nativeSandboxFixture
			beforeAgent, beforeWorkload, beforePod := f.current(t), f.workload(t), f.pod(t)
			policyKey := client.ObjectKey{Name: beforeAgent.Status.Policy.EffectivePolicyRef.Name}
			policies := &AgentPolicyReconciler{Client: h.kube, APIReader: h.kube,
				NativeExecution: true, Source: h.r.SandboxPreparation.Configuration}
			for _, identity := range []api.AgentPolicyAgentIdentity{
				{Allowed: true, CredentialServices: []string{"azure-resource-manager"}}, {},
			} {
				assertPolicyIdentityContinuity(t, h, beforeAgent, beforeWorkload, beforePod, nil, func() {
					policy := &api.AgentPolicy{}
					if err := h.admin.Get(h.ctx, policyKey, policy); err != nil {
						t.Fatal(err)
					}
					policy.Spec.AgentIdentity = identity
					if err := h.admin.Update(h.ctx, policy); err != nil {
						t.Fatalf("served uncoordinated identity policy update: %v", err)
					}
					if _, err := policies.Reconcile(h.ctx, ctrl.Request{NamespacedName: policyKey}); err != nil {
						t.Fatal("refresh policy acceptance:", err)
					}
					f.reconcileOK(t)
				})
			}
		})
	}
}

func TestReconciledSandboxMigratesLegacyOptionalIdentityRevisionAPIServer(t *testing.T) {
	h := newNativeStartAPIHarnessConfigured(t, true, true, corev1.ReadWriteOncePod, "openclaw", nil)
	policyKey := client.ObjectKey{Name: "policy-example"}
	before := &api.AgentPolicy{}
	if err := h.admin.Get(h.ctx, policyKey, before); err != nil {
		t.Fatal(err)
	}
	after := before.DeepCopy()
	after.Spec.AgentIdentity = api.AgentPolicyAgentIdentity{
		Allowed: true, CredentialServices: []string{"azure-resource-manager"},
	}
	store := h.r.SandboxNativeCoordination
	change, err := store.AcquireConfigurationChange(h.ctx, before, after)
	if err != nil {
		t.Fatal("reserve Optional policy before a native lifetime starts:", err)
	}
	adapter, err := sandboxcoordination.NewKubernetesChangeAdapter(h.admin, h.admin, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileChange(h.ctx, change, adapter); err != nil {
		t.Fatal("commit Optional policy before a native lifetime starts:", err)
	}
	h = readyReconciledPolicyIdentityHarness(t, h)

	beforeAgent, beforeWorkload := h.current(t), h.workload(t)
	if beforeAgent.Status.Phase != api.PhaseReady {
		t.Fatal("fixture must be Ready before migrating the credential revision")
	}
	reservation, found, err := store.ReadNative(h.ctx, beforeAgent.UID)
	if err != nil || !found {
		t.Fatal("missing active common-executor reservation:", err)
	}
	current := beforeAgent.DeepCopy()
	current.Status.Policy.Enforcement.Credential.DesiredRevision = legacyOptionalCredentialRevision(t, after.UID)
	if err := h.admin.Status().Update(h.ctx, current); err != nil {
		t.Fatal("seed legacy Optional credential revision:", err)
	}
	assertPolicyIdentityContinuity(t, h, beforeAgent, beforeWorkload, h.pod(t), &reservation, func() { h.reconcileOK(t) })
	migrated := h.current(t)
	if migrated.Status.Policy.Enforcement.Credential.DesiredRevision == legacyOptionalCredentialRevision(t, after.UID) {
		t.Fatal("legacy Optional credential revision was not normalized")
	}

	// Active common-executor policy changes remain coordination-protected.
	policy := &api.AgentPolicy{}
	if err := h.admin.Get(h.ctx, policyKey, policy); err != nil {
		t.Fatal(err)
	}
	disabled := policy.DeepCopy()
	disabled.Spec.AgentIdentity = api.AgentPolicyAgentIdentity{}
	if _, err := store.AcquireConfigurationChange(h.ctx, policy, disabled); !errors.Is(err, sandboxcoordination.ErrConflict) {
		t.Fatalf("active policy update bypassed native coordination: %v", err)
	}
	if err := h.admin.Update(h.ctx, disabled); err == nil || !strings.Contains(err.Error(), "CoordinationRequired") {
		t.Fatalf("served API accepted an unreserved active policy update: %v", err)
	}
}

func readyReconciledPolicyIdentityHarness(t *testing.T, h *nativeStartAPIHarness) *nativeStartAPIHarness {
	t.Helper()
	h = startReconciledAPIHarness(t, h)
	expected := reconciledExpectationForTest(t, h.nativeSandboxFixture)
	observed := *h.nativeSandboxFixture
	observed.kube = h.admin
	pod := reconciledPodForTest(t, &observed, expected.Workloads[0])
	h.create(t, pod)
	h.observeReadyPod(t, pod)
	setPolicyIdentityWorkloadReady(t, h)
	h.reconcileOK(t)
	if h.current(t).Status.Phase != api.PhaseReady {
		t.Fatal("fixture must be Ready before the identity policy changes")
	}
	return h
}

func assertPolicyIdentityContinuity(t *testing.T, h *nativeStartAPIHarness, beforeAgent *api.Agent,
	beforeWorkload *appsv1.StatefulSet, beforePod *corev1.Pod, reservation *sandboxcoordination.NativeTicket, action func(),
) {
	t.Helper()
	token := aigateway.Token(h.gateway.generated[0].Key)
	h.gateway.keys[token].Spend = ptr.To(2.5)
	gateway := &policyLimitBlockRecorder{fakeAIGateway: h.gateway}
	h.r.aiGatewayClientFactory = func(context.Context) (aiGatewayKeyClient, error) { return gateway, nil }
	stages, stopWrites := h.installer.stages, 0
	writer := interceptor.NewClient(h.kube, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, object client.Object, options ...client.UpdateOption) error {
			if stateful, ok := object.(*appsv1.StatefulSet); ok && stateful.UID == beforeWorkload.UID && ptr.Deref(stateful.Spec.Replicas, 1) == 0 {
				stopWrites++
			}
			return c.Update(ctx, object, options...)
		},
		Patch: func(ctx context.Context, c client.WithWatch, object client.Object, patch client.Patch, options ...client.PatchOption) error {
			if stateful, ok := object.(*appsv1.StatefulSet); ok && stateful.UID == beforeWorkload.UID && ptr.Deref(stateful.Spec.Replicas, 1) == 0 {
				stopWrites++
			}
			return c.Patch(ctx, object, patch, options...)
		},
	})
	h.r.Client, h.r.APIReader = writer, writer
	action()
	after, workload, pod := h.current(t), h.workload(t), h.pod(t)
	key := h.gateway.keys[token]
	if after.Status.Phase != api.PhaseReady || after.Status.AIGateway.KeyID != beforeAgent.Status.AIGateway.KeyID ||
		workload.UID != beforeWorkload.UID || !reflect.DeepEqual(workload.Spec, beforeWorkload.Spec) ||
		pod.UID != beforePod.UID || h.installer.stages != stages || stopWrites != 0 || gateway.blocks != 0 ||
		key == nil || key.Blocked || ptr.Deref(key.Spend, 0) != 2.5 || len(h.gateway.generated) != 1 ||
		len(h.gateway.deletedTokens) != 0 || len(h.gateway.deletedAliases) != 0 {
		t.Fatalf("identity-free Agent did not retain its running lifetime: phase=%s stops=%d blocks=%d", after.Status.Phase, stopWrites, gateway.blocks)
	}
	if reservation != nil {
		current, found, err := h.r.SandboxNativeCoordination.ReadNative(h.ctx, beforeAgent.UID)
		if err != nil || !found || current.Reservation.ID != reservation.Reservation.ID ||
			current.Reservation.Phase != sandboxcoordination.Reserved ||
			!reflect.DeepEqual(current.Reservation.Intent.SourceDigests, reservation.Reservation.Intent.SourceDigests) {
			t.Fatalf("legacy revision migration changed the active reservation: found=%t err=%v", found, err)
		}
	}
}

func setPolicyIdentityWorkloadReady(t *testing.T, h *nativeStartAPIHarness) {
	t.Helper()
	workload := h.workload(t)
	workload.Status.ObservedGeneration = workload.Generation
	workload.Status.Replicas, workload.Status.ReadyReplicas, workload.Status.UpdatedReplicas = 1, 1, 1
	if err := h.admin.Status().Update(h.ctx, workload); err != nil {
		t.Fatal(err)
	}
}
