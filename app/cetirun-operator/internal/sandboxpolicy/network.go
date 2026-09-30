// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxpolicy

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
)

var privateCIDRs = []string{
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
	"169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16", "224.0.0.0/4", "240.0.0.0/4",
	"::/128", "::1/128", "64:ff9b::/96", "fc00::/7", "fe80::/10", "ff00::/8",
}

var mandatoryCIDRs = []string{
	"0.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "168.63.129.16/32", "224.0.0.0/4", "240.0.0.0/4",
	"::/128", "::1/128", "64:ff9b::/96", "fe80::/10", "ff00::/8",
}

var dnsPorts = []api.AgentPolicyPort{
	{Port: 53, Protocol: corev1.ProtocolTCP},
	{Port: 53, Protocol: corev1.ProtocolUDP},
}

type addressGrant struct {
	Prefix netip.Prefix
	Ports  []api.AgentPolicyPort
}

// Cilium's allow-rule schema limits each toPorts[].ports array to 40 entries.
const maxPortsPerRule = 40

func canonicalPlatform(input Platform, capabilities sandbox.Capabilities) (Platform, error) {
	var platform Platform
	if err := validateControlInput(api.AgentPolicySpec{}, input); err != nil {
		return platform, err
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return platform, err
	}
	if err := json.Unmarshal(encoded, &platform); err != nil {
		return platform, err
	}
	adapter := platform.Adapter
	if adapter.Name != AzureCNIOverlayCilium || !adapter.Qualified || adapter.Revision == "" {
		return platform, fmt.Errorf("%w: no qualified candidate adapter registration", ErrUnsupportedControl)
	}
	if !adapter.Available {
		return platform, fmt.Errorf("%w: the registered network adapter is unavailable", ErrUnavailable)
	}
	if !slices.Contains(capabilities.Protocols, corev1.ProtocolTCP) || !slices.Contains(capabilities.Protocols, corev1.ProtocolUDP) {
		return platform, fmt.Errorf("%w: DNS requires both TCP and UDP", ErrUnsupportedControl)
	}
	if len(capabilities.IPFamilies) == 0 {
		return platform, fmt.Errorf("%w: at least one IP family is required", ErrUnsupportedControl)
	}
	for _, family := range capabilities.IPFamilies {
		if family != corev1.IPv4Protocol && family != corev1.IPv6Protocol {
			return platform, fmt.Errorf("%w: unknown IP family in registration", ErrUnsupportedControl)
		}
	}
	for _, protocol := range capabilities.Protocols {
		if !slices.Contains([]corev1.Protocol{corev1.ProtocolTCP, corev1.ProtocolUDP, corev1.ProtocolSCTP}, protocol) {
			return platform, fmt.Errorf("%w: unknown protocol in registration", ErrUnsupportedControl)
		}
	}
	topology := &platform.Topology
	if topology.Revision == "" || !topology.ManagedPeersComplete {
		return platform, fmt.Errorf("%w: a revisioned complete managed-peer inventory is required", ErrUnavailable)
	}
	for _, inventory := range []struct {
		name   string
		values *[]string
	}{
		{"pod", &topology.PodCIDRs}, {"service", &topology.ServiceCIDRs}, {"node", &topology.NodeCIDRs},
		{"management", &topology.ManagementCIDRs}, {"metadata", &topology.MetadataCIDRs},
	} {
		name, values := inventory.name, inventory.values
		if len(*values) == 0 {
			return platform, fmt.Errorf("%w: %s exclusion inventory is required", ErrUnavailable, name)
		}
		for i, value := range *values {
			prefix, err := parsePrefix(value, capabilities)
			if err != nil {
				return platform, err
			}
			(*values)[i] = prefix.String()
		}
		*values = sortedUnique(*values)
	}
	if len(topology.DNS) == 0 || len(topology.Gateway) == 0 {
		return platform, fmt.Errorf("%w: required DNS and Gateway bindings must be resolved", ErrUnavailable)
	}
	normalizeIP := func(raw string) (string, error) {
		address, err := netip.ParseAddr(raw)
		if err != nil || address.Is4In6() || address.Zone() != "" {
			return "", fmt.Errorf("%w: a resolved endpoint contains an invalid IP address", ErrUnavailable)
		}
		if !supportedFamily(address, capabilities) {
			return "", fmt.Errorf("%w: a resolved IP family is not qualified", ErrUnsupportedControl)
		}
		return address.String(), nil
	}
	normalizePeer := func(peer *Peer) error {
		if len(validation.IsDNS1123Subdomain(peer.Name)) != 0 || peer.UID == "" ||
			len(validation.IsValidLabelValue(string(peer.UID))) != 0 ||
			(peer.Namespace != "" && len(validation.IsDNS1123Label(peer.Namespace)) != 0) || len(peer.IPs) == 0 {
			return fmt.Errorf("%w: peers require names, immutable UIDs, valid namespaces and resolved IPs", ErrUnavailable)
		}
		for i, raw := range peer.IPs {
			address, err := normalizeIP(raw)
			if err != nil {
				return err
			}
			peer.IPs[i] = address
		}
		peer.IPs = sortedUnique(peer.IPs)
		return nil
	}
	dnsIPs := map[string]bool{}
	for i := range topology.DNS {
		if err := normalizePeer(&topology.DNS[i]); err != nil {
			return platform, err
		}
		for _, ip := range topology.DNS[i].IPs {
			dnsIPs[ip] = true
		}
	}
	if len(topology.DNSResolverIPs) == 0 {
		return platform, fmt.Errorf("%w: explicit Pod DNS resolver addresses are required", ErrUnavailable)
	}
	for i, raw := range topology.DNSResolverIPs {
		address, err := normalizeIP(raw)
		if err != nil {
			return platform, err
		}
		if !dnsIPs[address] {
			return platform, fmt.Errorf("%w: each Pod resolver must belong to an approved DNS peer", ErrUnavailable)
		}
		topology.DNSResolverIPs[i] = address
	}
	topology.DNSResolverIPs = sortedUnique(topology.DNSResolverIPs)
	if len(topology.DNSResolverIPs) > 3 {
		return platform, fmt.Errorf("%w: the protected Pod can configure at most three DNS server IPs", ErrUnsupportedControl)
	}
	topology.DNS, err = canonicalObjects(topology.DNS)
	if err != nil {
		return platform, err
	}
	for _, destinations := range []*[]Destination{&topology.Gateway, &topology.Ingress, &topology.KubernetesAPI} {
		for i := range *destinations {
			dest := &(*destinations)[i]
			if err := normalizePeer(&dest.Peer); err != nil {
				return platform, err
			}
			dest.Ports, err = canonicalPorts(dest.Ports, capabilities)
			if err != nil {
				return platform, err
			}
		}
		merged := make(map[string]Destination)
		for _, destination := range *destinations {
			peerKey, err := json.Marshal(destination.Peer)
			if err != nil {
				return platform, fmt.Errorf("encode destination peer: %w", err)
			}
			current := merged[string(peerKey)]
			current.Peer = destination.Peer
			current.Ports = append(current.Ports, destination.Ports...)
			current.Ports, err = canonicalObjects(current.Ports)
			if err != nil {
				return platform, err
			}
			merged[string(peerKey)] = current
		}
		*destinations = nil
		for _, destination := range merged {
			*destinations = append(*destinations, destination)
		}
		*destinations, err = canonicalObjects(*destinations)
		if err != nil {
			return platform, err
		}
	}
	allPeers := slices.Clone(topology.Peers)
	allPeers = append(allPeers, topology.DNS...)
	for _, destinations := range [][]Destination{topology.Gateway, topology.Ingress, topology.KubernetesAPI} {
		for _, destination := range destinations {
			allPeers = append(allPeers, destination.Peer)
		}
	}
	for i := range allPeers {
		if err := normalizePeer(&allPeers[i]); err != nil {
			return platform, err
		}
	}
	allPeers, err = canonicalObjects(allPeers)
	if err != nil {
		return platform, err
	}
	seenUIDs, seenIPs := map[string]bool{}, map[string]string{}
	for _, peer := range allPeers {
		if seenUIDs[string(peer.UID)] {
			return platform, fmt.Errorf("%w: a peer UID has inconsistent resolved membership", ErrUnavailable)
		}
		seenUIDs[string(peer.UID)] = true
		for _, ip := range peer.IPs {
			if uid, exists := seenIPs[ip]; exists && uid != string(peer.UID) {
				return platform, fmt.Errorf("%w: an IP is shared by distinct peer identities", ErrUnavailable)
			}
			seenIPs[ip] = string(peer.UID)
		}
	}
	topology.Peers = allPeers
	return platform, nil
}

