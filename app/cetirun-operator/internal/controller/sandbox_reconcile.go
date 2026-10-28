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

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxbinding"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxcoordination"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxplatform"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
	w "github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	"github.com/Azure/CetiRun/app/internal/registryauth"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	sandboxApplyStateAnnotation = "sandbox.cetirun.microsoft.com/apply-state"
	sandboxApplyInitializing    = "initializing"
	sandboxApplyPending         = "pending"
	sandboxApplySubmitted       = "submitted"
	sandboxApplyCancelled       = "cancelled"
)

func (r *AgentReconciler) usesReconciledSandbox(ctx context.Context, agent *api.Agent) (bool, error) {
	if !sandboxRequested(agent, nil) ||
		r.SandboxPreparation == nil || !r.SandboxPreparation.NativeExecution ||
		controllerutil.ContainsFinalizer(agent, sandbox.PreparationFinalizer) {
		return false, nil
	}
	_, receipt, err := r.readSandboxBootstrap(ctx, agent)
	if apierrors.IsNotFound(err) && !controllerutil.ContainsFinalizer(agent, sandbox.BootstrapFinalizer) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read Sandbox lifetime mode: %w", err)
	}
	return receipt.Reconciled, nil
}

func (r *AgentReconciler) bootstrapReconciledSandbox(ctx context.Context, agent *api.Agent) error {
	if r.SandboxPreparation.bootstrap == nil {
		return fmt.Errorf("Sandbox reconciliation requires owned bootstrap")
	}
	attempt, err := r.resolveSandboxInputs(ctx, agent, false, true)
	if err != nil {
		return err
	}
	if err := r.bootstrapSandboxOwned(ctx, attempt); err != nil {
		return err
	}
	if err := r.customReader().Get(ctx, client.ObjectKeyFromObject(agent), agent); err != nil {
		return err
	}
	recorded, err := r.recordedReconciledSandbox(ctx, agent)
	if err != nil {
		return err
	}
	if !recorded {
		return fmt.Errorf("common Sandbox reconciliation requires its recorded bootstrap ownership; existing namespaces are not adopted")
	}
	return nil
}

