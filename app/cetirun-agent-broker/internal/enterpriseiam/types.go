// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package enterpriseiam defines the wire contract for an enterprise IAM
// permission-request service. It deliberately has no provider-specific
// concepts: provider-side authorization is represented by an immutable
// request binding and independently verifiable evidence.
package enterpriseiam

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	maxIdentifierLength = 256
	maxValueLength      = 512
	maxItems            = 128
)

// State is the authorization state reported by the enterprise IAM service.
// Approved means the request was accepted by an approver. Only Applied means
// that the requested provider-side permission has been applied.
type State string

const (
	StatePending  State = "pending"
	StateApproved State = "approved"
	StateApplied  State = "applied"
	StateDenied   State = "denied"
	StateRevoked  State = "revoked"
)

// PermissionRequest is immutable once submitted. ID is both the local
// request identity and the idempotency key. The service must reject reuse of
// an ID with a different binding.
type PermissionRequest struct {
	ID          string    `json:"id"`
	AgentUID    string    `json:"agentUid"`
	Provider    string    `json:"provider"`
	Mode        string    `json:"mode"`
	TenantID    string    `json:"tenantId"`
	PrincipalID string    `json:"principalId"`
	ClientID    string    `json:"clientId"`
	Scopes      []string  `json:"scopes"`
	Actions     []string  `json:"actions"`
	ExpiresAt   time.Time `json:"expiresAt"`
	Requester   string    `json:"requester"`
	Revision    string    `json:"revision"`
}

// Evidence binds an IAM decision to every security-relevant request field.
// EvaluatedAt lets clients reject stale provider authorization evidence.
type Evidence struct {
	ID          string    `json:"id"`
	AgentUID    string    `json:"agentUid"`
	Provider    string    `json:"provider"`
	Mode        string    `json:"mode"`
	TenantID    string    `json:"tenantId"`
	PrincipalID string    `json:"principalId"`
	ClientID    string    `json:"clientId"`
	Scopes      []string  `json:"scopes"`
	Actions     []string  `json:"actions"`
	ExpiresAt   time.Time `json:"expiresAt"`
	Requester   string    `json:"requester"`
	Revision    string    `json:"revision"`
	EvaluatedAt time.Time `json:"evaluatedAt"`
}

// Decision is returned by all operations. ExternalRequestID is the service's
// own correlation identifier and must not replace PermissionRequest.ID.
type Decision struct {
	ExternalRequestID string   `json:"externalRequestId"`
	State             State    `json:"state"`
	Evidence          Evidence `json:"evidence"`
}

// Applied reports whether the decision may be used to authorize an operation
// at now. An approved request alone is intentionally insufficient.
func (d Decision) Applied(now time.Time) bool {
	return d.State == StateApplied && d.Evidence.ExpiresAt.After(now)
}

type errorCode string

const (
	errorInvalidRequest errorCode = "invalid_request"
	errorExpired        errorCode = "expired"
	errorInvalidConfig  errorCode = "invalid_config"
	errorUnavailable    errorCode = "unavailable"
	errorRejected       errorCode = "rejected"
	errorMalformed      errorCode = "malformed_response"
	errorTooLarge       errorCode = "response_too_large"
	errorBinding        errorCode = "binding_mismatch"
	errorStale          errorCode = "stale_evidence"
)

// Error is deliberately free of response content, which may contain private
// resource or approval details.
type Error struct {
	Code       errorCode
	StatusCode int
}

func (e *Error) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("enterprise IAM request failed: %s (%d)", e.Code, e.StatusCode)
	}
	return "enterprise IAM request failed: " + string(e.Code)
}

func requestError(code errorCode) error { return &Error{Code: code} }

