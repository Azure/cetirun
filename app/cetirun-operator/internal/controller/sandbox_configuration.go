// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	nodev1 "k8s.io/api/node/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
)

type SandboxRuntimePrerequisite struct {
	UID     types.UID
	Handler string
}

type SandboxStoragePrerequisite struct {
	UID         types.UID
	Provisioner string
}

// SandboxConfigurationSnapshot is trusted platform state, not CR status. Empty
// or stale snapshots never imply readiness. No production source is installed.
type SandboxConfigurationSnapshot struct {
	ObservedAt     time.Time
	Registrations  map[api.SandboxType]sandbox.Registration
	Platforms      map[api.SandboxType]sandboxpolicy.Platform
	RuntimeClasses map[string]SandboxRuntimePrerequisite
	StorageClasses map[string]SandboxStoragePrerequisite
}

type SandboxConfigurationSource interface {
	// Snapshot returns an immutable, atomically captured support inventory.
	Snapshot(context.Context) (SandboxConfigurationSnapshot, error)
}

type SandboxConfigurationSourceFunc func(context.Context) (SandboxConfigurationSnapshot, error)

func (f SandboxConfigurationSourceFunc) Snapshot(ctx context.Context) (SandboxConfigurationSnapshot, error) {
	return f(ctx)
}

type SandboxReconciler struct {
	client.Client
	APIReader      client.Reader
	Source         SandboxConfigurationSource
	Now            func() time.Time
	MaxSnapshotAge time.Duration
}

type AgentPolicyReconciler struct {
	client.Client
	APIReader      client.Reader
	Source         SandboxConfigurationSource
	Now            func() time.Time
	MaxSnapshotAge time.Duration
}

func (r *SandboxReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Client == nil {
		r.Client = mgr.GetClient()
	}
	if r.APIReader == nil {
		r.APIReader = mgr.GetAPIReader()
	}
	return ctrl.NewControllerManagedBy(mgr).For(&api.Sandbox{}).Complete(r)
}

func (r *AgentPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Client == nil {
		r.Client = mgr.GetClient()
	}
	if r.APIReader == nil {
		r.APIReader = mgr.GetAPIReader()
	}
	return ctrl.NewControllerManagedBy(mgr).For(&api.AgentPolicy{}).Complete(r)
}

func (r *SandboxReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	object := &api.Sandbox{}
	reader := configurationReader(r.APIReader, r.Client)
	if err := reader.Get(ctx, request.NamespacedName, object); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	before := object.DeepCopy()
	now := configurationNow(r.Now)
	condition, observeErr := r.assess(ctx, reader, object, now)
	condition.Type, condition.ObservedGeneration = api.ConditionReady, object.Generation
	condition.LastTransitionTime = metav1.NewTime(now)
	apimeta.SetStatusCondition(&object.Status.Conditions, condition)
	object.Status.ObservedGeneration = object.Generation
	if !reflect.DeepEqual(before.Status, object.Status) {
		if err := r.Status().Patch(ctx, object, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			return ctrl.Result{}, fmt.Errorf("update Sandbox prerequisite status: %w", err)
		}
	}
	return ctrl.Result{RequeueAfter: configurationRefresh(r.MaxSnapshotAge)}, observeErr
}

func (r *AgentPolicyReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	object := &api.AgentPolicy{}
	reader := configurationReader(r.APIReader, r.Client)
	if err := reader.Get(ctx, request.NamespacedName, object); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	before := object.DeepCopy()
	now := configurationNow(r.Now)
	condition, observeErr := r.assess(ctx, object, now)
	condition.Type, condition.ObservedGeneration = api.ConditionAgentPolicyAccepted, object.Generation
	condition.LastTransitionTime = metav1.NewTime(now)
	apimeta.SetStatusCondition(&object.Status.Conditions, condition)
	object.Status.ObservedGeneration = object.Generation
	if !reflect.DeepEqual(before.Status, object.Status) {
		if err := r.Status().Patch(ctx, object, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			return ctrl.Result{}, fmt.Errorf("update AgentPolicy configuration status: %w", err)
		}
	}
	return ctrl.Result{RequeueAfter: configurationRefresh(r.MaxSnapshotAge)}, observeErr
}

