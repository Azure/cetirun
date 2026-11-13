// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxcoordination

import (
	"context"
	"errors"
	"reflect"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestRecoverConfigurationChangeFencesUnchangedUpdate(t *testing.T) {
	for _, kind := range []string{"Team", "Sandbox", "AgentPolicy"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			store, storage, before, after := configurationFixture(t, kind)
			ticket := changingConfiguration(t, store, before, after)
			statusWrites := 0
			store.Client = statusWriter(storage, func(ctx context.Context, c client.Client, object client.Object) error {
				statusWrites++
				if object.GetUID() != before.GetUID() || object.GetResourceVersion() != before.GetResourceVersion() {
					t.Fatal("status fence did not retain the original UID/resourceVersion")
				}
				if err := hasConfigurationFence(object, ticket.Change.ID); err != nil {
					t.Fatal(err)
				}
				return c.Update(ctx, object)
			})

			got, outcome, err := store.RecoverConfigurationChange(ctx, before, after)
			if err != nil || !reflect.DeepEqual(got, ticket) || outcome == nil || !outcome.Aborted || !outcome.Superseded || outcome.Applied || !outcome.NoPendingSubmissions {
				t.Fatalf("unchanged update was not safely fenced: ticket=%+v outcome=%+v err=%v", got, outcome, err)
			}
			if statusWrites != 1 {
				t.Fatalf("got %d status fences, want one", statusWrites)
			}
			if err := store.CheckChange(ctx, ticket); !errors.Is(err, ErrConflict) {
				t.Fatal("safe recovery retained the reservation:", err)
			}
			if err := storage.Update(ctx, after.DeepCopyObject().(client.Object)); !apierrors.IsConflict(err) {
				t.Fatalf("delayed stale update was not excluded by the status fence: %v", err)
			}
			current := before.DeepCopyObject().(client.Object)
			if err := storage.Get(ctx, client.ObjectKeyFromObject(current), current); err != nil {
				t.Fatal(err)
			}
			if err := hasConfigurationFence(current, ticket.Change.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRecoverConfigurationChangeRecognizesCommittedUpdateWithoutFence(t *testing.T) {
	ctx := context.Background()
	store, storage, before, after := configurationFixture(t, "Team")
	ticket := changingConfiguration(t, store, before, after)
	if err := storage.Update(ctx, after.DeepCopyObject().(client.Object)); err != nil {
		t.Fatal(err)
	}
	statusWrites := 0
	store.Client = statusWriter(storage, func(context.Context, client.Client, client.Object) error {
		statusWrites++
		return errors.New("recovery should not fence a committed update")
	})
	_, outcome, err := store.RecoverConfigurationChange(ctx, before, after)
	if err != nil || outcome == nil || !outcome.Applied || outcome.Aborted || statusWrites != 0 {
		t.Fatalf("committed update was not released without a fence: outcome=%+v writes=%d err=%v", outcome, statusWrites, err)
	}
	if err := store.CheckChange(ctx, ticket); !errors.Is(err, ErrConflict) {
		t.Fatal("committed recovery retained the reservation:", err)
	}
}

func TestRecoverConfigurationChangeRetainsUncertainOrUnauthorizedFence(t *testing.T) {
	for _, scenario := range []string{"not-submitted", "forbidden"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			store, storage, before, after := configurationFixture(t, "AgentPolicy")
			ticket := changingConfiguration(t, store, before, after)
			store.Client = statusWriter(storage, func(context.Context, client.Client, client.Object) error {
				if scenario == "forbidden" {
					return apierrors.NewForbidden(schema.GroupResource{Group: api.GroupVersion.Group, Resource: "agentpolicies/status"}, before.GetName(), errors.New("synthetic forbidden"))
				}
				return errors.New("synthetic lost request before submission")
			})
			_, outcome, err := store.RecoverConfigurationChange(ctx, before, after)
			if outcome != nil || err == nil {
				t.Fatalf("unconfirmed fence was released: outcome=%+v err=%v", outcome, err)
			}
			if scenario == "forbidden" && !apierrors.IsForbidden(err) {
				t.Fatalf("forbidden status write was hidden: %v", err)
			}
			if err := store.CheckChange(ctx, ticket); err != nil {
				t.Fatal("unconfirmed fence lost the reservation:", err)
			}
		})
	}
}

func TestRecoverConfigurationChangeHandlesLostFenceResponse(t *testing.T) {
	ctx := context.Background()
	store, storage, before, after := configurationFixture(t, "Sandbox")
	ticket := changingConfiguration(t, store, before, after)
	store.Client = statusWriter(storage, func(ctx context.Context, c client.Client, object client.Object) error {
		if err := c.Update(ctx, object); err != nil {
			return err
		}
		return errors.New("synthetic lost status response")
	})
	_, outcome, err := store.RecoverConfigurationChange(ctx, before, after)
	if err != nil || outcome == nil || !outcome.Aborted || !outcome.Superseded {
		t.Fatalf("lost fence response was not recovered by observation: outcome=%+v err=%v", outcome, err)
	}
	if err := store.CheckChange(ctx, ticket); !errors.Is(err, ErrConflict) {
		t.Fatal("lost fence response retained the reservation:", err)
	}
}

func TestRecoverConfigurationChangeRequiresExactChangingTicket(t *testing.T) {
	ctx := context.Background()
	store, _, before, after := configurationFixture(t, "Team")
	if _, _, err := store.RecoverConfigurationChange(ctx, before, after); !errors.Is(err, ErrConflict) {
		t.Fatal("unrecorded update was recoverable:", err)
	}
	if _, err := store.AcquireConfigurationChange(ctx, before, after); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RecoverConfigurationChange(ctx, before, after); !errors.Is(err, ErrConflict) {
		t.Fatal("Reserved update was recoverable:", err)
	}
	wrong := desiredConfiguration(before)
	wrong.SetAnnotations(map[string]string{"example.com/change": "other"})
	if _, _, err := store.RecoverConfigurationChange(ctx, before, wrong); !errors.Is(err, ErrConflict) {
		t.Fatal("different requested update was recoverable:", err)
	}
}

func TestRecoverConfigurationChangeRefusesDriftAndAuthorityChanges(t *testing.T) {
	for _, scenario := range []string{"content-drift", "deleted", "replaced", "terminating", "authority-mismatch"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			store, storage, before, after := configurationFixture(t, "Team")
			ticket := changingConfiguration(t, store, before, after)
			statusWrites := 0
			store.Client = statusWriter(storage, func(context.Context, client.Client, client.Object) error {
				statusWrites++
				return errors.New("recovery must not fence this state")
			})
			if scenario == "content-drift" {
				drift := before.DeepCopyObject().(*api.Team)
				drift.Spec.DefaultSandboxRef = "spare"
				drift.Annotations = map[string]string{"example.com/change": "drift"}
				if err := storage.Update(ctx, drift); err != nil {
					t.Fatal(err)
				}
			} else if scenario == "deleted" || scenario == "replaced" {
				if err := storage.Delete(ctx, before); err != nil {
					t.Fatal(err)
				}
				if scenario == "replaced" {
					replacement := before.DeepCopyObject().(*api.Team)
					replacement.UID = "replacement-uid"
					replacement.ResourceVersion = ""
					if err := storage.Create(ctx, replacement); err != nil {
						t.Fatal(err)
					}
				}
			} else if scenario == "terminating" {
				store.APIReader = interceptor.NewClient(storage, interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
						if err := c.Get(ctx, key, object, opts...); err != nil {
							return err
						}
						if observed, ok := object.(*unstructured.Unstructured); ok && key.Name == before.GetName() {
							_ = unstructured.SetNestedField(observed.Object, "2026-01-01T00:00:00Z", "metadata", "deletionTimestamp")
						}
						return nil
					},
				})
			} else {
				store.Identity.UID = "different-authority"
			}
			if _, outcome, err := store.RecoverConfigurationChange(ctx, before, after); outcome != nil || err == nil {
				t.Fatalf("unsafe recovery succeeded: outcome=%+v err=%v", outcome, err)
			}
			if statusWrites != 0 {
				t.Fatal("unsafe recovery wrote a status fence")
			}
			if scenario == "authority-mismatch" {
				store.Identity.UID = ticket.AuthorityUID
			}
			if err := store.CheckChange(ctx, ticket); err != nil {
				t.Fatal("unsafe recovery lost its original reservation:", err)
			}
		})
	}
}

