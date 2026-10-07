// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxadmission

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const GovernanceActionValidationPath = "/validate-governance-action"

// ValidateGovernanceAction makes proposals immutable and permits exactly one
// nil-to-decision approval transition. Only the dedicated Portal workload may
// persist a proposal or a human Platform Admin decision already authorized by
// the Portal endpoint.
func (h *Handler) ValidateGovernanceAction(_ context.Context, request admission.Request) admission.Response {
	if request.Resource.Group != api.GroupVersion.Group || request.Resource.Version != api.GroupVersion.Version || request.Resource.Resource != "governanceactions" || request.Kind.Kind != "GovernanceAction" || request.Namespace != "" || request.SubResource != "" {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("unsupported GovernanceAction admission resource"))
	}
	if request.Operation == admissionv1.Delete {
		return admission.Denied("GovernanceAction audit records cannot be deleted")
	}
	if request.Operation != admissionv1.Create && request.Operation != admissionv1.Update {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("unsupported GovernanceAction admission operation"))
	}
	if request.UserInfo.Username != h.config.GovernanceApprovalUsername {
		return admission.Denied("GovernanceAction writes require the dedicated Portal approval identity")
	}
	action := &api.GovernanceAction{}
	if err := decodeJSON(request.Object.Raw, action, h.config.ValidationMode == ValidationStrict); err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("invalid GovernanceAction: %w", err))
	}
	if action.Name != request.Name || action.Namespace != "" {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("admitted object identity does not match the request"))
	}
	if err := validateActionShape(action); err != nil {
		return admission.Denied(err.Error())
	}
	if request.Operation == admissionv1.Create {
		if action.Spec.Approval != nil {
			return admission.Denied("GovernanceAction approval must be written only after proposal creation")
		}
		return admission.Allowed("GovernanceAction proposal is structurally valid")
	}
	old := &api.GovernanceAction{}
	if err := decodeJSON(request.OldObject.Raw, old, h.config.ValidationMode == ValidationStrict); err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("invalid previous GovernanceAction: %w", err))
	}
	if old.UID != action.UID || old.Name != action.Name {
		return admission.Denied("GovernanceAction identity is immutable")
	}
	if !sameActionMetadata(old, action) {
		return admission.Denied("GovernanceAction metadata is immutable after creation")
	}
	if !sameActionProposal(old.Spec, action.Spec) {
		return admission.Denied("GovernanceAction proposal fields are immutable after creation")
	}
	if old.Spec.Approval != nil {
		return admission.Denied("GovernanceAction approval is immutable once decided")
	}
	if action.Spec.Approval == nil {
		return admission.Denied("GovernanceAction updates may only set an approval decision")
	}
	if action.Spec.Approval.ApprovedAt.Before(&action.Spec.RequestedAt) || !action.Spec.Approval.ExpiresAt.After(action.Spec.Approval.ApprovedAt.Time) {
		return admission.Denied("GovernanceAction approval timestamps are invalid")
	}
	if action.Spec.Approval.ExpiresAt.After(action.Spec.ExpiresAt.Time) {
		return admission.Denied("GovernanceAction approval may not outlive its proposal")
	}
	digest, err := api.GovernanceActionDigest(action.Spec)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	if action.Spec.Approval.ActionDigest != digest {
		return admission.Denied("GovernanceAction approval digest does not match the immutable proposal")
	}
	return admission.Allowed("GovernanceAction approval is structurally valid")
}

func sameActionMetadata(old, updated *api.GovernanceAction) bool {
	return reflect.DeepEqual(old.Labels, updated.Labels) &&
		reflect.DeepEqual(old.Annotations, updated.Annotations) &&
		reflect.DeepEqual(old.Finalizers, updated.Finalizers) &&
		reflect.DeepEqual(old.OwnerReferences, updated.OwnerReferences) &&
		reflect.DeepEqual(old.DeletionTimestamp, updated.DeletionTimestamp)
}

func validateActionShape(action *api.GovernanceAction) error {
	if action.Spec.Requester.Kind != "CetiAdmin" || action.Spec.Requester.Subject == "" {
		return fmt.Errorf("GovernanceAction requester must be CetiAdmin with a subject")
	}
	if action.Spec.TargetRef.APIVersion != api.GroupVersion.String() || action.Spec.TargetRef.Kind != "Agent" || action.Spec.TargetRef.Name == "" || action.Spec.TargetRef.UID == "" || action.Spec.TargetRef.Generation < 1 {
		return fmt.Errorf("GovernanceAction must reference one persisted Agent generation")
	}
	if action.Spec.Operation != api.GovernanceActionSuspend && action.Spec.Operation != api.GovernanceActionResume && action.Spec.Operation != api.GovernanceActionRestart && action.Spec.Operation != api.GovernanceActionReconcile {
		return fmt.Errorf("GovernanceAction operation is not supported")
	}
	if action.Spec.Reason == "" || action.Spec.RequestedAt.IsZero() || !action.Spec.ExpiresAt.After(action.Spec.RequestedAt.Time) || action.Spec.ExpiresAt.Sub(action.Spec.RequestedAt.Time) > 24*time.Hour {
		return fmt.Errorf("GovernanceAction reason or expiry is invalid")
	}
	return nil
}

func sameActionProposal(old, updated api.GovernanceActionSpec) bool {
	old.Approval, updated.Approval = nil, nil
	return reflect.DeepEqual(old, updated)
}
