// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxbinding"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestSandboxPreparationUnsupportedIntegrationsRemainCleanlyHeld(t *testing.T) {
	for name, mutate := range map[string]func(*api.Agent){
		"tailscale": func(a *api.Agent) { a.Spec.Tailscale = &api.TailscaleSpec{AuthKeySecretRef: "example-vpn"} },
		"teams":     func(a *api.Agent) { a.Spec.MSTeams = &api.MSTeamsSpec{Enabled: true} },
		"identity":  func(a *api.Agent) { a.Spec.AgentIdentity = &api.AgentIdentitySpec{Enabled: true} },
	} {
		for _, previouslyPrepared := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/prepared=%v", name, previouslyPrepared), func(t *testing.T) {
				f := newPreparationFixture(t, "openclaw")
				if previouslyPrepared {
					if err := f.reconcile(); err != nil {
						t.Fatal(err)
					}
				}
				agent := f.current(t)
				mutate(agent)
				agent.Generation++
				if err := f.client.Update(context.Background(), agent); err != nil {
					t.Fatal(err)
				}
				stages, resolves := f.installer.stages, f.source.resolves
				result, err := f.reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: agent.Name}})
				if err != nil || result.RequeueAfter != time.Minute {
					t.Fatalf("unsupported configuration entered an error/backoff loop: %+v %v", result, err)
				}
				agent = f.current(t)
				condition := apimeta.FindStatusCondition(agent.Status.Conditions, api.ConditionSandboxReady)
				if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != "UnsupportedControl" ||
					agent.Status.AccessURL != "" || !slices.Contains(agent.Finalizers, sandbox.HoldFinalizer) ||
					f.installer.stages != stages || f.source.resolves != resolves {
					t.Fatal("unsupported configuration claimed progress or entered preparation")
				}
				if previouslyPrepared {
					bindings, err := (&sandboxpolicy.GrantJournalStore{Client: f.client, APIReader: f.client, Namespace: "cetirun-system"}).
						Read(context.Background(), agent.Name, agent.UID)
					if err != nil || len(bindings) != 0 {
						t.Fatalf("unsupported transition retained prior grants: %+v %v", bindings, err)
					}
				}
			})
		}
	}
}

func TestSandboxPreparationUnsupportedProviderControlIsNotRetryableFailure(t *testing.T) {
	unsupported := fmt.Errorf("%w: synthetic unimplemented control", sandboxpolicy.ErrUnsupportedControl)
	for _, tc := range []struct {
		name  string
		cause error
		clean bool
	}{
		{"single", unsupported, true},
		{"joined-unsupported", fmt.Errorf("synthetic source: %w", errors.Join(unsupported, sandboxpolicy.ErrUnsupportedControl)), true},
		{"joined-unavailable", errors.Join(unsupported, sandboxpolicy.ErrUnavailable), false},
		{"joined-deadline", errors.Join(unsupported, context.DeadlineExceeded), false},
		{"joined-cancellation", errors.Join(unsupported, context.Canceled), false},
		{"joined-other-failure", errors.Join(unsupported, errors.New("synthetic source failure")), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPreparationFixture(t, "hermes")
			f.source.resolveErr = tc.cause
			result, err := f.reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: f.fixture.Agent.Name}})
			condition := apimeta.FindStatusCondition(f.current(t).Status.Conditions, api.ConditionAgentConfigReady)
			if condition == nil || condition.Status != metav1.ConditionFalse || f.installer.stages != 0 {
				t.Fatalf("unsupported control did not remain held: %+v %v", condition, err)
			}
			if tc.clean {
				if err != nil || result.RequeueAfter != time.Minute || condition.Reason != "UnsupportedControl" {
					t.Fatalf("unsupported control was treated as missing prerequisites: %+v %v", result, err)
				}
			} else if !errors.Is(err, tc.cause) || result.RequeueAfter != 0 || condition.Reason != "SandboxPreparationPending" {
				t.Fatalf("unsupported hold hid an operational failure: %+v %+v %v", result, condition, err)
			}
		})
	}
}

