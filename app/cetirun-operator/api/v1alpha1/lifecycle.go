// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package v1alpha1

import "regexp"

const (
	AnnotationRestartRequestID     = "cetirun.microsoft.com/restart-request-id"
	SandboxAuthorizationAnnotation = "cetirun.microsoft.com/sandbox-authorization"
	SandboxReauthorizeAnnotation   = "cetirun.microsoft.com/sandbox-reauthorize"
	SandboxHoldFinalizer           = "cetirun.microsoft.com/sandbox-hold"
	SandboxBootstrapFinalizer      = "cetirun.microsoft.com/sandbox-bootstrap"
	// SandboxPreparationFinalizer identifies retained single-start lifetimes.
	SandboxPreparationFinalizer = "cetirun.microsoft.com/sandbox-preparation"
)

var restartRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

func ValidRestartRequestID(id string) bool {
	return restartRequestIDPattern.MatchString(id)
}
