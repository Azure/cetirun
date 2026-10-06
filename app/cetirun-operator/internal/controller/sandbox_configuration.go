// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
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
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxplatform"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
)

type SandboxRuntimePrerequisite = sandboxplatform.RuntimePrerequisite

type SandboxStoragePrerequisite = sandboxplatform.StoragePrerequisite

type SandboxConfigurationSnapshot = sandboxplatform.Snapshot

type SandboxConfigurationSource = sandboxplatform.Source

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
	if err := r.Get(ctx, request.NamespacedName, object); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	before := object.DeepCopy()
	condition, observedAt, observeErr := assessSandboxPrerequisites(ctx, reader, object, r.Source, r.MaxSnapshotAge, r.Now)
	now := configurationNow(r.Now)
	if observeErr == nil && !observedAt.IsZero() {
		if err := configurationSnapshotTime(observedAt, now, r.MaxSnapshotAge); err != nil {
			condition, observeErr = configurationErrorCondition(err), err
		}
	}
	condition.Type, condition.ObservedGeneration = api.ConditionReady, object.Generation
	condition.LastTransitionTime = metav1.NewTime(now)
	apimeta.SetStatusCondition(&object.Status.Conditions, condition)
	object.Status.ObservedGeneration = object.Generation
	if !reflect.DeepEqual(before.Status, object.Status) {
		if err := r.Status().Patch(ctx, object, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			return ctrl.Result{}, errors.Join(observeErr, fmt.Errorf("update Sandbox prerequisite status: %w", err))
		}
	}
	if observeErr != nil {
		return ctrl.Result{}, observeErr
	}
	return ctrl.Result{RequeueAfter: configurationRefresh(r.MaxSnapshotAge, now, observedAt)}, nil
}

func (r *AgentPolicyReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	object := &api.AgentPolicy{}
	reader := configurationReader(r.APIReader, r.Client)
	if err := r.Get(ctx, request.NamespacedName, object); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	before := object.DeepCopy()
	condition, observedAt, observeErr := r.assess(ctx, reader, object)
	now := configurationNow(r.Now)
	if observeErr == nil && !observedAt.IsZero() {
		if err := configurationSnapshotTime(observedAt, now, r.MaxSnapshotAge); err != nil {
			condition, observeErr = configurationErrorCondition(err), err
		}
	}
	condition.Type, condition.ObservedGeneration = api.ConditionAgentPolicyAccepted, object.Generation
	condition.LastTransitionTime = metav1.NewTime(now)
	apimeta.SetStatusCondition(&object.Status.Conditions, condition)
	object.Status.ObservedGeneration = object.Generation
	if !reflect.DeepEqual(before.Status, object.Status) {
		if err := r.Status().Patch(ctx, object, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			return ctrl.Result{}, errors.Join(observeErr, fmt.Errorf("update AgentPolicy configuration status: %w", err))
		}
		emitSandboxPolicyTransition(ctx, before, object)
	}
	if observeErr != nil {
		return ctrl.Result{}, observeErr
	}
	return ctrl.Result{RequeueAfter: configurationRefresh(r.MaxSnapshotAge, now, observedAt)}, nil
}