// ValidatePlatform checks trusted compiler prerequisites, not their live
// realization. The source supplying Available/Qualified owns that evidence.
func ValidatePlatform(platform Platform, capabilities sandbox.Capabilities) error {
	canonical, err := canonicalPlatform(platform, capabilities)
	if err != nil {
		return err
	}
	policy, errs := sandbox.NormalizeAgentPolicy(api.AgentPolicySpec{
		Network: api.AgentPolicyNetwork{Ingress: api.AgentPolicyIngress{Mode: api.AgentPolicyIngressDisabled}},
	})
	if len(errs) != 0 {
		return errs.ToAggregate()
	}
	_, err = compileNetwork(CompileInput{
		AgentName: "configuration", Namespace: "configuration",
		Resolution: sandbox.Resolution{
			Binding: sandbox.Binding{AgentUID: "configuration"}, AgentPolicy: policy, Capabilities: capabilities,
		},
		Platform: canonical,
	})
	return err
}

func compileNetwork(input CompileInput) (NetworkPlan, error) {
	policy, capabilities, topology := input.Resolution.AgentPolicy, input.Resolution.Capabilities, input.Platform.Topology
	plan := NetworkPlan{
		Adapter: input.Platform.Adapter.Name, IngressMode: policy.Network.Ingress.Mode,
		DNSIPs: slices.Clone(topology.DNSResolverIPs),
	}
	var dnsIPs []string
	for _, peer := range topology.DNS {
		dnsIPs = append(dnsIPs, peer.IPs...)
	}
	dnsIPs = sortedUnique(dnsIPs)
	hardDenied := append(slices.Clone(mandatoryCIDRs), topology.NodeCIDRs...)
	hardDenied = append(hardDenied, topology.ManagementCIDRs...)
	hardDenied = append(hardDenied, topology.MetadataCIDRs...)
	hardDenied = qualifiedCIDRs(hardDenied, capabilities)
	internal := append(slices.Clone(privateCIDRs), hardDenied...)
	internal = append(internal, topology.PodCIDRs...)
	internal = append(internal, topology.ServiceCIDRs...)
	for _, destination := range topology.KubernetesAPI {
		internal = append(internal, peerCIDRs(destination.Peer)...)
	}
	internal = qualifiedCIDRs(internal, capabilities)

	var grants []addressGrant
	blockedCIDRs := make([]netip.Prefix, 0, len(policy.Network.BlockedCIDRs))
	for _, cidr := range policy.Network.BlockedCIDRs {
		prefix, err := parsePrefix(cidr, capabilities)
		if err != nil {
			return plan, err
		}
		blockedCIDRs = append(blockedCIDRs, prefix)
	}
	for _, rule := range policy.Network.AllowedInternalCIDRs {
		prefix, err := parsePrefix(rule.CIDR, capabilities)
		if err != nil {
			return plan, err
		}
		if !containedByAny(prefix, internal) {
			return plan, fmt.Errorf("%w: internal exceptions must be within registered internal networks", ErrUnsupportedControl)
		}
		if overlapsAny(prefix, hardDenied) {
			return plan, fmt.Errorf("%w: internal exceptions cannot include management, metadata or nodes", ErrConflict)
		}
		ports, err := canonicalPorts(rule.Ports, capabilities)
		if err != nil {
			return plan, err
		}
		grants = append(grants, addressGrant{prefix, ports})
	}
	blockedPorts, err := canonicalPortsOptional(policy.Network.BlockedPorts, capabilities)
	if err != nil {
		return plan, err
	}
	var required []Destination
	for _, peer := range topology.DNS {
		required = append(required, Destination{Peer: peer, Ports: dnsPorts})
	}
	required = append(required, topology.Gateway...)
	if policy.KubernetesAccess != nil {
		if len(topology.KubernetesAPI) == 0 {
			return plan, fmt.Errorf("%w: explicit Kubernetes access requires resolved API destinations", ErrUnavailable)
		}
		required = append(required, topology.KubernetesAPI...)
	}
	for _, destination := range required {
		for _, port := range destination.Ports {
			if slices.Contains(blockedPorts, port) {
				return plan, fmt.Errorf("%w: a blocked port prevents required DNS, Gateway or Kubernetes API access", ErrConflict)
			}
			if port.Port == 53 && (port.Protocol == corev1.ProtocolTCP || port.Protocol == corev1.ProtocolUDP) {
				for _, ip := range destination.Peer.IPs {
					if !slices.Contains(dnsIPs, ip) {
						return plan, fmt.Errorf("%w: port 53 is reserved to the approved DNS peers", ErrConflict)
					}
				}
			}
		}
		for _, raw := range destination.Peer.IPs {
			ip := netip.MustParseAddr(raw)
			prefix := netip.PrefixFrom(ip, ip.BitLen())
			if overlapsAny(prefix, hardDenied) {
				return plan, fmt.Errorf("%w: a required destination overlaps management, metadata or node exclusions", ErrConflict)
			}
			for _, blocked := range blockedCIDRs {
				if blocked.Contains(ip) {
					return plan, fmt.Errorf("%w: a blocked CIDR prevents required DNS, Gateway or Kubernetes API access", ErrConflict)
				}
			}
			grants = append(grants, addressGrant{prefix, destination.Ports})
		}
	}
	exceptions := make([]string, 0, len(grants))
	for _, grant := range grants {
		exceptions = append(exceptions, grant.Prefix.String())
	}
	exceptions = sortedUnique(exceptions)
	spec := CiliumPolicySpec{
		EndpointSelector: metav1.LabelSelector{MatchLabels: map[string]string{
			"k8s:" + AgentUIDLabel:            string(input.Resolution.Binding.AgentUID),
			"k8s:io.kubernetes.pod.namespace": input.Namespace,
		}},
		EnableDefaultDeny: DefaultDeny{Ingress: true, Egress: true},
		Ingress:           []IngressRule{}, IngressDeny: []IngressRule{}, Egress: []EgressRule{}, EgressDeny: []EgressRule{},
	}
	budget := networkRuleBudget{}
	// Public-internet egress is intentionally permitted except for explicit
	// exclusions/denies below. Default-deny is not an internet allow-list; only
	// approved peers and ports are exceptions to internal-network isolation.
	for _, root := range familyRoots(capabilities) {
		ranges := cidrExcept(root, internal)
		if len(ranges) == 0 {
			return plan, fmt.Errorf("%w: internal CIDRs cover the entire %s address space", ErrConflict, root)
		}
		if err := budget.egress(&spec.Egress, EgressRule{ToCIDRSet: ranges}); err != nil {
			return plan, err
		}
	}
	for _, cidr := range internal {
		if ranges := cidrExcept(cidr, exceptions); len(ranges) != 0 {
			if err := budget.egress(&spec.EgressDeny, EgressRule{ToCIDRSet: ranges}); err != nil {
				return plan, err
			}
		}
	}
	if err := budget.egress(&spec.EgressDeny,
		EgressRule{ToCIDRSet: cidrRules(hardDenied)},
		EgressRule{ToEntities: []string{"host", "remote-node"}},
	); err != nil {
		return plan, err
	}
	for _, prefix := range blockedCIDRs {
		if err := budget.egress(&spec.EgressDeny, EgressRule{ToCIDRSet: []CIDRRule{{CIDR: prefix.String()}}}); err != nil {
			return plan, err
		}
	}
	if len(blockedPorts) != 0 {
		if err := budget.egress(&spec.EgressDeny, EgressRule{ToEntities: []string{"all"}, ToPorts: portRules(blockedPorts)}); err != nil {
			return plan, err
		}
	}
	sectors, err := partitionGrants(grants, capabilities)
	if err != nil {
		return plan, err
	}
	for _, sector := range sectors {
		if err := budget.egress(&spec.Egress, EgressRule{
			ToCIDRSet: []CIDRRule{{CIDR: sector.Prefix.String()}}, ToPorts: portRules(sector.Ports),
		}); err != nil {
			return plan, err
		}
		if err := budget.egress(&spec.EgressDeny, EgressRule{
			ToCIDRSet: []CIDRRule{{CIDR: sector.Prefix.String()}}, ToPorts: deniedPorts(sector.Ports),
		}); err != nil {
			return plan, err
		}
	}
	var allowedPeers, dnsPeers []Peer
	for _, peer := range topology.Peers {
		if peer.Namespace == "" {
			continue
		}
		ports, err := managedPeerPorts(peer, grants, blockedCIDRs, blockedPorts)
		if err != nil {
			return plan, err
		}
		if len(ports) == 0 {
			continue
		}
		allowedPeers = append(allowedPeers, peer)
		selector := []metav1.LabelSelector{peerSelector(peer)}
		if err := budget.egress(&spec.Egress, EgressRule{ToEndpoints: selector, ToPorts: portRules(ports)}); err != nil {
			return plan, err
		}
		if err := budget.egress(&spec.EgressDeny, EgressRule{ToEndpoints: selector, ToPorts: deniedPorts(ports)}); err != nil {
			return plan, err
		}
	}
	for _, peer := range topology.DNS {
		if peer.Namespace != "" {
			dnsPeers = append(dnsPeers, peer)
		}
	}
	selectors, err := excludingPeers(allowedPeers)
	if err != nil {
		return plan, err
	}
	for _, selector := range selectors {
		if err := budget.egress(&spec.EgressDeny, EgressRule{ToEndpoints: []metav1.LabelSelector{selector}}); err != nil {
			return plan, err
		}
	}
	for _, root := range familyRoots(capabilities) {
		if err := budget.egress(&spec.EgressDeny, EgressRule{
			ToCIDRSet: cidrExcept(root, ipCIDRs(dnsIPs)), ToPorts: portRules(dnsPorts),
		}); err != nil {
			return plan, err
		}
	}
	selectors, err = excludingPeers(dnsPeers)
	if err != nil {
		return plan, err
	}
	for _, selector := range selectors {
		if err := budget.egress(&spec.EgressDeny, EgressRule{
			ToEndpoints: []metav1.LabelSelector{selector}, ToPorts: portRules(dnsPorts),
		}); err != nil {
			return plan, err
		}
	}
	if policy.Network.Ingress.Mode == api.AgentPolicyIngressDisabled {
		if err := budget.ingress(&spec.IngressDeny, IngressRule{FromEntities: []string{"all"}}); err != nil {
			return plan, err
		}
	} else {
		if len(topology.Ingress) == 0 {
			return plan, fmt.Errorf("%w: authenticated ingress requires a registered authenticating Gateway", ErrUnavailable)
		}
		var ingressPeers []Peer
		for _, destination := range topology.Ingress {
			if destination.Peer.Namespace == "" {
				return plan, fmt.Errorf("%w: authenticated ingress requires protected managed Gateway identities", ErrUnsupportedControl)
			}
			ingressPeers = append(ingressPeers, destination.Peer)
			selector := []metav1.LabelSelector{peerSelector(destination.Peer)}
			if err := budget.ingress(&spec.Ingress, IngressRule{FromEndpoints: selector, ToPorts: portRules(destination.Ports)}); err != nil {
				return plan, err
			}
			if err := budget.ingress(&spec.IngressDeny, IngressRule{FromEndpoints: selector, ToPorts: deniedPorts(destination.Ports)}); err != nil {
				return plan, err
			}
		}
		selectors, err := excludingPeers(ingressPeers)
		if err != nil {
			return plan, err
		}
		for _, selector := range selectors {
			if err := budget.ingress(&spec.IngressDeny, IngressRule{FromEndpoints: []metav1.LabelSelector{selector}}); err != nil {
				return plan, err
			}
		}
		if err := budget.ingress(&spec.IngressDeny, IngressRule{FromEntities: []string{"world", "host", "remote-node"}}); err != nil {
			return plan, err
		}
	}
	for _, rules := range []*[]EgressRule{&spec.Egress, &spec.EgressDeny} {
		*rules, err = canonicalObjects(*rules)
		if err != nil {
			return plan, err
		}
	}
	for _, rules := range []*[]IngressRule{&spec.Ingress, &spec.IngressDeny} {
		*rules, err = canonicalObjects(*rules)
		if err != nil {
			return plan, err
		}
	}
	name, err := digest(input.Resolution.Binding.AgentUID)
	if err != nil {
		return plan, err
	}
	metadata, err := ownedMetadata(input, "sandbox-"+name[:32], input.Namespace)
	if err != nil {
		return plan, err
	}
	plan.Policy = CiliumNetworkPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "cilium.io/v2", Kind: "CiliumNetworkPolicy"},
		ObjectMeta: metadata, Spec: spec,
	}
	return plan, validateNetworkPolicySize(plan.Policy)
}

