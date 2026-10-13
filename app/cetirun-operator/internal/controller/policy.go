// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxbinding"
	ctrl "sigs.k8s.io/controller-runtime"
)

// holdPolicyDenied revokes a virtual key before using the existing Sandbox hold
// path to remove ingress and scale known workloads down. A revoke error never
// skips the compute stop: the next reconcile keeps the fail-closed hold and
// retries revocation.
func (r *AgentReconciler) holdPolicyDenied(ctx context.Context, agent *api.Agent) (ctrl.Result, error) {
	var revokeErr error
	if r.AIGateway.configured() {
		revokeErr = r.revokeAIGatewayKey(ctx, agent)
	}
	var result ctrl.Result
	var stopErr error
	if agent.Spec.Team != "" || sandboxRequested(agent, nil) {
		result, stopErr = r.holdSandboxWithPolicyFence(ctx, agent, &sandboxPolicyFenceStatus{
			phase: api.PhaseFailed, reason: "PolicyDenied",
			message: "Agent policy was denied; Sandbox execution is stopped.",
		})
	} else {
		result, stopErr = r.stopPolicyWorkload(ctx, agent, api.PhaseFailed, "PolicyDenied", "Agent policy was denied; workload execution is stopped.")
	}
	return result, errors.Join(revokeErr, stopErr)
}

// holdPolicyDependencyUnavailable preserves an existing key while policy
// assignment cannot be read. The condition is reported as Unknown rather than
// pretending policy execution is Pending.
func (r *AgentReconciler) holdPolicyDependencyUnavailable(ctx context.Context, agent *api.Agent) (ctrl.Result, error) {
	var blockErr error
	if r.AIGateway.configured() {
		blockErr = r.beginAIGatewayPolicyReprojection(ctx, agent)
	}
	var result ctrl.Result
	var stopErr error
	if agent.Spec.Team != "" || sandboxRequested(agent, nil) {
		result, stopErr = r.holdSandboxWithPolicyFence(ctx, agent, &sandboxPolicyFenceStatus{
			phase: api.PhasePending, reason: "PolicyDependencyUnavailable",
			message: "Current policy assignment cannot be read; Sandbox execution is stopped.",
		})
	} else {
		result, stopErr = r.stopPolicyWorkload(ctx, agent, api.PhasePending, "PolicyDependencyUnavailable", "Current policy assignment cannot be read; workload execution is stopped.")
	}
	return result, errors.Join(blockErr, stopErr)
}

// stopPolicyWorkload is deliberately separate from holdSandbox: it performs
// only the reversible fail-closed stop (route removal and replicas=0) and never
// creates a Sandbox finalizer, grants, preparation attempt, or workload.
func (r *AgentReconciler) stopPolicyWorkload(ctx context.Context, agent *api.Agent, phase, reason, message string) (ctrl.Result, error) {
	var failures []error
	if agent.Spec.Agent.Type == api.AgentTypeCustom {
		r.customPlans.Delete(agent.Name)
		failures = append(failures,
			r.stopCustomWorkload(ctx, agent),
			r.stopCustomTerminal(ctx, agent),
			r.removeCustomPlatformRoute(ctx, agent),
			r.retryCustomCompletion(ctx, agent),
		)
	} else {
		failures = append(failures, r.stopSandboxBuiltin(ctx, agent))
	}
	before := agent.Status.DeepCopy()
	if agent.Spec.Suspended {
		phase, reason, message = api.PhaseSuspended, "Suspended", "Suspension requested; workload execution is stopped."
	}
	agent.Status.Phase = phase
	agent.Status.AccessURL = ""
	meta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
		Type: api.ConditionReady, Status: metav1.ConditionFalse, ObservedGeneration: agent.Generation,
		Reason: reason, Message: message,
	})
	// SandboxReady is intentionally not modified here. It is Sandbox-owned
	// runtime evidence, not Policy-resolution or workload-admission evidence.
	if agent.Spec.Agent.Type == api.AgentTypeCustom {
		meta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
			Type: api.ConditionIngressReady, Status: metav1.ConditionFalse, ObservedGeneration: agent.Generation,
			Reason: reason, Message: message,
		})
	}
	if !reflect.DeepEqual(*before, agent.Status) {
		failures = append(failures, r.updateStatus(ctx, agent))
	}
	if err := errors.Join(failures...); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

