// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxcoordination

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Transition holds both selections while one Agent changes Team. It is not a
// workload fence: the controller must separately observe source quiescence
// before releasing the source NativeTicket and acknowledging the stop.
type Transition struct {
	ID        string            `json:"id"`
	RequestID string            `json:"requestID"`
	Source    NativeReservation `json:"source"`
	Target    NativeIntent      `json:"target"`
	Phase     Phase             `json:"phase"`
	// SourceStopped identifies a paused lifetime whose source selection was
	// captured after the controller observed quiescence, without a live ticket.
	SourceStopped bool `json:"sourceStopped,omitempty"`
}

type TransitionTicket struct {
	AuthorityUID types.UID
	Transition   Transition
}

func (t TransitionTicket) DeepCopy() TransitionTicket {
	t.Transition.Source = (NativeTicket{Reservation: t.Transition.Source}).DeepCopy().Reservation
	t.Transition.Target = (NativeTicket{Reservation: NativeReservation{Intent: t.Transition.Target}}).DeepCopy().Reservation.Intent
	return t
}

func (t Transition) validate() error {
	if !literal(t.ID, 128) || !api.ValidRestartRequestID(t.RequestID) || !literal(t.Source.ID, 128) ||
		t.Source.Phase != Reserved || !slices.Contains([]Phase{Reserved, Stopping, Changing, Starting}, t.Phase) ||
		t.Source.Intent.Validate() != nil || t.Target.Validate() != nil ||
		!t.Source.Intent.Reconciled || !t.Target.Reconciled ||
		t.Source.Intent.Agent != t.Target.Agent || t.Source.Intent.Namespace != t.Target.Namespace ||
		t.Source.Intent.Home != t.Target.Home || t.Source.Intent.Workload != t.Target.Workload ||
		t.Target.AgentGeneration < t.Source.Intent.AgentGeneration ||
		(t.SourceStopped && t.Phase != Changing && t.Phase != Starting) {
		return fmt.Errorf("%w: transition requires one retained common lifetime and exact source/target selections", ErrUnavailable)
	}
	return nil
}

// AcquireStoppedTransition captures a paused common lifetime after the caller
// has observed quiescence. It does not infer a stopped workload from a missing
// reservation. The durable pause and absence of native reservations are checked
// again here; source writers and target starts share the authority object's CAS.
func (s Store) AcquireStoppedTransition(ctx context.Context, requestID string, source, target NativeIntent) (TransitionTicket, error) {
	proposed := Transition{ID: rand.Text(), RequestID: requestID,
		Source: NativeReservation{ID: rand.Text(), Intent: source, Phase: Reserved},
		Target: target, Phase: Changing, SourceStopped: true}
	if err := proposed.validate(); err != nil {
		return TransitionTicket{}, err
	}
	object, state, err := s.read(ctx)
	if err != nil {
		return TransitionTicket{}, err
	}
	if target.Namespace.Name == s.Namespace {
		return TransitionTicket{}, ErrConflict
	}
	current := &api.Agent{}
	if err := s.APIReader.Get(ctx, client.ObjectKey{Name: source.Agent.Name}, current); err != nil {
		return TransitionTicket{}, err
	}
	if current.UID != source.Agent.UID || !current.Spec.Suspended || !current.DeletionTimestamp.IsZero() ||
		current.Spec.Team != source.Sources[0].Name || current.Spec.SandboxRef != source.SelectedSandbox() {
		return TransitionTicket{}, ErrConflict
	}
	for _, existing := range state.Transitions {
		if existing.Target.Agent.UID != target.Agent.UID {
			continue
		}
		if existing.SourceStopped && existing.RequestID == requestID &&
			equality.Semantic.DeepEqual(existing.Source.Intent, source) && equality.Semantic.DeepEqual(existing.Target, target) {
			return (TransitionTicket{object.UID, existing}).DeepCopy(), nil
		}
		return TransitionTicket{}, ErrConflict
	}
	for _, native := range state.NativeReservations {
		if native.Intent.Agent.UID == source.Agent.UID {
			return TransitionTicket{}, ErrConflict
		}
	}
	state.Transitions = append(state.Transitions, proposed)
	if err := s.write(ctx, object, state); err != nil {
		return TransitionTicket{}, err
	}
	ticket := (TransitionTicket{object.UID, proposed}).DeepCopy()
	return ticket, s.CheckTransition(ctx, ticket)
}

