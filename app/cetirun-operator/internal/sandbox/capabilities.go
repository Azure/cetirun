// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandbox

import (
	"fmt"
	"net/netip"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

const ProfileNativeSingleStart = "native-single-start"

// Capabilities is trusted platform registration, not an Agent-controlled input.
// An empty Profile retains the qualified contract. The native profile declares
// only bounded Kubernetes support, not external observation or physical fencing.
type Capabilities struct {
	Profile                           string                              `json:"profile,omitempty"`
	RuntimeClassName                  string                              `json:"runtimeClassName"`
	Revision                          string                              `json:"revision"`
	HostResourceLimits                bool                                `json:"hostResourceLimits"`
	PerContainerResourceLimits        bool                                `json:"perContainerResourceLimits"`
	RequirePerContainerResourceLimits bool                                `json:"requirePerContainerResourceLimits"`
	ReadOnlyRootFilesystem            bool                                `json:"readOnlyRootFilesystem"`
	HomeAccessModes                   []corev1.PersistentVolumeAccessMode `json:"homeAccessModes"`
	IPFamilies                        []corev1.IPFamily                   `json:"ipFamilies"`
	Protocols                         []corev1.Protocol                   `json:"protocols"`
	GuestExternalNetwork              bool                                `json:"guestExternalNetwork"`
	DenyPrecedence                    bool                                `json:"denyPrecedence"`
	NodeTraffic                       bool                                `json:"nodeTraffic"`
	DNSIPRestrictions                 bool                                `json:"dnsIPRestrictions"`
	BlockedPorts                      bool                                `json:"blockedPorts"`
	BlockedFQDNs                      bool                                `json:"blockedFQDNs"`
	AuthenticatedIngress              bool                                `json:"authenticatedIngress"`
	DisabledIngress                   bool                                `json:"disabledIngress"`
	MutualTLS                         bool                                `json:"mutualTLS"`
	KubernetesAccess                  bool                                `json:"kubernetesAccess"`
	EntraID                           bool                                `json:"entraID"`
	EnforcementObservation            bool                                `json:"enforcementObservation"`
	RestrictiveFailure                bool                                `json:"restrictiveFailure"`
}

type Registration struct {
	Capabilities Capabilities
	Available    bool
}

// CheckExecutionProfile prevents a native-only registration from authorizing
// the default held/externally observed execution path.
func CheckExecutionProfile(capabilities Capabilities, nativeExecution bool) error {
	if errs := CheckCapabilityProfile(capabilities); len(errs) != 0 {
		return errs.ToAggregate()
	}
	if capabilities.Profile == ProfileNativeSingleStart && !nativeExecution {
		return fmt.Errorf("registration profile %q requires explicit nativeExecution", ProfileNativeSingleStart)
	}
	return nil
}

func CheckAgentProfile(capabilities Capabilities, agentType cetirunv1alpha1.AgentType, class cetirunv1alpha1.AgentPolicyClass) error {
	if errs := CheckCapabilityProfile(capabilities); len(errs) != 0 {
		return errs.ToAggregate()
	}
	if capabilities.Profile == ProfileNativeSingleStart &&
		(class != cetirunv1alpha1.AgentPolicyClassNormal || agentType != cetirunv1alpha1.AgentTypeOpenClaw) {
		return fmt.Errorf("native single-start requires a normal OpenClaw Agent")
	}
	return nil
}

func CheckCapabilityProfile(capabilities Capabilities) field.ErrorList {
	root := field.NewPath("registration")
	switch capabilities.Profile {
	case "":
		return nil
	case ProfileNativeSingleStart:
	default:
		return field.ErrorList{field.NotSupported(root.Child("profile"), capabilities.Profile, []string{"", ProfileNativeSingleStart})}
	}
	var errs field.ErrorList
	if !capabilities.HostResourceLimits {
		errs = append(errs, field.Required(root.Child("hostResourceLimits"), "native runtime resource limits must be supported"))
	}
	if !slices.Equal(capabilities.HomeAccessModes, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOncePod}) {
		errs = append(errs, field.Invalid(root.Child("homeAccessModes"), capabilities.HomeAccessModes, "native single-start requires only ReadWriteOncePod HOME"))
	}
	for _, control := range []struct {
		name     string
		declared bool
	}{
		{"perContainerResourceLimits", capabilities.PerContainerResourceLimits},
		{"requirePerContainerResourceLimits", capabilities.RequirePerContainerResourceLimits},
		{"blockedPorts", capabilities.BlockedPorts},
		{"blockedFQDNs", capabilities.BlockedFQDNs},
		{"mutualTLS", capabilities.MutualTLS},
		{"kubernetesAccess", capabilities.KubernetesAccess},
		{"entraID", capabilities.EntraID},
		{"enforcementObservation", capabilities.EnforcementObservation},
		{"restrictiveFailure", capabilities.RestrictiveFailure},
	} {
		if control.declared {
			errs = append(errs, field.Forbidden(root.Child(control.name), "not a guarantee of the native single-start profile"))
		}
	}
	return errs
}

