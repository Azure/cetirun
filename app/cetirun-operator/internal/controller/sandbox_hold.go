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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
)

func sandboxRequested(agent *cetirunv1alpha1.Agent, team *cetirunv1alpha1.Team) bool {
	if agent.Spec.SandboxRef != "" ||
		(agent.Spec.PolicyClass != "" && agent.Spec.PolicyClass != cetirunv1alpha1.AgentPolicyClassNormal) ||
		agent.Status.Sandbox != nil || apimeta.FindStatusCondition(agent.Status.Conditions, cetirunv1alpha1.ConditionSandboxReady) != nil {
		return true
	}
	return team != nil && (team.Spec.NormalAgentPolicyRef != "" || team.Spec.AdminAgentPolicyRef != "" ||
		len(team.Spec.SandboxRefs) != 0 || team.Spec.DefaultSandboxRef != "")
}

// holdSandbox prevents runtime renderers from ignoring new governance fields.
// It requests an existing workload to stop but is not a qualified node fence.
// Once held, clearing spec fields cannot silently return an Agent to legacy mode.
func (r *AgentReconciler) holdSandbox(ctx context.Context, agent *cetirunv1alpha1.Agent) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	before := agent.DeepCopy().Status
	var failures []error
	namespace := instanceNamespace(agent.Name)

	ns := &corev1.Namespace{}
	if agent.Spec.Agent.Type == cetirunv1alpha1.AgentTypeCustom {
		r.customPlans.Delete(agent.Name)
		if err := r.stopCustomWorkload(ctx, agent); err != nil {
			failures = append(failures, fmt.Errorf("stop sandbox-held Custom workload: %w", err))
		}
		if err := r.removeCustomDesktopRoute(ctx, agent); err != nil {
			failures = append(failures, fmt.Errorf("remove sandbox-held Custom route: %w", err))
		}
		if err := r.retryCustomCompletion(ctx, agent); err != nil {
			failures = append(failures, fmt.Errorf("complete sandbox-held Custom apply: %w", err))
		}
	} else if err := r.Get(ctx, types.NamespacedName{Name: namespace}, ns); err != nil {
		if !apierrors.IsNotFound(err) {
			failures = append(failures, fmt.Errorf("inspect sandbox-held namespace: %w", err))
		}
	} else {
		sts := &appsv1.StatefulSet{}
		key := types.NamespacedName{Namespace: namespace, Name: instanceServiceName(agent.Name)}
		if err := r.Get(ctx, key, sts); err != nil {
			if !apierrors.IsNotFound(err) {
				failures = append(failures, fmt.Errorf("inspect sandbox-held workload: %w", err))
			}
		} else if sts.Spec.Replicas == nil || *sts.Spec.Replicas != 0 ||
			(sts.Spec.PersistentVolumeClaimRetentionPolicy != nil &&
				sts.Spec.PersistentVolumeClaimRetentionPolicy.WhenScaled == appsv1.DeletePersistentVolumeClaimRetentionPolicyType) {
			base := sts.DeepCopy()
			zero := int32(0)
			sts.Spec.Replicas = &zero
			if sts.Spec.PersistentVolumeClaimRetentionPolicy == nil {
				sts.Spec.PersistentVolumeClaimRetentionPolicy = &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
					WhenDeleted: appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
				}
			}
			sts.Spec.PersistentVolumeClaimRetentionPolicy.WhenScaled = appsv1.RetainPersistentVolumeClaimRetentionPolicyType
			if err := r.Patch(ctx, sts, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
				failures = append(failures, fmt.Errorf("stop sandbox-held workload: %w", err))
			}
		}

		route := &istiov1beta1.VirtualService{}
		key.Name = virtualServiceName(agent.Name)
		if err := r.Get(ctx, key, route); err != nil {
			if !apierrors.IsNotFound(err) {
				failures = append(failures, fmt.Errorf("inspect sandbox-held route: %w", err))
			}
		} else if err := r.Delete(ctx, route); err != nil && !apierrors.IsNotFound(err) {
			failures = append(failures, fmt.Errorf("remove sandbox-held route: %w", err))
		}
	}

	reason := "UnsupportedControl"
	message := "Sandbox execution is not enabled: a qualified enforcement adapter and admission are required; workload execution is held."
	if len(failures) != 0 {
		reason = "PolicyApplyFailed"
		message = "Sandbox execution remains disabled, but workload shutdown, route removal or prior apply completion failed; retrying."
	}
	agent.Status.Phase = cetirunv1alpha1.PhaseFailed
	agent.Status.AccessURL = ""
	for _, conditionType := range []string{cetirunv1alpha1.ConditionSandboxReady, cetirunv1alpha1.ConditionReady} {
		apimeta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
			Type: conditionType, Status: metav1.ConditionFalse,
			ObservedGeneration: agent.Generation, Reason: reason, Message: message,
		})
	}
	if !reflect.DeepEqual(before, agent.Status) {
		if err := r.updateStatus(ctx, agent); err != nil {
			failures = append(failures, err)
		}
		logger.Info("Holding sandbox-bound Agent", "name", agent.Name, "reason", reason)
		if r.EventRecorder != nil {
			r.EventRecorder.Event(agent, corev1.EventTypeWarning, reason, message)
		}
	}
	if err := errors.Join(failures...); err != nil {
		logger.Error(err, "Sandbox hold could not complete", "name", agent.Name)
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: time.Minute}, nil
}
