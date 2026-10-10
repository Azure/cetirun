// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AgentPolicySpec defines controls independently of runtime selection.
type AgentPolicySpec struct {
	// Harnesses limits runtimes that can receive this policy.
	Harnesses AgentPolicyHarnesses `json:"harnesses"`

	// Skills is the allowlist for Team defaults and per-Agent skill requests.
	// +optional
	Skills AgentPolicySkills `json:"skills,omitempty"`

	// ModelAccess defines the Gateway entitlements and one native USD budget
	// for the Agent virtual key.
	// +optional
	ModelAccess AgentPolicyModelAccess `json:"modelAccess,omitempty"`

	// AgentIdentity permits the fixed v1 credential-service entitlement.
	// +optional
	AgentIdentity AgentPolicyAgentIdentity `json:"agentIdentity,omitempty"`

	// EntraID remains the Sandbox adapter capability switch. Policy uses
	// AgentIdentity for the user-facing entitlement and requires both controls
	// before an Agent Identity can be provisioned.
	// +kubebuilder:default={}
	// +optional
	EntraID AgentPolicyEntraID `json:"entraID,omitempty"`
	// +kubebuilder:default={}
	// +optional
	SandboxSelection SandboxSelectionPolicy `json:"sandboxSelection,omitempty"`

	// +kubebuilder:default={}
	// +optional
	Security AgentPolicySecurity `json:"security,omitempty"`

	// +kubebuilder:default={}
	// +optional
	Network AgentPolicyNetwork `json:"network,omitempty"`

	// KubernetesAccess requires separate authorization; its presence grants nothing.
	// +optional
	KubernetesAccess *AgentPolicyAdminKubernetesAccess `json:"kubernetesAccess,omitempty"`
}

type AgentPolicyHarnesses struct {
	// +kubebuilder:validation:MinItems=1
	// Policy has typed Gateway and Sandbox enforcement adapters only for
	// the built-in harnesses. Custom workloads cannot be granted a policy.
	// +kubebuilder:validation:items:Enum=openclaw;hermes
	// +listType=set
	Allowed []AgentType `json:"allowed"`
}

type AgentPolicySkills struct {
	// +listType=set
	// +optional
	Allowed []string `json:"allowed,omitempty"`
}

type AgentPolicyModelAccess struct {
	// +optional
	Copilot AgentPolicyCopilotAccess `json:"copilot,omitempty"`
	// +optional
	SharedModels AgentPolicySharedModelsAccess `json:"sharedModels,omitempty"`
	// +optional
	GatewayBudget *AgentPolicyGatewayBudget `json:"gatewayBudget,omitempty"`
}

type AgentPolicyCopilotAccess struct {
	// +optional
	Allowed bool `json:"allowed,omitempty"`
}

type AgentPolicySharedModelsAccess struct {
	// Model identifiers are exact Gateway catalog names, not display labels.
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=256
	// +kubebuilder:validation:items:Pattern=`^[^\t\r\n ]+$`
	// +listType=set
	// +optional
	Allowed []string `json:"allowed,omitempty"`
}

type AgentPolicyGatewayBudget struct {
	// +kubebuilder:validation:Pattern=`^(0|[1-9][0-9]*)(\.[0-9]{1,6})?$`
	DefaultMonthlyUSD string `json:"defaultMonthlyUSD"`
	// +kubebuilder:validation:Pattern=`^(0|[1-9][0-9]*)(\.[0-9]{1,6})?$`
	MaxMonthlyUSD string `json:"maxMonthlyUSD"`
}

type AgentPolicyAgentIdentity struct {
	// +optional
	Allowed bool `json:"allowed,omitempty"`
	// +optional
	Required bool `json:"required,omitempty"`
	// +kubebuilder:validation:items:Enum=azure-resource-manager
	// +listType=set
	// +optional
	CredentialServices []string `json:"credentialServices,omitempty"`
}

