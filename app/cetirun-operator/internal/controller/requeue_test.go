// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/ingressmtls"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxcoordination"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
	"github.com/go-logr/logr/funcr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func TestReconciledSandboxSuspensionProgressDoesNotBackoff(t *testing.T) {
	for _, backend := range []string{"kata", "container"} {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()
			f := customTerminalFixture(t, backend)
			readyCustomWorkload(t, f)
			for range 3 {
				if _, err := f.reconcile(); err == nil {
					t.Fatal("fixture must retain its terminal provisioning failure")
				}
			}
			agent := f.current(t)
			keyID := agent.Status.AIGateway.KeyID
			agent.Spec.Suspended = true
			agent.Generation++
			if err := f.kube.Update(ctx, agent); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				result, err := f.reconcile()
				if err != nil || result.RequeueAfter != 5*time.Second {
					t.Fatalf("ordinary shutdown progress used error backoff: result=%+v err=%v", result, err)
				}
				ticket, found, err := f.r.SandboxNativeCoordination.ReadNative(ctx, agent.UID)
				if err != nil || !found || ticket.Reservation.Phase != sandboxcoordination.Stopping {
					t.Fatal("unacknowledged shutdown released its reservation:", err)
				}
			}
			main := f.workload(t)
			main.Status.ObservedGeneration, main.Status.Replicas = main.Generation, 0
			if err := f.kube.Status().Update(ctx, main); err != nil {
				t.Fatal(err)
			}
			terminal := customTerminalDeployment(t, f)
			terminal.Status = appsv1.DeploymentStatus{ObservedGeneration: terminal.Generation}
			if err := f.kube.Status().Update(ctx, terminal); err != nil {
				t.Fatal(err)
			}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pending-termination", Namespace: main.Namespace}}
			if err := f.kube.Create(ctx, pod); err != nil {
				t.Fatal(err)
			}
			result, err := f.reconcile()
			if err != nil || result.RequeueAfter != 5*time.Second || f.current(t).Status.Phase == api.PhaseSuspended {
				t.Fatalf("Pod termination was not a bounded pending stop: result=%+v err=%v", result, err)
			}
			if err := f.kube.Delete(ctx, pod); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				f.reconcileOK(t)
			}
			current := f.current(t)
			if current.Status.Phase != api.PhaseSuspended || current.Status.ObservedGeneration != current.Generation ||
				current.Status.AIGateway.KeyID != keyID || !current.Status.AIGateway.Blocked {
				t.Fatal("confirmed shutdown did not acknowledge the retained Agent")
			}
			if _, found, err := f.r.SandboxNativeCoordination.ReadNative(ctx, agent.UID); err != nil || found {
				t.Fatal("confirmed shutdown retained its stopping reservation:", err)
			}
		})
	}
}

