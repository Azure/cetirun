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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// TestSandboxDeletionWaitsForPods reproduces #556 item 3: while the Pods of a
// deleted Sandbox Agent terminate, each pass retries after a short interval
// without a reconcile error, and the deletion is reported once. An API error
// is still a reconcile error.
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
			waits := 0
			ctx := log.IntoContext(context.Background(), funcr.New(func(_, args string) {
				if strings.Contains(args, "Agent deletion waits for its Pods to terminate") {
					waits++
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
			for pass := range 3 {
				result, err := f.r.Reconcile(ctx, request)
				if err != nil || result.RequeueAfter != cleanupWaitRequeue {
					t.Fatalf("pass %d: the Pod wait is not a periodic retry: result=%+v err=%v", pass, result, err)
				}
			}
			namespace := &corev1.Namespace{}
			if err := f.kube.Get(ctx, client.ObjectKey{Name: pod.Namespace}, namespace); err != nil || !namespace.DeletionTimestamp.IsZero() {
				t.Fatal("the namespace deletion started while a Pod remains:", err)
			}
			if phase := f.current(t).Status.Phase; phase != api.PhaseDeleting || waits != 3 {
				t.Fatalf("the wait was not recorded: phase=%s waits=%d", phase, waits)
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

			// When the Pods are gone, the deletion continues with the namespace.
			if err := f.kube.Delete(ctx, pod); err != nil {
				t.Fatal(err)
			}
			if _, err := f.r.Reconcile(ctx, request); err != nil {
				t.Fatal("the deletion did not continue after the Pods terminated:", err)
			}
			if err := f.kube.Get(ctx, client.ObjectKey{Name: pod.Namespace}, namespace); err == nil && namespace.DeletionTimestamp.IsZero() {
				t.Fatal("the namespace deletion did not start after the Pods terminated")
			} else if err != nil && !apierrors.IsNotFound(err) {
				t.Fatal(err)
			}
		})
	}
}
