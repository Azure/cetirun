// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	admissionhandler "github.com/Azure/CetiRun/app/cetirun-operator/internal/admission"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	istio "istio.io/client-go/pkg/apis/networking/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func uidOnlyIngressMTLSFixture(t *testing.T) *nativeSandboxFixture {
	t.Helper()
	f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, "openclaw", true, false, withIngressMTLSPins(t, "openclaw", true))
	f.reconcileOK(t)
	return f
}

func relabel(t *testing.T, kube client.Client, object client.Object, key, value string) {
	t.Helper()
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(object), object); err != nil {
		t.Fatal(err)
	}
	labels := object.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[key] = value
	object.SetLabels(labels)
	annotations := object.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations["example.com/note"] = value
	object.SetAnnotations(annotations)
	if err := kube.Update(context.Background(), object); err != nil {
		t.Fatal(err)
	}
}

func TestIngressMTLSUIDOnlyPinsIgnoreMetadataChanges(t *testing.T) {
	ctx := context.Background()
	f := uidOnlyIngressMTLSFixture(t)
	expected := reconciledExpectationForTest(t, f)
	for _, name := range []struct{ namespace, name string }{
		{f.workload(t).Namespace, "agent-ingress-tls"}, {"platform", "gateway-agent-example"},
	} {
		if !slices.ContainsFunc(expected.Sources, func(ref w.SourceReference) bool {
			return ref.Kind == "Secret" && ref.Namespace == name.namespace && ref.Name == name.name &&
				ref.ImmutableSecret && ref.ResourceVersion == "" && ref.UID != ""
		}) {
			t.Fatal("immutable TLS Secret is not UID pinned:", name)
		}
	}
	replicas := ptr.Deref(f.workload(t).Spec.Replicas, -1)
	handler, err := admissionhandler.NewPodHandler(f.kube, admissionhandler.PodConfig{StateNamespace: f.r.OperatorNamespace})
	if err != nil {
		t.Fatal(err)
	}
	pod := reconciledPodForTest(t, f, expected.Workloads[0])
	if response := handler.Validate(ctx, reconciledPodRequest(t, pod)); !response.Allowed {
		t.Fatal("UID-pinned TLS Pod was not admitted:", response.Result)
	}

	// An Istio chart upgrade changes the Gateway chart label; an administrator
	// can label or annotate the Secrets. None of these change the approval.
	relabel(t, f.kube, &istio.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "platform", Name: "ingress"}}, "helm.sh/chart", "cetirun-istio-9.9.9")
	relabel(t, f.kube, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: f.workload(t).Namespace, Name: "agent-ingress-tls"}}, "example.com/owner", "platform")
	relabel(t, f.kube, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "platform", Name: "gateway-agent-example"}}, "example.com/owner", "platform")
	for range 2 {
		f.reconcileOK(t)
	}
	after := reconciledExpectationForTest(t, f)
	if after.Revision != expected.Revision || ptr.Deref(f.workload(t).Spec.Replicas, -1) != replicas {
		t.Fatal("metadata-only changes replaced or stopped the UID-pinned execution")
	}
	if ready := apimeta.FindStatusCondition(f.current(t).Status.Conditions, api.ConditionSandboxReady); ready != nil && ready.Reason == "IngressMTLSApprovalStale" {
		t.Fatal("metadata-only changes reported a stale approval")
	}
	if response := handler.Validate(ctx, reconciledPodRequest(t, pod)); !response.Allowed {
		t.Fatal("metadata-only changes stopped admission of the UID-pinned Pod:", response.Result)
	}
	if _, err := f.r.ensureVirtualService(ctx, f.current(t)); err != nil {
		t.Fatal("metadata-only changes withdrew the TLS route:", err)
	}
}

