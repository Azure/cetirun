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

func canonicalPlatform(input Platform, capabilities sandbox.Capabilities) (Platform, error) {
	var platform Platform
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
	normalizePeer := func(peer *Peer) error {
		if len(validation.IsDNS1123Subdomain(peer.Name)) != 0 || peer.UID == "" ||
			len(validation.IsValidLabelValue(string(peer.UID))) != 0 ||
			(peer.Namespace != "" && len(validation.IsDNS1123Label(peer.Namespace)) != 0) || len(peer.IPs) == 0 {
			return fmt.Errorf("%w: peers require names, immutable UIDs, valid namespaces and resolved IPs", ErrUnavailable)
		}
		for i, raw := range peer.IPs {
			address, err := netip.ParseAddr(raw)
			if err != nil || address.Is4In6() || address.Zone() != "" {
				return fmt.Errorf("%w: a peer contains an invalid IP address", ErrUnavailable)
			}
			if !supportedFamily(address, capabilities) {
				return fmt.Errorf("%w: a peer IP family is not qualified", ErrUnsupportedControl)
			}
			peer.IPs[i] = address.String()
		}
		peer.IPs = sortedUnique(peer.IPs)
		return nil
	}
	for i := range topology.DNS {
		if err := normalizePeer(&topology.DNS[i]); err != nil {
			return platform, err
		}
	}
	topology.DNS = canonicalObjects(topology.DNS)
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
			peerKey, _ := json.Marshal(destination.Peer)
			current := merged[string(peerKey)]
			current.Peer = destination.Peer
			current.Ports = append(current.Ports, destination.Ports...)
			current.Ports = canonicalObjects(current.Ports)
			merged[string(peerKey)] = current
		}
		*destinations = nil
		for _, destination := range merged {
			*destinations = append(*destinations, destination)
		}
		*destinations = canonicalObjects(*destinations)
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
	allPeers = canonicalObjects(allPeers)
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
	policy, _ := sandbox.NormalizeAgentPolicy(api.AgentPolicySpec{
		Network: api.AgentPolicyNetwork{Ingress: api.AgentPolicyIngress{Mode: api.AgentPolicyIngressDisabled}},
	})
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
	plan := NetworkPlan{Adapter: input.Platform.Adapter.Name, IngressMode: policy.Network.Ingress.Mode}
	for _, peer := range topology.DNS {
		plan.DNSIPs = append(plan.DNSIPs, peer.IPs...)
	}
	plan.DNSIPs = sortedUnique(plan.DNSIPs)
	if len(plan.DNSIPs) > 3 {
		return plan, fmt.Errorf("%w: the protected Pod can configure at most three DNS server IPs", ErrUnsupportedControl)
	}
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
					if !slices.Contains(plan.DNSIPs, ip) {
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
	for _, root := range familyRoots(capabilities) {
		spec.Egress = append(spec.Egress, EgressRule{ToCIDRSet: cidrExcept(root, internal)})
	}
	for _, cidr := range internal {
		if ranges := cidrExcept(cidr, exceptions); len(ranges) != 0 {
			spec.EgressDeny = append(spec.EgressDeny, EgressRule{ToCIDRSet: ranges})
		}
	}
	spec.EgressDeny = append(spec.EgressDeny,
		EgressRule{ToCIDRSet: cidrRules(hardDenied)},
		EgressRule{ToEntities: []string{"host", "remote-node"}},
	)
	for _, prefix := range blockedCIDRs {
		spec.EgressDeny = append(spec.EgressDeny, EgressRule{ToCIDRSet: []CIDRRule{{CIDR: prefix.String()}}})
	}
	if len(blockedPorts) != 0 {
		spec.EgressDeny = append(spec.EgressDeny, EgressRule{ToEntities: []string{"all"}, ToPorts: portRules(blockedPorts)})
	}
	for _, sector := range partitionGrants(grants, capabilities) {
		spec.Egress = append(spec.Egress, EgressRule{
			ToCIDRSet: []CIDRRule{{CIDR: sector.Prefix.String()}}, ToPorts: portRules(sector.Ports),
		})
		spec.EgressDeny = append(spec.EgressDeny, EgressRule{
			ToCIDRSet: []CIDRRule{{CIDR: sector.Prefix.String()}}, ToPorts: deniedPorts(sector.Ports),
		})
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
		spec.Egress = append(spec.Egress, EgressRule{ToEndpoints: selector, ToPorts: portRules(ports)})
		spec.EgressDeny = append(spec.EgressDeny, EgressRule{ToEndpoints: selector, ToPorts: deniedPorts(ports)})
	}
	for _, peer := range topology.DNS {
		if peer.Namespace != "" {
			dnsPeers = append(dnsPeers, peer)
		}
	}
	for _, selector := range excludingPeers(allowedPeers) {
		spec.EgressDeny = append(spec.EgressDeny, EgressRule{ToEndpoints: []metav1.LabelSelector{selector}})
	}
	for _, root := range familyRoots(capabilities) {
		spec.EgressDeny = append(spec.EgressDeny, EgressRule{
			ToCIDRSet: cidrExcept(root, ipCIDRs(plan.DNSIPs)), ToPorts: portRules(dnsPorts),
		})
	}
	for _, selector := range excludingPeers(dnsPeers) {
		spec.EgressDeny = append(spec.EgressDeny, EgressRule{
			ToEndpoints: []metav1.LabelSelector{selector}, ToPorts: portRules(dnsPorts),
		})
	}
	if policy.Network.Ingress.Mode == api.AgentPolicyIngressDisabled {
		spec.IngressDeny = append(spec.IngressDeny, IngressRule{FromEntities: []string{"all"}})
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
			spec.Ingress = append(spec.Ingress, IngressRule{FromEndpoints: selector, ToPorts: portRules(destination.Ports)})
			spec.IngressDeny = append(spec.IngressDeny, IngressRule{FromEndpoints: selector, ToPorts: deniedPorts(destination.Ports)})
		}
		for _, selector := range excludingPeers(ingressPeers) {
			spec.IngressDeny = append(spec.IngressDeny, IngressRule{FromEndpoints: []metav1.LabelSelector{selector}})
		}
		spec.IngressDeny = append(spec.IngressDeny, IngressRule{FromEntities: []string{"world", "host", "remote-node"}})
	}
	spec.Egress, spec.EgressDeny = canonicalObjects(spec.Egress), canonicalObjects(spec.EgressDeny)
	spec.Ingress, spec.IngressDeny = canonicalObjects(spec.Ingress), canonicalObjects(spec.IngressDeny)
	name, _ := digest(input.Resolution.Binding.AgentUID)
	plan.Policy = CiliumNetworkPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "cilium.io/v2", Kind: "CiliumNetworkPolicy"},
		ObjectMeta: ownedMetadata(input, "sandbox-"+name[:32], input.Namespace), Spec: spec,
	}
	return plan, nil
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
	return canonicalObjects(result), nil
}

