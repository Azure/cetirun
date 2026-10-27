// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"
	"slices"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxbinding"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxcoordination"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const teamChangeWait = 5 * time.Second

// reconcileTeamChange is deliberately ahead of policy and workload
// reconciliation. A TeamChangeRequest is request-only configuration: it must
// not make the old running proof stale until its target has been validated and
// the source has been fenced.
func (r *AgentReconciler) reconcileTeamChange(ctx context.Context, agent *api.Agent) (bool, ctrl.Result, error) {
	request, state := agent.Spec.TeamChangeRequest, agent.Status.TeamChange
	if request == nil {
		// A captured operation is retained for auditability. It cannot continue
		// without its durable request, so retain the safe hold if it had stopped.
		if state != nil && state.Phase != api.TeamChangeCompleted && state.Fenced {
			if agent.Spec.Suspended {
				return r.pauseTeamChange(ctx, agent, state)
			}
			return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "RequestRemoved", "the Team change request was removed after the source stopped")
		}
		return true, ctrl.Result{}, nil
	}
	if err := validTeamChangeRequest(agent, request); err != nil {
		// A malformed replacement must never discard an acquired operation's
		// durable fence, even if an older object bypassed request admission.
		if state != nil && state.Fenced && state.Phase != api.TeamChangeCompleted {
			if agent.Spec.Suspended {
				return r.pauseTeamChange(ctx, agent, state)
			}
			return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "InvalidRequest", err.Error())
		}
		return false, ctrl.Result{}, r.teamChangeFailure(ctx, agent, stateForTeamChange(agent, request), "InvalidRequest", err.Error())
	}
	if state == nil || state.RequestID != request.RequestID {
		if state != nil && state.Phase != api.TeamChangeCompleted &&
			(state.Fenced || state.Phase != api.TeamChangeFailed) {
			return false, ctrl.Result{}, r.teamChangeFailure(ctx, agent, state, "OperationInProgress", "a different Team change is already in progress")
		}
		state = stateForTeamChange(agent, request)
		agent.Status.TeamChange = state
		return false, ctrl.Result{Requeue: true}, r.updateTeamChangeStatus(ctx, agent, state)
	}
	accepted, known := state.OriginalTeamChangeRequest(agent.Spec.TeamChangeRequest)
	if !known || !api.TeamChangeRequestsEqual(accepted, request) {
		// A completed request may remain in status after its spec field was
		// cleared. Never let a same-ID replacement inherit that completion or
		// any fenced checkpoint; admission rejects it too, but reconciliation
		// must remain safe if an older object bypassed that boundary.
		return false, ctrl.Result{}, fmt.Errorf("Team change request ID conflicts with its recorded original payload")
	}
	// An accepted failure is retryable with the exact request. It is not safe
	// to let a different request replace a stopped source without revalidation.
	if state.Phase == api.TeamChangeFailed {
		if state.Fenced {
			// Suspension is the only lifecycle escape from a fenced failed
			// checkpoint.  It must still drive the common stop/quiescence path
			// and release the exact transition before ordinary suspension
			// reconciliation can proceed.
			if agent.Spec.Suspended {
				return r.pauseTeamChange(ctx, agent, state)
			}
			// A fenced operation may only be recovered by continuing its recorded
			// checkpoint.  Do not allow this Agent through ordinary reconciliation,
			// which could release the hold or unblock the old Gateway grant.
			return false, ctrl.Result{RequeueAfter: teamChangeWait}, nil
		}
	}
	if agent.Spec.Suspended && state.Phase != api.TeamChangeApplying && state.Phase != api.TeamChangeStarting &&
		state.Phase != api.TeamChangeCompleted && state.Phase != api.TeamChangeFailed {
		// A paused source has a durable Changing transition with SourceStopped.
		// It may continue its already-stopped reauthorization. A suspension that
		// interrupts a running transition instead takes the terminal pause path.
		// Validating has no transition yet: only a source already observed
		// stopped may acquire its stopped-source transition. A newly suspended
		// running Agent must immediately enter ordinary suspension instead of
		// attempting paused preflight with a still-live source.
		if state.Phase != api.TeamChangeValidating || !teamChangeStopped(agent) {
			if ticket, found, err := r.readTeamChangeTransition(ctx, agent, state); err != nil || !found || !ticket.Transition.SourceStopped {
				return r.pauseTeamChange(ctx, agent, state)
			}
		}
	}

	switch state.Phase {
	case api.TeamChangeValidating:
		if err := r.preflightTeamChange(ctx, agent, state); err != nil {
			return false, ctrl.Result{}, r.teamChangeFailure(ctx, agent, state, "PreflightFailed", err.Error())
		}
		state.Phase = api.TeamChangeStopping
		return false, ctrl.Result{Requeue: true}, r.updateTeamChangeStatus(ctx, agent, state)
	case api.TeamChangeStopping:
		// Persist the side-effect boundary first. A failed operation after this
		// point must retain the managed hold and retry the exact request.
		if !state.Fenced {
			state.Fenced = true
			return false, ctrl.Result{Requeue: true}, r.updateTeamChangeStatus(ctx, agent, state)
		}
		transition, err := r.teamChangeTransition(ctx, agent, state)
		if err != nil {
			return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "TransitionFenceFailed", err.Error())
		}
		if transition.Transition.Phase == sandboxcoordination.Reserved {
			transition, err = r.SandboxNativeCoordination.BeginTransitionStop(ctx, transition)
			if err != nil {
				return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "TransitionFenceFailed", err.Error())
			}
		}
		// Block the current key before route withdrawal and workload shutdown.
		// The policy-reprojection reason is operator-owned and is the only block
		// a successful target projection may later release.
		if r.AIGateway.configured() {
			if err := r.beginAIGatewayPolicyReprojection(ctx, agent); err != nil {
				return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "KeyFenceFailed", err.Error())
			}
		}
		if _, err := r.holdSandbox(ctx, agent); err != nil {
			return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "StopPending", err.Error())
		}
		reconciled, err := r.usesReconciledSandbox(ctx, agent)
		if err != nil {
			return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "StopPending", err.Error())
		}
		if !reconciled {
			return false, ctrl.Result{}, r.teamChangeFailure(ctx, agent, state, "StopUnsupported", "Team changes require common Sandbox execution")
		}
		// Releasing the native reservation verifies that no authorized writer
		// remains on HOME. It is intentionally independent of target inputs.
		if err := r.stopNativeSandboxReservation(ctx, agent); err != nil {
			return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "StopPending", err.Error())
		}
		if !teamChangeStopped(agent) {
			return false, ctrl.Result{RequeueAfter: teamChangeWait}, nil
		}
		if transition.Transition.Phase == sandboxcoordination.Stopping {
			if _, err := r.SandboxNativeCoordination.MarkTransitionStopped(ctx, transition); err != nil {
				return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "TransitionStopPending", err.Error())
			}
		}
		state.Stopped, state.Phase = true, api.TeamChangeCommitting
		return false, ctrl.Result{Requeue: true}, r.updateTeamChangeStatus(ctx, agent, state)
	case api.TeamChangeCommitting:
		if agent.Spec.Team == request.TargetTeam && agent.Spec.SandboxRef == state.TargetSandbox {
			// Skills are Team-scoped bootstrap content while HOME is an Agent
			// lifetime. Refresh the former only after the protected binding write
			// has committed, and keep the transition fenced while the replacement
			// publication is made durable. The helper is deliberately retryable:
			// a controller restart between any PVC/PV operation cannot resume the
			// workload with the source Team's managed skills.
			if err := r.refreshSandboxSkillsForTeamChange(ctx, agent); err != nil {
				return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "SkillsRefreshPending", err.Error())
			}
			state.Phase = api.TeamChangeApplying
			return false, ctrl.Result{Requeue: true}, r.updateTeamChangeStatus(ctx, agent, state)
		}
		// Re-read the pinned target immediately before the protected write. A
		// same-name configuration change cannot silently inherit the approval
		// made before the source was stopped.
		if err := r.preflightTeamChange(ctx, agent, state); err != nil {
			return false, ctrl.Result{}, r.teamChangeFailure(ctx, agent, state, "TargetChanged", err.Error())
		}
		if err := r.commitTeamChange(ctx, agent, state); err != nil {
			return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "CommitFailed", err.Error())
		}
		return false, ctrl.Result{Requeue: true}, nil
	case api.TeamChangeApplying:
		// Reconcile the replacement key while the hold remains in place. The
		// implementation updates the same UID-derived key, preserving spend and
		// reset time; read-back is part of reconcileAIGateway.
		if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
			return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "GatewayApplyFailed", err.Error())
		}
		transition, err := r.teamChangeTransition(ctx, agent, state)
		if err != nil {
			return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "TransitionStartFailed", err.Error())
		}
		if transition.Transition.Phase == sandboxcoordination.Changing {
			if _, err := r.SandboxNativeCoordination.StartTransitionTarget(ctx, transition); err != nil {
				return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "TransitionStartFailed", err.Error())
			}
		}
		state.Phase = api.TeamChangeStarting
		return true, ctrl.Result{Requeue: true}, r.updateTeamChangeStatus(ctx, agent, state)
	case api.TeamChangeStarting:
		if agent.Spec.Suspended {
			transition, found, err := r.readTeamChangeTransition(ctx, agent, state)
			if err != nil {
				return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "TransitionReleaseFailed", err.Error())
			}
			if found {
				// Applying lets the ordinary reconciler acquire the target
				// reservation. A suspend can therefore race its first target Pod;
				// do not release the transition until the common stop path has
				// observed quiescence and removed that reservation.
				// Starting normally releases the operator-owned reprojection block
				// after the target grant has been verified. Reassert it before the
				// potentially multi-pass stop, so the target key cannot remain
				// usable while its native reservation or Pod is still draining.
				if r.AIGateway.configured() {
					if err := r.beginAIGatewayPolicyReprojection(ctx, agent); err != nil {
						return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "PausePending", err.Error())
					}
				}
				if _, err := r.holdSandbox(ctx, agent); err != nil {
					return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "PausePending", err.Error())
				}
				if err := r.stopNativeSandboxReservation(ctx, agent); err != nil {
					return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "PausePending", err.Error())
				}
				if !teamChangeStopped(agent) {
					return false, ctrl.Result{RequeueAfter: teamChangeWait}, nil
				}
				if err := r.SandboxNativeCoordination.ReleaseStoppedTransition(ctx, transition); err != nil {
					return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "TransitionReleaseFailed", err.Error())
				}
			}
			// A source that was paused before this request remains paused after
			// its new binding is committed. A later suspension at this checkpoint
			// is also terminally safe: no target native reservation exists.
			state.Phase = api.TeamChangeCompleted
			return false, ctrl.Result{}, r.updateTeamChangeStatus(ctx, agent, state)
		}
		if isConditionTrue(agent.Status.Conditions, api.ConditionReady) &&
			!controllerutil.ContainsFinalizer(agent, sandbox.HoldFinalizer) {
			transition, found, err := r.readTeamChangeTransition(ctx, agent, state)
			if err != nil {
				return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "TransitionReleaseFailed", err.Error())
			}
			if found {
				if err := r.SandboxNativeCoordination.ReleaseTransition(ctx, transition); err != nil {
					return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "TransitionReleaseFailed", err.Error())
				}
			}
			// If the release was committed but its following status update was
			// interrupted, the absence of the exact durable ticket is positive
			// evidence that only the checkpoint write needs recovery. At this
			// point the target is already Ready and holds no Sandbox finalizer.
			state.Phase = api.TeamChangeCompleted
			return false, ctrl.Result{}, r.updateTeamChangeStatus(ctx, agent, state)
		}
		return true, ctrl.Result{RequeueAfter: teamChangeWait}, nil
	case api.TeamChangeFailed:
		// A failed preflight has not fenced or changed the source. Its request
		// remains visible for audit and replacement, while ordinary source
		// policy and Gateway reconciliation must continue.
		return true, ctrl.Result{}, nil
	case api.TeamChangeCompleted:
		return true, ctrl.Result{}, nil
	default:
		return false, ctrl.Result{}, r.teamChangeFailure(ctx, agent, state, "InvalidCheckpoint", "the Team change checkpoint has an unknown phase")
	}
}