func assertStaleHold(t *testing.T, f *nativeSandboxFixture, object string) {
	t.Helper()
	var phase, version string
	var condition metav1.Condition
	for pass := range 4 {
		if _, err := f.reconcile(); err == nil {
			t.Fatal("stale approval was accepted")
		}
		agent := f.current(t)
		ready := apimeta.FindStatusCondition(agent.Status.Conditions, api.ConditionReady)
		if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != "IngressMTLSApprovalStale" ||
			!strings.Contains(ready.Message, object) {
			t.Fatalf("pass %d: hold does not name the stale approval: %+v", pass, ready)
		}
		if pass > 0 && (agent.Status.Phase != phase || ready.Reason != condition.Reason || ready.Message != condition.Message) {
			t.Fatalf("pass %d: hold status changed from %s/%s to %s/%s", pass, phase, condition.Reason, agent.Status.Phase, ready.Reason)
		}
		// A steady hold must not write the Agent on every pass, for example
		// by alternating between two hold paths with different phases.
		if pass > 1 && agent.ResourceVersion != version {
			t.Fatalf("pass %d: a steady stale hold rewrote the Agent", pass)
		}
		phase, condition, version = agent.Status.Phase, *ready, agent.ResourceVersion
	}
	if phase != api.PhasePending {
		t.Fatal("stale approval hold is not Pending:", phase)
	}
	if ptr.Deref(f.workload(t).Spec.Replicas, -1) != 0 {
		t.Fatal("stale approval did not stop the workload")
	}
}

func TestIngressMTLSReplacedSecretReportsStaleApproval(t *testing.T) {
	ctx := context.Background()
	f := uidOnlyIngressMTLSFixture(t)
	expected := reconciledExpectationForTest(t, f)
	secret := &corev1.Secret{}
	if err := f.kube.Get(ctx, client.ObjectKey{Namespace: "platform", Name: "gateway-agent-example"}, secret); err != nil {
		t.Fatal(err)
	}
	if err := f.kube.Delete(ctx, secret); err != nil {
		t.Fatal(err)
	}
	replacement := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "platform", Name: "gateway-agent-example"}, Immutable: ptr.To(true)}
	if err := f.kube.Create(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	assertStaleHold(t, f, "Secret platform/gateway-agent-example")
	handler, err := admissionhandler.NewPodHandler(f.kube, admissionhandler.PodConfig{StateNamespace: f.r.OperatorNamespace})
	if err != nil {
		t.Fatal(err)
	}
	if response := handler.Validate(ctx, reconciledPodRequest(t, reconciledPodForTest(t, f, expected.Workloads[0]))); response.Allowed {
		t.Fatal("admission accepted a Pod after its UID-pinned Secret was replaced")
	}
}

func TestIngressMTLSUIDOnlyPinRequiresImmutableSecret(t *testing.T) {
	ctx := context.Background()
	f := uidOnlyIngressMTLSFixture(t)
	expected := reconciledExpectationForTest(t, f)
	secret := &corev1.Secret{}
	key := client.ObjectKey{Namespace: f.workload(t).Namespace, Name: "agent-ingress-tls"}
	if err := f.kube.Get(ctx, key, secret); err != nil {
		t.Fatal(err)
	}
	// The API server refuses this change; the fake client lets the test
	// describe a registration that names a mutable Secret.
	secret.Immutable = nil
	if err := f.kube.Update(ctx, secret); err != nil {
		t.Fatal(err)
	}
	assertStaleHold(t, f, "Secret "+key.Namespace+"/"+key.Name)
	handler, err := admissionhandler.NewPodHandler(f.kube, admissionhandler.PodConfig{StateNamespace: f.r.OperatorNamespace})
	if err != nil {
		t.Fatal(err)
	}
	if response := handler.Validate(ctx, reconciledPodRequest(t, reconciledPodForTest(t, f, expected.Workloads[0]))); response.Allowed {
		t.Fatal("admission accepted a UID-pinned Secret that is not immutable")
	}
}

func TestIngressMTLSVersionPinsStayStrict(t *testing.T) {
	f := newSandboxRuntimeFixtureForAgent(t, true, nil, true, "openclaw", true, false, withIngressMTLS(t, "openclaw"))
	f.reconcileOK(t)
	relabel(t, f.kube, &istio.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "platform", Name: "ingress"}}, "helm.sh/chart", "cetirun-istio-9.9.9")
	assertStaleHold(t, f, "Gateway platform/ingress")
	raw, err := json.Marshal(f.current(t).Status.Conditions)
	if err != nil || strings.Contains(string(raw), "PRIVATE KEY") {
		t.Fatal("status exposed credential material:", err)
	}
}
