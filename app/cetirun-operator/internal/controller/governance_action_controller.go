// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

const governanceActionObserveInterval = 10 * time.Second

var errGovernanceActionClaimed = errors.New("another GovernanceAction holds the target claim")

// GovernanceActionReconciler executes only approved, typed operations. It
// never interprets user supplied commands or generic patches. An Agent-owned
// optimistic-lock claim serializes action execution; status is not a lock.
// +kubebuilder:rbac:groups=cetirun.microsoft.com,resources=governanceactions,verbs=get;list;watch
// +kubebuilder:rbac:groups=cetirun.microsoft.com,resources=governanceactions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cetirun.microsoft.com,resources=agents,verbs=get;list;watch;patch
type GovernanceActionReconciler struct {
	client.Client
	APIReader client.Reader
	Now       func() time.Time
}

func (r *GovernanceActionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Client == nil {
		r.Client = mgr.GetClient()
	}
	if r.APIReader == nil {
		r.APIReader = mgr.GetAPIReader()
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&api.GovernanceAction{}).
		Watches(&api.Agent{}, handler.EnqueueRequestsFromMapFunc(r.enqueueTargetActions)).
		Complete(r)
}

func (r *GovernanceActionReconciler) enqueueTargetActions(ctx context.Context, object client.Object) []ctrl.Request {
	agent, ok := object.(*api.Agent)
	if !ok || agent.UID == "" {
		return nil
	}
	var actions api.GovernanceActionList
	if err := r.List(ctx, &actions); err != nil {
		return nil
	}
	requests := make([]ctrl.Request, 0)
	for _, action := range actions.Items {
		if action.Spec.TargetRef.UID == agent.UID && action.Status.Phase == "Executing" {
			requests = append(requests, ctrl.Request{NamespacedName: types.NamespacedName{Name: action.Name}})
		}
	}
	return requests
}

func (r *GovernanceActionReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	action := &api.GovernanceAction{}
	if err := r.Get(ctx, request.NamespacedName, action); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if action.DeletionTimestamp != nil || terminalActionPhase(action.Status.Phase) {
		return ctrl.Result{}, nil
	}
	now := r.now()
	// Expiry blocks an unstarted proposal. Once a typed mutation has been
	// claimed, cancelling it would make the audit result ambiguous; it remains
	// Executing until a definite observed result is available.
	if action.Status.Phase != "Executing" && !action.Spec.ExpiresAt.After(now) {
		return ctrl.Result{}, r.setActionStatus(ctx, action, "Expired", "ProposalExpired", "The governance proposal expired before execution.", nil)
	}
	if action.Spec.Approval == nil {
		return ctrl.Result{RequeueAfter: actionRequeueAfter(action.Spec.ExpiresAt.Time, now)}, r.setActionStatus(ctx, action, "AwaitingApproval", "ApprovalRequired", "A Platform Admin approval is required.", nil)
	}
	if action.Spec.Approval.Decision == "Denied" {
		return ctrl.Result{}, r.setActionStatus(ctx, action, "Denied", "ApprovalDenied", "The Platform Admin denied this proposal.", nil)
	}
	if action.Spec.Approval.Decision != "Approved" {
		return ctrl.Result{}, r.setActionStatus(ctx, action, "Failed", "ApprovalInvalid", "The approval decision is invalid.", nil)
	}
	if action.Spec.Approval.ExpiresAt.After(action.Spec.ExpiresAt.Time) {
		return ctrl.Result{}, r.setActionStatus(ctx, action, "Failed", "ApprovalExpiryInvalid", "The approval outlives the proposal.", nil)
	}
	if action.Status.Phase != "Executing" && !action.Spec.Approval.ExpiresAt.After(now) {
		return ctrl.Result{}, r.setActionStatus(ctx, action, "Expired", "ApprovalExpired", "The governance approval expired before execution.", nil)
	}
	digest, err := api.GovernanceActionDigest(action.Spec)
	if err != nil {
		return ctrl.Result{}, r.setActionStatus(ctx, action, "Failed", "DigestFailed", "Unable to verify the approved proposal digest.", nil)
	}
	if action.Spec.Approval.ActionDigest != digest {
		return ctrl.Result{}, r.setActionStatus(ctx, action, "Failed", "ApprovalDigestMismatch", "The approval does not match the immutable proposal.", nil)
	}
	if action.Status.Phase == "Executing" {
		return r.observeExecution(ctx, action)
	}
	return r.startExecution(ctx, action)
}

