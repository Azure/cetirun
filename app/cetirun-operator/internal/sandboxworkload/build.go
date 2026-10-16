// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxworkload

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"path"
	"slices"
	"strings"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"
)

func Build(input Input) (*Bundle, error) {
	if input.Resolution == nil || input.Resolution.Revision == "" {
		return nil, fmt.Errorf("a shared Sandbox resolution is required")
	}
	binding := input.Resolution.Binding
	if binding.AgentUID == "" || binding.TeamUID == "" || binding.SandboxUID == "" || binding.AgentPolicyUID == "" {
		return nil, fmt.Errorf("the resolved UID binding is incomplete")
	}
	if input.AgentType != "openclaw" && input.AgentType != "hermes" {
		return nil, fmt.Errorf("policy-bound %q rendering is unsupported; legacy fallback is forbidden", input.AgentType)
	}
	for _, name := range []string{input.AgentName, input.Namespace.Name, input.WorkloadName, input.ReleaseName} {
		if name == "" || len(validation.IsDNS1123Subdomain(name)) != 0 {
			return nil, fmt.Errorf("Sandbox workload names must be explicit DNS names")
		}
	}
	if input.Namespace.UID == "" || input.AgentGeneration < 1 {
		return nil, fmt.Errorf("persisted Agent generation and namespace UID are required")
	}
	if errs := sandbox.CheckCompatibility(input.Resolution.Sandbox, input.Resolution.AgentPolicy, input.Resolution.Capabilities); len(errs) != 0 {
		return nil, errs.ToAggregate()
	}
	if input.RuntimeClass == nil || input.RuntimeClass.UID == "" || !input.RuntimeClass.DeletionTimestamp.IsZero() ||
		input.RuntimeClass.Name != input.Resolution.Capabilities.RuntimeClassName {
		return nil, fmt.Errorf("the current registered RuntimeClass is required")
	}
	if input.RuntimeClass.Overhead != nil {
		for name, value := range input.RuntimeClass.Overhead.PodFixed {
			if (name != corev1.ResourceCPU && name != corev1.ResourceMemory) || value.Sign() < 0 {
				return nil, fmt.Errorf("unsupported RuntimeClass overhead")
			}
		}
	}
	if input.AgentType != "openclaw" && input.Helpers.Doctor {
		return nil, fmt.Errorf("doctor is only an approved OpenClaw helper")
	}
	if err := validateHome(input); err != nil {
		return nil, err
	}
	if err := input.SkillsMode.ValidateIdentity(skillsIdentity(input.Skills)); err != nil {
		return nil, err
	}
	if input.SkillsMode == SkillsModeEmpty {
		if input.Skills != nil || input.GatewayKey == nil || (input.AgentType != "openclaw" && !input.Reconciled) {
			return nil, fmt.Errorf("empty skills require native Gateway inputs without a skills PVC")
		}
	} else if input.Skills.Namespace != input.Namespace.Name ||
		input.Skills.Name == input.Home.Name || !input.Skills.DeletionTimestamp.IsZero() ||
		input.Skills.Status.Phase == corev1.ClaimLost || ptr.Deref(input.Skills.Spec.VolumeMode, corev1.PersistentVolumeFilesystem) != corev1.PersistentVolumeFilesystem {
		return nil, fmt.Errorf("a distinct, current filesystem skills PVC is required")
	}
	if err := validateControls(input); err != nil {
		return nil, err
	}
	if len(input.AuthorizedSecrets) == 0 {
		return nil, fmt.Errorf("an explicitly authorized native runtime Secret is required")
	}
	for _, secret := range input.AuthorizedSecrets {
		if len(validation.IsDNS1123Subdomain(secret)) != 0 {
			return nil, fmt.Errorf("invalid authorized Secret name")
		}
	}
	if key := input.GatewayKey; key != nil && ((input.AgentType != "openclaw" && !input.Reconciled) || key.UID == "" ||
		len(validation.IsDNS1123Subdomain(key.Name)) != 0 || !slices.Contains(input.AuthorizedSecrets, key.Name)) {
		return nil, fmt.Errorf("native Gateway rendering requires an authorized OpenClaw key identity")
	}
	copy, err := clone(input)
	if err != nil {
		return nil, err
	}
	copy.Resolution.Revision = input.Resolution.Revision
	// The resolver normalizes defaults; retain explicit false for this one
	// optional security control rather than treating it as an absent value.
	security, errs := sandbox.NormalizeAgentPolicy(copy.Resolution.AgentPolicy)
	if len(errs) != 0 {
		return nil, errs.ToAggregate()
	}
	copy.Resolution.AgentPolicy = security
	b := &Bundle{input: copy}
	spec := corev1.PodSpec{Containers: []corev1.Container{{Resources: b.agentResources()}}}
	if input.Helpers.Logger {
		spec.Containers = append(spec.Containers, corev1.Container{Resources: b.helperResources()})
	}
	if input.Helpers.Desktop {
		spec.Containers = append(spec.Containers, corev1.Container{Resources: b.helperResources()})
	}
	spec.InitContainers = []corev1.Container{{Resources: b.helperResources()}}
	if input.Helpers.Doctor {
		spec.InitContainers = append(spec.InitContainers, corev1.Container{Resources: b.helperResources()})
	}
	if input.GatewayKey != nil {
		spec.InitContainers = append(spec.InitContainers,
			corev1.Container{Resources: b.helperResources()}, corev1.Container{Resources: b.helperResources()})
	}
	b.accounting = AccountResources(spec, b.Overhead())
	return b, nil
}

