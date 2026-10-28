// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package sandboxworkload translates a shared Sandbox resolution into protected
// chart inputs. A render plan is not permission or evidence to start execution.
package sandboxworkload

import (
	"errors"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/ingressmtls"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

const (
	MaxPreparationSources   = 128
	ContractVersion         = "v1"
	ExpectationVersion      = "v1"
	ValuesKey               = "sandboxWorkload"
	RevisionAnnotation      = "cetirun.microsoft.com/sandbox-revision"
	AgentUIDAnnotation      = "cetirun.microsoft.com/agent-uid"
	AgentNameAnnotation     = "cetirun.microsoft.com/agent-name"
	ProtectedNamespaceLabel = "cetirun.microsoft.com/sandbox-protected"
	KindAnnotation          = "cetirun.microsoft.com/sandbox-workload"
	AgentWorkload           = "agent"
	HomePath                = "/home/vibe"
	SkillsPath              = "/home/vibe/.aks/skills"
	NativePodFinalizer      = "cetirun.microsoft.com/native-pod-lifetime"
)

var (
	ErrExecutionHeld             = errors.New("Sandbox execution remains held")
	ErrStorageTransitionRequired = errors.New("Sandbox HOME requires a separately approved stopped-workload transition")
)

// Controls carries compiler outputs, not user-selected destinations or grants.
// This package validates their shape but does not compile network or identity
// policy. Revision must cover both the resolution and the trusted control inputs.
type Controls struct {
	Revision           string
	ResolutionRevision string
	Binding            sandbox.Binding
	DNSIPs             []string
	TrustedProxyIPs    []string `json:",omitempty"`
	IngressMode        api.AgentPolicyIngressMode
	ServiceAccountName string
	Volumes            []corev1.Volume
	VolumeMounts       []corev1.VolumeMount
	Environment        []corev1.EnvVar
	Objects            []unstructured.Unstructured
	PodLabels          map[string]string
	IngressMTLS        *ingressmtls.Plan `json:",omitempty"`
}

type ObjectIdentity struct {
	Name string    `json:"name"`
	UID  types.UID `json:"uid"`
}

// Helpers selects only chart-owned helpers. Arbitrary init/sidecar paths are not
// part of the contract, including the not-yet-defined separate Copilot workload.
type Helpers struct {
	Doctor  bool `json:"doctor"`
	Logger  bool `json:"logger"`
	Desktop bool `json:"desktop"`
}

type Input struct {
	// SingleStart protects the retained native executor's sole Pod lifetime.
	// Gateway authorization is independent and is required for all executors.
	SingleStart      bool
	Reconciled       bool
	PlannedMigration *api.PlannedMigrationRequest `json:",omitempty"`
	Resolution       *sandbox.Resolution
	AgentName        string
	AgentGeneration  int64
	AgentType        string
	// CustomAgent contains the Harness-validated process identity. Built-in
	// harnesses keep their fixed profile identity and leave it empty.
	CustomAgent *CustomAgent
	// GatewayAPIBase is the protected, in-cluster OpenAI-compatible endpoint.
	// It is required for Custom because its process adapter consumes standard
	// OPENAI_BASE_URL/OPENAI_API_KEY variables.
	GatewayAPIBase string
	Namespace      ObjectIdentity
	WorkloadName   string
	ReleaseName    string
	RuntimeClass   *nodev1.RuntimeClass
	Home           *corev1.PersistentVolumeClaim
	Skills         *corev1.PersistentVolumeClaim
	SkillsMode     SkillsMode `json:",omitempty"`
	// Context is the controller-owned immutable snapshot of the Team context
	// consumed by Custom's canonical $HOME/.agents view. It is intentionally a
	// typed input: tenant Helm values must never select its source or files.
	Context           *Context `json:",omitempty"`
	Helpers           Helpers
	Controls          Controls
	AuthorizedSecrets []string
	GatewayKey        *ObjectIdentity
	ImagePullSecret   *ObjectIdentity `json:",omitempty"`
	// Terminal describes the separately isolated, platform-owned Custom
	// terminal. It never gives that workload HOME, model credentials, or the
	// Agent's service account.
	Terminal *Terminal `json:",omitempty"`
	// LoopbackProbe identifies the immutable platform image that installs the
	// static probe binary for Custom HTTP Harnesses. It is independent of
	// whether the browser terminal is enabled.
	LoopbackProbe *LoopbackProbe `json:",omitempty"`
}

// Context identifies an immutable ConfigMap and its exact canonical files.
// Custom currently supports only AGENTS.md; keeping the mapping explicit makes
// future additions an audited contract change rather than a chart input.
type Context struct {
	ObjectIdentity
	Files map[string]string `json:"files"`
}

// Terminal is selected by the platform and rendered only for a reconciled
// Custom Sandbox. Image is an immutable image reference.
type Terminal struct {
	Image              string `json:"image"`
	Port               int32  `json:"port"`
	SessionSeconds     int32  `json:"sessionSeconds"`
	ServiceAccountName string `json:"serviceAccountName"`
}

type LoopbackProbe struct {
	Image string `json:"image"`
}

// CustomAgent is the minimum Harness-validated identity required to render a
// portable Custom workload. It deliberately contains no command or arguments:
// Kubernetes preserves the approved image ENTRYPOINT/CMD.
type CustomAgent struct {
	Image       string              `json:"image"`
	Username    string              `json:"username"`
	Home        string              `json:"home"`
	Port        *int32              `json:"port,omitempty"`
	WebUI       bool                `json:"webUI,omitempty"`
	WebBasePath string              `json:"webBasePath,omitempty"`
	WebInstance string              `json:"webInstance,omitempty"`
	Environment []CustomEnvironment `json:"environment,omitempty"`
	Ports       []CustomPort        `json:"ports,omitempty"`
	HTTP        *CustomHTTP         `json:"http,omitempty"`
}

type CustomEnvironment struct {
	Name           string `json:"name"`
	Value          string `json:"value,omitempty"`
	CredentialSlot string `json:"credentialSlot,omitempty"`
}

type CustomPort struct {
	Name   string `json:"name"`
	Number int32  `json:"number"`
}

type CustomHTTP struct {
	PortName      string `json:"portName"`
	HealthPath    string `json:"healthPath"`
	ReadinessPath string `json:"readinessPath"`
	StartupPath   string `json:"startupPath"`
	ChatPath      string `json:"chatPath,omitempty"`
	ModelsPath    string `json:"modelsPath,omitempty"`
}

// Accounting uses Kubernetes effective init/sidecar accounting. Pod contains
// the supported Pod-level CPU/memory declarations; Effective includes runtime
// overhead and ephemeral-storage accounting, not an independent disk quota.
type Accounting struct {
	Pod       corev1.ResourceRequirements `json:"pod"`
	Effective corev1.ResourceRequirements `json:"effective"`
}

// Bundle is immutable to callers; methods return copies of its values.
type Bundle struct {
	input      Input
	accounting Accounting
}

// SourceReference pins exact resource versions except for the status-bearing
// Agent, whose generation and authorization-relevant intent are pinned instead.
type SourceReference struct {
	APIVersion      string    `json:"apiVersion"`
	Kind            string    `json:"kind"`
	Namespace       string    `json:"namespace,omitempty"`
	Name            string    `json:"name"`
	UID             types.UID `json:"uid"`
	ResourceVersion string    `json:"resourceVersion,omitempty"`
	Generation      int64     `json:"generation,omitempty"`
	IntentDigest    string    `json:"intentDigest,omitempty"`
}

// Expectation is persisted only by the controller in its protected state
// namespace. Admission re-reads it and its sources on every protected request.
// It cannot itself lift the lifecycle hold.
type Expectation struct {
	Version            string                 `json:"version"`
	Binding            sandbox.Binding        `json:"binding"`
	Revision           string                 `json:"revision"`
	Agent              ObjectIdentity         `json:"agent"`
	AgentGeneration    int64                  `json:"agentGeneration"`
	Namespace          ObjectIdentity         `json:"namespace"`
	Workload           ObjectIdentity         `json:"workload"`
	WorkloadKind       string                 `json:"workloadKind"`
	ControllerRevision string                 `json:"controllerRevision,omitempty"`
	ExecutionHeld      bool                   `json:"executionHeld"`
	SinglePod          bool                   `json:"singlePod,omitempty"`
	PodUID             types.UID              `json:"podUID,omitempty"`
	SourceDigests      map[string]string      `json:"sourceDigests,omitempty"`
	Template           corev1.PodTemplateSpec `json:"template"`
	Overhead           corev1.ResourceList    `json:"overhead,omitempty"`
	Sources            []SourceReference      `json:"sources"`
	Home               ObjectIdentity         `json:"home"`
	Skills             ObjectIdentity         `json:"skills"`
	SkillsMode         SkillsMode             `json:"skillsMode,omitempty"`
	Placement          *PlacementBinding      `json:"placement,omitempty"`
}

func AgentOwner(name string, uid types.UID) metav1.OwnerReference {
	controller, block := true, true
	return metav1.OwnerReference{
		APIVersion: "cetirun.microsoft.com/v1alpha1", Kind: "Agent", Name: name,
		UID: uid, Controller: &controller, BlockOwnerDeletion: &block,
	}
}
