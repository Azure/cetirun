// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
)

const (
	TeamChangeValidating = "Validating"
	TeamChangeStopping   = "Stopping"
	TeamChangeCommitting = "Committing"
	TeamChangeApplying   = "Applying"
	TeamChangeStarting   = "Starting"
	TeamChangeCompleted  = "Completed"
	TeamChangeFailed     = "Failed"
)

// TeamChangeRequest is accepted by the Portal after it has read the current
// Agent UID and resourceVersion. The preconditions prevent a request prepared
// for a deleted/recreated or concurrently edited Agent from being executed.
type TeamChangeRequest struct {
	// +kubebuilder:validation:MinLength=16
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_-]+$`
	RequestID string `json:"requestID"`
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:MinLength=1
	ExpectedUID types.UID `json:"expectedUID"`
	// +kubebuilder:validation:MinLength=1
	ExpectedResourceVersion string `json:"expectedResourceVersion"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	TargetTeam string `json:"targetTeam"`
	// +kubebuilder:validation:MaxLength=253
	// +optional
	TargetSandbox string `json:"targetSandbox,omitempty"`
	// +kubebuilder:validation:MaxLength=256
	// +optional
	Model string `json:"model,omitempty"`
	// Team changes select model sources only. MonthlyBudgetUSD must be omitted;
	// any existing per-Agent budget override remains unchanged.
	// +kubebuilder:validation:XValidation:rule="!has(self.monthlyBudgetUSD)",message="a Team change request cannot set monthlyBudgetUSD"
	// +optional
	ModelAccess *ModelAccessSpec `json:"modelAccess,omitempty"`
}

// TeamChangeStatus persists the accepted request, immutable target identities,
// and the stop acknowledgement needed to recover an operation after the
// Portal or controller restarts.
type TeamChangeStatus struct {
	TeamChangeRequest `json:",inline"`
	// OriginalRequest is the caller-authored operation payload. TargetSandbox
	// above is retained for the controller's resolved selection, so it cannot
	// establish whether a later request with the same ID is an exact retry.
	// +optional
	OriginalRequest *TeamChangeRequest `json:"originalRequest,omitempty"`
	// +kubebuilder:validation:Enum=Validating;Stopping;Committing;Applying;Starting;Completed;Failed
	Phase            string    `json:"phase"`
	SourceTeam       string    `json:"sourceTeam,omitempty"`
	SourceSandbox    string    `json:"sourceSandbox,omitempty"`
	SourceGeneration int64     `json:"sourceGeneration,omitempty"`
	TargetTeamUID    types.UID `json:"targetTeamUID,omitempty"`
	TargetSandboxUID types.UID `json:"targetSandboxUID,omitempty"`
	TargetPolicyUID  types.UID `json:"targetPolicyUID,omitempty"`
	// ExecutionRevision identifies the target policy projection verified before
	// the source was stopped.
	ExecutionRevision string `json:"executionRevision,omitempty"`
	// TransitionID identifies the durable coordination record that fences the
	// old native reservation and pins the target execution until it starts.
	TransitionID string `json:"transitionID,omitempty"`
	// Fenced is persisted before any key, ingress, or workload stop side
	// effect. It distinguishes a preflight failure from a failed stop.
	Fenced             bool   `json:"fenced,omitempty"`
	Stopped            bool   `json:"stopped,omitempty"`
	FailureReason      string `json:"failureReason,omitempty"`
	FailureMessage     string `json:"failureMessage,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
}

// TeamChangeRequestsEqual compares the complete caller-authored operation,
// including an omitted TargetSandbox versus an explicitly selected one.
func TeamChangeRequestsEqual(left, right *TeamChangeRequest) bool {
	return equality.Semantic.DeepEqual(left, right)
}

// OriginalTeamChangeRequest returns the durable caller payload. Older status
// records did not retain it separately, so they may be used only when their
// inline request is exactly equal to the current spec request. In particular,
// a controller-resolved TargetSandbox must never be treated as an omitted
// caller TargetSandbox.
func (s *TeamChangeStatus) OriginalTeamChangeRequest(spec *TeamChangeRequest) (*TeamChangeRequest, bool) {
	if s == nil {
		return nil, false
	}
	if s.OriginalRequest != nil {
		return s.OriginalRequest.DeepCopy(), true
	}
	if spec != nil && TeamChangeRequestsEqual(&s.TeamChangeRequest, spec) {
		return spec.DeepCopy(), true
	}
	return nil, false
}

// TeamChangeRequestForID returns the request currently in spec, or the
// durable original record after the caller has cleared spec following a
// completed operation.
func (a *Agent) TeamChangeRequestForID(requestID string) (*TeamChangeRequest, bool) {
	if a == nil || requestID == "" {
		return nil, false
	}
	if request := a.Spec.TeamChangeRequest; request != nil && request.RequestID == requestID {
		return request.DeepCopy(), true
	}
	request, known := a.Status.TeamChange.OriginalTeamChangeRequest(nil)
	if !known || request.RequestID != requestID {
		return nil, false
	}
	return request, true
}

func (a *Agent) TeamChangePending() bool {
	request, status := a.Spec.TeamChangeRequest, a.Status.TeamChange
	return request != nil && (status == nil || status.RequestID != request.RequestID ||
		(status.Phase != TeamChangeCompleted && (status.Phase != TeamChangeFailed || status.Fenced)))
}
