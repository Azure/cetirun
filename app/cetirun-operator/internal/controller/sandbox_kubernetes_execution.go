// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
)

const sandboxExecutionRequeue = 5 * time.Second

// reconcileKubernetesSandbox closes the native allow path without introducing
// another API. AgentPolicy and Sandbox remain declarative inputs; the Operator
// owns preparation, admission release, one-replica execution, observation,
// routing and status.
func (r *AgentReconciler) reconcileKubernetesSandbox(ctx context.Context, agent *api.Agent) (ctrl.Result, error) {
	if r.SandboxPreparation == nil {
		return r.holdSandbox(ctx, agent)
	}
	if agent.Spec.Suspended {
		return r.holdSandbox(ctx, agent)
	}
	if !sandboxGatewayProjectionReady(agent) {
		return r.holdSandbox(ctx, agent)
	}
	// A durable Agent hold always requires a fresh held preparation before it
	// can be released.  This also recovers resume and policy-change transitions
	// whose Agent generation is newer than the previous expectation.
	if controllerutil.ContainsFinalizer(agent, sandbox.HoldFinalizer) {
		return r.startKubernetesSandbox(ctx, agent)
	}
	_, expected, err := r.readKubernetesSandboxExpectation(ctx, agent)
	if apierrors.IsNotFound(err) {
		return r.holdSandbox(ctx, agent)
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if expected.ExecutionHeld {
		return r.startKubernetesSandbox(ctx, agent)
	}
	return r.observeKubernetesSandbox(ctx, agent, expected)
}

func sandboxGatewayProjectionReady(agent *api.Agent) bool {
	if agent == nil || agent.Status.Policy == nil {
		return false
	}
	if !agent.Spec.SharedModelsEnabled() && !agent.Spec.GitHubCopilotEnabled() {
		return true
	}
	status := agent.Status.AIGateway
	return status != nil && status.Phase == api.AIGatewayPhaseReady && status.Routed && !status.Blocked &&
		status.PolicyRevision != "" && status.PolicyRevision == agent.Status.Policy.Enforcement.Gateway.DesiredRevision
}

func (r *AgentReconciler) readKubernetesSandboxExpectation(ctx context.Context, agent *api.Agent) (*corev1.ConfigMap, *w.Expectation, error) {
	namespace := &corev1.Namespace{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Name: instanceNamespace(agent.Name)}, namespace); err != nil {
		return nil, nil, err
	}
	state := &corev1.ConfigMap{}
	key := client.ObjectKey{Namespace: r.OperatorNamespace, Name: w.PodExpectationName(namespace.Name, namespace.UID)}
	if err := r.APIReader.Get(ctx, key, state); err != nil {
		return nil, nil, err
	}
	expected, err := decodeSandboxExecutionExpectation(state)
	if err != nil {
		return nil, nil, err
	}
	if expected.Agent != (w.ObjectIdentity{Name: agent.Name, UID: agent.UID}) ||
		expected.Namespace != (w.ObjectIdentity{Name: namespace.Name, UID: namespace.UID}) ||
		expected.WorkloadKind != w.AgentWorkload || expected.Workload.Name != instanceServiceName(agent.Name) ||
		expected.Binding.AgentUID != agent.UID || expected.AgentGeneration != agent.Generation {
		return nil, nil, fmt.Errorf("Sandbox execution expectation does not match the current Agent generation")
	}
	return state, expected, nil
}

