// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

var (
	ErrUnauthorizedBinding = errors.New("sandbox binding is not authorized")
	ErrRuntimeUnavailable  = errors.New("sandbox runtime is unavailable")
)

type Binding struct {
	AgentUID       types.UID                        `json:"agentUID"`
	TeamUID        types.UID                        `json:"teamUID"`
	SandboxUID     types.UID                        `json:"sandboxUID"`
	PolicyClass    cetirunv1alpha1.AgentPolicyClass `json:"policyClass"`
	AgentPolicyUID types.UID                        `json:"agentPolicyUID"`
}

type Resources struct {
	Sandboxes     map[string]*cetirunv1alpha1.Sandbox
	AgentPolicies map[string]*cetirunv1alpha1.AgentPolicy
}

type ResolveInput struct {
	Agent             *cetirunv1alpha1.Agent
	Team              *cetirunv1alpha1.Team
	Resources         Resources
	Registrations     map[cetirunv1alpha1.SandboxType]Registration
	AuthorizedBinding Binding
}

// Resolution is a validated desired configuration, not an applied/enforced plan.
// The enforcement adapter must still compile/verify grants, topology and Pods.
type Resolution struct {
	Binding      Binding                         `json:"binding"`
	Sandbox      cetirunv1alpha1.SandboxSpec     `json:"sandbox"`
	AgentPolicy  cetirunv1alpha1.AgentPolicySpec `json:"agentPolicy"`
	Capabilities Capabilities                    `json:"capabilities"`
	Revision     string                          `json:"-"`
}

// DefaultAgentSelection is for creation only. Its result must be persisted by an
// authorized caller; Resolve never reselects an existing Agent's missing Sandbox.
func DefaultAgentSelection(spec cetirunv1alpha1.AgentSpec, team *cetirunv1alpha1.Team) (cetirunv1alpha1.AgentSpec, error) {
	spec = *spec.DeepCopy()
	if team == nil || team.Name == "" || spec.Team != team.Name || !team.DeletionTimestamp.IsZero() {
		return spec, fmt.Errorf("spec.team must name the authorized, non-terminating Team")
	}
	if errs := validateTeamSpec(team.Spec); len(errs) != 0 {
		return spec, fmt.Errorf("invalid Team bindings: %w", errs.ToAggregate())
	}
	if spec.PolicyClass == "" {
		spec.PolicyClass = cetirunv1alpha1.AgentPolicyClassNormal
	}
	if spec.SandboxRef == "" {
		spec.SandboxRef = team.Spec.DefaultSandboxRef
	}
	if _, err := selectedPolicyRef(spec.PolicyClass, team.Spec); err != nil {
		return spec, err
	}
	if !slices.Contains(team.Spec.SandboxRefs, spec.SandboxRef) {
		return spec, fmt.Errorf("spec.sandboxRef %q is not permitted by Team %q", spec.SandboxRef, team.Name)
	}
	return spec, nil
}

// ValidateTeam checks every configured Sandbox/policy pair, including unselected
// slots. Availability is checked only for the selected runtime during Resolve.
func ValidateTeam(team *cetirunv1alpha1.Team, resources Resources, registrations map[cetirunv1alpha1.SandboxType]Registration) field.ErrorList {
	if team == nil {
		return field.ErrorList{field.Required(field.NewPath("team"), "referenced Team is missing")}
	}
	errs := validateIdentity(team.ObjectMeta, team.Name, field.NewPath("team"))
	if len(errs) != 0 {
		return errs
	}
	return ValidateTeamConfiguration(team.Spec, resources, registrations)
}