// AcquireTransition uses the same CAS as source writers and native starts.
// After acquisition the caller must re-read target sources, comparing their
// effective digests with Target before stopping the source.
func (s Store) AcquireTransition(ctx context.Context, requestID string, source NativeTicket, target NativeIntent) (TransitionTicket, error) {
	proposed := Transition{ID: rand.Text(), RequestID: requestID, Source: source.Reservation, Target: target, Phase: Reserved}
	if err := proposed.validate(); err != nil {
		return TransitionTicket{}, err
	}
	object, state, err := s.read(ctx)
	if err != nil {
		return TransitionTicket{}, err
	}
	if object.UID != source.AuthorityUID || target.Namespace.Name == s.Namespace {
		return TransitionTicket{}, ErrConflict
	}
	for _, existing := range state.Transitions {
		if existing.Target.Agent.UID == target.Agent.UID {
			if existing.RequestID == requestID && equality.Semantic.DeepEqual(existing.Source, source.Reservation) &&
				equality.Semantic.DeepEqual(existing.Target, target) {
				return (TransitionTicket{object.UID, existing}).DeepCopy(), nil
			}
			return TransitionTicket{}, ErrConflict
		}
	}
	if !slices.ContainsFunc(state.NativeReservations, func(n NativeReservation) bool { return equality.Semantic.DeepEqual(n, source.Reservation) }) {
		return TransitionTicket{}, ErrConflict
	}
	state.Transitions = append(state.Transitions, proposed)
	if err := s.write(ctx, object, state); err != nil {
		return TransitionTicket{}, err
	}
	ticket := (TransitionTicket{object.UID, proposed}).DeepCopy()
	return ticket, s.CheckTransition(ctx, ticket)
}

func (s Store) ReadTransition(ctx context.Context, agentUID types.UID) (TransitionTicket, bool, error) {
	if !literal(string(agentUID), 128) {
		return TransitionTicket{}, false, ErrUnavailable
	}
	object, state, err := s.read(ctx)
	if err != nil {
		return TransitionTicket{}, false, err
	}
	for _, t := range state.Transitions {
		if t.Target.Agent.UID == agentUID {
			return (TransitionTicket{object.UID, t}).DeepCopy(), true, nil
		}
	}
	return TransitionTicket{}, false, nil
}

func (s Store) CheckTransition(ctx context.Context, ticket TransitionTicket) error {
	current, found, err := s.ReadTransition(ctx, ticket.Transition.Target.Agent.UID)
	if err != nil {
		return err
	}
	if !found || !equality.Semantic.DeepEqual(current, ticket) {
		return ErrConflict
	}
	return ctx.Err()
}

func (s Store) BeginTransitionStop(ctx context.Context, ticket TransitionTicket) (TransitionTicket, error) {
	return s.advanceTransition(ctx, ticket, Reserved, Stopping)
}

// MarkTransitionStopped requires that the trusted stop path has released the
// old NativeTicket after observing quiescence. It never releases that ticket.
func (s Store) MarkTransitionStopped(ctx context.Context, ticket TransitionTicket) (TransitionTicket, error) {
	return s.advanceTransition(ctx, ticket, Stopping, Changing)
}

// StartTransitionTarget follows a protected Agent binding commit and complete
// target projection. Only this selection may subsequently acquire/start.
func (s Store) StartTransitionTarget(ctx context.Context, ticket TransitionTicket) (TransitionTicket, error) {
	return s.advanceTransition(ctx, ticket, Changing, Starting)
}

func (s Store) advanceTransition(ctx context.Context, ticket TransitionTicket, before, after Phase) (TransitionTicket, error) {
	if ticket.Transition.Phase == after {
		return ticket, s.CheckTransition(ctx, ticket)
	}
	if ticket.Transition.Phase != before {
		return TransitionTicket{}, ErrConflict
	}
	object, state, err := s.read(ctx)
	if err != nil {
		return TransitionTicket{}, err
	}
	if object.UID != ticket.AuthorityUID {
		return TransitionTicket{}, ErrConflict
	}
	for i, t := range state.Transitions {
		if !equality.Semantic.DeepEqual(t, ticket.Transition) {
			continue
		}
		if after == Changing || after == Starting {
			for _, n := range state.NativeReservations {
				if n.Intent.Agent.UID == t.Target.Agent.UID {
					return TransitionTicket{}, ErrConflict
				}
			}
		}
		state.Transitions[i].Phase = after
		if err := s.write(ctx, object, state); err != nil {
			return TransitionTicket{}, err
		}
		ticket.Transition.Phase = after
		return ticket.DeepCopy(), s.CheckTransition(ctx, ticket)
	}
	return TransitionTicket{}, ErrConflict
}

// ReleaseTransition transfers protection to a matching target reservation, or
// aborts an unfenced preflight with the exact source still reserved.
func (s Store) ReleaseTransition(ctx context.Context, ticket TransitionTicket) error {
	return s.releaseTransition(ctx, ticket, false)
}

// ReleaseStoppedTransition is used only after observed quiescence on pause or
// deletion. It verifies the durable intent and absence of either reservation;
// absence of a reservation alone is not proof of HOME writer quiescence.
func (s Store) ReleaseStoppedTransition(ctx context.Context, ticket TransitionTicket) error {
	return s.releaseTransition(ctx, ticket, true)
}

