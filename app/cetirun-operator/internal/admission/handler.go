// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxadmission

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	jsonpatch "gomodules.xyz/jsonpatch/v2"
	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	ValidationPath = "/validate-sandbox-configuration"
	DefaultingPath = "/mutate-sandbox-agent"
)

type Handler struct {
	reader        client.Reader
	config        Config
	registrations map[api.SandboxType]sandbox.Registration
}

func NewHandler(reader client.Reader, config Config) (*Handler, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if reader == nil {
		return nil, fmt.Errorf("Sandbox admission requires an uncached resource reader")
	}
	config.AdminUsers = slices.Clone(config.AdminUsers)
	config.AdminGroups = slices.Clone(config.AdminGroups)
	// Production registrations remain absent until the enforcement adapter and
	// its qualified evidence source exist. Configuration is not proof of support.
	return &Handler{reader: reader, config: config}, nil
}

func (h *Handler) Validate(ctx context.Context, request admission.Request) admission.Response {
	if request.SubResource == "status" {
		return admission.Allowed("status authorization belongs to Kubernetes RBAC")
	}
	if request.Operation == admissionv1.Delete && request.Resource.Resource == "agents" {
		return admission.Allowed("Agent deletion must remain available")
	}
	object, old, err := decodeRequest(request)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if agent, ok := object.(*api.Agent); ok {
		var previous *api.Agent
		if old != nil {
			previous = old.(*api.Agent)
		}
		return h.validateAgent(ctx, request, agent, previous)
	}

	if team, ok := object.(*api.Team); ok && request.Operation == admissionv1.Update {
		previous := old.(*api.Team)
		if sameTeamGovernance(team.Spec, previous.Spec) {
			return admission.Allowed("Team governance is unchanged")
		}
	}
	if team, ok := object.(*api.Team); ok && request.Operation == admissionv1.Create && !sandbox.TeamGoverned(team) {
		return admission.Allowed("legacy Team")
	}
	if !h.config.administrator(request.UserInfo) {
		return admission.Denied("Sandbox configuration writes require an explicitly authorized administrator")
	}
	if request.Operation == admissionv1.Delete {
		return h.validateDeletion(ctx, object)
	}

	switch candidate := object.(type) {
	case *api.Team:
		if !sandbox.TeamGoverned(candidate) {
			if response := h.checkTeamConsumers(ctx, candidate, true); !response.Allowed {
				return response
			}
			return admission.Allowed("unreferenced Team governance removed")
		}
		resources, err := h.resources(ctx)
		if err != nil {
			return dependencyError(ctx, err)
		}
		if errs := sandbox.ValidateTeamConfiguration(candidate.Spec, resources, h.registrations); len(errs) != 0 {
			return admission.Denied(errs.ToAggregate().Error())
		}
		return h.checkTeamConsumers(ctx, candidate, false)
	case *api.Sandbox:
		if _, errs := sandbox.NormalizeSandbox(candidate.Spec); len(errs) != 0 {
			return admission.Denied(errs.ToAggregate().Error())
		}
	case *api.AgentPolicy:
		if _, errs := sandbox.NormalizeAgentPolicy(candidate.Spec); len(errs) != 0 {
			return admission.Denied(errs.ToAggregate().Error())
		}
	}
	return h.checkReferencingTeams(ctx, object)
}

