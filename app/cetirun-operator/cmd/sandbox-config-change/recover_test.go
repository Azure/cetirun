// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"errors"
	"net/http"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxcoordination"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestCLIRecoverUpdate(t *testing.T) {
	for _, scenario := range []string{"unrecorded", "reserved", "unchanged", "applied", "fence-rejected", "fence-lost-reply", "fence-not-committed", "release-rejected"} {
		t.Run(scenario, func(t *testing.T) {
			args, store, storage, before, after := cliFixture(t)
			ctx := context.Background()
			var ticket sandboxcoordination.ChangeTicket
			var err error
			if scenario != "unrecorded" {
				ticket, err = store.AcquireConfigurationChange(ctx, before, after)
				if err != nil {
					t.Fatal(err)
				}
				if scenario != "reserved" {
					ticket, err = store.BeginChange(ctx, ticket)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if scenario == "applied" {
				if err := storage.Update(ctx, after.DeepCopy()); err != nil {
					t.Fatal(err)
				}
			}
			statusWrites, sourceWrites, journalWrites := 0, 0, 0
			writer := interceptor.NewClient(storage, interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.UpdateOption) error {
					if _, ok := object.(*corev1.ConfigMap); ok {
						journalWrites++
						if scenario == "release-rejected" {
							return errors.New(syntheticBody)
						}
					} else {
						sourceWrites++
					}
					return c.Update(ctx, object, opts...)
				},
				SubResourceUpdate: func(ctx context.Context, c client.Client, name string, object client.Object, opts ...client.SubResourceUpdateOption) error {
					statusWrites++
					if name != "status" || object.GetUID() != before.UID || object.GetResourceVersion() != before.ResourceVersion {
						t.Fatal("fence changed its original preconditions")
					}
					if scenario == "fence-rejected" {
						return apierrors.NewForbidden(schema.GroupResource{Group: api.GroupVersion.Group, Resource: "teams/status"}, before.Name, errors.New(syntheticBody))
					}
					if scenario == "fence-not-committed" {
						return errors.New(syntheticBody)
					}
					if err := c.Status().Update(ctx, object, opts...); err != nil {
						return err
					}
					if scenario == "fence-lost-reply" {
						return errors.New(syntheticBody)
					}
					return nil
				},
			})
			code, result, _ := invoke(t, append(args, "--action", "recover-update"), injectedClients(writer))
			success := scenario == "unchanged" || scenario == "applied" || scenario == "fence-lost-reply"
			if (code == 0) != success || sourceWrites != 0 {
				t.Fatalf("recovery code=%d status=%+v sourceWrites=%d", code, result, sourceWrites)
			}
			if success {
				want := "update-superseded"
				if scenario == "applied" {
					want = "completed"
				}
				if result.Status != want || journalWrites != 1 {
					t.Fatalf("unexpected result: %+v journalWrites=%d", result, journalWrites)
				}
				if err := store.CheckChange(ctx, ticket); err == nil {
					t.Fatal("terminal recovery retained reservation")
				}
			} else if scenario != "unrecorded" {
				if err := store.CheckChange(ctx, ticket); err != nil {
					t.Fatal("failed recovery released reservation:", err)
				}
			}
			wantStatusWrites := 1
			if scenario == "unrecorded" || scenario == "reserved" || scenario == "applied" {
				wantStatusWrites = 0
			}
			if statusWrites != wantStatusWrites {
				t.Fatalf("status writes=%d want=%d", statusWrites, wantStatusWrites)
			}
			if scenario == "fence-rejected" && (result.Diagnostic == nil || result.Diagnostic.Stage != sandboxcoordination.SubmissionStageStatusUpdate || result.Diagnostic.State != sandboxcoordination.SubmissionRejected || result.Diagnostic.HTTPStatus != http.StatusForbidden) {
				t.Fatalf("missing sanitized status rejection: %+v", result.Diagnostic)
			}
			if scenario == "unchanged" || scenario == "fence-lost-reply" {
				current := &api.Team{}
				if err := storage.Get(ctx, client.ObjectKeyFromObject(before), current); err != nil {
					t.Fatal(err)
				}
				if current.Spec.DefaultSandboxRef != before.Spec.DefaultSandboxRef || meta.FindStatusCondition(current.Status.Conditions, sandboxcoordination.ConfigurationChangeFencedCondition) == nil {
					t.Fatal("recovery changed desired configuration or omitted its fence")
				}
				if err := storage.Update(ctx, after.DeepCopy()); !apierrors.IsConflict(err) {
					t.Fatal("old conditional update was not excluded:", err)
				}
			}
		})
	}
}

func TestCLIUpdateSubmissionDiagnostics(t *testing.T) {
	for _, rejection := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "rejected"}[rejection], func(t *testing.T) {
			args, store, storage, before, after := cliFixture(t)
			writer := interceptor.NewClient(storage, interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.UpdateOption) error {
					if _, ok := object.(*api.Team); ok {
						if rejection {
							return apierrors.NewForbidden(schema.GroupResource{Resource: "teams"}, before.Name, errors.New(syntheticBody))
						}
						return errors.New(syntheticBody)
					}
					return c.Update(ctx, object, opts...)
				},
			})
			code, result, _ := invoke(t, append(args, "--action", "update"), injectedClients(writer))
			want := sandboxcoordination.SubmissionUnknown
			if rejection {
				want = sandboxcoordination.SubmissionRejected
			}
			if code != 1 || result.Diagnostic == nil || result.Diagnostic.Stage != sandboxcoordination.SubmissionStageSourceUpdate || result.Diagnostic.State != want {
				t.Fatalf("missing diagnostic: code=%d result=%+v", code, result)
			}
			if _, err := store.ReadConfigurationChange(context.Background(), before, after); err != nil {
				t.Fatal("diagnostic classification released operation:", err)
			}
		})
	}
}