func (r *AgentReconciler) prepareReconciledSandbox(ctx context.Context, agent *api.Agent,
	prepared *helm.PreparedRelease, values map[string]any,
) (*helm.PreparedRelease, error) {
	attempt, err := r.resolveSandboxInputs(ctx, agent, false, true)
	if err != nil {
		return nil, err
	}
	source, ok := r.SandboxPreparation.Prerequisites.(interface {
		ResolveCurrent(context.Context, *api.Agent, *api.Team) (SandboxPrerequisites, error)
	})
	if !ok {
		return nil, fmt.Errorf("Sandbox prerequisites do not support current desired-state reconciliation")
	}
	attempt.prerequisites, err = source.ResolveCurrent(ctx, attempt.agent, attempt.team)
	if err != nil {
		return nil, err
	}
	if err := r.sandboxPreparationInput(ctx, attempt); err != nil {
		return nil, err
	}
	// Suspending an Agent only reconciles the already-owned workloads to zero.
	// It must not create or update terminal access RBAC because the generation
	// guard intentionally rejects suspended Agents.
	if attempt.input.Terminal != nil && !attempt.agent.Spec.Suspended {
		if err := r.ensureTerminalReadRoles(ctx, attempt.agent); err != nil {
			return nil, fmt.Errorf("ensure common Custom terminal RBAC: %w", err)
		}
	}
	// resolveSandboxInputs reads the current, UID-pinned Agent after Gateway
	// reconciliation. Use that snapshot for every protected render input;
	// the reconciler's argument can predate the first status projection.
	agent = attempt.agent
	if agent.Spec.Agent.RegistrySecretRef != nil {
		name, err := registryauth.PullSecretName(agent.UID)
		if err != nil {
			return nil, err
		}
		secret := &corev1.Secret{}
		if err := r.customReader().Get(ctx, client.ObjectKey{Namespace: attempt.input.Namespace.Name, Name: name}, secret); err != nil {
			return nil, fmt.Errorf("read Sandbox registry pull Secret: %w", err)
		}
		receipt := agent.Status.RegistryCredentials
		if receipt == nil || receipt.SourceUID == "" || receipt.SourceResourceVersion == "" ||
			!isConditionTrue(agent.Status.Conditions, api.ConditionRegistryCredentialsReady) ||
			secret.UID == "" || secret.ResourceVersion == "" || string(secret.UID) != receipt.PullSecretUID ||
			secret.Annotations[registryauth.SourceUID] != receipt.SourceUID ||
			secret.Annotations[registryauth.SourceVersion] != receipt.SourceResourceVersion ||
			registryauth.Validate(secret, agent) != nil {
			return nil, fmt.Errorf("Sandbox registry pull Secret does not match its synchronized approval")
		}
		attempt.input.ImagePullSecret = &w.ObjectIdentity{Name: secret.Name, UID: secret.UID}
		attempt.sources = append(attempt.sources, sandboxSource(secret, "v1", "Secret"))
	}
	// Rebuild platform values from the uncached Agent read above. The initial
	// chart capability probe can precede the first Gateway status projection;
	// execution values must instead include the current, verified key and model
	// projection captured by sandboxPreparationInput.
	platformValues, user, err := r.sandboxHelmValues(agent, attempt.team)
	if err != nil {
		return nil, err
	}
	// The caller observes the controller-owned runtime Secret after credential
	// reconciliation and supplies its resource version solely to trigger a
	// workload rollout. Do not merge the caller's broader Helm values here:
	// common Sandbox rendering owns the typed runtime contract and validates
	// Agent values separately below.
	if callerAnnotations, ok := values["podAnnotations"].(map[string]any); ok {
		if secretVersion, ok := callerAnnotations[gatewaySecretResourceVersionAnnotation].(string); ok && secretVersion != "" {
			platformValues["podAnnotations"] = deepMerge(platformValues["podAnnotations"], map[string]any{
				gatewaySecretResourceVersionAnnotation: secretVersion,
			})
		}
	}
	if agent.Spec.Suspended {
		// A common reconciled render normally starts its workload. Suspension is
		// the controller-owned exception and must stay at zero through the same
		// typed renderer used for resume.
		platformValues["replicaCount"] = 0
	}
	platformValues["memoryOptions"] = r.memoryOptions()
	if err := w.ValidateReconciledUserValues(user, attempt.input.AuthorizedSecrets); err != nil {
		return nil, err
	}
	// ReconcileSandbox accepts one values map after its first protected render.
	// Keep the already validated per-Agent values when rebuilding that render;
	// otherwise a Custom ttyd choice could disappear after its capability probe.
	effectiveValues := platformValues
	if len(user) != 0 {
		effectiveValues = deepMerge(platformValues, user).(map[string]any)
	}
	if agent.Spec.Suspended && attempt.input.Terminal != nil {
		// The terminal is a distinct Deployment. Do not let a normal common
		// render bring it back after the suspension path has scaled it down.
		// This controller-owned override is only used for the zero-replica
		// suspension render; the original requested value is used on resume.
		effectiveValues["ttyd"] = map[string]any{"enabled": false}
	}
	configuration, ok := r.SandboxPreparation.Configuration.(*sandboxplatform.KubernetesSource)
	if !ok {
		return nil, fmt.Errorf("common Sandbox reconciliation requires the configured Kubernetes authority")
	}
	authority := &corev1.ConfigMap{}
	if err := r.customReader().Get(ctx, client.ObjectKey{Namespace: configuration.Namespace, Name: configuration.Name}, authority); err != nil {
		return nil, err
	}
	attempt.sources = append(attempt.sources, sandboxSource(authority, "v1", "ConfigMap"))
	snapshot := attempt.configuration
	snapshot.ObservedAt = time.Time{}
	snapshotDigest, err := sandboxPreparationDigest(snapshot)
	if err != nil {
		return nil, err
	}
	platform := attempt.configuration.Platforms[attempt.resolution.Sandbox.Type]
	attempt.plan, err = sandboxpolicy.Compile(sandboxpolicy.CompileInput{
		NativeExecution: true, Resolution: *attempt.resolution, AgentName: agent.Name,
		Namespace: attempt.input.Namespace.Name, Platform: platform,
		CustomIngress: r.sandboxCustomIngress(attempt), TerminalIngress: r.sandboxTerminalIngress(attempt),
	})
	if err != nil {
		return nil, err
	}
	attempt.input.Controls, err = w.ControlsFromPlan(*attempt.plan)
	if err != nil {
		return nil, err
	}
	bundle, err := w.Build(attempt.input)
	if err != nil {
		return nil, err
	}
	if prepared == nil {
		prepared, err = r.SandboxPreparation.Installer.PrepareSandbox(
			attempt.input.Namespace.Name, platformValues, user, agent.Spec.Helm.Version, bundle,
		)
		if err != nil {
			return nil, err
		}
	}
	if prepared.SupportsValuePath(aiGatewayChartValue, aiGatewayChartCopilotValue, aiGatewayChartResponsesValue) {
		r.applyCopilotResponsesModels(platformValues, agent)
	}
	if prepared.SupportsValuePath(aiGatewayChartValue, aiGatewayChartCopilotValue, aiGatewayChartProxyKeyValue) {
		applyCopilotProxyKeyID(platformValues, agent)
	}
	if prepared.SupportsValuePath(aiGatewayChartValue, aiGatewayChartCopilotValue, aiGatewayChartSignInValue) {
		applyCopilotSignInGeneration(platformValues, agent)
	}
	sources := selectedSandboxExecutionSources(attempt.sources, attempt.resolution.Binding)
	agentSource, err := w.AgentSourceReference(agent)
	if err != nil {
		return nil, err
	}
	sources = append(sources, agentSource)
	digests := make(map[string]string, len(sources))
	for _, source := range sources {
		if !w.EffectiveSource(source) {
			continue
		}
		// The render attempt contains the Team snapshot that was materialized
		// into Custom's immutable context ConfigMap. Capture that same Team
		// resourceVersion for the initial digest; otherwise a Team update between
		// snapshot and digest could authorize new Team inputs with old context.
		capture := source.APIVersion == api.GroupVersion.String() && source.Kind == "Team"
		digest, err := r.nativeSourceDigest(ctx, source, agent.Spec.SandboxRef, capture)
		if err != nil {
			return nil, err
		}
		digests[w.SourceKey(source)] = digest
	}
	var published *corev1.ConfigMap
	var reservation *sandboxcoordination.NativeTicket
	var migrationTemplate *corev1.PodSpec
	guard := func(ctx context.Context) error {
		if err := r.checkMigrationApply(ctx, agent, migrationTemplate); err != nil {
			return err
		}
		if reservation != nil {
			if err := r.SandboxNativeCoordination.CheckNative(ctx, *reservation); err != nil {
				return err
			}
		}
		if published != nil {
			current := &corev1.ConfigMap{}
			if err := r.customReader().Get(ctx, client.ObjectKeyFromObject(published), current); err != nil {
				return err
			}
			if _, err := w.ReadReconcileExpectation(current); err != nil {
				return err
			}
			if current.UID != published.UID || current.ResourceVersion != published.ResourceVersion ||
				!reflect.DeepEqual(current.Data, published.Data) ||
				current.Annotations[sandboxApplyStateAnnotation] == sandboxApplyCancelled {
				return fmt.Errorf("Sandbox desired authority changed before apply")
			}
		}
		if err := configurationSnapshotTime(attempt.configuration.ObservedAt, configurationNow(r.SandboxPreparation.Now), r.SandboxPreparation.MaxSnapshotAge); err != nil {
			return err
		}
		currentConfiguration, err := configurationSnapshot(ctx, r.SandboxPreparation.Configuration, r.SandboxPreparation.Now, r.SandboxPreparation.MaxSnapshotAge)
		if err != nil {
			return err
		}
		currentConfiguration.ObservedAt = time.Time{}
		digest, err := sandboxPreparationDigest(currentConfiguration)
		if err != nil || digest != snapshotDigest {
			return fmt.Errorf("Sandbox platform approvals changed before reconciliation")
		}
		current, binding, err := sandboxbinding.Read(ctx, r.customReader(), client.ObjectKeyFromObject(agent))
		if err != nil {
			return err
		}
		currentAgentSource, err := w.AgentSourceReference(current)
		if err != nil || current.UID != agent.UID || currentAgentSource != agentSource || binding != attempt.resolution.Binding ||
			current.Spec.Suspended != agent.Spec.Suspended || !current.DeletionTimestamp.IsZero() {
			return fmt.Errorf("Sandbox intent changed before resource reconciliation")
		}
		for _, source := range sources {
			if !w.EffectiveSource(source) {
				if err := r.verifySandboxSources(ctx, []w.SourceReference{source}); err != nil {
					return err
				}
				continue
			}
			digest, err := r.nativeSourceDigest(ctx, source, current.Spec.SandboxRef, false)
			if err != nil {
				return err
			}
			if digest != digests[w.SourceKey(source)] {
				return fmt.Errorf("Sandbox input changed before resource reconciliation")
			}
		}
		return nil
	}
	applyGuard := func(ctx context.Context) error {
		if published == nil || published.Annotations[sandboxApplyStateAnnotation] == sandboxApplyInitializing {
			return fmt.Errorf("Sandbox apply requires an initialized expectation")
		}
		if err := guard(ctx); err != nil {
			return err
		}
		if published.Annotations[sandboxApplyStateAnnotation] == sandboxApplyPending {
			submitted := published.DeepCopy()
			submitted.Annotations[sandboxApplyStateAnnotation] = sandboxApplySubmitted
			if err := r.Patch(ctx, submitted, client.MergeFromWithOptions(published, client.MergeFromWithOptimisticLock{})); err != nil {
				return fmt.Errorf("record Sandbox apply submission: %w", err)
			}
			published = submitted
		}
		return guard(ctx)
	}
	result, err := prepared.ReconcileSandbox(effectiveValues, bundle, applyGuard)
	if err != nil {
		return nil, err
	}
	expected, err := result.ReconcileExpectation()
	if err != nil {
		return nil, err
	}
	for _, workload := range expected.Workloads {
		if workload.Kind == "StatefulSet" {
			migrationTemplate = workload.Template.Spec.DeepCopy()
		}
	}
	for _, object := range expected.Controls {
		current := &unstructured.Unstructured{}
		current.SetGroupVersionKind(object.GroupVersionKind())
		err := r.customReader().Get(ctx, client.ObjectKeyFromObject(&object), current)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !sandboxBuiltinOwned(current, agent) || !current.GetDeletionTimestamp().IsZero() {
			return nil, fmt.Errorf("Sandbox refuses to adopt an unowned or terminating chart resource")
		}
	}
	var pods []corev1.PodSpec
	for _, workload := range expected.Workloads {
		pods = append(pods, workload.Template.Spec)
		for _, source := range w.PodSourceReferences(workload.Template.Spec, expected.Namespace.Name) {
			if source.Kind != "Secret" && source.Kind != "ConfigMap" && source.Kind != "ServiceAccount" {
				continue
			}
			if slices.ContainsFunc(sources, func(existing w.SourceReference) bool { return w.SourceKey(source) == w.SourceKey(existing) }) {
				continue
			}
			if slices.ContainsFunc(expected.Controls, func(object unstructured.Unstructured) bool {
				return object.GetKind() == source.Kind && object.GetNamespace() == source.Namespace && object.GetName() == source.Name
			}) {
				continue
			}
			object := &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: source.Kind}}
			if err := r.customReader().Get(ctx, client.ObjectKey{Namespace: source.Namespace, Name: source.Name}, object); err != nil {
				return nil, err
			}
			if (!sandboxPlatformPodSource(source, expected.Namespace.Name) && !sandboxBuiltinOwned(object, agent)) || !object.DeletionTimestamp.IsZero() {
				return nil, fmt.Errorf("Sandbox component source is not owned by the current Agent")
			}
			source = sandboxSource(object, "v1", source.Kind)
			sources = append(sources, source)
			if source.Kind == "Secret" {
				attempt.input.AuthorizedSecrets = append(attempt.input.AuthorizedSecrets, source.Name)
			} else {
				digest, err := r.nativeSourceDigest(ctx, source, agent.Spec.SandboxRef, false)
				if err != nil {
					return nil, err
				}
				digests[w.SourceKey(source)] = digest
			}
		}
	}
	expected.Sources, expected.SourceDigests = sources, digests
	role, binding, access, err := r.sandboxSourceAccess(attempt, pods...)
	if err != nil {
		return nil, err
	}
	journal := r.sandboxGrantJournal()
	previous, err := journal.Read(ctx, agent.Name, agent.UID)
	missingJournal := apierrors.IsNotFound(err)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, err
	}
	for index := range expected.Workloads {
		workload := &expected.Workloads[index]
		current := &unstructured.Unstructured{}
		current.SetAPIVersion("apps/v1")
		current.SetKind(workload.Kind)
		err := r.customReader().Get(ctx, client.ObjectKey{Namespace: expected.Namespace.Name, Name: workload.Identity.Name}, current)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !sandboxBuiltinOwned(current, agent) {
			return nil, fmt.Errorf("Sandbox refuses to adopt an unowned workload")
		}
		workload.Identity.UID = current.GetUID()
	}
	if err := guard(ctx); err != nil {
		return nil, err
	}
	desired, err := expected.Object(r.OperatorNamespace)
	if err != nil {
		return nil, err
	}
	unobserved := !slices.ContainsFunc(expected.Workloads, func(workload w.RenderedWorkload) bool {
		return workload.Identity.UID != ""
	})
	current := &corev1.ConfigMap{}
	err = r.customReader().Get(ctx, client.ObjectKeyFromObject(desired), current)
	if apierrors.IsNotFound(err) {
		// Persist non-submission before the journal so either publication can
		// retry without losing the proof. Older or lost records stay uncertain.
		if missingJournal && unobserved {
			desired.Annotations[sandboxApplyStateAnnotation] = sandboxApplyInitializing
		}
		err = r.Create(ctx, desired)
		current = desired.DeepCopy()
	} else if err == nil {
		recorded, readErr := w.ReadReconcileExpectation(current)
		if readErr != nil || recorded.Agent != expected.Agent || recorded.Namespace != expected.Namespace || recorded.Home != expected.Home {
			return nil, fmt.Errorf("Sandbox expectation cannot replace a different recorded lifetime")
		}
		if missingJournal && current.Annotations[sandboxApplyStateAnnotation] != sandboxApplyInitializing {
			return nil, fmt.Errorf("Sandbox grant authority is missing for an existing execution")
		}
		switch state := current.Annotations[sandboxApplyStateAnnotation]; state {
		case sandboxApplyInitializing:
			if !unobserved || len(previous) != 0 {
				return nil, fmt.Errorf("initializing Sandbox expectation has recorded grants or an observed workload")
			}
			desired.Annotations[sandboxApplyStateAnnotation] = state
		case sandboxApplyPending, sandboxApplyCancelled:
			if !unobserved {
				return nil, fmt.Errorf("unsubmitted Sandbox expectation has an observed workload")
			}
			desired.Annotations[sandboxApplyStateAnnotation] = sandboxApplyPending
		case sandboxApplySubmitted:
			desired.Annotations[sandboxApplyStateAnnotation] = state
		case "":
		default:
			return nil, fmt.Errorf("Sandbox apply checkpoint is invalid")
		}
		if !reflect.DeepEqual(current.Data, desired.Data) || !reflect.DeepEqual(current.Annotations, desired.Annotations) {
			base := current.DeepCopy()
			current.Data, current.Annotations = desired.Data, desired.Annotations
			err = r.Patch(ctx, current, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
		}
	}
	if err != nil {
		return nil, err
	}
	initializing := current.Annotations[sandboxApplyStateAnnotation] == sandboxApplyInitializing
	if missingJournal || initializing {
		if err := journal.Record(ctx, agent.Name, agent.UID, nil); err != nil {
			return nil, err
		}
	}
	if initializing {
		if err := guard(ctx); err != nil {
			return nil, err
		}
		// The journal is confirmed before this CAS; no grant or workload write
		// is allowed while the expectation is still initializing.
		base := current.DeepCopy()
		current.Annotations[sandboxApplyStateAnnotation] = sandboxApplyPending
		if err := r.Patch(ctx, current, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
			return nil, fmt.Errorf("finish Sandbox expectation initialization: %w", err)
		}
	}
	desiredGrants := []sandboxpolicy.BindingReference{access}
	if err := journal.Revoke(ctx, agent.Name, agent.UID, sandboxpolicy.ObsoleteBindings(previous, desiredGrants)); err != nil {
		return nil, err
	}
	if err := journal.Record(ctx, agent.Name, agent.UID, desiredGrants); err != nil {
		return nil, err
	}
	if err := r.applySandboxSourceAccess(ctx, agent, role, binding, access); err != nil {
		return nil, err
	}
	confirmed, err := r.sandboxBindingUIDs(ctx, desiredGrants)
	if err != nil {
		return nil, err
	}
	if err := journal.Record(ctx, agent.Name, agent.UID, confirmed); err != nil {
		return nil, err
	}
	published = current.DeepCopy()
	if r.SandboxNativeCoordination != nil && !agent.Spec.Suspended {
		intent, err := sandboxcoordination.ReconciledIntentFor(expected)
		if err != nil {
			return nil, err
		}
		ticket, err := r.SandboxNativeCoordination.AcquireNative(ctx, intent)
		if err != nil {
			return nil, err
		}
		reservation = &ticket
	}
	return result, guard(ctx)
}

