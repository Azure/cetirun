// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package v1alpha1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// AnnotationGovernanceAction is copied into the workload Pod template for
	// Restart and Reconcile actions. Its value is the immutable action UID.
	AnnotationGovernanceAction = "cetirun.microsoft.com/governance-action"
	// AnnotationGovernanceActionClaim serializes GovernanceAction execution for
	// one Agent. It exists only while an action is Executing.
	AnnotationGovernanceActionClaim = "cetirun.microsoft.com/governance-action-claim"
)

// GovernanceAction is a narrow, auditable proposal to change an Agent's
// operational state. It is intentionally not a command, patch, URL, or task
// execution API.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=governanceactions,scope=Cluster,shortName=cga,singular=governanceaction
// +kubebuilder:metadata:annotations="helm.sh/resource-policy=keep"
// +kubebuilder:printcolumn:name="Operation",type=string,JSONPath=`.spec.operation`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Expires",type=date,JSONPath=`.spec.expiresAt`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type GovernanceAction struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GovernanceActionSpec   `json:"spec"`
	Status GovernanceActionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type GovernanceActionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GovernanceAction `json:"items"`
}

type GovernanceActionSpec struct {
	Requester GovernanceActionRequester `json:"requester"`
	TargetRef GovernanceActionTargetRef `json:"targetRef"`
	// +kubebuilder:validation:Enum=Suspend;Resume;Restart;Reconcile
	Operation           GovernanceActionOperation   `json:"operation"`
	ExpectedProjections GovernanceActionProjections `json:"expectedProjections"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	Reason      string      `json:"reason"`
	RequestedAt metav1.Time `json:"requestedAt"`
	ExpiresAt   metav1.Time `json:"expiresAt"`
	// +optional
	Approval *GovernanceActionApproval `json:"approval,omitempty"`
}

type GovernanceActionRequester struct {
	// +kubebuilder:validation:Enum=CetiAdmin
	Kind string `json:"kind"`
	// +kubebuilder:validation:MinLength=1
	Subject string `json:"subject"`
}

type GovernanceActionTargetRef struct {
	// +kubebuilder:validation:Enum=cetirun.microsoft.com/v1alpha1
	APIVersion string `json:"apiVersion"`
	// +kubebuilder:validation:Enum=Agent
	Kind       string    `json:"kind"`
	Name       string    `json:"name"`
	UID        types.UID `json:"uid"`
	Generation int64     `json:"generation"`
}

type GovernanceActionOperation string

const (
	GovernanceActionSuspend   GovernanceActionOperation = "Suspend"
	GovernanceActionResume    GovernanceActionOperation = "Resume"
	GovernanceActionRestart   GovernanceActionOperation = "Restart"
	GovernanceActionReconcile GovernanceActionOperation = "Reconcile"
)

type GovernanceActionProjections struct {
	AuthorizationRevision    string `json:"authorizationRevision,omitempty"`
	SandboxRevision          string `json:"sandboxRevision,omitempty"`
	GatewayPolicyRevision    string `json:"gatewayPolicyRevision,omitempty"`
	CredentialPolicyRevision string `json:"credentialPolicyRevision,omitempty"`
}

type GovernanceActionApproval struct {
	// +kubebuilder:validation:Enum=Approved;Denied
	Decision     string            `json:"decision"`
	Approver     GovernanceSubject `json:"approver"`
	ActionDigest string            `json:"actionDigest"`
	ApprovedAt   metav1.Time       `json:"approvedAt"`
	ExpiresAt    metav1.Time       `json:"expiresAt"`
}

type GovernanceActionStatus struct {
	// +kubebuilder:validation:Enum=Proposed;AwaitingApproval;Approved;Executing;Succeeded;Failed;Unknown;Expired;Denied
	Phase               string                      `json:"phase,omitempty"`
	ObservedProjections GovernanceActionProjections `json:"observedProjections,omitempty"`
	ResultSummary       string                      `json:"resultSummary,omitempty"`
	ObservedGeneration  int64                       `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// GovernanceActionDigest returns the stable digest which an approval binds to.
//
// It intentionally does not marshal GovernanceActionSpec directly: the wire
// representation of metav1.Time and future, non-authorizing spec fields must
// not silently change the approval contract. Keep this function as the only
// digest implementation used by API clients, admission, and controllers.
func GovernanceActionDigest(spec GovernanceActionSpec) (string, error) {
	payload := struct {
		Version     string                      `json:"version"`
		Requester   GovernanceActionRequester   `json:"requester"`
		TargetRef   GovernanceActionTargetRef   `json:"targetRef"`
		Operation   GovernanceActionOperation   `json:"operation"`
		Projections GovernanceActionProjections `json:"expectedProjections"`
		Reason      string                      `json:"reason"`
		RequestedAt string                      `json:"requestedAt"`
		ExpiresAt   string                      `json:"expiresAt"`
	}{
		Version:     "governance-action-v1",
		Requester:   spec.Requester,
		TargetRef:   spec.TargetRef,
		Operation:   spec.Operation,
		Projections: spec.ExpectedProjections,
		Reason:      spec.Reason,
		RequestedAt: spec.RequestedAt.UTC().Format(time.RFC3339Nano),
		ExpiresAt:   spec.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func init() {
	SchemeBuilder.Register(&GovernanceAction{}, &GovernanceActionList{})
}
