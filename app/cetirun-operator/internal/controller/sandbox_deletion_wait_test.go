// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// TestSandboxDeletionWaitsForPods reproduces #556 item 3: while the Pods of a
// deleted Sandbox Agent terminate, each pass retries after a short interval
// without a reconcile error, and the deletion is reported once. The first wait
// pass shows the wait in the Ready condition and logs it at Info level; later
// passes write nothing and log at V(1). An API error is still a reconcile
// error, and the deletion completes when the Pods are gone.
func TestSandboxDeletionWaitsForPods(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fixture  func(*testing.T) *nativeSandboxFixture
		recorded bool
		events   int
	}{
		// handleDelete. Like the test cluster of the issue, the fixture has no
		// native source coordination, whose own stop waits for the Pods first.
		{"common executor", func(t *testing.T) *nativeSandboxFixture {
			return readyPolicyRateLimitFixture(t, "openclaw", true)
		}, true, 1},
		// deleteSandboxBuiltin, for a retained bootstrap without preparation.
		// It reports no lifecycle events.
		{"retained bootstrap", func(t *testing.T) *nativeSandboxFixture {
			return newNativeSandboxFixture(t, true)
		}, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.fixture(t)
			info, verbose := 0, 0
			ctx := log.IntoContext(context.Background(), funcr.New(func(_, args string) {
				if strings.Contains(args, "Agent deletion waits for its Pods to terminate") {
					if strings.Contains(args, `"level"=0`) {
						info++
					} else {
						verbose++
					}
				}
			}, funcr.Options{Verbosity: 1}))
			agent := f.current(t)
			if recorded, err := f.r.recordedReconciledSandbox(ctx, agent); err != nil || recorded != tc.recorded ||
				!slices.Contains(agent.Finalizers, sandbox.BootstrapFinalizer) {
				t.Fatalf("the fixture does not take the expected deletion path: recorded=%t err=%v", recorded, err)
			}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "example-terminating-pod", Namespace: instanceNamespace(f.agent.Name)}}
			if err := f.kube.Create(ctx, pod); err != nil {
				t.Fatal(err)
			}
			recorder := record.NewFakeRecorder(100)
			f.r.EventRecorder = recorder
			if err := f.kube.Delete(ctx, agent); err != nil {
				t.Fatal(err)
			}
			request := ctrl.Request{NamespacedName: client.ObjectKey{Name: f.agent.Name}}
			version := ""
			for pass := range 3 {
				result, err := f.r.Reconcile(ctx, request)
				if err != nil || result.RequeueAfter != cleanupWaitRequeue {
					t.Fatalf("pass %d: the Pod wait is not a periodic retry: result=%+v err=%v", pass, result, err)
				}
				current := f.current(t)
				if pass == 0 {
					ready := apimeta.FindStatusCondition(current.Status.Conditions, api.ConditionReady)
					if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "PodsTerminating" ||
						ready.Message != "Deletion waits for the Pods of the Agent to terminate." {
						t.Fatalf("the first wait pass did not show the wait: %+v", ready)
					}
					version = current.ResourceVersion
				} else if current.ResourceVersion != version {
					t.Fatalf("pass %d wrote the Agent, but the wait did not change", pass)
				}
			}
			namespace := &corev1.Namespace{}
			if err := f.kube.Get(ctx, client.ObjectKey{Name: pod.Namespace}, namespace); err != nil || !namespace.DeletionTimestamp.IsZero() {
				t.Fatal("the namespace deletion started while a Pod remains:", err)
			}
			if phase := f.current(t).Status.Phase; phase != api.PhaseDeleting || info != 1 || verbose != 2 {
				t.Fatalf("the wait was not logged once: phase=%s info=%d verbose=%d", phase, info, verbose)
			}
			deleting := 0
			for len(recorder.Events) != 0 {
				if strings.Contains(<-recorder.Events, " InstanceDeleting ") {
					deleting++
				}
			}
			if deleting != tc.events {
				t.Fatalf("InstanceDeleting events = %d, want %d", deleting, tc.events)
			}

			// A failed Pod read is still a reconcile error.
			outage := errors.New("synthetic API outage")
			broken := interceptor.NewClient(f.kube, interceptor.Funcs{
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if _, ok := list.(*corev1.PodList); ok {
						return outage
					}
					return c.List(ctx, list, opts...)
				},
			})
			f.r.Client, f.r.APIReader = broken, broken
			if result, err := f.r.Reconcile(ctx, request); !errors.Is(err, outage) || result.RequeueAfter != 0 {
				t.Fatalf("a failed Pod read was hidden: result=%+v err=%v", result, err)
			}
			f.r.Client, f.r.APIReader = f.kube, f.kube

			// When the Pods are gone, the deletion removes the namespace and
			// then the finalizers, with optimistic locks after the wait writes.
			if err := f.kube.Delete(ctx, pod); err != nil {
				t.Fatal(err)
			}
			for pass := 0; ; pass++ {
				if pass == 5 {
					t.Fatalf("the deletion did not complete after the Pods terminated: finalizers=%v", f.current(t).Finalizers)
				}
				if _, err := f.r.Reconcile(ctx, request); err != nil {
					t.Fatalf("pass %d after the Pods terminated: %v", pass, err)
				}
				if err := f.kube.Get(ctx, request.NamespacedName, &api.Agent{}); apierrors.IsNotFound(err) {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				// The fake client has no namespace controller. When the deletion
				// of the namespace has started, remove its claims as that
				// controller would, so that the retained deletion can continue.
				terminating := &corev1.Namespace{}
				if err := f.kube.Get(ctx, client.ObjectKey{Name: pod.Namespace}, terminating); err != nil && !apierrors.IsNotFound(err) {
					t.Fatal(err)
				} else if err == nil && terminating.DeletionTimestamp.IsZero() {
					continue
				}
				claims := &corev1.PersistentVolumeClaimList{}
				if err := f.kube.List(ctx, claims, client.InNamespace(pod.Namespace)); err != nil {
					t.Fatal(err)
				}
				for i := range claims.Items {
					claim := &claims.Items[i]
					if len(claim.Finalizers) != 0 {
						claim.Finalizers = nil
						if err := f.kube.Update(ctx, claim); err != nil && !apierrors.IsNotFound(err) {
							t.Fatal(err)
						}
					}
					if err := f.kube.Delete(ctx, claim); err != nil && !apierrors.IsNotFound(err) {
						t.Fatal(err)
					}
				}
			}
			if err := f.kube.Get(ctx, client.ObjectKey{Name: pod.Namespace}, namespace); err == nil && namespace.DeletionTimestamp.IsZero() {
				t.Fatal("the namespace was not deleted")
			} else if err != nil && !apierrors.IsNotFound(err) {
				t.Fatal(err)
			}
			if info != 1 {
				t.Fatalf("the wait was logged at Info level %d times", info)
			}
		})
	}
}

