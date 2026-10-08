// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"
	"slices"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxcoordination"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	strictjson "sigs.k8s.io/json"
)

// SandboxExecutionIntent connects held preparation to the coordination
// protocol. The exact target must have been independently enrolled behind a
// guest-external start gate. This method never lifts the activation hold,
// selects a Pod from an observation, or marks an Agent ready.
func (r *AgentReconciler) SandboxExecutionIntent(ctx context.Context, requested *api.Agent,
	target sandboxcoordination.EnrolledTarget,
) (sandboxcoordination.Intent, error) {
	var empty sandboxcoordination.Intent
	if requested == nil || r.APIReader == nil || r.SandboxPreparation == nil ||
		r.SandboxPreparation.Configuration == nil || r.SandboxPreparation.Prerequisites == nil ||
		r.SandboxPreparation.Installer == nil || r.SandboxPreparation.Placement == nil {
		return empty, fmt.Errorf("%w: trusted preparation and dedicated placement are required", sandboxpolicy.ErrUnavailable)
	}
	attempt, err := r.resolveSandboxPreparation(ctx, requested)
	if err != nil {
		return empty, err
	}
	attempt.prerequisites, err = r.SandboxPreparation.Prerequisites.Resolve(ctx, attempt.agent.DeepCopy(), attempt.team.DeepCopy())
	if err != nil {
		return empty, err
	}
	if err := r.inheritSandboxPlacement(ctx, attempt.agent, &attempt.prerequisites); err != nil {
		return empty, err
	}
	if err := r.sandboxPreparationInput(ctx, attempt); err != nil {
		return empty, err
	}
	attempt.platformValues, attempt.userValues, err = r.sandboxHelmValues(attempt.agent, attempt.team)
	if err != nil {
		return empty, err
	}
	if reused, err := r.reuseSandboxPreparation(ctx, attempt); err != nil || !reused {
		if err != nil {
			return empty, err
		}
		return empty, fmt.Errorf("%w: current held preparation must be verified before reservation", sandboxpolicy.ErrUnavailable)
	}
	plan, err := sandboxpolicy.Compile(sandboxpolicy.CompileInput{
		Resolution: *attempt.resolution, AgentName: attempt.agent.Name, Namespace: attempt.input.Namespace.Name,
		Platform: attempt.configuration.Platforms[attempt.resolution.Sandbox.Type],
	})
	if err != nil {
		return empty, err
	}
	state := &corev1.ConfigMap{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: r.OperatorNamespace,
		Name: w.PodExpectationName(attempt.input.Namespace.Name, attempt.input.Namespace.UID)}, state); err != nil {
		return empty, err
	}
	var expected w.Expectation
	raw := state.Data[w.PodExpectationKey]
	if len(raw) == 0 || len(raw) > 512*1024 {
		return empty, fmt.Errorf("%w: prepared expectation exceeds its bound", sandboxpolicy.ErrUnavailable)
	}
	problems, err := strictjson.UnmarshalStrict([]byte(raw), &expected)
	if err != nil || len(problems) != 0 || len(state.Data) != 1 || len(state.BinaryData) != 0 ||
		state.UID == "" || state.ResourceVersion == "" || !state.DeletionTimestamp.IsZero() ||
		expected.Version != w.ExpectationVersion || !expected.ExecutionHeld ||
		expected.Agent != (w.ObjectIdentity{Name: attempt.agent.Name, UID: attempt.agent.UID}) ||
		expected.AgentGeneration != attempt.agent.Generation || expected.Namespace != attempt.input.Namespace ||
		expected.Home != attempt.prerequisites.Home || expected.Skills != attempt.prerequisites.Skills ||
		expected.WorkloadKind != w.AgentWorkload || expected.Workload.Name != attempt.input.WorkloadName ||
		expected.ControllerRevision == "" || expected.Binding != plan.Binding || expected.Revision != plan.Revision ||
		target.Binding != plan.Binding || target.Revision != plan.Revision ||
		state.Annotations[w.AgentUIDAnnotation] != string(attempt.agent.UID) ||
		state.Annotations[w.RevisionAnnotation] != expected.Revision ||
		!equality.Semantic.DeepEqual(state.OwnerReferences, []metav1.OwnerReference{w.AgentOwner(attempt.agent.Name, attempt.agent.UID)}) ||
		!equality.Semantic.DeepEqual(expected.Placement, attempt.prerequisites.Placement) {
		return empty, fmt.Errorf("%w: current held preparation and enrolled target do not agree", sandboxpolicy.ErrUnavailable)
	}
	if !slices.ContainsFunc(expected.Sources, func(source w.SourceReference) bool {
		return source.APIVersion == "apps/v1" && source.Kind == "ControllerRevision" &&
			source.Namespace == expected.Namespace.Name && source.Name == expected.ControllerRevision
	}) {
		return empty, fmt.Errorf("%w: prepared ControllerRevision must be pinned", sandboxpolicy.ErrUnavailable)
	}
	stateSource := sandboxSource(state, "v1", "ConfigMap")
	selectedPolicy := attempt.selectedPolicy().Name
	sources := make([]w.SourceReference, 0, len(expected.Sources)+1)
	for _, source := range expected.Sources {
		// Preparation validates every Team slot; only the selected Sandbox
		// and policy affect this execution's configuration reservation.
		if source.APIVersion == api.GroupVersion.String() &&
			((source.Kind == "Sandbox" && source.Name != attempt.agent.Spec.SandboxRef) ||
				(source.Kind == "AgentPolicy" && source.Name != selectedPolicy)) {
			continue
		}
		sources = append(sources, source)
	}
	sources = append(sources, stateSource)
	workload := &appsv1.StatefulSet{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: expected.Namespace.Name, Name: expected.Workload.Name}, workload); err != nil {
		return empty, err
	}
	if !slices.Contains(expected.Sources, sandboxSource(workload, "apps/v1", "StatefulSet")) {
		return empty, fmt.Errorf("%w: workload changed before execution inputs were captured", errSandboxSourceChanged)
	}
	workloadDigest, err := sandboxExecutionWorkloadDigest(workload)
	if err != nil {
		return empty, err
	}
	intent := sandboxcoordination.Intent{
		Agent: expected.Agent, AgentGeneration: expected.AgentGeneration, Namespace: expected.Namespace,
		Home: expected.Home, Workload: expected.Workload, Expected: target.ExecutionIdentity, Preparation: stateSource,
		WorkloadDigest: workloadDigest,
		Writer:         target.Writer, WriterJournal: target.WriterJournal,
		Placement: expected.Placement.DeepCopy(), Sources: sources,
		IngressMode: plan.Network.IngressMode,
	}
	if err := intent.Validate(); err != nil {
		return empty, err
	}
	if err := r.CheckSandboxExecutionSources(ctx, intent); err != nil {
		return empty, err
	}
	return intent, nil
}

