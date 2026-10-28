// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package configurationcoordination

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxcoordination"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestClientUsesChangingReservationAndBackgroundDeletion(t *testing.T) {
	ctx := context.Background()
	storage, store, authority, before := coordinationFixture(t)
	updateAuthorized, deleteAuthorized := false, false
	writer := interceptor.NewClient(storage, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.UpdateOption) error {
			if after, ok := object.(*api.AgentPolicy); ok {
				current := &api.AgentPolicy{}
				if err := storage.Get(ctx, client.ObjectKeyFromObject(after), current); err != nil {
					return err
				}
				if err := store.AuthorizeConfigurationUpdate(ctx, current, after); err != nil {
					t.Fatalf("update reached admission without its exact Changing reservation: %v", err)
				}
				updateAuthorized = true
			}
			return c.Update(ctx, object, opts...)
		},
		Delete: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.DeleteOption) error {
			current := &api.AgentPolicy{}
			if err := storage.Get(ctx, client.ObjectKeyFromObject(object), current); err != nil {
				return err
			}
			options := (&client.DeleteOptions{}).ApplyOptions(opts).AsDeleteOptions()
			if err := store.AuthorizeConfigurationDeletion(ctx, current, *options); err != nil {
				t.Fatalf("delete reached admission without an exact Changing reservation and background propagation: %v", err)
			}
			deleteAuthorized = true
			return c.Delete(ctx, object, opts...)
		},
	})
	coordinator, err := New(writer, storage, authority)
	if err != nil {
		t.Fatal(err)
	}
	after := before.DeepCopy()
	after.Spec.Harnesses.Allowed = []api.AgentType{api.AgentTypeHermes}
	if err := coordinator.Update(ctx, before, after); err != nil {
		t.Fatal(err)
	}
	if !updateAuthorized {
		t.Fatal("coordinated update did not reach its admission boundary")
	}
	updated := &api.AgentPolicy{}
	if err := storage.Get(ctx, client.ObjectKeyFromObject(before), updated); err != nil {
		t.Fatal(err)
	}
	if len(updated.Spec.Harnesses.Allowed) != 1 || updated.Spec.Harnesses.Allowed[0] != api.AgentTypeHermes {
		t.Fatalf("unexpected updated policy: %+v", updated.Spec)
	}
	if err := coordinator.Delete(ctx, updated); err != nil {
		t.Fatal(err)
	}
	if !deleteAuthorized {
		t.Fatal("coordinated delete did not reach its admission boundary")
	}
	if err := storage.Get(ctx, client.ObjectKeyFromObject(before), &api.AgentPolicy{}); !apierrors.IsNotFound(err) {
		t.Fatalf("policy still exists after coordinated deletion: %v", err)
	}
	assertNoChanges(t, store)
}

func TestClientRetainsReservationWhenUpdateOutcomeIsUnknown(t *testing.T) {
	ctx := context.Background()
	storage, store, authority, before := coordinationFixture(t)
	writer := interceptor.NewClient(storage, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.UpdateOption) error {
			if _, ok := object.(*api.AgentPolicy); ok {
				return errors.New("synthetic unavailable writer")
			}
			return c.Update(ctx, object, opts...)
		},
	})
	coordinator, err := New(writer, storage, authority)
	if err != nil {
		t.Fatal(err)
	}
	after := before.DeepCopy()
	after.Spec.Harnesses.Allowed = []api.AgentType{api.AgentTypeHermes}
	if err := coordinator.Update(ctx, before, after); err == nil {
		t.Fatal("unknown Update outcome was reported as successful")
	}
	current := &api.AgentPolicy{}
	if err := storage.Get(ctx, client.ObjectKeyFromObject(before), current); err != nil {
		t.Fatal(err)
	}
	if current.Spec.Harnesses.Allowed[0] != api.AgentTypeOpenClaw {
		t.Fatal("failed coordinated submission changed the policy")
	}
	ticket, err := store.ReadConfigurationChange(ctx, before, after)
	if err != nil || ticket.Change.Phase != sandboxcoordination.Changing {
		t.Fatalf("unknown outcome did not retain its Changing reservation: ticket=%+v err=%v", ticket, err)
	}
}

