// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxadmission

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxbinding"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxcoordination"
	jsonpatch "gomodules.xyz/jsonpatch/v2"
	admissionv1 "k8s.io/api/admission/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func (h *Handler) creationTeam(request admission.Request, agent *api.Agent) (string, string, error) {
	admin := h.config.administrator(request.UserInfo)
	if admin && agent.Spec.Team != "" {
		return agent.Spec.Team, "", nil
	}
	assigned, err := h.config.assignedTeam(request.UserInfo)
	if err != nil {
		return "", "", err
	}
	if assigned == "" {
		return agent.Spec.Team, "", nil
	}
	if agent.Spec.Team != "" && agent.Spec.Team != assigned {
		return "", "", fmt.Errorf("spec.team does not match the authenticated administrative Team assignment")
	}
	return assigned, assigned, nil
}

func (h *Handler) memberAuthority(request admission.Request, agent *api.Agent) error {
	assigned, err := h.config.assignedTeam(request.UserInfo)
	if err != nil {
		return err
	}
	if assigned == "" || assigned != agent.Spec.Team {
		return fmt.Errorf("policy-bound Agent writes require an explicitly authorized administrator or an authenticated assigned Team member")
	}
	return nil
}

func (h *Handler) defaultAgentCreation(ctx context.Context, request admission.Request, agent *api.Agent) admission.Response {
	if sandboxbinding.Present(agent) {
		return admission.Denied("authorization records and reauthorization requests cannot be supplied during Agent creation")
	}
	name, assigned, err := h.creationTeam(request, agent)
	if err != nil {
		return admission.Denied(err.Error())
	}
	team, err := h.team(ctx, name)
	if err != nil {
		return dependencyError(ctx, err)
	}
	if assigned != "" && team == nil {
		return admission.Denied("the administratively assigned Team does not exist")
	}
	desired := agent.DeepCopy()
	desired.Spec.Team = name
	patches := selectionPatches(agent, desired)
	if !sandbox.Requested(desired, team) {
		return admission.Patched("legacy Agent; preserve any trusted Team assignment", patches...)
	}
	if !h.config.administrator(request.UserInfo) {
		if err := h.memberAuthority(request, desired); err != nil {
			return admission.Denied(err.Error())
		}
	}
	spec, err := sandbox.DefaultAgentSelection(desired.Spec, team)
	if err != nil {
		return admission.Denied(err.Error())
	}
	desired.Spec = spec
	resources, response := h.agentAuthorizationResources(ctx, admissionv1.Create, desired, team)
	if !response.Allowed {
		return response
	}
	record, err := sandboxbinding.Snapshot(desired, team, resources)
	if err != nil {
		return admission.Denied(err.Error())
	}
	patches = append(selectionPatches(agent, desired), authorizationPatch(agent, record))
	return admission.Patched("persist creation-time selection and UID authorization; execution remains held", patches...)
}

// Ordinary updates never regenerate either defaults or authorization. The
// mutator only changes a record in response to an exact, fresh admin request.
func (h *Handler) reauthorizeAgent(ctx context.Context, request admission.Request, agent, old *api.Agent) admission.Response {
	if !sameAnnotation(agent, old, sandboxbinding.AuthorizationAnnotation) {
		return admission.Denied("protected authorization records cannot be authored or rewritten by callers; use explicit administrator reauthorization")
	}
	if sameAnnotation(agent, old, sandboxbinding.ReauthorizeAnnotation) {
		return admission.Allowed("updates never re-default or implicitly reauthorize an Agent")
	}
	controllerCommit := h.controllerTeamChangeCommit(request, agent, old)
	if controllerCommit {
		if err := h.validateControllerTeamChangeTransition(ctx, agent, old); err != nil {
			return admission.Denied(err.Error())
		}
	}
	if !h.config.administrator(request.UserInfo) && !controllerCommit {
		return admission.Denied("explicit reauthorization requires an authorized administrator")
	}
	intent, err := reauthorizationIntent(agent, old)
	if err != nil {
		return admission.Denied(err.Error())
	}
	team, err := h.team(ctx, agent.Spec.Team)
	if err != nil {
		return dependencyError(ctx, err)
	}
	resources, response := h.agentAuthorizationResources(ctx, admissionv1.Update, agent, team)
	if !response.Allowed {
		return response
	}
	current, err := sandboxbinding.CurrentBinding(agent, team, resources)
	if err != nil {
		return admission.Denied(err.Error())
	}
	if intent.Binding != current {
		return admission.Denied("explicit reauthorization does not match the current Agent, Team, Sandbox and AgentPolicy UIDs")
	}
	record, err := sandboxbinding.Snapshot(agent, team, resources)
	if err != nil {
		return admission.Denied(err.Error())
	}
	return admission.Patched("persist explicitly reauthorized UIDs; execution remains held", authorizationPatch(agent, record))
}