func assessSandboxPrerequisites(ctx context.Context, reader client.Reader, object *api.Sandbox,
	source SandboxConfigurationSource, maxAge time.Duration, clock func() time.Time,
) (metav1.Condition, time.Time, error) {
	if !object.DeletionTimestamp.IsZero() {
		return configurationCondition(metav1.ConditionFalse, "Terminating", "Sandbox configuration is terminating."), time.Time{}, nil
	}
	spec, errs := sandbox.NormalizeSandbox(object.Spec)
	if len(errs) != 0 {
		return configurationCondition(metav1.ConditionFalse, "InvalidConfiguration", "Sandbox configuration is invalid."), time.Time{}, nil
	}
	snapshot, err := configurationSnapshot(ctx, source, clock, maxAge)
	if err != nil {
		return configurationCondition(metav1.ConditionUnknown, "PrerequisitesUnavailable", "Current platform prerequisite state is unavailable."), time.Time{}, err
	}
	registration, exists := snapshot.Registrations[spec.Type]
	if !exists || !runtimePrerequisitesSupported(spec, registration.Capabilities) {
		return configurationCondition(metav1.ConditionFalse, "UnsupportedControl", "Sandbox runtime and mandatory controls are not qualified."), snapshot.ObservedAt, nil
	}
	if !registration.Available {
		return configurationCondition(metav1.ConditionFalse, "RuntimeUnavailable", "The registered Sandbox runtime is unavailable."), snapshot.ObservedAt, nil
	}
	platform, exists := snapshot.Platforms[spec.Type]
	if !exists {
		return configurationCondition(metav1.ConditionUnknown, "PrerequisitesUnavailable", "Trusted network topology and grants are unavailable."), snapshot.ObservedAt, nil
	}
	if err := sandboxpolicy.ValidatePlatform(platform, registration.Capabilities); err != nil {
		return configurationErrorCondition(err), snapshot.ObservedAt, nil
	}
	runtimeName := registration.Capabilities.RuntimeClassName
	expectedRuntime, exists := snapshot.RuntimeClasses[runtimeName]
	if !exists || expectedRuntime.UID == "" || expectedRuntime.Handler == "" {
		return configurationCondition(metav1.ConditionUnknown, "PrerequisitesUnavailable", "RuntimeClass identity approval is unavailable."), snapshot.ObservedAt, nil
	}
	runtimeClass := &nodev1.RuntimeClass{}
	if err := reader.Get(ctx, types.NamespacedName{Name: runtimeName}, runtimeClass); err != nil {
		condition, readErr := prerequisiteReadError(err)
		return condition, snapshot.ObservedAt, readErr
	}
	if runtimeClass.UID != expectedRuntime.UID || runtimeClass.Handler != expectedRuntime.Handler ||
		!runtimeClass.DeletionTimestamp.IsZero() {
		return configurationCondition(metav1.ConditionFalse, "ReferenceChanged", "RuntimeClass no longer matches the approved prerequisite."), snapshot.ObservedAt, nil
	}
	expectedStorage, exists := snapshot.StorageClasses[spec.Storage.Home.StorageClassName]
	if !exists || expectedStorage.UID == "" || expectedStorage.Provisioner == "" {
		return configurationCondition(metav1.ConditionFalse, "UnapprovedStorage", "The home StorageClass is not approved."), snapshot.ObservedAt, nil
	}
	storageClass := &storagev1.StorageClass{}
	if err := reader.Get(ctx, types.NamespacedName{Name: spec.Storage.Home.StorageClassName}, storageClass); err != nil {
		condition, readErr := prerequisiteReadError(err)
		return condition, snapshot.ObservedAt, readErr
	}
	if storageClass.UID != expectedStorage.UID || storageClass.Provisioner != expectedStorage.Provisioner ||
		!storageClass.DeletionTimestamp.IsZero() {
		return configurationCondition(metav1.ConditionFalse, "ReferenceChanged", "StorageClass no longer matches the approved prerequisite."), snapshot.ObservedAt, nil
	}
	return configurationCondition(metav1.ConditionTrue, "PrerequisitesReady", "Registered runtime, storage and control prerequisites are available; Agent enforcement is checked separately."), snapshot.ObservedAt, nil
}