// sandboxPlatformPodSource identifies inputs Kubernetes creates for every
// namespace. They are not owned by an Agent, but are safe only at their fixed,
// platform-defined identity. All other pod inputs must be Agent-owned.
func sandboxPlatformPodSource(source w.SourceReference, namespace string) bool {
	return (source.Kind == "ServiceAccount" && source.Name == "default") ||
		(source.Kind == "ConfigMap" && source.Name == "kube-root-ca.crt" && source.Namespace == namespace)
}

// readReconciledSandboxExpectation reads the durable contract emitted by the
// common executor. It is intentionally separate from the retained native
// single-start expectation reader, whose envelope has a different shape.
func (r *AgentReconciler) readReconciledSandboxExpectation(ctx context.Context, agent *api.Agent) (*corev1.ConfigMap, w.ReconcileExpectation, error) {
	var empty w.ReconcileExpectation
	namespace := &corev1.Namespace{}
	if err := r.customReader().Get(ctx, client.ObjectKey{Name: instanceNamespace(agent.Name)}, namespace); err != nil {
		return nil, empty, err
	}
	if !sandboxBuiltinOwned(namespace, agent) || !namespace.DeletionTimestamp.IsZero() {
		return nil, empty, fmt.Errorf("common Sandbox namespace is not current and owned")
	}
	state := &corev1.ConfigMap{}
	if err := r.customReader().Get(ctx, client.ObjectKey{
		Namespace: r.OperatorNamespace, Name: w.PodExpectationName(namespace.Name, namespace.UID),
	}, state); err != nil {
		return nil, empty, err
	}
	if state.Annotations[w.AgentUIDAnnotation] != string(agent.UID) ||
		!equality.Semantic.DeepEqual(state.OwnerReferences, []metav1.OwnerReference{w.AgentOwner(agent.Name, agent.UID)}) {
		return nil, empty, fmt.Errorf("common Sandbox expectation ownership changed")
	}
	expected, err := w.ReadReconcileExpectation(state)
	if err != nil {
		return nil, empty, err
	}
	if expected.Agent != (w.ObjectIdentity{Name: agent.Name, UID: agent.UID}) ||
		expected.AgentGeneration != agent.Generation ||
		expected.Namespace != (w.ObjectIdentity{Name: namespace.Name, UID: namespace.UID}) ||
		expected.Binding.AgentUID != agent.UID {
		return nil, empty, fmt.Errorf("common Sandbox expectation differs from the current Agent")
	}
	return state, expected, nil
}