func (r *SandboxReconciler) assess(ctx context.Context, reader client.Reader, object *api.Sandbox, now time.Time) (metav1.Condition, error) {
	if !object.DeletionTimestamp.IsZero() {
		return configurationCondition(metav1.ConditionFalse, "Terminating", "Sandbox configuration is terminating."), nil
	}
	spec, errs := sandbox.NormalizeSandbox(object.Spec)
	if len(errs) != 0 {
		return configurationCondition(metav1.ConditionFalse, "InvalidConfiguration", "Sandbox configuration is invalid."), nil
	}
	snapshot, err := configurationSnapshot(ctx, r.Source, now, r.MaxSnapshotAge)
	if err != nil {
		return configurationCondition(metav1.ConditionUnknown, "PrerequisitesUnavailable", "Current platform prerequisite state is unavailable."), err
	}
	registration, exists := snapshot.Registrations[spec.Type]
	if !exists || !runtimePrerequisitesSupported(spec, registration.Capabilities) {
		return configurationCondition(metav1.ConditionFalse, "UnsupportedControl", "Sandbox runtime and mandatory controls are not qualified."), nil
	}
	if !registration.Available {
		return configurationCondition(metav1.ConditionFalse, "RuntimeUnavailable", "The registered Sandbox runtime is unavailable."), nil
	}
	platform, exists := snapshot.Platforms[spec.Type]
	if !exists {
		return configurationCondition(metav1.ConditionUnknown, "PrerequisitesUnavailable", "Trusted network topology and grants are unavailable."), nil
	}
	if err := sandboxpolicy.ValidatePlatform(platform, registration.Capabilities); err != nil {
		return configurationErrorCondition(err), nil
	}
	runtimeName := registration.Capabilities.RuntimeClassName
	expectedRuntime, exists := snapshot.RuntimeClasses[runtimeName]
	if !exists || expectedRuntime.UID == "" || expectedRuntime.Handler == "" {
		return configurationCondition(metav1.ConditionUnknown, "PrerequisitesUnavailable", "RuntimeClass identity approval is unavailable."), nil
	}
	runtimeClass := &nodev1.RuntimeClass{}
	if err := reader.Get(ctx, types.NamespacedName{Name: runtimeName}, runtimeClass); err != nil {
		return prerequisiteReadError(err)
	}
	if runtimeClass.UID != expectedRuntime.UID || runtimeClass.Handler != expectedRuntime.Handler ||
		!runtimeClass.DeletionTimestamp.IsZero() {
		return configurationCondition(metav1.ConditionFalse, "ReferenceChanged", "RuntimeClass no longer matches the approved prerequisite."), nil
	}
	expectedStorage, exists := snapshot.StorageClasses[spec.Storage.Home.StorageClassName]
	if !exists || expectedStorage.UID == "" || expectedStorage.Provisioner == "" {
		return configurationCondition(metav1.ConditionFalse, "UnapprovedStorage", "The home StorageClass is not approved."), nil
	}
	storageClass := &storagev1.StorageClass{}
	if err := reader.Get(ctx, types.NamespacedName{Name: spec.Storage.Home.StorageClassName}, storageClass); err != nil {
		return prerequisiteReadError(err)
	}
	if storageClass.UID != expectedStorage.UID || storageClass.Provisioner != expectedStorage.Provisioner ||
		!storageClass.DeletionTimestamp.IsZero() {
		return configurationCondition(metav1.ConditionFalse, "ReferenceChanged", "StorageClass no longer matches the approved prerequisite."), nil
	}
	return configurationCondition(metav1.ConditionTrue, "PrerequisitesReady", "Registered runtime, storage and control prerequisites are available; Agent enforcement is checked separately."), nil
}