func (r *AgentReconciler) startKubernetesSandbox(ctx context.Context, requested *api.Agent) (ctrl.Result, error) {
	if controllerutil.ContainsFinalizer(requested, sandbox.HoldFinalizer) {
		prepareContext, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := r.prepareSandboxHeld(prepareContext, requested)
		cancel()
		if err != nil {
			_ = r.sandboxPreparationStatus(ctx, requested, false, err)
			return ctrl.Result{RequeueAfter: sandboxExecutionRequeue}, err
		}
	}
	agent := &api.Agent{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Name: requested.Name}, agent); err != nil {
		return ctrl.Result{}, err
	}
	if agent.UID != requested.UID || agent.Generation != requested.Generation || agent.Spec.Suspended ||
		!agent.DeletionTimestamp.IsZero() || !sandboxGatewayProjectionReady(agent) {
		return r.holdSandbox(ctx, agent)
	}
	state, expected, err := r.readKubernetesSandboxExpectation(ctx, agent)
	if err != nil {
		return ctrl.Result{}, err
	}
	workload := &appsv1.StatefulSet{}
	workloadKey := client.ObjectKey{Namespace: expected.Namespace.Name, Name: expected.Workload.Name}
	if err := r.APIReader.Get(ctx, workloadKey, workload); err != nil {
		return ctrl.Result{}, err
	}
	owned := sandboxBuiltinOwned(workload, agent)
	identityMatches := workload.UID == expected.Workload.UID
	revisionMatches := workload.Annotations[w.RevisionAnnotation] == expected.Revision
	templateMatches := w.SamePodTemplate(workload.Spec.Template, expected.Template)
	controllerRevisionMatches := workload.Status.UpdateRevision == expected.ControllerRevision && expected.ControllerRevision != ""
	replicas := ptr.Deref(workload.Spec.Replicas, -1)
	if !owned || !identityMatches || !revisionMatches || !templateMatches || !controllerRevisionMatches ||
		(replicas != 0 && replicas != 1) {
		return ctrl.Result{}, fmt.Errorf("Sandbox prepared StatefulSet changed before start (owned=%t identity=%t revision=%t template=%t controllerRevision=%t replicas=%d)",
			owned, identityMatches, revisionMatches, templateMatches, controllerRevisionMatches, replicas)
	}
	// The Pod expectation remains held until the last step below, so removing
	// the Agent marker does not by itself authorize Pod admission.  Release the
	// marker before requesting a replica: if either write fails, the next pass
	// can retry from a stopped StatefulSet or from the still-held expectation.
	// Scaling first would leave an unrecoverable replicas=1/finalizer-present
	// split if admission rejected the finalizer write.
	if controllerutil.ContainsFinalizer(agent, sandbox.HoldFinalizer) {
		finalizers := slices.DeleteFunc(slices.Clone(agent.Finalizers), func(value string) bool {
			return value == sandbox.HoldFinalizer
		})
		raw, err := json.Marshal(map[string]any{"metadata": map[string]any{
			"resourceVersion": agent.ResourceVersion, "finalizers": finalizers,
		}})
		if err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Patch(ctx, agent, client.RawPatch(types.MergePatchType, raw)); err != nil {
			return ctrl.Result{}, fmt.Errorf("release Sandbox execution hold: %w", err)
		}
		if err := r.APIReader.Get(ctx, client.ObjectKey{Name: agent.Name}, agent); err != nil {
			return ctrl.Result{}, err
		}
	}
	if replicas == 0 {
		base := workload.DeepCopy()
		workload.Spec.Replicas = ptr.To[int32](1)
		if err := r.Patch(ctx, workload, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
			return ctrl.Result{}, fmt.Errorf("start Sandbox StatefulSet: %w", err)
		}
	}
	if err := r.releaseKubernetesSandboxExpectation(ctx, state, expected, agent); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.sandboxExecutionPendingStatus(ctx, agent, "SandboxStarting", "Policy controls are applied; waiting for the Sandbox Pod to become Ready."); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: sandboxExecutionRequeue}, nil
}

func (r *AgentReconciler) releaseKubernetesSandboxExpectation(ctx context.Context, state *corev1.ConfigMap,
	expected *w.Expectation, agent *api.Agent,
) error {
	if state == nil || expected == nil || agent == nil || controllerutil.ContainsFinalizer(agent, sandbox.HoldFinalizer) {
		return fmt.Errorf("Sandbox expectation release requires an unheld current Agent")
	}
	agentSource, err := w.AgentSourceReference(agent)
	if err != nil {
		return err
	}
	copy := *expected
	copy.Template = *expected.Template.DeepCopy()
	copy.Sources = slices.Clone(expected.Sources)
	found := false
	for i := range copy.Sources {
		if copy.Sources[i].IsAgent() {
			if found {
				return fmt.Errorf("Sandbox expectation contains duplicate Agent sources")
			}
			copy.Sources[i], found = agentSource, true
		}
	}
	if !found {
		return fmt.Errorf("Sandbox expectation has no Agent source")
	}
	copy.ExecutionHeld = false
	raw, err := json.Marshal(copy)
	if err != nil {
		return err
	}
	base := state.DeepCopy()
	state.Data = map[string]string{w.PodExpectationKey: string(raw)}
	if err := r.Patch(ctx, state, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("release Sandbox Pod expectation: %w", err)
	}
	return nil
}