func TestSuspensionProgressDoesNotHideJoinedFailures(t *testing.T) {
	progress := fmt.Errorf("observe shutdown: %w", errSandboxShutdownPending)
	failure := apierrors.NewServiceUnavailable("synthetic API failure")
	for _, test := range []struct {
		name string
		err  error
		wait bool
	}{
		{"progress", progress, true},
		{"joined progress", errors.Join(progress, progress), true},
		{"wrapped progress", fmt.Errorf("suspend: %w", errors.Join(progress, progress)), true},
		{"API failure", errors.Join(progress, failure), false},
		{"wrapped API failure", fmt.Errorf("suspend: %w", errors.Join(progress, failure)), false},
		{"ownership failure", errors.Join(progress, errors.New("synthetic ownership mismatch")), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := reconciledSuspensionResult(test.err)
			if test.wait {
				if err != nil || result.RequeueAfter != 5*time.Second {
					t.Fatalf("expected bounded progress: result=%+v err=%v", result, err)
				}
			} else if err == nil || result.RequeueAfter != 0 || !errors.Is(err, test.err) {
				t.Fatalf("real failure was hidden by progress: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestPrerequisiteWaitClassification(t *testing.T) {
	unpublished := fmt.Errorf("%w: %w", sandboxpolicy.ErrUnavailable, errSkillsPublicationNotApproved)
	for _, test := range []struct {
		name string
		err  error
		wait bool
	}{
		{"missing Skills publication", unpublished, true},
		{"wrapped missing Skills publication", fmt.Errorf("bootstrap Sandbox prerequisites: %w", unpublished), true},
		{"Skills claim pending", errSkillsClaimPending, true},
		{"mTLS approval pending", fmt.Errorf("%w: %w", sandboxpolicy.ErrUnavailable, ingressmtls.ErrApprovalPending), true},
		{"recorded publication needs recovery", fmt.Errorf("%w: %w", errSkillsPublicationRecovery, unpublished), false},
		{"wait joined with an API error", errors.Join(unpublished, errors.New("synthetic API outage")), false},
		{"outage", fmt.Errorf("%w: read catalog", sandboxpolicy.ErrUnavailable), false},
		{"unsupported control", sandboxpolicy.ErrUnsupportedControl, false},
		{"API error", errors.New("synthetic API outage"), false},
		{"nil", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := prerequisiteWait(test.err); got != test.wait {
				t.Fatalf("prerequisiteWait = %t, want %t", got, test.wait)
			}
			result, err := prerequisiteResult(context.Background(), test.err, true, nil)
			if test.wait && (err != nil || result.RequeueAfter != prerequisiteRequeue) {
				t.Fatalf("wait returned result=%+v err=%v", result, err)
			}
			if !test.wait && (result.RequeueAfter != 0 || !errors.Is(err, test.err)) {
				t.Fatalf("failure returned result=%+v err=%v", result, err)
			}
			statusErr := errors.New("synthetic status write failure")
			if result, err := prerequisiteResult(context.Background(), test.err, false, statusErr); result.RequeueAfter != 0 || !errors.Is(err, statusErr) {
				t.Fatalf("a failed status write was hidden: result=%+v err=%v", result, err)
			}
		})
	}
}

func withUnpublishedSkillsCatalog(t *testing.T) func(*AgentReconciler, client.Client) {
	t.Helper()
	return func(r *AgentReconciler, kube client.Client) {
		catalog := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "skills-catalog", Namespace: r.OperatorNamespace},
			Data:       map[string]string{SandboxSkillsCatalogKey: `{"version":"v1","teams":{}}`},
		}
		if err := kube.Create(context.Background(), catalog); err != nil {
			t.Fatal(err)
		}
		r.SandboxPreparation.bootstrap.SkillsCatalogConfigMapName = catalog.Name
		r.SandboxPreparation.Prerequisites.(*KubernetesSandboxPrerequisites).SkillsCatalogConfigMapName = catalog.Name
	}
}

// TestNativeAgentWaitsForSkillsPublication reproduces #493 on the path of a new
// native Agent: common reconciliation holds it at the owned bootstrap while its
// Team selects Skills without an approved publication. Each caller of that
// bootstrap must retry periodically, not through the error rate limiter.
func TestNativeAgentWaitsForSkillsPublication(t *testing.T) {
	for _, path := range []string{"namespace", "registry credentials", "pending ingress mTLS approval"} {
		t.Run(path, func(t *testing.T) {
			f, _ := newUnreconciledSandboxRuntimeFixture(t, true, []string{"example-skill"}, true, "openclaw", true, false,
				withSkillMarketplace, withUnpublishedSkillsCatalog(t))
			switch path {
			case "registry credentials":
				agent := f.current(t)
				agent.Spec.Agent.RegistrySecretRef = &api.RegistrySecretRef{Name: "registry-source-example"}
				if err := f.kube.Update(context.Background(), agent); err != nil {
					t.Fatal(err)
				}
			case "pending ingress mTLS approval":
				withIngressMTLS(t, "openclaw")(f.r, f.kube)
				updateContainerPlatformForTest(t, f.r, f.kube, func(document *SandboxPlatformConfiguration) {
					registration := document.Platforms[api.SandboxKata].IngressMTLS
					registration.Bindings = registration.Bindings[:0]
				})
			}
			logged := 0
			ctx := log.IntoContext(context.Background(), funcr.New(func(_, args string) {
				if strings.Contains(args, "Agent waits for a platform prerequisite") {
					logged++
				}
			}, funcr.Options{}))
			request := ctrl.Request{NamespacedName: client.ObjectKey{Name: f.agent.Name}}
			version := ""
			for pass := range 3 {
				result, err := f.r.Reconcile(ctx, request)
				if err != nil || result.RequeueAfter <= 0 || result.RequeueAfter > prerequisiteRequeue {
					t.Fatalf("pass %d: missing Skills publication did not wait: result=%+v err=%v", pass, result, err)
				}
				// A repeated wait writes nothing: no watch event, no second log line.
				current := f.current(t).ResourceVersion
				if pass > 0 && current != version {
					t.Fatalf("pass %d: a repeated wait changed the Agent", pass)
				}
				version = current
			}
			agent := f.current(t)
			held := apimeta.FindStatusCondition(agent.Status.Conditions, api.ConditionSandboxReady)
			if agent.Status.Phase != api.PhasePending || held == nil || held.Status != metav1.ConditionFalse || held.Reason != "SandboxHeld" ||
				!slices.Contains(agent.Finalizers, sandbox.HoldFinalizer) || f.installer.stages != 0 {
				t.Fatalf("missing Skills publication was not recorded as a hold: phase=%s condition=%+v", agent.Status.Phase, held)
			}
			// Only a policy that does not resolve adds its reason to the hold
			// text (#556). Here only the mTLS approval holds the policy.
			want := reconciledSandboxHeldMessage
			if path == "pending ingress mTLS approval" {
				resolved := apimeta.FindStatusCondition(agent.Status.Conditions, api.ConditionPolicyResolved)
				if resolved == nil || resolved.Status != metav1.ConditionFalse {
					t.Fatalf("the pending mTLS approval did not hold the policy: %+v", resolved)
				}
				want += ": " + resolved.Message
			}
			if held.Message != want {
				t.Fatalf("the hold text does not match its cause: %q, want %q", held.Message, want)
			}
			if err := f.kube.Get(ctx, client.ObjectKey{Name: instanceNamespace(agent.Name)}, &corev1.Namespace{}); !apierrors.IsNotFound(err) {
				t.Fatal("bootstrap continued without the Skills publication:", err)
			}

			// A transient API error is still a reconcile error.
			outage := errors.New("synthetic API outage")
			broken := interceptor.NewClient(f.kube, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
					if _, ok := object.(*corev1.ConfigMap); ok && key.Name == "skills-catalog" {
						return outage
					}
					return c.Get(ctx, key, object, opts...)
				},
			})
			f.r.Client, f.r.APIReader = broken, broken
			if result, err := f.r.Reconcile(ctx, request); !errors.Is(err, outage) || result.RequeueAfter != 0 {
				t.Fatalf("a transient API error was hidden: result=%+v err=%v", result, err)
			}
			if logged != 1 {
				t.Fatalf("the wait was logged %d times, want once", logged)
			}
		})
	}
}

