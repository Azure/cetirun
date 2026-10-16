// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload/testfixture"
	"helm.sh/helm/v3/pkg/chart"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
)

func reconciledSandboxInputs(t *testing.T) (*w.Bundle, map[string]any) {
	t.Helper()
	f := testfixture.New("openclaw")
	f.Input.Reconciled = true
	f.Input.AuthorizedSecrets = append(f.Input.AuthorizedSecrets, "model-key")
	f.Input.GatewayKey = &w.ObjectIdentity{Name: "model-key", UID: "model-key-uid"}
	bundle, err := w.Build(f.Input)
	if err != nil {
		t.Fatal(err)
	}
	values := sampleInput("openclaw", "openai", false)
	values["secrets"].(map[string]any)["existingSecret"] = "runtime-secret"
	values["aiGateway"] = map[string]any{
		"enabled": true, "keySecretName": "model-key", "keySecretKey": "api-key",
		"models": []any{map[string]any{"id": "example-model", "name": "Example"}},
		"copilotProxy": map[string]any{
			"enabled": true, "image": "registry.example.com/copilot-proxy:example",
			"masterKeySecretName": "proxy-key", "tokenSecretName": "copilot-token",
		},
	}
	values["secrets"].(map[string]any)["model"] = "example-model"
	values["secrets"].(map[string]any)["apiBase"] = "http://gateway.example.svc.cluster.local:4000/v1"
	return bundle, values
}

func TestSandboxReusesCopilotResourceAssembly(t *testing.T) {
	bundle, values := reconciledSandboxInputs(t)
	prepared, err := PrepareSandboxChart(testChart(t), values, nil, bundle)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := prepared.SandboxManifest()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), `"kind":"Deployment"`) ||
		!strings.Contains(string(manifest), "copilot-proxy") {
		t.Fatal("Sandbox discarded the existing Copilot resources")
	}
}

func TestReconciledSandboxReusesAnApprovedChartComponent(t *testing.T) {
	for _, governed := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "sandbox"}[governed], func(t *testing.T) {
			bundle, values := reconciledSandboxInputs(t)
			helperResources := bundle.Identity().Resolution.Sandbox.Resources.Helpers
			chrt := testChart(t)
			chrt.Templates = append(chrt.Templates, &chart.File{
				Name: "templates/example-observer.yaml",
				Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: example-observer\ndata:\n  mode: observe\n"),
			})
			values["sidecars"] = []corev1.Container{{
				Name: "example-observer", Image: "registry.example.com/observer:example",
				Command: []string{"sleep", "3600"}, SecurityContext: bundle.SecurityContext(),
				Resources:    corev1.ResourceRequirements{Requests: helperResources.Requests, Limits: helperResources.Limits},
				VolumeMounts: []corev1.VolumeMount{{Name: "observer-config", MountPath: "/etc/observer", ReadOnly: true}},
			}}
			values["extraVolumes"] = []corev1.Volume{{Name: "observer-config", VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "example-observer"}},
			}}}
			raw, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &values); err != nil {
				t.Fatal(err)
			}
			var template corev1.PodTemplateSpec
			if governed {
				prepared, err := PrepareSandboxChart(chrt, values, nil, bundle)
				if err != nil {
					t.Fatal(err)
				}
				current, err := prepared.SandboxTemplate()
				if err != nil {
					t.Fatal(err)
				}
				template = *current
				expected, err := prepared.ReconcileExpectation()
				if err != nil || !slices.ContainsFunc(expected.Controls, func(control unstructured.Unstructured) bool {
					return control.GetKind() == "ConfigMap" && control.GetName() == "example-observer"
				}) {
					t.Fatal("the common component's configuration was not included in current admission state")
				}
				accounting := w.AccountResources(template.Spec, bundle.Overhead())
				if template.Spec.Resources == nil || !equality.Semantic.DeepEqual(*template.Spec.Resources, accounting.Pod) {
					t.Fatal("a new helper escaped aggregate Pod resource accounting")
				}
			} else {
				prepared, err := PrepareChart(chrt, values)
				if err != nil {
					t.Fatal(err)
				}
				template = registryRenderedTemplate(t, prepared)
			}
			if !slices.ContainsFunc(template.Spec.Containers, func(container corev1.Container) bool {
				return container.Name == "example-observer" && container.Image == "registry.example.com/observer:example"
			}) {
				t.Fatal("the once-defined chart component was dropped")
			}
		})
	}
}

