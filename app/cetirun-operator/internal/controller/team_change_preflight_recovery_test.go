// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

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