// waitingPrerequisites returns err instead of the Sandbox prerequisites, for
// example the classified wait of a Team without an approved Skills publication.
type waitingPrerequisites struct {
	*KubernetesSandboxPrerequisites
	err      error
	resolves int
}

func (p *waitingPrerequisites) Resolve(context.Context, *api.Agent, *api.Team) (SandboxPrerequisites, error) {
	p.resolves++
	return SandboxPrerequisites{}, p.err
}

// TestRetainedNativeAgentWaitsForSkillsPublication covers the wait branch of
// startKubernetesSandbox (#508, #556 item 4): the start of a retained
// single-start Agent waits for a Skills publication. Each pass retries
// periodically without an error. The Agent stays held, nothing is staged, and
// a repeated wait writes nothing and is not logged again. An API error is
// still a reconcile error.
func TestRetainedNativeAgentWaitsForSkillsPublication(t *testing.T) {
	f := newNativeSandboxFixture(t, true)
	f.projectGateway(t)
	if !slices.Contains(f.current(t).Finalizers, sandbox.HoldFinalizer) {
		t.Fatal("the retained fixture must be held before its start")
	}
	wait := &waitingPrerequisites{
		KubernetesSandboxPrerequisites: f.r.SandboxPreparation.Prerequisites.(*KubernetesSandboxPrerequisites),
		err:                            fmt.Errorf("%w: %w", sandboxpolicy.ErrUnavailable, errSkillsPublicationNotApproved),
	}
	f.r.SandboxPreparation.Prerequisites = wait
	logged := 0
	ctx := log.IntoContext(context.Background(), funcr.New(func(_, args string) {
		if strings.Contains(args, "Agent waits for a platform prerequisite") {
			logged++
		}
	}, funcr.Options{}))
	request := ctrl.Request{NamespacedName: client.ObjectKey{Name: f.agent.Name}}
	stages, version := f.installer.stages, ""
	for pass := range 3 {
		result, err := f.r.Reconcile(ctx, request)
		if err != nil || result.RequeueAfter <= 0 || result.RequeueAfter > prerequisiteRequeue {
			t.Fatalf("pass %d: the retained start did not wait: result=%+v err=%v", pass, result, err)
		}
		current := f.current(t).ResourceVersion
		if pass > 0 && current != version {
			t.Fatalf("pass %d: a repeated wait changed the Agent", pass)
		}
		version = current
	}
	agent := f.current(t)
	held := apimeta.FindStatusCondition(agent.Status.Conditions, api.ConditionSandboxReady)
	if wait.resolves == 0 || !slices.Contains(agent.Finalizers, sandbox.HoldFinalizer) || agent.Status.Phase != api.PhasePending ||
		held == nil || held.Status != metav1.ConditionUnknown || held.Reason != "ObservationUnavailable" || f.installer.stages != stages {
		t.Fatalf("the waiting start did not stay held: resolves=%d phase=%s condition=%+v", wait.resolves, agent.Status.Phase, held)
	}
	f.assertNotReady(t)
	if receipt, _, err := f.r.readSandboxPreparationReceipt(context.Background(), agent); err == nil && receipt.Annotations[nativeStartAnnotation] != "" {
		t.Fatal("the waiting start recorded a native start")
	} else if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	if logged != 1 {
		t.Fatalf("the wait was logged %d times, want once", logged)
	}

	// An API error is still a reconcile error.
	outage := errors.New("synthetic API outage")
	wait.err = outage
	if result, err := f.r.Reconcile(ctx, request); !errors.Is(err, outage) || result.RequeueAfter != 0 {
		t.Fatalf("an API error was hidden: result=%+v err=%v", result, err)
	}
}
