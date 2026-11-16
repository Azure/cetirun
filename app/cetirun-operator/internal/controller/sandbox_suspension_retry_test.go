// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxcoordination"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller"
)

func TestShutdownPendingPreservesJoinedFailures(t *testing.T) {
	wait := shutdownPendingError("waiting for acknowledged shutdown")
	failure := errors.New("synthetic cleanup failure")
	for _, tc := range []struct {
		name    string
		err     error
		pending bool
	}{
		{"complete", nil, false},
		{"wait", wait, true},
		{"wrapped wait", fmt.Errorf("observe: %w", wait), true},
		{"joined waits", errors.Join(wait, fmt.Errorf("observe: %w", wait)), true},
		{"ordinary failure", failure, false},
		{"same diagnostic without type", errors.New(wait.Error()), false},
		{"wait then failure", errors.Join(wait, failure), false},
		{"failure then wait", errors.Join(failure, wait), false},
		{"nested failure", fmt.Errorf("stop: %w", errors.Join(wait, errors.Join(failure, wait))), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := onlyShutdownPending(tc.err); got != tc.pending {
				t.Fatalf("onlyShutdownPending(%v) = %t, want %t", tc.err, got, tc.pending)
			}
		})
	}
}

func TestReconciledSandboxShutdownProgressRequiresOwnedObservations(t *testing.T) {
	for _, scenario := range []string{"complete", "StatefulSet acknowledgement", "StatefulSet replicas", "Deployment acknowledgement",
		"Pod termination", "ReplicaSet removal", "foreign workload", "replaced workload", "API read failure"} {
		t.Run(scenario, func(t *testing.T) {
			agent, inputs, namespace, home, main := nativeQuiescenceObjects()
			main.Generation, main.Status.ObservedGeneration = 2, 2
			aux := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "terminal", Namespace: namespace.Name, UID: "terminal-uid", Generation: 2,
					OwnerReferences: []metav1.OwnerReference{w.AgentOwner(agent.Name, agent.UID)}},
				Spec:   appsv1.DeploymentSpec{Replicas: ptr.To[int32](0)},
				Status: appsv1.DeploymentStatus{ObservedGeneration: 2},
			}
			expected := w.ReconcileExpectation{
				Namespace: w.ObjectIdentity{Name: namespace.Name, UID: namespace.UID}, Home: inputs.Home,
				Workloads: []w.RenderedWorkload{
					{Kind: "StatefulSet", Identity: w.ObjectIdentity{Name: main.Name, UID: main.UID}},
					{Kind: "Deployment", Identity: w.ObjectIdentity{Name: aux.Name, UID: aux.UID}},
				},
			}
			objects := []client.Object{namespace, home, main, aux}
			pending := true
			switch scenario {
			case "complete":
				pending = false
			case "StatefulSet acknowledgement":
				main.Status.ObservedGeneration--
			case "StatefulSet replicas":
				main.Status.Replicas = 1
			case "Deployment acknowledgement":
				aux.Status.ObservedGeneration--
			case "Pod termination":
				objects = append(objects, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "remaining-pod", Namespace: namespace.Name}})
			case "ReplicaSet removal":
				objects = []client.Object{namespace, home, main, &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
					Name: "remaining-rs", Namespace: namespace.Name,
					OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: aux.Name, UID: aux.UID}},
				}}}
			case "foreign workload":
				// Ownership failure must win even while shutdown is pending.
				main.Status.Replicas = 1
				main.OwnerReferences = nil
				pending = false
			case "replaced workload":
				main.Status.Replicas = 1
				main.UID = "replacement-uid"
				pending = false
			case "API read failure":
				pending = false
			}
			kube := fake.NewClientBuilder().WithObjects(objects...).Build()
			var reader client.Reader = kube
			injected := errors.New("synthetic API read failure")
			if scenario == "API read failure" {
				reader = interceptor.NewClient(kube, interceptor.Funcs{
					Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
						return injected
					},
				})
			}
			r := &AgentReconciler{Client: kube, APIReader: reader}
			err := r.observeReconciledSandboxShutdown(context.Background(), agent, &corev1.ConfigMap{}, expected)
			if (err == nil) != (scenario == "complete") || onlyShutdownPending(err) != pending {
				t.Fatalf("unexpected shutdown classification: err=%v, want pending=%t", err, pending)
			}
			if scenario == "API read failure" && !errors.Is(err, injected) {
				t.Fatalf("lost API failure: %v", err)
			}
		})
	}
}

type suspensionRetry struct {
	delay       time.Duration
	rateLimited bool
}

