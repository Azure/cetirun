// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"helm.sh/helm/v3/pkg/action"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func reconciledGrantSDKPlan(t *testing.T, source *PreparedRelease, beforeCreate func(context.Context, unstructured.Unstructured) error) *PreparedRelease {
	t.Helper()
	input := source.sandboxBundle.Identity()
	input.Reconciled = true
	bundle, err := w.Build(input)
	if err != nil {
		t.Fatal(err)
	}
	guard := func(context.Context) error { return nil }
	values := sampleInput("hermes", "openai", false)
	object(values["secrets"])["existingSecret"] = "runtime-secret"
	prepared, err := source.ReconcileSandboxWithPreflight(values, bundle, guard, guard, beforeCreate)
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func TestReconciledSandboxSDKGrantSubmissionPrecedesOnlyAttemptedCreates(t *testing.T) {
	for _, scenario := range []string{"success", "transport-failure", "guard-failure", "already-exists"} {
		for _, upgrade := range []bool{false, true} {
			t.Run(scenario+map[bool]string{false: "/install", true: "/upgrade"}[upgrade], func(t *testing.T) {
				source, _, _ := sandboxSDKKubernetesAccessPlans(t)
				injected := errors.New("synthetic grant request failure")
				var authorized []string
				prepared := reconciledGrantSDKPlan(t, source, func(_ context.Context, object unstructured.Unstructured) error {
					if scenario == "guard-failure" {
						return injected
					}
					authorized = append(authorized, object.GetName())
					return nil
				})
				cfg, api, _ := sandboxSDKConfig()
				if upgrade {
					initial := reconciledGrantSDKPlan(t, source, func(context.Context, unstructured.Unstructured) error { return nil })
					if err := (&Client{}).installOrUpgrade(initial.sandboxReconcile.Namespace.Name, initial.releaseName, initial,
						func(string) (*action.Configuration, error) { return cfg, nil }); err != nil {
						t.Fatal(err)
					}
					for path, object := range api.objects {
						if w.KubernetesBindingResource(*object) {
							delete(api.objects, path)
						}
					}
				}
				posts := 0
				api.before = func(call sandboxSDKRequest) {
					if call.method != http.MethodPost || !strings.HasSuffix(call.path, "/clusterrolebindings") {
						return
					}
					var object unstructured.Unstructured
					if err := object.UnmarshalJSON(call.body); err != nil {
						t.Fatal(err)
					}
					posts++
					if !slices.Contains(authorized, object.GetName()) {
						t.Fatal("the real Helm client posted a binding before its submission checkpoint")
					}
					if scenario == "already-exists" {
						object.SetUID("existing-binding")
						object.SetResourceVersion("existing-version")
						api.objects[sandboxTestPath(&object)] = object.DeepCopy()
					}
				}
				api.fail = func(call sandboxSDKRequest) error {
					if scenario == "transport-failure" && call.method == http.MethodPost &&
						strings.HasSuffix(call.path, "/clusterrolebindings") {
						return injected
					}
					return nil
				}
				err := (&Client{}).installOrUpgrade(prepared.sandboxReconcile.Namespace.Name, prepared.releaseName, prepared,
					func(string) (*action.Configuration, error) { return cfg, nil })
				if scenario == "success" {
					if err != nil || posts != 2 || len(authorized) != 2 {
						t.Fatalf("complete grant install: posts=%d authorized=%d error=%v", posts, len(authorized), err)
					}
				} else {
					want := 1
					if scenario == "guard-failure" {
						want = 0
					}
					if err == nil || posts != want || len(authorized) != want {
						t.Fatalf("partial grant install: posts=%d authorized=%d error=%v", posts, len(authorized), err)
					}
				}
			})
		}
	}
}

func TestReconciledSandboxSDKGrantUpdatesPreserveConditionalOwnership(t *testing.T) {
	for _, race := range []bool{false, true} {
		t.Run(map[bool]string{false: "metadata-update", true: "replacement-race"}[race], func(t *testing.T) {
			old, next, retired := sandboxSDKKubernetesAccessPlans(t)
			authorize := func(context.Context, unstructured.Unstructured) error { return nil }
			previous := reconciledGrantSDKPlan(t, old, authorize)
			desired := reconciledGrantSDKPlan(t, next, authorize)
			cfg, api, _ := sandboxSDKConfig()
			configure := func(string) (*action.Configuration, error) { return cfg, nil }
			if err := (&Client{}).installOrUpgrade(previous.sandboxReconcile.Namespace.Name, previous.releaseName, previous, configure); err != nil {
				t.Fatal(err)
			}
			retiredPath := sandboxTestPath(retired)
			retained := api.objects[retiredPath].DeepCopy()
			retained.SetUID("retired-name-replacement")
			retained.SetResourceVersion("retired-name-version")
			retained.SetOwnerReferences(nil)
			api.objects[retiredPath] = retained
			requests, patched := len(api.requests), false
			api.before = func(call sandboxSDKRequest) {
				if call.method != http.MethodPatch || !strings.Contains(call.path, "/clusterrolebindings/") {
					return
				}
				patched = true
				var patch struct {
					Metadata struct{ UID, ResourceVersion string }
				}
				if err := json.Unmarshal(call.body, &patch); err != nil || patch.Metadata.UID == "" || patch.Metadata.ResourceVersion == "" {
					t.Fatal("binding update omitted atomic identity/version preconditions:", err)
				}
				if race {
					replacement := api.objects[call.path].DeepCopy()
					replacement.SetUID("target-name-replacement")
					replacement.SetResourceVersion("target-name-version")
					api.objects[call.path] = replacement
				}
			}
			err := (&Client{}).installOrUpgrade(desired.sandboxReconcile.Namespace.Name, desired.releaseName, desired, configure)
			if !patched || (err != nil) != race {
				t.Fatalf("conditional grant update: patched=%t error=%v", patched, err)
			}
			if sandboxJSONHash(api.objects[retiredPath]) != sandboxJSONHash(retained) {
				t.Fatal("Helm changed a retired binding outside journal authority")
			}
			for _, call := range api.requests[requests:] {
				if call.method == http.MethodDelete && strings.Contains(call.path, "/clusterrolebindings/") {
					t.Fatal("Helm performed name-only pruning of a grant")
				}
			}
		})
	}
}