func CheckNetworkCapabilities(capabilities Capabilities) field.ErrorList {
	errs := CheckCapabilityProfile(capabilities)
	for _, requirement := range []struct {
		name          string
		supported     bool
		qualifiedOnly bool
	}{
		{"guestExternalNetwork", capabilities.GuestExternalNetwork, false},
		{"denyPrecedence", capabilities.DenyPrecedence, false},
		{"nodeTraffic", capabilities.NodeTraffic, false},
		{"dnsIPRestrictions", capabilities.DNSIPRestrictions, false},
		{"enforcementObservation", capabilities.EnforcementObservation, true},
		{"restrictiveFailure", capabilities.RestrictiveFailure, true},
	} {
		if requirement.qualifiedOnly && capabilities.Profile == ProfileNativeSingleStart {
			continue
		}
		if !requirement.supported {
			errs = append(errs, field.Required(field.NewPath("registration", requirement.name), "mandatory control is not supported by the selected profile"))
		}
	}
	return errs
}

// CheckNativePolicy bounds configuration support independently of declarations
// for optional controls. It does not authorize an Agent or release a hold.
func CheckNativePolicy(policy cetirunv1alpha1.AgentPolicySpec) field.ErrorList {
	root := field.NewPath("spec")
	var errs field.ErrorList
	if !slices.Equal(policy.Harnesses.Allowed, []cetirunv1alpha1.AgentType{cetirunv1alpha1.AgentTypeOpenClaw}) {
		errs = append(errs, field.Forbidden(root.Child("harnesses", "allowed"), "native single-start supports only OpenClaw"))
	}
	for _, control := range []struct {
		path      *field.Path
		requested bool
	}{
		{root.Child("network", "blockedCIDRs"), len(policy.Network.BlockedCIDRs) != 0},
		{root.Child("network", "blockedFQDNs"), len(policy.Network.BlockedFQDNs) != 0},
		{root.Child("network", "blockedPorts"), len(policy.Network.BlockedPorts) != 0},
		{root.Child("network", "allowedInternalCIDRs"), len(policy.Network.AllowedInternalCIDRs) != 0},
		{root.Child("network", "ingress", "requireMTLS"), policy.Network.Ingress.RequireMTLS},
		{root.Child("kubernetesAccess"), policy.KubernetesAccess != nil},
		{root.Child("entraID", "enabled"), policy.EntraID.Enabled},
	} {
		if control.requested {
			errs = append(errs, field.Forbidden(control.path, "unsupported by the native single-start profile"))
		}
	}
	return errs
}