func parsePrefix(raw string, capabilities sandbox.Capabilities) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(raw)
	if err != nil || prefix.Addr().Is4In6() {
		return netip.Prefix{}, fmt.Errorf("invalid network CIDR")
	}
	if !supportedFamily(prefix.Addr(), capabilities) {
		return netip.Prefix{}, fmt.Errorf("%w: a CIDR IP family is not qualified", ErrUnsupportedControl)
	}
	return prefix.Masked(), nil
}

func supportedFamily(address netip.Addr, capabilities sandbox.Capabilities) bool {
	family := corev1.IPv6Protocol
	if address.Is4() {
		family = corev1.IPv4Protocol
	}
	return slices.Contains(capabilities.IPFamilies, family)
}

func qualifiedCIDRs(values []string, capabilities sandbox.Capabilities) []string {
	var result []string
	for _, value := range values {
		prefix := netip.MustParsePrefix(value)
		if supportedFamily(prefix.Addr(), capabilities) {
			result = append(result, prefix.Masked().String())
		}
	}
	return sortedUnique(result)
}

func familyRoots(capabilities sandbox.Capabilities) []string {
	return qualifiedCIDRs([]string{"0.0.0.0/0", "::/0"}, capabilities)
}

func ipCIDRs(ips []string) []string {
	result := make([]string, 0, len(ips))
	for _, raw := range ips {
		ip := netip.MustParseAddr(raw)
		result = append(result, netip.PrefixFrom(ip, ip.BitLen()).String())
	}
	return sortedUnique(result)
}

