// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package sandboxadmission

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	kyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	"sigs.k8s.io/yaml"
)

func TestHelmManagedCRDUpgrade(t *testing.T) {
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		t.Fatal("KUBEBUILDER_ASSETS must name local envtest binaries")
	}
	directory := t.TempDir()
	definitions := renderedManagedCRDs(t)
	beforeChart := upgradeTestChart(t, directory, "before", "0.0.1", definitions)
	data, err := os.ReadFile("../../test/fixtures/registry-credentials-status.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var futureField apiextensionsv1.JSONSchemaProps
	if err := yaml.UnmarshalStrict(data, &futureField); err != nil {
		t.Fatal(err)
	}
	for _, definition := range definitions {
		if definition.Spec.Names.Kind == "Agent" {
			status := definition.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["status"]
			status.Properties["futureRegistryCredentials"] = futureField
			definition.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["status"] = status
		}
	}
	afterChart := upgradeTestChart(t, directory, "after", "0.0.2", definitions)
	environment := &envtest.Environment{BinaryAssetsDirectory: assets, UseExistingCluster: ptr.To(false),
		ControlPlaneStartTimeout: 45 * time.Second, ControlPlaneStopTimeout: 30 * time.Second}
	cfg, err := environment.Start()
	if err != nil {
		_ = environment.Stop()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	endpoint, err := url.Parse(cfg.Host)
	if err != nil || !net.ParseIP(endpoint.Hostname()).IsLoopback() {
		t.Fatal("refusing a non-loopback API server")
	}
	cfg.QPS, cfg.Burst = 1000, 1000
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{api.AddToScheme, corev1.AddToScheme, admissionregistrationv1.AddToScheme, apiextensionsv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	cl, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	kubeconfig := upgradeTestKubeconfig(t, directory, cfg)
	helm := func(arguments ...string) {
		t.Helper()
		arguments = append(arguments, "--namespace", "crd-upgrade-test", "--kubeconfig", kubeconfig)
		command := exec.CommandContext(ctx, "helm", arguments...)
		command.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig,
			"HELM_CACHE_HOME="+filepath.Join(directory, "cache"), "HELM_CONFIG_HOME="+filepath.Join(directory, "config"),
			"HELM_DATA_HOME="+filepath.Join(directory, "data"), "HELM_REGISTRY_CONFIG="+os.DevNull, "HELM_REPOSITORY_CONFIG="+os.DevNull)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("Helm %s: %v\n%s", arguments[0], err, output)
		}
	}
	helm("upgrade", "--install", "crd-upgrade-test", beforeChart, "--create-namespace", "--wait", "--timeout=45s")
	var originalCRD apiextensionsv1.CustomResourceDefinition
	if err := cl.Get(ctx, client.ObjectKey{Name: "agents." + api.GroupVersion.Group}, &originalCRD); err != nil {
		t.Fatal(err)
	}
	agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "upgrade-test"}, Spec: api.AgentSpec{
		Agent: api.HarnessSpec{Type: api.AgentTypeHermes}, User: api.UserSpec{Alias: "upgrade-user", DisplayName: "Example User"}, GatewayTokenSecretRef: "synthetic-token",
	}}
	if err := cl.Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	get := func() *unstructured.Unstructured {
		t.Helper()
		object := &unstructured.Unstructured{}
		object.SetAPIVersion(api.GroupVersion.String())
		object.SetKind("Agent")
		if err := cl.Get(ctx, client.ObjectKeyFromObject(agent), object); err != nil {
			t.Fatal(err)
		}
		return object
	}
	config := testConfig()
	config.ValidationMode = ValidationCompatible
	config.AdminGroups = []string{"system:masters"}
	compatible, err := NewHandler(cl, config)
	if err != nil {
		t.Fatal(err)
	}
	config.ValidationMode = ValidationStrict
	strict, err := NewHandler(cl, config)
	if err != nil {
		t.Fatal(err)
	}
	var strictMode atomic.Bool
	var calls atomic.Int64
	serve := func(mutate bool) admission.HandlerFunc {
		return func(ctx context.Context, request admission.Request) admission.Response {
			calls.Add(1)
			handler := compatible
			if strictMode.Load() {
				handler = strict
			}
			if mutate {
				return handler.DefaultAgent(ctx, request)
			}
			return handler.Validate(ctx, request)
		}
	}
	mux := http.NewServeMux()
	mux.Handle(ValidationPath, &admission.Webhook{Handler: serve(false)})
	mux.Handle(DefaultingPath, &admission.Webhook{Handler: serve(true)})
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	validating, mutating := renderedWebhooks(t, ca)
	for i := range validating.Webhooks {
		hook := &validating.Webhooks[i]
		address := server.URL + *hook.ClientConfig.Service.Path
		hook.ClientConfig.Service, hook.ClientConfig.URL = nil, &address
	}
	for i := range mutating.Webhooks {
		hook := &mutating.Webhooks[i]
		address := server.URL + *hook.ClientConfig.Service.Path
		hook.ClientConfig.Service, hook.ClientConfig.URL = nil, &address
	}
	for _, object := range []client.Object{validating, mutating} {
		if err := cl.Create(ctx, object); err != nil {
			t.Fatal(err)
		}
	}
	patch := func(ctx context.Context) error {
		object := &unstructured.Unstructured{}
		object.SetAPIVersion(api.GroupVersion.String())
		object.SetKind("Agent")
		object.SetName(agent.Name)
		data, _ := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{"example.com/heartbeat": fmt.Sprint(time.Now().UnixNano())}}})
		return cl.Patch(ctx, object, client.RawPatch(types.MergePatchType, data))
	}
	if err := wait.PollUntilContextTimeout(ctx, 20*time.Millisecond, 10*time.Second, true, func(ctx context.Context) (bool, error) {
		return patch(ctx) == nil && calls.Load() >= 2, nil
	}); err != nil {
		t.Fatal(err)
	}
	writerCtx, stopWriter := context.WithCancel(ctx)
	done := make(chan struct{})
	var writes, failures atomic.Int64
	go func() {
		defer close(done)
		for writerCtx.Err() == nil {
			if err := patch(writerCtx); err == nil {
				writes.Add(1)
			} else if writerCtx.Err() == nil {
				failures.Add(1)
			}
			select {
			case <-writerCtx.Done():
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
	t.Cleanup(func() { stopWriter(); <-done })
	if err := wait.PollUntilContextTimeout(ctx, 10*time.Millisecond, 10*time.Second, true, func(context.Context) (bool, error) { return writes.Load() > 0, nil }); err != nil {
		t.Fatal(err)
	}
	beforeUpgrade := writes.Load()
	helm("upgrade", "--install", "crd-upgrade-test", afterChart, "--wait", "--timeout=45s")
	if writes.Load() <= beforeUpgrade {
		t.Fatal("no requests completed while Helm was upgrading")
	}
	future := map[string]any{"sourceUID": "synthetic-source", "sourceResourceVersion": "7", "pullSecretUID": "synthetic-pull"}
	if err := wait.PollUntilContextTimeout(ctx, 20*time.Millisecond, 10*time.Second, true, func(ctx context.Context) (bool, error) {
		object := get()
		if err := unstructured.SetNestedMap(object.Object, future, "status", "futureRegistryCredentials"); err != nil {
			return false, err
		}
		return cl.Status().Update(ctx, object, client.FieldValidation(metav1.FieldValidationStrict)) == nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	afterStatus := writes.Load()
	if err := wait.PollUntilContextTimeout(ctx, 10*time.Millisecond, 10*time.Second, true, func(context.Context) (bool, error) {
		return writes.Load() >= afterStatus+20, nil
	}); err != nil {
		t.Fatal(err)
	}
	stopWriter()
	<-done
	if failures.Load() != 0 {
		t.Fatalf("compatible writes failed during schema upgrade: %d", failures.Load())
	}
	t.Logf("compatible writes: %d successful, 0 failed; at least 20 after the new status appeared", writes.Load())
	strictMode.Store(true)
	if err := patch(ctx); err == nil || !strings.Contains(err.Error(), `unknown field "futureRegistryCredentials"`) {
		t.Fatalf("strict mode did not reproduce the version-skew rejection: %v", err)
	}
	strictMode.Store(false)
	if err := patch(ctx); err != nil {
		t.Fatal(err)
	}
	object := get()
	if err := unstructured.SetNestedMap(object.Object, map[string]any{"sourceUID": "incomplete"}, "status", "futureRegistryCredentials"); err != nil {
		t.Fatal(err)
	}
	if err := cl.Status().Update(ctx, object); !apierrors.IsInvalid(err) {
		t.Fatalf("schema required fields were weakened: %v", err)
	}
	helm("uninstall", "crd-upgrade-test", "--wait", "--timeout=45s")
	var retained apiextensionsv1.CustomResourceDefinition
	if err := cl.Get(ctx, client.ObjectKey{Name: originalCRD.Name}, &retained); err != nil {
		t.Fatal(err)
	}
	object = get()
	if retained.UID != originalCRD.UID || object.GetUID() != agent.UID {
		t.Fatal("upgrade/uninstall replaced retained CRD or Agent data")
	}
	value, found, err := unstructured.NestedMap(object.Object, "status", "futureRegistryCredentials")
	if err != nil || !found || !reflect.DeepEqual(value, future) {
		t.Fatal("future status was not retained", value, err)
	}
}

func renderedManagedCRDs(t *testing.T) []*apiextensionsv1.CustomResourceDefinition {
	t.Helper()
	command := exec.Command("helm", "template", "crd-test", "../../chart")
	command.Env = append(os.Environ(), "KUBECONFIG="+os.DevNull, "HELM_REGISTRY_CONFIG="+os.DevNull, "HELM_REPOSITORY_CONFIG="+os.DevNull)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("render chart: %v\n%s", err, output)
	}
	decoder := kyaml.NewYAMLOrJSONDecoder(strings.NewReader(string(output)), 4096)
	var definitions []*apiextensionsv1.CustomResourceDefinition
	for {
		var object unstructured.Unstructured
		if err := decoder.Decode(&object); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		if object.GetKind() != "CustomResourceDefinition" {
			continue
		}
		definition := &apiextensionsv1.CustomResourceDefinition{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, definition); err != nil {
			t.Fatal(err)
		}
		if definition.Annotations["helm.sh/resource-policy"] != "keep" || definition.Annotations["helm.sh/hook"] != "" {
			t.Fatal("CRDs must be retained ordinary templates, not hooks")
		}
		definitions = append(definitions, definition)
	}
	if len(definitions) != 4 {
		t.Fatalf("ordinary Helm rendering must contain all four CRDs, got %d", len(definitions))
	}
	return definitions
}

func upgradeTestChart(t *testing.T, directory, name, version string, definitions []*apiextensionsv1.CustomResourceDefinition) string {
	t.Helper()
	chart := filepath.Join(directory, name)
	if err := os.MkdirAll(filepath.Join(chart, "templates"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chart, "Chart.yaml"), []byte("apiVersion: v2\nname: crd-upgrade-test\nversion: "+version+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, definition := range definitions {
		data, err := yaml.Marshal(definition)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(chart, "templates", definition.Name+".yaml"), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	return chart
}

func upgradeTestKubeconfig(t *testing.T, directory string, cfg *rest.Config) string {
	t.Helper()
	config := clientcmdapi.Config{
		Clusters:  map[string]*clientcmdapi.Cluster{"test": {Server: cfg.Host, CertificateAuthorityData: cfg.CAData}},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {ClientCertificateData: cfg.CertData, ClientKeyData: cfg.KeyData}},
		Contexts:  map[string]*clientcmdapi.Context{"test": {Cluster: "test", AuthInfo: "test"}}, CurrentContext: "test",
	}
	data, err := clientcmd.Write(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "kubeconfig")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