func canonicalRequest(request PermissionRequest, now time.Time) (PermissionRequest, error) {
	if !validRequestID(request.ID) ||
		!validIdentifier(request.AgentUID) ||
		!validIdentifier(request.Provider) ||
		!validIdentifier(request.Mode) ||
		!validIdentifier(request.TenantID) ||
		!validIdentifier(request.PrincipalID) ||
		!validIdentifier(request.ClientID) ||
		!validIdentifier(request.Requester) ||
		!validIdentifier(request.Revision) {
		return PermissionRequest{}, requestError(errorInvalidRequest)
	}
	var err error
	if request.Scopes, err = canonicalValues(request.Scopes); err != nil {
		return PermissionRequest{}, err
	}
	if request.Actions, err = canonicalValues(request.Actions); err != nil {
		return PermissionRequest{}, err
	}
	request.ExpiresAt = request.ExpiresAt.UTC().Round(0)
	if request.ExpiresAt.IsZero() || !request.ExpiresAt.After(now.UTC()) {
		return PermissionRequest{}, requestError(errorExpired)
	}
	return request, nil
}

func canonicalEvidence(evidence Evidence) (Evidence, error) {
	if !validRequestID(evidence.ID) ||
		!validIdentifier(evidence.AgentUID) ||
		!validIdentifier(evidence.Provider) ||
		!validIdentifier(evidence.Mode) ||
		!validIdentifier(evidence.TenantID) ||
		!validIdentifier(evidence.PrincipalID) ||
		!validIdentifier(evidence.ClientID) ||
		!validIdentifier(evidence.Requester) ||
		!validIdentifier(evidence.Revision) {
		return Evidence{}, requestError(errorMalformed)
	}
	var err error
	if evidence.Scopes, err = canonicalValues(evidence.Scopes); err != nil {
		return Evidence{}, requestError(errorMalformed)
	}
	if evidence.Actions, err = canonicalValues(evidence.Actions); err != nil {
		return Evidence{}, requestError(errorMalformed)
	}
	evidence.ExpiresAt = evidence.ExpiresAt.UTC().Round(0)
	evidence.EvaluatedAt = evidence.EvaluatedAt.UTC().Round(0)
	if evidence.ExpiresAt.IsZero() || evidence.EvaluatedAt.IsZero() {
		return Evidence{}, requestError(errorMalformed)
	}
	return evidence, nil
}

func canonicalValues(values []string) ([]string, error) {
	if len(values) == 0 || len(values) > maxItems {
		return nil, requestError(errorInvalidRequest)
	}
	copyValues := append([]string(nil), values...)
	for _, value := range copyValues {
		if !validValue(value) {
			return nil, requestError(errorInvalidRequest)
		}
	}
	sort.Strings(copyValues)
	for i := 1; i < len(copyValues); i++ {
		if copyValues[i-1] == copyValues[i] {
			return nil, requestError(errorInvalidRequest)
		}
	}
	return copyValues, nil
}

func validRequestID(value string) bool {
	if len(value) == 0 || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '.' || character == '_' || character == '-') {
			return false
		}
	}
	return true
}

func validIdentifier(value string) bool {
	if len(value) == 0 || len(value) > maxIdentifierLength || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validValue(value string) bool {
	if len(value) == 0 || len(value) > maxValueLength || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func sameBinding(request PermissionRequest, evidence Evidence) bool {
	return request.ID == evidence.ID &&
		request.AgentUID == evidence.AgentUID &&
		request.Provider == evidence.Provider &&
		request.Mode == evidence.Mode &&
		request.TenantID == evidence.TenantID &&
		request.PrincipalID == evidence.PrincipalID &&
		request.ClientID == evidence.ClientID &&
		request.ExpiresAt.Equal(evidence.ExpiresAt) &&
		request.Requester == evidence.Requester &&
		request.Revision == evidence.Revision &&
		stringSlicesEqual(request.Scopes, evidence.Scopes) &&
		stringSlicesEqual(request.Actions, evidence.Actions)
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func errorCodeOf(err error) (errorCode, bool) {
	var enterpriseError *Error
	if !errors.As(err, &enterpriseError) {
		return "", false
	}
	return enterpriseError.Code, true
}
