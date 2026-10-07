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
	ctrl "sigs.k8s.io/controller-runtime"
)

// holdGovernanceDenied revokes a virtual key before using the existing
// Sandbox hold path to remove ingress and scale known workloads down. A revoke
// error never skips the compute stop: the next reconcile keeps the fail-closed
// hold and retries revocation.
func (r *AgentReconciler) holdGovernanceDenied(ctx context.Context, agent *api.Agent) (ctrl.Result, error) {
	var revokeErr error
	if r.AIGateway.configured() {
		revokeErr = r.revokeAIGatewayKey(ctx, agent)
	}
	result, holdErr := r.holdSandbox(ctx, agent)
	if revokeErr != nil || holdErr != nil {
		return result, errors.Join(revokeErr, holdErr)
	}
	return result, nil
}

// holdGovernanceReprojection fences an already-running Agent while a changed
// expected projection is being applied. Unlike a denied binding it preserves
// the virtual-key identity and spend history, but blocks the old grant before
// removing workload ingress and compute. The Gateway records that this is an
// operator-owned temporary block; an administrator emergency block is never
// adopted or cleared by this path.
func (r *AgentReconciler) holdGovernanceReprojection(ctx context.Context, agent *api.Agent) (ctrl.Result, error) {
	var blockErr error
	if r.AIGateway.configured() {
		blockErr = r.beginAIGatewayPolicyReprojection(ctx, agent)
	}
	result, holdErr := r.holdSandbox(ctx, agent)
	if blockErr != nil || holdErr != nil {
		return result, errors.Join(blockErr, holdErr)
	}
	// The hold has removed ingress and stopped known compute. Reconcile the
	// replacement key while that fence is in place; this prevents a later
	// Sandbox preparation pass from deadlocking before the Gateway can observe
	// and clear its own PolicyReprojection block.
	if err := r.reconcileAIGateway(ctx, agent, true); err != nil {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, err
	}
	if err := r.updateStatus(ctx, agent); err != nil {
		return ctrl.Result{}, err
	}
	if _, err := r.reconcileGovernance(ctx, agent); err != nil {
		return ctrl.Result{}, err
	}
	return result, nil
}

// holdGovernanceDependencyUnavailable preserves the virtual-key identity and
// spend history while a control-plane dependency cannot be read. It blocks an
// existing key and stops compute, but deliberately does not try to calculate
// or apply a replacement grant without current authorization evidence.
func (r *AgentReconciler) holdGovernanceDependencyUnavailable(ctx context.Context, agent *api.Agent) (ctrl.Result, error) {
	var blockErr error
	if r.AIGateway.configured() {
		blockErr = r.beginAIGatewayPolicyReprojection(ctx, agent)
	}
	result, holdErr := r.holdSandbox(ctx, agent)
	return result, errors.Join(blockErr, holdErr)
}

const (
	governanceCoverageEnforced = "Enforced"
	governanceCoveragePartial  = "Partial"
	governanceCoverageNone     = "Unavailable"
)

// reconcileGovernance resolves only declarative authorization. Workload,
// Gateway, and credential controllers remain the authority for their own
// applied evidence. Invalid authorization is fail-closed before any workload
// recovery can happen.
func (r *AgentReconciler) reconcileGovernance(ctx context.Context, agent *api.Agent) (governanceReconcileResult, error) {
	resolution, err := r.resolveGovernance(ctx, agent)
	if err != nil {
		result := governanceReconcileResult{dependencyUnavailable: isGovernanceDependencyError(err)}
		return result, r.recordGovernance(ctx, agent, nil, err)
	}
	result := governanceReconcileResult{
		authorized:          true,
		projectionsVerified: governanceProjectionsVerified(agent, resolution),
		reprojectionHold:    governanceReprojectionMustHold(agent, resolution),
	}
	return result, r.recordGovernance(ctx, agent, resolution, nil)
}

type governanceResolution struct {
	team                  *api.Team
	policy                *api.AgentPolicy
	authorizationRevision string
	gatewayRevision       string
	credentialRevision    string
	sandboxRevision       string
}

// governanceReconcileResult distinguishes a declaratively authorized Agent
// from one whose current projections have been independently verified. This
// lets an initial binding create its projections while preventing an already
// running Agent from retaining an old key or workload during a revision
// transition.
type governanceReconcileResult struct {
	authorized            bool
	projectionsVerified   bool
	reprojectionHold      bool
	dependencyUnavailable bool
}