func (r *AgentPolicyReconciler) assess(ctx context.Context, reader client.Reader, object *api.AgentPolicy) (metav1.Condition, time.Time, error) {
	if !object.DeletionTimestamp.IsZero() {
		return configurationCondition(metav1.ConditionFalse, "Terminating", "AgentPolicy configuration is terminating."), time.Time{}, nil
	}
	if _, errs := sandbox.NormalizeAgentPolicy(object.Spec); len(errs) != 0 {
		return configurationCondition(metav1.ConditionFalse, "InvalidConfiguration", "AgentPolicy configuration is invalid."), time.Time{}, nil
	}
	snapshot, err := configurationSnapshot(ctx, r.Source, r.Now, r.MaxSnapshotAge)
	if err != nil {
		return configurationCondition(metav1.ConditionUnknown, "PrerequisitesUnavailable", "Current platform support inventory is unavailable."), time.Time{}, err
	}
	keys := make([]api.SandboxType, 0, len(snapshot.Registrations))
	for key := range snapshot.Registrations {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var failures []error
	for _, key := range keys {
		platform, exists := snapshot.Platforms[key]
		if !exists {
			failures = append(failures, fmt.Errorf("registration %s: %w: platform inventory is missing", key, sandboxpolicy.ErrUnavailable))
			continue
		}
		err := sandboxpolicy.ValidatePolicyConfiguration(object, snapshot.Registrations[key].Capabilities, platform)
		if err == nil {
			err = sandboxpolicy.VerifyPolicyGrants(ctx, reader, object, platform.Grants)
		}
		if err == nil {
			return configurationCondition(metav1.ConditionTrue, "PolicyAccepted", "Standalone policy support and requested role approvals match a registered adapter; Sandbox compatibility, home access and per-Agent enforcement are not assessed."), snapshot.ObservedAt, nil
		}
		failures = append(failures, fmt.Errorf("registration %s: %w", key, err))
	}
	if len(failures) == 0 {
		failures = append(failures, sandboxpolicy.ErrUnsupportedControl)
	}
	unsupported := errors.Join(failures...)
	condition := configurationErrorCondition(unsupported)
	if errors.Is(unsupported, sandboxpolicy.ErrUnavailable) {
		return condition, snapshot.ObservedAt, unsupported
	}
	return condition, snapshot.ObservedAt, nil
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
	// Standalone/offline callers may supply a direct client. Manager setup always
	// installs its uncached APIReader for identity checks; primary CRs use Client
	// and optimistic status patches, not the authoritative prerequisite reader.
	return fallback
}

func configurationNow(clock func() time.Time) time.Time {
	if clock != nil {
		return clock()
	}
	return time.Now()
}

// Successful assessments refresh in [1/2, 5/8] of the snapshot's remaining
// lifetime. Errors use controller-runtime backoff, not this periodic requeue.
func configurationRefresh(maxAge time.Duration, now, observedAt time.Time) time.Duration {
	if maxAge <= 0 {
		maxAge = 30 * time.Second
	}
	remaining := maxAge
	if !observedAt.IsZero() {
		remaining -= now.Sub(observedAt)
	}
	base, jitter := remaining/2, remaining/8
	if jitter > 0 {
		base += time.Duration(rand.Int64N(int64(jitter) + 1))
	}
	return max(time.Nanosecond, base)
}

func configurationSnapshot(ctx context.Context, source SandboxConfigurationSource, clock func() time.Time, maxAge time.Duration) (SandboxConfigurationSnapshot, error) {
	if source == nil {
		return SandboxConfigurationSnapshot{}, sandboxpolicy.ErrUnavailable
	}
	snapshot, err := source.Snapshot(ctx)
	if err != nil {
		return SandboxConfigurationSnapshot{}, err
	}
	// A synchronous source observes after the caller starts the request.
	// Sampling the clock before Snapshot would reject fresh data as future-dated.
	if err := configurationSnapshotTime(snapshot.ObservedAt, configurationNow(clock), maxAge); err != nil {
		return SandboxConfigurationSnapshot{}, err
	}
	return snapshot, nil
}

func configurationSnapshotTime(observedAt, now time.Time, maxAge time.Duration) error {
	return sandboxplatform.CheckFreshness(observedAt, now, maxAge)
}
