// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
)

// budgetAgent is a routed Agent whose key the operator has read: 12.4 of 50
// USD spent, a reset on the first day of the next month.
func budgetAgent() *cetirunv1alpha1.Agent {
	agent := routedAgent()
	reset := metav1.NewTime(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))
	checked := metav1.NewTime(time.Date(2026, 11, 25, 10, 20, 0, 0, time.FixedZone("UTC+8", 8*3600)))
	agent.Status.AIGateway.BudgetUSD = "50"
	agent.Status.AIGateway.SpendUSD = "12.4"
	agent.Status.AIGateway.BudgetDuration = "1mo"
	agent.Status.AIGateway.BudgetResetAt = &reset
	agent.Status.AIGateway.LastVerifiedTime = &checked
	return agent
}

// keyRecordLost sets the condition with which the operator reports that the AI
// Gateway lost the record of the Agent's key.
func keyRecordLost(agent *cetirunv1alpha1.Agent) {
	meta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
		Type: cetirunv1alpha1.ConditionAIGatewayReady, Status: metav1.ConditionFalse, Reason: aiGatewayReasonKeyRecordLost,
		Message: "the previously issued virtual key record is missing from the AI Gateway"})
}

func TestAIGatewayBudgetReportsTheGatewaysNumbers(t *testing.T) {
	budget := aiGatewayBudget(budgetAgent())
	want := AIGatewayBudgetResponse{
		BudgetUSD: "50", SpendUSD: "12.4", Duration: "1mo",
		ResetAt: "2026-12-01T00:00:00Z", CheckedAt: "2026-11-25T02:20:00Z", State: AIGatewayKeyActive,
	}
	if budget == nil || *budget != want {
		t.Fatalf("budget = %+v, want %+v", budget, want)
	}

	// Before the operator has read the key, there is nothing to report.
	unread := routedAgent()
	if budget := aiGatewayBudget(unread); budget != nil {
		t.Fatalf("unread key: budget = %+v", budget)
	}
	unread.Status.AIGateway = nil
	if budget := aiGatewayBudget(unread); budget != nil {
		t.Fatalf("no Gateway status: budget = %+v", budget)
	}
}

