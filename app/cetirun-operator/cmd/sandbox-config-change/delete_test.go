// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxcoordination"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestCLIDeleteAndObservationRecovery(t *testing.T) {
	for _, outcome := range []string{"success", "lost reply", "large replacement", "not committed"} {
		t.Run(outcome, func(t *testing.T) {
			args, store, storage, before, _ := cliFixture(t)
			args = args[:len(args)-2] // --after is forbidden for deletion.
			deletes := 0
			writer := interceptor.NewClient(storage, interceptor.Funcs{
				Delete: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.DeleteOption) error {
					deletes++
					if outcome == "not committed" {
						return errors.New(syntheticBody + " sk-dummy")
					}
					if err := c.Delete(ctx, object, opts...); err != nil {
						return err
					}
					if outcome == "lost reply" || outcome == "large replacement" {
						return errors.New(syntheticBody + " sk-dummy")
					}
					return nil
				},
			})
			code, status, message := invoke(t, append(args, "--action", "delete"), injectedClients(writer))
			if deletes != 1 || status.Operation == "" || status.Phase != sandboxcoordination.Changing {
				t.Fatal("deletion was not one-shot with recorded submission intent")
			}
			if outcome == "success" {
				if code != 0 || status.Status != "deletion-accepted" {
					t.Fatal("delete failed:", message)
				}
				if _, err := store.ReadConfigurationDeletion(context.Background(), before); !errors.Is(err, sandboxcoordination.ErrUnavailable) {
					t.Fatal("accepted deletion retained reservation:", err)
				}
				return
			}
			if code != 1 || status.Status != "uncertain" || !strings.Contains(message, deletionObservationInstruction) {
				t.Fatal("unknown Delete did not give the correct recovery instructions")
			}
			var replacement *api.Team
			if outcome == "large replacement" {
				replacement = &api.Team{
					ObjectMeta: metav1.ObjectMeta{Name: before.Name, UID: "replacement-uid"},
					Spec: api.TeamSpec{MemberContext: api.TeamContextSpec{
						Role: strings.Repeat("x", 270000),
					}},
				}
				if err := storage.Create(context.Background(), replacement); err != nil {
					t.Fatal(err)
				}
			}
			writes := 0
			observer := interceptor.NewClient(storage, interceptor.Funcs{
				Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
					writes++
					return errors.New("observation must not write")
				},
				Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
					writes++
					return errors.New("observation must not delete")
				},
			})
			code, observed, _ := invoke(t, append(args, "--action", "observe-delete"), injectedClients(observer))
			if writes != 0 || observed.Operation != status.Operation {
				t.Fatal("observe-delete changed state or selected a different operation")
			}
			if outcome != "not committed" {
				if code != 0 || observed.Status != "deletion-accepted-reservation-retained" {
					t.Fatal("deleted UID not observed")
				}
			} else if code != 1 || observed.Status != "uncertain" {
				t.Fatal("unchanged object declared completed")
			}
			if _, err := store.ReadConfigurationDeletion(context.Background(), before); err != nil {
				t.Fatal("observation released reservation:", err)
			}
			code, recovered, _ := invoke(t, append(args, "--action", "delete"), injectedClients(writer))
			if deletes != 1 || recovered.Operation != status.Operation {
				t.Fatal("recovery replayed or substituted the deletion")
			}
			if (code == 0) != (outcome != "not committed") {
				t.Fatal("unexpected recovery result")
			}
			if replacement != nil {
				current := &api.Team{}
				if err := storage.Get(context.Background(), client.ObjectKeyFromObject(replacement), current); err != nil ||
					current.UID != replacement.UID || current.ResourceVersion != replacement.ResourceVersion ||
					current.Spec.MemberContext.Role != replacement.Spec.MemberContext.Role {
					t.Fatal("recovery modified the replacement:", err)
				}
			}
		})
	}
}

func TestCLIDeleteRequiresExplicitActionAndForbidsAfter(t *testing.T) {
	args, store, storage, before, _ := cliFixture(t)
	for _, invalid := range [][]string{
		args[:len(args)-2],
		append(append([]string{}, args...), "--action", "delete"),
		append(append([]string{}, args...), "--action", "observe-delete"),
	} {
		calls := 0
		factory := func(options) (client.Client, client.Reader, error) {
			calls++
			return storage, storage, nil
		}
		code, _, _ := invoke(t, invalid, factory)
		if code != 2 || calls != 0 {
			t.Fatal("ambiguous deletion flags reached client setup")
		}
	}
	code, _, message := invoke(t, append(args[:len(args)-2], "--action", "observe-delete"), injectedClients(storage))
	if code != 1 || !strings.Contains(message, deletionObservationInstruction) {
		t.Fatal("unrecorded deletion was not reported explicitly")
	}
	if _, err := store.ReadConfigurationDeletion(context.Background(), before); !errors.Is(err, sandboxcoordination.ErrUnavailable) {
		t.Fatal("observe-delete created a reservation")
	}
}

func TestCLIDeleteAcceptancePreservesFinalizers(t *testing.T) {
	args, _, storage, before, _ := cliFixture(t)
	before.Finalizers = []string{"example.com/retain"}
	if err := storage.Update(context.Background(), before); err != nil {
		t.Fatal(err)
	}
	before.TypeMeta = metav1.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "Team"}
	for i, arg := range args {
		if arg == "--before" {
			writeObjectFile(t, args[i+1], before)
		}
	}
	code, status, message := invoke(t, append(args[:len(args)-2], "--action", "delete"), injectedClients(storage))
	if code != 0 || status.Status != "deletion-accepted" {
		t.Fatal("terminating deletion was not accepted:", message)
	}
	current := &api.Team{}
	if err := storage.Get(context.Background(), client.ObjectKeyFromObject(before), current); err != nil ||
		current.DeletionTimestamp.IsZero() || len(current.Finalizers) != 1 {
		t.Fatal("CLI deleted/finalized a retained object:", err)
	}
}

func TestCLITransportDoesNotRetryADelete(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method != http.MethodDelete {
			t.Error("unexpected method:", request.Method)
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Retry-After", "1")
		writer.WriteHeader(http.StatusTooManyRequests)
		if err := json.NewEncoder(writer).Encode(metav1.Status{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: metav1.StatusFailure,
			Reason: metav1.StatusReasonTooManyRequests, Code: http.StatusTooManyRequests,
		}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{api.GroupVersion})
	mapper.Add(api.GroupVersion.WithKind("Team"), meta.RESTScopeRoot)
	httpClient := &http.Client{Transport: &singleMutationTransport{next: http.DefaultTransport}}
	base, err := client.New(&rest.Config{Host: server.URL}, client.Options{Scheme: scheme, Mapper: mapper, HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	writer := &singleMutationClient{Client: base}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = writer.Delete(ctx, &api.Team{ObjectMeta: metav1.ObjectMeta{Name: "team", UID: "team-uid", ResourceVersion: "1"}})
	if err == nil || requests.Load() != 1 || apierrors.IsNotFound(err) {
		t.Fatalf("automatic Delete retry reached HTTP: requests=%d error=%v", requests.Load(), err)
	}
}