func TestSandboxPreparationPlatformPrerequisiteClassification(t *testing.T) {
	readFailure := errors.New("synthetic RuntimeClass read failure")
	for _, tc := range []struct {
		name   string
		mutate func(*preparationFixture)
		clean  bool
		cause  error
	}{
		{"unqualified adapter", func(f *preparationFixture) {
			platform := f.config.Platforms[api.SandboxKata]
			platform.Adapter.Qualified = false
			f.config.Platforms[api.SandboxKata] = platform
		}, true, nil},
		{"unsupported adapter", func(f *preparationFixture) {
			platform := f.config.Platforms[api.SandboxKata]
			platform.Adapter.Name = "synthetic-unsupported-adapter"
			f.config.Platforms[api.SandboxKata] = platform
		}, true, nil},
		{"unavailable adapter", func(f *preparationFixture) {
			platform := f.config.Platforms[api.SandboxKata]
			platform.Adapter.Available = false
			f.config.Platforms[api.SandboxKata] = platform
		}, false, sandboxpolicy.ErrUnavailable},
		{"missing platform inventory", func(f *preparationFixture) {
			delete(f.config.Platforms, api.SandboxKata)
		}, false, sandboxpolicy.ErrUnavailable},
		{"RuntimeClass read failure", func(f *preparationFixture) {
			f.reconciler.APIReader = interceptor.NewClient(f.client.(client.WithWatch), interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
					if _, ok := object.(*nodev1.RuntimeClass); ok {
						return readFailure
					}
					return c.Get(ctx, key, object, options...)
				},
			})
		}, false, readFailure},
	} {
		for _, previouslyPrepared := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/prepared=%v", tc.name, previouslyPrepared), func(t *testing.T) {
				f := newPreparationFixture(t, "hermes")
				if previouslyPrepared {
					if err := f.reconcile(); err != nil {
						t.Fatal(err)
					}
				}
				stages := f.installer.stages
				tc.mutate(f)
				result, err := f.reconciler.Reconcile(context.Background(), ctrl.Request{
					NamespacedName: client.ObjectKey{Name: f.fixture.Agent.Name},
				})
				agent := f.current(t)
				condition := apimeta.FindStatusCondition(agent.Status.Conditions, api.ConditionAgentConfigReady)
				if condition == nil || condition.Status != metav1.ConditionFalse ||
					condition.ObservedGeneration != agent.Generation || agent.Status.AccessURL != "" ||
					!slices.Contains(agent.Finalizers, sandbox.HoldFinalizer) ||
					!apimeta.IsStatusConditionFalse(agent.Status.Conditions, api.ConditionReady) ||
					!apimeta.IsStatusConditionFalse(agent.Status.Conditions, api.ConditionSandboxReady) ||
					f.installer.stages != stages {
					t.Fatalf("failed prerequisites did not retain the execution hold: %+v %v", agent.Status, err)
				}
				if tc.clean {
					if err != nil || result.RequeueAfter != time.Minute || condition.Reason != "UnsupportedControl" ||
						agent.Status.Phase != api.PhaseFailed {
						t.Fatalf("unsupported platform entered an outage/backoff loop: %+v %+v %v", result, condition, err)
					}
				} else if !errors.Is(err, tc.cause) || result.RequeueAfter != 0 ||
					condition.Reason != "SandboxPreparationPending" || agent.Status.Phase != api.PhasePending {
					t.Fatalf("an unavailable prerequisite was hidden by an unsupported hold: %+v %+v %v", result, condition, err)
				}
				if previouslyPrepared {
					bindings, err := (&sandboxpolicy.GrantJournalStore{Client: f.client, APIReader: f.client, Namespace: "cetirun-system"}).
						Read(context.Background(), agent.Name, agent.UID)
					if err != nil || len(bindings) != 0 {
						t.Fatalf("failed prerequisites retained previous grants: %+v %v", bindings, err)
					}
				}
			})
		}
	}
}

func TestSandboxPreparationDisabledUntrackedDoesNotReadState(t *testing.T) {
	f := newPreparationFixture(t, "hermes")
	f.reconciler.SandboxPreparation = nil
	stateReads := 0
	reader := interceptor.NewClient(f.client.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
			if _, ok := object.(*corev1.ConfigMap); ok {
				stateReads++
				return errors.New("unexpected preparation state access")
			}
			return c.Get(ctx, key, object, options...)
		},
	})
	f.reconciler.APIReader = reader
	if err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	if err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	if stateReads != 0 {
		t.Fatal("configured operator namespace activated untracked preparation state")
	}
}

