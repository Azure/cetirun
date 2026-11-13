// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxplatform"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// TestSandboxStatusFailureAddsFixedReason checks #556 item 2: the Agent's own
// SandboxReady text adds the fixed reason of a classified failure, as the
// AgentPolicy and Sandbox conditions do. Other error text, which can contain
// policy, registration or API values, never reaches the status.
func TestSandboxStatusFailureAddsFixedReason(t *testing.T) {
	const canary = "value-canary-5d1f"
	missing := apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, canary)
	for _, tc := range []struct {
		name    string
		err     error
		status  metav1.ConditionStatus
		reason  string
		message string
	}{
		{"unavailable dependency", fmt.Errorf("compile Sandbox policy: %w",
			sandboxpolicy.ControlFailure(sandboxpolicy.ErrUnavailable, "a fixed reason")),
			metav1.ConditionUnknown, "ObservationUnavailable", "Current trusted platform dependencies are unavailable: a fixed reason."},
		{"unavailable dependency without a fixed reason", fmt.Errorf("%w: %s", sandboxpolicy.ErrUnavailable, canary),
			metav1.ConditionUnknown, "ObservationUnavailable", "Current trusted platform dependencies are unavailable."},
		{"unsupported control", sandboxpolicy.ControlFailure(sandboxpolicy.ErrUnsupportedControl, "a fixed reason"),
			metav1.ConditionFalse, "UnsupportedControl", "A requested Sandbox control is not qualified: a fixed reason."},
		{"unapproved grant", sandboxpolicy.ControlFailure(sandboxpolicy.ErrUnapprovedGrant, "a fixed reason"),
			metav1.ConditionFalse, "UnauthorizedBinding", "The current binding or identity grant is not authorized: a fixed reason."},
		{"exceeded budget", fmt.Errorf("compile Sandbox policy: %w",
			sandboxpolicy.ControlFailure(sandboxpolicy.ErrConflict, "control input budget exceeded")),
			metav1.ConditionFalse, "PolicyConflict", "Policy rules conflict with required platform controls: control input budget exceeded."},
		{"conflict without a fixed reason", fmt.Errorf("%w: CiliumNetworkPolicy %q is too large", sandboxpolicy.ErrConflict, canary),
			metav1.ConditionFalse, "PolicyConflict", "Policy rules conflict with required platform controls."},
		{"unreadable platform dependency", sandboxpolicy.WithPublicDetail(fmt.Errorf("read: %w", missing), "a platform dependency could not be read"),
			metav1.ConditionFalse, "ReferenceMissing", "A required Sandbox configuration reference is missing: a platform dependency could not be read."},
		{"missing reference", missing,
			metav1.ConditionFalse, "ReferenceMissing", "A required Sandbox configuration reference is missing."},
		{"changed platform", sandboxpolicy.ControlFailure(sandboxplatform.ErrSourceChanged, "platform configuration changed during capture"),
			metav1.ConditionFalse, "PolicyApplyFailed", "Sandbox control processing failed; execution must remain held: platform configuration changed during capture."},
		{"other failure", errors.New("synthetic provider response " + canary),
			metav1.ConditionFalse, "PolicyApplyFailed", "Sandbox control processing failed; execution must remain held."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, reason, message := sandboxStatusFailure(tc.err)
			if status != tc.status || reason != tc.reason || message != tc.message {
				t.Fatalf("sandboxStatusFailure = %s, %s, %q; want %s, %s, %q", status, reason, message, tc.status, tc.reason, tc.message)
			}
			if strings.Contains(message, canary) {
				t.Fatalf("the status quoted the error: %q", message)
			}
		})
	}
}
