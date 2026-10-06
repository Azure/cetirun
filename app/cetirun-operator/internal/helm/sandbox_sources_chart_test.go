// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"reflect"
	"slices"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	kubeyaml "k8s.io/apimachinery/pkg/util/yaml"
)

func sandboxSourcesChartValues(t *testing.T) map[string]any {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true}
	certificate, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"namespace": "operator-example",
		"sandboxAdmission": map[string]any{
			"enabled": true, "enforce": true, "podsEnabled": true,
			"adminUsers": []string{"admin@example.com"}, "tlsSecretName": "synthetic-admission-tls",
			"caBundle": base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate})),
		},
		"sandboxSources": map[string]any{
			"enabled": true, "bootstrapOwnedPrerequisites": true,
			"platformConfigMapName": "sandbox-platform",
		},
	}
}

// This uses the repository's pinned Helm SDK, not a Helm CLI or an API server.
func renderSandboxSourcesChart(t *testing.T, values map[string]any) (map[string]*unstructured.Unstructured, error) {
	t.Helper()
	chart, err := loader.Load("../../chart")
	if err != nil {
		t.Fatal(err)
	}
	capabilities := chartutil.DefaultCapabilities.Copy()
	capabilities.KubeVersion = chartutil.KubeVersion{Version: "v1.37.0", Major: "1", Minor: "37"}
	input, err := chartutil.ToRenderValues(chart, values, chartutil.ReleaseOptions{
		Name: "publication-test", Namespace: "release-example", Revision: 1, IsInstall: true,
	}, capabilities)
	if err != nil {
		return nil, err
	}
	rendered, err := engine.Render(chart, input)
	if err != nil {
		return nil, err
	}
	objects := map[string]*unstructured.Unstructured{}
	for _, text := range rendered {
		decoder := kubeyaml.NewYAMLOrJSONDecoder(strings.NewReader(text), 4096)
		for {
			object := &unstructured.Unstructured{}
			if err := decoder.Decode(object); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			if object.GetKind() != "" {
				objects[object.GetKind()+"/"+object.GetName()] = object
			}
		}
	}
	return objects, nil
}

func sandboxChartEnvironment(t *testing.T, objects map[string]*unstructured.Unstructured, deployment, variable string) map[string]any {
	t.Helper()
	var object appsv1.Deployment
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(objects["Deployment/"+deployment].Object, &object); err != nil {
		t.Fatal(err)
	}
	for _, env := range object.Spec.Template.Spec.Containers[0].Env {
		if env.Name == variable {
			var value map[string]any
			if err := json.Unmarshal([]byte(env.Value), &value); err != nil {
				t.Fatal(err)
			}
			return value
		}
	}
	t.Fatalf("missing chart environment %s", variable)
	return nil
}

func TestSandboxSkillsCatalogChartWiring(t *testing.T) {
	values := sandboxSourcesChartValues(t)
	baseline, err := renderSandboxSourcesChart(t, values)
	if err != nil {
		t.Fatal(err)
	}
	if _, enabled := sandboxChartEnvironment(t, baseline, "cetirun-operator", "CETIRUN_SANDBOX_SOURCES")["skillsCatalogConfigMapName"]; enabled {
		t.Fatal("catalog setting was enabled by default")
	}
	values["sandboxSources"].(map[string]any)["skillsCatalogConfigMapName"] = "published-skills"
	configured, err := renderSandboxSourcesChart(t, values)
	if err != nil {
		t.Fatal(err)
	}
	config := sandboxChartEnvironment(t, configured, "cetirun-operator", "CETIRUN_SANDBOX_SOURCES")
	if config["skillsCatalogConfigMapName"] != "published-skills" || config["platformConfigMapName"] != "sandbox-platform" {
		t.Fatal("chart did not keep publication selection separate from platform.json")
	}
	if !reflect.DeepEqual(
		sandboxChartEnvironment(t, baseline, "cetirun-sandbox-admission", "CETIRUN_SANDBOX_ADMISSION"),
		sandboxChartEnvironment(t, configured, "cetirun-sandbox-admission", "CETIRUN_SANDBOX_ADMISSION")) {
		t.Fatal("catalog changed the shared configuration-admission wire contract")
	}
	var previous, current rbacv1.ClusterRole
	for _, entry := range []struct {
		objects map[string]*unstructured.Unstructured
		role    *rbacv1.ClusterRole
	}{{baseline, &previous}, {configured, &current}} {
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(entry.objects["ClusterRole/cetirun-sandbox-admission"].Object, entry.role); err != nil {
			t.Fatal(err)
		}
	}
	var otherRules []rbacv1.PolicyRule
	volumeRules := 0
	for _, rule := range current.Rules {
		if slices.Contains(rule.Resources, "persistentvolumes") {
			volumeRules++
			if !reflect.DeepEqual(rule, rbacv1.PolicyRule{
				APIGroups: []string{""}, Resources: []string{"persistentvolumes"}, Verbs: []string{"get"},
			}) {
				t.Fatal("publication metadata added excess admission privileges")
			}
		} else {
			otherRules = append(otherRules, rule)
		}
	}
	if volumeRules != 1 || !reflect.DeepEqual(previous.Rules, otherRules) ||
		!reflect.DeepEqual(baseline["Role/cetirun-sandbox-admission-pod-state"], configured["Role/cetirun-sandbox-admission-pod-state"]) {
		t.Fatal("catalog changed other admission source privileges")
	}
	for _, object := range configured {
		if object.GetKind() == "PersistentVolume" || object.GetKind() == "PersistentVolumeClaim" || object.GetKind() == "ConfigMap" {
			t.Fatal("operator chart provisioned publication content or catalog/bindings itself")
		}
	}
}

func TestSandboxSkillsCatalogChartRejectsInvalidConfiguration(t *testing.T) {
	for _, test := range []struct {
		name, message string
		edit          func(map[string]any)
	}{
		{"without sources", "requires enabled owned bootstrap", func(s map[string]any) {
			s["enabled"], s["bootstrapOwnedPrerequisites"] = false, false
		}},
		{"without bootstrap", "requires enabled owned bootstrap", func(s map[string]any) { s["bootstrapOwnedPrerequisites"] = false }},
		{"wrong type", "must be a resource name", func(s map[string]any) { s["skillsCatalogConfigMapName"] = true }},
		{"invalid name", "separate valid ConfigMap name", func(s map[string]any) { s["skillsCatalogConfigMapName"] = "Other/namespace" }},
		{"shared platform document", "separate valid ConfigMap name", func(s map[string]any) { s["skillsCatalogConfigMapName"] = "sandbox-platform" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := sandboxSourcesChartValues(t)
			sources := values["sandboxSources"].(map[string]any)
			sources["skillsCatalogConfigMapName"] = "published-skills"
			test.edit(sources)
			if _, err := renderSandboxSourcesChart(t, values); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatal("invalid catalog chart setting was accepted or misclassified:", err)
			}
		})
	}
}
