// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxcoordination

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func transitionFixture(t *testing.T) (Store, client.WithWatch, NativeTicket, NativeIntent) {
	t.Helper()
	store, storage := fixture(t)
	if err := api.AddToScheme(storage.Scheme()); err != nil {
		t.Fatal(err)
	}
	old := testNativeIntent("moving")
	old.Reconciled, old.Workload.UID = true, ""
	source := acquireNative(t, store, old)
	target := source.DeepCopy().Reservation.Intent
	target.Binding.TeamUID, target.Binding.AgentPolicyUID = "target-team-uid", "target-policy-uid"
	target.Sources[0] = sourceReference("Team", "target-team", "target-team-uid")
	target.Sources[2] = sourceReference("AgentPolicy", "target-policy", "target-policy-uid")
	target.SourceDigests = map[string]string{}
	for _, ref := range target.Sources {
		target.SourceDigests[w.SourceKey(ref)] = strings.Repeat("e", 64)
	}
	target.AgentGeneration++
	return store, storage, source, target
}

func sourceReference(kind, name, uid string) w.SourceReference {
	return source(api.GroupVersion.String(), kind, "", name, uid)
}

func requireTransition(t *testing.T, store Store, source NativeTicket, target NativeIntent) TransitionTicket {
	t.Helper()
	ticket, err := store.AcquireTransition(context.Background(), "team-change-test-0001", source, target)
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func stopTransitionSource(t *testing.T, store Store, ticket TransitionTicket, source NativeTicket) TransitionTicket {
	t.Helper()
	ctx := context.Background()
	ticket, err := store.BeginTransitionStop(ctx, ticket)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkTransitionStopped(ctx, ticket); !errors.Is(err, ErrConflict) {
		t.Fatalf("unreleased source accepted: %v", err)
	}
	source, err = store.BeginNativeStop(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	// This receipt stands for the trusted controller's observed quiescence.
	if err := store.ReleaseNative(ctx, source); err != nil {
		t.Fatal(err)
	}
	ticket, err = store.MarkTransitionStopped(ctx, ticket)
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func TestTransitionProtectsBothSelectionsAcrossSourceStop(t *testing.T) {
	ctx := context.Background()
	store, _, source, target := transitionFixture(t)
	// A target Team can already have running consumers.
	peer := testNativeIntent("target-peer")
	peer.Binding.TeamUID, peer.Binding.AgentPolicyUID = target.Binding.TeamUID, target.Binding.AgentPolicyUID
	peer.Sources, peer.SourceDigests = target.Sources, target.SourceDigests
	acquireNative(t, store, peer)
	ticket := requireTransition(t, store, source, target)
	if err := store.AuthorizeNativeStart(ctx, source.Reservation.Intent); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []Phase{Reserved, Changing, Starting} {
		if phase == Changing {
			ticket = stopTransitionSource(t, store, ticket, source)
		}
		if phase == Starting {
			var err error
			ticket, err = store.StartTransitionTarget(ctx, ticket)
			if err != nil {
				t.Fatal(err)
			}
		}
		for _, selection := range []NativeIntent{source.Reservation.Intent, target} {
			for _, ref := range selection.Sources {
				if _, err := store.AcquireChange(ctx, ref, strings.Repeat("a", 64)); !errors.Is(err, ErrConflict) {
					t.Fatalf("%s allowed mutation of %s: %v", phase, ref.Name, err)
				}
			}
		}
		if err := store.AuthorizeNativeStart(ctx, peer); err != nil {
			t.Fatalf("target consumer interrupted: %v", err)
		}
		if phase == Changing {
			if _, err := store.AcquireNative(ctx, target); !errors.Is(err, ErrConflict) {
				t.Fatal("target started before commit:", err)
			}
			if err := store.ReleaseTransition(ctx, ticket); !errors.Is(err, ErrConflict) {
				t.Fatal("transition released without target:", err)
			}
		}
	}
	if _, err := store.AcquireNative(ctx, source.Reservation.Intent); !errors.Is(err, ErrConflict) {
		t.Fatal("source replay accepted:", err)
	}
	target.AgentGeneration++ // The protected binding commit advances generation.
	acquireNative(t, store, target)
	if err := store.ReleaseTransition(ctx, ticket); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.ReadTransition(ctx, target.Agent.UID); err != nil || found {
		t.Fatal("completed transition retained:", err)
	}
	if _, err := store.AcquireChange(ctx, source.Reservation.Intent.Sources[0], strings.Repeat("a", 64)); err != nil {
		t.Fatal("old Team remains reserved:", err)
	}
}

func TestTransitionRecoveryAndExactIdentity(t *testing.T) {
	ctx := context.Background()
	store, _, source, target := transitionFixture(t)
	ticket := requireTransition(t, store, source, target)
	retry := requireTransition(t, store, source, target)
	if retry.Transition.ID != ticket.Transition.ID {
		t.Fatal("retry replaced operation")
	}
	wrong := ticket.DeepCopy()
	wrong.AuthorityUID = "replacement"
	if err := store.CheckTransition(ctx, wrong); !errors.Is(err, ErrConflict) {
		t.Fatal("replaced authority accepted")
	}
	wrong = ticket.DeepCopy()
	wrong.Transition.Target.Sources[0].UID = "replacement"
	if err := store.CheckTransition(ctx, wrong); !errors.Is(err, ErrConflict) {
		t.Fatal("changed target accepted")
	}
	if _, err := store.AcquireTransition(ctx, "team-change-test-0002", source, target); !errors.Is(err, ErrConflict) {
		t.Fatal("competing request accepted")
	}
	if err := store.ReleaseTransition(ctx, ticket); err != nil {
		t.Fatal("unfenced preflight cannot abort:", err)
	}
	if err := store.AuthorizeNativeStart(ctx, source.Reservation.Intent); err != nil {
		t.Fatal("abort interrupted source:", err)
	}
}

func TestTransitionPauseCleanupRequiresObservedIntentAndNoReservation(t *testing.T) {
	ctx := context.Background()
	store, storage, source, target := transitionFixture(t)
	agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: target.Agent.Name, UID: target.Agent.UID}}
	if err := storage.Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	ticket := requireTransition(t, store, source, target)
	if err := store.ReleaseStoppedTransition(ctx, ticket); !errors.Is(err, ErrConflict) {
		t.Fatal("active source released")
	}
	ticket = stopTransitionSource(t, store, ticket, source)
	if err := store.ReleaseStoppedTransition(ctx, ticket); !errors.Is(err, ErrConflict) {
		t.Fatal("unpaused Agent released")
	}
	agent.Spec.Suspended = true
	if err := storage.Update(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseStoppedTransition(ctx, ticket); err != nil {
		t.Fatal(err)
	}
}

func TestTransitionTargetWriterRaceHasOneCASWinner(t *testing.T) {
	ctx := context.Background()
	store, storage, source, target := transitionFixture(t)
	var reads atomic.Int32
	barrier := make(chan struct{})
	store.APIReader = interceptor.NewClient(storage, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
		if err := c.Get(ctx, key, object, options...); err != nil {
			return err
		}
		switch reads.Add(1) {
		case 1:
			<-barrier
		case 2:
			close(barrier)
		}
		return nil
	}})
	results := make(chan error, 2)
	var group sync.WaitGroup
	group.Go(func() {
		_, err := store.AcquireTransition(ctx, "team-change-race-0001", source, target)
		results <- err
	})
	group.Go(func() { _, err := store.AcquireChange(ctx, target.Sources[0], strings.Repeat("a", 64)); results <- err })
	group.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !apierrors.IsConflict(err) && !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("got %d winners", winners)
	}
}