func TestAIGatewayKeyStateNamesWhyTheGatewayRefusesTheKey(t *testing.T) {
	for _, tc := range []struct {
		name      string
		change    func(*cetirunv1alpha1.Agent)
		wantState string
	}{
		{"under the budget", func(*cetirunv1alpha1.Agent) {}, AIGatewayKeyActive},
		{"spend at the budget", func(a *cetirunv1alpha1.Agent) { a.Status.AIGateway.SpendUSD = "50" }, AIGatewayKeyBudgetReached},
		{"spend over the budget", func(a *cetirunv1alpha1.Agent) { a.Status.AIGateway.SpendUSD = "50.01" }, AIGatewayKeyBudgetReached},
		{"zero budget", func(a *cetirunv1alpha1.Agent) {
			a.Status.AIGateway.BudgetUSD, a.Status.AIGateway.SpendUSD = "0", ""
		}, AIGatewayKeyBudgetReached},
		{"no budget", func(a *cetirunv1alpha1.Agent) {
			a.Status.AIGateway.BudgetUSD, a.Status.AIGateway.SpendUSD = "", "75"
		}, AIGatewayKeyActive},
		{"unreadable spend", func(a *cetirunv1alpha1.Agent) { a.Status.AIGateway.SpendUSD = "n/a" }, AIGatewayKeyActive},
		{"suspended", func(a *cetirunv1alpha1.Agent) {
			a.Spec.Suspended = true
			a.Status.AIGateway.Blocked, a.Status.AIGateway.Reason = true, aiGatewayReasonSuspended
		}, AIGatewayKeySuspended},
		{"suspended over the budget", func(a *cetirunv1alpha1.Agent) {
			a.Spec.Suspended = true
			a.Status.AIGateway.Blocked, a.Status.AIGateway.SpendUSD = true, "60"
		}, AIGatewayKeySuspended},
		{"no model entitlement", func(a *cetirunv1alpha1.Agent) {
			a.Status.AIGateway.Blocked, a.Status.AIGateway.Reason = true, aiGatewayReasonNoModelEntitlement
		}, AIGatewayKeyNoModelAccess},
		{"no model entitlement without an issued key", func(a *cetirunv1alpha1.Agent) {
			a.Status.AIGateway.Reason, a.Status.AIGateway.KeyID = aiGatewayReasonNoModelEntitlement, ""
		}, AIGatewayKeyNoModelAccess},
		// The operator records its block before the AI Gateway applies it:
		// until the AI Gateway confirms it, the key can still be accepted.
		{"no model entitlement before the block", func(a *cetirunv1alpha1.Agent) {
			a.Status.AIGateway.Reason = aiGatewayReasonNoModelEntitlement
		}, AIGatewayKeyBlockPending},
		{"no model entitlement over the budget before the block", func(a *cetirunv1alpha1.Agent) {
			a.Status.AIGateway.Reason, a.Status.AIGateway.SpendUSD = aiGatewayReasonNoModelEntitlement, "60"
		}, AIGatewayKeyBlockPending},
		{"suspension before the block", func(a *cetirunv1alpha1.Agent) {
			a.Spec.Suspended = true
			a.Status.AIGateway.Reason = aiGatewayReasonSuspended
		}, AIGatewayKeyBlockPending},
		{"policy reprojection", func(a *cetirunv1alpha1.Agent) {
			a.Status.AIGateway.Blocked, a.Status.AIGateway.Reason = true, aiGatewayReasonPolicyReprojection
		}, AIGatewayKeyNoModelAccess},
		{"policy reprojection before the block", func(a *cetirunv1alpha1.Agent) {
			a.Status.AIGateway.Reason = aiGatewayReasonPolicyReprojection
		}, AIGatewayKeyBlockPending},
		{"blocked in the console", func(a *cetirunv1alpha1.Agent) {
			a.Status.AIGateway.Blocked, a.Status.AIGateway.Reason = true, aiGatewayReasonKeyBlocked
		}, AIGatewayKeyBlocked},
		// The operator reports such a key before it records the block.
		{"console block not recorded yet", func(a *cetirunv1alpha1.Agent) {
			a.Status.AIGateway.Reason = aiGatewayReasonKeyBlocked
		}, AIGatewayKeyBlocked},
		{"blocked for another reason", func(a *cetirunv1alpha1.Agent) { a.Status.AIGateway.Blocked = true }, AIGatewayKeyBlocked},
		// Without its record, the AI Gateway refuses the key whatever its
		// spend, and no budget reset restores it.
		{"key record lost", func(a *cetirunv1alpha1.Agent) {
			a.Status.AIGateway.Reason = aiGatewayReasonKeyRecordLost
			keyRecordLost(a)
		}, AIGatewayKeyRecoveryRequired},
		{"key record lost over the budget", func(a *cetirunv1alpha1.Agent) {
			a.Status.AIGateway.Reason, a.Status.AIGateway.SpendUSD = aiGatewayReasonKeyRecordLost, "60"
		}, AIGatewayKeyRecoveryRequired},
		// The operator keeps the reason of its own block in the status, and
		// the condition names the loss.
		{"key record lost while suspended", func(a *cetirunv1alpha1.Agent) {
			a.Spec.Suspended = true
			a.Status.AIGateway.Blocked, a.Status.AIGateway.Reason = true, aiGatewayReasonSuspended
			keyRecordLost(a)
		}, AIGatewayKeyRecoveryRequired},
		{"key record lost before a block", func(a *cetirunv1alpha1.Agent) {
			a.Status.AIGateway.Reason = aiGatewayReasonNoModelEntitlement
			keyRecordLost(a)
		}, AIGatewayKeyRecoveryRequired},
		{"another AI Gateway failure", func(a *cetirunv1alpha1.Agent) {
			meta.SetStatusCondition(&a.Status.Conditions, metav1.Condition{
				Type: cetirunv1alpha1.ConditionAIGatewayReady, Status: metav1.ConditionFalse, Reason: "GatewayUnavailable"})
		}, AIGatewayKeyActive},
		{"key record restored", func(a *cetirunv1alpha1.Agent) {
			keyRecordLost(a)
			meta.SetStatusCondition(&a.Status.Conditions, metav1.Condition{
				Type: cetirunv1alpha1.ConditionAIGatewayReady, Status: metav1.ConditionTrue, Reason: "KeyReady"})
		}, AIGatewayKeyActive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent := budgetAgent()
			tc.change(agent)
			budget := aiGatewayBudget(agent)
			if budget == nil || budget.State != tc.wantState {
				t.Fatalf("budget = %+v, want state %q", budget, tc.wantState)
			}
		})
	}
}

func TestInstanceAndAdminResponsesCarryTheBudget(t *testing.T) {
	agent := budgetAgent()
	fc := newFakeClient(agent, gatewayTokenSecret("cetirun-alice-gw"))
	h := &InstanceHandler{Kube: kube.NewClient(fc), Config: gatewayConfig()}
	resp, err := h.toInstanceResponse(context.Background(), agent)
	if err != nil {
		t.Fatal(err)
	}
	if resp.AIGateway == nil || resp.AIGateway.Budget == nil || resp.AIGateway.Budget.SpendUSD != "12.4" {
		t.Fatalf("instance response aiGateway = %+v", resp.AIGateway)
	}

	admin := &AdminInstanceHandler{Kube: kube.NewClient(newFakeClient(budgetAgent()))}
	w := httptest.NewRecorder()
	admin.ListAllInstances(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/instances", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, body=%s", w.Code, w.Body.String())
	}
	var list struct {
		Instances []map[string]json.RawMessage `json:"instances"`
	}
	if err := json.NewDecoder(w.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Instances) != 1 {
		t.Fatalf("instances = %d", len(list.Instances))
	}
	var budget AIGatewayBudgetResponse
	if err := json.Unmarshal(list.Instances[0]["aiGatewayBudget"], &budget); err != nil || budget.BudgetUSD != "50" || budget.State != AIGatewayKeyActive {
		t.Fatalf("admin aiGatewayBudget = %s (%v)", list.Instances[0]["aiGatewayBudget"], err)
	}
	// The administrator list never carries the key itself.
	for _, field := range []string{"keyID", "keySecretName", "token"} {
		if _, present := list.Instances[0][field]; present {
			t.Fatalf("admin summary has %q", field)
		}
	}
}