func (h *Handler) DefaultAgent(ctx context.Context, request admission.Request) admission.Response {
	if request.Operation != admissionv1.Create || request.Resource.Resource != "agents" {
		return admission.Denied("Sandbox selection defaulting applies only to Agent creation")
	}
	object, _, err := decodeRequest(request)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	agent := object.(*api.Agent)
	team, err := h.team(ctx, agent.Spec.Team)
	if err != nil {
		return dependencyError(ctx, err)
	}
	if !sandbox.Requested(agent, team) {
		return admission.Allowed("legacy Agent")
	}
	if !h.config.administrator(request.UserInfo) {
		return admission.Denied("governed Agent creation requires an administrator; member Team assignment is not enabled")
	}
	spec, err := sandbox.DefaultAgentSelection(agent.Spec, team)
	if err != nil {
		return admission.Denied(err.Error())
	}
	var patches []jsonpatch.JsonPatchOperation
	if spec.SandboxRef != agent.Spec.SandboxRef {
		patches = append(patches, jsonpatch.JsonPatchOperation{Operation: "add", Path: "/spec/sandboxRef", Value: spec.SandboxRef})
	}
	if spec.PolicyClass != agent.Spec.PolicyClass {
		patches = append(patches, jsonpatch.JsonPatchOperation{Operation: "add", Path: "/spec/policyClass", Value: spec.PolicyClass})
	}
	return admission.Patched("persist creation-time Sandbox selection", patches...)
}

func (h *Handler) validateAgent(ctx context.Context, request admission.Request, agent, old *api.Agent) admission.Response {
	if old != nil && !slices.Equal(agent.Finalizers, old.Finalizers) {
		if request.UserInfo.Username == h.config.ControllerUsername && controllerFinalizerUpdate(agent, old) {
			return admission.Allowed("controller lifecycle metadata only")
		}
		if changedLifecycleFinalizers(agent.Finalizers, old.Finalizers) {
			return admission.Denied("controller lifecycle finalizers may only change in a controller metadata-only update")
		}
	}
	if old == nil {
		for _, finalizer := range agent.Finalizers {
			if finalizer != api.Finalizer && lifecycleFinalizer(finalizer) {
				return admission.Denied("controller lifecycle markers cannot be supplied during Agent creation")
			}
		}
	}
	team, err := h.team(ctx, agent.Spec.Team)
	if err != nil {
		return dependencyError(ctx, err)
	}
	if !sandbox.Requested(agent, team) && !sandbox.Requested(old, nil) {
		if old != nil && old.Spec.Team != agent.Spec.Team {
			previousTeam, err := h.team(ctx, old.Spec.Team)
			if err != nil {
				return dependencyError(ctx, err)
			}
			if sandbox.TeamGoverned(previousTeam) {
				return admission.Denied("governed Agents cannot return to the legacy path by changing Team")
			}
		}
		return admission.Allowed("legacy Agent")
	}
	if !h.config.administrator(request.UserInfo) {
		return admission.Denied("governed Agent writes require an explicitly authorized administrator")
	}
	if !sandbox.TeamGoverned(team) {
		return admission.Denied("governed Agents require a governed Team; clearing bindings does not release a hold")
	}
	if team.DeletionTimestamp != nil {
		return admission.Denied("a terminating Team cannot authorize a governed Agent")
	}
	spec, err := sandbox.DefaultAgentSelection(agent.Spec, team)
	if err != nil {
		return admission.Denied(err.Error())
	}
	if spec.SandboxRef != agent.Spec.SandboxRef {
		return admission.Denied("spec.sandboxRef must be persisted at creation; existing Agents are never re-defaulted")
	}
	resources, err := h.resources(ctx)
	if err != nil {
		return dependencyError(ctx, err)
	}
	if errs := sandbox.ValidateTeam(team, resources, h.registrations); len(errs) != 0 {
		return admission.Denied(errs.ToAggregate().Error())
	}
	return admission.Allowed("configuration is valid; Sandbox execution remains held")
}

func (h *Handler) resources(ctx context.Context) (sandbox.Resources, error) {
	var boxes api.SandboxList
	var policies api.AgentPolicyList
	if err := h.reader.List(ctx, &boxes); err != nil {
		return sandbox.Resources{}, err
	}
	if err := h.reader.List(ctx, &policies); err != nil {
		return sandbox.Resources{}, err
	}
	resources := sandbox.Resources{
		Sandboxes: make(map[string]*api.Sandbox), AgentPolicies: make(map[string]*api.AgentPolicy),
	}
	for i := range boxes.Items {
		resources.Sandboxes[boxes.Items[i].Name] = &boxes.Items[i]
	}
	for i := range policies.Items {
		resources.AgentPolicies[policies.Items[i].Name] = &policies.Items[i]
	}
	return resources, nil
}