func (r *GovernanceActionReconciler) startExecution(ctx context.Context, action *api.GovernanceAction) (ctrl.Result, error) {
	agent := &api.Agent{}
	if err := r.reader().Get(ctx, types.NamespacedName{Name: action.Spec.TargetRef.Name}, agent); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.setActionStatus(ctx, action, "Failed", "TargetNotFound", "The approved Agent target no longer exists.", nil)
		}
		return ctrl.Result{RequeueAfter: governanceActionObserveInterval}, err
	}
	if agent.UID != action.Spec.TargetRef.UID || agent.Generation != action.Spec.TargetRef.Generation {
		return ctrl.Result{}, r.setActionStatus(ctx, action, "Failed", "TargetChanged", "The Agent UID or generation no longer matches the approved proposal.", nil)
	}
	observed, err := verifyActionProjections(agent, action.Spec.ExpectedProjections)
	if err != nil {
		return ctrl.Result{}, r.setActionStatus(ctx, action, "Failed", "ProjectionMismatch", "The Agent governance projections no longer match the approved proposal.", &observed)
	}
	if action.Spec.Operation != api.GovernanceActionSuspend &&
		(agent.Status.Governance == nil || agent.Status.Governance.Phase != "Ready" ||
			!meta.IsStatusConditionTrue(agent.Status.Conditions, api.ConditionGovernanceReady)) {
		return ctrl.Result{}, r.setActionStatus(ctx, action, "Failed", "GovernanceNotReady", "The approved operation requires current Governance enforcement evidence.", &observed)
	}
	claimed, err := r.claimAction(ctx, action)
	if errors.Is(err, errGovernanceActionClaimed) {
		statusErr := r.setActionStatus(ctx, action, "Approved", "TargetBusy", "Another approved governance action is executing for this Agent.", &observed)
		return ctrl.Result{RequeueAfter: governanceActionObserveInterval}, statusErr
	}
	if err != nil {
		return ctrl.Result{RequeueAfter: governanceActionObserveInterval}, err
	}
	// Persist Executing before changing the Agent. Recovery retries the same
	// idempotent typed operation while retaining the Agent claim.
	if err := r.setActionStatus(ctx, action, "Executing", "OperationStarted", "The approved typed operation is awaiting observed enforcement evidence.", &observed); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.executeAction(ctx, action, claimed); err != nil {
		return ctrl.Result{RequeueAfter: governanceActionObserveInterval}, err
	}
	return ctrl.Result{RequeueAfter: governanceActionObserveInterval}, nil
}