func (h *Handler) validateControllerTeamChangeTransition(ctx context.Context, agent, old *api.Agent) error {
	if h.coordination == nil || old == nil || old.Status.TeamChange == nil {
		return fmt.Errorf("controller Team change requires configured coordination")
	}
	state := old.Status.TeamChange
	ticket, found, err := h.coordination.ReadTransition(ctx, old.UID)
	if err != nil || !found || ticket.Transition.ID != state.TransitionID ||
		ticket.Transition.RequestID != state.RequestID || ticket.Transition.Phase != sandboxcoordination.Changing ||
		ticket.Transition.Target.Binding != (sandbox.Binding{AgentUID: old.UID, TeamUID: state.TargetTeamUID, SandboxUID: state.TargetSandboxUID, AgentPolicyUID: state.TargetPolicyUID}) ||
		ticket.Transition.Source.Intent.Binding.AgentUID != old.UID {
		return fmt.Errorf("controller Team change transition is not the current stopped target authorization")
	}
	return h.coordination.CheckTransition(ctx, ticket)
}

func (h *Handler) protectedAuthorization(request admission.Request, agent, old *api.Agent) error {
	if old == nil {
		if _, supplied := agent.Annotations[sandboxbinding.ReauthorizeAnnotation]; supplied {
			return fmt.Errorf("reauthorization requests apply only to existing Agents")
		}
		return nil
	}
	recordChanged := !sameAnnotation(agent, old, sandboxbinding.AuthorizationAnnotation)
	requestChanged := !sameAnnotation(agent, old, sandboxbinding.ReauthorizeAnnotation)
	if !recordChanged && !requestChanged {
		return nil
	}
	if !h.config.administrator(request.UserInfo) && !h.controllerTeamChangeCommit(request, agent, old) {
		return fmt.Errorf("protected authorization metadata may only change through explicit administrator reauthorization")
	}
	if !requestChanged {
		return fmt.Errorf("protected authorization records require explicit administrator reauthorization")
	}
	_, err := reauthorizationIntent(agent, old)
	return err
}

// validateTeamChangeRequest keeps the user-authored request as a narrow
// operation. The controller is the only writer allowed to commit the selected
// Team; callers can only add an immutable request against the exact object
// version they inspected. While the operation is pending, a suspend request is
// deliberately the sole lifecycle escape hatch (deletion is handled before
// Agent validation).
func (h *Handler) validateTeamChangeRequest(request admission.Request, agent, old *api.Agent) error {
	if old == nil {
		if agent.Spec.TeamChangeRequest != nil {
			return fmt.Errorf("Team change requests apply only to existing Agents")
		}
		return nil
	}
	if change := agent.Spec.TeamChangeRequest; change != nil && change.ModelAccess != nil && change.ModelAccess.MonthlyBudgetUSD != "" {
		// A Team change only selects model sources. Spend limits remain an
		// administrator-only, paused-Agent operation on spec.modelAccess.
		// Check this before the controller exception too, so an old request
		// that bypassed admission cannot be committed with a new budget.
		return fmt.Errorf("a Team change request cannot set monthlyBudgetUSD")
	}
	if h.controllerTeamChangeCommit(request, agent, old) {
		return nil
	}
	changed := !equality.Semantic.DeepEqual(agent.Spec.TeamChangeRequest, old.Spec.TeamChangeRequest)
	if old.TeamChangePending() {
		if suspensionOnlyUpdate(request, agent, old) {
			return nil
		}
		if changed {
			return fmt.Errorf("an in-progress Team change request is immutable; suspend or delete the Agent to stop it")
		}
		if !equality.Semantic.DeepEqual(agent.Spec, old.Spec) {
			return fmt.Errorf("an in-progress Team change permits no Agent configuration or lifecycle update except suspension")
		}
		return nil
	}
	if !changed {
		return nil
	}
	if agent.Spec.TeamChangeRequest == nil {
		// A completed or unfenced failed request may be cleared before a fresh
		// request. It does not change the execution selection.
		if !rawChangesOnly(request, []string{"teamChangeRequest"}, []string{"generation", "managedFields"}) {
			return fmt.Errorf("removing a Team change request cannot modify other Agent configuration")
		}
		return nil
	}
	if !rawChangesOnly(request, []string{"teamChangeRequest"}, []string{"generation", "managedFields"}) {
		return fmt.Errorf("a Team change request cannot modify other Agent configuration")
	}
	change := agent.Spec.TeamChangeRequest
	if state := old.Status.TeamChange; state != nil && state.RequestID == change.RequestID {
		accepted, known := state.OriginalTeamChangeRequest(old.Spec.TeamChangeRequest)
		if !known || !api.TeamChangeRequestsEqual(accepted, change) {
			return fmt.Errorf("a Team change request ID cannot change its original payload")
		}
		// A response lost after the original request completed may retry with
		// its old resourceVersion. It is an idempotent observation, not a new
		// operation, only when every caller-controlled field is identical.
		return nil
	}
	if !api.ValidRestartRequestID(change.RequestID) || change.TargetTeam == "" || change.ExpectedUID == "" ||
		change.ExpectedUID != old.UID || change.ExpectedResourceVersion == "" || change.ExpectedResourceVersion != old.ResourceVersion {
		return fmt.Errorf("Team change request must name the current Agent UID, resourceVersion, request ID, and target Team")
	}
	if old.Spec.TeamChangeRequest != nil && old.Spec.TeamChangeRequest.RequestID == change.RequestID {
		return fmt.Errorf("a Team change request ID cannot change its payload")
	}
	if !h.config.administrator(request.UserInfo) {
		if err := h.memberAuthority(request, old); err != nil {
			return fmt.Errorf("Team change request requires authority over the current Team: %w", err)
		}
	}
	return nil
}