func peerCIDRs(peer Peer) []string { return ipCIDRs(peer.IPs) }

func overlapsAny(prefix netip.Prefix, values []string) bool {
	for _, value := range values {
		if prefix.Overlaps(netip.MustParsePrefix(value)) {
			return true
		}
	}
	return false
}

func containedByAny(prefix netip.Prefix, values []string) bool {
	for _, value := range values {
		candidate := netip.MustParsePrefix(value)
		if candidate.Bits() <= prefix.Bits() && candidate.Contains(prefix.Addr()) {
			return true
		}
	}
	return false
}

func cidrRules(values []string) []CIDRRule {
	result := make([]CIDRRule, 0, len(values))
	for _, value := range values {
		result = append(result, CIDRRule{CIDR: value})
	}
	return result
}

func cidrExcept(value string, exceptions []string) []CIDRRule {
	prefix := netip.MustParsePrefix(value)
	rule := CIDRRule{CIDR: value}
	for _, exception := range exceptions {
		except := netip.MustParsePrefix(exception)
		if !prefix.Overlaps(except) {
			continue
		}
		if except.Bits() <= prefix.Bits() {
			return nil
		}
		rule.Except = append(rule.Except, except.String())
	}
	rule.Except = sortedUnique(rule.Except)
	return []CIDRRule{rule}
}

