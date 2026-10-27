// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package kube

import (
	"context"
	"errors"
	"fmt"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	ErrInvalidTeamChangeRequest = errors.New("a valid Idempotency-Key, uid, resourceVersion and target Team are required")
	ErrTeamChangePending        = errors.New("a Team change is already in progress")
)

// RequestTeamChangeByName records an operation request only. The controller
// validates the target and changes the effective binding after it has stopped
// the old workload; Portal never writes Team or Sandbox directly.
func (c *Client) RequestTeamChangeByName(ctx context.Context, name string, request api.TeamChangeRequest,
	authorize func(*api.Agent) error,
) (*api.Agent, error) {
	if request.ExpectedUID == "" || request.ExpectedResourceVersion == "" ||
		!api.ValidRestartRequestID(request.RequestID) || request.TargetTeam == "" {
		return nil, ErrInvalidTeamChangeRequest
	}
	var result *api.Agent
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		instance, err := c.GetInstanceByName(ctx, name)
		if err != nil {
			return err
		}
		if err := validateInstanceUpdate(instance, UpdateInstanceOpts{ExpectedUID: request.ExpectedUID, Authorize: authorize}); err != nil {
			return err
		}
		// A request ID identifies exactly one immutable operation. Once the
		// controller clears spec after completion, its status keeps the original
		// caller payload so a lost-response retry still returns that operation.
		if current, found := instance.TeamChangeRequestForID(request.RequestID); found {
			if api.TeamChangeRequestsEqual(current, &request) {
				result = instance
				return nil
			}
			return ErrTeamChangePending
		}
		if instance.Spec.TeamChangeRequest != nil {
			if instance.TeamChangePending() {
				return ErrTeamChangePending
			}
		}
		if instance.ResourceVersion != request.ExpectedResourceVersion {
			return ErrInstanceChanged
		}
		if !instance.DeletionTimestamp.IsZero() {
			return ErrInstanceDeleting
		}
		before := instance.DeepCopy()
		instance.Spec.TeamChangeRequest = &request
		if err := c.client.Patch(ctx, instance, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}),
			client.FieldValidation(metav1.FieldValidationStrict)); err != nil {
			return fmt.Errorf("request Team change: %w", err)
		}
		result = instance
		return nil
	})
	return result, err
}
