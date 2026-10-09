// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxcoordination

import (
	"context"
	"errors"
	"reflect"
	"testing"

	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func deletionAdapter(t *testing.T, writer client.Client, reader client.Reader, before client.Object) *KubernetesDeleteAdapter {
	t.Helper()
	adapter, err := NewKubernetesDeleteAdapter(writer, reader, before)
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func deletionOptions(before client.Object) metav1.DeleteOptions {
	return metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{
			UID: ptr.To(before.GetUID()), ResourceVersion: ptr.To(before.GetResourceVersion()),
		},
		PropagationPolicy: ptr.To(metav1.DeletePropagationBackground),
	}
}

func TestConfigurationDeletionRecovery(t *testing.T) {
	for _, kind := range []string{"Team", "Sandbox", "AgentPolicy"} {
		for _, scenario := range []string{"removed", "terminating", "lost reply", "never committed", "never submitted", "replacement"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				ctx := context.Background()
				store, storage, before, _ := configurationFixture(t, kind)
				if scenario == "terminating" {
					before.SetFinalizers([]string{"example.com/retain"})
					if err := storage.Update(ctx, before); err != nil {
						t.Fatal(err)
					}
				}
				original := before.DeepCopyObject()
				deletes := 0
				writer := interceptor.NewClient(storage, interceptor.Funcs{
					Delete: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.DeleteOption) error {
						deletes++
						options := &client.DeleteOptions{}
						options.ApplyOptions(opts)
						expected := deletionOptions(before)
						if !reflect.DeepEqual(options.Preconditions, expected.Preconditions) ||
							!reflect.DeepEqual(options.PropagationPolicy, expected.PropagationPolicy) {
							t.Fatal("Delete did not retain exact identity/version/background preconditions")
						}
						if scenario == "never committed" {
							return errors.New("synthetic failed request")
						}
						if err := c.Delete(ctx, object, opts...); err != nil {
							return err
						}
						if scenario == "lost reply" || scenario == "replacement" {
							return errors.New("synthetic lost reply")
						}
						return nil
					},
				})
				ticket, err := store.AcquireConfigurationDeletion(ctx, before)
				if err != nil || ticket.Change.Team != nil || ticket.Change.UnchangedDigest != "" {
					t.Fatal("deletion must reserve the whole configuration:", err)
				}
				if scenario == "never submitted" {
					ticket, err = store.BeginChange(ctx, ticket)
					if err != nil {
						t.Fatal(err)
					}
				}
				ticket, err = store.ReconcileChange(ctx, ticket, deletionAdapter(t, writer, storage, before))
				if scenario == "removed" || scenario == "terminating" {
					if err != nil || deletes != 1 {
						t.Fatal("deletion did not complete once:", err)
					}
				} else {
					if err == nil || ticket.Change.Phase != Changing {
						t.Fatal("uncertain deletion lost its reservation:", err)
					}
					if scenario == "replacement" {
						replacement := before.DeepCopyObject().(client.Object)
						replacement.SetUID("replacement-uid")
						replacement.SetResourceVersion("")
						if err := storage.Create(ctx, replacement); err != nil {
							t.Fatal(err)
						}
					}
					recovered, err := store.AcquireConfigurationDeletion(ctx, before)
					if err != nil || !reflect.DeepEqual(ticket, recovered) {
						t.Fatal("original input could not recover its operation:", err)
					}
					adapter := deletionAdapter(t, nil, storage, before)
					_, err = store.ReconcileChange(ctx, recovered, adapter)
					if scenario == "never committed" || scenario == "never submitted" {
						if err == nil || store.CheckChange(ctx, recovered) != nil {
							t.Fatal("unobserved deletion was inferred aborted/completed")
						}
					} else if err != nil {
						t.Fatal("lost reply could not recover by observation:", err)
					}
				}
				expectedDeletes := 1
				if scenario == "never submitted" {
					expectedDeletes = 0
				}
				if deletes != expectedDeletes || !reflect.DeepEqual(original, before) {
					t.Fatal("recovery retried deletion or mutated its caller's original input")
				}
				if scenario == "terminating" {
					current := before.DeepCopyObject().(client.Object)
					if err := storage.Get(ctx, client.ObjectKeyFromObject(before), current); err != nil ||
						current.GetDeletionTimestamp().IsZero() || len(current.GetFinalizers()) != 1 {
						t.Fatal("deletion acceptance removed finalizers or reported disappearance:", err)
					}
					cleaned := current.DeepCopyObject().(client.Object)
					cleaned.SetFinalizers(nil)
					if err := store.AuthorizeConfigurationFinalization(ctx, current, cleaned); err != nil {
						t.Fatal("accepted deletion prevented independent finalizer cleanup:", err)
					}
				}
				if scenario == "replacement" {
					current := before.DeepCopyObject().(client.Object)
					if err := storage.Get(ctx, client.ObjectKeyFromObject(before), current); err != nil || current.GetUID() != "replacement-uid" {
						t.Fatal("observation changed the replacement:", err)
					}
				}
			})
		}
	}
}

