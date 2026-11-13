// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/ingressmtls"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
)

// prerequisiteRequeue is the retry interval while an Agent waits for a
// platform prerequisite or an administrator action.
const prerequisiteRequeue = 30 * time.Second

// prerequisiteWait reports whether err only waits for an administrator or
// another controller. Every cause must be such a wait; a bare class sentinel
// such as sandboxpolicy.ErrUnavailable that labels a wait is allowed beside
// it. Any other cause, for example an API error, makes err a failure.
func prerequisiteWait(err error) bool {
	if err == nil || errors.Is(err, errSkillsPublicationRecovery) {
		return false
	}
	waits := 0
	var walk func(error) bool
	walk = func(current error) bool {
		if joined, ok := current.(interface{ Unwrap() []error }); ok {
			causes := joined.Unwrap()
			for _, nested := range causes {
				if !walk(nested) {
					return false
				}
			}
			return len(causes) != 0
		}
		if singlePrerequisiteWait(current) {
			waits++
			return true
		}
		return current == sandboxpolicy.ErrUnavailable
	}
	return walk(err) && waits != 0
}

// singlePrerequisiteWait lists only causes that wait for an administrator or
// another controller. Outages (sandboxpolicy.ErrUnavailable in general), stale
// evidence, refused approvals and unsupported controls stay errors: existing
// holds rely on them, and the start path uses an error to hold a single-start
// Agent.
func singlePrerequisiteWait(err error) bool {
	for _, sentinel := range []error{errSkillsPublicationNotApproved, errSkillsClaimPending, ingressmtls.ErrApprovalPending} {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}

// prerequisiteResult turns a classified wait into a periodic retry once its
// status is written. The wait is not a reconcile failure, so it does not use
// the error rate limiter and it is logged only when the status changed. Every
// other error is returned as it is, with an empty result: controller-runtime
// ignores RequeueAfter when an error is returned.
func prerequisiteResult(ctx context.Context, cause error, statusChanged bool, statusErr error) (ctrl.Result, error) {
	if statusErr != nil || !prerequisiteWait(cause) {
		return ctrl.Result{}, errors.Join(cause, statusErr)
	}
	if statusChanged {
		log.FromContext(ctx).Info("Agent waits for a platform prerequisite; retrying periodically",
			"reason", cause.Error(), "requeueAfter", prerequisiteRequeue)
	}
	return ctrl.Result{RequeueAfter: prerequisiteRequeue}, nil
}

// holdReconciledBootstrap holds a common-executor Agent whose owned bootstrap
// did not complete, for example because its Team has no approved Skills
// publication yet. A classified wait then retries periodically; a failed hold
// or any other cause stays a reconcile error. The hold writes the Agent only
// when its status changes, so a repeated wait is not logged again.
func (r *AgentReconciler) holdReconciledBootstrap(ctx context.Context, agent *api.Agent, cause error) (ctrl.Result, error) {
	version := agent.ResourceVersion
	_, holdErr := r.holdPolicyDependencyUnavailable(ctx, agent)
	return prerequisiteResult(ctx, cause, agent.ResourceVersion != version, holdErr)
}

// cleanupWaitRequeue is the retry interval while a deletion waits for the Pods
// of an Agent to terminate. Pods usually terminate within seconds, so it is
// shorter than prerequisiteRequeue.
const cleanupWaitRequeue = 5 * time.Second

// The Ready condition of a deleting Agent shows the Pod wait with this reason
// and fixed message.
const (
	cleanupWaitReason  = "PodsTerminating"
	cleanupWaitMessage = "Deletion waits for the Pods of the Agent to terminate."
)

// cleanupWaitResult turns the Pod wait of a deletion into a periodic retry
// without an error, so that it does not use the error rate limiter or log a
// reconcile error. The Ready condition of the Agent shows the wait. The pass
// that changes this condition writes it and logs the wait at Info level; a
// repeated pass writes nothing and logs at V(1). A failed status write, for
// example a conflict with a newer Agent, is returned as an error, and the next
// pass tries again. Every other error is returned as it is.
func (r *AgentReconciler) cleanupWaitResult(ctx context.Context, agent *api.Agent, err error) (ctrl.Result, error) {
	if !errors.Is(err, errSandboxCleanupWaitsForPods) {
		return ctrl.Result{}, err
	}
	changed, statusErr := r.recordCleanupWait(ctx, agent)
	if statusErr != nil {
		return ctrl.Result{}, fmt.Errorf("record the Pod wait of the deletion: %w", statusErr)
	}
	logger := log.FromContext(ctx)
	if changed {
		logger.Info("Agent deletion waits for its Pods to terminate; retrying periodically", "requeueAfter", cleanupWaitRequeue)
	} else {
		logger.V(1).Info("Agent deletion waits for its Pods to terminate", "requeueAfter", cleanupWaitRequeue)
	}
	return ctrl.Result{RequeueAfter: cleanupWaitRequeue}, nil
}

// cleanupWaitCondition is the Ready condition of an Agent whose deletion waits
// for its Pods.
func cleanupWaitCondition(agent *api.Agent) metav1.Condition {
	return metav1.Condition{
		Type: api.ConditionReady, Status: metav1.ConditionFalse, ObservedGeneration: agent.Generation,
		Reason: cleanupWaitReason, Message: cleanupWaitMessage,
	}
}

// cleanupWaitRecorded reports whether the Ready condition of agent already
// shows the Pod wait of its deletion.
func cleanupWaitRecorded(agent *api.Agent) bool {
	ready := apimeta.FindStatusCondition(agent.Status.Conditions, api.ConditionReady)
	wait := cleanupWaitCondition(agent)
	return ready != nil && ready.Status == wait.Status && ready.Reason == wait.Reason &&
		ready.Message == wait.Message && ready.ObservedGeneration == wait.ObservedGeneration
}

// recordCleanupWait writes the Pod wait to the Ready condition of a deleting
// Agent and reports whether it wrote. It reads the Agent only when the
// condition of this pass differs, and writes only when the current Agent
// differs too. It writes that condition only: a deletion pass can change other
// status only in memory, for example a completed Agent Identity cleanup, and
// those changes are not written.
func (r *AgentReconciler) recordCleanupWait(ctx context.Context, agent *api.Agent) (bool, error) {
	if cleanupWaitRecorded(agent) {
		return false, nil
	}
	current := &api.Agent{}
	if err := r.customReader().Get(ctx, client.ObjectKeyFromObject(agent), current); err != nil {
		return false, fmt.Errorf("read Agent: %w", err)
	}
	if current.UID != agent.UID {
		return false, fmt.Errorf("the Agent was replaced during its deletion")
	}
	readVersion := current.ResourceVersion
	if cleanupWaitRecorded(current) {
		return false, nil
	}
	apimeta.SetStatusCondition(&current.Status.Conditions, cleanupWaitCondition(current))
	if err := r.updateStatus(ctx, current); err != nil {
		return false, err
	}
	// If the Agent of this pass was current, keep it current, so that a later
	// write with an optimistic lock in this pass is not refused. An older copy
	// stays older, and such a write is still refused.
	if written := apimeta.FindStatusCondition(current.Status.Conditions, api.ConditionReady); written != nil && agent.ResourceVersion == readVersion {
		apimeta.SetStatusCondition(&agent.Status.Conditions, *written)
		agent.ResourceVersion = current.ResourceVersion
	}
	return true, nil
}
