// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
)

// policyAIGatewayKeyLimits returns the request limits the policy sets on the
// virtual key of every Agent that uses it. The limits are deliberately not
// part of the Gateway policy revision: changing them updates each key in
// place and does not start a policy reprojection.
func policyAIGatewayKeyLimits(policy *cetirunv1alpha1.AgentPolicy) (aigateway.KeyLimits, error) {
	if policy == nil || policy.Spec.ModelAccess.RateLimits == nil {
		return aigateway.KeyLimits{}, nil
	}
	source := policy.Spec.ModelAccess.RateLimits
	if errs := sandbox.ValidatePolicyRateLimits(source, field.NewPath("spec", "modelAccess", "rateLimits")); len(errs) != 0 {
		return aigateway.KeyLimits{}, fmt.Errorf("policy rate limits are invalid: %w", errs.ToAggregate())
	}
	limits := aigateway.KeyLimits{
		MaxParallelRequests: cloneLimit(source.MaxParallelRequests), RPM: cloneLimit(source.RPM), TPM: cloneLimit(source.TPM),
	}
	for model, value := range source.PerModel {
		if value.RPM != nil {
			if limits.ModelRPM == nil {
				limits.ModelRPM = map[string]int64{}
			}
			limits.ModelRPM[model] = *value.RPM
		}
		if value.TPM != nil {
			if limits.ModelTPM == nil {
				limits.ModelTPM = map[string]int64{}
			}
			limits.ModelTPM[model] = *value.TPM
		}
	}
	return limits, nil
}

func cloneLimit(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

// withTeamRouterModelLimits keys a per-model limit the policy sets for a
// router of the Agent's Team, by the router's name, under the router's
// internal name. The Gateway counts a router's requests under its internal
// name, also those the Agent sends by the router's alias, since LiteLLM
// counts a request after it resolves the alias. A limit for both names keeps
// the smaller value. A name that is also a shared model stays the shared
// model's: such a router is misconfigured and never granted.
func (r *AgentReconciler) withTeamRouterModelLimits(limits aigateway.KeyLimits, team *cetirunv1alpha1.Team) aigateway.KeyLimits {
	if team == nil || len(team.Spec.ModelRouters) == 0 || (len(limits.ModelRPM) == 0 && len(limits.ModelTPM) == 0) {
		return limits
	}
	shared := r.aiGatewayCatalog.sharedRouters().shared
	internal := map[string]string{}
	for _, router := range team.Spec.ModelRouters {
		if !shared[router.Name] {
			internal[router.Name] = modelRouterGatewayName(team.Name, router.Name)
		}
	}
	limits.ModelRPM = renamedModelLimits(limits.ModelRPM, internal)
	limits.ModelTPM = renamedModelLimits(limits.ModelTPM, internal)
	return limits
}

// renamedModelLimits renames the models of values that names maps, keeping
// the smaller value where two end up under one name.
func renamedModelLimits(values map[string]int64, names map[string]string) map[string]int64 {
	if len(values) == 0 {
		return values
	}
	renamed := make(map[string]int64, len(values))
	for model, value := range values {
		if name, ok := names[model]; ok {
			model = name
		}
		if current, ok := renamed[model]; ok && current <= value {
			continue
		}
		renamed[model] = value
	}
	return renamed
}

// reconcileAIGatewayKeyLimits brings an existing policy-bound key's request
// limits to the policy's values without replacing or blocking the key, so its
// spend and other settings are kept. Legacy keys are never changed: an
// administrator may manage their limits in the Gateway.
func (r *AgentReconciler) reconcileAIGatewayKeyLimits(ctx context.Context, client aiGatewayKeyClient, instance *cetirunv1alpha1.Agent,
	token string, key *aigateway.Key, grant aiGatewayGrant,
) error {
	if aiGatewayKeyLimitsMatch(key, grant) {
		return nil
	}
	if err := client.UpdateKeyLimits(ctx, token, grant.limits); err != nil {
		return err
	}
	r.EventRecorder.Eventf(instance, corev1.EventTypeNormal, "AIGatewayKeyLimitsUpdated",
		"Updated AI Gateway virtual key %s request limits from its AgentPolicy", aiGatewayKeyAlias(instance))
	return nil
}

// aiGatewayKeyLimitsMatch is the read-back check for a key's request limits.
// Only policy-bound keys carry policy limits.
func aiGatewayKeyLimitsMatch(key *aigateway.Key, grant aiGatewayGrant) bool {
	return !grant.policyBound || key.Limits.Equal(grant.limits)
}