func canonicalPortsOptional(ports []api.AgentPolicyPort, capabilities sandbox.Capabilities) ([]api.AgentPolicyPort, error) {
	if len(ports) == 0 {
		return nil, nil
	}
	return canonicalPorts(ports, capabilities)
}

func canonicalPorts(ports []api.AgentPolicyPort, capabilities sandbox.Capabilities) ([]api.AgentPolicyPort, error) {
	if len(ports) == 0 {
		return nil, fmt.Errorf("%w: every destination requires explicit ports", ErrUnavailable)
	}
	result := slices.Clone(ports)
	for i := range result {
		port := &result[i]
		if port.Protocol == "" {
			port.Protocol = corev1.ProtocolTCP
		}
		if port.Port < 1 || port.Port > 65535 {
			return nil, fmt.Errorf("destination port must be between 1 and 65535")
		}
		if !slices.Contains([]corev1.Protocol{corev1.ProtocolTCP, corev1.ProtocolUDP, corev1.ProtocolSCTP}, port.Protocol) ||
			!slices.Contains(capabilities.Protocols, port.Protocol) {
			return nil, fmt.Errorf("%w: destination protocol is not qualified", ErrUnsupportedControl)
		}
	}
	return canonicalObjects(result)
}

func portRules(ports []api.AgentPolicyPort) []PortRule {
	rules := []PortRule{{}}
	for _, port := range ports {
		if len(rules[len(rules)-1].Ports) == maxPortsPerRule {
			rules = append(rules, PortRule{})
		}
		rule := &rules[len(rules)-1]
		rule.Ports = append(rule.Ports, PortRange{Port: strconv.Itoa(int(port.Port)), Protocol: port.Protocol})
	}
	return rules
}