// pauseTeamChange gives suspend precedence over a requested or in-progress
// Team handoff. It never resumes the source: the common stop path observes
// quiescence and releases the transition only after the Agent is suspended.
func (r *AgentReconciler) pauseTeamChange(ctx context.Context, agent *api.Agent, state *api.TeamChangeStatus) (bool, ctrl.Result, error) {
	if state == nil || !state.Fenced {
		return true, ctrl.Result{}, nil
	}
	if r.SandboxNativeCoordination == nil {
		return false, ctrl.Result{RequeueAfter: teamChangeWait}, fmt.Errorf("Team change pause requires native Sandbox coordination")
	}
	transition, found, err := r.readTeamChangeTransition(ctx, agent, state)
	if err != nil {
		return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "PausePending", err.Error())
	}
	if found && transition.Transition.Phase == sandboxcoordination.Reserved {
		if transition, err = r.SandboxNativeCoordination.BeginTransitionStop(ctx, transition); err != nil {
			return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "PausePending", err.Error())
		}
	}
	if r.AIGateway.configured() {
		if err := r.beginAIGatewayPolicyReprojection(ctx, agent); err != nil {
			return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "PausePending", err.Error())
		}
	}
	if _, err := r.holdSandbox(ctx, agent); err != nil {
		return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "PausePending", err.Error())
	}
	if err := r.stopNativeSandboxReservation(ctx, agent); err != nil {
		return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "PausePending", err.Error())
	}
	if !teamChangeStopped(agent) {
		return false, ctrl.Result{RequeueAfter: teamChangeWait}, nil
	}
	if found {
		if err := r.SandboxNativeCoordination.ReleaseStoppedTransition(ctx, transition); err != nil {
			return false, ctrl.Result{RequeueAfter: teamChangeWait}, r.teamChangeFailure(ctx, agent, state, "PausePending", err.Error())
		}
	}
	// Do not claim that the requested binding completed. A later resume needs a
	// new request ID, while ordinary suspension reconciliation can proceed now.
	state.Phase, state.Fenced, state.Stopped, state.TransitionID = api.TeamChangeFailed, false, false, ""
	state.FailureReason, state.FailureMessage = "Paused", "The Team change was stopped because the Agent is suspended."
	if err := r.updateTeamChangeStatus(ctx, agent, state); err != nil {
		return false, ctrl.Result{RequeueAfter: teamChangeWait}, err
	}
	return true, ctrl.Result{}, nil
}