func (r *GovernanceActionReconciler) observeExecution(ctx context.Context, action *api.GovernanceAction) (ctrl.Result, error) {
	agent := &api.Agent{}
	if err := r.reader().Get(ctx, types.NamespacedName{Name: action.Spec.TargetRef.Name}, agent); err != nil {
		if apierrors.IsNotFound(err) {
			return r.finishAction(ctx, action, "Failed", "TargetNotFound", "The target Agent was deleted during execution.")
		}
		return ctrl.Result{RequeueAfter: governanceActionObserveInterval}, err
	}
	if err := verifyExecutingActionTarget(agent, action); err != nil {
		return r.finishAction(ctx, action, "Failed", "TargetReplaced", "The target Agent was replaced during execution.")
	}
	if agent.Annotations[api.AnnotationGovernanceActionClaim] != string(action.UID) {
		return r.finishAction(ctx, action, "Failed", "ClaimLost", "The action no longer owns the Agent execution claim.")
	}
	observed, err := verifyActionProjections(agent, action.Spec.ExpectedProjections)
	if err != nil {
		return r.finishAction(ctx, action, "Failed", "ProjectionDrift", "The Agent governance projections changed while the action was executing.")
	}
	// A controller crash (or a resource-version conflict) can persist
	// Executing before the typed Agent mutation reaches storage. Re-apply the
	// same desired state rather than observing forever. All operations are
	// idempotent and remain protected by the Agent claim.
	if !actionMutationObserved(agent, action) {
		if err := r.executeAction(ctx, action, agent); err != nil {
			return ctrl.Result{RequeueAfter: governanceActionObserveInterval}, err
		}
		if err := r.setActionStatus(ctx, action, "Executing", "MutationRetried", "The persisted action mutation was missing and has been retried.", &observed); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: governanceActionObserveInterval}, nil
	}
	complete, message, err := r.actionObserved(ctx, action, agent)
	if err != nil {
		return ctrl.Result{RequeueAfter: governanceActionObserveInterval}, err
	}
	if !complete {
		if err := r.setActionStatus(ctx, action, "Executing", "ObservationPending", message, nil); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: governanceActionObserveInterval}, nil
	}
	return r.finishAction(ctx, action, "Succeeded", "Observed", message)
}

// verifyExecutingActionTarget permits only the generation transition caused
// by Suspend or Resume itself. Restart/Reconcile are metadata-only, so any
// generation change after the claim is target drift. The admission claim
// fence rejects competing spec writers; this check also detects a stale
// controller that recovered after such a fence was bypassed or removed.
func verifyExecutingActionTarget(agent *api.Agent, action *api.GovernanceAction) error {
	if agent.UID != action.Spec.TargetRef.UID {
		return fmt.Errorf("target UID changed")
	}
	if agent.Generation < action.Spec.TargetRef.Generation {
		return fmt.Errorf("target generation regressed")
	}
	switch action.Spec.Operation {
	case api.GovernanceActionSuspend, api.GovernanceActionResume:
		if agent.Generation > action.Spec.TargetRef.Generation+1 {
			return fmt.Errorf("target changed outside the action mutation")
		}
	case api.GovernanceActionRestart, api.GovernanceActionReconcile:
		if agent.Generation != action.Spec.TargetRef.Generation {
			return fmt.Errorf("target changed during metadata-only action")
		}
	default:
		return fmt.Errorf("unsupported governance operation %q", action.Spec.Operation)
	}
	return nil
}

func actionMutationObserved(agent *api.Agent, action *api.GovernanceAction) bool {
	switch action.Spec.Operation {
	case api.GovernanceActionSuspend:
		return agent.Spec.Suspended
	case api.GovernanceActionResume:
		return !agent.Spec.Suspended
	case api.GovernanceActionRestart, api.GovernanceActionReconcile:
		return agent.Annotations[api.AnnotationGovernanceAction] == string(action.UID)
	default:
		return false
	}
}