// FenceStoppedTransitionsForDeletion prevents a paused acquisition that read
// the Agent before deletion from committing after finalizer cleanup. Call this
// after observing quiescence and releasing the native reservation, then read
// and release any remaining transition before removing the Agent finalizer.
//
// Acquisition reads the authority before reading the Agent. A successful CAS
// here invalidates every earlier authority snapshot; a later acquisition must
// observe the already-persisted deletion timestamp. No per-Agent tombstones are
// needed. The nonce makes the CAS a real write even when the record is unchanged.
func (s Store) FenceStoppedTransitionsForDeletion(ctx context.Context, agent w.ObjectIdentity) error {
	if !literal(agent.Name, 253) || !literal(string(agent.UID), 128) || s.APIReader == nil {
		return ErrUnavailable
	}
	current := &api.Agent{}
	if err := s.APIReader.Get(ctx, client.ObjectKey{Name: agent.Name}, current); err != nil {
		return err
	}
	if current.UID != agent.UID || current.DeletionTimestamp.IsZero() {
		return ErrConflict
	}
	object, state, err := s.read(ctx)
	if err != nil {
		return err
	}
	for _, native := range state.NativeReservations {
		if native.Intent.Agent.UID == agent.UID {
			return ErrConflict
		}
	}
	if object.Annotations == nil {
		object.Annotations = map[string]string{}
	}
	object.Annotations["sandbox.cetirun.microsoft.com/deletion-barrier"] = rand.Text()
	return s.write(ctx, object, state)
}

func (s Store) releaseTransition(ctx context.Context, ticket TransitionTicket, stopped bool) error {
	object, state, err := s.read(ctx)
	if err != nil {
		return err
	}
	if object.UID != ticket.AuthorityUID {
		return ErrConflict
	}
	for i, t := range state.Transitions {
		if !equality.Semantic.DeepEqual(t, ticket.Transition) {
			continue
		}
		allowed, foundNative := false, false
		for _, n := range state.NativeReservations {
			if n.Intent.Agent.UID != t.Target.Agent.UID {
				continue
			}
			foundNative = true
			allowed = (t.Phase == Reserved && equality.Semantic.DeepEqual(n, t.Source)) ||
				(t.Phase == Starting && n.Phase == Reserved && sameReconciledSources(t.Target, n.Intent))
		}
		if stopped {
			current := &api.Agent{}
			if err := s.APIReader.Get(ctx, client.ObjectKey{Name: t.Target.Agent.Name}, current); err != nil {
				return err
			}
			allowed = !foundNative && current.UID == t.Target.Agent.UID &&
				(current.Spec.Suspended || !current.DeletionTimestamp.IsZero())
		}
		if !allowed {
			return ErrConflict
		}
		state.Transitions = slices.Delete(state.Transitions, i, i+1)
		return s.write(ctx, object, state)
	}
	return ErrConflict
}

func transitionChangeAffects(t Transition, change Change) bool {
	return nativeChangeAffects(t.Source.Intent, change) || nativeChangeAffects(t.Target, change)
}

func transitionAllowsNative(t Transition, intent NativeIntent, starting bool) error {
	if intent.Agent.UID == t.Target.Agent.UID {
		if !starting {
			// The old reservation may be stopping until its quiescence receipt.
			if (t.Phase == Reserved || t.Phase == Stopping) && sameReconciledSources(t.Source.Intent, intent) {
				return nil
			}
		}
		if (t.Phase == Reserved && sameReconciledSources(t.Source.Intent, intent)) ||
			(t.Phase == Starting && sameReconciledSources(t.Target, intent)) {
			return nil
		}
		return ErrConflict
	}
	if err := compatibleNativeIntents(t.Source.Intent, intent); err != nil {
		return err
	}
	return compatibleNativeIntents(t.Target, intent)
}

func validateTransitions(state record, ids map[string]bool) error {
	agents := map[types.UID]bool{}
	for i, t := range state.Transitions {
		if err := t.validate(); err != nil {
			return err
		}
		if ids[t.ID] || agents[t.Target.Agent.UID] {
			return fmt.Errorf("%w: duplicate transition identity or Agent", ErrUnavailable)
		}
		ids[t.ID], agents[t.Target.Agent.UID] = true, true
		for _, n := range state.NativeReservations {
			if err := transitionAllowsNative(t, n.Intent, false); err != nil {
				return err
			}
		}
		for _, e := range state.Executions {
			for _, selected := range []NativeIntent{t.Source.Intent, t.Target} {
				if err := compatibleNativeExecution(selected, e.Intent); err != nil {
					return err
				}
			}
		}
		for _, other := range state.Transitions[:i] {
			for _, selected := range []NativeIntent{t.Source.Intent, t.Target} {
				if err := transitionAllowsNative(other, selected, false); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