func TestSandboxPreparationInterruptedFinalizationRetainsStateAfterProviderDisablement(t *testing.T) {
	f := newPreparationFixture(t, "openclaw")
	ctx := context.Background()
	if err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	if err := f.client.Delete(ctx, f.current(t)); err != nil {
		t.Fatal(err)
	}
	namespace := &corev1.Namespace{}
	if err := f.client.Get(ctx, client.ObjectKeyFromObject(f.fixture.Namespace), namespace); err != nil {
		t.Fatal(err)
	}
	if err := f.client.Delete(ctx, namespace); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("synthetic interrupted journal finalization")
	fired := false
	f.reconciler.APIReader = interceptor.NewClient(f.client.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
			if key.Name == sandboxpolicy.GrantJournalName(f.fixture.Agent.UID) && !fired {
				agent := &api.Agent{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(f.fixture.Agent), agent); err != nil {
					return err
				}
				if !slices.Contains(agent.Finalizers, sandbox.PreparationFinalizer) {
					fired = true
					return injected
				}
			}
			return c.Get(ctx, key, object, options...)
		},
	})
	if err := f.reconcile(); !errors.Is(err, injected) || !fired {
		t.Fatalf("fixture did not interrupt cleanup after marker release: %v", err)
	}
	if slices.Contains(f.current(t).Finalizers, sandbox.PreparationFinalizer) {
		t.Fatal("fixture must reach the released-marker cleanup phase")
	}
	preparation := f.reconciler.SandboxPreparation
	f.reconciler.SandboxPreparation = nil
	if err := f.reconcile(); err == nil || !slices.Contains(f.current(t).Finalizers, sandbox.HoldFinalizer) {
		t.Fatal("provider disablement discarded interrupted state cleanup")
	}
	f.reconciler.SandboxPreparation = preparation
	if err := f.reconcile(); err != nil {
		t.Fatal("qualified cleanup retry failed:", err)
	}
}

