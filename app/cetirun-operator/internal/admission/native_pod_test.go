// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxadmission

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	admissionv1 "k8s.io/api/admission/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func nativePodFixture(t *testing.T) podFixture {
	t.Helper()
	f := newPodFixture(t)
	f.expected.SinglePod = true
	f.expected.SourceDigests = map[string]string{}
	for _, source := range f.expected.Sources {
		if !w.EffectiveSource(source) || (source.APIVersion == "v1" && source.Kind == "PersistentVolumeClaim") {
			continue
		}
		for _, object := range f.objects {
			if object.GetName() != source.Name || object.GetNamespace() != source.Namespace || object.GetUID() != source.UID {
				continue
			}
			digest, err := w.ExecutionSourceDigest(object, f.fixture.Sandbox.Name, f.expected.Binding.PolicyClass)
			if err != nil {
				t.Fatal(err)
			}
			f.expected.SourceDigests[w.SourceKey(source)] = digest
		}
	}
	saveNativePodExpectation(t, &f)
	return f
}

func saveNativePodExpectation(t *testing.T, f *podFixture) {
	t.Helper()
	if err := f.client.Get(context.Background(), client.ObjectKeyFromObject(f.state), f.state); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(f.expected)
	if err != nil {
		t.Fatal(err)
	}
	f.state.Data[w.PodExpectationKey] = string(raw)
	if err := f.client.Update(context.Background(), f.state); err != nil {
		t.Fatal(err)
	}
}

func TestNativePodAdmissionToleratesOnlyObservedSourceChanges(t *testing.T) {
	for _, test := range []struct {
		name    string
		change  func(*testing.T, *podFixture)
		allowed bool
	}{
		{name: "initial", allowed: true},
		{name: "StatefulSet status", allowed: true, change: func(t *testing.T, f *podFixture) {
			workload := &appsv1.StatefulSet{}
			if err := f.client.Get(context.Background(), client.ObjectKeyFromObject(f.workload), workload); err != nil {
				t.Fatal(err)
			}
			workload.Status.ReadyReplicas = 1
			if err := f.client.Update(context.Background(), workload); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "effective input changed", change: func(t *testing.T, f *podFixture) {
			config := &corev1.ConfigMap{}
			key := client.ObjectKey{Namespace: f.pod.Namespace, Name: "example-config"}
			if err := f.client.Get(context.Background(), key, config); err != nil {
				t.Fatal(err)
			}
			config.Data = map[string]string{"setting": "changed"}
			if err := f.client.Update(context.Background(), config); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing captured inputs", change: func(t *testing.T, f *podFixture) {
			f.expected.SourceDigests = nil
			saveNativePodExpectation(t, f)
		}},
		{name: "workload template changed", change: func(t *testing.T, f *podFixture) {
			workload := &appsv1.StatefulSet{}
			if err := f.client.Get(context.Background(), client.ObjectKeyFromObject(f.workload), workload); err != nil {
				t.Fatal(err)
			}
			workload.Spec.Template.Spec.Containers[0].Image = "registry.example.com/unapproved:1"
			if err := f.client.Update(context.Background(), workload); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := nativePodFixture(t)
			if test.change != nil {
				test.change(t, &f)
			}
			response := f.handler.Validate(context.Background(), podRequest(t, f.pod, admissionv1.Create, nil))
			if response.Allowed != test.allowed {
				t.Fatalf("allowed=%t want=%t result=%+v", response.Allowed, test.allowed, response.Result)
			}
		})
	}
}

func TestNativePodAdmissionNeverRecreatesAPinnedPod(t *testing.T) {
	f := nativePodFixture(t)
	f.expected.PodUID = "first-pod-uid"
	saveNativePodExpectation(t, &f)
	for _, spoofUID := range []bool{false, true} {
		pod := f.pod.DeepCopy()
		if spoofUID {
			pod.UID = f.expected.PodUID
		}
		response := f.handler.Validate(context.Background(), podRequest(t, pod, admissionv1.Create, nil))
		if response.Allowed || response.Result == nil || !strings.Contains(response.Result.Message, "ReplacementUnsupported") {
			t.Fatalf("replacement was not rejected independently of caller-supplied UID: %+v", response.Result)
		}
	}
}

func TestNativePodPinKeepsOrdinaryDeletionAvailable(t *testing.T) {
	f := nativePodFixture(t)
	f.expected.PodUID = "first-pod-uid"
	f.expected.ExecutionHeld = true
	saveNativePodExpectation(t, &f)
	response := f.handler.Validate(context.Background(), podRequest(t, f.pod, admissionv1.Delete, nil))
	if !response.Allowed {
		t.Fatalf("native one-shot state blocked ordinary Pod cleanup: %+v", response.Result)
	}
}