func stateForTeamChange(agent *api.Agent, request *api.TeamChangeRequest) *api.TeamChangeStatus {
	return &api.TeamChangeStatus{TeamChangeRequest: cloneTeamChangeRequest(request), Phase: api.TeamChangeValidating,
		OriginalRequest: request.DeepCopy(),
		SourceTeam:      agent.Spec.Team, SourceSandbox: agent.Spec.SandboxRef,
		SourceGeneration: agent.Status.ObservedGeneration, ObservedGeneration: agent.Generation}
}

func validTeamChangeRequest(agent *api.Agent, request *api.TeamChangeRequest) error {
	if agent.UID == "" || request == nil || !api.ValidRestartRequestID(request.RequestID) || request.TargetTeam == "" ||
		request.ExpectedUID == "" || request.ExpectedUID != agent.UID || request.ExpectedResourceVersion == "" {
		return fmt.Errorf("Team change request must name the current Agent UID, resource version, request ID, and target Team")
	}
	if request.ModelAccess != nil && request.ModelAccess.MonthlyBudgetUSD != "" {
		// Team changes may alter requested model sources but must never become
		// an alternate path for the administrator-only budget override.
		return fmt.Errorf("a Team change request cannot set monthlyBudgetUSD")
	}
	return nil
}

func (r *AgentReconciler) preflightTeamChange(ctx context.Context, agent *api.Agent, state *api.TeamChangeStatus) (resultErr error) {
	if agent.MigrationPending() {
		return fmt.Errorf("a planned migration is in progress")
	}
	if r.APIReader == nil {
		return fmt.Errorf("Team changes require an authoritative API reader")
	}
	if r.SandboxNativeCoordination == nil {
		return fmt.Errorf("Team changes require native Sandbox coordination")
	}
	// Recover coordination before reading mutable target configuration. A
	// release may have committed while its following status write was lost,
	// and an acquire may have committed before its checkpoint was recorded.
	// Neither recovery may depend on the target still existing or matching.
	var preflightTicket *sandboxcoordination.TransitionTicket
	if state.Phase == api.TeamChangeValidating && !state.Stopped {
		ticket, found, err := r.SandboxNativeCoordination.ReadTransition(ctx, agent.UID)
		if err != nil {
			return fmt.Errorf("read preflight Team change transition: %w", err)
		}
		if !found && (state.TransitionID != "" || state.Fenced) {
			state.TransitionID, state.Fenced = "", false
			return fmt.Errorf("the preflight Team change transition was released")
		}
		if found {
			if ticket.Transition.RequestID != state.RequestID ||
				(state.TransitionID != "" && state.TransitionID != ticket.Transition.ID) ||
				(ticket.Transition.Phase != sandboxcoordination.Reserved &&
					!(ticket.Transition.Phase == sandboxcoordination.Changing && ticket.Transition.SourceStopped)) {
				return fmt.Errorf("preflight Team change transition differs from its checkpoint")
			}
			preflightTicket = &ticket
		}
		defer func() {
			if resultErr != nil && preflightTicket != nil {
				resultErr = r.releaseFailedTeamChangePreflight(ctx, agent, state, *preflightTicket, resultErr)
			}
		}()
		if preflightTicket != nil {
			if err := r.persistTeamChangeTransition(ctx, agent, state, *preflightTicket); err != nil {
				return err
			}
			if err := r.verifyTeamChangeTarget(ctx, preflightTicket.Transition.Target); err != nil {
				return err
			}
		}
	}
	reconciled, err := r.usesReconciledSandbox(ctx, agent)
	if err != nil {
		return err
	}
	if !reconciled {
		return fmt.Errorf("Team changes require common Sandbox execution")
	}
	var reservation sandboxcoordination.NativeTicket
	var source sandboxcoordination.NativeIntent
	sourceStopped := agent.Spec.Suspended
	if !state.Stopped {
		if sourceStopped {
			source, err = r.pausedTeamChangeSource(ctx, agent, state)
			if err != nil {
				return err
			}
		} else {
			var found bool
			reservation, found, err = r.SandboxNativeCoordination.ReadNative(ctx, agent.UID)
			if err != nil {
				return fmt.Errorf("read source Sandbox reservation: %w", err)
			}
			if !found || reservation.Reservation.Phase != sandboxcoordination.Reserved ||
				reservation.Reservation.Intent.Agent != (w.ObjectIdentity{Name: agent.Name, UID: agent.UID}) ||
				reservation.Reservation.Intent.SelectedSandbox() != agent.Spec.SandboxRef ||
				len(reservation.Reservation.Intent.Sources) == 0 || reservation.Reservation.Intent.Sources[0].Name != agent.Spec.Team {
				return fmt.Errorf("Team changes require the current common Sandbox source reservation")
			}
			source = reservation.Reservation.Intent
		}
	}
	target := &api.Team{}
	if err := r.APIReader.Get(ctx, types.NamespacedName{Name: state.TargetTeam}, target); err != nil {
		return fmt.Errorf("read target Team: %w", err)
	}
	selected := state.TargetSandbox
	if selected == "" {
		if slices.Contains(target.Spec.SandboxRefs, agent.Spec.SandboxRef) {
			selected = agent.Spec.SandboxRef
		} else {
			selected = target.Spec.DefaultSandboxRef
		}
	}
	if selected == "" || !slices.Contains(target.Spec.SandboxRefs, selected) {
		return fmt.Errorf("target Team does not permit the requested Sandbox")
	}
	box := &api.Sandbox{}
	if err := r.APIReader.Get(ctx, types.NamespacedName{Name: selected}, box); err != nil {
		return fmt.Errorf("read target Sandbox: %w", err)
	}
	policy := &api.AgentPolicy{}
	if target.Spec.AgentPolicyRef == "" {
		return fmt.Errorf("target Team has no AgentPolicy")
	}
	if err := r.APIReader.Get(ctx, types.NamespacedName{Name: target.Spec.AgentPolicyRef}, policy); err != nil {
		return fmt.Errorf("read target AgentPolicy: %w", err)
	}
	if (state.TargetTeamUID != "" && state.TargetTeamUID != target.UID) ||
		(state.TargetSandboxUID != "" && state.TargetSandboxUID != box.UID) ||
		(state.TargetPolicyUID != "" && state.TargetPolicyUID != policy.UID) {
		return fmt.Errorf("pinned target Team, Sandbox, or AgentPolicy was replaced")
	}
	if err := r.teamChangeHomeCompatible(ctx, agent, box); err != nil {
		return err
	}
	candidate := agent.DeepCopy()
	candidate.Spec.Team, candidate.Spec.SandboxRef = target.Name, selected
	candidate.Spec.Model = teamChangeModel(agent, state)
	candidate.Spec.ModelAccess = teamChangeModelAccess(agent, state)
	// resolvePolicy deliberately trusts admission-owned UID authorization. Use
	// a validation-only snapshot for the pinned target; it is never persisted
	// until the source has stopped and the controller submits its narrow
	// reauthorization commit.
	resources := sandbox.Resources{Sandboxes: map[string]*api.Sandbox{selected: box},
		AgentPolicies: map[string]*api.AgentPolicy{policy.Name: policy}}
	record, err := sandboxbinding.Snapshot(candidate, target, resources)
	if err != nil {
		return fmt.Errorf("snapshot target authorization: %w", err)
	}
	if candidate.Annotations == nil {
		candidate.Annotations = map[string]string{}
	}
	candidate.Annotations[sandboxbinding.AuthorizationAnnotation] = record
	resolution, err := r.resolvePolicy(ctx, candidate)
	if err != nil {
		return fmt.Errorf("validate target Team policy: %w", err)
	}
	revision := resolution.sandboxRevision + ":" + resolution.gatewayRevision
	if state.ExecutionRevision != "" && state.ExecutionRevision != revision {
		return fmt.Errorf("pinned target policy projection changed")
	}
	state.TargetSandbox, state.TargetTeamUID, state.TargetSandboxUID, state.TargetPolicyUID = selected, target.UID, box.UID, policy.UID
	state.ExecutionRevision = revision
	if state.TransitionID == "" {
		targetIntent, err := r.teamChangeTargetIntent(ctx, agent, state, source, target, box, policy)
		if err != nil {
			return err
		}
		var ticket sandboxcoordination.TransitionTicket
		if sourceStopped {
			ticket, err = r.SandboxNativeCoordination.AcquireStoppedTransition(ctx, state.RequestID, source, targetIntent)
		} else {
			ticket, err = r.SandboxNativeCoordination.AcquireTransition(ctx, state.RequestID, reservation, targetIntent)
		}
		if err != nil {
			return fmt.Errorf("acquire Team change transition: %w", err)
		}
		if state.Phase == api.TeamChangeValidating && !state.Stopped {
			preflightTicket = &ticket
		}
		// The transition is the fence between its CAS write and the target
		// digest recheck.  Persist its exact identity before that recheck, so a
		// failed cleanup or status write can be recovered after a restart.
		if err := r.persistTeamChangeTransition(ctx, agent, state, ticket); err != nil {
			return err
		}
		if err := r.verifyTeamChangeTarget(ctx, ticket.Transition.Target); err != nil {
			return err
		}
	} else {
		ticket, found, err := r.readTeamChangeTransition(ctx, agent, state)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("Team change transition differs from its checkpoint")
		}
		if err := r.verifyTeamChangeTarget(ctx, ticket.Transition.Target); err != nil {
			return err
		}
	}
	return nil
}

