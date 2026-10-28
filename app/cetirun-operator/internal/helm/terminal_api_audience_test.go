// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"testing"

	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func terminalAPIProjection(t *testing.T, spec *corev1.PodSpec) *corev1.ServiceAccountTokenProjection {
	t.Helper()
	for i := range spec.Volumes {
		volume := &spec.Volumes[i]
		if volume.Name != "api-access" || volume.Projected == nil {
			continue
		}
		for j := range volume.Projected.Sources {
			if token := volume.Projected.Sources[j].ServiceAccountToken; token != nil {
				return token
			}
		}
	}
	t.Fatal("terminal has no projected Kubernetes API token")
	return nil
}

func assertTerminalAPIAudience(t *testing.T, prepared *PreparedRelease, workload w.RenderedWorkload, pod *corev1.Pod, owner metav1.OwnerReference) {
	t.Helper()
	projection := terminalAPIProjection(t, &pod.Spec)
	if projection.Audience != "" || projection.Path != "token" || ptr.Deref(projection.ExpirationSeconds, 0) != 3600 {
		t.Fatal("terminal must use a bounded token with the API server's default audience")
	}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: workload.Identity.Name},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](1), Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Selector: &metav1.LabelSelector{MatchLabels: workload.Template.Labels},
			Template: *workload.Template.DeepCopy(),
		},
	}
	if err := prepared.validateTerminalDeployment(deployment); err != nil {
		t.Fatalf("terminal API default audience rejected before publication: %v", err)
	}
	terminalAPIProjection(t, &deployment.Spec.Template.Spec).Audience = "https://unapproved.example.com"
	if err := prepared.validateTerminalDeployment(deployment); err == nil {
		t.Fatal("terminal accepted a caller-selected token audience before publication")
	}
	changed := pod.DeepCopy()
	terminalAPIProjection(t, &changed.Spec).Audience = "https://unapproved.example.com"
	if err := w.ValidateRenderedPod(changed, workload, owner, "hash"); err == nil {
		t.Fatal("terminal accepted a changed audience after expectation publication")
	}
}
