// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandbox

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

func TestCompatibilityFailsClosed(t *testing.T) {
	input := resolverFixture()
	sandbox := input.Resources.Sandboxes["standard"].Spec
	policy := input.Resources.AgentPolicies["normal"].Spec
	for name, remove := range map[string]func(*Capabilities){
		"runtime class": func(c *Capabilities) { c.RuntimeClassName = "" },
		"revision":      func(c *Capabilities) { c.Revision = "" },
		"host limits":   func(c *Capabilities) { c.HostResourceLimits = false },
		"read-only":     func(c *Capabilities) { c.ReadOnlyRootFilesystem = false },
		"home":          func(c *Capabilities) { c.HomeAccessModes = nil },
		"families":      func(c *Capabilities) { c.IPFamilies = nil },
		"protocols":     func(c *Capabilities) { c.Protocols = nil },
		"external":      func(c *Capabilities) { c.GuestExternalNetwork = false },
		"precedence":    func(c *Capabilities) { c.DenyPrecedence = false },
		"node paths":    func(c *Capabilities) { c.NodeTraffic = false },
		"DNS IPs":       func(c *Capabilities) { c.DNSIPRestrictions = false },
		"ingress":       func(c *Capabilities) { c.AuthenticatedIngress = false },
		"observation":   func(c *Capabilities) { c.EnforcementObservation = false },
		"containment":   func(c *Capabilities) { c.RestrictiveFailure = false },
		"bad family":    func(c *Capabilities) { c.IPFamilies = []corev1.IPFamily{"other"} },
		"bad protocol":  func(c *Capabilities) { c.Protocols = []corev1.Protocol{corev1.ProtocolTCP, "other"} },
	} {
		t.Run(name, func(t *testing.T) {
			capabilities := fixtureCapabilities()
			remove(&capabilities)
			if errs := CheckCompatibility(sandbox, policy, capabilities); len(errs) == 0 {
				t.Fatal("missing mandatory capability accepted")
			}
		})
	}
	if len(CheckCompatibility(sandbox, policy, Capabilities{})) == 0 {
		t.Fatal("zero-value platform registration must not be usable")
	}
}

func TestCompatibilityOptionalControls(t *testing.T) {
	input := resolverFixture()
	sandbox := input.Resources.Sandboxes["standard"].Spec
	for _, tc := range []struct {
		name   string
		policy cetirunv1alpha1.AgentPolicySpec
		enable func(*Capabilities)
	}{
		{
			"FQDN", cetirunv1alpha1.AgentPolicySpec{Network: cetirunv1alpha1.AgentPolicyNetwork{BlockedFQDNs: []string{"blocked.example.com"}}},
			func(c *Capabilities) { c.BlockedFQDNs = true },
		},
		{
			"mTLS", cetirunv1alpha1.AgentPolicySpec{Network: cetirunv1alpha1.AgentPolicyNetwork{Ingress: cetirunv1alpha1.AgentPolicyIngress{RequireMTLS: true}}},
			func(c *Capabilities) { c.MutualTLS = true },
		},
		{
			"UDP port", cetirunv1alpha1.AgentPolicySpec{Network: cetirunv1alpha1.AgentPolicyNetwork{BlockedPorts: []cetirunv1alpha1.AgentPolicyPort{{Port: 1234, Protocol: corev1.ProtocolUDP}}}},
			func(c *Capabilities) { c.Protocols = append(c.Protocols, corev1.ProtocolUDP) },
		},
		{
			"Entra", cetirunv1alpha1.AgentPolicySpec{EntraID: cetirunv1alpha1.AgentPolicyEntraID{Enabled: true}},
			func(c *Capabilities) { c.EntraID = true },
		},
		{
			"Kubernetes", cetirunv1alpha1.AgentPolicySpec{KubernetesAccess: &cetirunv1alpha1.AgentPolicyAdminKubernetesAccess{
				ServiceAccountName: "operations",
				RoleBindings:       []cetirunv1alpha1.AgentPolicyRoleBinding{{Namespace: "services", ClusterRoleName: "reader"}},
			}},
			func(c *Capabilities) { c.KubernetesAccess = true },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capabilities := fixtureCapabilities()
			if len(CheckCompatibility(sandbox, tc.policy, capabilities)) == 0 {
				t.Fatal("unqualified optional control accepted")
			}
			tc.enable(&capabilities)
			if errs := CheckCompatibility(sandbox, tc.policy, capabilities); len(errs) != 0 {
				t.Fatalf("qualified control rejected: %v", errs)
			}
		})
	}
}