func (r *AgentReconciler) observeReconciledSandbox(ctx context.Context, agent *api.Agent, prepared *helm.PreparedRelease) error {
	expected, err := prepared.ReconcileExpectation()
	if err != nil {
		return err
	}
	state := &corev1.ConfigMap{}
	if err := r.customReader().Get(ctx, client.ObjectKey{
		Namespace: r.OperatorNamespace, Name: w.PodExpectationName(expected.Namespace.Name, expected.Namespace.UID),
	}, state); err != nil {
		return err
	}
	current, err := w.ReadReconcileExpectation(state)
	agentSource, sourceErr := w.AgentSourceReference(agent)
	if err != nil || sourceErr != nil || current.Agent != expected.Agent || current.PlanHash != expected.PlanHash ||
		current.Revision != expected.Revision || current.AgentGeneration != agentSource.Generation {
		return fmt.Errorf("Sandbox desired expectation changed during reconciliation")
	}
	for _, desired := range current.Controls {
		actual := &unstructured.Unstructured{}
		actual.SetGroupVersionKind(desired.GroupVersionKind())
		if err := r.customReader().Get(ctx, client.ObjectKeyFromObject(&desired), actual); err != nil {
			return err
		}
		if !w.ReconciledObjectMatches(actual, &desired) {
			return fmt.Errorf("Sandbox enforced resource differs from the current desired controls")
		}
	}
	binding := current.Binding
	agent.Status.Sandbox = &api.SandboxStatus{
		TeamUID: binding.TeamUID, SandboxUID: binding.SandboxUID,
		AgentPolicyUID: binding.AgentPolicyUID, AppliedRevision: current.Revision,
	}
	setCondition(&agent.Status.Conditions, api.ConditionSandboxReady, metav1.ConditionTrue,
		"ReconciledSandboxReady", "Selected Sandbox configuration is applied through common reconciliation; physical failover fencing is not provided")
	for index := range agent.Status.Conditions {
		if agent.Status.Conditions[index].Type == api.ConditionSandboxReady {
			agent.Status.Conditions[index].ObservedGeneration = agent.Generation
		}
	}
	return nil
}

