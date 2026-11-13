// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxcoordination

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SubmissionStage identifies the last local step reached by one configuration
// update attempt. It deliberately contains no server or configuration content.
type SubmissionStage string

const (
	SubmissionStageValidation         SubmissionStage = "validation"
	SubmissionStageGuard              SubmissionStage = "guard"
	SubmissionStageSourceRead         SubmissionStage = "source-read"
	SubmissionStageRequestPreparation SubmissionStage = "request-preparation"
	SubmissionStageSourceUpdate       SubmissionStage = "source-update"
	SubmissionStageStatusUpdate       SubmissionStage = "status-update"
)

// SubmissionState states whether this invocation is known not to have called
// the source update, was definitively rejected, or has an unknown outcome.
type SubmissionState string

const (
	SubmissionNotSubmitted SubmissionState = "not-submitted"
	SubmissionRejected     SubmissionState = "rejected"
	SubmissionUnknown      SubmissionState = "unknown"
)

// SubmissionDiagnostic is safe to show in command status. Reason is from a
// fixed local vocabulary and HTTPStatus is set only for a known rejection.
type SubmissionDiagnostic struct {
	Stage      SubmissionStage `json:"stage"`
	State      SubmissionState `json:"state"`
	Reason     string          `json:"reason"`
	HTTPStatus int             `json:"httpStatus,omitempty"`
}

// SubmissionError retains the original error for errors.Is and errors.As, but
// its Error method intentionally never exposes an API response or input value.
type SubmissionError struct {
	Diagnostic SubmissionDiagnostic
	err        error
}

func (e *SubmissionError) Error() string {
	return fmt.Sprintf("configuration submission %s (%s)", e.Diagnostic.Stage, e.Diagnostic.State)
}

func (e *SubmissionError) Unwrap() error { return e.err }

// SubmissionDiagnosticFor extracts a bounded diagnostic from an attempted
// configuration update. It returns false for errors from unrelated operations.
func SubmissionDiagnosticFor(err error) (SubmissionDiagnostic, bool) {
	var submission *SubmissionError
	if !errors.As(err, &submission) {
		return SubmissionDiagnostic{}, false
	}
	return submission.Diagnostic, true
}

func submissionFailure(stage SubmissionStage, state SubmissionState, reason string, status int, err error) error {
	return &SubmissionError{Diagnostic: SubmissionDiagnostic{
		Stage: stage, State: state, Reason: reason, HTTPStatus: status,
	}, err: err}
}

func classifySourceUpdateFailure(err error) error {
	return classifyMutationFailure(SubmissionStageSourceUpdate, err)
}

// classifyMutationFailure classifies the response from exactly one attempted
// Kubernetes mutation. It never makes a release decision; callers still must
// observe a terminal source state before releasing a reservation.
func classifyMutationFailure(stage SubmissionStage, err error) error {
	// An API status can prove that this conditional request was rejected only
	// for these client-side response classes. Timeouts, throttling and every
	// other response remain unknown because a request may still commit.
	reason := apierrors.ReasonForError(err)
	if label, expectedStatus, ok := rejectedReason(reason); ok {
		if status := responseStatus(err); status == expectedStatus {
			return submissionFailure(stage, SubmissionRejected, label, status,
				errors.Join(ErrUnavailable, err))
		}
	}
	return submissionFailure(stage, SubmissionUnknown, unknownReason(err), 0,
		errors.Join(ErrUnavailable, err))
}

func rejectedReason(reason metav1.StatusReason) (string, int, bool) {
	switch reason {
	case metav1.StatusReasonBadRequest:
		return "bad-request", 400, true
	case metav1.StatusReasonUnauthorized:
		return "unauthorized", 401, true
	case metav1.StatusReasonForbidden:
		return "forbidden", 403, true
	case metav1.StatusReasonNotFound:
		return "not-found", 404, true
	case metav1.StatusReasonConflict:
		return "conflict", 409, true
	case metav1.StatusReasonInvalid:
		return "invalid", 422, true
	case metav1.StatusReasonRequestEntityTooLarge:
		return "request-too-large", 413, true
	case metav1.StatusReasonUnsupportedMediaType:
		return "unsupported-media-type", 415, true
	case metav1.StatusReasonMethodNotAllowed:
		return "method-not-allowed", 405, true
	default:
		return "", 0, false
	}
}

func responseStatus(err error) int {
	var statusError apierrors.APIStatus
	if !errors.As(err, &statusError) {
		return 0
	}
	status := int(statusError.Status().Code)
	if status < 400 || status > 499 || status == 408 || status == 429 {
		return 0
	}
	return status
}

func unknownReason(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, io.EOF):
		return "transport"
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		if networkError.Timeout() {
			return "timeout"
		}
		return "transport"
	}
	return "unknown"
}