func (h *Handler) validateAgentAuthorization(request admission.Request, agent, old *api.Agent, team *api.Team, resources sandbox.Resources) admission.Response {
	expected, err := sandboxbinding.Snapshot(agent, team, resources)
	if err != nil {
		return admission.Denied(err.Error())
	}
	record := agent.Annotations[sandboxbinding.AuthorizationAnnotation]
	if old == nil {
		if record != expected {
			return admission.Denied("the creation-defaulting webhook must persist the current UID authorization; caller-supplied records are not trusted")
		}
		return admission.Allowed("creation authorization is valid; Sandbox execution remains held")
	}
	reauthorizing := !sameAnnotation(agent, old, sandboxbinding.ReauthorizeAnnotation)
	if reauthorizing {
		if !h.config.administrator(request.UserInfo) && !h.controllerTeamChangeCommit(request, agent, old) {
			return admission.Denied("explicit reauthorization requires an authorized administrator")
		}
		intent, err := reauthorizationIntent(agent, old)
		if err != nil {
			return admission.Denied(err.Error())
		}
		current, err := sandboxbinding.CurrentBinding(agent, team, resources)
		if err != nil {
			return admission.Denied(err.Error())
		}
		if intent.Binding != current || record != expected {
			return admission.Denied("the explicit reauthorization request and protected record must match current reference UIDs; the reauthorization mutator must run")
		}
		return admission.Allowed("explicit administrator reauthorization is valid; Sandbox execution remains held")
	}
	if !sameAgentSelection(agent, old) {
		return admission.Denied("changing a policy-bound Agent's Team, policy class or Sandbox binding requires explicit administrator reauthorization")
	}
	if _, err := sandboxbinding.AuthorizedBinding(agent); err != nil {
		return admission.Denied(fmt.Sprintf("explicit administrator reauthorization is required: %v", err))
	}
	if record != expected {
		return admission.Denied("current reference UIDs differ from protected authorization; explicit administrator reauthorization is required")
	}
	return admission.Allowed("durable UID authorization is unchanged; Sandbox execution remains held")
}

// controllerTeamChangeCommit is the controller's deliberately narrow authority
// to commit a binding after it has persisted a TeamChange checkpoint. It does
// not grant the controller normal administrator powers: no other fields or
// operations pass this predicate, and the mutating webhook still constructs
// the protected UID authorization record from live references.
func (h *Handler) controllerTeamChangeCommit(request admission.Request, agent, old *api.Agent) bool {
	if old == nil || request.Operation != admissionv1.Update || !h.config.NativeExecution ||
		request.UserInfo.Username != h.config.ControllerUsername || !authenticated(request.UserInfo) {
		return false
	}
	state, change := old.Status.TeamChange, old.Spec.TeamChangeRequest
	if state == nil || change == nil || state.RequestID != change.RequestID ||
		state.Phase != api.TeamChangeCommitting || !state.Fenced || !state.Stopped || state.TransitionID == "" ||
		!slices.Contains(old.Finalizers, sandbox.HoldFinalizer) ||
		change.ExpectedUID != old.UID || state.TargetTeam == "" || state.TargetSandbox == "" ||
		state.TargetTeamUID == "" || state.TargetSandboxUID == "" || state.TargetPolicyUID == "" {
		return false
	}
	if agent.UID != old.UID || agent.Spec.Team != state.TargetTeam || agent.Spec.SandboxRef != state.TargetSandbox ||
		agent.Spec.Model != teamChangeModel(old, state) || !equality.Semantic.DeepEqual(agent.Spec.ModelAccess, teamChangeModelAccess(old, state)) ||
		!equality.Semantic.DeepEqual(agent.Spec.TeamChangeRequest, old.Spec.TeamChangeRequest) ||
		!equality.Semantic.DeepEqual(agent.Spec.PlannedMigration, old.Spec.PlannedMigration) ||
		agent.Spec.Suspended != old.Spec.Suspended || !sameControllerTeamChangeMetadata(agent, old) {
		return false
	}
	intent, err := sandboxbinding.ParseReauthorization(agent.Annotations[sandboxbinding.ReauthorizeAnnotation])
	return err == nil && intent.RequestID == state.RequestID && intent.Binding == (sandbox.Binding{
		AgentUID: old.UID, TeamUID: state.TargetTeamUID, SandboxUID: state.TargetSandboxUID, AgentPolicyUID: state.TargetPolicyUID,
	})
}

