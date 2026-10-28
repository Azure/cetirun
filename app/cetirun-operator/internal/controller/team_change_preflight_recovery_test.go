// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestTeamChangeStaleValidatingReconcileRetainsStoppingTransition(t *testing.T) {
	ctx := context.Background()
	f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, "hermes", true, false, withReconciledCoordination(t))
	markTeamChangeSourceRunning(t, f)
	target := createTeamChangeTarget(t, f, "stale-checkpoint-target", false)
	submitTeamChange(t, f, target, "stale-checkpoint-request-0001")
	f.reconcileOK(t) // Persist the initial Validating checkpoint.
	stale := f.current(t)
	if stale.Status.TeamChange.Phase != api.TeamChangeValidating {
		t.Fatalf("initial checkpoint phase = %s, want Validating", stale.Status.TeamChange.Phase)
	}
	f.reconcileOK(t) // Acquire and persist the current Stopping checkpoint.
	current := f.current(t)
	if current.Status.TeamChange.Phase != api.TeamChangeStopping {
		t.Fatalf("current checkpoint phase = %s, want Stopping", current.Status.TeamChange.Phase)
	}
	ticket, found, err := f.r.SandboxNativeCoordination.ReadTransition(ctx, current.UID)
	if err != nil || !found {
		t.Fatalf("read acquired transition: found=%t err=%v", found, err)
	}

	// An informer pass that started before the previous status update can still
	// provide Validating. APIReader remains current, so its status checkpoint
	// write conflicts. That stale pass must not release the transition now owned
	// by the authoritative Stopping checkpoint.
	f.r.Client = interceptor.NewClient(f.kube, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
			if out, ok := object.(*api.Agent); ok && key.Name == stale.Name {
				*out = *stale.DeepCopy()
				return nil
			}
			return c.Get(ctx, key, object, options...)
		},
	})
	_, reconcileErr := f.reconcile()
	f.r.Client = f.kube
	if reconcileErr == nil {
		t.Fatal("stale status checkpoint unexpectedly succeeded")
	}
	current = f.current(t)
	retained, found, err := f.r.SandboxNativeCoordination.ReadTransition(ctx, current.UID)
	if err != nil || !found || retained.Transition.ID != ticket.Transition.ID {
		t.Fatalf("stale full Reconcile removed current transition: found=%t err=%v", found, err)
	}
	if current.Status.TeamChange.Phase != api.TeamChangeStopping || !current.Status.TeamChange.Fenced {
		t.Fatalf("stale reconcile changed the authoritative checkpoint: %+v", current.Status.TeamChange)
	}
}

func TestTeamChangeTransitionSurvivesCheckpointWriteFailure(t *testing.T) {
	for _, committed := range []bool{false, true} {
		name := "before-write"
		if committed {
			name = "response-lost-after-commit"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, "hermes", true, false, withReconciledCoordination(t))
			markTeamChangeSourceRunning(t, f)
			target := createTeamChangeTarget(t, f, "checkpoint-write-failure-"+name, false)
			submitTeamChange(t, f, target, "checkpoint-write-failure-"+name)
			f.reconcileOK(t) // Persist the initial Validating checkpoint.

			// An acquire may commit while the status write fails, or while its
			// response is lost after the API server accepted it. This pass cannot
			// prove it still owns the checkpoint, so it retains the ticket for a
			// fresh Validating reconcile to recover.
			f.r.Client = interceptor.NewClient(f.kube, interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, underlying client.Client, subResource string, object client.Object, patch client.Patch, options ...client.SubResourcePatchOption) error {
					if subResource == "status" {
						if _, ok := object.(*api.Agent); ok {
							if committed {
								if err := underlying.SubResource(subResource).Patch(ctx, object, patch, options...); err != nil {
									return err
								}
							}
							return fmt.Errorf("synthetic lost status response")
						}
					}
					return underlying.SubResource(subResource).Patch(ctx, object, patch, options...)
				},
			})
			_, reconcileErr := f.reconcile()
			f.r.Client = f.kube
			if reconcileErr == nil {
				t.Fatal("checkpoint write failure unexpectedly succeeded")
			}
			current := f.current(t)
			ticket, found, err := f.r.SandboxNativeCoordination.ReadTransition(ctx, current.UID)
			if err != nil || !found {
				t.Fatalf("checkpoint write failure released the acquired transition: found=%t err=%v", found, err)
			}
			if current.Status.TeamChange.Phase != api.TeamChangeValidating || current.Status.TeamChange.Fenced != committed {
				t.Fatalf("unexpected durable validating state after failed checkpoint write: %+v", current.Status.TeamChange)
			}

			f.reconcileOK(t)
			current = f.current(t)
			if current.Status.TeamChange.Phase != api.TeamChangeStopping || current.Status.TeamChange.TransitionID != ticket.Transition.ID {
				t.Fatalf("fresh reconcile did not recover the retained transition: %+v", current.Status.TeamChange)
			}
		})
	}
}