func (r *AgentReconciler) observeKubernetesSandbox(ctx context.Context, agent *api.Agent, expected *w.Expectation) (ctrl.Result, error) {
	attempt, err := r.resolveRunningKubernetesSandbox(ctx, agent)
	if err != nil {
		result, holdErr := r.holdSandbox(ctx, agent)
		return result, errors.Join(err, holdErr)
	}
	if expected.ExecutionHeld || expected.Binding != attempt.plan.Binding || expected.Revision != attempt.plan.Revision {
		result, holdErr := r.holdSandbox(ctx, agent)
		return result, errors.Join(fmt.Errorf("Sandbox execution inputs changed"), holdErr)
	}
	workload := &appsv1.StatefulSet{}
	key := client.ObjectKey{Namespace: expected.Namespace.Name, Name: expected.Workload.Name}
	if err := r.APIReader.Get(ctx, key, workload); err != nil {
		result, holdErr := r.holdSandbox(ctx, agent)
		return result, errors.Join(err, holdErr)
	}
	if !sandboxBuiltinOwned(workload, agent) || workload.UID != expected.Workload.UID ||
		ptr.Deref(workload.Spec.Replicas, 0) != 1 || workload.Status.UpdateRevision != expected.ControllerRevision ||
		!w.SamePodTemplate(workload.Spec.Template, expected.Template) {
		result, holdErr := r.holdSandbox(ctx, agent)
		return result, errors.Join(fmt.Errorf("running Sandbox StatefulSet differs from its expectation"), holdErr)
	}
	pod := &corev1.Pod{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: expected.Namespace.Name, Name: expected.Workload.Name + "-0"}, pod); err != nil {
		if apierrors.IsNotFound(err) {
			if err := r.sandboxExecutionPendingStatus(ctx, agent, "SandboxStarting", "Waiting for the Sandbox Pod to be created."); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: sandboxExecutionRequeue}, nil
		}
		return ctrl.Result{}, err
	}
	if pod.UID == "" || !pod.DeletionTimestamp.IsZero() || pod.Spec.NodeName == "" || !sandboxPodReady(pod) {
		if agent.Status.Phase == api.PhaseReady {
			return r.holdSandbox(ctx, agent)
		}
		if err := r.sandboxExecutionPendingStatus(ctx, agent, "SandboxStarting", "Waiting for the Sandbox Pod and all containers to become Ready."); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: sandboxExecutionRequeue}, nil
	}
	// Scheduling is the one Kubernetes-owned PodSpec transition after admission.
	// Validate the final Pod against the protected template with only the
	// authoritative persisted node assignment grandfathered, matching the Pod
	// admission update path.
	observedExpectation := *expected
	observedExpectation.Template = *expected.Template.DeepCopy()
	observedExpectation.Template.Spec.NodeName = pod.Spec.NodeName
	if err := w.ValidatePod(pod, observedExpectation); err != nil {
		result, holdErr := r.holdSandbox(ctx, agent)
		return result, errors.Join(err, holdErr)
	}
	node := &corev1.Node{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Name: pod.Spec.NodeName}, node); err != nil {
		return ctrl.Result{}, err
	}
	if node.UID == "" || !node.DeletionTimestamp.IsZero() || !sandboxNodeReady(node) {
		return r.holdSandbox(ctx, agent)
	}
	now := time.Now().UTC()
	identity := sandbox.ExecutionIdentity{Binding: attempt.plan.Binding, Revision: attempt.plan.Revision,
		PodUID: pod.UID, NodeUID: node.UID, EnforcementEpoch: "pod-" + string(pod.UID)}
	observation := &sandbox.Observation{ExecutionIdentity: identity, ObservedAt: now,
		RuntimeVerified: true, PodVerified: true, ControlsVerified: true}
	input := SandboxStatusInput{AgentGeneration: agent.Generation, Resolution: attempt.resolution, Plan: attempt.plan,
		Expected: identity, Observation: observation, Now: now, MaxAge: time.Minute}
	if _, err := UpdateSandboxAgentStatus(agent, input); err != nil {
		return ctrl.Result{}, err
	}
	if attempt.plan.Network.IngressMode == api.AgentPolicyIngressAuthenticated {
		if err := r.ensureVirtualService(ctx, agent); err != nil {
			return ctrl.Result{}, err
		}
		agent.Status.AccessURL = accessURLPathBased(agent.Name, r.DomainSuffix)
		apimeta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{Type: api.ConditionIngressReady,
			Status: metav1.ConditionTrue, Reason: "AuthenticatedIngressReady", Message: "Authenticated Sandbox route is published.",
			ObservedGeneration: agent.Generation, LastTransitionTime: metav1.NewTime(now)})
	} else {
		if err := r.withdrawKubernetesSandboxRoute(ctx, agent); err != nil {
			return ctrl.Result{}, err
		}
		agent.Status.AccessURL = ""
	}
	agent.Status.Phase = api.PhaseReady
	agent.Status.ObservedGeneration = agent.Generation
	apimeta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{Type: api.ConditionReady,
		Status: metav1.ConditionTrue, Reason: "SandboxReady", Message: "Sandbox workload and current policy controls are Ready.",
		ObservedGeneration: agent.Generation, LastTransitionTime: metav1.NewTime(now)})
	if err := r.updateStatus(ctx, agent); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
}

