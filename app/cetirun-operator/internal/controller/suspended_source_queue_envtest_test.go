// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package controller

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	crcontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// TestSuspendedUnavailableSourceStartupQueueAPIServer exercises the manager
// queue with its normal initial-list priority and status watch events. The
// Agent objects, manager and watches use a real API server. Each fixture keeps
// its Sandbox source and Gateway dependencies in an isolated in-memory client;
// status patches from the real AgentReconciler are mirrored to the API server
// so they become actual Agent update events without reproducing the complete
// protected Sandbox bootstrap inventory six times.
func TestSuspendedUnavailableSourceStartupQueueAPIServer(t *testing.T) {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		t.Fatal("KUBEBUILDER_ASSETS must name local envtest binaries")
	}
	environment := &envtest.Environment{
		BinaryAssetsDirectory: assets, UseExistingCluster: ptr.To(false),
		CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true,
	}
	config, err := environment.Start()
	if err != nil {
		_ = environment.Stop()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	writer, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	const agents = 6
	entries := make(map[string]*suspendedSourceQueueFixture, agents)
	for index := range agents {
		name := fmt.Sprintf("queue-suspended-%d", index)
		fixture := newSuspendedSourceQueueFixture(t, writer, name, index < 4)
		entries[name] = fixture
		if err := writer.Create(ctx, fixture.agent.DeepCopy()); err != nil {
			t.Fatal(err)
		}
		created := &api.Agent{}
		if err := writer.Get(ctx, client.ObjectKey{Name: name}, created); err != nil {
			t.Fatal(err)
		}
		created.Status = *fixture.agent.Status.DeepCopy()
		if err := writer.Status().Update(ctx, created); err != nil {
			t.Fatal(err)
		}
	}

	stop := startSuspendedSourceQueueManager(t, config, scheme, entries, "suspended-source-queue-first")
	waitForSuspendedSourceQueueProgress(t, entries, 1)
	settled := waitForSuspendedSourceQueueSettlement(t, writer, entries)
	for index := range 4 {
		fixture := entries[fmt.Sprintf("queue-suspended-%d", index)]
		if fixture.statusPatches() == 0 {
			t.Fatalf("stale blocked receipt %d did not produce an API-server status watch event", index)
		}
	}
	stop()

	// A fresh controller has no in-memory workqueue state. It must still
	// process every retained Agent once and must not manufacture a new status
	// event after the first startup has converged.
	for _, fixture := range entries {
		fixture.restart()
	}
	stop = startSuspendedSourceQueueManager(t, config, scheme, entries, "suspended-source-queue-restart")
	waitForSuspendedSourceQueueProgress(t, entries, 1)
	if after := waitForSuspendedSourceQueueSettlement(t, writer, entries); !sameSuspendedSourceQueueSnapshot(settled, after) {
		t.Fatalf("controller restart rewrote stable suspended status: before=%v after=%v", settled, after)
	}
	stop()
}

func startSuspendedSourceQueueManager(t *testing.T, config *rest.Config, scheme *runtime.Scheme, entries map[string]*suspendedSourceQueueFixture, name string) func() {
	t.Helper()
	mgr, err := ctrl.NewManager(config, ctrl.Options{Scheme: scheme,
		Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&api.Agent{}).
		WithOptions(controllerOptionsForSuspendedSourceQueue()).
		Complete(reconcile.Func(func(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
			fixture := entries[request.Name]
			if fixture == nil {
				return reconcile.Result{}, nil
			}
			return fixture.reconcile(ctx, request)
		})); err != nil {
		t.Fatal(err)
	}
	runtimeCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(runtimeCtx) }()
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(10 * time.Second):
				t.Error("controller manager did not stop")
			}
		})
	}
	t.Cleanup(stop)
	syncCtx, syncCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer syncCancel()
	if !mgr.GetCache().WaitForCacheSync(syncCtx) {
		stop()
		t.Fatal("manager cache did not sync")
	}
	return stop
}

type suspendedSourceQueueSnapshot map[string]struct {
	patches int
	rv      string
}