// ValidateTeamConfiguration also accepts a candidate Team that has not yet been
// assigned a UID. Every referenced configuration must already be persisted.
func ValidateTeamConfiguration(spec cetirunv1alpha1.TeamSpec, resources Resources, registrations map[cetirunv1alpha1.SandboxType]Registration) field.ErrorList {
	errs := validateTeamSpec(spec)
	if len(errs) != 0 {
		return errs
	}
	team := &cetirunv1alpha1.Team{Spec: spec}

	sandboxes := make(map[string]cetirunv1alpha1.SandboxSpec, len(team.Spec.SandboxRefs))
	for _, name := range team.Spec.SandboxRefs {
		path := field.NewPath("sandboxes").Key(name)
		sandbox := resources.Sandboxes[name]
		if sandbox == nil {
			errs = append(errs, field.NotFound(path, name))
			continue
		}
		objectErrs := validateIdentity(sandbox.ObjectMeta, name, path)
		errs = append(errs, objectErrs...)
		spec, specErrs := NormalizeSandbox(sandbox.Spec)
		errs = append(errs, specErrs...)
		if len(objectErrs) == 0 && len(specErrs) == 0 {
			sandboxes[name] = spec
		}
	}

	for _, slot := range []struct {
		name   string
		ref    string
		normal bool
	}{
		{"normalAgentPolicyRef", team.Spec.NormalAgentPolicyRef, true},
		{"adminAgentPolicyRef", team.Spec.AdminAgentPolicyRef, false},
	} {
		if slot.ref == "" {
			continue
		}
		path := field.NewPath("agentPolicies").Key(slot.ref)
		policy := resources.AgentPolicies[slot.ref]
		if policy == nil {
			errs = append(errs, field.NotFound(path, slot.ref))
			continue
		}
		errs = append(errs, validateIdentity(policy.ObjectMeta, slot.ref, path)...)
		spec, specErrs := NormalizeAgentPolicy(policy.Spec)
		errs = append(errs, specErrs...)
		if len(specErrs) != 0 {
			continue
		}
		if slot.normal && spec.KubernetesAccess != nil {
			errs = append(errs, field.Forbidden(field.NewPath("spec", slot.name), "normal policy must omit kubernetesAccess"))
		}
		if len(team.Spec.SandboxRefs) > 1 && !spec.SandboxSelection.AllowMultiple {
			errs = append(errs, field.Forbidden(field.NewPath("spec", "sandboxRefs"), fmt.Sprintf("policy %q permits only one Sandbox", slot.ref)))
		}
		for _, name := range team.Spec.SandboxRefs {
			sandbox, valid := sandboxes[name]
			if !valid {
				continue
			}
			registration, found := registrations[sandbox.Type]
			if !found {
				errs = append(errs, field.NotFound(field.NewPath("registrations"), sandbox.Type))
				continue
			}
			if compatibilityErrs := CheckCompatibility(sandbox, spec, registration.Capabilities); len(compatibilityErrs) != 0 {
				errs = append(errs, field.Invalid(field.NewPath("spec", slot.name), slot.ref,
					fmt.Sprintf("Sandbox %q is incompatible: %v", name, compatibilityErrs.ToAggregate())))
			}
		}
	}
	return errs
}

func Resolve(input ResolveInput) (*Resolution, error) {
	if input.Agent == nil {
		return nil, fmt.Errorf("Agent is required")
	}
	if errs := validateIdentity(input.Agent.ObjectMeta, input.Agent.Name, field.NewPath("agent")); len(errs) != 0 {
		return nil, errs.ToAggregate()
	}
	if errs := ValidateTeam(input.Team, input.Resources, input.Registrations); len(errs) != 0 {
		return nil, fmt.Errorf("invalid Team configuration: %w", errs.ToAggregate())
	}
	agent, team := input.Agent, input.Team
	if agent.Spec.Team != team.Name {
		return nil, fmt.Errorf("Agent spec.team %q does not match Team %q", agent.Spec.Team, team.Name)
	}
	if agent.Spec.SandboxRef == "" || !slices.Contains(team.Spec.SandboxRefs, agent.Spec.SandboxRef) {
		return nil, fmt.Errorf("Agent spec.sandboxRef must persist one of Team %q's permitted Sandboxes", team.Name)
	}
	class := agent.Spec.PolicyClass
	if class == "" {
		class = cetirunv1alpha1.AgentPolicyClassNormal
	}
	ref, err := selectedPolicyRef(class, team.Spec)
	if err != nil {
		return nil, err
	}
	sandbox := input.Resources.Sandboxes[agent.Spec.SandboxRef]
	policy := input.Resources.AgentPolicies[ref]
	binding := Binding{
		AgentUID: agent.UID, TeamUID: team.UID, SandboxUID: sandbox.UID,
		PolicyClass: class, AgentPolicyUID: policy.UID,
	}
	if input.AuthorizedBinding != binding {
		return nil, fmt.Errorf("%w: authorize the current Agent, Team, Sandbox, policy class and AgentPolicy UIDs explicitly", ErrUnauthorizedBinding)
	}
	sandboxSpec, sandboxErrs := NormalizeSandbox(sandbox.Spec)
	policySpec, policyErrs := NormalizeAgentPolicy(policy.Spec)
	if errs := append(sandboxErrs, policyErrs...); len(errs) != 0 {
		return nil, errs.ToAggregate()
	}
	registration := input.Registrations[sandboxSpec.Type]
	if !registration.Available {
		return nil, fmt.Errorf("%w: %s", ErrRuntimeUnavailable, sandboxSpec.Type)
	}
	resolution := &Resolution{
		Binding: binding, Sandbox: sandboxSpec, AgentPolicy: policySpec,
		Capabilities: canonicalCapabilities(registration.Capabilities),
	}
	// Selection rules govern Team validity, not an already-selected workload.
	effectivePolicy := *policySpec.DeepCopy()
	effectivePolicy.SandboxSelection = cetirunv1alpha1.SandboxSelectionPolicy{}
	encoded, err := json.Marshal(struct {
		Version          string                          `json:"version"`
		Binding          Binding                         `json:"binding"`
		Sandbox          cetirunv1alpha1.SandboxSpec     `json:"sandbox"`
		Policy           cetirunv1alpha1.AgentPolicySpec `json:"policy"`
		RuntimeClassName string                          `json:"runtimeClassName"`
		PlatformRevision string                          `json:"platformRevision"`
	}{
		"sandbox-foundation-v1", binding, sandboxSpec, effectivePolicy,
		registration.Capabilities.RuntimeClassName, registration.Capabilities.Revision,
	})
	if err != nil {
		return nil, fmt.Errorf("encode effective sandbox revision: %w", err)
	}
	digest := sha256.Sum256(encoded)
	resolution.Revision = hex.EncodeToString(digest[:])
	return resolution, nil
}