func (h *Handler) team(ctx context.Context, name string) (*api.Team, error) {
	if name == "" {
		return nil, nil
	}
	var team api.Team
	if err := h.reader.Get(ctx, client.ObjectKey{Name: name}, &team); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &team, nil
}

func (h *Handler) checkTeamConsumers(ctx context.Context, team *api.Team, removing bool) admission.Response {
	var agents api.AgentList
	if err := h.reader.List(ctx, &agents); err != nil {
		return dependencyError(ctx, err)
	}
	for _, agent := range agents.Items {
		if agent.Spec.Team != team.Name {
			continue
		}
		if removing || (agent.Spec.SandboxRef != "" && !slices.Contains(team.Spec.SandboxRefs, agent.Spec.SandboxRef)) ||
			(agent.Spec.PolicyClass == api.AgentPolicyClassAdmin && team.Spec.AdminAgentPolicyRef == "") {
			return admission.Denied("Team change would invalidate a referencing Agent; rebind or delete its consumers first")
		}
	}
	return admission.Allowed("Team bindings preserve existing consumers")
}

func (h *Handler) checkReferencingTeams(ctx context.Context, candidate client.Object) admission.Response {
	var teams api.TeamList
	if err := h.reader.List(ctx, &teams); err != nil {
		return dependencyError(ctx, err)
	}
	var resources sandbox.Resources
	for i := range teams.Items {
		team := &teams.Items[i]
		if !references(team, candidate) {
			continue
		}
		if resources.Sandboxes == nil {
			var err error
			resources, err = h.resources(ctx)
			if err != nil {
				return dependencyError(ctx, err)
			}
			switch object := candidate.(type) {
			case *api.Sandbox:
				resources.Sandboxes[object.Name] = object
			case *api.AgentPolicy:
				resources.AgentPolicies[object.Name] = object
			}
		}
		if errs := sandbox.ValidateTeam(team, resources, h.registrations); len(errs) != 0 {
			return admission.Denied(fmt.Sprintf("configuration would invalidate a referencing Team: %v", errs.ToAggregate()))
		}
	}
	return admission.Allowed("configuration is valid for its referencing Teams")
}

func (h *Handler) validateDeletion(ctx context.Context, object client.Object) admission.Response {
	if team, ok := object.(*api.Team); ok {
		return h.checkTeamConsumers(ctx, team, true)
	}
	var teams api.TeamList
	if err := h.reader.List(ctx, &teams); err != nil {
		return dependencyError(ctx, err)
	}
	for i := range teams.Items {
		if references(&teams.Items[i], object) {
			return admission.Denied("configuration is still referenced by a Team")
		}
	}
	// Check dangling direct Agent selections as well as Team references.
	if box, ok := object.(*api.Sandbox); ok {
		var agents api.AgentList
		if err := h.reader.List(ctx, &agents); err != nil {
			return dependencyError(ctx, err)
		}
		for _, agent := range agents.Items {
			if agent.Spec.SandboxRef == box.Name {
				return admission.Denied("Sandbox is still referenced by an Agent")
			}
		}
	}
	return admission.Allowed("configuration has no remaining references")
}

func references(team *api.Team, object client.Object) bool {
	switch object.(type) {
	case *api.Sandbox:
		return slices.Contains(team.Spec.SandboxRefs, object.GetName())
	case *api.AgentPolicy:
		return team.Spec.NormalAgentPolicyRef == object.GetName() || team.Spec.AdminAgentPolicyRef == object.GetName()
	default:
		return false
	}
}

func sameTeamGovernance(a, b api.TeamSpec) bool {
	return a.NormalAgentPolicyRef == b.NormalAgentPolicyRef && a.AdminAgentPolicyRef == b.AdminAgentPolicyRef &&
		a.DefaultSandboxRef == b.DefaultSandboxRef && slices.Equal(a.SandboxRefs, b.SandboxRefs)
}