func validateHome(input Input) error {
	home := input.Home
	if home == nil || home.UID == "" || home.Namespace != input.Namespace.Name ||
		!home.DeletionTimestamp.IsZero() || home.Status.Phase == corev1.ClaimLost ||
		home.Annotations[AgentUIDAnnotation] != string(input.Resolution.Binding.AgentUID) {
		return fmt.Errorf("an existing HOME PVC with confirmed Agent UID ownership is required")
	}
	for _, owner := range home.OwnerReferences {
		if owner.APIVersion != api.GroupVersion.String() || owner.Kind != "Agent" ||
			owner.Name != input.AgentName || owner.UID != input.Resolution.Binding.AgentUID {
			return fmt.Errorf("HOME has a foreign or workload-lifetime owner")
		}
	}
	wanted := input.Resolution.Sandbox.Storage.Home
	size := home.Spec.Resources.Requests[corev1.ResourceStorage]
	if ptr.Deref(home.Spec.StorageClassName, "") != wanted.StorageClassName ||
		!slices.Equal(home.Spec.AccessModes, []corev1.PersistentVolumeAccessMode{wanted.AccessMode}) ||
		size.Cmp(wanted.Size) != 0 || ptr.Deref(home.Spec.VolumeMode, corev1.PersistentVolumeFilesystem) != corev1.PersistentVolumeFilesystem {
		return fmt.Errorf("%w: changing or resizing the existing HOME claim is not a render operation", ErrStorageTransitionRequired)
	}
	return nil
}

func validateControls(input Input) error {
	c := input.Controls
	if c.Revision == "" || c.ResolutionRevision != input.Resolution.Revision || c.Binding != input.Resolution.Binding ||
		c.ServiceAccountName == "" || len(validation.IsDNS1123Subdomain(c.ServiceAccountName)) != 0 {
		return fmt.Errorf("compiler controls must match the shared resolution and bound UIDs")
	}
	if len(c.PodLabels) != 1 || c.PodLabels[sandboxpolicy.AgentUIDLabel] != string(c.Binding.AgentUID) {
		return fmt.Errorf("compiler workload selector must bind the Agent UID")
	}
	if c.IngressMode != input.Resolution.AgentPolicy.Network.Ingress.Mode {
		return fmt.Errorf("compiler ingress mode must match the shared resolution")
	}
	if err := validateDNSIPs(c.DNSIPs); err != nil {
		return err
	}
	nativeIngress := input.GatewayKey != nil && (input.AgentType == "openclaw" || input.Reconciled) && c.IngressMode == api.AgentPolicyIngressAuthenticated
	if nativeIngress != (len(c.TrustedProxyIPs) != 0) {
		return fmt.Errorf("native authenticated ingress requires explicit compiler proxy IPs; other modes cannot grant proxy trust")
	}
	for i, value := range c.TrustedProxyIPs {
		address, err := netip.ParseAddr(value)
		if err != nil || address.Is4In6() || address.Zone() != "" || address.String() != value ||
			!address.IsGlobalUnicast() || (i > 0 && c.TrustedProxyIPs[i-1] >= value) {
			return fmt.Errorf("compiler proxy IPs must be sorted, distinct canonical unicast addresses, not CIDRs")
		}
	}
	volumes := map[string]bool{}
	for _, volume := range c.Volumes {
		if volume.Name == "" || volumes[volume.Name] || slices.Contains([]string{"data", "tmp", "tmp-desktop", "config", "secrets", "shared-skills", "empty-skills"}, volume.Name) {
			return fmt.Errorf("identity volumes conflict with protected mounts")
		}
		volumes[volume.Name] = true
		source := volume.VolumeSource.DeepCopy()
		source.Projected, source.ConfigMap = nil, nil
		if !equality.Semantic.DeepEqual(*source, corev1.VolumeSource{}) ||
			(volume.Projected == nil && volume.ConfigMap == nil) || (volume.Projected != nil && volume.ConfigMap != nil) {
			return fmt.Errorf("identity volumes must be independent projections or compiler-owned configuration")
		}
		if volume.Projected != nil {
			if len(volume.Projected.Sources) == 0 {
				return fmt.Errorf("identity projection is empty")
			}
			for _, projection := range volume.Projected.Sources {
				if projection.ServiceAccountToken != nil {
					token := projection.ServiceAccountToken
					if token.Audience == "" || !relativePath(token.Path) ||
						ptr.Deref(token.ExpirationSeconds, 0) < 600 || ptr.Deref(token.ExpirationSeconds, 0) > 3600 {
						return fmt.Errorf("identity tokens need a bounded lifetime, audience and relative path")
					}
				}
			}
		}
	}
	mounted := map[string]bool{}
	var paths []string
	for _, mount := range c.VolumeMounts {
		if !volumes[mount.Name] || mounted[mount.Name] || !mount.ReadOnly || mount.SubPath != "" || mount.SubPathExpr != "" ||
			mount.MountPropagation != nil || !path.IsAbs(mount.MountPath) || path.Clean(mount.MountPath) != mount.MountPath {
			return fmt.Errorf("identity mounts must be distinct, read-only projections")
		}
		for _, reserved := range append([]string{HomePath, "/tmp", "/etc/cetirun", "/etc/cetirun-secrets"}, paths...) {
			if overlappingPaths(mount.MountPath, reserved) {
				return fmt.Errorf("identity mount overlaps a protected path")
			}
		}
		paths = append(paths, mount.MountPath)
		mounted[mount.Name] = true
	}
	if len(mounted) != len(volumes) {
		return fmt.Errorf("all identity volumes require explicit main-container mounts")
	}
	names := map[string]bool{}
	for _, env := range c.Environment {
		if names[env.Name] || len(validation.IsEnvVarName(env.Name)) != 0 || env.ValueFrom != nil ||
			!(strings.HasPrefix(env.Name, "AZURE_") || strings.HasPrefix(env.Name, "KUBERNETES_") || env.Name == "KUBECONFIG") {
			return fmt.Errorf("invalid or conflicting compiler identity environment")
		}
		names[env.Name] = true
	}
	return validateObjects(input)
}

