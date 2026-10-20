// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxcoordination

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	strictjson "sigs.k8s.io/json"
)

func TestNativeCoordinationPreviousReaderWireContract(t *testing.T) {
	// Freeze the pre-reconciliation intent shape rather than decoding with the
	// current NativeIntent, which would hide strict-reader version skew.
	var previous struct {
		Version            int         `json:"version"`
		Executions         []Execution `json:"executions"`
		Changes            []Change    `json:"changes"`
		NativeReservations []struct {
			ID     string `json:"id"`
			Phase  Phase  `json:"phase"`
			Intent struct {
				Agent           w.ObjectIdentity    `json:"agent"`
				AgentGeneration int64               `json:"agentGeneration"`
				Namespace       w.ObjectIdentity    `json:"namespace"`
				Home            w.ObjectIdentity    `json:"home"`
				Workload        w.ObjectIdentity    `json:"workload"`
				Binding         sandbox.Binding     `json:"binding"`
				Revision        string              `json:"revision"`
				Sources         []w.SourceReference `json:"sources"`
				SourceDigests   map[string]string   `json:"sourceDigests"`
			} `json:"intent"`
		} `json:"nativeReservations,omitempty"`
	}
	ctx := context.Background()
	store, _ := fixture(t)
	existing := acquireNative(t, store, testNativeIntent("existing"))
	rolling := acquireNative(t, store, testNativeIntent("rolling"))
	object, _, err := store.read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	problems, err := strictjson.UnmarshalStrict([]byte(object.Data[DataKey]), &previous)
	if err != nil || len(problems) != 0 || previous.Version != 1 || len(previous.NativeReservations) != 2 {
		t.Fatalf("single-start writers broke the previous reader: %v, %v", err, problems)
	}
	for _, reservation := range previous.NativeReservations {
		if !identity(reservation.Intent.Workload) {
			t.Fatal("single-start writer omitted the previous reader's workload identity")
		}
	}
	stopping, err := store.BeginNativeStop(ctx, rolling)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseNative(ctx, stopping); err != nil {
		t.Fatal(err)
	}
	reconciled := testNativeIntent("upgraded")
	reconciled.Reconciled, reconciled.Workload.UID = true, ""
	acquireNative(t, store, reconciled)
	object, _, err = store.read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	problems, err = strictjson.UnmarshalStrict([]byte(object.Data[DataKey]), &previous)
	if err != nil || len(problems) != 1 || !strings.Contains(problems[0].Error(), `unknown field "nativeReservations[1].intent.reconciled"`) {
		t.Fatalf("fixture no longer exercises the previous strict reader boundary: %v, %v", err, problems)
	}
	if identity(previous.NativeReservations[1].Intent.Workload) {
		t.Fatal("reconciled intent claimed a previous-format physical workload identity")
	}
	for _, intent := range []NativeIntent{existing.Reservation.Intent, reconciled} {
		if err := store.AuthorizeNativeStart(ctx, intent); err != nil {
			t.Fatal("upgraded reader could not authorize mixed lifetime formats:", err)
		}
	}
}

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
