// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package kube

import (
	"context"
	"errors"
	"fmt"
	"slices"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/agentconfig"
	"github.com/Azure/CetiRun/app/internal/chataccess"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	ErrInstanceVersionRequired  = errors.New("uid and resourceVersion from the current Agent are required")
	ErrSuspensionUnsupported    = errors.New("retained single-start Agents do not support owner suspend/resume")
	ErrSuspensionHomeRequired   = errors.New("retained suspension requires persistent HOME storage; ephemeral HOME would be lost")
	ErrSuspensionHomeUnverified = errors.New("retained suspension is unavailable until the current HOME storage can be verified")
)

// CheckSuspensionHome reads the effective workload, including chart and
// deployment defaults that are not present in the Agent's Helm overrides.
func (c *Client) CheckSuspensionHome(ctx context.Context, instance *api.Agent) error {
	workload := &appsv1.StatefulSet{}
	key := client.ObjectKey{Namespace: InstanceNamespace(instance.Name), Name: chataccess.ServiceName(instance.Name)}
	if err := c.client.Get(ctx, key, workload); err != nil {
		return fmt.Errorf("%w: %w", ErrSuspensionHomeUnverified, err)
	}
	if workload.UID == "" || !workload.DeletionTimestamp.IsZero() ||
		(workload.Annotations[chataccess.AgentUIDAnnotation] != "" &&
			workload.Annotations[chataccess.AgentUIDAnnotation] != string(instance.UID)) {
		return ErrSuspensionHomeUnverified
	}
	if owner := metav1.GetControllerOf(workload); owner != nil {
		if len(workload.OwnerReferences) != 1 || owner.APIVersion != api.GroupVersion.String() || owner.Kind != "Agent" ||
			owner.Name != instance.Name || owner.UID != instance.UID {
			return ErrSuspensionHomeUnverified
		}
	} else if instance.Status.Namespace != key.Namespace || instance.Status.HelmReleaseName != "openclaw-"+instance.Name ||
		!chatReleaseOwned(workload, instance) {
		return ErrSuspensionHomeUnverified
	}
	containers := workload.Spec.Template.Spec.Containers
	if len(containers) == 0 {
		return ErrSuspensionHomeUnverified
	}
	home := "/home/" + *agentconfig.Defaults(instance.Spec.Agent).Username
	volumeName := ""
	for _, mount := range containers[0].VolumeMounts {
		if mount.MountPath == home {
			volumeName = mount.Name
		}
	}
	if volumeName == "" {
		return ErrSuspensionHomeUnverified
	}
	for _, claim := range workload.Spec.VolumeClaimTemplates {
		if claim.Name == volumeName {
			return nil
		}
	}
	for _, volume := range workload.Spec.Template.Spec.Volumes {
		if volume.Name == volumeName && volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName != "" {
			return nil
		}
	}
	return ErrSuspensionHomeRequired
}

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
	if suspended {
		if err := c.CheckSuspensionHome(ctx, instance); err != nil {
			return nil, err
		}
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