func portRules(ports []api.AgentPolicyPort) []PortRule {
	rule := PortRule{}
	for _, port := range ports {
		rule.Ports = append(rule.Ports, PortRange{Port: strconv.Itoa(int(port.Port)), Protocol: port.Protocol})
	}
	return []PortRule{rule}
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

func excludingPeers(peers []Peer) []metav1.LabelSelector {
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
		}}}}
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
		ports = canonicalObjects(ports)
		if i > 0 && !slices.Equal(ports, previous) {
			return nil, fmt.Errorf("%w: managed peer addresses require inconsistent controls", ErrUnsupportedControl)
		}
		previous = ports
	}
	return previous, nil
}

// Partition overlapping internal exceptions before producing port complements;
// otherwise the deny for one rule would incorrectly override a sibling allow.
func partitionGrants(grants []addressGrant, capabilities sandbox.Capabilities) []addressGrant {
	var result []addressGrant
	var visit func(netip.Prefix, []addressGrant)
	visit = func(prefix netip.Prefix, candidates []addressGrant) {
		var overlaps []addressGrant
		var ports []api.AgentPolicyPort
		partial := false
		for _, candidate := range candidates {
			if !prefix.Overlaps(candidate.Prefix) {
				continue
			}
			overlaps = append(overlaps, candidate)
			if candidate.Prefix.Bits() <= prefix.Bits() {
				ports = append(ports, candidate.Ports...)
			} else {
				partial = true
			}
		}
		if len(overlaps) == 0 {
			return
		}
		if !partial {
			result = append(result, addressGrant{prefix, canonicalObjects(ports)})
			return
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
		visit(left, overlaps)
		visit(netip.PrefixFrom(right, bits), overlaps)
	}
	for _, root := range familyRoots(capabilities) {
		visit(netip.MustParsePrefix(root), grants)
	}
	return result
}