func TestCompatibilityResourceGuarantees(t *testing.T) {
	input := resolverFixture()
	sandbox := input.Resources.Sandboxes["standard"].Spec
	sandbox.Type = cetirunv1alpha1.SandboxGVisor
	capabilities := fixtureCapabilities()
	capabilities.RuntimeClassName = "gvisor-test"
	policy := cetirunv1alpha1.AgentPolicySpec{}
	if errs := CheckCompatibility(sandbox, policy, capabilities); len(errs) != 0 {
		t.Fatalf("aggregate gVisor limits must not imply independent quotas: %v", errs)
	}
	capabilities.RequirePerContainerResourceLimits = true
	if len(CheckCompatibility(sandbox, policy, capabilities)) == 0 {
		t.Fatal("explicit stronger guarantee must fail without qualified support")
	}
	capabilities.PerContainerResourceLimits = true
	if errs := CheckCompatibility(sandbox, policy, capabilities); len(errs) != 0 {
		t.Fatalf("qualified stronger guarantee rejected: %v", errs)
	}
}

func TestCompatibilityBlockedPortsRequireQualifiedSupport(t *testing.T) {
	input := resolverFixture()
	sandbox := input.Resources.Sandboxes["standard"].Spec
	policy := cetirunv1alpha1.AgentPolicySpec{Network: cetirunv1alpha1.AgentPolicyNetwork{
		BlockedPorts: []cetirunv1alpha1.AgentPolicyPort{{Port: 25}},
	}}
	capabilities := fixtureCapabilities()
	capabilities.BlockedPorts = false
	if len(CheckCompatibility(sandbox, policy, capabilities)) == 0 {
		t.Fatal("TCP support alone must not imply destination-port blocking")
	}
	capabilities.BlockedPorts = true
	if errs := CheckCompatibility(sandbox, policy, capabilities); len(errs) != 0 {
		t.Fatalf("qualified destination-port blocking rejected: %v", errs)
	}
}

func TestCompatibilityAddressFamiliesAndDisabledIngress(t *testing.T) {
	input := resolverFixture()
	sandbox := input.Resources.Sandboxes["standard"].Spec
	policy := cetirunv1alpha1.AgentPolicySpec{Network: cetirunv1alpha1.AgentPolicyNetwork{
		BlockedCIDRs: []string{"2001:db8::/32"},
		Ingress:      cetirunv1alpha1.AgentPolicyIngress{Mode: cetirunv1alpha1.AgentPolicyIngressDisabled},
	}}
	capabilities := fixtureCapabilities()
	capabilities.IPFamilies = []corev1.IPFamily{corev1.IPv4Protocol}
	if len(CheckCompatibility(sandbox, policy, capabilities)) == 0 {
		t.Fatal("IPv6 policy accepted by IPv4-only registration")
	}
	capabilities.IPFamilies = append(capabilities.IPFamilies, corev1.IPv6Protocol)
	capabilities.DisabledIngress = false
	if len(CheckCompatibility(sandbox, policy, capabilities)) == 0 {
		t.Fatal("disabled ingress accepted without qualified removal")
	}
	capabilities.DisabledIngress = true
	if errs := CheckCompatibility(sandbox, policy, capabilities); len(errs) != 0 {
		t.Fatalf("qualified disabled ingress rejected: %v", errs)
	}
}