// persistTeamChangeTransition makes the coordination ticket recoverable before
// examining data which can change independently of the acquire CAS.  Fenced is
// deliberately set at this boundary: a failed cleanup must retry this durable
// checkpoint rather than returning the Agent to ordinary reconciliation.
func (r *AgentReconciler) persistTeamChangeTransition(ctx context.Context, agent *api.Agent, state *api.TeamChangeStatus, ticket sandboxcoordination.TransitionTicket) error {
	state.TransitionID = ticket.Transition.ID
	state.Fenced = true
	if err := r.updateTeamChangeStatus(ctx, agent, state); err != nil {
		return fmt.Errorf("persist acquired Team change transition: %w", err)
	}
	return nil
}

// releaseFailedTeamChangePreflight releases the exact acquired ticket.  A
// paused source has no reservation, so ReleaseTransition cannot prove its
// safety; ReleaseStoppedTransition verifies the durable paused/deleting Agent
// and the absence of both reservations instead.
func (r *AgentReconciler) releaseFailedTeamChangePreflight(ctx context.Context, agent *api.Agent, state *api.TeamChangeStatus, ticket sandboxcoordination.TransitionTicket, cause error) error {
	var err error
	if ticket.Transition.SourceStopped {
		err = r.SandboxNativeCoordination.ReleaseStoppedTransition(ctx, ticket)
	} else {
		err = r.SandboxNativeCoordination.ReleaseTransition(ctx, ticket)
	}
	if err != nil {
		return fmt.Errorf("release acquired Team change transition after target recheck: %w", err)
	}
	// The source has not crossed the stop boundary. If the following status
	// write is lost, the next Validating pass observes the absent ticket before
	// consulting mutable target inputs and persists the same unfenced failure.
	state.TransitionID, state.Fenced = "", false
	return fmt.Errorf("recheck acquired Team change target: %w", cause)
}

