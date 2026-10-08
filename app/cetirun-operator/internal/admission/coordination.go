// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxadmission

import (
	"context"
	"errors"
	"net/http"

	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxcoordination"
	admissionv1 "k8s.io/api/admission/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func (h *Handler) validateCoordinated(ctx context.Context, request admission.Request) admission.Response {
	response := h.validate(ctx, request)
	if !response.Allowed || h.coordination == nil || request.Operation != admissionv1.Update || request.SubResource != "" {
		return response
	}
	switch request.Resource.Resource {
	case "teams", "sandboxes", "agentpolicies":
	default:
		return response
	}
	object, previous, err := decodeRequest(request, ValidationStrict)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if garbageCollectorFinalizerUpdate(request, object, previous) {
		return response
	}
	if err := h.coordination.AuthorizeConfigurationUpdate(ctx, previous, object); err != nil {
		if errors.Is(err, sandboxcoordination.ErrConflict) {
			return admission.Denied("CoordinationRequired: configuration updates require the exact submitted change reservation; use sandbox-config-change")
		}
		return dependencyError(ctx, err)
	}
	return response
}
