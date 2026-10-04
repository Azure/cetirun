// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func (r *AgentReconciler) sandboxBootstrapForCleanup(ctx context.Context, agent *api.Agent) (*corev1.ConfigMap, *sandboxBootstrapReceipt, error) {
	if r.OperatorNamespace == "" {
		if controllerutil.ContainsFinalizer(agent, sandbox.BootstrapFinalizer) {
			return nil, nil, fmt.Errorf("Sandbox bootstrap cleanup requires the operator state namespace")
		}
		return nil, nil, nil
	}
	object, receipt, err := r.readSandboxBootstrap(ctx, agent)
	if apierrors.IsNotFound(err) {
		if controllerutil.ContainsFinalizer(agent, sandbox.BootstrapFinalizer) {
			return nil, nil, fmt.Errorf("Sandbox bootstrap receipt is missing; cleanup requires recovery")
		}
		return nil, nil, nil
	}
	return object, receipt, err
}

// This validates ownership, not writer fencing. The preparation cleanup gate
// remains mandatory for any Agent that has progressed beyond bootstrap.
func (r *AgentReconciler) checkSandboxBootstrapCleanup(ctx context.Context, agent *api.Agent) error {
	object, receipt, err := r.sandboxBootstrapForCleanup(ctx, agent)
	if err != nil || receipt == nil {
		return err
	}
	for i, desired := range r.sandboxBootstrapObjects(agent, object, receipt) {
		current := desired.DeepCopyObject().(client.Object)
		if err := r.customReader().Get(ctx, client.ObjectKeyFromObject(desired), current); apierrors.IsNotFound(err) {
			if i == 0 {
				return nil
			}
			continue
		} else if err != nil {
			return err
		}
		if err := validateSandboxBootstrapObject(current, desired, receipt.Objects[i].UID, true); err != nil {
			return err
		}
	}
	var pods corev1.PodList
	if err := r.customReader().List(ctx, &pods, client.InNamespace(receipt.Namespace), client.Limit(1)); err != nil {
		return err
	}
	if len(pods.Items) != 0 || pods.Continue != "" {
		return fmt.Errorf("Sandbox bootstrap cleanup waits for Pods to terminate")
	}
	return ctx.Err()
}

func (r *AgentReconciler) releaseSandboxBootstrapNamespace(ctx context.Context, agent *api.Agent, namespace *corev1.Namespace) error {
	if !controllerutil.ContainsFinalizer(namespace, sandbox.BootstrapFinalizer) {
		return nil
	}
	object, receipt, err := r.sandboxBootstrapForCleanup(ctx, agent)
	if err != nil {
		return err
	}
	if receipt == nil || agent.DeletionTimestamp.IsZero() {
		return fmt.Errorf("Sandbox namespace bootstrap finalizer requires its deleting Agent and receipt")
	}
	if err := validateSandboxBootstrapObject(namespace, r.sandboxBootstrapObjects(agent, object, receipt)[0], receipt.Objects[0].UID, true); err != nil {
		return err
	}
	base := namespace.DeepCopy()
	controllerutil.RemoveFinalizer(namespace, sandbox.BootstrapFinalizer)
	return r.Patch(ctx, namespace, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

func (r *AgentReconciler) finishSandboxBootstrap(ctx context.Context, agent *api.Agent) error {
	if err := r.refreshSandboxCleanupAgent(ctx, agent); err != nil {
		return err
	}
	object, receipt, err := r.sandboxBootstrapForCleanup(ctx, agent)
	if err != nil || receipt == nil {
		return err
	}
	if err := r.customReader().Get(ctx, client.ObjectKey{Name: receipt.Namespace}, &corev1.Namespace{}); err == nil {
		return fmt.Errorf("Sandbox namespace must be absent before releasing bootstrap state")
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	if controllerutil.ContainsFinalizer(agent, sandbox.BootstrapFinalizer) {
		base := agent.DeepCopy()
		controllerutil.RemoveFinalizer(agent, sandbox.BootstrapFinalizer)
		if err := r.Patch(ctx, agent, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
			return err
		}
	}
	if controllerutil.ContainsFinalizer(object, sandbox.BootstrapFinalizer) {
		base := object.DeepCopy()
		controllerutil.RemoveFinalizer(object, sandbox.BootstrapFinalizer)
		if err := r.Patch(ctx, object, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
			return client.IgnoreNotFound(err)
		}
	}
	return client.IgnoreNotFound(r.Delete(ctx, object, client.Preconditions{UID: &object.UID, ResourceVersion: &object.ResourceVersion}))
}