func (r *AgentReconciler) verifyTeamChangeTarget(ctx context.Context, intent sandboxcoordination.NativeIntent) error {
	for _, source := range intent.Sources {
		actual, err := r.executionSourceDigest(ctx, source, intent.SelectedSandbox(), false)
		if err != nil || actual != intent.SourceDigests[w.SourceKey(source)] {
			if err == nil {
				err = fmt.Errorf("target source digest changed: %s", w.SourceKey(source))
			}
			return err
		}
	}
	return nil
}

func (r *AgentReconciler) teamChangeTransition(ctx context.Context, agent *api.Agent, state *api.TeamChangeStatus) (sandboxcoordination.TransitionTicket, error) {
	ticket, found, err := r.readTeamChangeTransition(ctx, agent, state)
	if err != nil {
		return sandboxcoordination.TransitionTicket{}, err
	}
	if !found {
		return sandboxcoordination.TransitionTicket{}, fmt.Errorf("Team change transition differs from its checkpoint")
	}
	return ticket, nil
}

func (r *AgentReconciler) readTeamChangeTransition(ctx context.Context, agent *api.Agent, state *api.TeamChangeStatus) (sandboxcoordination.TransitionTicket, bool, error) {
	if r.SandboxNativeCoordination == nil || state.TransitionID == "" {
		return sandboxcoordination.TransitionTicket{}, false, fmt.Errorf("Team change transition is not recorded")
	}
	ticket, found, err := r.SandboxNativeCoordination.ReadTransition(ctx, agent.UID)
	if err != nil {
		return sandboxcoordination.TransitionTicket{}, false, fmt.Errorf("read Team change transition: %w", err)
	}
	if !found {
		return sandboxcoordination.TransitionTicket{}, false, nil
	}
	if ticket.Transition.ID != state.TransitionID || ticket.Transition.RequestID != state.RequestID {
		return sandboxcoordination.TransitionTicket{}, false, fmt.Errorf("Team change transition differs from its checkpoint")
	}
	if err := r.SandboxNativeCoordination.CheckTransition(ctx, ticket); err != nil {
		return sandboxcoordination.TransitionTicket{}, false, err
	}
	return ticket, true, nil
}

