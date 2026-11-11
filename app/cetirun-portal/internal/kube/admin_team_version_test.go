// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package kube

import (
	"context"
	"errors"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestTeamGenerationAllowsStatusRefresh(t *testing.T) {
	for _, coordinated := range []bool{false, true} {
		for _, operation := range []string{"update", "noop", "delete"} {
			t.Run(operation+map[bool]string{false: "/direct", true: "/coordinated"}[coordinated], func(t *testing.T) {
				ctx := context.Background()
				team := &api.Team{ObjectMeta: metav1.ObjectMeta{Name: "example", UID: "team-uid", Generation: 3,
					Labels: map[string]string{"example.com/keep": "yes"}}, Spec: api.TeamSpec{MemberContext: api.TeamContextSpec{Role: "Engineer"}}}
				storage := fake.NewClientBuilder().WithScheme(newScheme()).WithStatusSubresource(team).WithObjects(team).Build()
				if err := storage.Get(ctx, client.ObjectKeyFromObject(team), team); err != nil {
					t.Fatal(err)
				}
				version := ConfigurationVersion{UID: team.UID, ResourceVersion: team.ResourceVersion, Generation: team.Generation}
				team.Status.ObservedGeneration = team.Generation
				if err := storage.Status().Update(ctx, team); err != nil {
					t.Fatal(err)
				}
				currentRV := team.ResourceVersion
				if currentRV == version.ResourceVersion {
					t.Fatal("fixture did not advance status version")
				}
				portal := NewClient(storage)
				coordination := &recordingPolicyCoordination{storage: storage}
				if coordinated {
					portal.SetConfigurationCoordination(coordination)
				}
				if operation == "delete" {
					if err := portal.DeleteTeam(ctx, team.Name, version); err != nil {
						t.Fatal(err)
					}
					if err := storage.Get(ctx, client.ObjectKeyFromObject(team), &api.Team{}); !apierrors.IsNotFound(err) {
						t.Fatalf("not deleted: %v", err)
					}
					if coordinated && coordination.deleted.GetResourceVersion() != currentRV {
						t.Fatal("delete used the browser's stale resourceVersion")
					}
					return
				}
				desired := *team.Spec.DeepCopy()
				if operation == "update" {
					desired.MemberContext.Role = "Reviewer"
				}
				updated, err := portal.UpdateTeamSpec(ctx, team.Name, desired, version)
				if err != nil {
					t.Fatal(err)
				}
				if updated.Spec.MemberContext.Role != desired.MemberContext.Role || updated.Status.ObservedGeneration != 3 || updated.Labels["example.com/keep"] != "yes" {
					t.Fatalf("lost configuration, status or metadata: %+v", updated)
				}
				if operation == "noop" {
					if updated.ResourceVersion != currentRV || coordination.before != nil {
						t.Fatal("no-op wrote or reserved a change")
					}
				} else if coordinated && (coordination.before.GetResourceVersion() != currentRV || coordination.after.GetResourceVersion() != currentRV) {
					t.Fatal("coordination did not use latest exact before/after versions")
				}
			})
		}
	}
}

func TestTeamGenerationRejectsChangedConfiguration(t *testing.T) {
	for _, scenario := range []string{"legacy-stale", "changed-generation", "replacement", "missing-uid", "missing-rv", "negative-generation", "terminating"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			team := &api.Team{ObjectMeta: metav1.ObjectMeta{Name: "example", UID: "team-uid", Generation: 3, ResourceVersion: "10"}}
			version := ConfigurationVersion{UID: team.UID, ResourceVersion: "9", Generation: team.Generation}
			switch scenario {
			case "legacy-stale":
				version.Generation = 0
			case "changed-generation":
				version.Generation--
			case "replacement":
				version.UID = "old-uid"
			case "missing-uid":
				version.UID = ""
			case "missing-rv":
				version.ResourceVersion = ""
			case "negative-generation":
				version.Generation = -1
			case "terminating":
				now := metav1.Now()
				team.DeletionTimestamp = &now
				team.Finalizers = []string{"example.com/cleanup"}
			}
			storage := newFakeClient(team)
			coordination := &recordingPolicyCoordination{storage: storage}
			portal := NewClient(storage)
			portal.SetConfigurationCoordination(coordination)
			// Even an unchanged save must not accept a different reviewed configuration.
			if _, err := portal.UpdateTeamSpec(ctx, team.Name, team.Spec, version); !errors.Is(err, ErrConfigurationChanged) {
				t.Fatalf("update accepted stale configuration: %v", err)
			}
			if err := portal.DeleteTeam(ctx, team.Name, version); !errors.Is(err, ErrConfigurationChanged) {
				t.Fatalf("delete accepted stale configuration: %v", err)
			}
			if coordination.before != nil || coordination.deleted != nil {
				t.Fatal("rejected configuration entered mutation coordination")
			}
		})
	}
}

func TestTeamGenerationPreservesConditionalWriteRace(t *testing.T) {
	for _, operation := range []string{"update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			team := &api.Team{ObjectMeta: metav1.ObjectMeta{Name: "example", UID: "team-uid", Generation: 1}}
			storage := fake.NewClientBuilder().WithScheme(newScheme()).WithStatusSubresource(team).WithObjects(team).Build()
			if err := storage.Get(ctx, client.ObjectKeyFromObject(team), team); err != nil {
				t.Fatal(err)
			}
			version := ConfigurationVersion{UID: team.UID, ResourceVersion: team.ResourceVersion, Generation: team.Generation}
			attempts := 0
			refresh := func(c client.WithWatch) {
				attempts++
				current := &api.Team{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(team), current); err != nil {
					t.Fatal(err)
				}
				current.Status.ObservedGeneration++
				if err := c.Status().Update(ctx, current); err != nil {
					t.Fatal(err)
				}
			}
			writer := interceptor.NewClient(storage, interceptor.Funcs{
				Patch: func(ctx context.Context, c client.WithWatch, object client.Object, patch client.Patch, opts ...client.PatchOption) error {
					refresh(c)
					return c.Patch(ctx, object, patch, opts...)
				},
				Delete: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.DeleteOption) error {
					refresh(c)
					return c.Delete(ctx, object, opts...)
				},
			})
			portal := NewClient(writer)
			var err error
			if operation == "update" {
				_, err = portal.UpdateTeamSpec(ctx, team.Name, api.TeamSpec{MemberContext: api.TeamContextSpec{Role: "Reviewer"}}, version)
			} else {
				err = portal.DeleteTeam(ctx, team.Name, version)
			}
			if !apierrors.IsConflict(err) || attempts != 1 {
				t.Fatalf("race was ignored or retried: attempts=%d err=%v", attempts, err)
			}
			current := &api.Team{}
			if err := storage.Get(ctx, client.ObjectKeyFromObject(team), current); err != nil {
				t.Fatal(err)
			}
			if current.Spec.MemberContext.Role != "" {
				t.Fatal("conflicting write applied")
			}
		})
	}
}
