// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxadmission

import (
	"fmt"
	"reflect"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// protectedPolicyAssignment prevents ordinary Kubernetes writers from
// altering Portal-derived ownership and Team membership evidence. The
// membership decision itself is checked again by the Operator from current
// Team data before compute is allowed.
func (h *Handler) protectedPolicyAssignment(request admission.Request, agent, old *api.Agent) error {
	tenant, tenantOK := agent.Annotations[api.AnnotationOwnerTenantID]
	object, objectOK := agent.Annotations[api.AnnotationOwnerObjectID]
	if !tenantOK || !objectOK || tenant == "" || object == "" {
		return fmt.Errorf("policy assignment requires immutable verified owner annotations")
	}
	authorization := agent.Spec.TeamAuthorization
	if authorization.AuthorizedAt.IsZero() || authorization.Subject.Kind == "" || authorization.Subject.TenantID == "" || authorization.Subject.ObjectID == "" {
		return fmt.Errorf("policy assignment requires complete Team authorization")
	}
	if old == nil {
		return nil
	}
	if old.Annotations[api.AnnotationOwnerTenantID] != tenant || old.Annotations[api.AnnotationOwnerObjectID] != object {
		return fmt.Errorf("verified owner annotations are immutable")
	}
	if !reflect.DeepEqual(old.Spec.TeamAuthorization, agent.Spec.TeamAuthorization) || monthlyBudgetUSD(old) != monthlyBudgetUSD(agent) {
		if !h.config.administrator(request.UserInfo) || !old.Spec.Suspended || !agent.Spec.Suspended {
			return fmt.Errorf("policy authorization and budget require an authorized administrator and a suspended Agent")
		}
	}
	return nil
}

func monthlyBudgetUSD(agent *api.Agent) string {
	if agent == nil || agent.Spec.ModelAccess == nil {
		return ""
	}
	return agent.Spec.ModelAccess.MonthlyBudgetUSD
}
