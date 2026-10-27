// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package kube

import (
	"context"
	"errors"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestRequestTeamChangeIsIdempotentAndFencesConcurrentRequests(t *testing.T) {
	ctx := context.Background()
	agent := sampleInstance()
	agent.UID = types.UID("agent-uid")
	store := newFakeClient(agent)
	portal := NewClient(store)
	instance, err := portal.GetInstanceByName(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	request := api.TeamChangeRequest{
		RequestID: "team-change-0001", ExpectedUID: instance.UID, ExpectedResourceVersion: instance.ResourceVersion,
		TargetTeam: "restricted", TargetSandbox: "restricted-sandbox", Model: "gpt-6-mini",
	}
	accepted, err := portal.RequestTeamChangeByName(ctx, instance.Name, request, nil)
	if err != nil || accepted.Spec.TeamChangeRequest == nil || accepted.Spec.TeamChangeRequest.RequestID != request.RequestID {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
	// Retrying a lost response must not make another write or require the old
	// resourceVersion to survive a status update.
	retried, err := portal.RequestTeamChangeByName(ctx, instance.Name, request, nil)
	if err != nil || retried.Spec.TeamChangeRequest == nil || retried.Spec.TeamChangeRequest.RequestID != request.RequestID {
		t.Fatalf("retry=%+v err=%v", retried, err)
	}
	other := request
	other.RequestID = "team-change-0002"
	if _, err := portal.RequestTeamChangeByName(ctx, instance.Name, other, nil); !errors.Is(err, ErrTeamChangePending) {
		t.Fatalf("concurrent request error=%v, want %v", err, ErrTeamChangePending)
	}
	current := &api.Agent{}
	if err := store.Get(ctx, client.ObjectKeyFromObject(instance), current); err != nil {
		t.Fatal(err)
	}
	if current.Spec.Team != "example-team" || current.Spec.SandboxRef != "standard" {
		t.Fatalf("Portal changed effective binding: team=%q sandbox=%q", current.Spec.Team, current.Spec.SandboxRef)
	}
}

func TestRequestTeamChangePreservesRequestIdentityAndFence(t *testing.T) {
	ctx := context.Background()
	request := api.TeamChangeRequest{
		RequestID: "team-change-0001", ExpectedUID: "agent-uid", ExpectedResourceVersion: "1",
		TargetTeam: "restricted", TargetSandbox: "restricted-sandbox",
	}

	t.Run("same request ID cannot change target after completion", func(t *testing.T) {
		agent := sampleInstance()
		agent.UID = types.UID("agent-uid")
		agent.Spec.TeamChangeRequest = &request
		agent.Status.TeamChange = &api.TeamChangeStatus{TeamChangeRequest: request, Phase: api.TeamChangeCompleted}
		portal := NewClient(newFakeClient(agent))
		current, err := portal.GetInstanceByName(ctx, agent.Name)
		if err != nil {
			t.Fatal(err)
		}
		changed := request
		changed.TargetTeam = "other"
		changed.ExpectedResourceVersion = current.ResourceVersion
		if _, err := portal.RequestTeamChangeByName(ctx, agent.Name, changed, nil); !errors.Is(err, ErrTeamChangePending) {
			t.Fatalf("changed request ID payload error=%v, want %v", err, ErrTeamChangePending)
		}
	})

	t.Run("a new request follows an unfenced completed operation", func(t *testing.T) {
		agent := sampleInstance()
		agent.UID = types.UID("agent-uid")
		agent.Spec.TeamChangeRequest = &request
		agent.Status.TeamChange = &api.TeamChangeStatus{TeamChangeRequest: request, Phase: api.TeamChangeCompleted}
		portal := NewClient(newFakeClient(agent))
		current, err := portal.GetInstanceByName(ctx, agent.Name)
		if err != nil {
			t.Fatal(err)
		}
		next := request
		next.RequestID = "team-change-0002"
		next.ExpectedResourceVersion = current.ResourceVersion
		accepted, err := portal.RequestTeamChangeByName(ctx, agent.Name, next, nil)
		if err != nil || accepted.Spec.TeamChangeRequest == nil || accepted.Spec.TeamChangeRequest.RequestID != next.RequestID {
			t.Fatalf("accepted=%+v err=%v", accepted, err)
		}
	})

	t.Run("a status record returns an exact retry after spec is cleared", func(t *testing.T) {
		agent := sampleInstance()
		agent.UID = types.UID("agent-uid")
		original := request
		original.TargetSandbox = ""
		original.ModelAccess = &api.ModelAccessSpec{SharedModels: ptr.To(true), GitHubCopilot: ptr.To(false)}
		resolved := *original.DeepCopy()
		resolved.TargetSandbox = "restricted-sandbox"
		agent.Status.TeamChange = &api.TeamChangeStatus{
			TeamChangeRequest: resolved,
			OriginalRequest:   original.DeepCopy(),
			Phase:             api.TeamChangeCompleted,
		}
		portal := NewClient(newFakeClient(agent))
		retried, err := portal.RequestTeamChangeByName(ctx, agent.Name, original, nil)
		if err != nil || retried.Spec.TeamChangeRequest != nil || retried.Status.TeamChange == nil {
			t.Fatalf("retry=%+v err=%v", retried, err)
		}
		changed := *original.DeepCopy()
		changed.ModelAccess.GitHubCopilot = ptr.To(true)
		if _, err := portal.RequestTeamChangeByName(ctx, agent.Name, changed, nil); !errors.Is(err, ErrTeamChangePending) {
			t.Fatalf("changed status payload error=%v, want %v", err, ErrTeamChangePending)
		}
	})

	t.Run("a fenced failure blocks a new request", func(t *testing.T) {
		agent := sampleInstance()
		agent.UID = types.UID("agent-uid")
		agent.Spec.TeamChangeRequest = &request
		agent.Status.TeamChange = &api.TeamChangeStatus{TeamChangeRequest: request, Phase: api.TeamChangeFailed, Fenced: true}
		portal := NewClient(newFakeClient(agent))
		current, err := portal.GetInstanceByName(ctx, agent.Name)
		if err != nil {
			t.Fatal(err)
		}
		next := request
		next.RequestID = "team-change-0002"
		next.ExpectedResourceVersion = current.ResourceVersion
		if _, err := portal.RequestTeamChangeByName(ctx, agent.Name, next, nil); !errors.Is(err, ErrTeamChangePending) {
			t.Fatalf("fenced request error=%v, want %v", err, ErrTeamChangePending)
		}
	})
}

func TestTeamChangePendingBlocksConfigurationAndRestart(t *testing.T) {
	ctx := context.Background()
	agent := sampleInstance()
	agent.UID = types.UID("agent-uid")
	agent.Spec.TeamChangeRequest = &api.TeamChangeRequest{
		RequestID: "team-change-0001", ExpectedUID: agent.UID, ExpectedResourceVersion: "1", TargetTeam: "restricted",
	}
	portal := NewClient(newFakeClient(agent))
	current, err := portal.GetInstanceByName(ctx, agent.Name)
	if err != nil {
		t.Fatal(err)
	}
	name := "Updated"
	if _, err := portal.UpdateInstanceByName(ctx, agent.Name, UpdateInstanceOpts{
		ExpectedUID: current.UID, ExpectedResourceVersion: current.ResourceVersion, DisplayName: &name,
	}); !errors.Is(err, ErrTeamChangePending) {
		t.Fatalf("configuration update error=%v, want %v", err, ErrTeamChangePending)
	}
	if _, err := portal.RestartInstanceByName(ctx, agent.Name, InstanceVersion{UID: current.UID, ResourceVersion: current.ResourceVersion}, "restart-request-0001", nil); !errors.Is(err, ErrTeamChangePending) {
		t.Fatalf("restart error=%v, want %v", err, ErrTeamChangePending)
	}
}

func TestTeamChangePendingBlocksResumeButNotSuspendAvailability(t *testing.T) {
	ctx := context.Background()
	agent := sampleInstance()
	agent.UID = types.UID("agent-uid")
	agent.Spec.Suspended = true
	agent.Spec.TeamChangeRequest = &api.TeamChangeRequest{
		RequestID: "team-change-0001", ExpectedUID: agent.UID, ExpectedResourceVersion: "1", TargetTeam: "restricted",
	}
	portal := NewClient(newFakeClient(agent))
	current, err := portal.GetInstanceByName(ctx, agent.Name)
	if err != nil {
		t.Fatal(err)
	}
	if CanResumeInstance(current) {
		t.Fatal("resume remained available while Team change was pending")
	}
	if _, err := portal.SetInstanceSuspendedByName(ctx, current.Name,
		InstanceVersion{UID: current.UID, ResourceVersion: current.ResourceVersion}, false, nil); !errors.Is(err, ErrTeamChangePending) {
		t.Fatalf("resume error=%v, want %v", err, ErrTeamChangePending)
	}
}