func TestReconciledSandboxProxyPreparationIsIndependent(t *testing.T) {
	bundle, values := reconciledSandboxInputs(t)
	values["replicaCount"] = 0
	values["aiGateway"].(map[string]any)["copilotProxy"].(map[string]any)["replicaCount"] = 1
	prepared, err := PrepareSandboxChart(testChart(t), values, nil, bundle)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := prepared.ReconcileExpectation()
	if err != nil {
		t.Fatal(err)
	}
	if len(expected.Workloads) != 2 {
		t.Fatal("missing independently prepared Copilot proxy")
	}
	for _, workload := range expected.Workloads {
		want := int32(0)
		if workload.Kind == "Deployment" {
			want = 1
		}
		if workload.Replicas != want {
			t.Fatal("proxy authentication still depends on Agent replicas")
		}
	}
}

func TestReconciledSandboxAuxiliaryRoleConstraints(t *testing.T) {
	bundle, _ := reconciledSandboxInputs(t)
	for name, change := range map[string]func(*corev1.PodSpec){
		"baseline": nil,
		"key environment": func(p *corev1.PodSpec) {
			p.Containers[0].Env = []corev1.EnvVar{{Name: "KEY", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "model-key"}, Key: "api-key"},
			}}}
		},
		"key envFrom": func(p *corev1.PodSpec) {
			p.Containers[0].EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "runtime-secret"}}}}
		},
		"key volume": func(p *corev1.PodSpec) {
			p.Volumes = []corev1.Volume{{Name: "credentials", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "model-key"}}}}
		},
		"token projection": func(p *corev1.PodSpec) {
			p.Volumes = []corev1.Volume{{Name: "identity", VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{
					ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token"},
				}}},
			}}}
		},
		"HOME": func(p *corev1.PodSpec) {
			p.Volumes = []corev1.Volume{{Name: "home", VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: bundle.Identity().Home.Name},
			}}}
		},
		"capabilities": func(p *corev1.PodSpec) {
			p.Containers[0].SecurityContext.Capabilities.Add = []corev1.Capability{"NET_ADMIN"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			deployment := &appsv1.Deployment{
				TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
				ObjectMeta: metav1.ObjectMeta{Name: "approved-component"},
				Spec: appsv1.DeploymentSpec{
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "approved-component"}},
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
						AutomountServiceAccountToken: ptr.To(false), SecurityContext: bundle.PodSecurityContext(),
						Containers: []corev1.Container{{Name: "helper", Image: "registry.example.com/helper:example",
							SecurityContext: bundle.SecurityContext()}},
					}},
				},
			}
			if change != nil {
				change(&deployment.Spec.Template.Spec)
			}
			object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(deployment)
			if err != nil {
				t.Fatal(err)
			}
			prepared := &PreparedRelease{sandboxBundle: bundle, sandboxReconcile: &w.ReconcileExpectation{}}
			err = prepared.validateReconciledObject(unstructured.Unstructured{Object: object})
			if (err != nil) != (change != nil) {
				t.Fatalf("auxiliary constraint %s: %v", name, err)
			}
		})
	}
}

func TestReconciledSandboxApplyRequiresCurrentAuthority(t *testing.T) {
	bundle, values := reconciledSandboxInputs(t)
	base, err := PrepareChart(testChart(t), values)
	if err != nil {
		t.Fatal(err)
	}
	stale := errors.New("synthetic authority changed")
	prepared, err := base.ReconcileSandbox(values, bundle, func(context.Context) error { return stale })
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Client{}).InstallOrUpgrade(bundle.Identity().Namespace.Name, bundle.Identity().ReleaseName, prepared); !errors.Is(err, stale) {
		t.Fatalf("common apply did not recheck authority before Kubernetes access: %v", err)
	}
}
