// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestPolicyDenialRecoversSameKeyAndHome(t *testing.T) {
	for _, kind := range []string{"openclaw", "hermes", "custom"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			var f *nativeSandboxFixture
			if kind == "custom" {
				f = newSandboxRuntimeFixtureForAgent(t, true, nil, true, kind, true, false,
					withCommonCustom(t, "chat"), withReconciledCoordination(t))
				settleReadyForTest(t, f)
			} else {
				f = readyPolicyRateLimitFixture(t, kind, true)
			}
			before := f.current(t)
			token := aigateway.Token(f.gateway.generated[0].Key)
			key := f.gateway.keys[token]
			reset := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
			key.Spend, key.BudgetResetAt = ptr.To(7.5), &reset
			budget, duration := *key.MaxBudget, key.BudgetDuration
			claims := &corev1.PersistentVolumeClaimList{}
			if err := f.kube.List(ctx, claims, client.InNamespace(instanceNamespace(before.Name))); err != nil || len(claims.Items) == 0 {
				t.Fatal("fixture must have HOME:", err)
			}
			gateway := &policyLimitBlockRecorder{fakeAIGateway: f.gateway}
			f.r.aiGatewayClientFactory = func(context.Context) (aiGatewayKeyClient, error) { return gateway, nil }
			policy := &api.AgentPolicy{}
			policyKey := client.ObjectKey{Name: before.Status.Policy.EffectivePolicyRef.Name}
			if err := f.kube.Get(ctx, policyKey, policy); err != nil {
				t.Fatal(err)
			}
			allowed := append([]string(nil), policy.Spec.ModelAccess.SharedModels.Allowed...)
			config := f.r.SandboxPreparation
			policies := &AgentPolicyReconciler{Client: f.kube, APIReader: f.kube, NativeExecution: true,
				Source: config.Configuration, Now: config.Now, MaxSnapshotAge: config.MaxSnapshotAge}
			setModels := func(models []string) {
				t.Helper()
				if err := f.kube.Get(ctx, policyKey, policy); err != nil {
					t.Fatal(err)
				}
				policy.Spec.ModelAccess.SharedModels.Allowed = models
				policy.Generation++
				if err := f.kube.Update(ctx, policy); err != nil {
					t.Fatal(err)
				}
				if _, err := policies.Reconcile(ctx, ctrl.Request{NamespacedName: policyKey}); err != nil {
					t.Fatal(err)
				}
			}
			setModels(nil)
			for range 3 {
				_, _ = f.reconcile()
			}
			held := f.current(t)
			resolved := apimeta.FindStatusCondition(held.Status.Conditions, api.ConditionPolicyResolved)
			if held.Status.Phase == api.PhaseReady || resolved == nil || resolved.Reason != "PolicyDenied" ||
				ptr.Deref(f.workload(t).Spec.Replicas, 1) != 0 || !key.Blocked || gateway.blocks != 1 {
				t.Fatalf("denial did not stop and block once: phase=%s policy=%+v blocked=%t writes=%d", held.Status.Phase, resolved, key.Blocked, gateway.blocks)
			}
			f.r = restartEntraReconciler(f.r)
			setModels(allowed)
			// A failed read during recovery must keep the durable block ownership.
			f.gateway.getErr = errors.New("synthetic Gateway outage")
			_, _ = f.reconcile()
			if !key.Blocked || f.current(t).Status.AIGateway.Reason != aiGatewayReasonPolicyReprojection {
				t.Fatal("Gateway outage lost the temporary block")
			}
			f.gateway.getErr = nil
			settleReadyForTest(t, f)
			after := f.current(t)
			if after.Status.AIGateway.KeyID != before.Status.AIGateway.KeyID || f.gateway.keys[token] != key || key.Blocked ||
				*key.Spend != 7.5 || !key.BudgetResetAt.Equal(reset) || *key.MaxBudget != budget || key.BudgetDuration != duration ||
				len(f.gateway.generated) != 1 || len(f.gateway.deletedTokens) != 0 || len(f.gateway.deletedAliases) != 0 {
				t.Fatalf("recovery replaced the key or ledger: status=%+v key=%+v", after.Status.AIGateway, key)
			}
			for _, claim := range claims.Items {
				current := &corev1.PersistentVolumeClaim{}
				if err := f.kube.Get(ctx, client.ObjectKeyFromObject(&claim), current); err != nil || current.UID != claim.UID || !reflect.DeepEqual(current.Spec, claim.Spec) {
					t.Fatalf("recovery changed HOME: %s err=%v", claim.Name, err)
				}
			}
		})
	}
}

