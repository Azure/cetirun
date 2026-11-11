// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"fmt"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/ingressmtls"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

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

// TestNativeStartWaitsForSkillsPublication reproduces #493: a Team without an
// approved Skills publication must not retry through the error rate limiter.
func TestNativeStartWaitsForSkillsPublication(t *testing.T) {
	f, _ := newUnreconciledSandboxRuntimeFixture(t, true, nil, false, "openclaw", true, false, withUnpublishedSkillsCatalog(t))
	// The retained single-start path starts through startKubernetesSandbox.
	f.seedRetainedBootstrap(t)
	f.projectGateway(t)
	ctx := context.Background()
	for pass := range 3 {
		result, err := f.r.startKubernetesSandbox(ctx, f.current(t))
		if err != nil || result.RequeueAfter <= 0 || result.RequeueAfter > prerequisiteRequeue {
			t.Fatalf("pass %d: missing Skills publication did not wait: result=%+v err=%v", pass, result, err)
		}
	}
	// The wait is logged once: a repeated pass does not change its result.
	unpublished := fmt.Errorf("%w: %w", sandboxpolicy.ErrUnavailable, errSkillsPublicationNotApproved)
	if changed, err := f.r.recordSandboxPreparationStatus(ctx, f.current(t), false, unpublished); err != nil || changed {
		t.Fatalf("a repeated wait reported a changed result: changed=%t err=%v", changed, err)
	}
	agent := f.current(t)
	ready := apimeta.FindStatusCondition(agent.Status.Conditions, api.ConditionSandboxReady)
	if agent.Status.Phase == api.PhaseReady || ready == nil || ready.Status == metav1.ConditionTrue ||
		ready.ObservedGeneration != agent.Generation {
		t.Fatalf("missing Skills publication was not recorded: phase=%s condition=%+v", agent.Status.Phase, ready)
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
	if result, err := f.r.startKubernetesSandbox(ctx, f.current(t)); !errors.Is(err, outage) || result.RequeueAfter != 0 {
		t.Fatalf("a transient API error was hidden: result=%+v err=%v", result, err)
	}
}
