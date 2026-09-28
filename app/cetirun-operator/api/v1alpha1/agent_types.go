// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// SkillOverrideSpec defines per-instance skill customization as a delta against team defaults.
type SkillOverrideSpec struct {
	// Add lists skills to install in addition to team defaults.
	// Each entry is a ClawHub package ID (e.g. "@anthropic/mcp-server-fetch")
	// or an npm package prefixed with "npm:" (e.g. "npm:@openclaw/matrix").
	// +kubebuilder:validation:MaxItems=20
	// +kubebuilder:validation:items:Pattern=`^(npm:)?(@[a-z0-9][\w.-]*\/)?[a-z0-9][\w.-]*(@[\w.+-]+)?$`
	// +listType=set
	// +optional
	Add []string `json:"add,omitempty"`

	// Remove lists team-default skills to exclude from this instance.
	// Uses the same format as Add. Entries not present in the team skill
	// list are silently ignored.
	// +kubebuilder:validation:MaxItems=20
	// +kubebuilder:validation:items:Pattern=`^(npm:)?(@[a-z0-9][\w.-]*\/)?[a-z0-9][\w.-]*(@[\w.+-]+)?$`
	// +listType=set
	// +optional
	Remove []string `json:"remove,omitempty"`
}

// AgentSpec defines the desired state of Agent.
// +kubebuilder:validation:XValidation:rule="self.agent.type == 'custom' ? !has(self.gatewayTokenSecretRef) : has(self.gatewayTokenSecretRef)",message="gatewayTokenSecretRef is required for built-in agents and forbidden for custom agents",fieldPath=".gatewayTokenSecretRef"
// +kubebuilder:validation:XValidation:rule="!has(self.agentIdentity) || !has(self.agentIdentity.enabled) || !self.agentIdentity.enabled || self.agent.type != 'custom'",message="agentIdentity is not supported for custom agents",fieldPath=".agentIdentity"
type AgentSpec struct {
	// Agent is the required per-instance Agent configuration.
	// +required
	Agent HarnessSpec `json:"agent"`

	// User information for this instance.
	// +kubebuilder:validation:Required
	User UserSpec `json:"user"`

	// Team is the name of the Team this instance belongs to.
	// The operator merges team defaults (user context, AI model, skills) into
	// the instance configuration. Per-instance fields take precedence.
	// +optional
	Team string `json:"team,omitempty"`

	// Model is the GitHub Copilot model ID for this instance. When empty, the
	// operator deployment's configured default model is used.
	// +kubebuilder:validation:MaxLength=256
	// +optional
	Model string `json:"model,omitempty"`

	// SkillOverrides customizes the set of skills installed for this instance.
	// Add appends skills on top of the team defaults; Remove excludes
	// specific team skills from this instance.
	// +optional
	SkillOverrides SkillOverrideSpec `json:"skillOverrides,omitempty"`

	// AgentIdentity enables the Microsoft Entra Agent Identity integration for
	// this instance. Identity identifiers and owner claims are platform-managed
	// and are deliberately not accepted in spec.
	// +optional
	AgentIdentity *AgentIdentitySpec `json:"agentIdentity,omitempty"`

	// GatewayTokenSecretRef names the built-in Agent's multi-purpose Secret
	// in the instance namespace. Required for openclaw/hermes and forbidden for
	// custom; it is not the model-access or registry credential.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +optional
	GatewayTokenSecretRef string `json:"gatewayTokenSecretRef,omitempty"`

	// Tailscale configures the Tailscale mesh VPN sidecar for this instance.
	// When set, the operator reads the auth key from the referenced Secret
	// and passes it to the OpenClaw Helm chart. Other Tailscale settings
	// (hostname, serve, relaxGatewayAuth) use chart defaults and can be
	// overridden via spec.helm.values if needed.
	// +optional
	Tailscale *TailscaleSpec `json:"tailscale,omitempty"`

	// Helm chart configuration overrides.
	// +optional
	Helm HelmSpec `json:"helm,omitempty"`

	// MSTeams configures Microsoft Teams bot integration for this instance.
	// When set, the operator provisions the necessary Helm values, volumes,
	// and Istio routing for the Teams webhook endpoint.
	// +optional
	MSTeams *MSTeamsSpec `json:"msteams,omitempty"`

	// Suspended, when true, scales the instance to zero replicas and removes
	// ingress routing. The instance data (PVC) is preserved. Set this field
	// to restrict access for a user without deleting their instance.
	// Default: false.
	// +optional
	Suspended bool `json:"suspended,omitempty"`
}

// TailscaleSpec configures the Tailscale mesh VPN sidecar.
type TailscaleSpec struct {
	// AuthKeySecretRef is the name of a Secret in the instance namespace (cetirun-<alias>)
	// containing a Tailscale auth key under the data key "TAILSCALE_AUTH_KEY".
	// Setting this enables Tailscale (no separate toggle).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`
	AuthKeySecretRef string `json:"authKeySecretRef"`
}

