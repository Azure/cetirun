// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package kube

import (
	"context"
	"errors"
	"fmt"
	"slices"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	ErrInstanceVersionRequired = errors.New("uid and resourceVersion from the current Agent are required")
	ErrSuspensionUnsupported   = errors.New("retained single-start Agents do not support owner suspend/resume")
)

func canChangeSuspension(instance *api.Agent) bool {
	return instance.UID != "" && instance.ResourceVersion != "" && instance.DeletionTimestamp.IsZero() &&
		!slices.Contains(instance.Finalizers, api.SandboxPreparationFinalizer)
}

func CanSuspendInstance(instance *api.Agent) bool {
	return canChangeSuspension(instance) && !instance.Spec.Suspended
}

func CanResumeInstance(instance *api.Agent) bool {
	return canChangeSuspension(instance) && instance.Spec.Suspended
}

func (c *Client) SetInstanceSuspendedByName(ctx context.Context, name string, version InstanceVersion,
	suspended bool, authorize func(*api.Agent) error,
) (*api.Agent, error) {
	if version.UID == "" || version.ResourceVersion == "" {
		return nil, ErrInstanceVersionRequired
	}
	instance, err := c.GetInstanceByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if authorize != nil {
		if err := authorize(instance); err != nil {
			return nil, err
		}
	}
	if err := validateInstanceUpdate(instance, UpdateInstanceOpts{ExpectedUID: version.UID}); err != nil {
		return nil, err
	}
	if slices.Contains(instance.Finalizers, api.SandboxPreparationFinalizer) {
		return nil, ErrSuspensionUnsupported
	}
	// A retry may observe the requested intent after a lost response or status
	// write. A stale version must never reverse a newer, opposite intent.
	if instance.Spec.Suspended == suspended {
		return instance, nil
	}
	if instance.ResourceVersion != version.ResourceVersion {
		return nil, ErrInstanceChanged
	}
	before := instance.DeepCopy()
	// Keep pending restart/migration requests: suspension takes precedence, and
	// resumption lets the existing controller continue those same operations.
	instance.Spec.Suspended = suspended
	if err := c.client.Patch(ctx, instance, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}),
		client.FieldValidation(metav1.FieldValidationStrict)); err != nil {
		return nil, fmt.Errorf("request Agent suspension change: %w", err)
	}
	return instance, nil
}