type AgentPolicyEntraID struct {
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`
}

type SandboxSelectionPolicy struct {
	// +kubebuilder:default=false
	// +optional
	AllowMultiple bool `json:"allowMultiple,omitempty"`
}

type AgentPolicySecurity struct {
	// +kubebuilder:default=true
	// +optional
	ReadOnlyRootFilesystem *bool `json:"readOnlyRootFilesystem,omitempty"`
}

type AgentPolicyNetwork struct {
	// +kubebuilder:default={}
	// +listType=atomic
	// +optional
	BlockedCIDRs []string `json:"blockedCIDRs,omitempty"`

	// BlockedFQDNs contains exact lowercase ASCII hostnames, not URLs or IPs.
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=253
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +kubebuilder:default={}
	// +listType=atomic
	// +optional
	BlockedFQDNs []string `json:"blockedFQDNs,omitempty"`

	// +kubebuilder:default={}
	// +listType=atomic
	// +optional
	BlockedPorts []AgentPolicyPort `json:"blockedPorts,omitempty"`

	// +kubebuilder:default={}
	// +listType=atomic
	// +optional
	AllowedInternalCIDRs []AgentPolicyCIDRRule `json:"allowedInternalCIDRs,omitempty"`

	// +kubebuilder:default={}
	// +optional
	Ingress AgentPolicyIngress `json:"ingress,omitempty"`
}

type AgentPolicyCIDRRule struct {
	// +kubebuilder:validation:MinLength=1
	CIDR string `json:"cidr"`

	// +kubebuilder:validation:MinItems=1
	// +listType=atomic
	Ports []AgentPolicyPort `json:"ports"`
}

type AgentPolicyPort struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`

	// +kubebuilder:validation:Enum=TCP;UDP;SCTP
	// +kubebuilder:default=TCP
	// +optional
	Protocol corev1.Protocol `json:"protocol,omitempty"`
}

// +kubebuilder:validation:Enum=authenticated;disabled
type AgentPolicyIngressMode string

const (
	AgentPolicyIngressAuthenticated AgentPolicyIngressMode = "authenticated"
	AgentPolicyIngressDisabled      AgentPolicyIngressMode = "disabled"
)

// +kubebuilder:validation:XValidation:rule="!has(self.mode) || self.mode != 'disabled' || !has(self.requireMTLS) || !self.requireMTLS",message="disabled ingress cannot require mTLS"
type AgentPolicyIngress struct {
	// +kubebuilder:default=authenticated
	// +optional
	Mode AgentPolicyIngressMode `json:"mode,omitempty"`

	// +kubebuilder:default=false
	// +optional
	RequireMTLS bool `json:"requireMTLS,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="(has(self.roleBindings) && size(self.roleBindings) > 0) || (has(self.clusterRoleBindings) && size(self.clusterRoleBindings) > 0)",message="at least one explicit RBAC grant is required"
type AgentPolicyAdminKubernetesAccess struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	ServiceAccountName string `json:"serviceAccountName"`

	// +listType=atomic
	// +optional
	RoleBindings []AgentPolicyRoleBinding `json:"roleBindings,omitempty"`

	// +listType=atomic
	// +optional
	ClusterRoleBindings []AgentPolicyClusterRoleBinding `json:"clusterRoleBindings,omitempty"`
}

type AgentPolicyRoleBinding struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Namespace string `json:"namespace"`

	// ClusterRoleName is an explicit Kubernetes path-segment name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[^/%]+$`
	// +kubebuilder:validation:XValidation:rule="self != '.' && self != '..'",message="a ClusterRole name cannot be '.' or '..'"
	ClusterRoleName string `json:"clusterRoleName"`
}

type AgentPolicyClusterRoleBinding struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[^/%]+$`
	// +kubebuilder:validation:XValidation:rule="self != '.' && self != '..'",message="a ClusterRole name cannot be '.' or '..'"
	ClusterRoleName string `json:"clusterRoleName"`
}

const ConditionAgentPolicyAccepted = "Accepted"

// AgentPolicyStatus reports configuration acceptance, not Agent enforcement.
type AgentPolicyStatus struct {
	// +kubebuilder:validation:Minimum=0
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=agentpolicies,scope=Cluster,shortName=crp,singular=agentpolicy
// +kubebuilder:metadata:annotations="helm.sh/resource-policy=keep"
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=`.status.conditions[?(@.type=="Accepted")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

type AgentPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec AgentPolicySpec `json:"spec"`

	// +optional
	Status AgentPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type AgentPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentPolicy{}, &AgentPolicyList{})
}