func TestPolicyDenialPreservesAdministratorBlock(t *testing.T) {
	ctx := context.Background()
	f := readyPolicyRateLimitFixture(t, "openclaw", true)
	current := f.current(t)
	token := aigateway.Token(f.gateway.generated[0].Key)
	f.gateway.keys[token].Blocked = true
	current.Spec.Suspended = true
	current.Generation++
	if err := f.kube.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	current = f.current(t)
	if _, err := f.r.holdPolicyDenied(ctx, current); err != nil {
		t.Fatal(err)
	}
	current = f.current(t)
	if current.Status.Phase != api.PhaseSuspended || aiGatewayOperatorOwnedBlock(current.Status.AIGateway.Reason) || !f.gateway.keys[token].Blocked {
		t.Fatalf("denied suspension adopted an administrator block: %+v", current.Status)
	}
	current.Spec.Suspended = false
	current.Generation++
	if err := f.kube.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	f.r = restartEntraReconciler(f.r)
	for range 3 {
		_, _ = f.reconcile()
	}
	if !f.gateway.keys[token].Blocked || f.current(t).Status.Phase == api.PhaseReady || len(f.gateway.generated) != 1 || len(f.gateway.deletedTokens) != 0 {
		t.Fatal("recovery cleared or replaced an administrator block")
	}
}

func TestPolicyDenialStatusFailureStillStopsCompute(t *testing.T) {
	for _, receipt := range []bool{false, true} {
		name := "before block"
		if receipt {
			name = "after block"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := readyPolicyRateLimitFixture(t, "openclaw", true)
			token := aigateway.Token(f.gateway.generated[0].Key)
			failure := errors.New("synthetic status write failure")
			failed := false
			writer := interceptor.NewClient(f.kube, interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, object client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					if agent, ok := object.(*api.Agent); ok && !failed && agent.Status.AIGateway != nil &&
						agent.Status.AIGateway.Reason == aiGatewayReasonPolicyReprojection && agent.Status.AIGateway.Blocked == receipt {
						failed = true
						return failure
					}
					return c.SubResource(subresource).Patch(ctx, object, patch, opts...)
				},
			})
			f.r.Client, f.r.APIReader = writer, writer
			if _, err := f.r.holdPolicyDenied(ctx, f.current(t)); !errors.Is(err, failure) || !failed {
				t.Fatalf("status failure was hidden: %v", err)
			}
			if ptr.Deref(f.workload(t).Spec.Replicas, 1) != 0 || f.gateway.keys[token].Blocked != receipt {
				t.Fatal("failed status write skipped shutdown or blocked before durable intent")
			}
			f.r = restartEntraReconciler(f.r)
			if _, err := f.r.holdPolicyDenied(ctx, f.current(t)); err != nil {
				t.Fatal(err)
			}
			if !f.gateway.keys[token].Blocked {
				t.Fatal("retry did not block the original key")
			}
			settleReadyForTest(t, f)
			if f.gateway.keys[token].Blocked || len(f.gateway.generated) != 1 {
				t.Fatal("status recovery did not restore the original key")
			}
		})
	}
}

func TestPolicyDenialDoesNotReplaceMissingKey(t *testing.T) {
	ctx := context.Background()
	f := readyPolicyRateLimitFixture(t, "openclaw", true)
	current := f.current(t)
	token := aigateway.Token(f.gateway.generated[0].Key)
	delete(f.gateway.keys, token)
	if _, err := f.r.holdPolicyDenied(ctx, current); err == nil {
		t.Fatal("missing key was accepted as a confirmed block")
	}
	for range 3 {
		_, _ = f.reconcile()
	}
	condition := apimeta.FindStatusCondition(f.current(t).Status.Conditions, api.ConditionAIGatewayReady)
	if len(f.gateway.generated) != 1 || condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != aiGatewayReasonKeyRecordLost {
		t.Fatalf("missing record bypassed continuity guard: %+v", condition)
	}
}

// Deletion can revisit migration cleanup after the ordinary deletion handler
// already revoked the key. Only that destructive lifetime may retry deletion.
func TestMigrationCleanupAfterGatewayDeletion(t *testing.T) {
	ctx := context.Background()
	f := newMigrationFixture(t, "openclaw", true)
	requestMigration(t, f, "migration-00000001", "node-b")
	for range 3 {
		f.reconcileOK(t)
	}
	if err := f.kube.Delete(ctx, f.current(t)); err != nil {
		t.Fatal(err)
	}
	acknowledgeMigrationSource(t, f)
	f.reconcileOK(t)
	f.reconcileOK(t)
	acknowledgeMigrationDetach(t, f)
	f.reconcileOK(t)
	if done, err := f.r.cleanupMigration(ctx, f.current(t)); err != nil || !done {
		t.Fatalf("migration cleanup did not finish: done=%t err=%v", done, err)
	}
	if err := f.r.deleteAIGatewayKey(ctx, f.current(t)); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if done, err := f.r.cleanupMigration(ctx, f.current(t)); err != nil || !done {
			t.Fatalf("deleted key prevented repeated migration cleanup: done=%t err=%v", done, err)
		}
	}
	if len(f.gateway.keys) != 0 || len(f.gateway.generated) != 1 {
		t.Fatal("deletion recreated its key")
	}
}