func TestClientRetainsReservationWhenSubmissionIntentReplyIsLost(testContext *testing.T) {
	for _, action := range []string{"update", "delete"} {
		testContext.Run(action, func(testContext *testing.T) {
			ctx := context.Background()
			storage, store, authority, before := coordinationFixture(testContext)
			mutations := 0
			writer := interceptor.NewClient(storage, interceptor.Funcs{
				Update: func(ctx context.Context, storage client.WithWatch, object client.Object, opts ...client.UpdateOption) error {
					if authority, ok := object.(*corev1.ConfigMap); ok {
						var state struct {
							Changes []sandboxcoordination.Change `json:"changes"`
						}
						if err := json.Unmarshal([]byte(authority.Data[sandboxcoordination.DataKey]), &state); err != nil {
							testContext.Fatal(err)
						}
						if len(state.Changes) == 1 && state.Changes[0].Phase == sandboxcoordination.Changing {
							if err := storage.Update(ctx, object, opts...); err != nil {
								return err
							}
							return errors.New("synthetic lost submission intent reply")
						}
					} else {
						mutations++
					}
					return storage.Update(ctx, object, opts...)
				},
				Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
					mutations++
					return errors.New("unconfirmed submission intent must not delete")
				},
			})
			coordinator, err := New(writer, storage, authority)
			if err != nil {
				testContext.Fatal(err)
			}
			var ticket sandboxcoordination.ChangeTicket
			if action == "update" {
				after := before.DeepCopy()
				after.Spec.Harnesses.Allowed = []api.AgentType{api.AgentTypeHermes}
				err = coordinator.Update(ctx, before, after)
				ticket, _ = store.ReadConfigurationChange(ctx, before, after)
			} else {
				err = coordinator.Delete(ctx, before)
				ticket, _ = store.ReadConfigurationDeletion(ctx, before)
			}
			if !IsUnavailable(err) || mutations != 0 || ticket.Change.Phase != sandboxcoordination.Changing {
				testContext.Fatalf("lost intent reply did not preserve an unavailable, unsubmitted Changing operation: %v %+v writes=%d", err, ticket, mutations)
			}
		})
	}
}