// releaseTeamChangeTransitionForDeletion is called only after the common
// Sandbox stop path has released its Native reservation following observed
// quiescence.  Read by Agent UID so deletion also recovers a transition whose
// acquire succeeded before the status checkpoint write.  A recorded checkpoint
// must still name this exact ticket; otherwise deletion retains its finalizer
// rather than releasing an unexpected handoff.
func (r *AgentReconciler) releaseTeamChangeTransitionForDeletion(ctx context.Context, agent *api.Agent) error {
	if r.SandboxNativeCoordination == nil || agent == nil {
		return nil
	}
	if err := r.SandboxNativeCoordination.FenceStoppedTransitionsForDeletion(ctx,
		w.ObjectIdentity{Name: agent.Name, UID: agent.UID}); err != nil {
		return fmt.Errorf("fence Team change transitions during deletion: %w", err)
	}
	ticket, found, err := r.SandboxNativeCoordination.ReadTransition(ctx, agent.UID)
	if err != nil {
		return fmt.Errorf("read Team change transition during deletion: %w", err)
	}
	if !found {
		return nil
	}
	if ticket.Transition.Target.Agent != (w.ObjectIdentity{Name: agent.Name, UID: agent.UID}) {
		return fmt.Errorf("Team change transition during deletion differs from the Agent")
	}
	if state := agent.Status.TeamChange; state != nil && state.TransitionID != "" &&
		(state.TransitionID != ticket.Transition.ID || state.RequestID != ticket.Transition.RequestID) {
		return fmt.Errorf("Team change transition during deletion differs from its checkpoint")
	}
	if err := r.SandboxNativeCoordination.ReleaseStoppedTransition(ctx, ticket); err != nil {
		return fmt.Errorf("release Team change transition during deletion: %w", err)
	}
	return nil
}

func (r *AgentReconciler) teamChangeTargetIntent(ctx context.Context, agent *api.Agent, state *api.TeamChangeStatus, source sandboxcoordination.NativeIntent, team *api.Team, box *api.Sandbox, policy *api.AgentPolicy) (sandboxcoordination.NativeIntent, error) {
	intent := source
	intent.AgentGeneration = agent.Generation
	intent.Binding = sandbox.Binding{AgentUID: agent.UID, TeamUID: team.UID, SandboxUID: box.UID, AgentPolicyUID: policy.UID}
	intent.Revision = state.ExecutionRevision
	intent.Sources = []w.SourceReference{sandboxSource(team, api.GroupVersion.String(), "Team"), sandboxSource(box, api.GroupVersion.String(), "Sandbox"), sandboxSource(policy, api.GroupVersion.String(), "AgentPolicy")}
	intent.SourceDigests = map[string]string{}
	for _, source := range intent.Sources {
		digest, err := r.executionSourceDigest(ctx, source, box.Name, true)
		if err != nil {
			return sandboxcoordination.NativeIntent{}, err
		}
		intent.SourceDigests[w.SourceKey(source)] = digest
	}
	return intent, intent.Validate()
}

