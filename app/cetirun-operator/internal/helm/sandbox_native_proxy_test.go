// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload/testfixture"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func nativeProxyRenderInput() (w.Input, map[string]any) {
	input := testfixture.New("openclaw").Input
	input.Resolution.Capabilities.AuthenticatedIngress = true
	input.Helpers.Desktop = false
	input.GatewayKey = &w.ObjectIdentity{Name: "gateway-key", UID: "gateway-key-uid"}
	input.AuthorizedSecrets = append(input.AuthorizedSecrets, input.GatewayKey.Name)
	input.Controls.IngressMode, input.Resolution.AgentPolicy.Network.Ingress.Mode = api.AgentPolicyIngressAuthenticated, api.AgentPolicyIngressAuthenticated
	input.Controls.TrustedProxyIPs = []string{"192.0.2.10", "2001:db8::10"}
	values := sampleInput("openclaw", "openai", false)
	secrets := values["secrets"].(map[string]any)
	secrets["existingSecret"], secrets["apiBase"], secrets["model"] = "runtime-secret", "http://gateway.example.com/v1", "example-model"
	values["aiGateway"] = map[string]any{
		"enabled": true, "keySecretName": input.GatewayKey.Name, "keySecretKey": "api-key",
		"models": []any{map[string]any{"id": "example-model", "name": "Example"}},
	}
	values["openclaw"] = map[string]any{"config": map[string]any{"gateway": map[string]any{
		"trustedProxies": []any{"0.0.0.0/0"},
		"auth":           map[string]any{"mode": "token"},
		"controlUi":      map[string]any{"dangerouslyDisableDeviceAuth": false, "allowInsecureAuth": false},
	}}}
	return input, values
}

func TestSandboxNativeProxyTrustRendering(t *testing.T) {
	input, values := nativeProxyRenderInput()
	bundle, err := w.Build(input)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareSandboxChart(testChart(t), values, nil, bundle)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	files := object(object(prepared.Values["runtime"])["configFiles"])
	if err := json.Unmarshal([]byte(text(files["openclaw.json"])), &config); err != nil {
		t.Fatal(err)
	}
	gateway := object(config["gateway"])
	if !reflect.DeepEqual(gateway["trustedProxies"], []any{"192.0.2.10", "2001:db8::10"}) ||
		object(gateway["auth"])["mode"] != "token" ||
		object(gateway["controlUi"])["dangerouslyDisableDeviceAuth"] != false ||
		object(gateway["controlUi"])["allowInsecureAuth"] != false {
		t.Fatal("native proxy trust was broadened, discarded or changed authentication")
	}
	if !reflect.DeepEqual(object(prepared.Values[w.ValuesKey])["trustedProxyIPs"], gateway["trustedProxies"]) {
		t.Fatal("Go-rendered native config and protected chart hook disagree")
	}
	input.Controls.TrustedProxyIPs[0] = "192.0.2.11"
	changedBundle, err := w.Build(input)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := PrepareSandboxChart(testChart(t), values, nil, changedBundle)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.WorkloadHash == changed.WorkloadHash || prepared.manifestHash == changed.manifestHash {
		t.Fatal("rendered proxy trust was not bound into the workload and manifest hashes")
	}
}

func TestSandboxNativeProxyTrustRejectsMissingOrModifiedRender(t *testing.T) {
	input, _ := nativeProxyRenderInput()
	bundle, err := w.Build(input)
	if err != nil {
		t.Fatal(err)
	}
	prepared := &PreparedRelease{sandboxBundle: bundle}
	for _, proxies := range [][]string{
		input.Controls.TrustedProxyIPs, nil, {"0.0.0.0/0"}, {"::/0"}, {"127.0.0.6"},
		{"192.0.2.10"}, {"192.0.2.10", "192.0.2.11", "2001:db8::10"},
	} {
		raw, err := json.Marshal(map[string]any{"gateway": map[string]any{"trustedProxies": proxies}})
		if err != nil {
			t.Fatal(err)
		}
		object := unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "ConfigMap",
			"metadata": map[string]any{"name": input.WorkloadName + "-config"},
			"data":     map[string]any{"openclaw.json": string(raw)},
		}}
		err = prepared.validateSandboxNative(object)
		if (err == nil) != slices.Equal(proxies, input.Controls.TrustedProxyIPs) {
			t.Fatalf("render validation mismatch for %v: %v", proxies, err)
		}
	}
}

func TestSandboxProxyTrustAbsentHookPreservesHeldAndLegacyRendering(t *testing.T) {
	held, _, _ := sandboxRender(t, "openclaw", true, false)
	legacy, err := PrepareChart(testChart(t), sampleInput("openclaw", "openai", false))
	if err != nil {
		t.Fatal(err)
	}
	for mode, prepared := range map[string]*PreparedRelease{"held": held, "legacy": legacy} {
		if _, present := object(prepared.Values[w.ValuesKey])["trustedProxyIPs"]; present {
			t.Fatalf("%s rendering gained a native proxy binding", mode)
		}
		var config map[string]any
		files := object(object(prepared.Values["runtime"])["configFiles"])
		if err := json.Unmarshal([]byte(text(files["openclaw.json"])), &config); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(object(config["gateway"])["trustedProxies"], []any{"127.0.0.6"}) {
			t.Fatalf("%s rendering changed proxy trust without a native hook", mode)
		}
	}
}
