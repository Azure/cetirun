// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package agentpolicy contains component-neutral AgentPolicy validation.
package agentpolicy

import (
	"math/big"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation/field"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

// Default returns a deep copy of an AgentPolicy specification. AgentPolicy
// does not default Sandbox, identity, security, or network controls.
func Default(spec api.AgentPolicySpec) api.AgentPolicySpec {
	return *spec.DeepCopy()
}

// Normalize validates the component-neutral AgentPolicy contract.
func Normalize(spec api.AgentPolicySpec) (api.AgentPolicySpec, field.ErrorList) {
	policy := Default(spec)
	path := field.NewPath("spec")
	var errs field.ErrorList
	if len(policy.Harnesses.Allowed) == 0 {
		errs = append(errs, field.Required(path.Child("harnesses", "allowed"), "at least one harness is required"))
	}
	slices.Sort(policy.Harnesses.Allowed)
	policy.Harnesses.Allowed = slices.Compact(policy.Harnesses.Allowed)
	for i, harness := range policy.Harnesses.Allowed {
		if harness != api.AgentTypeOpenClaw && harness != api.AgentTypeHermes {
			errs = append(errs, field.NotSupported(path.Child("harnesses", "allowed").Index(i), harness, []string{string(api.AgentTypeOpenClaw), string(api.AgentTypeHermes)}))
		}
	}
	policy.Skills.Allowed = canonicalNonEmptyStrings(policy.Skills.Allowed, path.Child("skills", "allowed"), &errs)
	policy.ModelAccess.SharedModels.Allowed = canonicalNonEmptyStrings(policy.ModelAccess.SharedModels.Allowed, path.Child("modelAccess", "sharedModels", "allowed"), &errs)
	hasModels := policy.ModelAccess.Copilot.Allowed || len(policy.ModelAccess.SharedModels.Allowed) != 0
	budgetPath := path.Child("modelAccess", "gatewayBudget")
	if hasModels && policy.ModelAccess.GatewayBudget == nil {
		errs = append(errs, field.Required(budgetPath, "a Gateway budget is required for every model entitlement"))
	}
	if !hasModels && policy.ModelAccess.GatewayBudget != nil {
		errs = append(errs, field.Forbidden(budgetPath, "a Gateway budget requires a model entitlement"))
	}
	if budget := policy.ModelAccess.GatewayBudget; budget != nil {
		defaultBudget, defaultOK := new(big.Rat).SetString(budget.DefaultMonthlyUSD)
		maximumBudget, maximumOK := new(big.Rat).SetString(budget.MaxMonthlyUSD)
		if !defaultOK || defaultBudget.Sign() <= 0 {
			errs = append(errs, field.Invalid(budgetPath.Child("defaultMonthlyUSD"), budget.DefaultMonthlyUSD, "must be a positive decimal"))
		}
		if !maximumOK || maximumBudget.Sign() <= 0 {
			errs = append(errs, field.Invalid(budgetPath.Child("maxMonthlyUSD"), budget.MaxMonthlyUSD, "must be a positive decimal"))
		}
		if defaultOK && maximumOK && defaultBudget.Sign() > 0 && maximumBudget.Sign() > 0 && defaultBudget.Cmp(maximumBudget) > 0 {
			errs = append(errs, field.Invalid(budgetPath.Child("defaultMonthlyUSD"), budget.DefaultMonthlyUSD, "must not exceed maxMonthlyUSD"))
		}
	}
	return policy, errs
}

func canonicalNonEmptyStrings(values []string, path *field.Path, errs *field.ErrorList) []string {
	canonical := append([]string(nil), values...)
	for i, value := range canonical {
		if strings.TrimSpace(value) == "" {
			*errs = append(*errs, field.Invalid(path.Index(i), value, "must not be blank"))
		}
	}
	slices.Sort(canonical)
	return slices.Compact(canonical)
}