func TestSandboxPreparationDeletionResumesReleasedEmptyJournal(t *testing.T) {
	const foreignFinalizer = "cleanup.example.com/retained"
	for _, tc := range []struct {
		name        string
		empty       bool
		terminating bool
		mutate      func(*corev1.ConfigMap)
		wantError   bool
	}{
		{name: "released live journal", empty: true},
		{name: "released terminating journal", empty: true, terminating: true},
		{name: "unprotected grant inventory", wantError: true},
		{name: "foreign empty journal", empty: true, wantError: true, mutate: func(object *corev1.ConfigMap) {
			object.OwnerReferences[0].UID = "different-agent-uid"
		}},
		{name: "corrupt empty journal", empty: true, wantError: true, mutate: func(object *corev1.ConfigMap) {
			for key := range object.Data {
				object.Data[key] = "not-json"
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPreparationFixture(t, "openclaw")
			ctx := context.Background()
			if err := f.reconcile(); err != nil {
				t.Fatal(err)
			}
			agent := f.current(t)
			if err := f.client.Delete(ctx, agent); err != nil {
				t.Fatal(err)
			}
			journal := &sandboxpolicy.GrantJournalStore{Client: f.client, APIReader: f.client, Namespace: "cetirun-system"}
			if tc.empty {
				bindings, err := journal.Read(ctx, agent.Name, agent.UID)
				if err != nil {
					t.Fatal(err)
				}
				if err := journal.Revoke(ctx, agent.Name, agent.UID, bindings); err != nil {
					t.Fatal(err)
				}
			}
			if !tc.wantError {
				namespace := &corev1.Namespace{}
				if err := f.client.Get(ctx, client.ObjectKeyFromObject(f.fixture.Namespace), namespace); err != nil {
					t.Fatal(err)
				}
				if err := f.client.Delete(ctx, namespace); err != nil {
					t.Fatal(err)
				}
			}
			agent = f.current(t)
			agent.Finalizers = slices.DeleteFunc(agent.Finalizers, func(value string) bool {
				return value == sandbox.PreparationFinalizer
			})
			if err := f.client.Update(ctx, agent); err != nil {
				t.Fatal(err)
			}
			key := client.ObjectKey{Namespace: "cetirun-system", Name: sandboxpolicy.GrantJournalName(agent.UID)}
			object := &corev1.ConfigMap{}
			if err := f.client.Get(ctx, key, object); err != nil {
				t.Fatal(err)
			}
			object.Finalizers = nil
			if tc.terminating {
				object.Finalizers = []string{foreignFinalizer}
			}
			if tc.mutate != nil {
				tc.mutate(object)
			}
			if err := f.client.Update(ctx, object); err != nil {
				t.Fatal(err)
			}
			if tc.terminating {
				if err := f.client.Delete(ctx, object); err != nil {
					t.Fatal(err)
				}
			}
			err := f.reconcile()
			if tc.wantError {
				if err == nil || !slices.Contains(f.current(t).Finalizers, sandbox.HoldFinalizer) {
					t.Fatal("unsafe journal state allowed cleanup to complete:", err)
				}
				if err := f.client.Get(ctx, client.ObjectKeyFromObject(f.fixture.Namespace), &corev1.Namespace{}); err != nil {
					t.Fatal("unsafe journal state allowed namespace removal:", err)
				}
				return
			}
			if err != nil {
				t.Fatal("interrupted journal release prevented deletion retry:", err)
			}
			if err := f.client.Get(ctx, client.ObjectKeyFromObject(agent), &api.Agent{}); !apierrors.IsNotFound(err) {
				t.Fatal("completed journal cleanup retained the Agent:", err)
			}
			err = f.client.Get(ctx, key, object)
			if tc.terminating {
				if err != nil || object.DeletionTimestamp.IsZero() || !slices.Equal(object.Finalizers, []string{foreignFinalizer}) {
					t.Fatalf("cleanup did not preserve the foreign finalizer: %+v %v", object, err)
				}
			} else if !apierrors.IsNotFound(err) {
				t.Fatal("released journal was not deleted:", err)
			}
			if err := f.reconcile(); err != nil {
				t.Fatal("completed deletion did not remain idempotent:", err)
			}
		})
	}
}

type preparationRequestCounts struct {
	client.Client
	gets, lists, writes int
}

func (c *preparationRequestCounts) Get(ctx context.Context, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
	c.gets++
	return c.Client.Get(ctx, key, object, opts...)
}
func (c *preparationRequestCounts) List(ctx context.Context, objects client.ObjectList, opts ...client.ListOption) error {
	c.lists++
	return c.Client.List(ctx, objects, opts...)
}
func (c *preparationRequestCounts) Create(ctx context.Context, object client.Object, opts ...client.CreateOption) error {
	c.writes++
	return c.Client.Create(ctx, object, opts...)
}
func (c *preparationRequestCounts) Update(ctx context.Context, object client.Object, opts ...client.UpdateOption) error {
	c.writes++
	return c.Client.Update(ctx, object, opts...)
}
func (c *preparationRequestCounts) Patch(ctx context.Context, object client.Object, patch client.Patch, opts ...client.PatchOption) error {
	c.writes++
	return c.Client.Patch(ctx, object, patch, opts...)
}
func (c *preparationRequestCounts) Delete(ctx context.Context, object client.Object, opts ...client.DeleteOption) error {
	c.writes++
	return c.Client.Delete(ctx, object, opts...)
}
func (c *preparationRequestCounts) Status() client.SubResourceWriter {
	return &preparationStatusCounts{SubResourceWriter: c.Client.Status(), counts: c}
}

type preparationStatusCounts struct {
	client.SubResourceWriter
	counts *preparationRequestCounts
}

func (s *preparationStatusCounts) Patch(ctx context.Context, object client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	s.counts.writes++
	return s.SubResourceWriter.Patch(ctx, object, patch, opts...)
}
func (s *preparationStatusCounts) Update(ctx context.Context, object client.Object, opts ...client.SubResourceUpdateOption) error {
	s.counts.writes++
	return s.SubResourceWriter.Update(ctx, object, opts...)
}

func TestSandboxPreparationUnchangedRequestBudget(t *testing.T) {
	for _, admin := range []bool{false, true} {
		t.Run(fmt.Sprintf("admin=%v", admin), func(t *testing.T) {
			f := newPreparationFixture(t, "hermes")
			if admin {
				f.makeAdmin(t)
			}
			if err := f.reconcile(); err != nil {
				t.Fatal(err)
			}
			state := &corev1.ConfigMap{}
			key := client.ObjectKey{Namespace: "cetirun-system", Name: w.PodExpectationName(f.fixture.Namespace.Name, f.fixture.Namespace.UID)}
			if err := f.client.Get(context.Background(), key, state); err != nil {
				t.Fatal(err)
			}
			// A missing reuse proof takes the unchanged but fully guarded path.
			delete(state.Annotations, "cetirun.microsoft.com/sandbox-preparation-digest")
			if err := f.client.Update(context.Background(), state); err != nil {
				t.Fatal(err)
			}
			counts := &preparationRequestCounts{Client: f.client}
			f.reconciler.Client, f.reconciler.APIReader, f.installer.client = counts, counts, counts
			if err := f.reconcile(); err != nil {
				t.Fatal(err)
			}
			fullReads := counts.gets + counts.lists
			stages, renders, fences, digestReads := f.installer.stages, f.installer.renders, f.source.fences, f.installer.digestReads
			counts.gets, counts.lists, counts.writes = 0, 0, 0
			f.now = f.now.Add(time.Second)
			f.config.ObservedAt, f.source.inputs.ObservedAt = f.now, f.now
			if err := f.reconcile(); err != nil {
				t.Fatal(err)
			}
			sources := len(f.expectation(t).Sources)
			reads := counts.gets + counts.lists
			if counts.writes != 0 || reads > sources+25 || reads*2 >= fullReads || counts.lists != 1 ||
				f.installer.stages != stages || f.installer.renders != renders ||
				f.source.fences != fences+1 || f.installer.digestReads != digestReads+1 {
				t.Fatalf("unchanged preparation exceeded its read-only budget: reads=%d full=%d sources=%d writes=%d renders=%d stages=%d fences=%d",
					reads, fullReads, sources, counts.writes, f.installer.renders-renders, f.installer.stages-stages, f.source.fences-fences)
			}
			t.Logf("unchanged held cycle: %d controller API reads vs %d without reuse; %d writes, 1 release-store read, 1 fence", reads, fullReads, counts.writes)
		})
	}
}

func TestSandboxPreparationReuseRefreshesChangedIntentAndRelease(t *testing.T) {
	for name, change := range map[string]func(*testing.T, *preparationFixture){
		"reauthorization": func(t *testing.T, f *preparationFixture) {
			agent := f.current(t)
			reauthorize, err := sandboxbinding.FormatReauthorization("new-request", f.expectation(t).Binding)
			if err != nil {
				t.Fatal(err)
			}
			agent.Annotations[sandboxbinding.ReauthorizeAnnotation] = reauthorize
			if err := f.client.Update(context.Background(), agent); err != nil {
				t.Fatal(err)
			}
		},
		"render defaults": func(_ *testing.T, f *preparationFixture) {
			f.reconciler.CetiRunHelmValues["secrets"].(map[string]any)["apiBase"] = "https://api.example.org/v1"
		},
		"Helm store": func(_ *testing.T, f *preparationFixture) { f.installer.stageDigest = "changed-synthetic-release" },
		"Service": func(t *testing.T, f *preparationFixture) {
			service := &corev1.Service{}
			expected := f.expectation(t)
			if err := f.client.Get(context.Background(), client.ObjectKey{Namespace: expected.Namespace.Name, Name: expected.Workload.Name}, service); err != nil {
				t.Fatal(err)
			}
			service.Labels["example.com/changed"] = "true"
			if err := f.client.Update(context.Background(), service); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newPreparationFixture(t, "openclaw")
			if err := f.reconcile(); err != nil {
				t.Fatal(err)
			}
			stages := f.installer.stages
			change(t, f)
			if err := f.reconcile(); err != nil {
				t.Fatal(err)
			}
			if f.installer.stages != stages+1 {
				t.Fatal("changed preparation authority or release was skipped")
			}
			assertSandboxPrepared(t, f)
		})
	}
}

