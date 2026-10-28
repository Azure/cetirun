// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxworkload

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/runtimebindings"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/internal/terminalpolicy"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"
	strictjson "sigs.k8s.io/json"
)

const (
	ReconcileExpectationVersion = "v2"
	ReconciledAnnotation        = "sandbox.cetirun.microsoft.com/reconciled"
)

// ReconcileExpectation replaces a one-Pod allowance with the current authorized
// chart output. It uses the same protected state object, not another authority.
type ReconcileExpectation struct {
	Version         string                      `json:"version"`
	Agent           ObjectIdentity              `json:"agent"`
	AgentGeneration int64                       `json:"agentGeneration"`
	Namespace       ObjectIdentity              `json:"namespace"`
	Binding         sandbox.Binding             `json:"binding"`
	Revision        string                      `json:"revision"`
	PlanHash        string                      `json:"planHash"`
	Home            ObjectIdentity              `json:"home"`
	Workloads       []RenderedWorkload          `json:"workloads"`
	Sources         []SourceReference           `json:"sources"`
	SourceDigests   map[string]string           `json:"sourceDigests"`
	Controls        []unstructured.Unstructured `json:"controls"`
	Terminal        *TerminalTarget             `json:"terminal,omitempty"`
}

// TerminalTarget records the only CONNECT target allowed for a platform
// terminal. It is controller-owned expectation data, never caller input.
type TerminalTarget struct {
	ServiceAccountName string `json:"serviceAccountName"`
	TargetWorkload     string `json:"targetWorkload"`
	Container          string `json:"container"`
}

type RenderedWorkload struct {
	Kind     string                 `json:"kind"`
	Identity ObjectIdentity         `json:"identity"`
	Replicas int32                  `json:"replicas"`
	Template corev1.PodTemplateSpec `json:"template"`
	Overhead corev1.ResourceList    `json:"overhead,omitempty"`
}

func (e ReconcileExpectation) Object(namespace string) (*corev1.ConfigMap, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	if namespace == "" || namespace == e.Namespace.Name {
		return nil, fmt.Errorf("Sandbox expectations require an independent controller namespace")
	}
	raw, err := json.Marshal(e)
	if err != nil || len(raw) > 512*1024 {
		return nil, fmt.Errorf("Sandbox reconciled expectation exceeds its encoding limit")
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace, Name: PodExpectationName(e.Namespace.Name, e.Namespace.UID),
			OwnerReferences: []metav1.OwnerReference{AgentOwner(e.Agent.Name, e.Agent.UID)},
			Annotations: map[string]string{
				AgentUIDAnnotation: string(e.Agent.UID), RevisionAnnotation: e.Revision,
			},
		},
		Data: map[string]string{PodExpectationKey: string(raw)},
	}, nil
}

func ReadReconcileExpectation(state *corev1.ConfigMap) (ReconcileExpectation, error) {
	return readReconcileExpectation(state, false)
}

func ReadReconcileExpectationForCleanup(state *corev1.ConfigMap) (ReconcileExpectation, error) {
	return readReconcileExpectation(state, true)
}

func readReconcileExpectation(state *corev1.ConfigMap, cleanup bool) (ReconcileExpectation, error) {
	var e ReconcileExpectation
	if state == nil || len(state.Data) != 1 || len(state.BinaryData) != 0 ||
		state.UID == "" || state.ResourceVersion == "" || (!cleanup && !state.DeletionTimestamp.IsZero()) {
		return e, fmt.Errorf("Sandbox reconciled expectation is unavailable")
	}
	raw := state.Data[PodExpectationKey]
	if len(raw) == 0 || len(raw) > 512*1024 {
		return e, fmt.Errorf("Sandbox reconciled expectation is empty or oversized")
	}
	problems, err := strictjson.UnmarshalStrict([]byte(raw), &e)
	if err != nil || len(problems) != 0 {
		return e, fmt.Errorf("Sandbox reconciled expectation is invalid")
	}
	if err := e.Validate(); err != nil {
		return e, err
	}
	if state.Name != PodExpectationName(e.Namespace.Name, e.Namespace.UID) ||
		state.Namespace == e.Namespace.Name ||
		state.Annotations[AgentUIDAnnotation] != string(e.Agent.UID) ||
		state.Annotations[RevisionAnnotation] != e.Revision ||
		!equality.Semantic.DeepEqual(state.OwnerReferences, []metav1.OwnerReference{AgentOwner(e.Agent.Name, e.Agent.UID)}) {
		return e, fmt.Errorf("Sandbox reconciled expectation ownership differs")
	}
	return e, nil
}

