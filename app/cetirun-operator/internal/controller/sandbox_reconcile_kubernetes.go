// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"
	"slices"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *AgentReconciler) checkMissingReconciledGrantJournal(ctx context.Context, agent *api.Agent) error {
	recorded, err := r.recordedReconciledSandbox(ctx, agent)
	if err != nil || !recorded {
		return err
	}
	namespace := &corev1.Namespace{}
	if err := r.customReader().Get(ctx, client.ObjectKey{Name: instanceNamespace(agent.Name)}, namespace); err != nil {
		// Journal deletion follows confirmed grant revocation and namespace
		// absence. A later finalizer retry need not recreate an empty journal.
		return client.IgnoreNotFound(err)
	}
	state := &corev1.ConfigMap{}
	if err := r.customReader().Get(ctx, client.ObjectKey{
		Namespace: r.OperatorNamespace, Name: w.PodExpectationName(namespace.Name, namespace.UID),
	}, state); err != nil {
		if apierrors.IsNotFound(err) && agent.Status.Sandbox == nil &&
			!isConditionTrue(agent.Status.Conditions, api.ConditionHelmDeployed) {
			return nil
		}
		return fmt.Errorf("common Sandbox cleanup authority is unavailable: %w", err)
	}
	expected, err := w.ReadReconcileExpectationForCleanup(state)
	if err != nil {
		return err
	}
	if state.Annotations[sandboxApplyStateAnnotation] == sandboxApplyInitializing &&
		!slices.ContainsFunc(expected.Workloads, func(workload w.RenderedWorkload) bool { return workload.Identity.UID != "" }) {
		return nil
	}
	return fmt.Errorf("recorded common Sandbox execution requires its missing grant journal; recovery is required")
}

func reconciledKubernetesRoleSources(plan *sandboxpolicy.Plan, platform sandboxpolicy.Platform) ([]w.SourceReference, error) {
	var sources []w.SourceReference
	for _, binding := range plan.Identity.Bindings {
		for _, approval := range platform.Grants {
			if approval.PolicyUID != plan.Binding.AgentPolicyUID || approval.Namespace != binding.Namespace ||
				approval.RoleRef != binding.RoleRef {
				continue
			}
			source := w.SourceReference{
				APIVersion: rbacv1.SchemeGroupVersion.String(), Kind: "ClusterRole", Name: approval.RoleRef.Name,
				UID: approval.RoleUID, ResourceVersion: approval.RoleResourceVersion,
			}
			index := slices.IndexFunc(sources, func(old w.SourceReference) bool { return w.SourceKey(old) == w.SourceKey(source) })
			if index >= 0 {
				if sources[index] != source {
					return nil, fmt.Errorf("Kubernetes role approvals have conflicting source pins")
				}
				continue
			}
			sources = append(sources, source)
		}
	}
	return sources, nil
}

func (r *AgentReconciler) validateReconciledKubernetesCA(ctx context.Context, source w.SourceReference) error {
	ca := &corev1.ConfigMap{}
	if err := r.customReader().Get(ctx, client.ObjectKey{Namespace: source.Namespace, Name: source.Name}, ca); err != nil {
		return err
	}
	// Kubernetes publishes this one namespaced CA without an Agent owner.
	// Its UID and contents join the normal admission source inventory.
	if source.Name != "kube-root-ca.crt" || ca.UID == "" || ca.ResourceVersion == "" ||
		!ca.DeletionTimestamp.IsZero() || len(ca.OwnerReferences) != 0 || ca.Data["ca.crt"] == "" {
		return fmt.Errorf("Kubernetes API access requires the namespace's published root CA")
	}
	return nil
}

func (r *AgentReconciler) confirmReconciledKubernetesGrants(ctx context.Context, agent *api.Agent, prepared *helm.PreparedRelease) error {
	rendered, err := prepared.ReconcileExpectation()
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(rendered.Controls, w.KubernetesBindingResource) {
		return nil
	}
	if agent.Spec.Suspended {
		return fmt.Errorf("suspended reconciliation cannot apply Kubernetes workload grants")
	}
	if err := prepared.AuthorizeSandboxApply(ctx); err != nil {
		return err
	}
	state := &corev1.ConfigMap{}
	if err := r.customReader().Get(ctx, client.ObjectKey{
		Namespace: r.OperatorNamespace, Name: w.PodExpectationName(rendered.Namespace.Name, rendered.Namespace.UID),
	}, state); err != nil {
		return err
	}
	expected, err := w.ReadReconcileExpectation(state)
	if err != nil || expected.Agent != rendered.Agent || expected.PlanHash != rendered.PlanHash ||
		expected.Revision != rendered.Revision || expected.AgentGeneration != agent.Generation {
		return fmt.Errorf("Kubernetes grants require the current reconciled expectation")
	}
	bindings, err := expected.KubernetesBindings()
	if err != nil {
		return err
	}
	confirmed, err := r.sandboxBindingUIDs(ctx, bindings)
	if err != nil {
		return err
	}
	journal := r.sandboxGrantJournal()
	if err := journal.Record(ctx, agent.Name, agent.UID, confirmed); err != nil {
		return err
	}
	if err := journal.VerifyKubernetesBindings(ctx, agent.Name, agent.UID, bindings); err != nil {
		return err
	}
	return prepared.AuthorizeSandboxApply(ctx)
}