func (r *AgentReconciler) reconciledSandboxApplied(ctx context.Context, agent *api.Agent, prepared *helm.PreparedRelease) (bool, error) {
	expected, err := prepared.ReconcileExpectation()
	if err != nil {
		return false, err
	}
	for _, desired := range expected.Controls {
		current := &unstructured.Unstructured{}
		current.SetGroupVersionKind(desired.GroupVersionKind())
		err := r.customReader().Get(ctx, client.ObjectKeyFromObject(&desired), current)
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !w.ReconciledObjectMatches(current, &desired) {
			return false, nil
		}
	}
	for _, desired := range expected.Workloads {
		key := client.ObjectKey{Namespace: expected.Namespace.Name, Name: desired.Identity.Name}
		var object client.Object
		var replicas *int32
		var template corev1.PodTemplateSpec
		if desired.Kind == "StatefulSet" {
			current := &appsv1.StatefulSet{}
			err = r.customReader().Get(ctx, key, current)
			object, replicas, template = current, current.Spec.Replicas, current.Spec.Template
		} else {
			current := &appsv1.Deployment{}
			err = r.customReader().Get(ctx, key, current)
			object, replicas, template = current, current.Spec.Replicas, current.Spec.Template
		}
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !sandboxBuiltinOwned(object, agent) || !object.GetDeletionTimestamp().IsZero() {
			return false, fmt.Errorf("Sandbox workload ownership changed during reconciliation")
		}
		if ptr.Deref(replicas, 1) != desired.Replicas || !w.SamePodTemplate(template, desired.Template) {
			return false, nil
		}
	}
	return true, nil
}