func sandboxPodReady(pod *corev1.Pod) bool {
	return pod != nil && slices.ContainsFunc(pod.Status.Conditions, func(condition corev1.PodCondition) bool {
		return condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue
	})
}

func sandboxNodeReady(node *corev1.Node) bool {
	return node != nil && slices.ContainsFunc(node.Status.Conditions, func(condition corev1.NodeCondition) bool {
		return condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue
	})
}

func (r *AgentReconciler) resolveRunningKubernetesSandbox(ctx context.Context, requested *api.Agent) (*sandboxPreparationAttempt, error) {
	attempt, err := r.resolveSandboxExecutionInputs(ctx, requested, false)
	if err != nil {
		return nil, err
	}
	source, ok := r.SandboxPreparation.Prerequisites.(interface {
		ResolveRunning(context.Context, *api.Agent, *api.Team) (SandboxPrerequisites, error)
	})
	if !ok {
		return nil, fmt.Errorf("running Sandbox prerequisite reader is unavailable")
	}
	attempt.prerequisites, err = source.ResolveRunning(ctx, attempt.agent.DeepCopy(), attempt.team.DeepCopy())
	if err != nil {
		return nil, err
	}
	if err := r.inheritSandboxPlacement(ctx, attempt.agent, &attempt.prerequisites); err != nil {
		return nil, err
	}
	if err := r.sandboxPreparationInput(ctx, attempt); err != nil {
		return nil, err
	}
	platform := attempt.configuration.Platforms[attempt.resolution.Sandbox.Type]
	if err := sandboxpolicy.VerifyPolicyGrants(ctx, r.APIReader, attempt.selectedPolicy(), platform.Grants); err != nil {
		return nil, err
	}
	attempt.plan, err = sandboxpolicy.Compile(sandboxpolicy.CompileInput{Resolution: *attempt.resolution,
		AgentName: attempt.agent.Name, Namespace: attempt.input.Namespace.Name,
		WorkloadProbePort: ptr.Deref(attempt.agent.Spec.Agent.Port, 0), Platform: platform})
	if err != nil {
		return nil, err
	}
	return attempt, nil
}

func (r *AgentReconciler) sandboxExecutionPendingStatus(ctx context.Context, expected *api.Agent, reason, message string) error {
	agent := &api.Agent{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Name: expected.Name}, agent); err != nil {
		return err
	}
	if agent.UID != expected.UID || agent.Generation != expected.Generation {
		return fmt.Errorf("Sandbox Agent changed before execution status")
	}
	agent.Status.Phase = api.PhaseProvisioning
	agent.Status.AccessURL = ""
	for _, kind := range []string{api.ConditionReady, api.ConditionSandboxReady, api.ConditionIngressReady} {
		apimeta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{Type: kind, Status: metav1.ConditionFalse,
			Reason: reason, Message: message, ObservedGeneration: agent.Generation})
	}
	return r.updateStatus(ctx, agent)
}

func (r *AgentReconciler) withdrawKubernetesSandboxRoute(ctx context.Context, agent *api.Agent) error {
	route := &istiov1beta1.VirtualService{}
	key := types.NamespacedName{Namespace: instanceNamespace(agent.Name), Name: virtualServiceName(agent.Name)}
	if err := r.APIReader.Get(ctx, key, route); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !sandboxBuiltinOwned(route, agent) {
		return fmt.Errorf("Sandbox route ownership is unconfirmed")
	}
	return client.IgnoreNotFound(r.Delete(ctx, route, client.Preconditions{UID: &route.UID, ResourceVersion: &route.ResourceVersion}))
}