func TestClientRequiresRecoveryWhenReleaseFails(testContext *testing.T) {
	resource := schema.GroupResource{Resource: "configmaps"}
	for _, action := range []string{"update", "delete"} {
		for _, cause := range []error{
			apierrors.NewNotFound(resource, "configuration-coordination"),
			apierrors.NewForbidden(resource, "configuration-coordination", errors.New("synthetic denied release")),
			apierrors.NewConflict(resource, "configuration-coordination", errors.New("synthetic conflicting release")),
		} {
			testContext.Run(action+"/"+cause.Error(), func(testContext *testing.T) {
				ctx := context.Background()
				storage, store, authority, before := coordinationFixture(testContext)
				deletes := 0
				writer := interceptor.NewClient(storage, interceptor.Funcs{
					Update: func(ctx context.Context, storage client.WithWatch, object client.Object, opts ...client.UpdateOption) error {
						if authority, ok := object.(*corev1.ConfigMap); ok {
							var state struct {
								Changes []sandboxcoordination.Change `json:"changes"`
							}
							if err := json.Unmarshal([]byte(authority.Data[sandboxcoordination.DataKey]), &state); err != nil {
								testContext.Fatal(err)
							}
							if len(state.Changes) == 0 {
								return cause
							}
						}
						return storage.Update(ctx, object, opts...)
					},
					Delete: func(ctx context.Context, storage client.WithWatch, object client.Object, opts ...client.DeleteOption) error {
						deletes++
						return storage.Delete(ctx, object, opts...)
					},
				})
				coordinator, err := New(writer, storage, authority)
				if err != nil {
					testContext.Fatal(err)
				}
				if action == "update" {
					after := before.DeepCopy()
					after.Spec.Harnesses.Allowed = []api.AgentType{api.AgentTypeHermes}
					err = coordinator.Update(ctx, before, after)
				} else {
					err = coordinator.Delete(ctx, before)
				}
				if !IsUnavailable(err) || !errors.Is(err, cause) {
					testContext.Fatalf("release failure did not require recovery while retaining its API status: %v", err)
				}
				journal := &corev1.ConfigMap{}
				if err := storage.Get(ctx, client.ObjectKey{Namespace: authority.Namespace, Name: authority.Name}, journal); err != nil {
					testContext.Fatal(err)
				}
				var state struct {
					Changes []sandboxcoordination.Change `json:"changes"`
				}
				if err := json.Unmarshal([]byte(journal.Data[sandboxcoordination.DataKey]), &state); err != nil || len(state.Changes) != 1 ||
					state.Changes[0].Phase != sandboxcoordination.Changing {
					testContext.Fatalf("release failure did not retain its Changing reservation: %+v %v", state, err)
				}
				if action != "delete" {
					return
				}
				if err := storage.Get(ctx, client.ObjectKeyFromObject(before), &api.AgentPolicy{}); !apierrors.IsNotFound(err) {
					testContext.Fatal("deletion did not commit before its release failure:", err)
				}
				ticket, err := store.ReadConfigurationDeletionOperation(ctx, state.Changes[0].ID)
				if err != nil {
					testContext.Fatal("Portal deletion was not recoverable without its original body:", err)
				}
				adapter, err := sandboxcoordination.NewKubernetesDeletionRecoveryAdapter(storage, ticket)
				if err != nil {
					testContext.Fatal(err)
				}
				if _, err := store.ReconcileChange(ctx, ticket, adapter); err != nil || deletes != 1 {
					testContext.Fatal("recovery failed or replayed the committed Delete:", err)
				}
				assertNoChanges(testContext, store)
			})
		}
	}
}

func coordinationFixture(t *testing.T) (client.WithWatch, sandboxcoordination.Store, Authority, *api.AgentPolicy) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	authorityObject, err := sandboxcoordination.NewState("cetirun-system", "configuration-coordination")
	if err != nil {
		t.Fatal(err)
	}
	authorityObject.UID = types.UID("authority-uid")
	policy := &api.AgentPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "standard", UID: types.UID("policy-uid")},
		Spec:       api.AgentPolicySpec{Harnesses: api.AgentPolicyHarnesses{Allowed: []api.AgentType{api.AgentTypeOpenClaw}}},
	}
	storage := fake.NewClientBuilder().WithScheme(scheme).WithGlobalResourceVersionCounter().WithStatusSubresource(&api.AgentPolicy{}).WithObjects(authorityObject, policy).Build()
	if err := storage.Get(context.Background(), client.ObjectKeyFromObject(policy), policy); err != nil {
		t.Fatal(err)
	}
	authority := Authority{Namespace: authorityObject.Namespace, Name: authorityObject.Name, UID: authorityObject.UID}
	store := sandboxcoordination.Store{Client: storage, APIReader: storage, Namespace: authority.Namespace,
		Identity: w.ObjectIdentity{Name: authority.Name, UID: authority.UID}}
	return storage, store, authority, policy
}

func assertNoChanges(t *testing.T, store sandboxcoordination.Store) {
	t.Helper()
	authority := &corev1.ConfigMap{}
	if err := store.APIReader.Get(context.Background(), client.ObjectKey{Namespace: store.Namespace, Name: store.Identity.Name}, authority); err != nil {
		t.Fatal(err)
	}
	state := struct {
		Changes []json.RawMessage `json:"changes"`
	}{}
	if err := json.Unmarshal([]byte(authority.Data[sandboxcoordination.DataKey]), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Changes) != 0 {
		t.Fatalf("successful operations left %d reservations", len(state.Changes))
	}
}