// pausedTeamChangeSource reconstructs a source intent from the durable common
// expectation after the normal suspend path has released its Native ticket. It
// intentionally does not manufacture a source from Agent status: the retained
// HOME and workload lifetime must be proved by the controller-owned
// expectation, while the current admission-owned binding proves the source
// references still name the paused Agent's Team and Sandbox.
func (r *AgentReconciler) pausedTeamChangeSource(ctx context.Context, agent *api.Agent, state *api.TeamChangeStatus) (sandboxcoordination.NativeIntent, error) {
	if agent == nil || state == nil || !agent.Spec.Suspended || !teamChangeStopped(agent) {
		return sandboxcoordination.NativeIntent{}, fmt.Errorf("paused Team change requires an observed stopped Agent")
	}
	if r.APIReader == nil {
		return sandboxcoordination.NativeIntent{}, fmt.Errorf("paused Team change requires an authoritative API reader")
	}
	if _, found, err := r.SandboxNativeCoordination.ReadNative(ctx, agent.UID); err != nil {
		return sandboxcoordination.NativeIntent{}, fmt.Errorf("read paused source reservation: %w", err)
	} else if found {
		return sandboxcoordination.NativeIntent{}, fmt.Errorf("paused Team change requires the released source reservation")
	}
	namespace := &corev1.Namespace{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Name: instanceNamespace(agent.Name)}, namespace); err != nil {
		return sandboxcoordination.NativeIntent{}, fmt.Errorf("read paused source namespace: %w", err)
	}
	if !sandboxBuiltinOwned(namespace, agent) {
		return sandboxcoordination.NativeIntent{}, fmt.Errorf("paused Team change source namespace is not owned by the Agent")
	}
	record := &corev1.ConfigMap{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: r.OperatorNamespace, Name: w.PodExpectationName(namespace.Name, namespace.UID)}, record); err != nil {
		return sandboxcoordination.NativeIntent{}, fmt.Errorf("read paused source expectation: %w", err)
	}
	expected, err := w.ReadReconcileExpectation(record)
	if err != nil || expected.Agent != (w.ObjectIdentity{Name: agent.Name, UID: agent.UID}) ||
		expected.Namespace != (w.ObjectIdentity{Name: namespace.Name, UID: namespace.UID}) ||
		expected.AgentGeneration != state.SourceGeneration || expected.Home.UID == "" {
		return sandboxcoordination.NativeIntent{}, fmt.Errorf("paused Team change expectation differs from the recorded source lifetime")
	}
	workload := &appsv1.StatefulSet{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: namespace.Name, Name: instanceServiceName(agent.Name)}, workload); err != nil {
		return sandboxcoordination.NativeIntent{}, fmt.Errorf("read paused source workload: %w", err)
	}
	if !sandboxBuiltinOwned(workload, agent) || workload.Spec.Replicas == nil || *workload.Spec.Replicas != 0 {
		return sandboxcoordination.NativeIntent{}, fmt.Errorf("paused Team change source workload is not observed stopped")
	}
	intent, err := sandboxcoordination.ReconciledIntentFor(expected)
	if err != nil {
		return sandboxcoordination.NativeIntent{}, fmt.Errorf("read paused source intent: %w", err)
	}
	binding, err := sandboxbinding.AuthorizedBinding(agent)
	if err != nil || intent.Binding != binding || intent.SelectedSandbox() != agent.Spec.SandboxRef || intent.Sources[0].Name != agent.Spec.Team {
		return sandboxcoordination.NativeIntent{}, fmt.Errorf("paused Team change source binding is no longer authorized")
	}
	// The source captures the current effective digests. A Team can be edited
	// while its Agent is paused; the durable binding still pins object UIDs, and
	// capturing the current approved contents prevents a stale expectation from
	// acting as an authorization oracle for this new operation.
	for _, source := range intent.Sources {
		digest, err := r.executionSourceDigest(ctx, source, intent.SelectedSandbox(), false)
		if err != nil {
			return sandboxcoordination.NativeIntent{}, fmt.Errorf("read paused source authorization: %w", err)
		}
		intent.SourceDigests[w.SourceKey(source)] = digest
	}
	return intent, intent.Validate()
}

func (r *AgentReconciler) teamChangeHomeCompatible(ctx context.Context, agent *api.Agent, target *api.Sandbox) error {
	if agent.Spec.SandboxRef != "" && agent.Spec.SandboxRef != target.Name {
		source := &api.Sandbox{}
		if err := r.APIReader.Get(ctx, types.NamespacedName{Name: agent.Spec.SandboxRef}, source); err != nil {
			return fmt.Errorf("read current Sandbox: %w", err)
		}
		a, b := source.Spec.Storage.Home, target.Spec.Storage.Home
		if a.StorageClassName != b.StorageClassName || a.AccessMode != b.AccessMode || a.Size.Cmp(b.Size) != 0 {
			return fmt.Errorf("target Sandbox HOME profile is incompatible with the retained HOME")
		}
	}
	if r.APIReader == nil {
		return fmt.Errorf("read retained HOME: authoritative API reader is required")
	}
	namespaceRecord, homeRecord, err := r.teamChangeHomeClaim(ctx, agent)
	if err != nil {
		return err
	}
	namespace := &corev1.Namespace{}
	if err := r.APIReader.Get(ctx, types.NamespacedName{Name: instanceNamespace(agent.Name)}, namespace); err != nil {
		return fmt.Errorf("read retained HOME namespace: %w", err)
	}
	if namespace.UID != namespaceRecord.UID || !sandboxBuiltinOwned(namespace, agent) {
		return fmt.Errorf("retained HOME namespace differs from the recorded Agent lifetime")
	}
	home := &corev1.PersistentVolumeClaim{}
	if err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: namespace.Name, Name: homeRecord.Name}, home); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("retained HOME PVC %q does not exist", homeRecord.Name)
		}
		return fmt.Errorf("read retained HOME PVC: %w", err)
	}
	if home.UID != homeRecord.UID || home.Annotations[w.AgentUIDAnnotation] != string(agent.UID) || !teamChangeHomeOwnersMatch(home, agent) {
		return fmt.Errorf("retained HOME PVC is not owned by the current Agent UID")
	}
	wanted := target.Spec.Storage.Home
	size := home.Spec.Resources.Requests[corev1.ResourceStorage]
	if ptr.Deref(home.Spec.StorageClassName, "") != wanted.StorageClassName ||
		!slices.Equal(home.Spec.AccessModes, []corev1.PersistentVolumeAccessMode{wanted.AccessMode}) ||
		size.Cmp(wanted.Size) != 0 || ptr.Deref(home.Spec.VolumeMode, corev1.PersistentVolumeFilesystem) != corev1.PersistentVolumeFilesystem {
		return fmt.Errorf("target Sandbox HOME profile is incompatible with the retained HOME PVC")
	}
	return nil
}