func (r *AgentReconciler) stopReconciledReservation(ctx context.Context, agent *api.Agent, ticket sandboxcoordination.NativeTicket) error {
	intent := ticket.Reservation.Intent
	state := &corev1.ConfigMap{}
	if err := r.customReader().Get(ctx, client.ObjectKey{
		Namespace: r.OperatorNamespace, Name: w.PodExpectationName(intent.Namespace.Name, intent.Namespace.UID),
	}, state); err != nil {
		return err
	}
	expected, err := w.ReadReconcileExpectationForCleanup(state)
	if err != nil || expected.Agent != intent.Agent || expected.Namespace != intent.Namespace ||
		expected.Home != intent.Home || expected.Binding != intent.Binding {
		return fmt.Errorf("common Sandbox cleanup differs from its source reservation")
	}
	applyState := state.Annotations[sandboxApplyStateAnnotation]
	if applyState == sandboxApplyPending || applyState == sandboxApplyCancelled {
		if slices.ContainsFunc(expected.Workloads, func(workload w.RenderedWorkload) bool {
			return workload.Identity.UID != ""
		}) {
			return fmt.Errorf("unsubmitted Sandbox expectation has a recorded workload")
		}
		if applyState == sandboxApplyPending {
			// Compete with submission on the same resource version. A cancelled
			// snapshot cannot authorize an in-flight or subsequently retried apply.
			before := state.DeepCopy()
			state.Annotations[sandboxApplyStateAnnotation] = sandboxApplyCancelled
			if err := r.Patch(ctx, state, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
				return fmt.Errorf("cancel unsubmitted Sandbox apply: %w", err)
			}
			applyState = sandboxApplyCancelled
		}
	} else if applyState != "" && applyState != sandboxApplySubmitted {
		return fmt.Errorf("Sandbox apply checkpoint is invalid")
	}
	for _, desired := range expected.Workloads {
		key := client.ObjectKey{Namespace: expected.Namespace.Name, Name: desired.Identity.Name}
		var current client.Object
		stopped := false
		if desired.Kind == "StatefulSet" {
			workload := &appsv1.StatefulSet{}
			err = r.customReader().Get(ctx, key, workload)
			current = workload
			stopped = ptr.Deref(workload.Spec.Replicas, 1) == 0 &&
				workload.Status.ObservedGeneration >= workload.Generation && workload.Status.Replicas == 0
		} else {
			workload := &appsv1.Deployment{}
			err = r.customReader().Get(ctx, key, workload)
			current = workload
			stopped = ptr.Deref(workload.Spec.Replicas, 1) == 0 &&
				workload.Status.ObservedGeneration >= workload.Generation && workload.Status.Replicas == 0
		}
		if apierrors.IsNotFound(err) && desired.Kind == "StatefulSet" && applyState == sandboxApplyCancelled {
			continue
		}
		if apierrors.IsNotFound(err) && desired.Kind == "Deployment" {
			// A removed or never-created auxiliary has no HOME writer. Its
			// ReplicaSets must be gone; namespace Pods are checked below.
			var replicas appsv1.ReplicaSetList
			if err := r.customReader().List(ctx, &replicas, client.InNamespace(expected.Namespace.Name)); err != nil {
				return fmt.Errorf("observe removed Sandbox auxiliary children: %w", err)
			}
			for _, replica := range replicas.Items {
				if slices.ContainsFunc(replica.OwnerReferences, func(owner metav1.OwnerReference) bool {
					return owner.Kind == "Deployment" && (owner.Name == desired.Identity.Name ||
						(desired.Identity.UID != "" && owner.UID == desired.Identity.UID))
				}) {
					return fmt.Errorf("common Sandbox source reservation waits for removed auxiliary ReplicaSets")
				}
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("observe common controller shutdown: %w", err)
		}
		if !stopped || !sandboxBuiltinOwned(current, agent) ||
			(desired.Identity.UID != "" && desired.Identity.UID != current.GetUID()) {
			return fmt.Errorf("common Sandbox source reservation waits for acknowledged controller shutdown")
		}
	}
	quiescence := &KubernetesNativeQuiescence{APIReader: r.customReader(), Reconciled: true}
	// Cleanup has already verified the persisted expectation against the
	// reservation. A terminating Agent is deliberately ineligible for a new
	// authorization snapshot, so do not re-read its current execution intent.
	// The stored generation belongs to the exact lifetime being stopped.
	if err := quiescence.CheckQuiesced(ctx, agent, SandboxPrerequisites{
		AgentUID: agent.UID, AgentGeneration: expected.AgentGeneration, NamespaceUID: expected.Namespace.UID, Home: expected.Home,
	}); err != nil {
		return err
	}
	return r.SandboxNativeCoordination.ReleaseNative(ctx, ticket)
}

func (r *AgentReconciler) stopReconciledAuxiliaries(ctx context.Context, agent *api.Agent) error {
	var deployments appsv1.DeploymentList
	if err := r.customReader().List(ctx, &deployments, client.InNamespace(instanceNamespace(agent.Name))); err != nil {
		return err
	}
	var failures []error
	for index := range deployments.Items {
		object := &deployments.Items[index]
		// The common Custom terminal has the same Agent owner and UID label as
		// the protected workload, but deliberately does not carry the legacy
		// Helm annotations accepted by sandboxBuiltinOwned.
		owned := sandboxBuiltinOwned(object, agent)
		if !owned && agent.Spec.Agent.Type == api.AgentTypeCustom {
			owned = commonCustomTerminalOwned(object, agent)
		}
		if !owned {
			failures = append(failures, fmt.Errorf("Sandbox auxiliary workload ownership is unconfirmed"))
			continue
		}
		if ptr.Deref(object.Spec.Replicas, 0) != 0 {
			before := object.DeepCopy()
			object.Spec.Replicas = ptr.To[int32](0)
			failures = append(failures, r.Patch(ctx, object, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})))
		}
	}
	return errors.Join(failures...)
}