// CheckCompatibility checks declared support, not runtime health or enforcement.
func CheckCompatibility(sandbox cetirunv1alpha1.SandboxSpec, policy cetirunv1alpha1.AgentPolicySpec, capabilities Capabilities) field.ErrorList {
	sandbox, errs := NormalizeSandbox(sandbox)
	policy, policyErrs := NormalizeAgentPolicy(policy)
	errs = append(errs, policyErrs...)
	if len(errs) != 0 {
		return errs
	}

	root := field.NewPath("registration")
	if problems := validation.IsDNS1123Subdomain(capabilities.RuntimeClassName); len(problems) != 0 {
		errs = append(errs, field.Invalid(root.Child("runtimeClassName"), capabilities.RuntimeClassName, "must name a registered RuntimeClass"))
	}
	if capabilities.Revision == "" {
		errs = append(errs, field.Required(root.Child("revision"), "a platform revision is required"))
	}
	errs = append(errs, CheckNetworkCapabilities(capabilities)...)
	if !capabilities.HostResourceLimits {
		errs = append(errs, field.Required(root.Child("hostResourceLimits"), "mandatory control is not qualified"))
	}
	if capabilities.Profile == ProfileNativeSingleStart {
		if sandbox.Type != cetirunv1alpha1.SandboxKata || sandbox.Storage.Home.AccessMode != corev1.ReadWriteOncePod {
			errs = append(errs, field.Forbidden(field.NewPath("spec"), "native single-start requires Kata and ReadWriteOncePod HOME"))
		}
		errs = append(errs, CheckNativePolicy(policy)...)
	}
	if capabilities.RequirePerContainerResourceLimits && !capabilities.PerContainerResourceLimits {
		errs = append(errs, field.Required(root.Child("perContainerResourceLimits"), "the platform requires independent container guarantees"))
	}
	if !slices.Contains(capabilities.HomeAccessModes, sandbox.Storage.Home.AccessMode) {
		errs = append(errs, field.Invalid(field.NewPath("spec", "storage", "home", "accessMode"), sandbox.Storage.Home.AccessMode, "writer exclusion is not qualified for this access mode"))
	}
	if *policy.Security.ReadOnlyRootFilesystem && !capabilities.ReadOnlyRootFilesystem {
		errs = append(errs, field.Required(root.Child("readOnlyRootFilesystem"), "read-only root filesystems are not qualified"))
	}
	if len(capabilities.IPFamilies) == 0 {
		errs = append(errs, field.Required(root.Child("ipFamilies"), "at least one qualified address family is required"))
	}
	for i, family := range capabilities.IPFamilies {
		if family != corev1.IPv4Protocol && family != corev1.IPv6Protocol {
			errs = append(errs, field.NotSupported(root.Child("ipFamilies").Index(i), family, []string{"IPv4", "IPv6"}))
		}
	}
	if !slices.Contains(capabilities.Protocols, corev1.ProtocolTCP) {
		errs = append(errs, field.Required(root.Child("protocols"), "TCP must be qualified"))
	}
	for i, protocol := range capabilities.Protocols {
		if protocol != corev1.ProtocolTCP && protocol != corev1.ProtocolUDP && protocol != corev1.ProtocolSCTP {
			errs = append(errs, field.NotSupported(root.Child("protocols").Index(i), protocol, []string{"TCP", "UDP", "SCTP"}))
		}
	}
	checkCIDR := func(cidr string, path *field.Path) {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			errs = append(errs, field.Invalid(path, cidr, err.Error()))
			return
		}
		family := corev1.IPv6Protocol
		if prefix.Addr().Is4() {
			family = corev1.IPv4Protocol
		}
		if !slices.Contains(capabilities.IPFamilies, family) {
			errs = append(errs, field.Invalid(path, cidr, "address family is not qualified"))
		}
	}
	checkPorts := func(ports []cetirunv1alpha1.AgentPolicyPort, path *field.Path) {
		for i, port := range ports {
			if !slices.Contains(capabilities.Protocols, port.Protocol) {
				errs = append(errs, field.Invalid(path.Index(i).Child("protocol"), port.Protocol, "protocol is not qualified"))
			}
		}
	}
	network := field.NewPath("spec", "network")
	for i, cidr := range policy.Network.BlockedCIDRs {
		checkCIDR(cidr, network.Child("blockedCIDRs").Index(i))
	}
	for i, rule := range policy.Network.AllowedInternalCIDRs {
		checkCIDR(rule.CIDR, network.Child("allowedInternalCIDRs").Index(i).Child("cidr"))
		checkPorts(rule.Ports, network.Child("allowedInternalCIDRs").Index(i).Child("ports"))
	}
	checkPorts(policy.Network.BlockedPorts, network.Child("blockedPorts"))
	if len(policy.Network.BlockedPorts) > 0 && !capabilities.BlockedPorts {
		errs = append(errs, field.Required(root.Child("blockedPorts"), "destination-port blocking is not qualified"))
	}
	if len(policy.Network.BlockedFQDNs) > 0 && !capabilities.BlockedFQDNs {
		errs = append(errs, field.Required(root.Child("blockedFQDNs"), "full hostname blocking is not qualified"))
	}
	switch policy.Network.Ingress.Mode {
	case cetirunv1alpha1.AgentPolicyIngressAuthenticated:
		if !capabilities.AuthenticatedIngress {
			errs = append(errs, field.Required(root.Child("authenticatedIngress"), "authenticated ingress is not qualified"))
		}
	case cetirunv1alpha1.AgentPolicyIngressDisabled:
		if !capabilities.DisabledIngress {
			errs = append(errs, field.Required(root.Child("disabledIngress"), "route and inbound-permission removal is not qualified"))
		}
	}
	if policy.Network.Ingress.RequireMTLS && !capabilities.MutualTLS {
		errs = append(errs, field.Required(root.Child("mutualTLS"), "guest-external gateway-to-workload mutual TLS is not qualified"))
	}
	if policy.KubernetesAccess != nil && !capabilities.KubernetesAccess {
		errs = append(errs, field.Required(root.Child("kubernetesAccess"), "administrative Kubernetes access is not qualified"))
	}
	if policy.EntraID.Enabled && !capabilities.EntraID {
		errs = append(errs, field.Required(root.Child("entraID"), "workload identity is not qualified"))
	}
	return errs
}

func canonicalCapabilities(input Capabilities) Capabilities {
	input.HomeAccessModes = slices.Clone(input.HomeAccessModes)
	slices.Sort(input.HomeAccessModes)
	input.HomeAccessModes = slices.Compact(input.HomeAccessModes)
	input.IPFamilies = slices.Clone(input.IPFamilies)
	slices.Sort(input.IPFamilies)
	input.IPFamilies = slices.Compact(input.IPFamilies)
	input.Protocols = slices.Clone(input.Protocols)
	slices.Sort(input.Protocols)
	input.Protocols = slices.Compact(input.Protocols)
	return input
}
