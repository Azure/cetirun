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