// governanceDependencyError distinguishes an unavailable control-plane read
// from a definitive authorization denial. The former must fence access while
// preserving the key so recovery can continue with the same spend record.
type governanceDependencyError struct{ err error }

func (e *governanceDependencyError) Error() string { return e.err.Error() }
func (e *governanceDependencyError) Unwrap() error { return e.err }

func governanceDependency(format string, args ...any) error {
	return &governanceDependencyError{err: fmt.Errorf(format, args...)}
}

func isGovernanceDependencyError(err error) bool {
	var dependency *governanceDependencyError
	return errors.As(err, &dependency)
}

func (r *AgentReconciler) resolveGovernance(ctx context.Context, agent *api.Agent) (*governanceResolution, error) {
	if agent.Spec.Team == "" || agent.Spec.SandboxRef == "" {
		return nil, fmt.Errorf("governance requires spec.team and spec.sandboxRef")
	}
	ownerTenant, ownerTenantOK := agent.Annotations[api.AnnotationOwnerTenantID]
	ownerObject, ownerObjectOK := agent.Annotations[api.AnnotationOwnerObjectID]
	if !ownerTenantOK || !ownerObjectOK || ownerTenant == "" || ownerObject == "" {
		return nil, fmt.Errorf("governance requires immutable verified owner annotations")
	}
	authorization := agent.Spec.Governance.TeamAuthorization
	if authorization.AuthorizedAt.IsZero() || !validGovernanceSubject(authorization.Subject) {
		return nil, fmt.Errorf("governance requires a complete Team authorization")
	}
	if !strings.EqualFold(authorization.Subject.TenantID, ownerTenant) {
		return nil, fmt.Errorf("Team authorization tenant does not match the verified owner")
	}
	if authorization.Subject.Kind == "User" && !strings.EqualFold(authorization.Subject.ObjectID, ownerObject) {
		return nil, fmt.Errorf("user Team authorization subject does not match the verified owner")
	}
	team := &api.Team{}
	if err := r.Get(ctx, types.NamespacedName{Name: agent.Spec.Team}, team); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, governanceDependency("read governed Team: %w", err)
		}
		return nil, fmt.Errorf("read governed Team: %w", err)
	}
	if !slices.ContainsFunc(team.Spec.Membership.Subjects, func(subject api.GovernanceSubject) bool {
		return subject.Kind == authorization.Subject.Kind && strings.EqualFold(subject.TenantID, authorization.Subject.TenantID) && strings.EqualFold(subject.ObjectID, authorization.Subject.ObjectID)
	}) {
		return nil, fmt.Errorf("recorded Team authorization subject is not a current Team member")
	}
	if team.Spec.DefaultSandboxRef == "" || !slices.Contains(team.Spec.SandboxRefs, team.Spec.DefaultSandboxRef) || !slices.Contains(team.Spec.SandboxRefs, agent.Spec.SandboxRef) {
		return nil, fmt.Errorf("Agent Sandbox is not permitted by the current Team")
	}
	if team.Status.ObservedGeneration != team.Generation {
		return nil, governanceDependency("Team governance observation is pending for generation %d", team.Generation)
	}
	teamAccepted := meta.FindStatusCondition(team.Status.Conditions, api.ConditionTeamAccepted)
	if teamAccepted == nil {
		return nil, governanceDependency("Team governance acceptance observation is unavailable")
	}
	if teamAccepted.Status != metav1.ConditionTrue {
		return nil, fmt.Errorf("Team governance configuration is not accepted")
	}
	policyRef := team.Spec.NormalAgentPolicyRef
	if agent.Spec.PolicyClass == api.AgentPolicyClassAdmin {
		policyRef = team.Spec.AdminAgentPolicyRef
	}
	if policyRef == "" {
		return nil, fmt.Errorf("Team has no policy for policyClass %q", agent.Spec.PolicyClass)
	}
	policy := &api.AgentPolicy{}
	if err := r.Get(ctx, types.NamespacedName{Name: policyRef}, policy); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, governanceDependency("read effective AgentPolicy: %w", err)
		}
		return nil, fmt.Errorf("read effective AgentPolicy: %w", err)
	}
	if policy.Status.ObservedGeneration != policy.Generation {
		return nil, governanceDependency("AgentPolicy observation is pending for generation %d", policy.Generation)
	}
	policyAccepted := meta.FindStatusCondition(policy.Status.Conditions, api.ConditionAgentPolicyAccepted)
	if policyAccepted == nil {
		return nil, governanceDependency("AgentPolicy acceptance observation is unavailable")
	}
	if policyAccepted.Status != metav1.ConditionTrue {
		return nil, fmt.Errorf("effective AgentPolicy is not accepted")
	}
	if !slices.Contains(policy.Spec.Harnesses.Allowed, agent.Spec.Agent.Type) {
		return nil, fmt.Errorf("Agent harness %q is not allowed by policy", agent.Spec.Agent.Type)
	}
	// Governance currently has a typed Gateway projection only for the built-in
	// Harnesses. A Custom workload could otherwise retain a direct provider
	// credential path, so it must remain held until it has an equivalent adapter.
	if !aiGatewayHarnessSupported(agent.Spec.Agent.Type) {
		return nil, fmt.Errorf("Agent harness %q has no typed AI Gateway enforcement adapter", agent.Spec.Agent.Type)
	}
	if !r.AIGateway.Enabled || !r.AIGateway.configured() {
		return nil, fmt.Errorf("AI Gateway is not enabled and fully configured; governed Agents cannot use a direct model provider path")
	}
	if err := validateGovernanceSkills(agent, team, policy); err != nil {
		return nil, err
	}
	budget, models, err := effectiveGovernanceModels(agent, policy)
	if err != nil {
		return nil, err
	}
	if err := validateGovernanceIdentity(agent, policy); err != nil {
		return nil, err
	}
	// A display catalog is not authorization evidence. Read the current managed
	// deployment groups before computing the Gateway projection revision so a
	// same-name backend replacement invalidates both the projection and any
	// action that was approved for the previous backend.
	catalogFingerprint := ""
	if agent.Spec.SharedModelsEnabled() {
		gateway, err := r.aiGatewayClient(ctx)
		if err != nil {
			return nil, governanceDependency("create AI Gateway client for Governance catalog: %w", err)
		}
		if err := r.refreshAIGatewayCatalogForGrant(ctx, gateway); err != nil {
			return nil, governanceDependency("refresh live AI Gateway catalog: %w", err)
		}
		var known bool
		catalogFingerprint, known = r.aiGatewayCatalog.managedFingerprint()
		if !known || catalogFingerprint == "" {
			return nil, governanceDependency("live AI Gateway managed catalog is unavailable")
		}
		if _, complete := r.aiGatewayCatalog.resolvedGroups(models); !complete {
			return nil, governanceDependency("the live AI Gateway catalog cannot resolve every authorized shared model")
		}
	}
	authorizationRevision, err := governanceHash(struct {
		Version        string                `json:"version"`
		AgentUID       types.UID             `json:"agentUID"`
		OwnerTenant    string                `json:"ownerTenant"`
		OwnerObject    string                `json:"ownerObject"`
		TeamUID        types.UID             `json:"teamUID"`
		TeamGeneration int64                 `json:"teamGeneration"`
		Subject        api.GovernanceSubject `json:"subject"`
		AuthorizedAt   metav1.Time           `json:"authorizedAt"`
	}{"governance-authorization-v1", agent.UID, strings.ToLower(ownerTenant), strings.ToLower(ownerObject), team.UID, team.Generation, canonicalGovernanceSubject(authorization.Subject), authorization.AuthorizedAt})
	if err != nil {
		return nil, err
	}
	gatewayRevision, err := governanceHash(struct {
		Version            string    `json:"version"`
		PolicyUID          types.UID `json:"policyUID"`
		PolicyGeneration   int64     `json:"policyGeneration"`
		Models             []string  `json:"models"`
		Budget             string    `json:"budget"`
		RequestedShared    bool      `json:"requestedShared"`
		RequestedCopilot   bool      `json:"requestedCopilot"`
		CatalogFingerprint string    `json:"catalogFingerprint"`
	}{"governance-gateway-v1", policy.UID, policy.Generation, models, budget, agent.Spec.SharedModelsEnabled(), agent.Spec.GitHubCopilotEnabled(), catalogFingerprint})
	if err != nil {
		return nil, err
	}
	credentialRevision, err := governanceHash(struct {
		Version          string                       `json:"version"`
		PolicyUID        types.UID                    `json:"policyUID"`
		PolicyGeneration int64                        `json:"policyGeneration"`
		Enabled          bool                         `json:"enabled"`
		Identity         api.AgentPolicyAgentIdentity `json:"identity"`
	}{"governance-credential-v1", policy.UID, policy.Generation, agent.Spec.AgentIdentity != nil && agent.Spec.AgentIdentity.Enabled, policy.Spec.AgentIdentity})
	if err != nil {
		return nil, err
	}
	sandboxRevision := ""
	if agent.Status.Sandbox != nil {
		sandboxRevision = agent.Status.Sandbox.AppliedRevision
	}
	return &governanceResolution{team: team, policy: policy, authorizationRevision: authorizationRevision, gatewayRevision: gatewayRevision, credentialRevision: credentialRevision, sandboxRevision: sandboxRevision}, nil
}