func (r *AgentReconciler) teamChangeHomeClaim(ctx context.Context, agent *api.Agent) (sandboxBootstrapObject, sandboxBootstrapObject, error) {
	_, receipt, err := r.readSandboxBootstrap(ctx, agent)
	if err != nil {
		return sandboxBootstrapObject{}, sandboxBootstrapObject{}, fmt.Errorf("read retained HOME bootstrap receipt: %w", err)
	}
	if receipt == nil || !receipt.Reconciled || len(receipt.Objects) < 4 || receipt.Objects[0].Kind != "Namespace" ||
		receipt.Objects[0].Name != receipt.Namespace || receipt.Objects[0].UID == "" ||
		receipt.Objects[3].Kind != "PersistentVolumeClaim" || receipt.Objects[3].Name == "" || receipt.Objects[3].UID == "" {
		return sandboxBootstrapObject{}, sandboxBootstrapObject{}, fmt.Errorf("retained HOME bootstrap receipt is incomplete")
	}
	return receipt.Objects[0], receipt.Objects[3], nil
}

func teamChangeHomeOwnersMatch(home *corev1.PersistentVolumeClaim, agent *api.Agent) bool {
	if home == nil || agent == nil || home.DeletionTimestamp != nil || home.Status.Phase == corev1.ClaimLost {
		return false
	}
	return slices.EqualFunc(home.OwnerReferences, []metav1.OwnerReference{w.AgentOwner(agent.Name, agent.UID)}, func(a, b metav1.OwnerReference) bool {
		return a.APIVersion == b.APIVersion && a.Kind == b.Kind && a.Name == b.Name && a.UID == b.UID &&
			a.Controller != nil && b.Controller != nil && *a.Controller == *b.Controller &&
			a.BlockOwnerDeletion != nil && b.BlockOwnerDeletion != nil && *a.BlockOwnerDeletion == *b.BlockOwnerDeletion
	})
}

func (r *AgentReconciler) commitTeamChange(ctx context.Context, agent *api.Agent, state *api.TeamChangeStatus) error {
	if !state.Stopped || state.TargetTeamUID == "" || state.TargetSandboxUID == "" || state.TargetPolicyUID == "" {
		return fmt.Errorf("Team change has no stopped source or pinned target authorization")
	}
	binding := sandbox.Binding{AgentUID: agent.UID, TeamUID: state.TargetTeamUID, SandboxUID: state.TargetSandboxUID, AgentPolicyUID: state.TargetPolicyUID}
	reauthorization, err := sandboxbinding.FormatReauthorization(state.RequestID, binding)
	if err != nil {
		return err
	}
	before := agent.DeepCopy()
	agent.Spec.Team, agent.Spec.SandboxRef = state.TargetTeam, state.TargetSandbox
	agent.Spec.Model, agent.Spec.ModelAccess = teamChangeModel(agent, state), teamChangeModelAccess(agent, state)
	if agent.Annotations == nil {
		agent.Annotations = map[string]string{}
	}
	agent.Annotations[sandboxbinding.ReauthorizeAnnotation] = reauthorization
	if err := r.Patch(ctx, agent, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("commit protected Team binding: %w", err)
	}
	return nil
}

// updateTeamChangeStatus retains the operation pointer across status patches.
// updateStatus refreshes the Agent with a deep-copied server response; later
// checkpoints in this reconcile must still update the same operation object.
func (r *AgentReconciler) updateTeamChangeStatus(ctx context.Context, agent *api.Agent, state *api.TeamChangeStatus) error {
	agent.Status.TeamChange = state
	err := r.updateStatus(ctx, agent)
	agent.Status.TeamChange = state
	return err
}

func (r *AgentReconciler) teamChangeFailure(ctx context.Context, agent *api.Agent, state *api.TeamChangeStatus, reason, message string) error {
	if state == nil {
		return fmt.Errorf("%s: %s", reason, message)
	}
	if state.Fenced {
		// Retain the current phase so the exact durable request retries after a
		// transient stop or Gateway failure. A source that has crossed this
		// boundary is never returned to ordinary reconciliation automatically.
		state.FailureReason, state.FailureMessage = reason, message
		return r.updateTeamChangeStatus(ctx, agent, state)
	}
	state.Phase, state.FailureReason, state.FailureMessage = api.TeamChangeFailed, reason, message
	return r.updateTeamChangeStatus(ctx, agent, state)
}

func teamChangeStopped(agent *api.Agent) bool {
	return agent.Status.AccessURL == "" && !isConditionTrue(agent.Status.Conditions, api.ConditionReady)
}

func cloneModelAccess(in *api.ModelAccessSpec) *api.ModelAccessSpec {
	if in == nil {
		return nil
	}
	out := *in
	if in.SharedModels != nil {
		value := *in.SharedModels
		out.SharedModels = &value
	}
	if in.GitHubCopilot != nil {
		value := *in.GitHubCopilot
		out.GitHubCopilot = &value
	}
	return &out
}

func teamChangeModel(agent *api.Agent, state *api.TeamChangeStatus) string {
	if state.Model == "" {
		return agent.Spec.Model
	}
	return state.Model
}

func teamChangeModelAccess(agent *api.Agent, state *api.TeamChangeStatus) *api.ModelAccessSpec {
	if state.ModelAccess == nil {
		return cloneModelAccess(agent.Spec.ModelAccess)
	}
	access := cloneModelAccess(state.ModelAccess)
	if agent.Spec.ModelAccess != nil {
		access.MonthlyBudgetUSD = agent.Spec.ModelAccess.MonthlyBudgetUSD
	}
	return access
}

func cloneTeamChangeRequest(in *api.TeamChangeRequest) api.TeamChangeRequest {
	out := *in
	out.ModelAccess = cloneModelAccess(in.ModelAccess)
	return out
}
