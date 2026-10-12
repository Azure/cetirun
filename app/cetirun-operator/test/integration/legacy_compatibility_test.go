// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build envtest

package integration_test

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestLegacyAgentAndTeamSchemaCreateUpdate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	teams := coreClient.Resource(schema.GroupVersionResource{Group: "cetirun.microsoft.com", Version: "v1alpha1", Resource: "teams"})
	team := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cetirun.microsoft.com/v1alpha1", "kind": "Team",
		"metadata": map[string]any{"name": "legacy-schema-team"},
		"spec": map[string]any{
			"memberContext": map[string]any{"role": "Engineer"},
			"defaults":      map[string]any{"provider": "openai"},
		},
	}}
	storedTeam, err := teams.Create(ctx, team, metav1.CreateOptions{FieldValidation: "Strict"})
	if err != nil {
		t.Fatal("legacy Team schema rejected:", err)
	}
	if err := unstructured.SetNestedField(storedTeam.Object, "Reviewer", "spec", "memberContext", "role"); err != nil {
		t.Fatal(err)
	}
	if _, err := teams.Update(ctx, storedTeam, metav1.UpdateOptions{FieldValidation: "Strict"}); err != nil {
		t.Fatal("legacy Team update rejected:", err)
	}
	for _, typ := range []string{"openclaw", "hermes", "custom"} {
		t.Run(typ, func(t *testing.T) {
			for _, teamName := range []string{"", team.GetName()} {
				obj := instance(typ)
				spec := obj.Object["spec"].(map[string]any)
				delete(spec, "sandboxRef")
				delete(spec, "team")
				spec["user"].(map[string]any)["team"] = "legacy display context"
				if teamName != "" {
					spec["team"] = teamName
				}
				stored := create(t, obj)
				if value, _, err := unstructured.NestedString(stored.Object, "spec", "user", "team"); err != nil || value != "legacy display context" {
					t.Fatal("legacy display context was pruned or replaced:", err)
				}
				if _, found, _ := unstructured.NestedFieldNoCopy(stored.Object, "spec", "sandboxRef"); found {
					t.Fatal("schema invented a Sandbox selection")
				}
				if err := unstructured.SetNestedField(stored.Object, true, "spec", "suspended"); err != nil {
					t.Fatal(err)
				}
				if _, err := instances.Update(ctx, stored, metav1.UpdateOptions{FieldValidation: "Strict"}); err != nil {
					t.Fatal("legacy Agent update rejected:", err)
				}
			}
		})
	}
}