func TestConfigurationDeletionExcludesEveryConsumerPhase(t *testing.T) {
	for _, kind := range []string{"Team", "Sandbox", "AgentPolicy"} {
		for _, phase := range []Phase{Reserved, Starting, Running, Stopping} {
			t.Run(kind+"/"+string(phase), func(t *testing.T) {
				ctx := context.Background()
				store, storage, before, after := configurationFixture(t, kind)
				intent := configurationIntent(t, before, "delete")
				ticket := acquire(t, store, intent)
				var err error
				if phase != Reserved {
					ticket, err = store.BeginStart(ctx, ticket)
					if err != nil {
						t.Fatal(err)
					}
				}
				if phase == Running {
					ticket, err = store.MarkRunning(ctx, ticket)
				}
				if phase == Stopping {
					ticket, err = store.BeginStop(ctx, ticket)
				}
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.AcquireConfigurationDeletion(ctx, before); !errors.Is(err, ErrConflict) {
					t.Fatal("deletion did not exclude a recorded consumer:", err)
				}
				if phase != Stopping {
					ticket, err = store.BeginStop(ctx, ticket)
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := store.ReleaseExecution(ctx, ticket); err != nil {
					t.Fatal(err)
				}
				deleting, err := store.AcquireConfigurationDeletion(ctx, before)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.AcquireExecution(ctx, intent); !errors.Is(err, ErrConflict) {
					t.Fatal("a deletion reservation allowed a new execution:", err)
				}
				if _, err := store.AcquireConfigurationChange(ctx, before, after); !errors.Is(err, ErrConflict) {
					t.Fatal("an update adopted a deletion reservation:", err)
				}
				if _, err := store.ReadConfigurationChange(ctx, before, after); !errors.Is(err, ErrConflict) {
					t.Fatal("an update read a deletion ticket:", err)
				}
				if _, err := store.ReconcileChange(ctx, deleting, deletionAdapter(t, storage, storage, before)); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestConfigurationDeletionAdmissionPinsRequestAndDoesNotWrite(t *testing.T) {
	ctx := context.Background()
	store, storage, before, _ := configurationFixture(t, "AgentPolicy")
	options := deletionOptions(before)
	if err := store.AuthorizeConfigurationDeletion(ctx, before, options); !errors.Is(err, ErrConflict) {
		t.Fatal("unrecorded delete was authorized:", err)
	}
	ticket, err := store.AcquireConfigurationDeletion(ctx, before)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AuthorizeConfigurationDeletion(ctx, before, options); !errors.Is(err, ErrConflict) {
		t.Fatal("reserved delete was authorized before submission intent:", err)
	}
	ticket, err = store.BeginChange(ctx, ticket)
	if err != nil {
		t.Fatal(err)
	}
	original := &corev1.ConfigMap{}
	key := client.ObjectKey{Namespace: store.Namespace, Name: store.Identity.Name}
	if err := storage.Get(ctx, key, original); err != nil {
		t.Fatal(err)
	}
	readOnly := store
	readOnly.Client = nil
	for name, mutate := range map[string]func(*metav1.DeleteOptions){
		"valid":       func(*metav1.DeleteOptions) {},
		"dry run":     func(o *metav1.DeleteOptions) { o.DryRun = []string{metav1.DryRunAll} },
		"no pins":     func(o *metav1.DeleteOptions) { o.Preconditions = nil },
		"no uid":      func(o *metav1.DeleteOptions) { o.Preconditions.UID = nil },
		"wrong uid":   func(o *metav1.DeleteOptions) { o.Preconditions.UID = ptr.To(before.GetUID() + "-other") },
		"no version":  func(o *metav1.DeleteOptions) { o.Preconditions.ResourceVersion = nil },
		"wrong rv":    func(o *metav1.DeleteOptions) { o.Preconditions.ResourceVersion = ptr.To("other") },
		"propagation": func(o *metav1.DeleteOptions) { o.PropagationPolicy = ptr.To(metav1.DeletePropagationForeground) },
		"no policy":   func(o *metav1.DeleteOptions) { o.PropagationPolicy = nil },
		"orphan":      func(o *metav1.DeleteOptions) { o.OrphanDependents = ptr.To(true) },
		"grace":       func(o *metav1.DeleteOptions) { o.GracePeriodSeconds = ptr.To(int64(30)) },
		"unsafe":      func(o *metav1.DeleteOptions) { o.IgnoreStoreReadErrorWithClusterBreakingPotential = ptr.To(true) },
		"bad dryrun":  func(o *metav1.DeleteOptions) { o.DryRun = []string{"invalid"} },
	} {
		t.Run(name, func(t *testing.T) {
			request := options.DeepCopy()
			mutate(request)
			err := readOnly.AuthorizeConfigurationDeletion(ctx, before, *request)
			if (err == nil) != (name == "valid" || name == "dry run") {
				t.Fatal("unexpected deletion authorization:", err)
			}
		})
	}
	current := &corev1.ConfigMap{}
	if err := storage.Get(ctx, key, current); err != nil || !reflect.DeepEqual(original, current) {
		t.Fatal("admission changed the authority:", err)
	}
	changed := before.DeepCopyObject().(client.Object)
	changed.SetLabels(map[string]string{"example.com/drift": "true"})
	if err := storage.Update(ctx, changed); err != nil {
		t.Fatal(err)
	}
	if err := readOnly.AuthorizeConfigurationDeletion(ctx, before, options); !errors.Is(err, ErrConflict) {
		t.Fatal("stale before image remained authorized:", err)
	}
	if err := store.CheckChange(ctx, ticket); err != nil {
		t.Fatal("a rejected stale write released the reservation:", err)
	}
}

func TestConfigurationDeletionRetainsUnknownOutcomesAndPreflightFailures(t *testing.T) {
	for _, scenario := range []string{"source drift", "guard", "cancelled", "authority", "read error"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			store, storage, before, _ := configurationFixture(t, "Team")
			ticket, err := store.AcquireConfigurationDeletion(ctx, before)
			if err != nil {
				t.Fatal(err)
			}
			ticket, err = store.BeginChange(ctx, ticket)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			writer := interceptor.NewClient(storage, interceptor.Funcs{
				Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
					calls++
					return errors.New("unexpected delete")
				},
			})
			reader := client.Reader(storage)
			guard := func(ctx context.Context) error { return store.CheckChange(ctx, ticket) }
			switch scenario {
			case "source drift":
				current := before.DeepCopyObject().(client.Object)
				current.SetLabels(map[string]string{"example.com/drift": "true"})
				if err := storage.Update(ctx, current); err != nil {
					t.Fatal(err)
				}
			case "guard":
				guard = func(context.Context) error { return ErrConflict }
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "authority":
				ticket.AuthorityUID = "wrong-authority"
			case "read error":
				reader = interceptor.NewClient(storage, interceptor.Funcs{
					Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
						return errors.New("synthetic unavailable source")
					},
				})
			}
			adapter := deletionAdapter(t, writer, reader, before)
			if err := adapter.Submit(ctx, ticket, guard); err == nil || calls != 0 {
				t.Fatal("failed preflight submitted a deletion:", err)
			}
			if _, err := adapter.ObserveChange(ctx, ticket); err == nil {
				t.Fatal("unobserved deletion was declared complete")
			}
		})
	}
}

func TestConfigurationDeletionInputAndOutcomeBounds(t *testing.T) {
	ctx := context.Background()
	store, storage, before, _ := configurationFixture(t, "Team")
	for name, mutate := range map[string]func(client.Object) client.Object{
		"nil":         func(client.Object) client.Object { return nil },
		"wrong type":  func(client.Object) client.Object { return &corev1.ConfigMap{} },
		"no uid":      func(o client.Object) client.Object { o.SetUID(""); return o },
		"no rv":       func(o client.Object) client.Object { o.SetResourceVersion(""); return o },
		"namespace":   func(o client.Object) client.Object { o.SetNamespace("not-cluster-scoped"); return o },
		"terminating": func(o client.Object) client.Object { o.SetDeletionTimestamp(ptr.To(metav1.Now())); return o },
	} {
		t.Run(name, func(t *testing.T) {
			input := mutate(before.DeepCopyObject().(client.Object))
			if _, err := NewKubernetesDeleteAdapter(storage, storage, input); err == nil {
				t.Fatal("invalid deletion input accepted")
			}
			if _, err := store.AcquireConfigurationDeletion(ctx, input); err == nil {
				t.Fatal("invalid deletion input reserved")
			}
		})
	}
	ticket, err := store.AcquireConfigurationDeletion(ctx, before)
	if err != nil {
		t.Fatal(err)
	}
	adapter := deletionAdapter(t, storage, storage, before)
	ticket, err = store.BeginChange(ctx, ticket)
	if err != nil {
		t.Fatal(err)
	}
	guard := func(ctx context.Context) error { return store.CheckChange(ctx, ticket) }
	if err := adapter.Submit(ctx, ticket, guard); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Submit(ctx, ticket, guard); !errors.Is(err, ErrConflict) {
		t.Fatal("adapter allowed a repeat submission:", err)
	}
	if err := storage.Get(ctx, client.ObjectKeyFromObject(before), before.DeepCopyObject().(client.Object)); !apierrors.IsNotFound(err) {
		t.Fatal("fixture was not deleted")
	}
	if validChangeAfter(&ChangeOutcome{Removed: true, Applied: true, After: w.SourceReference{Name: "not-empty"}}) ||
		validChangeAfter(&ChangeOutcome{Removed: true, Aborted: true}) {
		t.Fatal("inconsistent removed receipt accepted")
	}
}