func TestTeamChangePreflightRecoversTargetDriftAndLostCleanupCheckpoint(t *testing.T) {
	for _, targetDeleted := range []bool{false, true} {
		for _, acquireCheckpointLost := range []bool{false, true} {
			name := "changed-target"
			if targetDeleted {
				name = "deleted-target"
			}
			if acquireCheckpointLost {
				name += "-orphan-acquire"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, "openclaw", true, false, withReconciledCoordination(t))
				markTeamChangeSourceRunning(t, f)
				source := f.current(t)
				target := createTeamChangeTarget(t, f, "preflight-race-target", false)
				submitTeamChange(t, f, target, "preflight-recovery-0001")
				f.reconcileOK(t) // Capture the request before acquiring its ticket.
				current := f.current(t)
				if err := f.r.preflightTeamChange(ctx, current, current.Status.TeamChange); err != nil {
					t.Fatal(err)
				}
				// Simulate a restart after acquire, before the next phase checkpoint.
				// An orphan acquisition must also be found before target validation.
				if acquireCheckpointLost {
					current = f.current(t)
					current.Status.TeamChange = stateForTeamChange(current, current.Spec.TeamChangeRequest)
					if err := f.kube.Status().Update(ctx, current); err != nil {
						t.Fatal(err)
					}
				}
				changed := &api.Team{}
				if err := f.kube.Get(ctx, client.ObjectKey{Name: target.team.Name}, changed); err != nil {
					t.Fatal(err)
				}
				if targetDeleted {
					if err := f.kube.Delete(ctx, changed); err != nil {
						t.Fatal(err)
					}
				} else {
					changed.Spec.MemberContext.Role = "Updated role"
					if err := f.kube.Update(ctx, changed); err != nil {
						t.Fatal(err)
					}
				}
				current = f.current(t)
				preflightErr := f.r.preflightTeamChange(ctx, current, current.Status.TeamChange)
				if preflightErr == nil {
					t.Fatal("target drift was accepted")
				}
				if current.Status.TeamChange.Fenced || current.Status.TeamChange.TransitionID != "" {
					t.Fatalf("successful preflight cleanup retained its fence: %v", preflightErr)
				}
				if _, found, err := f.r.SandboxNativeCoordination.ReadTransition(ctx, source.UID); err != nil || found {
					t.Fatalf("preflight ticket was not released: found=%t err=%v", found, err)
				}
				// Lose the failure status write after release. Recovery must inspect
				// the absent ticket before any stale/deleted target configuration.
				if !f.current(t).Status.TeamChange.Fenced {
					t.Fatal("fixture did not retain the lost cleanup checkpoint")
				}
				f.reconcileOK(t)
				current = f.current(t)
				if current.Status.TeamChange.Phase != api.TeamChangeFailed || current.Status.TeamChange.Fenced ||
					current.Status.TeamChange.TransitionID != "" || current.Status.Phase != api.PhaseReady ||
					current.Spec.Team != source.Spec.Team || *f.workload(t).Spec.Replicas != 1 {
					t.Fatalf("preflight recovery did not retain the running source: %+v", current.Status.TeamChange)
				}
				// A failed preflight is terminal for this request, and a fresh ID
				// must be able to complete a new handoff without manual cleanup.
				retryTarget := createTeamChangeTarget(t, f, "preflight-retry-target", false)
				submitTeamChange(t, f, retryTarget, "preflight-recovery-0002")
				completeTeamChange(t, f, retryTarget, nil, false)
			})
		}
	}
}

func TestTeamChangeInvalidReplacementPreservesAcquiredCheckpoint(t *testing.T) {
	ctx := context.Background()
	f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, "openclaw", true, false, withReconciledCoordination(t))
	markTeamChangeSourceRunning(t, f)
	target := createTeamChangeTarget(t, f, "invalid-replacement-target", false)
	submitTeamChange(t, f, target, "invalid-replacement-0001")
	f.reconcileOK(t)
	current := f.current(t)
	if err := f.r.preflightTeamChange(ctx, current, current.Status.TeamChange); err != nil {
		t.Fatal(err)
	}
	ticketID := current.Status.TeamChange.TransitionID
	current.Spec.TeamChangeRequest.ExpectedUID = "different-agent-uid"
	if err := f.kube.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	f.reconcileOK(t)
	current = f.current(t)
	if !current.Status.TeamChange.Fenced || current.Status.TeamChange.TransitionID != ticketID ||
		current.Status.TeamChange.Phase != api.TeamChangeValidating ||
		current.Status.TeamChange.OriginalRequest.ExpectedUID != current.UID {
		t.Fatalf("invalid replacement discarded the acquired checkpoint: %+v", current.Status.TeamChange)
	}
}
