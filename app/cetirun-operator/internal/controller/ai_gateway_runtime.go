// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// gatewayPodConfigured inspects the applied workload, not key status: a key
// can be ready while Helm has not yet replaced an old direct-provider Pod.
func gatewayPodConfigured(spec corev1.PodSpec, agentType api.AgentType, gatewayURL string) bool {
	if len(spec.Containers) == 0 {
		return false
	}
	env := map[string]corev1.EnvVar{}
	for _, item := range spec.Containers[0].Env {
		if _, duplicate := env[item.Name]; duplicate {
			return false
		}
		env[item.Name] = item
	}
	literal := func(name, value string) bool {
		item, ok := env[name]
		return ok && item.ValueFrom == nil && item.Value == value
	}
	key := func(item corev1.EnvVar) bool {
		if item.Value != "" || item.ValueFrom == nil || item.ValueFrom.SecretKeyRef == nil {
			return false
		}
		ref := item.ValueFrom.SecretKeyRef
		return ref.Name == aiGatewayKeySecretName && ref.Key == aiGatewayKeySecretField && (ref.Optional == nil || !*ref.Optional)
	}
	if agentType == api.AgentTypeCustom {
		return literal("OPENAI_BASE_URL", gatewayURL+"/v1") && key(env["OPENAI_API_KEY"])
	}
	marked := false
	for _, init := range spec.InitContainers {
		if init.Name != "init-home-data" {
			continue
		}
		count := 0
		for _, item := range init.Env {
			if item.Name == "CETIRUN_AI_GATEWAY" {
				count++
				marked = item.Value == "true" && item.ValueFrom == nil
			}
		}
		marked = marked && count == 1
	}
	if !marked {
		return false
	}
	if agentType == api.AgentTypeOpenClaw {
		return key(env["OPENAI_API_KEY"])
	}
	if agentType != api.AgentTypeHermes || !literal("CETIRUN_AI_GATEWAY", "true") {
		return false
	}
	for _, item := range env {
		if key(item) {
			return true
		}
	}
	return false
}

// stopGatewayRuntime never needs a Chart or the Gateway management API. HOME
// and key identity survive the transition. Each independent stop is attempted.
func (r *AgentReconciler) stopGatewayRuntime(ctx context.Context, agent *api.Agent) error {
	if agent.Spec.Agent.Type == api.AgentTypeCustom {
		return errors.Join(r.stopCustomWorkload(ctx, agent), r.stopCustomTerminal(ctx, agent),
			r.stopCustomCopilot(ctx, agent), r.removeCustomPlatformRoute(ctx, agent))
	}
	return errors.Join(r.stopGatewayNativeWorkload(ctx, agent), r.suspendCopilotProxy(ctx, agent), r.removeRegistryBuiltinRoute(ctx, agent))
}