func (r *GovernanceActionReconciler) actionObserved(ctx context.Context, action *api.GovernanceAction, agent *api.Agent) (bool, string, error) {
	switch action.Spec.Operation {
	case api.GovernanceActionSuspend:
		if agent.Status.Phase != api.PhaseSuspended {
			return false, "Waiting for the Agent controller to report Suspended.", nil
		}
		if gateway := agent.Status.AIGateway; gateway != nil && gateway.Routed && !gateway.Blocked {
			return false, "Waiting for the routed AI Gateway key to be blocked.", nil
		}
		return true, "The Agent is suspended and any routed Gateway key is blocked.", nil
	case api.GovernanceActionResume:
		if agent.Status.Phase != api.PhaseReady {
			return false, "Waiting for the Agent controller to report Ready.", nil
		}
		if agent.Status.Governance == nil || agent.Status.Governance.Phase != "Ready" || !meta.IsStatusConditionTrue(agent.Status.Conditions, api.ConditionGovernanceReady) {
			return false, "Waiting for Governance enforcement to report Ready.", nil
		}
		return true, "The Agent and Governance enforcement report Ready.", nil
	case api.GovernanceActionRestart, api.GovernanceActionReconcile:
		if agent.Annotations[api.AnnotationGovernanceAction] != string(action.UID) {
			return false, "Waiting for the action rollout marker to be persisted.", nil
		}
		workload := &appsv1.StatefulSet{}
		if err := r.reader().Get(ctx, types.NamespacedName{Namespace: instanceNamespace(agent.Name), Name: instanceServiceName(agent.Name)}, workload); err != nil {
			return false, "", client.IgnoreNotFound(err)
		}
		if workload.Spec.Template.Annotations[api.AnnotationGovernanceAction] != string(action.UID) || !statefulSetReady(workload) {
			return false, "Waiting for the action-marked StatefulSet rollout to complete.", nil
		}
		if agent.Status.Phase != api.PhaseReady || agent.Status.Governance == nil || agent.Status.Governance.Phase != "Ready" || !meta.IsStatusConditionTrue(agent.Status.Conditions, api.ConditionGovernanceReady) {
			return false, "Waiting for the post-rollout Agent and Governance status to become Ready.", nil
		}
		return true, "The action-marked workload rollout and Governance enforcement are Ready.", nil
	default:
		return false, "", fmt.Errorf("unsupported governance operation %q", action.Spec.Operation)
	}
}

func statefulSetReady(workload *appsv1.StatefulSet) bool {
	desired := int32(1)
	if workload.Spec.Replicas != nil {
		desired = *workload.Spec.Replicas
	}
	return workload.Status.UpdatedReplicas >= desired && workload.Status.ReadyReplicas >= desired && workload.Status.ObservedGeneration >= workload.Generation
}

func (r *GovernanceActionReconciler) claimAction(ctx context.Context, action *api.GovernanceAction) (*api.Agent, error) {
	for range 3 {
		agent := &api.Agent{}
		if err := r.Get(ctx, types.NamespacedName{Name: action.Spec.TargetRef.Name}, agent); err != nil {
			return nil, err
		}
		if agent.UID != action.Spec.TargetRef.UID || agent.Generation != action.Spec.TargetRef.Generation {
			return nil, fmt.Errorf("target changed before execution claim")
		}
		if claim := agent.Annotations[api.AnnotationGovernanceActionClaim]; claim != "" && claim != string(action.UID) {
			return nil, errGovernanceActionClaimed
		}
		if agent.Annotations[api.AnnotationGovernanceActionClaim] == string(action.UID) {
			return agent, nil
		}
		before := agent.DeepCopy()
		if agent.Annotations == nil {
			agent.Annotations = map[string]string{}
		}
		agent.Annotations[api.AnnotationGovernanceActionClaim] = string(action.UID)
		if err := r.Patch(ctx, agent, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			continue
		}
		return agent, nil
	}
	return nil, fmt.Errorf("could not acquire governance action claim due to concurrent Agent updates")
}

