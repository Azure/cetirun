// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxcoordination

import (
	"context"
	"errors"
	"strings"
	"testing"

	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
)

func TestReconciledReservationsRefreshOnlyTheSameSources(t *testing.T) {
	ctx := context.Background()
	store, _ := fixture(t)
	initial := testNativeIntent("one")
	initial.Reconciled, initial.Workload.UID = true, ""
	first := acquireNative(t, store, initial)
	peer := testNativeIntent("two")
	peer.Reconciled, peer.Workload.UID = true, ""
	acquireNative(t, store, peer)
	next := first.DeepCopy().Reservation.Intent
	next.AgentGeneration++
	next.Revision = "updated-render"
	next.Sources[0].ResourceVersion = "new-status-version"
	updated := acquireNative(t, store, next)
	if updated.Reservation.ID != first.Reservation.ID {
		t.Fatal("an ordinary template update replaced the HOME source reservation")
	}
	if err := store.AuthorizeNativeStart(ctx, initial); !errors.Is(err, ErrConflict) {
		t.Fatal("the prior desired generation still authorized new Pods")
	}
	if err := store.AuthorizeNativeStart(ctx, next); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"home", "source", "generation", "legacy"} {
		desired := updated.DeepCopy().Reservation.Intent
		switch change {
		case "home":
			desired.Home.UID = "another-home"
		case "source":
			desired.SourceDigests[w.SourceKey(desired.Sources[0])] = strings.Repeat("f", 64)
		case "generation":
			desired.AgentGeneration--
		case "legacy":
			desired.Reconciled = false
			desired.Workload.UID = "a-physical-workload"
		}
		if _, err := store.AcquireNative(ctx, desired); !errors.Is(err, ErrConflict) {
			t.Fatalf("%s crossed the same-source refresh boundary: %v", change, err)
		}
	}
	stopping, err := store.BeginNativeStop(ctx, updated)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireNative(ctx, next); !errors.Is(err, ErrConflict) {
		t.Fatal("reconciliation reopened a stopping reservation")
	}
	if err := store.ReleaseNative(ctx, stopping); err != nil {
		t.Fatal(err)
	}
	if err := store.AuthorizeNativeStart(ctx, peer); err != nil {
		t.Fatal("stopping one HOME interrupted an independent peer:", err)
	}
}