// TestSandboxDeletionWaitRetriesAStatusConflict checks that a conflict on the
// status write of the Pod wait is a reconcile error only once: the next pass
// records the wait, and the passes after it write nothing.
func TestSandboxDeletionWaitRetriesAStatusConflict(t *testing.T) {
	f := readyPolicyRateLimitFixture(t, "openclaw", true)
	ctx := context.Background()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "example-terminating-pod", Namespace: instanceNamespace(f.agent.Name)}}
	if err := f.kube.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	f.r.EventRecorder = record.NewFakeRecorder(100)
	if err := f.kube.Delete(ctx, f.current(t)); err != nil {
		t.Fatal(err)
	}
	conflicts := 0
	conflicting := interceptor.NewClient(f.kube, interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, object client.Object, patch client.Patch, options ...client.SubResourcePatchOption) error {
			if agent, ok := object.(*api.Agent); ok && conflicts == 0 && cleanupWaitRecorded(agent) {
				conflicts++
				return apierrors.NewConflict(api.GroupVersion.WithResource("agents").GroupResource(), agent.Name,
					errors.New("synthetic concurrent write"))
			}
			return c.SubResource(subresource).Patch(ctx, object, patch, options...)
		},
	})
	f.r.Client, f.r.APIReader = conflicting, conflicting
	request := ctrl.Request{NamespacedName: client.ObjectKey{Name: f.agent.Name}}
	if result, err := f.r.Reconcile(ctx, request); !apierrors.IsConflict(err) || result.RequeueAfter != 0 || conflicts != 1 {
		t.Fatalf("the conflict was not returned once: result=%+v err=%v conflicts=%d", result, err, conflicts)
	}
	version := ""
	for pass := range 3 {
		if result, err := f.r.Reconcile(ctx, request); err != nil || result.RequeueAfter != cleanupWaitRequeue {
			t.Fatalf("pass %d after the conflict: result=%+v err=%v", pass, result, err)
		}
		current := f.current(t)
		if !cleanupWaitRecorded(current) {
			t.Fatalf("pass %d after the conflict did not show the wait: %+v", pass, current.Status.Conditions)
		}
		if pass == 0 {
			version = current.ResourceVersion
		} else if current.ResourceVersion != version {
			t.Fatalf("pass %d after the conflict wrote the Agent again", pass)
		}
	}
}