func lifecycleFinalizer(value string) bool {
	return slices.Contains([]string{api.Finalizer, sandbox.HoldFinalizer,
		"cetirun.microsoft.com/custom-runtime-ownership", "cetirun.microsoft.com/custom-apply-completion"}, value)
}

func changedLifecycleFinalizers(a, b []string) bool {
	for _, values := range [][2][]string{{a, b}, {b, a}} {
		for _, value := range values[0] {
			if lifecycleFinalizer(value) && !slices.Contains(values[1], value) {
				return true
			}
		}
	}
	return false
}

func controllerFinalizerUpdate(agent, old *api.Agent) bool {
	a, b := agent.DeepCopy(), old.DeepCopy()
	a.Finalizers, b.Finalizers = nil, nil
	a.ManagedFields, b.ManagedFields = nil, nil
	if !equality.Semantic.DeepEqual(a, b) {
		return false
	}
	for _, values := range [][2][]string{{agent.Finalizers, old.Finalizers}, {old.Finalizers, agent.Finalizers}} {
		for _, value := range values[0] {
			if !slices.Contains(values[1], value) && !lifecycleFinalizer(value) {
				return false
			}
		}
	}
	if !old.DeletionTimestamp.IsZero() {
		for _, value := range agent.Finalizers {
			if !slices.Contains(old.Finalizers, value) {
				return false
			}
		}
	} else {
		for _, value := range old.Finalizers {
			if !slices.Contains(agent.Finalizers, value) {
				return false
			}
		}
	}
	return true
}

func decodeRequest(request admission.Request) (client.Object, client.Object, error) {
	kinds := map[string]string{"agents": "Agent", "teams": "Team", "sandboxes": "Sandbox", "agentpolicies": "AgentPolicy"}
	kind, known := kinds[request.Resource.Resource]
	if !known || request.Resource.Group != api.GroupVersion.Group || request.Resource.Version != api.GroupVersion.Version ||
		request.Kind.Group != api.GroupVersion.Group || request.Kind.Version != api.GroupVersion.Version || request.Kind.Kind != kind ||
		request.SubResource != "" || request.Namespace != "" {
		return nil, nil, fmt.Errorf("unsupported Sandbox admission resource or subresource")
	}
	if request.Operation != admissionv1.Create && request.Operation != admissionv1.Update && request.Operation != admissionv1.Delete {
		return nil, nil, fmt.Errorf("unsupported Sandbox admission operation")
	}
	makeObject := func(raw runtime.RawExtension) (client.Object, error) {
		var object client.Object
		switch kind {
		case "Agent":
			object = &api.Agent{}
		case "Team":
			object = &api.Team{}
		case "Sandbox":
			object = &api.Sandbox{}
		case "AgentPolicy":
			object = &api.AgentPolicy{}
		}
		if err := decodeStrict(raw.Raw, object); err != nil {
			return nil, fmt.Errorf("invalid %s: %w", kind, err)
		}
		if object.GetName() == "" || object.GetName() != request.Name || object.GetNamespace() != "" {
			return nil, fmt.Errorf("admitted object identity does not match the request")
		}
		return object, nil
	}
	var old client.Object
	if request.Operation != admissionv1.Create {
		var err error
		old, err = makeObject(request.OldObject)
		if err != nil {
			return nil, nil, err
		}
	}
	if request.Operation == admissionv1.Delete {
		return old, old, nil
	}
	object, err := makeObject(request.Object)
	return object, old, err
}

func dependencyError(ctx context.Context, err error) admission.Response {
	log.FromContext(ctx).Error(err, "Sandbox admission cannot verify current configuration")
	return admission.Errored(http.StatusServiceUnavailable, fmt.Errorf("Sandbox admission dependencies are unavailable; retry without weakening governance"))
}