func TestSandboxPreparationReuseKeepsFreshFailureChecks(t *testing.T) {
	for name, change := range map[string]func(*testing.T, *preparationFixture){
		"fence unavailable": func(_ *testing.T, f *preparationFixture) { f.source.fenceErr = errors.New("synthetic missing fence") },
		"fence expires sources": func(_ *testing.T, f *preparationFixture) {
			f.source.onFence = func() { f.now = f.now.Add(time.Minute) }
		},
		"old Pod": func(t *testing.T, f *preparationFixture) {
			if err := f.client.Create(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Name: "old-writer", Namespace: f.fixture.Namespace.Name,
			}}); err != nil {
				t.Fatal(err)
			}
		},
		"release store unavailable": func(_ *testing.T, f *preparationFixture) {
			f.installer.digestErr = errors.New("synthetic unavailable release store")
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newPreparationFixture(t, "hermes")
			if err := f.reconcile(); err != nil {
				t.Fatal(err)
			}
			stages := f.installer.stages
			change(t, f)
			if err := f.reconcile(); err == nil {
				t.Fatal("a previous successful preparation concealed a current failure")
			}
			if f.installer.stages != stages || apimeta.IsStatusConditionTrue(f.current(t).Status.Conditions, api.ConditionAgentConfigReady) {
				t.Fatal("unsafe reuse restaged or reported successful preparation")
			}
		})
	}
}

func TestSandboxPreparationStatusChurnDoesNotRestageOrRewriteExpectation(t *testing.T) {
	f := newPreparationFixture(t, "openclaw")
	if err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	stages := f.installer.stages
	before, err := json.Marshal(f.expectation(t))
	if err != nil {
		t.Fatal(err)
	}
	agent := f.current(t)
	apimeta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
		Type: "ExampleObservation", Status: metav1.ConditionTrue, Reason: "Observed", Message: "synthetic observation",
	})
	if err := f.client.Status().Update(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	if err := f.reconcile(); err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(f.expectation(t))
	if err != nil || string(before) != string(after) || f.installer.stages != stages {
		t.Fatal("Agent status-only write forced a new expectation or stage")
	}
}