func waitForSuspendedSourceQueueProgress(t *testing.T, entries map[string]*suspendedSourceQueueFixture, minimum int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		allProcessed := true
		for _, fixture := range entries {
			if fixture.completed() < minimum {
				allProcessed = false
				break
			}
		}
		if allProcessed {
			for name, fixture := range entries {
				if err := fixture.err(); err != nil {
					t.Fatalf("Agent %s reconciliation failed: %v", name, err)
				}
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	for name, fixture := range entries {
		t.Logf("Agent %s: started=%d completed=%d patches=%d err=%v", name, fixture.calls(), fixture.completed(), fixture.statusPatches(), fixture.err())
	}
	t.Fatal("not every initial-list Agent completed reconciliation before the deadline")
}

func waitForSuspendedSourceQueueSettlement(t *testing.T, writer client.Client, entries map[string]*suspendedSourceQueueFixture) suspendedSourceQueueSnapshot {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		before := snapshotSuspendedSourceQueue(t, writer, entries)
		time.Sleep(500 * time.Millisecond)
		after := snapshotSuspendedSourceQueue(t, writer, entries)
		if sameSuspendedSourceQueueSnapshot(before, after) {
			return after
		}
	}
	t.Fatal("suspended source status did not settle")
	return nil
}

func snapshotSuspendedSourceQueue(t *testing.T, writer client.Client, entries map[string]*suspendedSourceQueueFixture) suspendedSourceQueueSnapshot {
	t.Helper()
	snapshot := make(suspendedSourceQueueSnapshot, len(entries))
	for name, fixture := range entries {
		current := &api.Agent{}
		if err := writer.Get(context.Background(), client.ObjectKey{Name: name}, current); err != nil {
			t.Fatal(err)
		}
		snapshot[name] = struct {
			patches int
			rv      string
		}{fixture.statusPatches(), current.ResourceVersion}
	}
	return snapshot
}

func sameSuspendedSourceQueueSnapshot(left, right suspendedSourceQueueSnapshot) bool {
	if len(left) != len(right) {
		return false
	}
	for name, before := range left {
		if before != right[name] {
			return false
		}
	}
	return true
}

func controllerOptionsForSuspendedSourceQueue() crcontroller.Options {
	return crcontroller.Options{MaxConcurrentReconciles: 4}
}

type suspendedSourceQueueFixture struct {
	reconciler     *AgentReconciler
	localClient    client.Client
	writer         client.Client
	agent          *api.Agent
	localName      string
	mu             sync.Mutex
	reconcileCalls int
	completedCalls int
	patches        int
	lastErr        error
}

func newSuspendedSourceQueueFixture(t *testing.T, writer client.Client, name string, staleReceipt bool) *suspendedSourceQueueFixture {
	t.Helper()
	fixture := readyPolicyRateLimitFixture(t, "openclaw", true)
	ctx := context.Background()
	current := fixture.current(t)
	current.Spec.Suspended = true
	current.Generation++
	if err := fixture.kube.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	updateContainerPlatformForTest(t, fixture.r, fixture.kube, func(document *SandboxPlatformConfiguration) {
		for backend, platform := range document.Platforms {
			platform.Topology.Gateway[0].Peer.PodSelector = map[string]string{"app.kubernetes.io/instance": "example-gateway"}
			document.Platforms[backend] = platform
		}
	})
	fixture.reconcileOK(t)
	current = fixture.current(t)
	if staleReceipt {
		// Model the write-ahead receipt from an interrupted pre-upgrade hold:
		// external Gateway enforcement is already blocked, but its final status
		// receipt has not been persisted. The first four workers turn this into
		// ordinary-priority watch traffic while two initial-list items remain.
		current.Status.AIGateway.Reason = aiGatewayReasonPolicyReprojection
		current.Status.AIGateway.Blocked = false
		if err := fixture.kube.Status().Update(ctx, current); err != nil {
			t.Fatal(err)
		}
		current = fixture.current(t)
	}
	current.Name = name
	current.ResourceVersion = ""
	current.UID = ""
	current.ManagedFields = nil
	current.CreationTimestamp = metav1.Time{}
	result := &suspendedSourceQueueFixture{
		reconciler: fixture.r, localClient: fixture.kube, writer: writer,
		agent: current, localName: fixture.agent.Name,
	}
	result.reconciler.Client = interceptor.NewClient(fixture.r.Client.(client.WithWatch), interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, object client.Object, patch client.Patch, options ...client.SubResourcePatchOption) error {
			if err := c.SubResource(subresource).Patch(ctx, object, patch, options...); err != nil {
				return err
			}
			if subresource != "status" {
				return nil
			}
			agent, ok := object.(*api.Agent)
			if !ok {
				return nil
			}
			current := &api.Agent{}
			if err := result.writer.Get(ctx, client.ObjectKey{Name: result.agent.Name}, current); err != nil {
				return err
			}
			current.Status = *agent.Status.DeepCopy()
			if err := result.writer.Status().Update(ctx, current); err != nil {
				return err
			}
			result.mu.Lock()
			result.patches++
			result.mu.Unlock()
			return nil
		},
	})
	return result
}

func (f *suspendedSourceQueueFixture) reconcile(ctx context.Context, request reconcile.Request) (result reconcile.Result, err error) {
	f.mu.Lock()
	f.reconcileCalls++
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.completedCalls++
		if err != nil {
			f.lastErr = err
		}
	}()
	current := &api.Agent{}
	if err := f.writer.Get(ctx, types.NamespacedName{Name: request.Name}, current); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	// Feed the last API-server receipt into this Agent's isolated lifecycle.
	local := &api.Agent{}
	if err := f.localClient.Get(ctx, client.ObjectKey{Name: f.localName}, local); err != nil {
		return reconcile.Result{}, err
	}
	local.Status = *current.Status.DeepCopy()
	if err := f.localClient.Status().Update(ctx, local); err != nil {
		return reconcile.Result{}, err
	}
	return f.reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKey{Name: f.localName}})
}

func (f *suspendedSourceQueueFixture) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reconcileCalls
}

func (f *suspendedSourceQueueFixture) completed() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.completedCalls
}

func (f *suspendedSourceQueueFixture) err() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastErr
}

func (f *suspendedSourceQueueFixture) statusPatches() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.patches
}

func (f *suspendedSourceQueueFixture) restart() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reconciler = restartEntraReconciler(f.reconciler)
	f.reconcileCalls, f.completedCalls, f.lastErr = 0, 0, nil
}