func TestRecoverConfigurationChangeReleasesConcurrentStatusSupersession(t *testing.T) {
	ctx := context.Background()
	store, storage, before, after := configurationFixture(t, "Sandbox")
	ticket := changingConfiguration(t, store, before, after)
	current := before.DeepCopyObject().(*api.Sandbox)
	current.Status.ObservedGeneration = current.Generation
	if err := storage.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	statusWrites := 0
	store.Client = statusWriter(storage, func(context.Context, client.Client, client.Object) error {
		statusWrites++
		return errors.New("status supersession must not be fenced again")
	})
	_, outcome, err := store.RecoverConfigurationChange(ctx, before, after)
	if err != nil || outcome == nil || !outcome.Aborted || !outcome.Superseded || statusWrites != 0 {
		t.Fatalf("concurrent status supersession was not released: outcome=%+v writes=%d err=%v", outcome, statusWrites, err)
	}
	if err := store.CheckChange(ctx, ticket); !errors.Is(err, ErrConflict) {
		t.Fatal("status supersession retained the reservation:", err)
	}
}

func statusWriter(storage client.WithWatch, update func(context.Context, client.Client, client.Object) error) client.Client {
	return interceptor.NewClient(storage, interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, subresource string, object client.Object, _ ...client.SubResourceUpdateOption) error {
			if subresource != "status" {
				return errors.New("unexpected subresource")
			}
			return update(ctx, c, object)
		},
	})
}

func hasConfigurationFence(object client.Object, operation string) error {
	var conditions []metav1.Condition
	switch object := object.(type) {
	case *api.Team:
		conditions = object.Status.Conditions
	case *api.Sandbox:
		conditions = object.Status.Conditions
	case *api.AgentPolicy:
		conditions = object.Status.Conditions
	default:
		return errors.New("unsupported configuration type")
	}
	for _, condition := range conditions {
		if condition.Type == ConfigurationChangeFencedCondition && condition.Status == metav1.ConditionTrue && condition.Reason == ConfigurationChangeFencedCondition && condition.Message == "Coordinated configuration update fenced: "+operation {
			return nil
		}
	}
	return errors.New("configuration fence condition missing")
}