// reconcilePolicy resolves the declarative Team-to-AgentPolicy binding. It
// deliberately does not wait for a Sandbox, Gateway, or Agent Identity to
// execute its own projection: those components publish their own status.
func (r *AgentReconciler) reconcilePolicy(ctx context.Context, agent *api.Agent) (policyReconcileResult, error) {
	if agent.Spec.Agent.Type == api.AgentTypeCustom {
		return policyReconcileResult{authorized: true}, nil
	}
	resolution, err := r.resolvePolicy(ctx, agent)
	if err != nil {
		return policyReconcileResult{dependencyUnavailable: isPolicyDependencyError(err)}, r.recordPolicy(ctx, agent, nil, err)
	}
	return policyReconcileResult{authorized: true}, r.recordPolicy(ctx, agent, resolution, nil)
}

// agentPolicyRequired keeps AI Gateway and Agent reconciliation on the same
// explicit boundary. Platform-managed built-in harnesses require a Team policy;
// Custom images use the separately configured Custom runtime contract.
func (r *AgentReconciler) agentPolicyRequired(_ context.Context, agent *api.Agent) (bool, error) {
	return agent != nil && agent.Spec.Agent.Type != api.AgentTypeCustom, nil
}

type policyResolution struct {
	team   *api.Team
	policy *api.AgentPolicy
}

type policyReconcileResult struct {
	authorized            bool
	dependencyUnavailable bool
}

type policyDependencyError struct{ err error }

func (e *policyDependencyError) Error() string { return e.err.Error() }
func (e *policyDependencyError) Unwrap() error { return e.err }

func policyDependency(format string, args ...any) error {
	return &policyDependencyError{err: fmt.Errorf(format, args...)}
}

func isPolicyDependencyError(err error) bool {
	var dependency *policyDependencyError
	return errors.As(err, &dependency)
}

