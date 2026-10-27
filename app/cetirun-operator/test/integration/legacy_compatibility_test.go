// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package integration_test

import (
	"context"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
)

func TestLegacyAgentAndTeamSchemaRejectUngovernedExecution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	teams := coreClient.Resource(schema.GroupVersionResource{Group: "cetirun.microsoft.com", Version: "v1alpha1", Resource: "teams"})
	team := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cetirun.microsoft.com/v1alpha1", "kind": "Team",
		"metadata": map[string]any{"name": "schema-governed-team"},
		"spec":     map[string]any{"agentPolicyRef": "example-policy", "sandboxRefs": []any{"standard"}, "defaultSandboxRef": "standard"},
	}}
	for _, field := range []string{"agentPolicyRef", "sandboxRefs", "defaultSandboxRef"} {
		missing := team.DeepCopy()
		unstructured.RemoveNestedField(missing.Object, "spec", field)
		if _, err := teams.Create(ctx, missing, metav1.CreateOptions{FieldValidation: "Strict"}); !apierrors.IsInvalid(err) {
			t.Fatalf("Team without %s must be rejected: %v", field, err)
		}
	}
	storedTeam, err := teams.Create(ctx, team, metav1.CreateOptions{FieldValidation: "Strict"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = teams.Delete(context.Background(), team.GetName(), metav1.DeleteOptions{}) })
	if err := unstructured.SetNestedField(storedTeam.Object, "openai", "spec", "defaults", "provider"); err != nil {
		t.Fatal(err)
	}
	if _, err := teams.Update(ctx, storedTeam, metav1.UpdateOptions{FieldValidation: "Strict"}); !apierrors.IsInvalid(err) {
		t.Fatalf("removed Team provider default must be rejected on update: %v", err)
	}
	for _, typ := range []string{"openclaw", "hermes", "custom"} {
		t.Run(typ, func(t *testing.T) {
			for _, field := range []string{"team", "sandboxRef"} {
				missing := instance(typ)
				unstructured.RemoveNestedField(missing.Object, "spec", field)
				if _, err := instances.Create(ctx, missing, metav1.CreateOptions{FieldValidation: "Strict"}); !apierrors.IsInvalid(err) {
					t.Fatalf("Agent without %s must be rejected: %v", field, err)
				}
			}
			obj := instance(typ)
			if err := unstructured.SetNestedField(obj.Object, "Existing display context", "spec", "user", "team"); err != nil {
				t.Fatal(err)
			}
			stored := create(t, obj)
			if value, _, err := unstructured.NestedString(stored.Object, "spec", "user", "team"); err != nil || value != "Existing display context" {
				t.Fatal("display context must remain independent of the required Team binding:", err)
			}
			if err := unstructured.SetNestedField(stored.Object, true, "spec", "suspended"); err != nil {
				t.Fatal(err)
			}
			if _, err := instances.Update(ctx, stored, metav1.UpdateOptions{FieldValidation: "Strict"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A schema upgrade must leave a previously stored, paused legacy object
// available for status observation and safe finalizer cleanup. Use a separate
// synthetic CRD so the other integration cases retain the current schema.
func TestPausedLegacyAgentSurvivesRequiredGovernanceSchemaUpgrade(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	crds := coreClient.Resource(schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"})
	current, err := crds.Get(ctx, "agents.cetirun.microsoft.com", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	current.SetName("upgradeagents.upgrade.example.com")
	current.SetUID("")
	current.SetResourceVersion("")
	current.SetGeneration(0)
	current.SetManagedFields(nil)
	current.SetAnnotations(nil)
	current.SetCreationTimestamp(metav1.Time{})
	delete(current.Object, "status")
	_ = unstructured.SetNestedField(current.Object, "upgrade.example.com", "spec", "group")
	_ = unstructured.SetNestedMap(current.Object, map[string]any{"kind": "UpgradeAgent", "listKind": "UpgradeAgentList", "plural": "upgradeagents", "singular": "upgradeagent"}, "spec", "names")
	old := current.DeepCopy()
	versions, _, _ := unstructured.NestedSlice(old.Object, "spec", "versions")
	properties := versions[0].(map[string]any)["schema"].(map[string]any)["openAPIV3Schema"].(map[string]any)["properties"].(map[string]any)
	properties["spec"].(map[string]any)["required"] = []any{"agent", "user"}
	_ = unstructured.SetNestedSlice(old.Object, versions, "spec", "versions")
	_, err = crds.Create(ctx, old, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = crds.Delete(context.Background(), current.GetName(), metav1.DeleteOptions{}) })
	resources := coreClient.Resource(schema.GroupVersionResource{Group: "upgrade.example.com", Version: "v1alpha1", Resource: "upgradeagents"})
	legacy := instance("hermes")
	legacy.SetAPIVersion("upgrade.example.com/v1alpha1")
	legacy.SetKind("UpgradeAgent")
	legacy.SetFinalizers([]string{"upgrade.example.com/retain-home"})
	unstructured.RemoveNestedField(legacy.Object, "spec", "team")
	unstructured.RemoveNestedField(legacy.Object, "spec", "sandboxRef")
	_ = unstructured.SetNestedField(legacy.Object, true, "spec", "suspended")
	var stored *unstructured.Unstructured
	if err := wait.PollUntilContextTimeout(ctx, 50*time.Millisecond, 10*time.Second, true, func(ctx context.Context) (bool, error) {
		stored, err = resources.Create(ctx, legacy, metav1.CreateOptions{})
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return err == nil, err
	}); err != nil {
		t.Fatal(err)
	}
	storedCRD, err := crds.Get(ctx, current.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	current.SetResourceVersion(storedCRD.GetResourceVersion())
	if _, err := crds.Update(ctx, current, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	// Wait until the updated schema rejects a new ungoverned object.
	invalid := legacy.DeepCopy()
	invalid.SetName(legacy.GetName() + "-new")
	invalid.SetFinalizers(nil)
	if err := wait.PollUntilContextTimeout(ctx, 50*time.Millisecond, 10*time.Second, true, func(ctx context.Context) (bool, error) {
		_, err := resources.Create(ctx, invalid, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
		if apierrors.IsInvalid(err) {
			return true, nil
		}
		return false, err
	}); err != nil {
		t.Fatal(err)
	}
	originalUID := stored.GetUID()
	_ = unstructured.SetNestedMap(stored.Object, map[string]any{"phase": "Suspended"}, "status")
	stored, err = resources.UpdateStatus(ctx, stored, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("stored legacy Agent status became unwritable after schema upgrade: %v", err)
	}
	stored.SetFinalizers(nil)
	stored, err = resources.Update(ctx, stored, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("stored legacy Agent finalizer cleanup became unwritable after schema upgrade: %v", err)
	}
	if stored.GetUID() != originalUID {
		t.Fatal("legacy Agent identity changed")
	}
	for _, field := range []string{"team", "sandboxRef"} {
		if _, present, _ := unstructured.NestedFieldNoCopy(stored.Object, "spec", field); present {
			t.Fatalf("upgrade fabricated %s binding", field)
		}
	}
	if suspended, _, _ := unstructured.NestedBool(stored.Object, "spec", "suspended"); !suspended {
		t.Fatal("legacy Agent lost its suspension")
	}
	if err := resources.Delete(ctx, stored.GetName(), metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
}