func deniedPorts(allowed []api.AgentPolicyPort) []PortRule {
	rule := PortRule{}
	for _, protocol := range []corev1.Protocol{corev1.ProtocolTCP, corev1.ProtocolUDP, corev1.ProtocolSCTP} {
		var ports []int
		for _, port := range allowed {
			if port.Protocol == protocol {
				ports = append(ports, int(port.Port))
			}
		}
		slices.Sort(ports)
		ports = append(slices.Compact(ports), 65536)
		start := 1
		for _, port := range ports {
			if port > start {
				denied := PortRange{Port: strconv.Itoa(start), Protocol: protocol}
				if port > start+1 {
					denied.EndPort = int32(port - 1)
				}
				rule.Ports = append(rule.Ports, denied)
			}
			start = port + 1
		}
	}
	return []PortRule{rule}
}

func peerSelector(peer Peer) metav1.LabelSelector {
	return metav1.LabelSelector{MatchLabels: map[string]string{
		"k8s:" + PeerUIDLabel:             string(peer.UID),
		"k8s:io.kubernetes.pod.namespace": peer.Namespace,
	}}
}

func excludingPeers(peers []Peer) ([]metav1.LabelSelector, error) {
	var uids []string
	var selectors []metav1.LabelSelector
	for _, peer := range peers {
		uids = append(uids, string(peer.UID))
		selectors = append(selectors, metav1.LabelSelector{
			MatchLabels: map[string]string{"k8s:" + PeerUIDLabel: string(peer.UID)},
			MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key: "k8s:io.kubernetes.pod.namespace", Operator: metav1.LabelSelectorOpNotIn, Values: []string{peer.Namespace},
			}},
		})
	}
	if len(uids) == 0 {
		return []metav1.LabelSelector{{MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key: "k8s:io.kubernetes.pod.namespace", Operator: metav1.LabelSelectorOpExists,
		}}}}, nil
	}
	selectors = append(selectors, metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
		{Key: "k8s:io.kubernetes.pod.namespace", Operator: metav1.LabelSelectorOpExists},
		{Key: "k8s:" + PeerUIDLabel, Operator: metav1.LabelSelectorOpNotIn, Values: sortedUnique(uids)},
	}})
	return canonicalObjects(selectors)
}