func (r *AgentReconciler) resolvePolicy(ctx context.Context, agent *api.Agent) (*policyResolution, error) {
	if agent == nil || agent.Spec.Agent.Type == api.AgentTypeCustom {
		return nil, fmt.Errorf("Custom Agents do not support AgentPolicy")
	}
	if agent.Spec.Team == "" || agent.Spec.SandboxRef == "" {
		return nil, fmt.Errorf("policy resolution requires spec.team and spec.sandboxRef")
	}
	team := &api.Team{}
	if err := r.Get(ctx, types.NamespacedName{Name: agent.Spec.Team}, team); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, policyDependency("read Team: %w", err)
		}
		return nil, fmt.Errorf("read Team: %w", err)
	}
	if !team.DeletionTimestamp.IsZero() {
		return nil, fmt.Errorf("Team %q is terminating", team.Name)
	}
	if team.Spec.DefaultSandboxRef == "" || !slices.Contains(team.Spec.SandboxRefs, team.Spec.DefaultSandboxRef) ||
		!slices.Contains(team.Spec.SandboxRefs, agent.Spec.SandboxRef) {
		return nil, fmt.Errorf("Agent Sandbox is not permitted by the current Team")
	}
	if team.Status.ObservedGeneration != team.Generation {
		return nil, policyDependency("Team observation is pending for generation %d", team.Generation)
	}
	teamAccepted := meta.FindStatusCondition(team.Status.Conditions, api.ConditionTeamAccepted)
	if teamAccepted == nil || teamAccepted.ObservedGeneration != team.Generation || teamAccepted.Status == metav1.ConditionUnknown {
		return nil, policyDependency("Team acceptance observation is unavailable")
	}
	if teamAccepted.Status != metav1.ConditionTrue {
		return nil, fmt.Errorf("Team policy assignment is not accepted")
	}
	if team.Spec.AgentPolicyRef == "" {
		return nil, fmt.Errorf("Team has no AgentPolicy assignment")
	}
	policy := &api.AgentPolicy{}
	if err := r.Get(ctx, types.NamespacedName{Name: team.Spec.AgentPolicyRef}, policy); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, policyDependency("read effective AgentPolicy: %w", err)
		}
		return nil, fmt.Errorf("read effective AgentPolicy: %w", err)
	}
	if !policy.DeletionTimestamp.IsZero() {
		return nil, fmt.Errorf("effective AgentPolicy %q is terminating", policy.Name)
	}
	if policy.Status.ObservedGeneration != policy.Generation {
		return nil, policyDependency("AgentPolicy observation is pending for generation %d", policy.Generation)
	}
	policyAccepted := meta.FindStatusCondition(policy.Status.Conditions, api.ConditionAgentPolicyAccepted)
	if policyAccepted == nil || policyAccepted.ObservedGeneration != policy.Generation || policyAccepted.Status == metav1.ConditionUnknown {
		return nil, policyDependency("AgentPolicy acceptance observation is unavailable")
	}
	if policyAccepted.Status != metav1.ConditionTrue {
		return nil, fmt.Errorf("effective AgentPolicy is not accepted")
	}
	authorized, err := sandboxbinding.AuthorizedBinding(agent)
	if err != nil {
		return nil, fmt.Errorf("read protected Team policy assignment: %w", err)
	}
	if authorized.TeamUID != team.UID || authorized.AgentPolicyUID != policy.UID {
		return nil, fmt.Errorf("current Team policy assignment differs from the protected authorization")
	}
	if !slices.Contains(policy.Spec.Harnesses.Allowed, agent.Spec.Agent.Type) {
		return nil, fmt.Errorf("Agent harness %q is not allowed by policy", agent.Spec.Agent.Type)
	}
	if err := validatePolicySkills(agent, team, policy); err != nil {
		return nil, err
	}
	if _, _, err := effectivePolicyModels(agent, policy); err != nil {
		return nil, err
	}
	if err := validatePolicyIdentity(agent, policy); err != nil {
		return nil, err
	}
	return &policyResolution{team: team, policy: policy}, nil
}

func validatePolicySkills(agent *api.Agent, team *api.Team, policy *api.AgentPolicy) error {
	requested := make(map[string]struct{}, len(team.Spec.Skills)+len(agent.Spec.SkillOverrides.Add))
	for _, skill := range team.Spec.Skills {
		requested[skill] = struct{}{}
	}
	for _, skill := range agent.Spec.SkillOverrides.Add {
		requested[skill] = struct{}{}
	}
	for _, skill := range agent.Spec.SkillOverrides.Remove {
		delete(requested, skill)
	}
	skills := make([]string, 0, len(requested))
	for skill := range requested {
		skills = append(skills, skill)
	}
	slices.Sort(skills)
	for _, skill := range skills {
		if !slices.Contains(policy.Spec.Skills.Allowed, skill) {
			return fmt.Errorf("skill %q is not allowed by policy", skill)
		}
	}
	return nil
}

