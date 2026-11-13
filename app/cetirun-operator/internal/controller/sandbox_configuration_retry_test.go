// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/log"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/controller"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
)

// unavailableConfigurationRequeue is the retry interval of an AgentPolicy or a
// Sandbox whose platform prerequisites are unavailable.
const unavailableConfigurationRequeue = 30 * time.Second

// configurationRetryLog keeps the retry log lines of the AgentPolicy and
// Sandbox reconcilers, and counts them by kind at Info level and at V(1).
type configurationRetryLog struct {
	info, verbose map[string]int
	lines         []string
}

func captureConfigurationRetries() (context.Context, *configurationRetryLog) {
	capture := &configurationRetryLog{info: map[string]int{}, verbose: map[string]int{}}
	ctx := log.IntoContext(context.Background(), funcr.New(func(_, args string) {
		for _, kind := range []string{"AgentPolicy", "Sandbox"} {
			if !strings.Contains(args, `"msg"="`+kind+` waits for platform prerequisites`) {
				continue
			}
			capture.lines = append(capture.lines, args)
			if strings.Contains(args, `"level"=0`) {
				capture.info[kind]++
			} else {
				capture.verbose[kind]++
			}
		}
	}, funcr.Options{Verbosity: 1}))
	return ctx, capture
}

// TestUnusableRegistrationRetriesPoliciesAndSandboxesPeriodically reproduces a
// finding of the #556 test on a cluster: while the platform registration could
// not be used, the AgentPolicy and Sandbox reconcilers returned an error. The
// error rate limiter then delayed the next assessment by up to about 17
// minutes, also after the registration was corrected. A pass retries after a
// fixed interval without an error. Only the first pass writes the Unknown
// condition and logs it at Info level. A failed status write is still an
// error, and the first pass after the correction accepts the policy and the
// Sandbox again.
func TestUnusableRegistrationRetriesPoliciesAndSandboxesPeriodically(t *testing.T) {
	const canary = "value-canary-5d1f"
	const reason = "invalid Sandbox platform declaration: a selector-bound AI Gateway peer must select the protected " +
		sandboxpolicy.PeerLabel + " Pod label"
	box, snapshot, objects := nativeConfigurationSnapshot()
	valid := kubernetesPlatformJSON(t, controller.SandboxPlatformConfiguration{
		Version: "v1", Registrations: snapshot.Registrations, Platforms: snapshot.Platforms,
		RuntimeClasses: snapshot.RuntimeClasses, StorageClasses: snapshot.StorageClasses,
	})
	var unusable controller.SandboxPlatformConfiguration
	if err := json.Unmarshal([]byte(valid), &unusable); err != nil {
		t.Fatal(err)
	}
	platform := unusable.Platforms[api.SandboxKata]
	platform.Topology.Gateway[0].Peer.PodSelector = map[string]string{"selector-canary.example.com/key": canary}
	unusable.Platforms[api.SandboxKata] = platform
	registration := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "platform-system", Name: "sandbox-platform", UID: "native-platform-uid"},
		Data:       map[string]string{controller.SandboxPlatformConfigurationKey: valid},
	}
	policy := &api.AgentPolicy{ObjectMeta: metav1.ObjectMeta{Name: "example-policy", UID: "example-policy-uid", Generation: 2},
		Spec: api.AgentPolicySpec{Harnesses: api.AgentPolicyHarnesses{Allowed: []api.AgentType{api.AgentTypeOpenClaw}}}}
	scheme := configurationScheme(t)
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	writes := map[string]int{}
	writeFailure := errors.New("synthetic status write failure")
	failWrites := false
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(box, policy).
		WithObjects(append(objects, box, policy, registration)...).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, object client.Object, patch client.Patch, options ...client.SubResourcePatchOption) error {
				if failWrites {
					return writeFailure
				}
				writes[fmt.Sprintf("%T", object)]++
				return c.SubResource(subresource).Patch(ctx, object, patch, options...)
			},
		}).Build()
	now := func() time.Time { return snapshot.ObservedAt }
	source := &controller.KubernetesSandboxConfigurationSource{
		APIReader: kube, Namespace: registration.Namespace, Name: registration.Name, Now: now,
	}
	policies := &controller.AgentPolicyReconciler{Client: kube, APIReader: kube, Source: source, Now: now, NativeExecution: true}
	boxes := &controller.SandboxReconciler{Client: kube, APIReader: kube, Source: source, Now: now, NativeExecution: true}
	passes := []struct {
		kind      string
		key       client.ObjectKey
		reconcile func(context.Context, ctrl.Request) (ctrl.Result, error)
	}{
		{"AgentPolicy", client.ObjectKeyFromObject(policy), policies.Reconcile},
		{"Sandbox", client.ObjectKeyFromObject(box), boxes.Reconcile},
	}
	condition := func(kind string, key client.ObjectKey) *metav1.Condition {
		t.Helper()
		if kind == "AgentPolicy" {
			current := &api.AgentPolicy{}
			if err := kube.Get(context.Background(), key, current); err != nil {
				t.Fatal(err)
			}
			return apimeta.FindStatusCondition(current.Status.Conditions, api.ConditionAgentPolicyAccepted)
		}
		current := &api.Sandbox{}
		if err := kube.Get(context.Background(), key, current); err != nil {
			t.Fatal(err)
		}
		return apimeta.FindStatusCondition(current.Status.Conditions, api.ConditionReady)
	}
	setRegistration := func(raw string) {
		t.Helper()
		current := &corev1.ConfigMap{}
		if err := kube.Get(context.Background(), client.ObjectKeyFromObject(registration), current); err != nil {
			t.Fatal(err)
		}
		current.Data[controller.SandboxPlatformConfigurationKey] = raw
		if err := kube.Update(context.Background(), current); err != nil {
			t.Fatal(err)
		}
	}
	accepted := func(ctx context.Context, when string) {
		t.Helper()
		for _, pass := range passes {
			result, err := pass.reconcile(ctx, ctrl.Request{NamespacedName: pass.key})
			if err != nil || result.RequeueAfter <= 0 || result.RequeueAfter >= unavailableConfigurationRequeue {
				t.Fatalf("%s, %s: the usable registration did not refresh: result=%+v err=%v", when, pass.kind, result, err)
			}
			if got := condition(pass.kind, pass.key); got == nil || got.Status != metav1.ConditionTrue {
				t.Fatalf("%s, %s: the usable registration was not accepted: %+v", when, pass.kind, got)
			}
		}
	}
	ctx, logged := captureConfigurationRetries()
	accepted(ctx, "before the change")
	setRegistration(kubernetesPlatformJSON(t, unusable))

	// A failed status write is still a reconcile error, with its cause.
	failWrites = true
	for _, pass := range passes {
		result, err := pass.reconcile(ctx, ctrl.Request{NamespacedName: pass.key})
		if !errors.Is(err, writeFailure) || !errors.Is(err, sandboxpolicy.ErrUnsupportedControl) || result.RequeueAfter != 0 {
			t.Fatalf("%s: a failed status write was hidden: result=%+v err=%v", pass.kind, result, err)
		}
	}
	failWrites = false
	clear(writes)
	for round := range 3 {
		for _, pass := range passes {
			result, err := pass.reconcile(ctx, ctrl.Request{NamespacedName: pass.key})
			if err != nil || result.RequeueAfter != unavailableConfigurationRequeue {
				t.Fatalf("%s, pass %d: the unusable registration is not a periodic retry: result=%+v err=%v", pass.kind, round, result, err)
			}
			got := condition(pass.kind, pass.key)
			if got == nil || got.Status != metav1.ConditionUnknown || got.Reason != "PrerequisitesUnavailable" ||
				!strings.Contains(got.Message, reason) || strings.Contains(got.Message, canary) {
				t.Fatalf("%s, pass %d: the status does not show the unusable registration: %+v", pass.kind, round, got)
			}
		}
	}
	for _, kind := range []string{"AgentPolicy", "Sandbox"} {
		if logged.info[kind] != 1 || logged.verbose[kind] != 2 {
			t.Fatalf("%s: the retry was not logged once at Info level: info=%d verbose=%d", kind, logged.info[kind], logged.verbose[kind])
		}
	}
	if writes["*v1alpha1.AgentPolicy"] != 1 || writes["*v1alpha1.Sandbox"] != 1 {
		t.Fatalf("a repeated retry wrote the status again: %v", writes)
	}
	for _, line := range logged.lines {
		if !strings.Contains(line, reason) || strings.Contains(line, canary) {
			t.Fatalf("the retry log does not show only the fixed reason: %s", line)
		}
	}

	// The first pass after the correction accepts the policy and the Sandbox.
	setRegistration(valid)
	accepted(ctx, "after the correction")
	if len(logged.lines) != 6 {
		t.Fatalf("an accepted pass logged a retry: %d lines", len(logged.lines))
	}
}