func (r *AgentReconciler) stopGatewayNativeWorkload(ctx context.Context, agent *api.Agent) error {
	sts := &appsv1.StatefulSet{}
	key := client.ObjectKey{Namespace: instanceNamespace(agent.Name), Name: instanceServiceName(agent.Name)}
	if err := r.customReader().Get(ctx, key, sts); err != nil {
		return client.IgnoreNotFound(err)
	}
	ns := &corev1.Namespace{}
	if err := r.customReader().Get(ctx, client.ObjectKey{Name: key.Namespace}, ns); err != nil {
		return err
	}
	if !hasAgentOwner(ns, agent) ||
		(len(sts.OwnerReferences) != 0 && !hasAgentOwner(sts, agent)) ||
		(!hasAgentOwner(sts, agent) && (sts.Annotations["meta.helm.sh/release-name"] != helmReleaseName(agent.Name) ||
			sts.Annotations["meta.helm.sh/release-namespace"] != key.Namespace || sts.Labels["app.kubernetes.io/managed-by"] != "Helm")) {
		return fmt.Errorf("native workload ownership must be confirmed before AI Gateway migration")
	}
	if sts.Spec.Replicas != nil && *sts.Spec.Replicas == 0 &&
		(sts.Spec.PersistentVolumeClaimRetentionPolicy == nil || sts.Spec.PersistentVolumeClaimRetentionPolicy.WhenScaled != appsv1.DeletePersistentVolumeClaimRetentionPolicyType) {
		return nil
	}
	before := sts.DeepCopy()
	zero := int32(0)
	sts.Spec.Replicas = &zero
	if sts.Spec.PersistentVolumeClaimRetentionPolicy == nil {
		sts.Spec.PersistentVolumeClaimRetentionPolicy = &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{WhenDeleted: appsv1.RetainPersistentVolumeClaimRetentionPolicyType}
	}
	sts.Spec.PersistentVolumeClaimRetentionPolicy.WhenScaled = appsv1.RetainPersistentVolumeClaimRetentionPolicyType
	return r.Patch(ctx, sts, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
}

func (r *AgentReconciler) gatewayRuntimeWaiting(ctx context.Context, agent *api.Agent, reason string) (ctrl.Result, error) {
	stopErr := r.stopGatewayRuntime(ctx, agent)
	agent.Status.AccessURL = ""
	agent.Status.Phase = api.PhasePending
	agent.Status.ObservedGeneration = 0
	agent.Status.ObservedOperatorHash = ""
	for _, condition := range []string{api.ConditionReady, api.ConditionPodReady, api.ConditionIngressReady, api.ConditionAgentConfigReady} {
		setCondition(&agent.Status.Conditions, condition, metav1.ConditionFalse, reason,
			"Agent execution requires an AI Gateway binding and a compatible Chart; old workload shutdown is requested")
	}
	return ctrl.Result{RequeueAfter: 30 * time.Second}, errors.Join(stopErr, r.updateStatus(ctx, agent))
}

// migrateDirectGatewayRuntime runs before configuration lookup can fail. Once
// the old workload is stopped and its Pods are gone, normal reconciliation can
// replace the old template; returning forever on that template would deadlock.
func (r *AgentReconciler) migrateDirectGatewayRuntime(ctx context.Context, agent *api.Agent) (bool, ctrl.Result, error) {
	observationFailed := func(err error) (bool, ctrl.Result, error) {
		reason := "AIGatewayObservationUnavailable"
		if agent.Spec.Agent.Type == api.AgentTypeCustom && apierrors.IsForbidden(err) {
			reason = "CustomPermissionDenied"
		}
		result, stopErr := r.gatewayRuntimeWaiting(ctx, agent, reason)
		return false, result, errors.Join(err, stopErr)
	}
	ns := &corev1.Namespace{}
	if err := r.customReader().Get(ctx, client.ObjectKey{Name: instanceNamespace(agent.Name)}, ns); err != nil {
		if apierrors.IsNotFound(err) {
			return true, ctrl.Result{}, nil
		}
		return observationFailed(err)
	}
	sts := &appsv1.StatefulSet{}
	err := r.customReader().Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: instanceServiceName(agent.Name)}, sts)
	if err != nil && !apierrors.IsNotFound(err) {
		return observationFailed(err)
	}
	hasWorkload := err == nil
	needsStop := hasWorkload && !gatewayPodConfigured(sts.Spec.Template.Spec, agent.Spec.Agent.Type, r.AIGateway.URL)
	pods := &corev1.PodList{}
	if err := r.customReader().List(ctx, pods, client.InNamespace(ns.Name)); err != nil {
		return observationFailed(err)
	}
	// Include orphaned Pods from a same-name StatefulSet replacement. Their old
	// UID prevents automatic adoption, but must never permit new execution.
	oldPod := false
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		for _, owner := range pod.OwnerReferences {
			if owner.Kind == "StatefulSet" && owner.Name == instanceServiceName(agent.Name) &&
				!gatewayPodConfigured(pod.Spec, agent.Spec.Agent.Type, r.AIGateway.URL) {
				oldPod = true
			}
		}
	}
	if !r.AIGateway.Enabled || oldPod || (needsStop && (sts.Spec.Replicas == nil || *sts.Spec.Replicas != 0)) {
		result, err := r.gatewayRuntimeWaiting(ctx, agent, "AIGatewayMigrationPending")
		return false, result, err
	}
	return true, ctrl.Result{}, nil
}

// Suspension blocks an existing key without provisioning a new one or consulting
// the model catalog. An outage must not prevent the independent compute stop.
func (r *AgentReconciler) suspendGatewayKey(ctx context.Context, agent *api.Agent) error {
	secret, err := r.aiGatewayKeySecret(ctx, agent)
	if err != nil {
		return err
	}
	if secret == nil && !aiGatewayKeyWasIssued(agent) {
		return nil
	}
	gateway, err := r.aiGatewayClient(ctx)
	if err != nil {
		return err
	}
	blocked, err := r.blockAIGatewayKeyIfPresent(ctx, gateway, agent, aiGatewayReasonSuspended)
	if err == nil && blocked {
		status := ensureAIGatewayStatus(agent)
		status.Blocked, status.Routed = true, false
	}
	return err
}
