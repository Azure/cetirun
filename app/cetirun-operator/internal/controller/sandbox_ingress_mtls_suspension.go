// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	istio "istio.io/client-go/pkg/apis/networking/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// A missing or stale certificate approval prevents execution, but not an
// independently confirmed suspension. Keep the approval diagnosis for resume.
func (r *AgentReconciler) holdSuspendedIngressMTLS(ctx context.Context, agent *api.Agent, reason, message string) (ctrl.Result, error) {
	var failures []error
	if !controllerutil.ContainsFinalizer(agent, sandbox.HoldFinalizer) {
		before := agent.DeepCopy()
		controllerutil.AddFinalizer(agent, sandbox.HoldFinalizer)
		failures = append(failures, r.Patch(ctx, agent, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})))
	}
	// Repeat revocation and cleanup after prerequisite reconciliation. Neither
	// an earlier successful stop nor a missing reservation proves current stop.
	failures = append(failures, r.stopReconciledSandboxForSuspension(ctx, agent))
	failures = append(failures, r.observeIngressMTLSSuspension(ctx, agent))
	stopErr := errors.Join(failures...)
	agent.Status.Phase, agent.Status.AccessURL = api.PhasePending, ""
	stopReason, stopMessage := "SuspensionPending", "Suspension requested; waiting for shutdown and access revocation"
	if stopErr == nil {
		agent.Status.Phase = api.PhaseSuspended
		agent.Status.ObservedGeneration = agent.Generation
		stopReason, stopMessage = "Suspended", "Suspension is confirmed; mTLS approval must be renewed before resume"
	}
	for _, kind := range []string{api.ConditionReady, api.ConditionPodReady} {
		setCondition(&agent.Status.Conditions, kind, metav1.ConditionFalse, stopReason, stopMessage)
	}
	for _, kind := range []string{api.ConditionSandboxReady, api.ConditionIngressReady} {
		setCondition(&agent.Status.Conditions, kind, metav1.ConditionFalse, reason, message)
	}
	for index := range agent.Status.Conditions {
		switch agent.Status.Conditions[index].Type {
		case api.ConditionReady, api.ConditionPodReady, api.ConditionSandboxReady, api.ConditionIngressReady:
			agent.Status.Conditions[index].ObservedGeneration = agent.Generation
		}
	}
	if err := errors.Join(stopErr, r.updateStatus(ctx, agent)); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
}

func (r *AgentReconciler) observeIngressMTLSSuspension(ctx context.Context, agent *api.Agent) error {
	namespace := &corev1.Namespace{}
	if err := r.customReader().Get(ctx, client.ObjectKey{Name: instanceNamespace(agent.Name)}, namespace); err != nil {
		return err
	}
	state := &corev1.ConfigMap{}
	err := r.customReader().Get(ctx, client.ObjectKey{
		Namespace: r.OperatorNamespace, Name: w.PodExpectationName(namespace.Name, namespace.UID),
	}, state)
	if apierrors.IsNotFound(err) {
		if err := r.observeInitialIngressMTLSSuspension(ctx, agent, namespace); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		// The stopped workload belongs to the previous running generation.
		// Read its cleanup identities without accepting the stale TLS sources.
		expected, err := w.ReadReconcileExpectationForCleanup(state)
		if err != nil || expected.Agent != (w.ObjectIdentity{Name: agent.Name, UID: agent.UID}) ||
			expected.Namespace != (w.ObjectIdentity{Name: namespace.Name, UID: namespace.UID}) {
			return fmt.Errorf("mTLS suspension requires its recorded Sandbox lifetime")
		}
		if err := r.observeReconciledSandboxShutdown(ctx, agent, state, expected); err != nil {
			return err
		}
	}
	if err := r.customReader().Get(ctx, client.ObjectKey{
		Namespace: namespace.Name, Name: virtualServiceName(agent.Name),
	}, &istio.VirtualService{}); !apierrors.IsNotFound(err) {
		if err != nil {
			return err
		}
		return fmt.Errorf("mTLS suspension waits for native route removal")
	}
	return nil
}

func (r *AgentReconciler) observeInitialIngressMTLSSuspension(ctx context.Context, agent *api.Agent, namespace *corev1.Namespace) error {
	if agent.Status.Sandbox != nil || agent.Status.HelmReleaseName != "" ||
		agent.Status.HelmMaintenance != nil || agent.Spec.RestartRequestID != "" || agent.Status.ObservedRestartRequestID != "" ||
		agent.Spec.PlannedMigration != nil || agent.Status.PlannedMigration != nil ||
		controllerutil.ContainsFinalizer(agent, sandbox.PreparationFinalizer) {
		return fmt.Errorf("mTLS suspension cannot discard a missing execution expectation")
	}
	if err := r.verifySuspendedReconciledBootstrap(ctx, agent); err != nil {
		return err
	}
	for _, key := range []client.ObjectKey{
		r.sandboxPreparationKey(agent),
		{Namespace: r.sandboxGrantJournal().Namespace, Name: sandboxpolicy.GrantJournalName(agent.UID)},
	} {
		if err := r.customReader().Get(ctx, key, &corev1.ConfigMap{}); err == nil {
			return fmt.Errorf("initial mTLS suspension has preparation or grant history without its execution expectation")
		} else if !apierrors.IsNotFound(err) {
			return err
		}
	}
	if r.SandboxNativeCoordination != nil {
		_, found, err := r.SandboxNativeCoordination.ReadNative(ctx, agent.UID)
		if err != nil {
			return err
		}
		if found {
			return fmt.Errorf("initial mTLS suspension still has an execution reservation")
		}
	}
	for _, list := range []client.ObjectList{&appsv1.StatefulSetList{}, &appsv1.DeploymentList{}, &appsv1.ReplicaSetList{}, &corev1.PodList{}} {
		if err := r.customReader().List(ctx, list, client.InNamespace(namespace.Name), client.Limit(1)); err != nil {
			return err
		}
		if apimeta.LenList(list) != 0 || list.GetContinue() != "" {
			return fmt.Errorf("initial mTLS suspension requires no submitted workload or remaining Pod")
		}
	}
	return nil
}