func (r *AgentReconciler) recordedReconciledSandbox(ctx context.Context, agent *api.Agent) (bool, error) {
	if !controllerutil.ContainsFinalizer(agent, sandbox.BootstrapFinalizer) ||
		controllerutil.ContainsFinalizer(agent, sandbox.PreparationFinalizer) {
		return false, nil
	}
	_, receipt, err := r.readSandboxBootstrap(ctx, agent)
	if err != nil {
		return false, err
	}
	return receipt.Reconciled, nil
}

func (r *AgentReconciler) holdReconciledPolicyChange(ctx context.Context, agent *api.Agent) error {
	var holdErr error
	if !controllerutil.ContainsFinalizer(agent, sandbox.HoldFinalizer) {
		before := agent.DeepCopy()
		controllerutil.AddFinalizer(agent, sandbox.HoldFinalizer)
		holdErr = r.Patch(ctx, agent, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
	}
	blockErr := r.beginAIGatewayPolicyReprojection(ctx, agent)
	auxiliaryErr := r.stopReconciledAuxiliaries(ctx, agent)
	stopErr := r.stopSandboxBuiltin(ctx, agent)
	agent.Status.Phase = api.PhaseProvisioning
	agent.Status.AccessURL = ""
	for _, kind := range []string{api.ConditionReady, api.ConditionIngressReady, api.ConditionSandboxReady} {
		setCondition(&agent.Status.Conditions, kind, metav1.ConditionFalse,
			"PolicyReprojection", "Applying current policy through common reconciliation")
	}
	return errors.Join(holdErr, blockErr, stopErr, auxiliaryErr, r.updateStatus(ctx, agent))
}

func (r *AgentReconciler) holdReconciledSandbox(ctx context.Context, agent *api.Agent) (ctrl.Result, error) {
	var failures []error
	if !controllerutil.ContainsFinalizer(agent, sandbox.HoldFinalizer) {
		before := agent.DeepCopy()
		controllerutil.AddFinalizer(agent, sandbox.HoldFinalizer)
		failures = append(failures, r.Patch(ctx, agent, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})))
	}
	failures = append(failures, r.stopSandboxBuiltin(ctx, agent))
	if controllerutil.ContainsFinalizer(agent, sandbox.BootstrapFinalizer) {
		failures = append(failures, r.stopReconciledAuxiliaries(ctx, agent))
	}
	agent.Status.Phase, agent.Status.AccessURL = api.PhasePending, ""
	for _, kind := range []string{api.ConditionReady, api.ConditionSandboxReady, api.ConditionIngressReady, api.ConditionAgentConfigReady} {
		setCondition(&agent.Status.Conditions, kind, metav1.ConditionFalse,
			"SandboxHeld", "Current Sandbox authorization or prerequisites are unavailable")
	}
	failures = append(failures, r.updateStatus(ctx, agent))
	return ctrl.Result{RequeueAfter: 15 * time.Second}, errors.Join(failures...)
}

