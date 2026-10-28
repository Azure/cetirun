// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package podtemplate

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

func TestSameAcceptsAPIDefaultedTemplateAndRejectsMaterialChanges(t *testing.T) {
	expected := corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		ServiceAccountName: "agent",
		Containers: []corev1.Container{{
			Name: "cetirun", Image: "registry.example.com/agent:1",
			Env: []corev1.EnvVar{{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
			}}},
			Ports:         []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}},
			Resources:     corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}},
			LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Port: intstr.FromInt(8080)}}, InitialDelaySeconds: 1},
		}},
		Volumes: []corev1.Volume{{Name: "settings", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}}}}},
	}}
	apiDefaulted := *expected.DeepCopy()
	// This is a served PodTemplate: write the API defaults explicitly instead
	// of deriving it with DefaultSpec, so the positive case detects omitted
	// defaulting rules as well as comparison regressions.
	apiDefaulted.Spec.RestartPolicy = corev1.RestartPolicyAlways
	apiDefaulted.Spec.DNSPolicy = corev1.DNSClusterFirst
	apiDefaulted.Spec.SchedulerName = corev1.DefaultSchedulerName
	apiDefaulted.Spec.TerminationGracePeriodSeconds = ptr.To(int64(30))
	apiDefaulted.Spec.DeprecatedServiceAccount = "agent"
	container := &apiDefaulted.Spec.Containers[0]
	container.ImagePullPolicy = corev1.PullIfNotPresent
	container.TerminationMessagePath = corev1.TerminationMessagePathDefault
	container.TerminationMessagePolicy = corev1.TerminationMessageReadFile
	container.Env[0].ValueFrom.FieldRef.APIVersion = "v1"
	container.Ports[0].Protocol = corev1.ProtocolTCP
	container.LivenessProbe.TimeoutSeconds = 1
	container.LivenessProbe.PeriodSeconds = 10
	container.LivenessProbe.SuccessThreshold = 1
	container.LivenessProbe.FailureThreshold = 3
	container.LivenessProbe.HTTPGet.Scheme = corev1.URISchemeHTTP
	container.LivenessProbe.HTTPGet.Path = "/"
	apiDefaulted.Spec.Volumes[0].ConfigMap.DefaultMode = ptr.To(int32(0644))
	if equality.Semantic.DeepEqual(apiDefaulted, expected) {
		t.Fatal("served API defaults did not change the raw template")
	}
	if !Same(apiDefaulted, expected) {
		t.Fatal("API-defaulted template was rejected")
	}

	cases := map[string]func(*corev1.PodTemplateSpec){
		"image": func(template *corev1.PodTemplateSpec) {
			template.Spec.Containers[0].Image = "registry.example.com/agent:2"
		},
		"environment": func(template *corev1.PodTemplateSpec) {
			template.Spec.Containers[0].Env = append(template.Spec.Containers[0].Env, corev1.EnvVar{Name: "EXTRA", Value: "changed"})
		},
		"command": func(template *corev1.PodTemplateSpec) {
			template.Spec.Containers[0].Command = []string{"different-command"}
		},
		"security context": func(template *corev1.PodTemplateSpec) {
			template.Spec.Containers[0].SecurityContext = &corev1.SecurityContext{Privileged: ptr.To(true)}
		},
		"resources": func(template *corev1.PodTemplateSpec) {
			template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("200m")
		},
		"volume":   func(template *corev1.PodTemplateSpec) { template.Spec.Volumes[0].ConfigMap.Name = "other-settings" },
		"priority": func(template *corev1.PodTemplateSpec) { template.Spec.Priority = ptr.To(int32(1)) },
		"toleration": func(template *corev1.PodTemplateSpec) {
			template.Spec.Tolerations = append(template.Spec.Tolerations, corev1.Toleration{Key: "synthetic", Operator: corev1.TolerationOpExists})
		},
		"probe": func(template *corev1.PodTemplateSpec) {
			template.Spec.Containers[0].LivenessProbe.HTTPGet.Path = "/changed"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			actual := *apiDefaulted.DeepCopy()
			mutate(&actual)
			if Same(actual, expected) {
				t.Fatalf("accepted changed %s", name)
			}
		})
	}
}
