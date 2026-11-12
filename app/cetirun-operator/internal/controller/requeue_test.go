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

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/ingressmtls"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/log"
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