// Observe controller-runtime's scheduling decisions without waiting through
// minutes of error backoff. The test explicitly delivers each next attempt.
type suspensionRetryQueue struct {
	workqueue.TypedRateLimitingInterface[ctrl.Request]
	limiter workqueue.TypedRateLimiter[ctrl.Request]
	retries chan suspensionRetry
}

func (q *suspensionRetryQueue) AddAfter(_ ctrl.Request, delay time.Duration) {
	q.retries <- suspensionRetry{delay: delay}
}

func (q *suspensionRetryQueue) AddRateLimited(request ctrl.Request) {
	q.retries <- suspensionRetry{delay: q.limiter.When(request), rateLimited: true}
}

func TestReconciledSandboxSuspensionResetsProvisioningBackoff(t *testing.T) {
	f := customTerminalFixture(t, "container")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	main := f.workload(t)
	main.Status = appsv1.StatefulSetStatus{ObservedGeneration: main.Generation, Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1}
	if err := f.kube.Status().Update(ctx, main); err != nil {
		t.Fatal(err)
	}
	// The main workload is observed, but its terminal dependency never becomes
	// ready. This is a persistent provisioning error on the real Custom path.
	request := ctrl.Request{NamespacedName: client.ObjectKey{Name: f.agent.Name}}
	limiter := workqueue.DefaultTypedControllerRateLimiter[ctrl.Request]()
	q := &suspensionRetryQueue{
		TypedRateLimitingInterface: workqueue.NewTypedRateLimitingQueue(limiter),
		limiter:                    limiter, retries: make(chan suspensionRetry, 1),
	}
	c, err := controller.NewUnmanaged("suspension-backoff-test", controller.Options{
		Reconciler: f.r, RateLimiter: limiter, UsePriorityQueue: ptr.To(false), SkipNameValidation: ptr.To(true),
		NewQueue: func(string, workqueue.TypedRateLimiter[ctrl.Request]) workqueue.TypedRateLimitingInterface[ctrl.Request] {
			return q
		},
	})
	if err != nil {
		q.ShutDown()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("controller did not stop")
		}
	})
	attempt := func() suspensionRetry {
		t.Helper()
		q.Add(request)
		select {
		case retry := <-q.retries:
			return retry
		case <-ctx.Done():
			t.Fatal("controller did not schedule its next attempt")
			return suspensionRetry{}
		}
	}
	var previous suspensionRetry
	for range 20 {
		previous = attempt()
		if !previous.rateLimited {
			t.Fatalf("provisioning failure did not accumulate error backoff: retry=%+v, status=%+v", previous, f.current(t).Status)
		}
	}
	if previous.delay < 10*time.Minute || limiter.NumRequeues(request) != 20 {
		t.Fatalf("fixture did not accumulate prolonged error backoff: %+v", previous)
	}
	agent := f.current(t)
	agent.Spec.Suspended = true
	agent.Generation++
	if err := f.kube.Update(ctx, agent); err != nil {
		t.Fatal(err)
	}
	retry := attempt()
	if retry.rateLimited || retry.delay != 5*time.Second || limiter.NumRequeues(request) != 0 {
		t.Fatalf("normal suspension inherited provisioning backoff: retry=%+v, failures=%d", retry, limiter.NumRequeues(request))
	}
	pending := f.current(t)
	if pending.Status.Phase != api.PhasePending || pending.Status.ObservedGeneration == pending.Generation {
		t.Fatalf("unacknowledged shutdown completed suspension: %+v", pending.Status)
	}
	ticket, found, err := f.r.SandboxNativeCoordination.ReadNative(ctx, agent.UID)
	if err != nil || !found || ticket.Reservation.Phase != sandboxcoordination.Stopping {
		t.Fatalf("unacknowledged shutdown lost its stopping reservation: found=%t, err=%v", found, err)
	}
	main = f.workload(t)
	main.Status = appsv1.StatefulSetStatus{ObservedGeneration: main.Generation}
	if err := f.kube.Status().Update(ctx, main); err != nil {
		t.Fatal(err)
	}
	if retry := attempt(); retry.rateLimited {
		t.Fatalf("acknowledged shutdown returned to error backoff: %+v", retry)
	}
	current := f.current(t)
	if current.Status.Phase != api.PhaseSuspended || current.Status.ObservedGeneration != current.Generation {
		t.Fatalf("acknowledged shutdown did not complete suspension: %+v", current.Status)
	}
	if _, found, err := f.r.SandboxNativeCoordination.ReadNative(ctx, agent.UID); err != nil || found {
		t.Fatalf("completed suspension retained its reservation: found=%t, err=%v", found, err)
	}
}
