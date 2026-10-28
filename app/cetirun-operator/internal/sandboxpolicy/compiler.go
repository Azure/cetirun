// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxpolicy

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/ingressmtls"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
)

// Compile has no side effects and never releases an execution hold. Qualified
// execution requires external observation; native single-start has its own gate.
func Compile(input CompileInput) (*Plan, error) {
	resolution := input.Resolution
	binding := resolution.Binding
	if binding.AgentUID == "" || binding.TeamUID == "" || binding.SandboxUID == "" ||
		binding.AgentPolicyUID == "" || resolution.Revision == "" {
		return nil, fmt.Errorf("%w: a complete authorized resolution is required", sandbox.ErrUnauthorizedBinding)
	}
	if len(validation.IsDNS1123Subdomain(input.AgentName)) != 0 ||
		len(validation.IsDNS1123Label(input.Namespace)) != 0 ||
		len(validation.IsValidLabelValue(string(binding.AgentUID))) != 0 {
		return nil, fmt.Errorf("valid Agent name, UID label and workload namespace are required")
	}
	if err := sandbox.CheckExecutionProfile(resolution.Capabilities, input.NativeExecution); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupportedControl, err)
	}
	if err := validateControlInput(resolution.AgentPolicy, input.Platform); err != nil {
		return nil, err
	}
	normalizedSandbox, sandboxErrors := sandbox.NormalizeSandbox(resolution.Sandbox)
	normalizedPolicy, policyErrors := sandbox.NormalizeAgentPolicy(resolution.AgentPolicy)
	if errs := append(sandboxErrors, policyErrors...); len(errs) != 0 {
		return nil, errs.ToAggregate()
	}
	resolution.Sandbox, resolution.AgentPolicy = normalizedSandbox, normalizedPolicy
	if errs := sandbox.CheckCompatibility(resolution.Sandbox, resolution.AgentPolicy, resolution.Capabilities); len(errs) != 0 {
		return nil, fmt.Errorf("%w: %v", ErrUnsupportedControl, errs.ToAggregate())
	}
	platform, err := canonicalPlatform(input.Platform, resolution.Capabilities)
	if err != nil {
		return nil, err
	}
	if err := supportedPolicy(resolution.AgentPolicy, resolution.Capabilities); err != nil {
		return nil, err
	}
	input.Resolution, input.Platform = resolution, platform
	var mtls *ingressmtls.Plan
	if resolution.AgentPolicy.Network.Ingress.RequireMTLS {
		if !input.NativeExecution || platform.IngressMTLS == nil {
			return nil, fmt.Errorf("%w: managed ingress mTLS requires native execution and certificate registration", ErrUnsupportedControl)
		}
		if len(validation.IsDNS1123Label(input.ServiceName)) != 0 {
			return nil, fmt.Errorf("%w: mTLS requires the resolved native Service name", ErrUnavailable)
		}
		mtls, err = platform.IngressMTLS.Resolve(binding.AgentUID, input.Namespace, input.ServiceName)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
	}
	identity, grants, approval, err := compileIdentity(input)
	if err != nil {
		return nil, err
	}
	network, err := compileNetwork(input, mtls)
	if err != nil {
		return nil, err
	}
	plan := &Plan{
		Binding: binding, AgentName: input.AgentName, Namespace: input.Namespace,
		ResolutionRevision: resolution.Revision, Network: network, Identity: identity,
		IngressMTLS: mtls,
	}
	capabilities := resolution.Capabilities
	capabilities.IPFamilies = sortedUnique(capabilities.IPFamilies)
	capabilities.Protocols = sortedUnique(capabilities.Protocols)
	capabilities.HomeAccessModes = sortedUnique(capabilities.HomeAccessModes)
	// Canonical topology already binds proxy IPs. Preserve the v1 digest for
	// existing lifetimes; new renderings seal the derived projection separately.
	revisionNetwork := network
	revisionNetwork.TrustedProxyIPs = nil
	plan.Revision, err = digest(struct {
		Version      string
		Binding      sandbox.Binding
		AgentName    string
		Namespace    string
		Sandbox      api.SandboxSpec
		Policy       sandbox.PolicyProjection
		Capabilities sandbox.Capabilities
		Adapter      AdapterRegistration
		Topology     Topology
		Grants       []ApprovedGrant
		Approval     *ApprovedIdentity
		Identity     IdentityPlan
		Network      NetworkPlan
		IngressMTLS  *ingressmtls.Plan `json:",omitempty"`
	}{
		"sandbox-controls-v1", binding, input.AgentName, input.Namespace,
		resolution.Sandbox, sandbox.ProjectPolicy(resolution.AgentPolicy), capabilities, platform.Adapter,
		platform.Topology, grants, approval, identity, revisionNetwork, mtls,
	})
	if err != nil {
		return nil, err
	}
	plan.Network.Policy.Annotations[RevisionAnnotation] = plan.Revision
	if err := validateNetworkPolicySize(plan.Network.Policy); err != nil {
		return nil, err
	}
	resource, err := manifest(plan.Network.Policy)
	if err != nil {
		return nil, err
	}
	plan.Resources = append(plan.Resources, resource)
	if mtls != nil {
		resources, err := IngressMTLSResources(input, *mtls, plan.Revision)
		if err != nil {
			return nil, err
		}
		plan.Resources = append(plan.Resources, resources...)
	}
	if plan.Identity.ServiceAccount != nil {
		plan.Identity.ServiceAccount.Annotations[RevisionAnnotation] = plan.Revision
		if !plan.Identity.BootstrapServiceAccount {
			resource, err = manifest(plan.Identity.ServiceAccount)
			if err != nil {
				return nil, err
			}
			plan.Resources = append(plan.Resources, resource)
		}
	}
	for i := range plan.Identity.RoleBindings {
		plan.Identity.RoleBindings[i].Annotations[RevisionAnnotation] = plan.Revision
		resource, err = manifest(plan.Identity.RoleBindings[i])
		if err != nil {
			return nil, err
		}
		plan.Resources = append(plan.Resources, resource)
	}
	for i := range plan.Identity.ClusterRoleBindings {
		plan.Identity.ClusterRoleBindings[i].Annotations[RevisionAnnotation] = plan.Revision
		resource, err = manifest(plan.Identity.ClusterRoleBindings[i])
		if err != nil {
			return nil, err
		}
		plan.Resources = append(plan.Resources, resource)
	}
	return plan, nil
}