func (r *GovernanceActionReconciler) executeAction(ctx context.Context, action *api.GovernanceAction, agent *api.Agent) error {
	before := agent.DeepCopy()
	switch action.Spec.Operation {
	case api.GovernanceActionSuspend:
		agent.Spec.Suspended = true
	case api.GovernanceActionResume:
		agent.Spec.Suspended = false
	case api.GovernanceActionRestart, api.GovernanceActionReconcile:
		if agent.Annotations == nil {
			agent.Annotations = map[string]string{}
		}
		agent.Annotations[api.AnnotationGovernanceAction] = string(action.UID)
	default:
		return fmt.Errorf("unsupported governance operation %q", action.Spec.Operation)
	}
	if reflect.DeepEqual(before.Spec, agent.Spec) && reflect.DeepEqual(before.Annotations, agent.Annotations) {
		return nil
	}
	return r.Patch(ctx, agent, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
}

func (r *GovernanceActionReconciler) finishAction(ctx context.Context, action *api.GovernanceAction, phase, reason, message string) (ctrl.Result, error) {
	if err := r.releaseActionClaim(ctx, action); err != nil {
		return ctrl.Result{RequeueAfter: governanceActionObserveInterval}, err
	}
	return ctrl.Result{}, r.setActionStatus(ctx, action, phase, reason, message, nil)
}

func (r *GovernanceActionReconciler) releaseActionClaim(ctx context.Context, action *api.GovernanceAction) error {
	agent := &api.Agent{}
	if err := r.Get(ctx, types.NamespacedName{Name: action.Spec.TargetRef.Name}, agent); err != nil {
		return client.IgnoreNotFound(err)
	}
	if agent.UID != action.Spec.TargetRef.UID || agent.Annotations[api.AnnotationGovernanceActionClaim] != string(action.UID) {
		return nil
	}
	before := agent.DeepCopy()
	delete(agent.Annotations, api.AnnotationGovernanceActionClaim)
	return r.Patch(ctx, agent, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
}

func verifyActionProjections(agent *api.Agent, expected api.GovernanceActionProjections) (api.GovernanceActionProjections, error) {
	actual := api.GovernanceActionProjections{}
	if agent.Status.Governance != nil {
		if agent.Status.Governance.Authorization != nil {
			actual.AuthorizationRevision = agent.Status.Governance.Authorization.Revision
		}
		actual.SandboxRevision = agent.Status.Governance.Projections.Sandbox.ExpectedRevision
		actual.GatewayPolicyRevision = agent.Status.Governance.Projections.Gateway.ExpectedRevision
		actual.CredentialPolicyRevision = agent.Status.Governance.Projections.CredentialPolicy.ExpectedRevision
	}
	if expected.AuthorizationRevision != "" && expected.AuthorizationRevision != actual.AuthorizationRevision {
		return actual, fmt.Errorf("authorization revision differs")
	}
	if expected.SandboxRevision != "" && expected.SandboxRevision != actual.SandboxRevision {
		return actual, fmt.Errorf("sandbox revision differs")
	}
	if expected.GatewayPolicyRevision != "" && expected.GatewayPolicyRevision != actual.GatewayPolicyRevision {
		return actual, fmt.Errorf("gateway revision differs")
	}
	if expected.CredentialPolicyRevision != "" && expected.CredentialPolicyRevision != actual.CredentialPolicyRevision {
		return actual, fmt.Errorf("credential policy revision differs")
	}
	return actual, nil
}

func (r *GovernanceActionReconciler) setActionStatus(ctx context.Context, action *api.GovernanceAction, phase, reason, message string, observed *api.GovernanceActionProjections) error {
	before := action.DeepCopy()
	action.Status.Phase, action.Status.ObservedGeneration = phase, action.Generation
	if observed != nil {
		action.Status.ObservedProjections = *observed
	}
	meta.SetStatusCondition(&action.Status.Conditions, metav1.Condition{Type: "Ready", Status: conditionForActionPhase(phase), Reason: reason, Message: message, ObservedGeneration: action.Generation, LastTransitionTime: metav1.Now()})
	if reflect.DeepEqual(before.Status, action.Status) {
		return nil
	}
	return r.Status().Patch(ctx, action, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
}

func (r *GovernanceActionReconciler) reader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}
func (r *GovernanceActionReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}
func terminalActionPhase(phase string) bool {
	return phase == "Succeeded" || phase == "Failed" || phase == "Denied" || phase == "Expired"
}
func actionRequeueAfter(expiresAt, now time.Time) time.Duration {
	if remaining := expiresAt.Sub(now); remaining > 0 {
		return remaining
	}
	return time.Second
}
func conditionForActionPhase(phase string) metav1.ConditionStatus {
	if phase == "Succeeded" {
		return metav1.ConditionTrue
	}
	if phase == "AwaitingApproval" || phase == "Executing" || phase == "Approved" {
		return metav1.ConditionUnknown
	}
	return metav1.ConditionFalse
}