func (r *AgentPolicyReconciler) assess(ctx context.Context, object *api.AgentPolicy, now time.Time) (metav1.Condition, error) {
	if !object.DeletionTimestamp.IsZero() {
		return configurationCondition(metav1.ConditionFalse, "Terminating", "AgentPolicy configuration is terminating."), nil
	}
	if _, errs := sandbox.NormalizeAgentPolicy(object.Spec); len(errs) != 0 {
		return configurationCondition(metav1.ConditionFalse, "InvalidConfiguration", "AgentPolicy configuration is invalid."), nil
	}
	snapshot, err := configurationSnapshot(ctx, r.Source, now, r.MaxSnapshotAge)
	if err != nil {
		return configurationCondition(metav1.ConditionUnknown, "PrerequisitesUnavailable", "Current platform support inventory is unavailable."), err
	}
	keys := make([]api.SandboxType, 0, len(snapshot.Registrations))
	for key := range snapshot.Registrations {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var unsupported error = sandboxpolicy.ErrUnsupportedControl
	for _, key := range keys {
		platform, exists := snapshot.Platforms[key]
		if !exists {
			unsupported = sandboxpolicy.ErrUnavailable
			continue
		}
		err := sandboxpolicy.ValidatePolicyConfiguration(object, snapshot.Registrations[key].Capabilities, platform)
		if err == nil {
			return configurationCondition(metav1.ConditionTrue, "PolicyAccepted", "Policy controls and grants are valid for a registered adapter; this does not report per-Agent enforcement."), nil
		}
		unsupported = err
	}
	return configurationErrorCondition(unsupported), nil
}

func runtimePrerequisitesSupported(spec api.SandboxSpec, capabilities sandbox.Capabilities) bool {
	return len(validation.IsDNS1123Subdomain(capabilities.RuntimeClassName)) == 0 && capabilities.Revision != "" &&
		capabilities.HostResourceLimits && capabilities.GuestExternalNetwork && capabilities.DenyPrecedence &&
		capabilities.NodeTraffic && capabilities.DNSIPRestrictions && capabilities.EnforcementObservation &&
		capabilities.RestrictiveFailure &&
		(!capabilities.RequirePerContainerResourceLimits || capabilities.PerContainerResourceLimits) &&
		slices.Contains(capabilities.HomeAccessModes, spec.Storage.Home.AccessMode)
}

func prerequisiteReadError(err error) (metav1.Condition, error) {
	if apierrors.IsNotFound(err) {
		return configurationCondition(metav1.ConditionFalse, "ReferenceMissing", "A registered runtime or storage prerequisite is missing."), nil
	}
	return configurationCondition(metav1.ConditionUnknown, "PrerequisitesUnavailable", "A current runtime or storage prerequisite could not be read."), err
}

func configurationErrorCondition(err error) metav1.Condition {
	switch {
	case errors.Is(err, sandboxpolicy.ErrUnavailable):
		return configurationCondition(metav1.ConditionUnknown, "PrerequisitesUnavailable", "Current trusted prerequisite bindings are unavailable.")
	case errors.Is(err, sandboxpolicy.ErrUnapprovedGrant):
		return configurationCondition(metav1.ConditionFalse, "UnapprovedGrant", "A requested identity or role grant is not approved.")
	case errors.Is(err, sandboxpolicy.ErrConflict):
		return configurationCondition(metav1.ConditionFalse, "PolicyConflict", "Policy rules conflict with required platform controls.")
	default:
		return configurationCondition(metav1.ConditionFalse, "UnsupportedControl", "A requested control has no supported, qualified implementation.")
	}
}

func configurationCondition(status metav1.ConditionStatus, reason, message string) metav1.Condition {
	return metav1.Condition{Status: status, Reason: reason, Message: message}
}

func configurationReader(reader client.Reader, fallback client.Client) client.Reader {
	if reader != nil {
		return reader
	}
	return fallback
}

func configurationNow(clock func() time.Time) time.Time {
	if clock != nil {
		return clock()
	}
	return time.Now()
}

func configurationRefresh(maxAge time.Duration) time.Duration {
	if maxAge <= 0 {
		maxAge = 30 * time.Second
	}
	return maxAge / 2
}

func configurationSnapshot(ctx context.Context, source SandboxConfigurationSource, now time.Time, maxAge time.Duration) (SandboxConfigurationSnapshot, error) {
	if source == nil {
		return SandboxConfigurationSnapshot{}, sandboxpolicy.ErrUnavailable
	}
	snapshot, err := source.Snapshot(ctx)
	if err != nil {
		return SandboxConfigurationSnapshot{}, err
	}
	if maxAge <= 0 {
		maxAge = 30 * time.Second
	}
	if now.IsZero() || snapshot.ObservedAt.IsZero() || snapshot.ObservedAt.After(now) || now.Sub(snapshot.ObservedAt) > maxAge {
		return SandboxConfigurationSnapshot{}, fmt.Errorf("%w: platform snapshot is stale or incomplete", sandboxpolicy.ErrUnavailable)
	}
	return snapshot, nil
}