// ValidatePolicyConfiguration checks standalone policy support and recorded
// role approvals against platform topology, without a selected Sandbox or live
// role reads. It does not validate Sandbox compatibility, home access, per-Agent
// identity or enforcement. An Accepted policy is not an execution permit.
func ValidatePolicyConfiguration(policy *api.AgentPolicy, capabilities sandbox.Capabilities, platform Platform) error {
	if policy == nil || policy.UID == "" || !policy.DeletionTimestamp.IsZero() {
		return fmt.Errorf("%w: a persisted non-terminating policy is required", ErrUnavailable)
	}
	if err := validateControlInput(policy.Spec, platform); err != nil {
		return err
	}
	normalized, errs := sandbox.NormalizeAgentPolicy(policy.Spec)
	if len(errs) != 0 {
		return errs.ToAggregate()
	}
	if err := supportedPolicy(normalized, capabilities); err != nil {
		return err
	}
	canonical, err := canonicalPlatform(platform, capabilities)
	if err != nil {
		return err
	}
	if _, err := approvedGrants(policy.UID, normalized.KubernetesAccess, canonical.Grants); err != nil {
		return err
	}
	if normalized.KubernetesAccess != nil || normalized.EntraID.Enabled {
		if _, err := tokenSettings(canonical); err != nil {
			return err
		}
	}
	// Network compilation is independent of per-Agent identity. Synthetic object
	// names only select the unreturned fixture policy; no resource is applied.
	_, err = compileNetwork(CompileInput{
		AgentName: "configuration", Namespace: "configuration",
		Resolution: sandbox.Resolution{
			Binding:     sandbox.Binding{AgentUID: "configuration"},
			AgentPolicy: normalized, Capabilities: capabilities,
		},
		Platform: canonical,
	})
	return err
}

