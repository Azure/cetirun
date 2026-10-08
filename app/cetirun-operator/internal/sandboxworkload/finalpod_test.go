// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxworkload

import (
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"
)

func TestPodDefaultNormalizationIsStableAndPreservesExplicitValues(t *testing.T) {
	spec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name: "example", Image: "example.com/agent:v1",
			Env: []corev1.EnvVar{
				{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
				{Name: "CPU", ValueFrom: &corev1.EnvVarSource{ResourceFieldRef: &corev1.ResourceFieldSelector{Resource: "limits.cpu"}}},
			},
		}},
		Volumes: []corev1.Volume{{
			Name: "fields", VolumeSource: corev1.VolumeSource{DownwardAPI: &corev1.DownwardAPIVolumeSource{
				Items: []corev1.DownwardAPIVolumeFile{
					{Path: "name", FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}},
					{Path: "cpu", ResourceFieldRef: &corev1.ResourceFieldSelector{ContainerName: "example", Resource: "limits.cpu"}},
				},
			}},
		}},
	}
	if err := defaultPod(&spec); err != nil {
		t.Fatal(err)
	}
	if *spec.Volumes[0].DownwardAPI.DefaultMode != 0644 ||
		spec.Volumes[0].DownwardAPI.Items[0].FieldRef.APIVersion != "v1" ||
		spec.Containers[0].Env[0].ValueFrom.FieldRef.APIVersion != "v1" ||
		spec.Containers[0].Env[1].ValueFrom.ResourceFieldRef.Divisor.Cmp(resource.MustParse("1")) != 0 {
		t.Fatal("known API defaults were not normalized")
	}
	before := spec.DeepCopy()
	if err := defaultPod(&spec); err != nil || !equality.Semantic.DeepEqual(before, &spec) {
		t.Fatalf("normalization is not a fixed point: %v", err)
	}
	spec.Volumes[0].DownwardAPI.DefaultMode = ptr.To(int32(0400))
	spec.Containers[0].Env[1].ValueFrom.ResourceFieldRef.Divisor = resource.MustParse("2")
	if err := defaultPod(&spec); err != nil {
		t.Fatal(err)
	}
	if *spec.Volumes[0].DownwardAPI.DefaultMode != 0400 ||
		spec.Containers[0].Env[1].ValueFrom.ResourceFieldRef.Divisor.Cmp(resource.MustParse("2")) != 0 {
		t.Fatal("normalization erased explicit nondefault values")
	}
}

func TestAKSKataTolerationNormalizationIsNarrow(t *testing.T) {
	runtimeClass := "kata-vm-isolation"
	expected := corev1.PodSpec{
		RuntimeClassName: &runtimeClass,
		NodeSelector:     map[string]string{"kubernetes.azure.com/kata-vm-isolation": "true"},
	}
	actual := expected.DeepCopy()
	actual.Tolerations = []corev1.Toleration{{
		Key: "node.kubernetes.io/memory-pressure", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule,
	}}
	if err := defaultPod(&expected); err != nil {
		t.Fatal(err)
	}
	if err := defaultPod(actual); err != nil {
		t.Fatal(err)
	}
	if !equality.Semantic.DeepEqual(&expected, actual) {
		t.Fatal("exact AKS Kata admission toleration was not normalized")
	}

	nonKataExpected := corev1.PodSpec{RuntimeClassName: &runtimeClass}
	nonKataActual := nonKataExpected.DeepCopy()
	nonKataActual.Tolerations = []corev1.Toleration{{
		Key: "node.kubernetes.io/memory-pressure", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule,
	}}
	if err := defaultPod(&nonKataExpected); err != nil {
		t.Fatal(err)
	}
	if err := defaultPod(nonKataActual); err != nil {
		t.Fatal(err)
	}
	if equality.Semantic.DeepEqual(&nonKataExpected, nonKataActual) {
		t.Fatal("AKS Kata toleration was normalized without the Kata scheduling selector")
	}

	for name, mutate := range map[string]func(*corev1.PodSpec){
		"different operator": func(spec *corev1.PodSpec) {
			spec.Tolerations[0].Operator = corev1.TolerationOpEqual
			spec.Tolerations[0].Value = "true"
		},
		"additional toleration": func(spec *corev1.PodSpec) {
			spec.Tolerations = append(spec.Tolerations, corev1.Toleration{
				Key: "example.com/extra", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule,
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := actual.DeepCopy()
			mutate(candidate)
			if err := defaultPod(candidate); err != nil {
				t.Fatal(err)
			}
			if equality.Semantic.DeepEqual(&expected, candidate) {
				t.Fatal("normalization accepted a toleration outside the exact AKS Kata default")
			}
		})
	}
}

func TestPodDifferencePathsAreBoundedAndDoNotExposeValuesOrMapKeys(t *testing.T) {
	want := corev1.PodSpec{
		NodeSelector: map[string]string{"synthetic-private-key": "synthetic-private-value"},
		Containers: []corev1.Container{{
			Name: "example", Image: "example.com/agent:v1",
			Env: []corev1.EnvVar{{Name: "EXAMPLE", Value: "synthetic-old-value"}},
		}},
	}
	got := want.DeepCopy()
	got.NodeSelector["synthetic-private-key"] = "synthetic-new-value"
	got.Containers[0].Env[0].Value = "synthetic-sensitive-env-value"
	var fields []string
	podDifferencePaths(reflect.ValueOf(want), reflect.ValueOf(*got), "spec", &fields)
	message := (&PodMismatchError{Fields: fields}).Error()
	if !strings.Contains(message, "spec.nodeSelector") || !strings.Contains(message, "spec.containers[0].env[0].value") {
		t.Fatalf("expected safe paths, got %s", message)
	}
	if strings.Contains(message, "synthetic") || strings.Contains(message, "EXAMPLE") {
		t.Fatal("field-path diagnostic disclosed a value or dynamic map key")
	}
	for range 20 {
		podDifferencePaths(reflect.ValueOf(want), reflect.ValueOf(*got), "spec", &fields)
	}
	if len(fields) != 8 {
		t.Fatal("diagnostic field count is not bounded to eight paths")
	}
}
