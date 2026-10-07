// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxadmission

import (
	"fmt"
	"reflect"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// governanceActionControlledUpdate is the deliberately narrow exception for
// the operator's GovernanceAction executor. The executor may claim/release an
// action, request a pod-template rollout, or toggle suspension only while it
// owns the Agent claim. It cannot use this route to alter authorization,
// policy selection, Helm values, or any other Agent field.
func (h *Handler) governanceActionControlledUpdate(request admission.Request, agent, old *api.Agent) bool {
	if request.UserInfo.Username != h.config.ControllerUsername || old == nil {
		return false
	}
	if governanceActionMetadataOnly(request, []string{api.AnnotationGovernanceAction, api.AnnotationGovernanceActionClaim}) {
		return true
	}
	if old.Annotations[api.AnnotationGovernanceActionClaim] == "" ||
		agent.Annotations[api.AnnotationGovernanceActionClaim] != old.Annotations[api.AnnotationGovernanceActionClaim] {
		return false
	}
	return governanceActionSuspensionOnly(request)
}

// protectedGovernanceBinding prevents ordinary Kubernetes writers from
// altering Portal-derived ownership and Team membership evidence. The
// membership decision itself is checked again by the Operator from current
// Team data before compute is allowed.
func (h *Handler) protectedGovernanceBinding(request admission.Request, agent, old *api.Agent) error {
	tenant, tenantOK := agent.Annotations[api.AnnotationOwnerTenantID]
	object, objectOK := agent.Annotations[api.AnnotationOwnerObjectID]
	if !tenantOK || !objectOK || tenant == "" || object == "" {
		return fmt.Errorf("governance requires immutable verified owner annotations")
	}
	authorization := agent.Spec.Governance.TeamAuthorization
	if authorization.AuthorizedAt.IsZero() || authorization.Subject.Kind == "" || authorization.Subject.TenantID == "" || authorization.Subject.ObjectID == "" {
		return fmt.Errorf("governance requires complete Team authorization")
	}
	if old == nil {
		return nil
	}
	if old.Annotations[api.AnnotationOwnerTenantID] != tenant || old.Annotations[api.AnnotationOwnerObjectID] != object {
		return fmt.Errorf("verified owner annotations are immutable")
	}
	if !reflect.DeepEqual(old.Spec.Governance, agent.Spec.Governance) {
		if !h.config.administrator(request.UserInfo) || !old.Spec.Suspended || !agent.Spec.Suspended {
			return fmt.Errorf("governance authorization and budget require an authorized administrator and a suspended Agent")
		}
	}
	return nil
}