func supportedPolicy(policy api.AgentPolicySpec, capabilities sandbox.Capabilities) error {
	if policy.Security.ReadOnlyRootFilesystem == nil ||
		(policy.Network.Ingress.Mode != api.AgentPolicyIngressAuthenticated && policy.Network.Ingress.Mode != api.AgentPolicyIngressDisabled) {
		return fmt.Errorf("%w: policy must be normalized before support evaluation", ErrUnsupportedControl)
	}
	if len(policy.Network.BlockedFQDNs) != 0 {
		return fmt.Errorf("%w: full FQDN blocking is not implemented by the selected adapter", ErrUnsupportedControl)
	}
	if policy.Network.Ingress.RequireMTLS && (capabilities.Profile != sandbox.ProfileNativeSingleStart || !capabilities.MutualTLS) {
		return fmt.Errorf("%w: managed ingress mTLS requires an explicitly registered native endpoint", ErrUnsupportedControl)
	}
	if errs := sandbox.CheckNetworkCapabilities(capabilities); len(errs) != 0 {
		return fmt.Errorf("%w: %v", ErrUnsupportedControl, errs.ToAggregate())
	}
	if capabilities.Profile == sandbox.ProfileNativeSingleStart {
		if errs := sandbox.CheckNativePolicy(policy); len(errs) != 0 {
			return fmt.Errorf("%w: %v", ErrUnsupportedControl, errs.ToAggregate())
		}
	}
	if len(policy.Network.BlockedPorts) > 0 && !capabilities.BlockedPorts ||
		policy.Network.Ingress.Mode == api.AgentPolicyIngressAuthenticated && !capabilities.AuthenticatedIngress ||
		policy.Network.Ingress.Mode == api.AgentPolicyIngressDisabled && !capabilities.DisabledIngress ||
		policy.KubernetesAccess != nil && !capabilities.KubernetesAccess ||
		policy.EntraID.Enabled && !capabilities.EntraID ||
		*policy.Security.ReadOnlyRootFilesystem && !capabilities.ReadOnlyRootFilesystem {
		return fmt.Errorf("%w: a requested policy control is not qualified", ErrUnsupportedControl)
	}
	return nil
}

func ownedMetadata(input CompileInput, name, namespace string) (metav1.ObjectMeta, error) {
	binding, err := json.Marshal(input.Resolution.Binding)
	if err != nil {
		return metav1.ObjectMeta{}, fmt.Errorf("encode sandbox binding metadata: %w", err)
	}
	controller, block := true, true
	return metav1.ObjectMeta{
		Name: name, Namespace: namespace,
		Labels:      map[string]string{AgentUIDLabel: string(input.Resolution.Binding.AgentUID)},
		Annotations: map[string]string{BindingAnnotation: string(binding)},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: api.GroupVersion.String(), Kind: "Agent", Name: input.AgentName,
			UID: input.Resolution.Binding.AgentUID, Controller: &controller, BlockOwnerDeletion: &block,
		}},
	}, nil
}

func digest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode sandbox control revision: %w", err)
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

func manifest(value any) (unstructured.Unstructured, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return unstructured.Unstructured{}, fmt.Errorf("encode sandbox resource: %w", err)
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		return unstructured.Unstructured{}, fmt.Errorf("decode sandbox resource: %w", err)
	}
	return unstructured.Unstructured{Object: object}, nil
}

func sortedUnique[S ~[]E, E ~string](values S) S {
	result := slices.Clone(values)
	slices.Sort(result)
	return slices.Compact(result)
}

func canonicalObjects[T any](values []T) ([]T, error) {
	type entry struct {
		value T
		key   string
	}
	entries := make([]entry, len(values))
	for i, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encode canonical sandbox control: %w", err)
		}
		entries[i] = entry{value: value, key: string(data)}
	}
	slices.SortFunc(entries, func(a, b entry) int {
		return cmp.Compare(a.key, b.key)
	})
	result := slices.Clone(values)
	count := 0
	for i, entry := range entries {
		if i == 0 || entry.key != entries[i-1].key {
			result[count] = entry.value
			count++
		}
	}
	return result[:count], nil
}
