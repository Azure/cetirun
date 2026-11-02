// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"reflect"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/event"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

func TestAgentTeamRuntimeInputPredicateAndFingerprint(t *testing.T) {
	now := metav1.NewTime(time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC))
	deletion := now.DeepCopy()
	team := &api.Team{
		TypeMeta: metav1.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "Team"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            "engineering",
			UID:             types.UID("team-uid"),
			Generation:      3,
			ResourceVersion: "100",
			Labels:          map[string]string{"role": "engineering"},
			Annotations:     map[string]string{"owner": "admin"},
			Finalizers:      []string{"cetirun.microsoft.com/finalizer"},
			ManagedFields:   []metav1.ManagedFieldsEntry{{Manager: "controller"}},
		},
		Spec: api.TeamSpec{
			MemberContext: api.TeamContextSpec{Role: "Engineer"},
			Skills:        []string{"npm:fetch"},
		},
		Status: api.TeamStatus{
			ObservedGeneration: 3,
			Conditions:         []metav1.Condition{{Type: api.ConditionTeamAccepted, Status: metav1.ConditionTrue, Reason: "Accepted", LastTransitionTime: now}},
			ModelRouters:       []api.TeamModelRouterStatus{{Name: "router", GatewayModel: "internal/router", Ready: true}},
			Compatibility: &api.TeamCompatibilityStatus{
				State:     api.CompatibilityCompatible,
				Team:      api.CompatibilityReference{Name: "engineering", UID: "team-uid", Generation: 3},
				CheckedAt: now, ValidUntil: metav1.NewTime(now.Add(30 * time.Second)),
			},
		},
	}
	filter := AgentTeamRuntimeInputPredicate()

	ignored := map[string]func(*api.Team){
		"resource version": func(changed *api.Team) { changed.ResourceVersion = "101" },
		"managed fields": func(changed *api.Team) {
			changed.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "status-writer"}}
		},
		"type metadata":         func(changed *api.Team) { changed.APIVersion, changed.Kind = "other/v1", "Other" },
		"compatibility removed": func(changed *api.Team) { changed.Status.Compatibility = nil },
		"compatibility add/remove/change": func(changed *api.Team) {
			changed.Status.Compatibility = &api.TeamCompatibilityStatus{
				State:     api.CompatibilityIncompatible,
				Issues:    []api.CompatibilityIssue{{Code: "sandbox_missing", Field: "spec.sandboxRefs", Message: "Choose at least one Sandbox."}},
				CheckedAt: metav1.NewTime(now.Add(time.Minute)), ValidUntil: metav1.NewTime(now.Add(90 * time.Second)),
			}
		},
	}
	for name, mutate := range ignored {
		t.Run(name, func(t *testing.T) {
			before := team.DeepCopy()
			changed := team.DeepCopy()
			mutate(changed)
			if filter.Update(event.UpdateEvent{ObjectOld: team, ObjectNew: changed}) {
				t.Fatal("runtime-inert Team update requeued Agent")
			}
			if teamRuntimeInputFingerprint(team) != teamRuntimeInputFingerprint(changed) {
				t.Fatal("runtime-inert Team update invalidated Agent cache")
			}
			if !reflect.DeepEqual(team, before) {
				t.Fatal("runtime input normalization mutated the original Team")
			}
		})
	}

	meaningful := map[string]func(*api.Team){
		"member context": func(changed *api.Team) { changed.Spec.MemberContext.Role = "Reviewer" },
		"skills":         func(changed *api.Team) { changed.Spec.Skills = append(changed.Spec.Skills, "npm:extra") },
		"uid":            func(changed *api.Team) { changed.UID = "replacement-uid" },
		"generation":     func(changed *api.Team) { changed.Generation++ },
		"labels":         func(changed *api.Team) { changed.Labels["role"] = "platform" },
		"annotations":    func(changed *api.Team) { changed.Annotations["owner"] = "other" },
		"finalizers":     func(changed *api.Team) { changed.Finalizers = nil },
		"deletion":       func(changed *api.Team) { changed.DeletionTimestamp = deletion },
		"accepted status": func(changed *api.Team) {
			changed.Status.Conditions[0].Status = metav1.ConditionFalse
		},
		"observed generation status": func(changed *api.Team) { changed.Status.ObservedGeneration++ },
		"model router status":        func(changed *api.Team) { changed.Status.ModelRouters[0].Ready = false },
	}
	for name, mutate := range meaningful {
		t.Run(name, func(t *testing.T) {
			changed := team.DeepCopy()
			mutate(changed)
			if !filter.Update(event.UpdateEvent{ObjectOld: team, ObjectNew: changed}) {
				t.Fatal("runtime-meaningful Team update did not requeue Agent")
			}
			if teamRuntimeInputFingerprint(team) == teamRuntimeInputFingerprint(changed) {
				t.Fatal("runtime-meaningful Team update did not invalidate Agent cache")
			}
		})
	}
}

func TestAgentReconcileIgnoresTeamCompatibilityRefresh(t *testing.T) {
	f := newNativeSandboxFixture(t, true)
	f.ready(t)
	stages := f.installer.stages

	team := &api.Team{}
	if err := f.kube.Get(t.Context(), types.NamespacedName{Name: f.current(t).Spec.Team}, team); err != nil {
		t.Fatal(err)
	}
	team.Status.Compatibility = &api.TeamCompatibilityStatus{
		State:     api.CompatibilityCompatible,
		Team:      api.CompatibilityReference{Name: team.Name, UID: string(team.UID), Generation: team.Generation},
		CheckedAt: metav1.Now(), ValidUntil: metav1.NewTime(time.Now().Add(30 * time.Second)),
	}
	if err := f.kube.Status().Update(t.Context(), team); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		f.reconcileOK(t)
	}
	if f.installer.stages != stages {
		t.Fatalf("compatibility refresh installed the native runtime %d additional times", f.installer.stages-stages)
	}
}

func TestAgentTeamRuntimeInputPredicatePreservesCreateDeleteAndGenericSemantics(t *testing.T) {
	filter := AgentTeamRuntimeInputPredicate()
	if !filter.Create(event.CreateEvent{}) || !filter.Delete(event.DeleteEvent{}) || !filter.Generic(event.GenericEvent{}) {
		t.Fatal("unexpected Agent Team watch event semantics")
	}
	if !filter.Update(event.UpdateEvent{}) {
		t.Fatal("missing update objects must remain observable")
	}
}