// CheckSandboxExecutionSources re-resolves current authorization and compiler
// inputs using authoritative reads. It does not stop a writer, restage Helm, or
// renew a stale intent. A participating adapter must additionally verify actual
// runtime/control enforcement at its start and publication boundaries.
func (r *AgentReconciler) CheckSandboxExecutionSources(ctx context.Context, intent sandboxcoordination.Intent) error {
	if r.APIReader == nil || r.SandboxPreparation == nil || r.SandboxPreparation.Configuration == nil ||
		r.SandboxPreparation.Placement == nil {
		return fmt.Errorf("%w: execution source verification is not configured", sandboxpolicy.ErrUnavailable)
	}
	if err := intent.Validate(); err != nil {
		return err
	}
	if err := r.verifySandboxExecutionSources(ctx, intent); err != nil {
		return err
	}
	attempt, err := r.resolveSandboxPreparation(ctx, &api.Agent{ObjectMeta: metav1.ObjectMeta{
		Name: intent.Agent.Name, UID: intent.Agent.UID, Generation: intent.AgentGeneration,
	}})
	if err != nil {
		return err
	}
	platform := attempt.configuration.Platforms[attempt.resolution.Sandbox.Type]
	if err := sandboxpolicy.VerifyPolicyGrants(ctx, r.APIReader, attempt.selectedPolicy(), platform.Grants); err != nil {
		return err
	}
	plan, err := sandboxpolicy.Compile(sandboxpolicy.CompileInput{
		Resolution: *attempt.resolution, AgentName: intent.Agent.Name, Namespace: intent.Namespace.Name, Platform: platform,
	})
	if err != nil {
		return err
	}
	if plan.Binding != intent.Expected.Binding || plan.Revision != intent.Expected.Revision ||
		plan.Network.IngressMode != intent.IngressMode {
		return fmt.Errorf("%w: execution authorization or compiled policy changed", errSandboxSourceChanged)
	}
	attempt.prerequisites.Placement = intent.Placement.DeepCopy()
	if err := r.verifySandboxPlacement(ctx, attempt); err != nil {
		return err
	}
	if err := r.verifySandboxExecutionSources(ctx, intent); err != nil {
		return err
	}
	return configurationSnapshotTime(attempt.configuration.ObservedAt,
		configurationNow(r.SandboxPreparation.Now), r.SandboxPreparation.MaxSnapshotAge)
}

func (r *AgentReconciler) verifySandboxExecutionSources(ctx context.Context, intent sandboxcoordination.Intent) error {
	sources := make([]w.SourceReference, 0, len(intent.Sources))
	for _, source := range intent.Sources {
		if source.APIVersion == "apps/v1" && source.Kind == "StatefulSet" &&
			source.Namespace == intent.Namespace.Name && source.Name == intent.Workload.Name {
			continue
		}
		sources = append(sources, source)
	}
	if err := r.verifySandboxSources(ctx, sources); err != nil {
		return err
	}
	workload := &appsv1.StatefulSet{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: intent.Namespace.Name, Name: intent.Workload.Name}, workload); err != nil {
		return err
	}
	current, err := sandboxExecutionWorkloadDigest(workload)
	if err != nil {
		return err
	}
	if workload.UID != intent.Workload.UID || current != intent.WorkloadDigest {
		return fmt.Errorf("%w: workload identity or effective inputs changed", errSandboxSourceChanged)
	}
	return ctx.Err()
}

func sandboxExecutionWorkloadDigest(workload *appsv1.StatefulSet) (string, error) {
	if workload == nil || workload.UID == "" || workload.ResourceVersion == "" ||
		!workload.DeletionTimestamp.IsZero() || ptr.Deref(workload.Spec.Replicas, 1) < 0 ||
		ptr.Deref(workload.Spec.Replicas, 1) > 1 {
		return "", fmt.Errorf("%w: a persisted, nonterminating zero/one-replica workload is required", errSandboxSourceChanged)
	}
	spec := workload.Spec.DeepCopy()
	// Zero/one replica intent belongs to controlled start/stop. Status,
	// observation versions and managed field ownership are not configuration.
	spec.Replicas = nil
	return sandboxPreparationDigest(struct {
		Spec        *appsv1.StatefulSetSpec
		Owners      []metav1.OwnerReference
		Labels      map[string]string
		Annotations map[string]string
	}{spec, workload.OwnerReferences, workload.Labels, workload.Annotations})
}
