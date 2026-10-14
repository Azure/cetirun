// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/runtimebindings"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (r *AgentReconciler) bindCustomAgentIdentity(ctx context.Context, instance *api.Agent, bindings *runtimebindings.Custom) (bool, error) {
	if bindings.AgentIdentity != nil {
		return false, fmt.Errorf("only the identity controller may supply an Agent Identity binding")
	}
	if instance.Spec.AgentIdentity == nil && instance.Status.AgentIdentity == nil {
		return true, nil
	}
	status := instance.Status.AgentIdentity
	if status == nil || status.ObservedGeneration != instance.Generation || agentIdentityVerificationDue(instance, time.Now()) ||
		(!agentIdentityRequested(instance) && status.Phase != api.AgentIdentityPhaseDisabled) {
		previous := instance.DeepCopy().Status
		err := r.reconcileAgentIdentity(ctx, instance)
		if err != nil {
			setCondition(&instance.Status.Conditions, api.ConditionAgentIdentityReady, metav1.ConditionFalse,
				"ReconcileFailed", "Agent Identity is not ready")
		} else if agentIdentityRequested(instance) && instance.Status.AgentIdentity.Phase == api.AgentIdentityPhaseReady {
			now := metav1.Now()
			instance.Status.AgentIdentity.LastVerifiedTime = &now
		}
		if err := errors.Join(err, r.updateCustomStatus(ctx, instance, previous)); err != nil {
			return false, err
		}
	}
	if !agentIdentityRequested(instance) {
		return true, nil
	}
	if !r.AgentIdentityEnabled || instance.Status.AgentIdentity == nil || instance.Status.AgentIdentity.Phase != api.AgentIdentityPhaseReady {
		return false, nil
	}
	if _, err := runtimebindings.AgentIdentityBrokerNamespace(r.AgentIdentityBrokerURL); err != nil {
		return false, err
	}
	bindings.AgentIdentity = &runtimebindings.AgentIdentity{BrokerURL: r.AgentIdentityBrokerURL}
	return true, nil
}
