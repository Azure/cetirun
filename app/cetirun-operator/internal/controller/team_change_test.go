// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// A pre-fence failure has not changed the source binding or created a
// transition. It must not block later ordinary reconciliation, which still
// applies source-side policy and Gateway governance using the normalized
// source identity.
func TestTeamChangePreflightFailureAllowsSourceGovernance(t *testing.T) {
	ctx := context.Background()
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, gateway := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())

	current := &api.Agent{}
	if err := kube.Get(ctx, types.NamespacedName{Name: agent.Name}, current); err != nil {
		t.Fatal(err)
	}
	current.Spec.TeamChangeRequest = &api.TeamChangeRequest{
		RequestID:               "request-preflight-failure-0001",
		ExpectedUID:             current.UID,
		ExpectedResourceVersion: current.ResourceVersion,
		TargetTeam:              testAIGatewayTeam,
	}
	if err := kube.Update(ctx, current); err != nil {
		t.Fatal(err)
	}

	// The first pass persists the operation checkpoint. The second pass fails
	// because no common Sandbox execution has been configured. This is before
	// every fence, stop, Gateway mutation, or protected Team write.
	for range 2 {
		if err := kube.Get(ctx, types.NamespacedName{Name: agent.Name}, current); err != nil {
			t.Fatal(err)
		}
		allowed, _, err := r.reconcileTeamChange(ctx, current)
		if err != nil || allowed {
			t.Fatalf("preflight pass allowed=%t err=%v", allowed, err)
		}
	}
	if err := kube.Get(ctx, types.NamespacedName{Name: agent.Name}, current); err != nil {
		t.Fatal(err)
	}
	state := current.Status.TeamChange
	if state == nil || state.Phase != api.TeamChangeFailed || state.Fenced || state.Stopped {
		t.Fatalf("preflight failure must be an unfenced checkpoint, got %+v", state)
	}
	if current.Spec.Team != testAIGatewayTeam || current.Spec.SandboxRef != testSandboxName {
		t.Fatalf("preflight changed source binding: team=%q sandbox=%q", current.Spec.Team, current.Spec.SandboxRef)
	}

	allowed, _, err := r.reconcileTeamChange(ctx, current)
	if err != nil || !allowed {
		t.Fatalf("unfenced failed request must allow ordinary source governance: allowed=%t err=%v", allowed, err)
	}
	if len(gateway.generated) != 0 || len(gateway.ownerUpdates) != 0 || len(gateway.updates) != 0 {
		t.Fatalf("Team change admission itself touched the source Gateway key: generated=%d owner=%d models=%d", len(gateway.generated), len(gateway.ownerUpdates), len(gateway.updates))
	}
}

func TestValidTeamChangeRequestRejectsBudgetOverride(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	request := &api.TeamChangeRequest{RequestID: "team-change-budget-0001", ExpectedUID: agent.UID,
		ExpectedResourceVersion: "1", TargetTeam: testAIGatewayTeam,
		ModelAccess: &api.ModelAccessSpec{MonthlyBudgetUSD: "100"}}
	if err := validTeamChangeRequest(agent, request); err == nil {
		t.Fatal("controller accepted a Team change request with a budget override")
	}
}

func TestTeamChangeModelAccessPreservesBudgetOverride(t *testing.T) {
	shared, copilot := false, true
	agent := &api.Agent{Spec: api.AgentSpec{ModelAccess: &api.ModelAccessSpec{
		SharedModels: &shared, GitHubCopilot: &copilot, MonthlyBudgetUSD: "7",
	}}}
	targetShared, targetCopilot := true, false
	state := &api.TeamChangeStatus{ModelAccess: &api.ModelAccessSpec{SharedModels: &targetShared, GitHubCopilot: &targetCopilot}}
	got := teamChangeModelAccess(agent, state)
	if got.MonthlyBudgetUSD != "7" || !*got.SharedModels || *got.GitHubCopilot {
		t.Fatalf("Team change did not retain the existing budget while replacing model sources: %+v", got)
	}
}

func TestTeamChangeFailureAfterFenceRetainsRetryCheckpoint(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	state := &api.TeamChangeStatus{
		TeamChangeRequest: api.TeamChangeRequest{RequestID: "request-fenced-failure-00001"},
		Phase:             api.TeamChangeStopping,
		Fenced:            true,
	}
	agent.Status.TeamChange = state
	r, kube, _ := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	current := &api.Agent{}
	if err := kube.Get(context.Background(), types.NamespacedName{Name: agent.Name}, current); err != nil {
		t.Fatal(err)
	}
	if err := r.teamChangeFailure(context.Background(), current, current.Status.TeamChange, "StopPending", "synthetic transient stop failure"); err != nil {
		t.Fatal(err)
	}
	if current.Status.TeamChange.Phase != api.TeamChangeStopping || !current.Status.TeamChange.Fenced ||
		current.Status.TeamChange.FailureReason != "StopPending" {
		t.Fatalf("fenced failure lost its resumable checkpoint: %+v", current.Status.TeamChange)
	}
}