// TailscaleAuthKeyField is the Secret data key for the Tailscale auth key.
const TailscaleAuthKeyField = "TAILSCALE_AUTH_KEY"

// UserSpec identifies the user this instance is provisioned for.
type UserSpec struct {
	// Alias is the user's short name, used for the namespace, hostname, and Helm release.
	// Must be a lowercase alphanumeric string optionally separated by hyphens.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9-]{0,62}$`
	Alias string `json:"alias"`

	// DisplayName is the human-readable name shown in the UI.
	// +kubebuilder:validation:Required
	DisplayName string `json:"displayName"`

	// Email address for notifications (optional).
	// +optional
	Email string `json:"email,omitempty"`

	// Team is the user's team name.
	// Deprecated: use spec.team instead. This field will be removed in a future version.
	// +optional
	Team string `json:"team,omitempty"`
}

// HelmSpec allows per-instance Helm chart configuration overrides.
type HelmSpec struct {
	// Version pins the OpenClaw Helm chart version for this instance.
	// When empty on first reconcile, the operator backfills it with
	// its own --chart-version default so that future operator upgrades
	// do not automatically roll out a new chart version (which would
	// restart the StatefulSet pod and interrupt the user's session).
	// Note: the initial backfill bumps the CR generation, which triggers
	// a single Helm upgrade call. Because the backfilled version matches
	// the chart already installed, this upgrade is a no-op and does not
	// restart the StatefulSet pod.
	// To upgrade an instance, update this field to the desired version.
	// +optional
	Version string `json:"version,omitempty"`

	// Values contains arbitrary Helm values merged on top of operator defaults.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Values map[string]runtime.RawExtension `json:"values,omitempty"`
}

// AgentStatus defines the observed state of Agent.
type AgentStatus struct {
	// Conditions contains the latest available observations of the instance's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Phase represents the current lifecycle phase.
	// +kubebuilder:validation:Enum=Pending;Provisioning;Ready;Failed;Deleting;Suspended
	// +optional
	Phase string `json:"phase,omitempty"`

	// AccessURL is the HTTPS endpoint for the instance's native agent UI.
	// Example: "https://cetirun.example.com/ui/alice/"
	// +optional
	AccessURL string `json:"accessUrl,omitempty"`

	// Namespace is the Kubernetes namespace where the instance is deployed.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// HelmReleaseName is the Helm release name for this instance.
	// +optional
	HelmReleaseName string `json:"helmReleaseName,omitempty"`

	// ObservedGeneration is the last CR generation that was fully reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ObservedOperatorHash is a hash of the operator-level configuration (e.g. ChartVersion, DomainSuffix)
	// that was in effect during the last successful reconciliation. A change in this hash
	// triggers a Helm upgrade even when the CR generation has not changed.
	// +optional
	ObservedOperatorHash string `json:"observedOperatorHash,omitempty"`

	// LastReconcileTime is the timestamp of the last successful reconciliation.
	// +optional
	LastReconcileTime *metav1.Time `json:"lastReconcileTime,omitempty"`

	// MSTeams reports the observed state of Teams integration resources.
	// +optional
	MSTeams *MSTeamsStatus `json:"msteams,omitempty"`

	// AgentIdentity reports the platform-managed Entra identity lifecycle.
	// It never contains user tokens, authorization caches, or credentials.
	// +optional
	AgentIdentity *AgentIdentityStatus `json:"agentIdentity,omitempty"`
}

// AgentIdentitySpec exposes only the feature switch. Tenant, Blueprint,
// owner, and credential settings are controlled by the CetiRun deployment.
type AgentIdentitySpec struct {
	// Enabled requests a dedicated Agent Identity for this instance.
	// +optional
	Enabled bool `json:"enabled,omitempty"`
}

// AgentIdentityStatus reports non-secret identity lifecycle information.
type AgentIdentityStatus struct {
	// Phase is the current Agent Identity lifecycle phase.
	// +kubebuilder:validation:Enum=Disabled;Provisioning;Ready;Degraded;Failed;Deleting
	// +optional
	Phase string `json:"phase,omitempty"`

	// ClientID is the public application identifier of the Agent Identity.
	// +optional
	ClientID string `json:"clientId,omitempty"`

	// ObjectID is the directory object identifier used for deterministic cleanup.
	// It is platform-managed and is not a credential.
	// +optional
	ObjectID string `json:"objectId,omitempty"`

	// DisplayName is the immutable directory display name bound to this Agent
	// Identity. It remains stable if the deployment name prefix changes.
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// CreationAttempted records a durable create checkpoint, which prevents a
	// retry after an unknown request outcome from creating a second identity.
	// +optional
	CreationAttempted bool `json:"creationAttempted,omitempty"`

	// ObservedGeneration is the Agent generation reconciled into this status.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastVerifiedTime records the most recent canonical directory verification.
	// +optional
	LastVerifiedTime *metav1.Time `json:"lastVerifiedTime,omitempty"`

	// Reason is a stable, non-sensitive machine-readable status reason.
	// +optional
	Reason string `json:"reason,omitempty"`
}

