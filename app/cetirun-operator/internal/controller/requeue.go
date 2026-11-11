// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

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