func TestTeamChangeValidatingRunningSourceDefersToSuspension(t *testing.T) {
	ctx := context.Background()
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	r, kube, _ := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	current := &api.Agent{}
	if err := kube.Get(ctx, types.NamespacedName{Name: agent.Name}, current); err != nil {
		t.Fatal(err)
	}
	current.Spec.Suspended = true
	current.Status.AccessURL = "https://example.test/agent"
	current.Status.Conditions = []metav1.Condition{{Type: api.ConditionReady, Status: metav1.ConditionTrue}}
	request := api.TeamChangeRequest{RequestID: "request-suspend-validating-001", ExpectedUID: current.UID,
		ExpectedResourceVersion: current.ResourceVersion, TargetTeam: testAIGatewayTeam}
	current.Spec.TeamChangeRequest = &request
	if err := kube.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, types.NamespacedName{Name: agent.Name}, current); err != nil {
		t.Fatal(err)
	}
	current.Status.AccessURL = "https://example.test/agent"
	current.Status.Conditions = []metav1.Condition{{Type: api.ConditionReady, Status: metav1.ConditionTrue}}
	current.Status.TeamChange = &api.TeamChangeStatus{TeamChangeRequest: request, Phase: api.TeamChangeValidating,
		SourceTeam: current.Spec.Team, SourceSandbox: current.Spec.SandboxRef, SourceGeneration: current.Status.ObservedGeneration}
	if err := kube.Status().Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, types.NamespacedName{Name: agent.Name}, current); err != nil {
		t.Fatal(err)
	}
	allowed, _, err := r.reconcileTeamChange(ctx, current)
	if err != nil || !allowed {
		t.Fatalf("newly suspended running source must defer to ordinary suspension: allowed=%t err=%v", allowed, err)
	}
}

func TestTeamChangeRejectsCompletedRequestIDPayloadReplacement(t *testing.T) {
	ctx := context.Background()
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	original := api.TeamChangeRequest{RequestID: "request-completed-reuse-0001", ExpectedUID: agent.UID,
		TargetTeam: testAIGatewayTeam}
	r, kube, _ := aiGatewayTestReconciler(t, agent, aiGatewayNamespace())
	current := &api.Agent{}
	if err := kube.Get(ctx, types.NamespacedName{Name: agent.Name}, current); err != nil {
		t.Fatal(err)
	}
	original.ExpectedResourceVersion = current.ResourceVersion
	current.Spec.TeamChangeRequest = original.DeepCopy()
	if err := kube.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, types.NamespacedName{Name: agent.Name}, current); err != nil {
		t.Fatal(err)
	}
	current.Status.TeamChange = &api.TeamChangeStatus{TeamChangeRequest: original, OriginalRequest: original.DeepCopy(),
		Phase: api.TeamChangeCompleted}
	if err := kube.Status().Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, types.NamespacedName{Name: agent.Name}, current); err != nil {
		t.Fatal(err)
	}
	if current.Status.TeamChange == nil || current.Status.TeamChange.OriginalRequest == nil {
		t.Fatalf("fixture did not persist original request: %+v", current.Status.TeamChange)
	}
	current.Spec.TeamChangeRequest.TargetSandbox = "forged-selected-sandbox"
	if current.Status.TeamChange.OriginalRequest.TargetSandbox != "" {
		t.Fatalf("test object aliases spec request and original request")
	}
	if err := kube.Update(ctx, current); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, types.NamespacedName{Name: agent.Name}, current); err != nil {
		t.Fatal(err)
	}
	if current.Status.TeamChange.OriginalRequest.TargetSandbox != "" {
		t.Fatalf("client aliases spec request and original request")
	}
	if current.Spec.TeamChangeRequest.TargetSandbox != "forged-selected-sandbox" {
		t.Fatalf("fixture did not persist replacement request: %+v", current.Spec.TeamChangeRequest)
	}
	if _, _, err := r.reconcileTeamChange(ctx, current); err == nil {
		t.Fatal("controller accepted a completed Team change ID with a replacement payload")
	}
}