func TestStoppedTransitionRequiresPauseAndReleasedSource(t *testing.T) {
	ctx := context.Background()
	store, storage, source, target := transitionFixture(t)
	agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: source.Reservation.Intent.Agent.Name, UID: source.Reservation.Intent.Agent.UID},
		Spec: api.AgentSpec{Team: source.Reservation.Intent.Sources[0].Name,
			SandboxRef: source.Reservation.Intent.SelectedSandbox(), Suspended: true}}
	if err := storage.Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireStoppedTransition(ctx, "paused-team-change-0001", source.Reservation.Intent, target); !errors.Is(err, ErrConflict) {
		t.Fatal("paused intent accepted without released source:", err)
	}
	stopping, err := store.BeginNativeStop(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseNative(ctx, stopping); err != nil {
		t.Fatal(err)
	}
	agent.Spec.Suspended = false
	if err := storage.Update(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireStoppedTransition(ctx, "paused-team-change-0001", source.Reservation.Intent, target); !errors.Is(err, ErrConflict) {
		t.Fatal("unpaused lifetime accepted:", err)
	}
	agent.Spec.Suspended = true
	if err := storage.Update(ctx, agent); err != nil {
		t.Fatal(err)
	}
	ticket, err := store.AcquireStoppedTransition(ctx, "paused-team-change-0001", source.Reservation.Intent, target)
	if err != nil {
		t.Fatal(err)
	}
	if ticket.Transition.Phase != Changing || !ticket.Transition.SourceStopped {
		t.Fatal("paused acquisition did not retain stopped checkpoint")
	}
	retry, err := store.AcquireStoppedTransition(ctx, "paused-team-change-0001", source.Reservation.Intent, target)
	if err != nil || retry.Transition.ID != ticket.Transition.ID {
		t.Fatal("lost acquisition response cannot recover:", err)
	}
	for _, intent := range []NativeIntent{source.Reservation.Intent, target} {
		if _, err := store.AcquireNative(ctx, intent); !errors.Is(err, ErrConflict) {
			t.Fatal("stopped transition allowed native start:", err)
		}
		if _, err := store.AcquireChange(ctx, intent.Sources[0], strings.Repeat("a", 64)); !errors.Is(err, ErrConflict) {
			t.Fatal("stopped transition allowed source mutation:", err)
		}
	}
	if err := store.ReleaseTransition(ctx, ticket); !errors.Is(err, ErrConflict) {
		t.Fatal("paused transition released without durable pause check:", err)
	}
	if err := store.ReleaseStoppedTransition(ctx, ticket); err != nil {
		t.Fatal(err)
	}
}

func TestStoppedTransitionRacesHaveOneCASWinner(t *testing.T) {
	for _, operation := range []string{"native start", "target configuration write"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			store, storage, source, target := transitionFixture(t)
			stopping, err := store.BeginNativeStop(ctx, source)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.ReleaseNative(ctx, stopping); err != nil {
				t.Fatal(err)
			}
			agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: source.Reservation.Intent.Agent.Name, UID: source.Reservation.Intent.Agent.UID},
				Spec: api.AgentSpec{Team: source.Reservation.Intent.Sources[0].Name,
					SandboxRef: source.Reservation.Intent.SelectedSandbox(), Suspended: true}}
			if err := storage.Create(ctx, agent); err != nil {
				t.Fatal(err)
			}
			var reads atomic.Int32
			barrier := make(chan struct{})
			store.APIReader = interceptor.NewClient(storage, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
				if err := c.Get(ctx, key, object, options...); err != nil {
					return err
				}
				if _, ok := object.(*corev1.ConfigMap); ok {
					switch reads.Add(1) {
					case 1:
						<-barrier
					case 2:
						close(barrier)
					}
				}
				return nil
			}})
			results := make(chan error, 2)
			var group sync.WaitGroup
			group.Go(func() {
				_, err := store.AcquireStoppedTransition(ctx, "paused-team-race-0001", source.Reservation.Intent, target)
				results <- err
			})
			group.Go(func() {
				var err error
				if operation == "native start" {
					_, err = store.AcquireNative(ctx, source.Reservation.Intent)
				} else {
					_, err = store.AcquireChange(ctx, target.Sources[0], strings.Repeat("a", 64))
				}
				results <- err
			})
			group.Wait()
			close(results)
			winners := 0
			for err := range results {
				if err == nil {
					winners++
				} else if !apierrors.IsConflict(err) && !errors.Is(err, ErrConflict) {
					t.Fatal(err)
				}
			}
			if winners != 1 {
				t.Fatalf("got %d winners", winners)
			}
		})
	}
}