func validGovernanceSubject(subject api.GovernanceSubject) bool {
	return (subject.Kind == "User" || subject.Kind == "Group") && subject.TenantID != "" && subject.ObjectID != ""
}

func canonicalGovernanceSubject(subject api.GovernanceSubject) api.GovernanceSubject {
	subject.TenantID = strings.ToLower(subject.TenantID)
	subject.ObjectID = strings.ToLower(subject.ObjectID)
	return subject
}

func validateGovernanceSkills(agent *api.Agent, team *api.Team, policy *api.AgentPolicy) error {
	requested := append(append([]string{}, team.Spec.Skills...), agent.Spec.SkillOverrides.Add...)
	for _, skill := range requested {
		if !slices.Contains(policy.Spec.Skills.Allowed, skill) {
			return fmt.Errorf("skill %q is not allowed by policy", skill)
		}
	}
	return nil
}

func effectiveGovernanceModels(agent *api.Agent, policy *api.AgentPolicy) (string, []string, error) {
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
		if agent.Spec.Governance.GatewayBudgetUSD != "" {
			return "", nil, fmt.Errorf("an Agent without model entitlement must omit gatewayBudgetUSD")
		}
		return "", models, nil
	}
	if policy.Spec.ModelAccess.GatewayBudget == nil {
		return "", nil, fmt.Errorf("policy must define a Gateway budget for model access")
	}
	budget := policy.Spec.ModelAccess.GatewayBudget.DefaultMonthlyUSD
	if agent.Spec.Governance.GatewayBudgetUSD != "" {
		budget = agent.Spec.Governance.GatewayBudgetUSD
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

func validateGovernanceIdentity(agent *api.Agent, policy *api.AgentPolicy) error {
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

func governanceHash(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (r *AgentReconciler) recordGovernance(ctx context.Context, agent *api.Agent, resolution *governanceResolution, resolutionErr error) error {
	before := agent.Status.DeepCopy()
	status := &api.GovernanceStatus{ObservedGeneration: agent.Generation}
	if resolutionErr != nil {
		if isGovernanceDependencyError(resolutionErr) {
			if agent.Status.Governance != nil {
				status = agent.Status.Governance.DeepCopy()
				status.ObservedGeneration = agent.Generation
			}
			status.Phase, status.Coverage = "Pending", governanceCoverageNone
			setCondition(&agent.Status.Conditions, api.ConditionGovernanceAuthorizationReady, metav1.ConditionFalse, "DependencyUnavailable", resolutionErr.Error())
			setCondition(&agent.Status.Conditions, api.ConditionGovernancePolicyReady, metav1.ConditionFalse, "DependencyUnavailable", "Governance projections are blocked until current authorization evidence can be read")
			setCondition(&agent.Status.Conditions, api.ConditionGovernanceReady, metav1.ConditionFalse, "DependencyUnavailable", resolutionErr.Error())
		} else {
			status.Phase, status.Coverage = "Denied", governanceCoverageNone
			setCondition(&agent.Status.Conditions, api.ConditionGovernanceAuthorizationReady, metav1.ConditionFalse, "AuthorizationDenied", resolutionErr.Error())
			setCondition(&agent.Status.Conditions, api.ConditionGovernancePolicyReady, metav1.ConditionFalse, "AuthorizationDenied", "Governance projections are withheld until authorization is valid")
			setCondition(&agent.Status.Conditions, api.ConditionGovernanceReady, metav1.ConditionFalse, "AuthorizationDenied", resolutionErr.Error())
		}
	} else {
		resolvedAt := metav1.NewTime(time.Now().UTC())
		if existing := agent.Status.Governance; existing != nil && existing.Authorization != nil && existing.Authorization.Revision == resolution.authorizationRevision {
			resolvedAt = existing.Authorization.ResolvedAt
		}
		status.Authorization = &api.GovernanceAuthorizationStatus{Revision: resolution.authorizationRevision, TeamSubject: agent.Spec.Governance.TeamAuthorization.Subject, TeamRef: api.GovernanceTeamReference{Name: resolution.team.Name, UID: resolution.team.UID, Generation: resolution.team.Generation}, AuthorizedAt: agent.Spec.Governance.TeamAuthorization.AuthorizedAt, ResolvedAt: resolvedAt}
		status.Projections = api.GovernanceProjectionsStatus{Sandbox: api.GovernanceRevisionProjection{ExpectedRevision: resolution.sandboxRevision}, Gateway: api.GovernanceRevisionProjection{ExpectedRevision: resolution.gatewayRevision}, CredentialPolicy: api.GovernanceRevisionProjection{ExpectedRevision: resolution.credentialRevision}}
		status.Phase, status.Coverage = "Pending", governanceCoveragePartial
		setCondition(&agent.Status.Conditions, api.ConditionGovernanceAuthorizationReady, metav1.ConditionTrue, "OwnerAndTeamAuthorized", "The immutable owner and recorded Team subject match the current binding")
		verified := governanceProjectionsVerified(agent, resolution)
		if verified {
			status.Phase, status.Coverage = "Ready", governanceCoverageEnforced
			setCondition(&agent.Status.Conditions, api.ConditionGovernancePolicyReady, metav1.ConditionTrue, "RequiredProjectionsVerified", "Every required control projection matches component evidence")
			setCondition(&agent.Status.Conditions, api.ConditionGovernanceReady, metav1.ConditionTrue, "Enforced", "Governance authorization and required projections are verified")
		} else {
			setCondition(&agent.Status.Conditions, api.ConditionGovernancePolicyReady, metav1.ConditionFalse, "ProjectionPending", "Waiting for Sandbox, Gateway, or credential policy verification")
			setCondition(&agent.Status.Conditions, api.ConditionGovernanceReady, metav1.ConditionFalse, "ProjectionPending", "Governance projections are not verified")
		}
	}
	agent.Status.Governance = status
	if reflect.DeepEqual(*before, agent.Status) {
		return nil
	}
	return r.updateStatus(ctx, agent)
}

func governanceProjectionsVerified(agent *api.Agent, resolution *governanceResolution) bool {
	if agent.Status.Sandbox == nil || agent.Status.Sandbox.AppliedRevision == "" || !meta.IsStatusConditionTrue(agent.Status.Conditions, api.ConditionSandboxReady) {
		return false
	}
	needsModels := agent.Spec.SharedModelsEnabled() || agent.Spec.GitHubCopilotEnabled()
	if needsModels && (agent.Status.AIGateway == nil || agent.Status.AIGateway.Phase != api.AIGatewayPhaseReady || !agent.Status.AIGateway.Routed || agent.Status.AIGateway.PolicyRevision != resolution.gatewayRevision) {
		return false
	}
	// Agent ID currently has a legacy bearer issuance path. It can receive a
	// policy projection but must not make Governance aggregate Ready until a
	// typed credential-service executor supplies equivalent evidence.
	if agent.Spec.AgentIdentity != nil && agent.Spec.AgentIdentity.Enabled {
		return false
	}
	return resolution != nil
}

// governanceReprojectionMustHold detects a change away from a projection that
// may still be in use. It intentionally does not hold a brand-new Pending
// Agent: its component controllers need an opportunity to create and verify
// the first Sandbox and Gateway projections. A persisted Ready/routed Agent,
// however, is fail-closed before it can retain the previous projection.
func governanceReprojectionMustHold(agent *api.Agent, resolution *governanceResolution) bool {
	if resolution == nil || !governanceWorkloadMayBeActive(agent) {
		return false
	}
	existing := agent.Status.Governance
	if existing == nil || existing.Authorization == nil {
		return true
	}
	if existing.Authorization.Revision != resolution.authorizationRevision ||
		existing.Projections.Sandbox.ExpectedRevision != resolution.sandboxRevision ||
		existing.Projections.Gateway.ExpectedRevision != resolution.gatewayRevision ||
		existing.Projections.CredentialPolicy.ExpectedRevision != resolution.credentialRevision {
		return true
	}
	return !governanceProjectionsVerified(agent, resolution)
}

func governanceWorkloadMayBeActive(agent *api.Agent) bool {
	if agent == nil {
		return false
	}
	if agent.Status.Phase == api.PhaseReady || meta.IsStatusConditionTrue(agent.Status.Conditions, api.ConditionReady) ||
		meta.IsStatusConditionTrue(agent.Status.Conditions, api.ConditionIngressReady) {
		return true
	}
	return false
}