func selectedPolicyRef(class cetirunv1alpha1.AgentPolicyClass, team cetirunv1alpha1.TeamSpec) (string, error) {
	switch class {
	case cetirunv1alpha1.AgentPolicyClassNormal:
		if team.NormalAgentPolicyRef == "" {
			return "", fmt.Errorf("Team normalAgentPolicyRef is required")
		}
		return team.NormalAgentPolicyRef, nil
	case cetirunv1alpha1.AgentPolicyClassAdmin:
		if team.AdminAgentPolicyRef == "" {
			return "", fmt.Errorf("admin policyClass requires Team adminAgentPolicyRef")
		}
		return team.AdminAgentPolicyRef, nil
	default:
		return "", fmt.Errorf("invalid policyClass %q", class)
	}
}

func validateTeamSpec(spec cetirunv1alpha1.TeamSpec) field.ErrorList {
	root := field.NewPath("spec")
	var errs field.ErrorList
	for _, ref := range []struct{ name, value string }{
		{"normalAgentPolicyRef", spec.NormalAgentPolicyRef},
		{"defaultSandboxRef", spec.DefaultSandboxRef},
	} {
		if len(validation.IsDNS1123Subdomain(ref.value)) != 0 {
			errs = append(errs, field.Invalid(root.Child(ref.name), ref.value, "a valid resource name is required"))
		}
	}
	if spec.AdminAgentPolicyRef != "" && len(validation.IsDNS1123Subdomain(spec.AdminAgentPolicyRef)) != 0 {
		errs = append(errs, field.Invalid(root.Child("adminAgentPolicyRef"), spec.AdminAgentPolicyRef, "must be a valid resource name"))
	}
	if len(spec.SandboxRefs) == 0 {
		errs = append(errs, field.Required(root.Child("sandboxRefs"), "at least one Sandbox is required"))
	}
	seen := make(map[string]bool, len(spec.SandboxRefs))
	for i, ref := range spec.SandboxRefs {
		path := root.Child("sandboxRefs").Index(i)
		if len(validation.IsDNS1123Subdomain(ref)) != 0 {
			errs = append(errs, field.Invalid(path, ref, "must be a valid resource name"))
		}
		if seen[ref] {
			errs = append(errs, field.Duplicate(path, ref))
		}
		seen[ref] = true
	}
	if !seen[spec.DefaultSandboxRef] {
		errs = append(errs, field.Invalid(root.Child("defaultSandboxRef"), spec.DefaultSandboxRef, "must be listed in sandboxRefs"))
	}
	return errs
}

func validateIdentity(metadata metav1.ObjectMeta, expectedName string, path *field.Path) field.ErrorList {
	var errs field.ErrorList
	if metadata.Name != expectedName || len(validation.IsDNS1123Subdomain(metadata.Name)) != 0 {
		errs = append(errs, field.Invalid(path.Child("metadata", "name"), metadata.Name, "must match the referenced resource name"))
	}
	if metadata.Namespace != "" {
		errs = append(errs, field.Forbidden(path.Child("metadata", "namespace"), "sandbox configuration resources are cluster-scoped"))
	}
	if metadata.UID == "" {
		errs = append(errs, field.Required(path.Child("metadata", "uid"), "persisted resource identity is required"))
	}
	if !metadata.DeletionTimestamp.IsZero() {
		errs = append(errs, field.Forbidden(path.Child("metadata", "deletionTimestamp"), "terminating references cannot be bound"))
	}
	return errs
}