func TestStoppedTransitionDeletionBarrierDefeatsStaleAcquire(t *testing.T) {
	ctx := context.Background()
	store, storage, source, target := transitionFixture(t)
	stopping, err := store.BeginNativeStop(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseNative(ctx, stopping); err != nil {
		t.Fatal(err)
	}
	agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: source.Reservation.Intent.Agent.Name,
		UID: source.Reservation.Intent.Agent.UID, Finalizers: []string{"test.example.com/retain"}},
		Spec: api.AgentSpec{Team: source.Reservation.Intent.Sources[0].Name,
			SandboxRef: source.Reservation.Intent.SelectedSandbox(), Suspended: true}}
	if err := storage.Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	identity := source.Reservation.Intent.Agent
	if err := store.FenceStoppedTransitionsForDeletion(ctx, identity); !errors.Is(err, ErrConflict) {
		t.Fatalf("barrier accepted a non-deleting Agent: %v", err)
	}
	acquiring := store
	var raced bool
	acquiring.APIReader = interceptor.NewClient(storage, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
		if err := c.Get(ctx, key, object, options...); err != nil {
			return err
		}
		if _, ok := object.(*api.Agent); !ok || raced {
			return nil
		}
		raced = true
		// Acquire has captured both authority RV and the old paused Agent.
		// Complete deletion cleanup before allowing its stale CAS to proceed.
		if err := storage.Delete(ctx, agent); err != nil {
			return err
		}
		if err := store.FenceStoppedTransitionsForDeletion(ctx, identity); err != nil {
			return err
		}
		if _, found, err := store.ReadTransition(ctx, agent.UID); err != nil || found {
			t.Fatalf("unexpected transition before stale acquire: found=%v err=%v", found, err)
		}
		current := &api.Agent{}
		if err := storage.Get(ctx, client.ObjectKeyFromObject(agent), current); err != nil {
			return err
		}
		current.Finalizers = nil
		return storage.Update(ctx, current)
	}})
	if _, err := acquiring.AcquireStoppedTransition(ctx, "deleting-team-race-0001", source.Reservation.Intent, target); !apierrors.IsConflict(err) {
		t.Fatalf("stale acquire escaped deletion barrier: %v", err)
	}
	if _, found, err := store.ReadTransition(ctx, agent.UID); err != nil || found {
		t.Fatalf("orphan transition after Agent deletion: found=%v err=%v", found, err)
	}
	if _, err := store.AcquireStoppedTransition(ctx, "deleting-team-race-0001", source.Reservation.Intent, target); !apierrors.IsNotFound(err) {
		t.Fatalf("new acquisition accepted a deleted Agent: %v", err)
	}
}

