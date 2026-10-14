// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package kube

import (
	"context"
	"errors"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestTeamAndSandboxChangesUseExactVersionsAndCoordination(t *testing.T) {
	for _, kind := range []string{"Team", "Sandbox"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			meta := metav1.ObjectMeta{Name: "example", UID: "configuration-uid", Labels: map[string]string{"example.com/keep": "yes"}}
			var object client.Object = &api.Team{ObjectMeta: meta, Spec: api.TeamSpec{AgentPolicyRef: "old", SandboxRefs: []string{"standard"}, DefaultSandboxRef: "standard"}}
			if kind == "Sandbox" {
				object = &api.Sandbox{ObjectMeta: meta, Spec: api.SandboxSpec{Type: api.SandboxKata}}
			}
			storage := newFakeClient(object)
			if err := storage.Get(ctx, client.ObjectKeyFromObject(object), object); err != nil {
				t.Fatal(err)
			}
			original := ConfigurationVersion{UID: object.GetUID(), ResourceVersion: object.GetResourceVersion()}
			coordination := &recordingPolicyCoordination{storage: storage}
			portal := NewClient(storage)
			portal.SetConfigurationCoordination(coordination)
			update := func(version ConfigurationVersion) (client.Object, error) {
				if kind == "Team" {
					return portal.UpdateTeamSpec(ctx, object.GetName(), api.TeamSpec{AgentPolicyRef: "new", SandboxRefs: []string{"restricted"}, DefaultSandboxRef: "restricted"}, version)
				}
				return portal.UpdateSandboxSpec(ctx, object.GetName(), api.SandboxSpec{Type: api.SandboxGVisor}, version)
			}
			for _, invalid := range []ConfigurationVersion{{}, {UID: "replacement", ResourceVersion: original.ResourceVersion}, {UID: original.UID, ResourceVersion: "stale"}} {
				if _, err := update(invalid); !errors.Is(err, ErrConfigurationChanged) {
					t.Fatalf("invalid version accepted: %v", err)
				}
				if coordination.before != nil {
					t.Fatal("invalid version entered coordination")
				}
			}
			updated, err := update(original)
			if err != nil {
				t.Fatal(err)
			}
			if coordination.before == nil || coordination.after == nil || coordination.before.GetUID() != original.UID || coordination.before.GetResourceVersion() != original.ResourceVersion || coordination.after.GetResourceVersion() != original.ResourceVersion {
				t.Fatal("configuration update did not preserve the exact reviewed before/after identity")
			}
			if updated.GetLabels()["example.com/keep"] != "yes" {
				t.Fatal("configuration update dropped unrelated metadata")
			}
			current := ConfigurationVersion{UID: updated.GetUID(), ResourceVersion: updated.GetResourceVersion()}
			coordination.before, coordination.after = nil, nil
			if _, err := update(current); err != nil {
				t.Fatal(err)
			}
			if coordination.before != nil {
				t.Fatal("unchanged spec should not reserve a configuration operation")
			}
			remove := func(version ConfigurationVersion) error {
				if kind == "Team" {
					return portal.DeleteTeam(ctx, object.GetName(), version)
				}
				return portal.DeleteSandbox(ctx, object.GetName(), version)
			}
			if err := remove(original); !errors.Is(err, ErrConfigurationChanged) {
				t.Fatalf("stale deletion accepted: %v", err)
			}
			if coordination.deleted != nil {
				t.Fatal("stale deletion entered coordination")
			}
			if err := remove(current); err != nil {
				t.Fatal(err)
			}
			if coordination.deleted == nil || coordination.deleted.GetUID() != current.UID || coordination.deleted.GetResourceVersion() != current.ResourceVersion {
				t.Fatal("deletion was not bound to the reviewed object")
			}
		})
	}
}