func effectivePolicyModels(agent *api.Agent, policy *api.AgentPolicy) (string, []string, error) {
	shared := agent.Spec.SharedModelsEnabled()
	copilot := agent.Spec.GitHubCopilotEnabled()
	if shared && len(policy.Spec.ModelAccess.SharedModels.Allowed) == 0 {
		return "", nil, fmt.Errorf("shared models were requested but policy grants none")
	}
	if copilot && !policy.Spec.ModelAccess.Copilot.Allowed {
		return "", nil, fmt.Errorf("GitHub Copilot was requested but policy does not allow it")
	}
	models := []string{}
	if shared {
		models = append(models, policy.Spec.ModelAccess.SharedModels.Allowed...)
	}
	if agent.Spec.Model != "" && !(shared && slices.Contains(models, agent.Spec.Model)) && !(copilot && strings.HasPrefix(agent.Spec.Model, api.AIGatewayCopilotModelPrefix)) {
		return "", nil, fmt.Errorf("requested default model %q is not authorized", agent.Spec.Model)
	}
	if !shared && !copilot {
		if agent.Spec.ModelAccess != nil && agent.Spec.ModelAccess.MonthlyBudgetUSD != "" {
			return "", nil, fmt.Errorf("an Agent without model entitlement must omit monthlyBudgetUSD")
		}
		return "", models, nil
	}
	if policy.Spec.ModelAccess.GatewayBudget == nil {
		return "", nil, fmt.Errorf("policy must define a Gateway budget for model access")
	}
	budget := policy.Spec.ModelAccess.GatewayBudget.DefaultMonthlyUSD
	if agent.Spec.ModelAccess != nil && agent.Spec.ModelAccess.MonthlyBudgetUSD != "" {
		budget = agent.Spec.ModelAccess.MonthlyBudgetUSD
	}
	if err := validateBudget(budget, policy.Spec.ModelAccess.GatewayBudget.MaxMonthlyUSD); err != nil {
		return "", nil, err
	}
	return budget, models, nil
}

func validateBudget(value, maximum string) error {
	v, valid := new(big.Rat).SetString(value)
	if !valid || v.Sign() <= 0 {
		return fmt.Errorf("gateway budget must be a positive decimal")
	}
	m, valid := new(big.Rat).SetString(maximum)
	if !valid || m.Sign() <= 0 || v.Cmp(m) > 0 {
		return fmt.Errorf("gateway budget exceeds the policy maximum")
	}
	return nil
}

func validatePolicyIdentity(agent *api.Agent, policy *api.AgentPolicy) error {
	requested := agent.Spec.AgentIdentity != nil && agent.Spec.AgentIdentity.Enabled
	identity := policy.Spec.AgentIdentity
	if identity.Required && !requested {
		return fmt.Errorf("policy requires Agent Identity")
	}
	if requested && (!identity.Allowed || !slices.Contains(identity.CredentialServices, "azure-resource-manager")) {
		return fmt.Errorf("Agent Identity is not permitted by policy")
	}
	return nil
}

func policyHash(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (r *AgentReconciler) recordPolicy(ctx context.Context, agent *api.Agent, resolution *policyResolution, resolutionErr error) error {
	before := agent.Status.DeepCopy()
	if resolutionErr != nil {
		if isPolicyDependencyError(resolutionErr) {
			// A stale resolved policy is not evidence while its dependencies cannot
			// be read. Clear it so consumers cannot mistake it for a current grant.
			agent.Status.Policy = nil
			setCondition(&agent.Status.Conditions, api.ConditionPolicyResolved, metav1.ConditionUnknown, "DependencyUnavailable", resolutionErr.Error())
		} else {
			agent.Status.Policy = &api.PolicyStatus{Phase: "Denied", ObservedGeneration: agent.Generation}
			setCondition(&agent.Status.Conditions, api.ConditionPolicyResolved, metav1.ConditionFalse, "PolicyDenied", resolutionErr.Error())
		}
	} else {
		agent.Status.Policy = &api.PolicyStatus{
			Phase:              "Resolved",
			ObservedGeneration: agent.Generation,
			EffectivePolicyRef: &api.EffectivePolicyReference{PolicyObjectReference: api.PolicyObjectReference{Name: resolution.policy.Name, UID: resolution.policy.UID, Generation: resolution.policy.Generation}},
		}
		setCondition(&agent.Status.Conditions, api.ConditionPolicyResolved, metav1.ConditionTrue, "EffectivePolicyResolved", "The Team policy assignment and requested harness, skills, and models are valid")
	}
	if reflect.DeepEqual(*before, agent.Status) {
		return nil
	}
	return r.updateStatus(ctx, agent)
}