func TestStoppedTransitionDeletionBarrierRetainsExactCleanup(t *testing.T) {
	ctx := context.Background()
	store, storage, source, target := transitionFixture(t)
	agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: source.Reservation.Intent.Agent.Name,
		UID: source.Reservation.Intent.Agent.UID, Finalizers: []string{"test.example.com/retain"}},
		Spec: api.AgentSpec{Team: source.Reservation.Intent.Sources[0].Name,
			SandboxRef: source.Reservation.Intent.SelectedSandbox(), Suspended: true}}
	if err := storage.Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	stopping, err := store.BeginNativeStop(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseNative(ctx, stopping); err != nil {
		t.Fatal(err)
	}
	ticket, err := store.AcquireStoppedTransition(ctx, "delete-existing-0001", source.Reservation.Intent, target)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Delete(ctx, agent); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := store.FenceStoppedTransitionsForDeletion(ctx, source.Reservation.Intent.Agent); err != nil {
			t.Fatal(err)
		}
	}
	fresh, found, err := store.ReadTransition(ctx, agent.UID)
	if err != nil || !found || fresh.Transition.ID != ticket.Transition.ID {
		t.Fatalf("barrier changed the cleanup ticket: found=%v err=%v", found, err)
	}
	if err := store.ReleaseStoppedTransition(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireStoppedTransition(ctx, "delete-existing-0002", source.Reservation.Intent, target); !errors.Is(err, ErrConflict) {
		t.Fatalf("new acquisition accepted a terminating Agent: %v", err)
	}
}
