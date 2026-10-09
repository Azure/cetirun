// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxcoordination

import (
	"context"
	"fmt"

	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
)

// ChangeOutcome is a terminal receipt from a participating mutation adapter.
// NoPendingSubmissions includes delayed/in-flight submissions under this
// operation, not merely absence of a currently visible API change.
type ChangeOutcome struct {
	AuthorityUID         string
	OperationID          string
	Before               w.SourceReference
	After                w.SourceReference
	DesiredDigest        string
	Applied              bool
	Aborted              bool
	NoPendingSubmissions bool
	// Removed proves the Before UID is absent. After must be empty, and any
	// delayed submission must be unable to affect a same-name replacement.
	Removed bool
}

type ChangeAdapter interface {
	Submit(context.Context, ChangeTicket, func(context.Context) error) error
	ObserveChange(context.Context, ChangeTicket) (*ChangeOutcome, error)
}

// ReconcileChange never resubmits a recovered Changing ticket, even after a
// timeout or an unchanged API read. Direct, uncoordinated source writes remain
// outside this integration and must be prohibited before execution enablement.
func (s Store) ReconcileChange(ctx context.Context, ticket ChangeTicket, adapter ChangeAdapter) (ChangeTicket, error) {
	ticket = ticket.DeepCopy()
	if adapter == nil {
		return ticket, fmt.Errorf("%w: a coordinated mutation adapter is required", ErrUnavailable)
	}
	if err := s.CheckChange(ctx, ticket); err != nil {
		return ticket, err
	}
	if ticket.Change.Phase == Reserved {
		next, err := s.BeginChange(ctx, ticket)
		if err != nil {
			return ticket, err
		}
		ticket = next
		guard := func(ctx context.Context) error { return s.CheckChange(ctx, ticket) }
		if err := adapter.Submit(ctx, ticket.DeepCopy(), guard); err != nil {
			return ticket, err
		}
	}
	if ticket.Change.Phase != Changing {
		return ticket, ErrConflict
	}
	outcome, err := adapter.ObserveChange(ctx, ticket.DeepCopy())
	if err != nil {
		return ticket, err
	}
	if outcome == nil || outcome.AuthorityUID != string(ticket.AuthorityUID) ||
		outcome.OperationID != ticket.Change.ID || outcome.Before != ticket.Change.Before ||
		outcome.DesiredDigest != ticket.Change.DesiredDigest || !outcome.NoPendingSubmissions ||
		outcome.Applied == outcome.Aborted || !validChangeAfter(outcome) {
		return ticket, fmt.Errorf("%w: source change outcome is unknown or mismatched", ErrUnavailable)
	}
	return ticket, s.ReleaseChange(ctx, ticket)
}

func validChangeAfter(outcome *ChangeOutcome) bool {
	if outcome.Removed {
		return outcome.Applied && !outcome.Aborted && outcome.After == (w.SourceReference{})
	}
	return validSource(outcome.After) && sourceKey(outcome.After) == sourceKey(outcome.Before) &&
		outcome.After.UID == outcome.Before.UID &&
		((outcome.Applied && outcome.After != outcome.Before) || (outcome.Aborted && outcome.After == outcome.Before))
}