func managedPeerPorts(peer Peer, grants []addressGrant, blocks []netip.Prefix, blockedPorts []api.AgentPolicyPort) ([]api.AgentPolicyPort, error) {
	var previous []api.AgentPolicyPort
	for i, raw := range peer.IPs {
		ip := netip.MustParseAddr(raw)
		var ports []api.AgentPolicyPort
		blocked := false
		for _, prefix := range blocks {
			blocked = blocked || prefix.Contains(ip)
		}
		if !blocked {
			for _, grant := range grants {
				if grant.Prefix.Contains(ip) {
					ports = append(ports, grant.Ports...)
				}
			}
			ports = slices.DeleteFunc(ports, func(port api.AgentPolicyPort) bool { return slices.Contains(blockedPorts, port) })
		}
		var err error
		ports, err = canonicalObjects(ports)
		if err != nil {
			return nil, err
		}
		if i > 0 && !slices.Equal(ports, previous) {
			return nil, fmt.Errorf("%w: managed peer addresses require inconsistent controls", ErrUnsupportedControl)
		}
		previous = ports
	}
	return previous, nil
}

// Partition overlapping internal exceptions before producing port complements;
// otherwise the deny for one rule would incorrectly override a sibling allow.
func partitionGrants(grants []addressGrant, capabilities sandbox.Capabilities) ([]addressGrant, error) {
	var result []addressGrant
	remaining := MaxNetworkPartitionWork
	var visit func(netip.Prefix, []addressGrant) error
	visit = func(prefix netip.Prefix, candidates []addressGrant) error {
		if err := consumeBudget(&remaining, 1+len(candidates), "network partition work"); err != nil {
			return err
		}
		var overlaps []addressGrant
		var ports []api.AgentPolicyPort
		partial := false
		for _, candidate := range candidates {
			if !prefix.Overlaps(candidate.Prefix) {
				continue
			}
			overlaps = append(overlaps, candidate)
			if candidate.Prefix.Bits() <= prefix.Bits() {
				if err := consumeBudget(&remaining, len(candidate.Ports), "network partition work"); err != nil {
					return err
				}
				ports = append(ports, candidate.Ports...)
			} else {
				partial = true
			}
		}
		if len(overlaps) == 0 {
			return nil
		}
		if !partial {
			if len(result) == MaxNetworkRules/2 {
				return fmt.Errorf("%w: partitioned grants exceed the network rule budget", ErrConflict)
			}
			canonical, err := canonicalObjects(ports)
			if err != nil {
				return err
			}
			result = append(result, addressGrant{prefix, canonical})
			return nil
		}
		bits := prefix.Bits() + 1
		left := netip.PrefixFrom(prefix.Addr(), bits)
		right := prefix.Addr()
		if right.Is4() {
			bytes := right.As4()
			bytes[(bits-1)/8] |= 1 << (7 - ((bits - 1) % 8))
			right = netip.AddrFrom4(bytes)
		} else {
			bytes := right.As16()
			bytes[(bits-1)/8] |= 1 << (7 - ((bits - 1) % 8))
			right = netip.AddrFrom16(bytes)
		}
		if err := visit(left, overlaps); err != nil {
			return err
		}
		return visit(netip.PrefixFrom(right, bits), overlaps)
	}
	for _, root := range familyRoots(capabilities) {
		if err := visit(netip.MustParsePrefix(root), grants); err != nil {
			return nil, err
		}
	}
	return result, nil
}