func validateDNSIPs(ips []string) error {
	if len(ips) == 0 || len(ips) > 3 {
		return fmt.Errorf("compiler DNS requires one to three explicit server IPs")
	}
	seen := map[string]bool{}
	for _, value := range ips {
		address, err := netip.ParseAddr(value)
		if err != nil || address.Zone() != "" || address.String() != value || seen[value] {
			return fmt.Errorf("compiler DNS server IPs must be canonical, distinct addresses")
		}
		seen[value] = true
	}
	return nil
}

func relativePath(value string) bool {
	return value != "" && !path.IsAbs(value) && value != "." && value != ".." &&
		path.Clean(value) == value && !strings.HasPrefix(value, "../")
}

func overlappingPaths(a, b string) bool {
	return a == b || strings.HasPrefix(a, strings.TrimSuffix(b, "/")+"/") || strings.HasPrefix(b, strings.TrimSuffix(a, "/")+"/")
}

func clone[T any](value T) (T, error) {
	var out T
	raw, err := json.Marshal(value)
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}

func (b *Bundle) agentResources() corev1.ResourceRequirements {
	r := b.input.Resolution.Sandbox.Resources.Agent
	return corev1.ResourceRequirements{Requests: r.Requests.DeepCopy(), Limits: r.Limits.DeepCopy()}
}

func (b *Bundle) helperResources() corev1.ResourceRequirements {
	r := b.input.Resolution.Sandbox.Resources.Helpers
	return corev1.ResourceRequirements{Requests: r.Requests.DeepCopy(), Limits: r.Limits.DeepCopy()}
}

func (b *Bundle) Overhead() corev1.ResourceList {
	if b.input.RuntimeClass.Overhead == nil || len(b.input.RuntimeClass.Overhead.PodFixed) == 0 {
		return nil
	}
	return b.input.RuntimeClass.Overhead.PodFixed.DeepCopy()
}

func (b *Bundle) Accounting() Accounting {
	return Accounting{Pod: *b.accounting.Pod.DeepCopy(), Effective: *b.accounting.Effective.DeepCopy()}
}

func (b *Bundle) Identity() Input {
	out, _ := clone(b.input)
	out.Resolution.Revision = b.input.Resolution.Revision
	return out
}

// CheckStatefulSetTransition deliberately does not migrate immutable claim
// templates or certify writer exclusion, even when replicas is already zero.
func (b *Bundle) CheckStatefulSetTransition(existing *appsv1.StatefulSet) error {
	if existing == nil {
		return nil
	}
	if existing.Name != b.input.WorkloadName || existing.Namespace != b.input.Namespace.Name ||
		existing.UID == "" || existing.Annotations[AgentUIDAnnotation] != string(b.input.Resolution.Binding.AgentUID) ||
		!equality.Semantic.DeepEqual(existing.OwnerReferences, []metav1.OwnerReference{AgentOwner(b.input.AgentName, b.input.Resolution.Binding.AgentUID)}) {
		return fmt.Errorf("existing StatefulSet ownership is not confirmed")
	}
	if len(existing.Spec.VolumeClaimTemplates) != 0 {
		return ErrStorageTransitionRequired
	}
	for _, volume := range existing.Spec.Template.Spec.Volumes {
		if volume.Name == "data" && volume.PersistentVolumeClaim != nil &&
			volume.PersistentVolumeClaim.ClaimName == b.input.Home.Name && !volume.PersistentVolumeClaim.ReadOnly {
			return nil
		}
	}
	return ErrStorageTransitionRequired
}
