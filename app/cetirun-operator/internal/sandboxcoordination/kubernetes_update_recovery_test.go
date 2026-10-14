// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxcoordination

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestUpdateRecoveryRequiresExactCommittedContent(t *testing.T) {
	for _, kind := range []string{"Team", "Sandbox", "AgentPolicy"} {
		for _, scenario := range []string{"committed", "unchanged", "same-version", "replaced", "spec-drift", "metadata-drift", "terminating", "unknown-field", "read-error", "wrong-operation"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				ctx := context.Background()
				store, storage, before, after := configurationFixture(t, kind)
				ticket, err := store.AcquireConfigurationChange(ctx, before, after)
				if err != nil {
					t.Fatal(err)
				}
				ticket, err = store.BeginChange(ctx, ticket)
				if err != nil {
					t.Fatal(err)
				}
				if scenario != "unchanged" {
					if err := storage.Update(ctx, after); err != nil {
						t.Fatal(err)
					}
				}
				reader := interceptor.NewClient(storage, interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
						if err := c.Get(ctx, key, object, opts...); err != nil {
							return err
						}
						observed, ok := object.(*unstructured.Unstructured)
						if !ok {
							return nil
						}
						switch scenario {
						case "same-version":
							observed.SetResourceVersion(before.GetResourceVersion())
						case "replaced":
							observed.SetUID("replacement-uid")
						case "spec-drift":
							_ = unstructured.SetNestedMap(observed.Object, map[string]interface{}{}, "spec")
						case "metadata-drift":
							observed.SetAnnotations(map[string]string{"example.com/drift": "different"})
						case "terminating":
							_ = unstructured.SetNestedField(observed.Object, "2026-01-01T00:00:00Z", "metadata", "deletionTimestamp")
						case "unknown-field":
							observed.Object["unknown"] = "unrecognized"
						case "read-error":
							return errors.New("synthetic read failure")
						}
						return nil
					},
				})
				// A new Store and adapter retain only the durable journal and current
				// source key/UID; neither original request body participates.
				restarted := store
				restarted.APIReader = reader
				current := before.DeepCopyObject().(client.Object)
				if err := storage.Get(ctx, client.ObjectKeyFromObject(current), current); err != nil {
					t.Fatal(err)
				}
				recovered, found, err := restarted.ReadConfigurationUpdate(ctx, current)
				if err != nil || !found || recovered.Change.ID != ticket.Change.ID {
					t.Fatalf("durable lookup: %v found=%v", err, found)
				}
				adapter, err := NewKubernetesUpdateRecoveryAdapter(reader, recovered, current)
				if err != nil {
					t.Fatal(err)
				}
				if err := adapter.Submit(ctx, recovered, func(context.Context) error { t.Fatal("recovery attempted submission"); return nil }); !errors.Is(err, ErrConflict) {
					t.Fatal("recovery allowed submission")
				}
				if scenario == "wrong-operation" {
					forged := recovered.DeepCopy()
					forged.Change.ID = "another-operation"
					if _, err := adapter.ObserveChange(ctx, forged); !errors.Is(err, ErrConflict) {
						t.Fatal("observer accepted another operation")
					}
					return
				}
				_, err = restarted.ReconcileChange(ctx, recovered, adapter)
				if scenario == "committed" {
					if err != nil {
						t.Fatal("committed update was not recovered:", err)
					}
					if _, found, err := restarted.ReadConfigurationUpdate(ctx, current); err != nil || found {
						t.Fatal("successful recovery retained the reservation")
					}
				} else {
					if err == nil {
						t.Fatal("uncertain update was released")
					}
					if err := store.CheckChange(ctx, ticket); err != nil {
						t.Fatal("failed observation lost the reservation:", err)
					}
				}
			})
		}
	}
}

func TestUpdateRecoveryCannotAdoptDeletionOrReservedOperation(t *testing.T) {
	for _, scenario := range []string{"deletion", "reserved", "replacement"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			store, storage, before, after := configurationFixture(t, "Team")
			var ticket ChangeTicket
			var err error
			if scenario == "deletion" {
				ticket, err = store.AcquireConfigurationDeletion(ctx, before)
			} else {
				ticket, err = store.AcquireConfigurationChange(ctx, before, after)
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario != "reserved" {
				ticket, err = store.BeginChange(ctx, ticket)
				if err != nil {
					t.Fatal(err)
				}
			}
			current := before.DeepCopyObject().(client.Object)
			if scenario == "replacement" {
				current.SetUID("replacement-uid")
			}
			if _, _, err := store.ReadConfigurationUpdate(ctx, current); !errors.Is(err, ErrConflict) {
				t.Fatal("invalid recovery lookup accepted:", err)
			}
			if _, err := NewKubernetesUpdateRecoveryAdapter(storage, ticket, current); !errors.Is(err, ErrConflict) {
				t.Fatal("invalid recovery adapter accepted:", err)
			}
			if err := store.CheckChange(ctx, ticket); err != nil {
				t.Fatal("invalid recovery changed the reservation:", err)
			}
		})
	}
}