func (r *AgentReconciler) stopReconciledSandboxForSuspension(ctx context.Context, agent *api.Agent) error {
	return r.stopReconciledSandbox(ctx, agent, true)
}

func (r *AgentReconciler) stopReconciledSandboxForMigration(ctx context.Context, agent *api.Agent) error {
	// Migration retains terminal Pods until their acknowledgement is durable.
	// Releasing the reservation here would wait for those same Pods to vanish.
	return r.stopReconciledSandbox(ctx, agent, false)
}

func (r *AgentReconciler) stopReconciledSandbox(ctx context.Context, agent *api.Agent, finishReservation bool) error {
	if !finishReservation && !agent.DeletionTimestamp.IsZero() {
		if err := r.checkMigrationIntent(ctx, agent); err != nil {
			return err
		}
	} else {
		if err := r.checkRegistryIntent(ctx, agent); err != nil {
			return err
		}
	}
	var blockErr error
	if r.AIGateway.configured() {
		gateway, err := r.aiGatewayClient(ctx)
		blockErr = err
		if err == nil {
			blocked, err := r.blockAIGatewayKeyIfPresent(ctx, gateway, agent, aiGatewayReasonSuspended)
			blockErr = err
			if err == nil && !blocked && aiGatewayKeyWasIssued(agent) {
				blockErr = fmt.Errorf("the previously issued AI Gateway key is missing during suspension")
			}
			if blockErr == nil && agent.Status.AIGateway != nil {
				agent.Status.AIGateway.Blocked = blocked
			}
		}
	} else if aiGatewayKeyWasIssued(agent) {
		blockErr = fmt.Errorf("AI Gateway configuration is required to block the existing key during suspension")
	}
	// Independent shutdown must precede chart/bootstrap reads and must still
	// run when Gateway revocation or one component's stop cannot complete.
	auxiliaryErr := r.stopReconciledAuxiliaries(ctx, agent)
	stopErr := r.stopSandboxBuiltinWorkload(ctx, agent)
	if stopErr == nil && finishReservation {
		stopErr = r.stopNativeSandboxReservation(ctx, agent)
	}
	agent.Status.AccessURL = ""
	if err := errors.Join(blockErr, auxiliaryErr, stopErr); err != nil {
		agent.Status.Phase = api.PhasePending
		for _, kind := range []string{api.ConditionReady, api.ConditionPodReady, api.ConditionIngressReady} {
			setCondition(&agent.Status.Conditions, kind, metav1.ConditionFalse,
				"SuspensionPending", "Suspension requested; waiting for shutdown and access revocation")
		}
		return errors.Join(err, r.updateStatus(ctx, agent))
	}
	return nil
}

func (r *AgentReconciler) releaseReconciledHold(ctx context.Context, agent *api.Agent) error {
	if agent.Spec.Suspended || !slices.Contains(agent.Finalizers, sandbox.HoldFinalizer) {
		return nil
	}
	before := agent.DeepCopy()
	controllerutil.RemoveFinalizer(agent, sandbox.HoldFinalizer)
	return r.Patch(ctx, agent, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
}