func (e ReconcileExpectation) Validate() error {
	if e.Version != ReconcileExpectationVersion || e.Agent.Name == "" || e.Agent.UID == "" ||
		e.AgentGeneration < 1 || e.Namespace.Name == "" || e.Namespace.UID == "" ||
		e.Home.Name == "" || e.Home.UID == "" || e.Binding.AgentUID != e.Agent.UID ||
		e.Binding.TeamUID == "" || e.Binding.SandboxUID == "" || e.Binding.AgentPolicyUID == "" ||
		!ValidInputDigest(e.Revision) || !ValidInputDigest(e.PlanHash) ||
		len(e.Workloads) == 0 || len(e.Workloads) > 32 || len(e.Sources) > MaxPreparationSources ||
		len(e.Controls) < 3 || len(e.Controls) > MaxPreparationSources {
		return fmt.Errorf("Sandbox reconciled expectation requires complete bounded identities")
	}

	names := map[string]bool{}
	var main *RenderedWorkload
	for _, workload := range e.Workloads {
		key := workload.Kind + "/" + workload.Identity.Name
		if (workload.Kind != "StatefulSet" && workload.Kind != "Deployment") || workload.Identity.Name == "" ||
			names[key] || workload.Replicas < 0 || workload.Replicas > 1 || len(workload.Template.Spec.Containers) == 0 {
			return fmt.Errorf("Sandbox reconciled workload inventory is invalid")
		}
		names[key] = true
		if workload.Kind == "StatefulSet" {
			if main != nil {
				return fmt.Errorf("Sandbox requires exactly one retained HOME workload")
			}
			copy := workload
			main = &copy
		}
	}
	if main == nil {
		return fmt.Errorf("Sandbox has no retained HOME workload")
	}
	if terminal := e.Terminal; terminal != nil {
		if len(validation.IsDNS1123Label(terminal.ServiceAccountName)) != 0 || terminal.ServiceAccountName != terminalpolicy.WorkloadName(main.Identity.Name) ||
			terminal.TargetWorkload != main.Identity.Name || terminal.Container != "cetirun" {
			return fmt.Errorf("Sandbox terminal target is invalid")
		}
		found := false
		for _, workload := range e.Workloads {
			if workload.Kind == "Deployment" && workload.Identity.Name == terminal.ServiceAccountName && workload.Replicas == 1 {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("Sandbox terminal workload is absent")
		}
	}
	sources := map[string]bool{}
	required := map[string]bool{}
	effective := 0
	for _, source := range e.Sources {
		key := SourceKey(source)
		if !source.ValidIdentity() || sources[key] {
			return fmt.Errorf("Sandbox source identities must be complete and unique")
		}
		sources[key] = true
		if EffectiveSource(source) {
			effective++
			if !ValidInputDigest(e.SourceDigests[key]) {
				return fmt.Errorf("Sandbox effective source digest is missing")
			}
		}
		if source.APIVersion == api.GroupVersion.String() && source.Namespace == "" {
			switch source.Kind {
			case "Agent":
				required["Agent"] = source.Name == e.Agent.Name && source.UID == e.Agent.UID
			case "Team":
				required["Team"] = source.UID == e.Binding.TeamUID
			case "Sandbox":
				required["Sandbox"] = source.UID == e.Binding.SandboxUID
			case "AgentPolicy":
				required["AgentPolicy"] = source.UID == e.Binding.AgentPolicyUID
			}
		}
		if source.APIVersion == "v1" && source.Kind == "Namespace" {
			required["Namespace"] = source.Name == e.Namespace.Name && source.UID == e.Namespace.UID && source.Namespace == ""
		}
		if source.APIVersion == "v1" && source.Kind == "PersistentVolumeClaim" && source.Name == e.Home.Name && source.Namespace == e.Namespace.Name {
			required["HOME"] = source.UID == e.Home.UID
		}
		if source.APIVersion == "node.k8s.io/v1" && source.Kind == "RuntimeClass" {
			required["RuntimeClass"] = source.Namespace == "" && source.Name == ptr.Deref(main.Template.Spec.RuntimeClassName, "")
		}
		if source.APIVersion == "storage.k8s.io/v1" && source.Kind == "StorageClass" && source.Namespace == "" {
			required["StorageClass"] = true
		}
		if source.APIVersion == "v1" && source.Kind == "ConfigMap" && source.Namespace != "" && source.Namespace != e.Namespace.Name {
			required["Platform"] = true
		}
		if source.APIVersion == "v1" && source.Kind == "ServiceAccount" && source.Namespace == e.Namespace.Name &&
			source.Name == main.Template.Spec.ServiceAccountName {
			required["ServiceAccount"] = true
		}
	}
	if effective != len(e.SourceDigests) {
		return fmt.Errorf("Sandbox source digest inventory differs from its sources")
	}
	for _, kind := range []string{"Agent", "Team", "Sandbox", "AgentPolicy", "Namespace", "HOME", "RuntimeClass", "StorageClass", "Platform"} {
		if !required[kind] {
			return fmt.Errorf("Sandbox source inventory is missing %s", kind)
		}
	}
	controls := map[string]bool{}
	for _, object := range e.Controls {
		key := SourceKey(SourceReference{APIVersion: object.GetAPIVersion(), Kind: object.GetKind(), Namespace: object.GetNamespace(), Name: object.GetName()})
		if controls[key] || object.GetName() == "" || object.GetNamespace() != e.Namespace.Name ||
			!equality.Semantic.DeepEqual(object.GetOwnerReferences(), []metav1.OwnerReference{AgentOwner(e.Agent.Name, e.Agent.UID)}) {
			return fmt.Errorf("Sandbox control inventory has ambiguous or foreign ownership")
		}
		controls[key] = true
		switch object.GetAPIVersion() + "/" + object.GetKind() {
		case "v1/ConfigMap", "v1/Service", "v1/ServiceAccount", "rbac.authorization.k8s.io/v1/RoleBinding", "networking.k8s.io/v1/NetworkPolicy", "cilium.io/v2/CiliumNetworkPolicy":
		default:
			return fmt.Errorf("Sandbox control inventory contains an unsupported resource")
		}
		required[object.GetKind()] = true
	}
	for _, kind := range []string{"ServiceAccount", "CiliumNetworkPolicy"} {
		if !required[kind] {
			return fmt.Errorf("Sandbox control inventory is missing %s", kind)
		}
	}
	if terminal := e.Terminal; terminal != nil {
		terminalAccount, terminalReader, terminalExec := false, false, false
		for _, object := range e.Controls {
			terminalAccount = terminalAccount || (object.GetAPIVersion() == "v1" && object.GetKind() == "ServiceAccount" &&
				object.GetName() == terminal.ServiceAccountName)
			terminalReader = terminalReader || (object.GetAPIVersion() == "rbac.authorization.k8s.io/v1" && object.GetKind() == "RoleBinding" &&
				object.GetName() == terminal.ServiceAccountName)
			terminalExec = terminalExec || (object.GetAPIVersion() == "rbac.authorization.k8s.io/v1" && object.GetKind() == "RoleBinding" &&
				object.GetName() == terminal.ServiceAccountName+"-exec")
		}
		if !terminalAccount || !terminalReader || !terminalExec {
			return fmt.Errorf("Sandbox terminal identity controls are absent")
		}
	}
	for _, workload := range e.Workloads {
		for _, source := range PodSourceReferences(workload.Template.Spec, e.Namespace.Name) {
			if !sources[SourceKey(source)] && !controls[SourceKey(source)] {
				return fmt.Errorf("Sandbox workload references an unrecorded %s", source.Kind)
			}
		}
	}
	return nil
}

func ValidateReconciledVolumes(spec corev1.PodSpec, claims []string, projections []corev1.Volume, scratchLimit resource.Quantity) error {
	for _, volume := range spec.Volumes {
		switch {
		case volume.PersistentVolumeClaim != nil:
			if volume.PersistentVolumeClaim.ClaimName == "" || !slices.Contains(claims, volume.PersistentVolumeClaim.ClaimName) {
				return fmt.Errorf("Sandbox component cannot mount unapproved persistent storage")
			}
		case volume.Projected != nil:
			for _, source := range volume.Projected.Sources {
				if source.Secret != nil || source.ConfigMap != nil || source.DownwardAPI != nil {
					continue
				}
				if source.ServiceAccountToken == nil || !slices.ContainsFunc(projections, func(approved corev1.Volume) bool {
					return equality.Semantic.DeepEqual(volume, approved)
				}) {
					return fmt.Errorf("Sandbox component cannot request an unapproved identity projection")
				}
			}
		case volume.EmptyDir != nil:
			if volume.EmptyDir.Medium == corev1.StorageMediumDefault &&
				(scratchLimit.Sign() <= 0 || volume.EmptyDir.SizeLimit == nil ||
					volume.EmptyDir.SizeLimit.Sign() <= 0 || volume.EmptyDir.SizeLimit.Cmp(scratchLimit) > 0) {
				return fmt.Errorf("Sandbox temporary volume %q requires a positive sizeLimit no greater than the selected scratch limit", volume.Name)
			}
		case volume.ConfigMap != nil, volume.Secret != nil, volume.DownwardAPI != nil, volume.Image != nil:
		default:
			return fmt.Errorf("Sandbox component volume source is not approved")
		}
	}
	return nil
}

func ReconciledObjectMatches(actual, desired *unstructured.Unstructured) bool {
	if actual == nil || desired == nil || actual.GetUID() == "" || !actual.GetDeletionTimestamp().IsZero() ||
		!equality.Semantic.DeepEqual(actual.GetOwnerReferences(), desired.GetOwnerReferences()) {
		return false
	}
	for key, value := range desired.Object {
		if key == "spec" && desired.GetKind() == "Service" {
			want, _, wantErr := unstructured.NestedMap(desired.Object, "spec")
			got, _, gotErr := unstructured.NestedMap(actual.Object, "spec")
			if wantErr != nil || gotErr != nil {
				return false
			}
			for name, field := range want {
				if !reflect.DeepEqual(field, got[name]) {
					return false
				}
			}
			continue
		}
		if key != "metadata" && !reflect.DeepEqual(actual.Object[key], value) {
			return false
		}
	}
	return true
}

// ValidateRenderedPod checks the current controller-approved template without
// fixing helper names or permanently consuming one Pod UID.
func ValidateRenderedPod(pod *corev1.Pod, workload RenderedWorkload, owner metav1.OwnerReference, revision string) error {
	if pod == nil || !equality.Semantic.DeepEqual(pod.OwnerReferences, []metav1.OwnerReference{owner}) ||
		!equalJSON(pod.Annotations, workload.Template.Annotations) ||
		!slices.Equal(pod.Finalizers, workload.Template.Finalizers) {
		return &PodMismatchError{[]string{"metadata"}}
	}
	labels := maps.Clone(workload.Template.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	want := workload.Template.Spec.DeepCopy()
	switch workload.Kind {
	case "StatefulSet":
		name := workload.Identity.Name + "-0"
		if pod.Name != name {
			return &PodMismatchError{[]string{"metadata.name"}}
		}
		labels["statefulset.kubernetes.io/pod-name"], labels["apps.kubernetes.io/pod-index"] = name, "0"
		if revision == "" {
			return fmt.Errorf("Sandbox StatefulSet controller revision is unavailable")
		}
		labels["controller-revision-hash"] = revision
		want.Hostname, want.Subdomain = name, workload.Identity.Name
	case "Deployment":
		if revision == "" {
			return fmt.Errorf("Sandbox ReplicaSet revision is unavailable")
		}
		labels["pod-template-hash"] = revision
	default:
		return fmt.Errorf("unsupported rendered workload kind")
	}
	if !equalJSON(pod.Labels, labels) {
		return &PodMismatchError{[]string{"metadata.labels"}}
	}
	want.Overhead = workload.Overhead.DeepCopy()
	got := pod.Spec.DeepCopy()
	if err := defaultPod(want); err != nil {
		return err
	}
	if err := defaultPod(got); err != nil {
		return err
	}
	if !equality.Semantic.DeepEqual(want, got) {
		var fields []string
		podDifferencePaths(reflect.ValueOf(want), reflect.ValueOf(got), "spec", &fields)
		return &PodMismatchError{fields}
	}
	return nil
}

func (b *Bundle) ValidateReconciledTemplate(template *corev1.PodTemplateSpec, image string, memoryEnabled bool) error {
	spec := template.Spec
	if !b.input.Reconciled || len(spec.Containers) == 0 || spec.Containers[0].Image != image ||
		ptr.Deref(spec.RuntimeClassName, "") != b.input.RuntimeClass.Name ||
		spec.HostNetwork || spec.HostPID || spec.HostIPC || ptr.Deref(spec.ShareProcessNamespace, false) ||
		ptr.Deref(spec.AutomountServiceAccountToken, true) || spec.SecurityContext == nil || spec.DNSPolicy != corev1.DNSNone ||
		!equality.Semantic.DeepEqual(spec.DNSConfig, b.DNSConfig()) ||
		spec.ServiceAccountName != b.input.Controls.ServiceAccountName ||
		!equality.Semantic.DeepEqual(spec.Containers[0].Resources, b.agentResources()) {
		return fmt.Errorf("reconciled Sandbox runtime, DNS, identity or resources differ from required controls")
	}
	foundHome := false
	for _, volume := range spec.Volumes {
		if volume.HostPath != nil {
			return fmt.Errorf("reconciled Sandbox cannot mount host paths")
		}
		if volume.Name == "data" {
			foundHome = volume.PersistentVolumeClaim != nil &&
				volume.PersistentVolumeClaim.ClaimName == b.input.Home.Name && !volume.PersistentVolumeClaim.ReadOnly
		}
	}
	if !foundHome {
		return fmt.Errorf("reconciled Sandbox must retain its owned HOME")
	}
	if err := ValidateReconciledVolumes(spec, []string{b.input.Home.Name, skillsIdentity(b.input.Skills).Name}, b.input.Controls.Volumes,
		b.input.Resolution.Sandbox.Storage.Scratch.SizeLimit); err != nil {
		return err
	}
	var pullSecrets []corev1.LocalObjectReference
	if b.input.ImagePullSecret != nil {
		pullSecrets = []corev1.LocalObjectReference{{Name: b.input.ImagePullSecret.Name}}
	}
	if !slices.Equal(spec.ImagePullSecrets, pullSecrets) {
		return fmt.Errorf("reconciled Sandbox image pulls differ from the approved Secret binding")
	}
	// Kubelet pull credentials must not authorize container environment or mounts.
	runtimeSources := spec
	runtimeSources.ImagePullSecrets = nil
	for _, source := range PodSourceReferences(runtimeSources, b.input.Namespace.Name) {
		if source.Kind == "Secret" && !slices.Contains(b.input.AuthorizedSecrets, source.Name) {
			return fmt.Errorf("reconciled Agent cannot consume an unauthorized Secret")
		}
		if source.Kind == "PersistentVolumeClaim" && source.Name != b.input.Home.Name && source.Name != skillsIdentity(b.input.Skills).Name {
			return fmt.Errorf("reconciled Agent cannot mount unapproved persistent storage")
		}
	}
	for _, group := range [][]corev1.Container{spec.Containers, spec.InitContainers} {
		for _, container := range group {
			sc := container.SecurityContext
			if sc == nil || ptr.Deref(sc.Privileged, false) || ptr.Deref(sc.AllowPrivilegeEscalation, true) ||
				!ptr.Deref(sc.RunAsNonRoot, ptr.Deref(spec.SecurityContext.RunAsNonRoot, false)) ||
				ptr.Deref(sc.RunAsUser, ptr.Deref(spec.SecurityContext.RunAsUser, 0)) <= 0 ||
				sc.Capabilities == nil || len(sc.Capabilities.Add) != 0 || !slices.Contains(sc.Capabilities.Drop, corev1.Capability("ALL")) ||
				!ptr.Deref(sc.ReadOnlyRootFilesystem, false) && *b.input.Resolution.AgentPolicy.Security.ReadOnlyRootFilesystem {
				return fmt.Errorf("reconciled Sandbox container violates the selected security constraints")
			}
		}
	}
	if b.input.AgentType == "custom" {
		if err := b.validateCustomReconciledTemplate(&spec, memoryEnabled); err != nil {
			return err
		}
	}
	return nil
}

func (b *Bundle) validateCustomReconciledTemplate(spec *corev1.PodSpec, memoryEnabled bool) error {
	custom := b.input.CustomAgent
	context := b.input.Context
	if custom == nil || context == nil || b.input.GatewayKey == nil || len(spec.Containers) == 0 || len(spec.InitContainers) == 0 {
		return fmt.Errorf("reconciled Custom runtime lacks its approved Harness or AI Gateway identity")
	}
	main := spec.Containers[0]
	var init *corev1.Container
	for index := range spec.InitContainers {
		if spec.InitContainers[index].Name == "init-home-data" {
			init = &spec.InitContainers[index]
			break
		}
	}
	if init == nil || main.Name != "cetirun" || main.Image != custom.Image || len(main.Command) != 0 || len(main.Args) != 0 ||
		init.Image != custom.Image ||
		!slices.Equal(init.Command, []string{"/bin/sh", "/etc/cetirun/custom-init-home.sh"}) || len(init.Args) != 0 {
		return fmt.Errorf("reconciled Custom runtime rewrites an approved image entrypoint or HOME initializer")
	}
	for _, expected := range custom.Environment {
		matched := false
		for _, actual := range main.Env {
			want := corev1.EnvVar{Name: expected.Name, Value: expected.Value}
			if expected.CredentialSlot != "" {
				if expected.CredentialSlot != "model" {
					return fmt.Errorf("reconciled Custom runtime contains an unsupported credential slot")
				}
				want.Value = ""
				want.ValueFrom = &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: b.input.GatewayKey.Name}, Key: "api-key",
				}}
			}
			matched = matched || equality.Semantic.DeepEqual(actual, want)
		}
		if !matched {
			return fmt.Errorf("reconciled Custom runtime is missing a Harness or typed AI Gateway environment value")
		}
	}
	if !slices.Equal(main.Ports, customContainerPorts(custom.Ports)) {
		return fmt.Errorf("reconciled Custom runtime ports differ from the Harness contract")
	}
	if custom.HTTP == nil {
		if main.StartupProbe != nil || main.ReadinessProbe != nil || main.LivenessProbe != nil {
			return fmt.Errorf("headless Custom runtime cannot define HTTP probes")
		}
	} else if !customHTTPProbesMatch(main, custom.HTTP) {
		return fmt.Errorf("reconciled Custom runtime probes differ from the Harness contract")
	}
	// Custom always materializes a bounded canonical view. Besides keeping the
	// Team snapshot immutable, this guarantees an empty skills directory for
	// Harnesses when Memory and shared skills are both disabled.
	mainMounts := []corev1.VolumeMount{
		{Name: "data", MountPath: custom.Home}, {Name: "tmp", MountPath: "/tmp"},
		{Name: "config", MountPath: "/etc/cetirun", ReadOnly: true},
	}
	mainMounts = append(mainMounts, corev1.VolumeMount{Name: "context-view", MountPath: custom.Home + "/.agents", SubPath: "current", ReadOnly: true})
	if memoryEnabled {
		mainMounts = append(mainMounts, runtimebindings.MemoryMounts()...)
	}
	mainMounts = append(mainMounts, b.input.Controls.VolumeMounts...)
	initMounts := []corev1.VolumeMount{
		{Name: "data", MountPath: "/home-data"}, {Name: "tmp", MountPath: "/tmp"},
		{Name: "config", MountPath: "/etc/cetirun", ReadOnly: true},
		{Name: "context", MountPath: "/etc/cetirun-context", ReadOnly: true},
	}
	initEnv := []corev1.EnvVar{
		{Name: "SOURCE_HOME", Value: custom.Home}, {Name: "TARGET_HOME", Value: "/home-data"},
		{Name: "CETIRUN_INIT_IMAGE", Value: custom.Image}, {Name: "CETIRUN_INIT_COMMON", Value: "/etc/cetirun/init-home-common.sh"},
		{Name: "CETIRUN_CONTEXT_DIR", Value: "/etc/cetirun-context"},
	}
	initMounts = append(initMounts, corev1.VolumeMount{Name: "context-view", MountPath: "/etc/cetirun-context-view"})
	initEnv = append(initEnv, corev1.EnvVar{Name: "CETIRUN_CONTEXT_VIEW", Value: "/etc/cetirun-context-view"})
	if b.input.SkillsMode != SkillsModeEmpty {
		initMounts = append(initMounts, corev1.VolumeMount{Name: "shared-skills", MountPath: "/etc/cetirun-shared-skills", ReadOnly: true})
		initEnv = append(initEnv, corev1.EnvVar{Name: "CETIRUN_SHARED_SKILLS_DIR", Value: "/etc/cetirun-shared-skills"})
	}
	if memoryEnabled {
		initMounts = append(initMounts, runtimebindings.MemoryMounts()...)
		initEnv = append(initEnv, corev1.EnvVar{Name: "CETIRUN_MEMORY_ASSETS", Value: runtimebindings.MemoryAssetsDir})
	}
	initMounts = append(initMounts, b.input.Controls.VolumeMounts...)
	if !equalMounts(main.VolumeMounts, mainMounts) || !equalMounts(init.VolumeMounts, initMounts) ||
		!equality.Semantic.DeepEqual(init.Env, initEnv) {
		return fmt.Errorf("reconciled Custom runtime mounts differ from the common Sandbox contract")
	}
	contextVolume := corev1.Volume{Name: "context", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
		LocalObjectReference: corev1.LocalObjectReference{Name: context.Name}, DefaultMode: ptr.To[int32](0444),
		Items: []corev1.KeyToPath{{Key: "AGENTS.md", Path: "AGENTS.md"}},
	}}}
	if !hasExactVolume(*spec, contextVolume) {
		return fmt.Errorf("reconciled Custom runtime context source differs from the controller snapshot")
	}
	if !hasExactVolume(*spec, corev1.Volume{Name: "context-view", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{
		SizeLimit: ptr.To(b.input.Resolution.Sandbox.Storage.Scratch.SizeLimit),
	}}}) {
		return fmt.Errorf("reconciled Custom runtime canonical context view differs from the Sandbox scratch limit")
	}
	return nil
}