// Condition type constants.
const (
	ConditionReady              = "Ready"
	ConditionHelmDeployed       = "HelmDeployed"
	ConditionIngressReady       = "IngressReady"
	ConditionPodReady           = "PodReady"
	ConditionTailscaleReady     = "TailscaleReady"
	ConditionTeamsReady         = "TeamsReady"
	ConditionAgentConfigReady   = "AgentConfigReady"
	ConditionAgentIdentityReady = "AgentIdentityReady"
)

// Agent Identity lifecycle phases.
const (
	AgentIdentityPhaseDisabled     = "Disabled"
	AgentIdentityPhaseProvisioning = "Provisioning"
	AgentIdentityPhaseReady        = "Ready"
	AgentIdentityPhaseDegraded     = "Degraded"
	AgentIdentityPhaseFailed       = "Failed"
	AgentIdentityPhaseDeleting     = "Deleting"
)

// MSTeams state constants.
const (
	TeamsStateProvisioning           = "Provisioning"
	TeamsStatePendingAppRegistration = "PendingAppRegistration"
	TeamsStateReady                  = "Ready"
	TeamsStateFailed                 = "Failed"
)

// Phase constants.
const (
	PhasePending      = "Pending"
	PhaseProvisioning = "Provisioning"
	PhaseReady        = "Ready"
	PhaseFailed       = "Failed"
	PhaseDeleting     = "Deleting"
	PhaseSuspended    = "Suspended"
)

// Finalizer name used by the operator.
const Finalizer = "cetirun.microsoft.com/finalizer"

// Owner binding annotations are written only by trusted platform code from
// verified Entra ID-token claims.
const (
	AnnotationOwnerTenantID = "agentidentity.cetirun.microsoft.com/owner-tenant-id"
	AnnotationOwnerObjectID = "agentidentity.cetirun.microsoft.com/owner-object-id"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=agents,scope=Cluster,shortName=cra,singular=agent
// +kubebuilder:printcolumn:name="Alias",type=string,JSONPath=`.spec.user.alias`
// +kubebuilder:printcolumn:name="Agent",type=string,JSONPath=`.spec.agent.type`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="AccessURL",type=string,JSONPath=`.status.accessUrl`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Agent is a cluster-scoped, long-lived agent environment.
type Agent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec   AgentSpec   `json:"spec"`
	Status AgentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AgentList contains a list of Agent.
type AgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Agent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Agent{}, &AgentList{})
}

// MSTeamsSpec configures Microsoft Teams bot integration.
type MSTeamsSpec struct {
	// Enabled toggles Teams bot provisioning. When false or omitted, no Teams
	// resources are created even if the MSTeams block is present.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// AppID is the Azure AD Application (client) ID for the Teams bot.
	// If Enabled=true and AppID is empty, the operator creates the app
	// automatically and writes the generated appId to status.msteams.appId.
	// If provided, the operator uses this existing app instead.
	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`
	AppID string `json:"appId,omitempty"`

	// TenantID is the Azure AD tenant ID. Defaults to operator-level config if not set.
	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`
	TenantID string `json:"tenantId,omitempty"`

	// WebhookPort is the port the Teams webhook listener runs on inside the container.
	// Defaults to 3978.
	// +kubebuilder:default=3978
	// +optional
	WebhookPort int32 `json:"webhookPort,omitempty"`
}

// MSTeamsStatus reports the observed state of Teams integration resources.
type MSTeamsStatus struct {
	// State is the current state of Teams provisioning.
	// +kubebuilder:validation:Enum=Provisioning;PendingAppRegistration;Ready;Failed;Deleting
	// +optional
	State string `json:"state,omitempty"`

	// Message provides human-readable detail about the current state.
	// +optional
	Message string `json:"message,omitempty"`

	// AppID is the Azure AD Application (client) ID, whether auto-created or from spec.
	// +optional
	AppID string `json:"appId,omitempty"`

	// AppObjectID is the Azure AD Application Object ID (used for Graph API management).
	// +optional
	AppObjectID string `json:"appObjectId,omitempty"`

	// BotResourceID is the Azure Resource Manager resource ID of the Bot Service.
	// +optional
	BotResourceID string `json:"botResourceId,omitempty"`

	// ServicePrincipalID is the Object ID of the service principal for the bot app.
	// +optional
	ServicePrincipalID string `json:"servicePrincipalId,omitempty"`

	// AppNotFoundSince is the timestamp when the cached AppObjectID first returned
	// a 404 from Graph API. Used to distinguish AAD eventual consistency delays
	// (which can last up to ~15 min) from genuinely deleted apps. Cleared on
	// successful app lookup.
	// +optional
	AppNotFoundSince *metav1.Time `json:"appNotFoundSince,omitempty"`
}
