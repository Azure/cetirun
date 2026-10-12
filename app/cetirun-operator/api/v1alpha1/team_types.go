// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TeamSpec defines team-wide configuration applied to every member instance.
type TeamSpec struct {
	// AgentPolicyRef names the policy applied to every Agent in this Team.
	// A Team without any policy or Sandbox fields retains legacy execution.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +optional
	AgentPolicyRef string `json:"agentPolicyRef,omitempty"`

	// DeprecatedNormalAgentPolicyRef retains old policy intent for rejection only.
	// +kubebuilder:validation:MaxLength=0
	// +optional
	DeprecatedNormalAgentPolicyRef string `json:"normalAgentPolicyRef,omitempty"`

	// DeprecatedAdminAgentPolicyRef retains old policy intent for rejection only.
	// +kubebuilder:validation:MaxLength=0
	// +optional
	DeprecatedAdminAgentPolicyRef string `json:"adminAgentPolicyRef,omitempty"`

	// SandboxRefs lists the Sandboxes permitted for this Team.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=253
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +listType=set
	// +optional
	SandboxRefs []string `json:"sandboxRefs,omitempty"`

	// DefaultSandboxRef selects a listed Sandbox for newly created Agents.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +optional
	DefaultSandboxRef string `json:"defaultSandboxRef,omitempty"`

	// MemberContext defines the team context injected into USER.md for every member instance.
	// +optional
	MemberContext TeamContextSpec `json:"memberContext,omitempty"`

	// Defaults sets the default AI provider for all team members.
	// Per-instance Helm values overrides take precedence when set.
	// +optional
	Defaults TeamDefaultsSpec `json:"defaults,omitempty"`

	// Skills is the list of default skills for all team members.
	// Each entry is a ClawHub package ID (e.g. "@anthropic/mcp-server-fetch")
	// or an npm package prefixed with "npm:" (e.g. "npm:@openclaw/matrix").
	// +kubebuilder:validation:MaxItems=20
	// +kubebuilder:validation:items:Pattern=`^(npm:)?(@[a-z0-9][\w.-]*\/)?[a-z0-9][\w.-]*(@[\w.+-]+)?$`
	// +listType=set
	// +optional
	Skills []string `json:"skills,omitempty"`

	// SkillStorage describes a shared Azure Blob NFS container that the operator
	// mounts read-only into every member instance's pod. When set, the operator
	// populates the chart's skills.shared mode with the given storage account
	// coordinates so that a PV+PVC are auto-provisioned per instance namespace.
	// +optional
	SkillStorage *SkillStorageSpec `json:"skillStorage,omitempty"`
}

// PolicyConfigured reports policy intent, including incomplete configurations.
func (s TeamSpec) PolicyConfigured() bool {
	return s.AgentPolicyRef != "" || s.DeprecatedNormalAgentPolicyRef != "" || s.DeprecatedAdminAgentPolicyRef != "" ||
		len(s.SandboxRefs) != 0 || s.DefaultSandboxRef != ""
}

// SkillStorageSpec describes a shared Azure Blob NFS container holding
// team-wide skills. Mounted read-only at MountPath in every member's pod.
type SkillStorageSpec struct {
	// ResourceGroup of the Azure storage account.
	ResourceGroup string `json:"resourceGroup"`

	// StorageAccount name (globally unique, lowercase alphanumeric, 3-24 chars).
	StorageAccount string `json:"storageAccount"`

	// ContainerName of the blob container holding the skills tree.
	ContainerName string `json:"containerName"`

	// StorageClassName for the auto-provisioned PV/PVC. Defaults to the
	// chart default ("azureblob-nfs-premium") when empty.
	// +optional
	StorageClassName string `json:"storageClassName,omitempty"`

	// StorageSize requested for the PVC. Defaults to the chart default
	// ("100Gi") when empty.
	// +optional
	StorageSize string `json:"storageSize,omitempty"`

	// MountPath inside the container. Defaults to the chart default
	// ("/home/vibe/.aks/skills") when empty.
	// +optional
	MountPath string `json:"mountPath,omitempty"`
}

// TeamContextSpec defines the team-level context injected into every member's environment.
type TeamContextSpec struct {
	// Role is the team member's role, e.g. "Engineer".
	// +optional
	Role string `json:"role,omitempty"`

	// Description contains lines injected into the ## Context section of USER.md.
	// +kubebuilder:validation:MaxItems=20
	// +optional
	Description []string `json:"description,omitempty"`
}

// TeamDefaultsSpec sets the default AI provider for team members.
type TeamDefaultsSpec struct {
	// Provider is the default AI provider (e.g. "github_copilot", "anthropic", "openai").
	// +optional
	Provider string `json:"provider,omitempty"`
}

// TeamStatus defines the observed state of Team.
type TeamStatus struct {
	// Conditions contains the latest available observations of the team's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the last CR generation that was fully reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

const ConditionTeamAccepted = "Accepted"

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=teams,scope=Cluster,shortName=crt,singular=team
// +kubebuilder:metadata:annotations="helm.sh/resource-policy=keep"
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=`.spec.memberContext.role`
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.defaults.provider`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Team is a cluster-scoped resource that defines team-wide configuration
// (user context, AI provider defaults, and skills) applied to every Agent that
// references this team via spec.team.
type Team struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TeamSpec   `json:"spec,omitempty"`
	Status TeamStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TeamList contains a list of Team.
type TeamList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Team `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Team{}, &TeamList{})
}