func hasExactVolume(spec corev1.PodSpec, expected corev1.Volume) bool {
	return slices.ContainsFunc(spec.Volumes, func(actual corev1.Volume) bool {
		return equality.Semantic.DeepEqual(actual, expected)
	})
}

func customContainerPorts(ports []CustomPort) []corev1.ContainerPort {
	result := make([]corev1.ContainerPort, 0, len(ports))
	for _, port := range ports {
		result = append(result, corev1.ContainerPort{Name: port.Name, ContainerPort: port.Number, Protocol: corev1.ProtocolTCP})
	}
	return result
}

func customHTTPProbesMatch(container corev1.Container, http *CustomHTTP) bool {
	return container.LivenessProbe == nil && container.StartupProbe != nil && container.ReadinessProbe != nil &&
		container.StartupProbe.HTTPGet != nil && container.ReadinessProbe.HTTPGet != nil &&
		container.StartupProbe.HTTPGet.Path == http.StartupPath && container.ReadinessProbe.HTTPGet.Path == http.HealthPath &&
		container.StartupProbe.HTTPGet.Port.StrVal == http.PortName && container.ReadinessProbe.HTTPGet.Port.StrVal == http.PortName &&
		container.StartupProbe.PeriodSeconds == 5 && container.StartupProbe.FailureThreshold == 60 &&
		container.ReadinessProbe.PeriodSeconds == 10
}

func equalMounts(actual, expected []corev1.VolumeMount) bool {
	actual, expected = slices.Clone(actual), slices.Clone(expected)
	sortMounts(actual)
	sortMounts(expected)
	return equality.Semantic.DeepEqual(actual, expected)
}
