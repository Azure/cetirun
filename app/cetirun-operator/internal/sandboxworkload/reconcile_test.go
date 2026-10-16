// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxworkload

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestReconciledPodNormalizesActualQOSAdmission(t *testing.T) {
	for _, scenario := range []struct {
		name string
		spec corev1.PodSpec
		want bool
	}{
		{"best effort", corev1.PodSpec{}, false},
		{"storage only", corev1.PodSpec{Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{corev1.ResourceEphemeralStorage: resource.MustParse("128Mi")},
		}}}}, false},
		{"container CPU", corev1.PodSpec{Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m")},
		}}}}, true},
		{"init memory", corev1.PodSpec{InitContainers: []corev1.Container{{Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
		}}}}, true},
		{"Pod budget", corev1.PodSpec{Resources: &corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
		}}, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			if err := defaultPod(&scenario.spec); err != nil {
				t.Fatal(err)
			}
			found := slices.ContainsFunc(scenario.spec.Tolerations, func(value corev1.Toleration) bool {
				return value.Key == "node.kubernetes.io/memory-pressure" && value.Operator == corev1.TolerationOpExists &&
					value.Effect == corev1.TaintEffectNoSchedule && value.Value == "" && value.TolerationSeconds == nil
			})
			if found != scenario.want {
				t.Fatalf("memory-pressure default = %v, want %v", found, scenario.want)
			}
		})
	}
}