func sameControllerTeamChangeMetadata(agent, old *api.Agent) bool {
	a, b := agent.DeepCopy(), old.DeepCopy()
	for _, value := range []*api.Agent{a, b} {
		if value.Annotations != nil {
			delete(value.Annotations, sandboxbinding.ReauthorizeAnnotation)
			delete(value.Annotations, sandboxbinding.AuthorizationAnnotation)
		}
		value.Spec.Team, value.Spec.SandboxRef = "", ""
		value.Spec.Model, value.Spec.ModelAccess = "", nil
	}
	return equality.Semantic.DeepEqual(a.Spec, b.Spec) &&
		equality.Semantic.DeepEqual(a.Annotations, b.Annotations) &&
		equality.Semantic.DeepEqual(a.Labels, b.Labels) &&
		equality.Semantic.DeepEqual(a.Finalizers, b.Finalizers) &&
		a.Name == b.Name && a.UID == b.UID
}

func teamChangeModel(agent *api.Agent, state *api.TeamChangeStatus) string {
	if state.Model == "" {
		return agent.Spec.Model
	}
	return state.Model
}

func teamChangeModelAccess(agent *api.Agent, state *api.TeamChangeStatus) *api.ModelAccessSpec {
	if state.ModelAccess == nil {
		return agent.Spec.ModelAccess
	}
	access := state.ModelAccess.DeepCopy()
	if agent.Spec.ModelAccess != nil {
		access.MonthlyBudgetUSD = agent.Spec.ModelAccess.MonthlyBudgetUSD
	}
	return access
}

func reauthorizationIntent(agent, old *api.Agent) (sandboxbinding.Reauthorization, error) {
	if old == nil || old.UID == "" || old.UID != agent.UID || old.Name != agent.Name || agent.ResourceVersion == "" {
		return sandboxbinding.Reauthorization{}, fmt.Errorf("explicit reauthorization requires an existing Agent UID and resourceVersion")
	}
	intent, err := sandboxbinding.ParseReauthorization(agent.Annotations[sandboxbinding.ReauthorizeAnnotation])
	if err != nil {
		return intent, err
	}
	if previous, err := sandboxbinding.ParseReauthorization(old.Annotations[sandboxbinding.ReauthorizeAnnotation]); err == nil &&
		previous.RequestID == intent.RequestID {
		return intent, fmt.Errorf("explicit reauthorization requires a fresh requestID")
	}
	return intent, nil
}

func sameAnnotation(a, b *api.Agent, key string) bool {
	first, firstFound := a.Annotations[key]
	second, secondFound := b.Annotations[key]
	return firstFound == secondFound && first == second
}

func sameAgentSelection(a, b *api.Agent) bool {
	return a.Spec.Team == b.Spec.Team && a.Spec.SandboxRef == b.Spec.SandboxRef
}

func selectionPatches(old, desired *api.Agent) []jsonpatch.JsonPatchOperation {
	var patches []jsonpatch.JsonPatchOperation
	for _, field := range []struct {
		path string
		old  any
		new  any
	}{
		{"/spec/team", old.Spec.Team, desired.Spec.Team},
		{"/spec/sandboxRef", old.Spec.SandboxRef, desired.Spec.SandboxRef},
	} {
		if field.old != field.new {
			patches = append(patches, jsonpatch.JsonPatchOperation{Operation: "add", Path: field.path, Value: field.new})
		}
	}
	return patches
}

func authorizationPatch(agent *api.Agent, record string) jsonpatch.JsonPatchOperation {
	if agent.Annotations == nil {
		return jsonpatch.JsonPatchOperation{Operation: "add", Path: "/metadata/annotations",
			Value: map[string]string{sandboxbinding.AuthorizationAnnotation: record}}
	}
	path := strings.ReplaceAll(strings.ReplaceAll(sandboxbinding.AuthorizationAnnotation, "~", "~0"), "/", "~1")
	return jsonpatch.JsonPatchOperation{Operation: "add", Path: "/metadata/annotations/" + path, Value: record}
}