// TestCleanupWaitWritesOnlyItsCondition checks the status write of the Pod
// wait: it writes only the Ready condition, not other status that the pass
// changed in memory, and keeps a current Agent of the pass current. A copy
// that is older than the written wait writes nothing.
func TestCleanupWaitWritesOnlyItsCondition(t *testing.T) {
	f := readyPolicyRateLimitFixture(t, "openclaw", true)
	info := 0
	ctx := log.IntoContext(context.Background(), funcr.New(func(_, args string) {
		if strings.Contains(args, "Agent deletion waits for its Pods to terminate") && strings.Contains(args, `"level"=0`) {
			info++
		}
	}, funcr.Options{Verbosity: 1}))
	stale := f.current(t)
	pass := stale.DeepCopy()
	pass.Status.AccessURL = "https://unwritten.example.invalid/"
	if result, err := f.r.cleanupWaitResult(ctx, pass, errSandboxCleanupWaitsForPods); err != nil || result.RequeueAfter != cleanupWaitRequeue {
		t.Fatalf("the wait is not a periodic retry: result=%+v err=%v", result, err)
	}
	written := f.current(t)
	if !cleanupWaitRecorded(written) || written.Status.AccessURL != stale.Status.AccessURL || info != 1 {
		t.Fatalf("the wait was not written alone: access=%q info=%d conditions=%+v", written.Status.AccessURL, info, written.Status.Conditions)
	}
	if pass.ResourceVersion != written.ResourceVersion || !cleanupWaitRecorded(pass) {
		t.Fatal("the current Agent of the pass did not stay current")
	}
	if result, err := f.r.cleanupWaitResult(ctx, stale, errSandboxCleanupWaitsForPods); err != nil || result.RequeueAfter != cleanupWaitRequeue {
		t.Fatalf("an older copy did not retry: result=%+v err=%v", result, err)
	}
	if current := f.current(t); current.ResourceVersion != written.ResourceVersion || info != 1 || stale.ResourceVersion == written.ResourceVersion {
		t.Fatalf("an older copy wrote or logged the wait again: info=%d", info)
	}
	outage := errors.New("synthetic API outage")
	if result, err := f.r.cleanupWaitResult(ctx, f.current(t), outage); !errors.Is(err, outage) || result.RequeueAfter != 0 {
		t.Fatalf("another error was not returned: result=%+v err=%v", result, err)
	}
}
