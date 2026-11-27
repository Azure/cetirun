// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"strconv"
	"time"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

// The states of an Agent's AI Gateway key that its budget summary reports.
const (
	// AIGatewayKeyActive means the AI Gateway accepts the key's calls.
	AIGatewayKeyActive = "active"
	// AIGatewayKeyBudgetReached means the spend of the period reached the
	// budget: the AI Gateway refuses the key's calls until the reset, but the
	// key is not blocked.
	AIGatewayKeyBudgetReached = "budgetReached"
	// AIGatewayKeySuspended means the operator blocked the key because the
	// Agent is suspended.
	AIGatewayKeySuspended = "suspended"
	// AIGatewayKeyNoModelAccess means the Agent Policy grants the Agent no
	// model now: the operator blocked its key until a new policy grant is in
	// place, or the Agent has no key.
	AIGatewayKeyNoModelAccess = "noModelAccess"
	// AIGatewayKeyBlockPending means the operator has recorded a block of the
	// key, for a suspension or for a policy without models, that the AI
	// Gateway has not confirmed yet. Until it does, the key can still be
	// accepted.
	AIGatewayKeyBlockPending = "blockPending"
	// AIGatewayKeyBlocked means the key is blocked outside CetiRun, for
	// example in the AI Gateway console.
	AIGatewayKeyBlocked = "blocked"
)

// The operator's AI Gateway reasons that explain a blocked key, as its
// controller sets them in the Agent status.
const (
	aiGatewayReasonSuspended          = "Suspended"
	aiGatewayReasonNoModelEntitlement = "NoModelEntitlement"
	aiGatewayReasonPolicyReprojection = "PolicyReprojection"
	aiGatewayReasonKeyBlocked         = "KeyBlocked"
)

// AIGatewayBudgetResponse is the current period of an Agent key's budget, as
// the operator last read it from the AI Gateway. It reports what the AI
// Gateway reported: the Portal computes no balance from it.
type AIGatewayBudgetResponse struct {
	// BudgetUSD and SpendUSD are decimal amounts in USD, as the AI Gateway
	// reports them. SpendUSD is the spend of the current period.
	BudgetUSD string `json:"budgetUSD,omitempty"`
	SpendUSD  string `json:"spendUSD,omitempty"`
	// Duration is the length of the period, such as 1mo.
	Duration string `json:"duration,omitempty"`
	// ResetAt is when the AI Gateway starts the next period, in RFC 3339.
	ResetAt string `json:"resetAt,omitempty"`
	// CheckedAt is when the operator last read the key from the AI Gateway,
	// in RFC 3339.
	CheckedAt string `json:"checkedAt,omitempty"`
	// State is why the AI Gateway refuses the key's calls, or active.
	State string `json:"state"`
}

// aiGatewayBudget summarises the budget of the Agent's key, or returns nil
// while the operator has read nothing of it.
func aiGatewayBudget(instance *cetirunv1alpha1.Agent) *AIGatewayBudgetResponse {
	status := instance.Status.AIGateway
	if status == nil || (status.BudgetUSD == "" && status.SpendUSD == "" && status.LastVerifiedTime == nil) {
		return nil
	}
	budget := &AIGatewayBudgetResponse{
		BudgetUSD: status.BudgetUSD,
		SpendUSD:  status.SpendUSD,
		Duration:  status.BudgetDuration,
		State:     aiGatewayKeyState(instance, status),
	}
	if status.BudgetResetAt != nil {
		budget.ResetAt = status.BudgetResetAt.UTC().Format(time.RFC3339)
	}
	if status.LastVerifiedTime != nil {
		budget.CheckedAt = status.LastVerifiedTime.UTC().Format(time.RFC3339)
	}
	return budget
}

// aiGatewayKeyState names why the AI Gateway refuses the key's calls. A
// blocked key has the operator's reason for the block; a key over its budget
// is refused without being blocked.
func aiGatewayKeyState(instance *cetirunv1alpha1.Agent, status *cetirunv1alpha1.AIGatewayStatus) string {
	operatorBlock := status.Reason == aiGatewayReasonSuspended || status.Reason == aiGatewayReasonNoModelEntitlement ||
		status.Reason == aiGatewayReasonPolicyReprojection
	switch {
	case status.Reason == aiGatewayReasonKeyBlocked:
		return AIGatewayKeyBlocked
	// Without a model grant and without an issued key, the Agent has no
	// model access and no key to block.
	case status.Reason == aiGatewayReasonNoModelEntitlement && status.KeyID == "":
		return AIGatewayKeyNoModelAccess
	// The operator records its block before it asks the AI Gateway to apply
	// it. Until the AI Gateway confirms the block, the key can still be
	// accepted.
	case operatorBlock && !status.Blocked:
		return AIGatewayKeyBlockPending
	case status.Blocked && (status.Reason == aiGatewayReasonSuspended || instance.Spec.Suspended):
		return AIGatewayKeySuspended
	case status.Blocked && (status.Reason == aiGatewayReasonNoModelEntitlement || status.Reason == aiGatewayReasonPolicyReprojection):
		return AIGatewayKeyNoModelAccess
	// The operator gives each of its own blocks a reason: any other block is
	// an administrator's.
	case status.Blocked:
		return AIGatewayKeyBlocked
	case budgetReached(status.SpendUSD, status.BudgetUSD):
		return AIGatewayKeyBudgetReached
	}
	return AIGatewayKeyActive
}

// budgetReached reports whether the spend reached the budget: the AI Gateway
// refuses a key's calls once its spend is at least its budget. A key without
// a budget has no limit, and a key without spend has spent nothing.
func budgetReached(spend, budget string) bool {
	limit, err := strconv.ParseFloat(budget, 64)
	if err != nil {
		return false
	}
	spent := 0.0
	if spend != "" {
		if spent, err = strconv.ParseFloat(spend, 64); err != nil {
			return false
		}
	}
	return spent >= limit
}
