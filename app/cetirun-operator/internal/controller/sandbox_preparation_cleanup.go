// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxworkload"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	strictjson "sigs.k8s.io/json"
)

const sandboxPreparationReceiptKey = "preparation.json"

type sandboxPreparationReceipt struct {
	Version         string               `json:"version"`
	AgentName       string               `json:"agentName"`
	AgentUID        types.UID            `json:"agentUID"`
	NativeExecution bool                 `json:"nativeExecution,omitempty"`
	Prerequisites   SandboxPrerequisites `json:"prerequisites"`
}

func (r *AgentReconciler) sandboxPreparationKey(agent *api.Agent) client.ObjectKey {
	sum := sha256.Sum256([]byte(agent.UID))
	return client.ObjectKey{
		Namespace: r.sandboxGrantJournal().Namespace,
		Name:      "sandbox-preparation-" + hex.EncodeToString(sum[:20]),
	}
}

func (r *AgentReconciler) readSandboxPreparationReceipt(ctx context.Context, agent *api.Agent) (*corev1.ConfigMap, *sandboxPreparationReceipt, error) {
	object := &corev1.ConfigMap{}
	if err := r.customReader().Get(ctx, r.sandboxPreparationKey(agent), object); err != nil {
		return nil, nil, err
	}
	var receipt sandboxPreparationReceipt
	raw := object.Data[sandboxPreparationReceiptKey]
	problems, err := strictjson.UnmarshalStrict([]byte(raw), &receipt)
	owner := []metav1.OwnerReference{sandboxworkload.AgentOwner(agent.Name, agent.UID)}
	if err != nil || len(problems) != 0 || len(raw) > 64*1024 || len(object.Data) != 1 || len(object.BinaryData) != 0 ||
		object.UID == "" || object.ResourceVersion == "" ||
		!equality.Semantic.DeepEqual(object.OwnerReferences, owner) ||
		object.Annotations[sandboxworkload.AgentUIDAnnotation] != string(agent.UID) ||
		!((receipt.Version == "v1" && receipt.Prerequisites.Placement == nil) ||
			(receipt.Version == "v2" && receipt.Prerequisites.Placement != nil)) ||
		receipt.AgentName != agent.Name || receipt.AgentUID != agent.UID ||
		receipt.Prerequisites.AgentUID != agent.UID || receipt.Prerequisites.AgentGeneration < 1 ||
		receipt.Prerequisites.NamespaceUID == "" || receipt.Prerequisites.Home.Name == "" || receipt.Prerequisites.Home.UID == "" {
		return nil, nil, fmt.Errorf("Sandbox preparation ownership receipt is invalid")
	}
	if err := validateSandboxPlacementTuple(agent, receipt.Prerequisites); err != nil {
		return nil, nil, err
	}
	if inputs := receipt.Prerequisites; inputs.SkillsMode != "" {
		if err := inputs.SkillsMode.ValidateIdentity(inputs.Skills); err != nil {
			return nil, nil, err
		}
		if len(inputs.PublicationSources) != 0 || inputs.Placement != nil {
			return nil, nil, fmt.Errorf("empty skills preparation cannot contain a publication or non-native placement")
		}
	}
	return object, &receipt, nil
}

func (r *AgentReconciler) recordSandboxPreparationReceipt(ctx context.Context, attempt *sandboxPreparationAttempt) error {
	agent := attempt.agent
	desired := sandboxPreparationReceipt{Version: "v1", AgentName: agent.Name, AgentUID: agent.UID,
		NativeExecution: r.SandboxPreparation.NativeExecution, Prerequisites: attempt.prerequisites}
	if desired.Prerequisites.Placement != nil {
		desired.Version = "v2"
	}
	raw, err := json.Marshal(desired)
	if err != nil {
		return err
	}
	if len(raw) > 64*1024 {
		return fmt.Errorf("Sandbox preparation ownership receipt exceeds its bound")
	}
	object, receipt, err := r.readSandboxPreparationReceipt(ctx, agent)
	if apierrors.IsNotFound(err) {
		if controllerutil.ContainsFinalizer(agent, sandbox.PreparationFinalizer) {
			return fmt.Errorf("Sandbox preparation receipt is missing; recovery is required")
		}
		key := r.sandboxPreparationKey(agent)
		return r.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: key.Name, Namespace: key.Namespace,
				OwnerReferences: []metav1.OwnerReference{sandboxworkload.AgentOwner(agent.Name, agent.UID)},
				Finalizers:      []string{sandbox.PreparationFinalizer},
				Annotations:     map[string]string{sandboxworkload.AgentUIDAnnotation: string(agent.UID)},
			},
			Data: map[string]string{sandboxPreparationReceiptKey: string(raw)},
		})
	}
	if err != nil {
		return err
	}
	if !object.DeletionTimestamp.IsZero() || !controllerutil.ContainsFinalizer(object, sandbox.PreparationFinalizer) {
		return fmt.Errorf("Sandbox preparation receipt is no longer protected for writes")
	}
	recordedNative := receipt.NativeExecution || object.Annotations[nativeStartAnnotation] != ""
	if recordedNative != desired.NativeExecution {
		return fmt.Errorf("Sandbox preparation cannot change its recorded execution mode")
	}
	if receipt.Prerequisites.NamespaceUID != desired.Prerequisites.NamespaceUID || receipt.Prerequisites.Home != desired.Prerequisites.Home {
		return fmt.Errorf("%w: Sandbox namespace or HOME replacement requires a separate handoff", sandboxworkload.ErrStorageTransitionRequired)
	}
	if !equality.Semantic.DeepEqual(receipt.Prerequisites.Placement, desired.Prerequisites.Placement) {
		return fmt.Errorf("Sandbox lifetime placement cannot be replaced or removed during preparation")
	}
	if object.Data[sandboxPreparationReceiptKey] == string(raw) {
		return nil
	}
	base := object.DeepCopy()
	object.Data[sandboxPreparationReceiptKey] = string(raw)
	return r.Patch(ctx, object, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
}

func (r *AgentReconciler) checkSandboxPreparationCleanup(ctx context.Context, agent *api.Agent) error {
	if err := r.refreshSandboxCleanupAgent(ctx, agent); err != nil {
		return err
	}
	if !r.sandboxPreparationStateNeeded(agent) {
		return nil
	}
	object, receipt, err := r.readSandboxPreparationReceipt(ctx, agent)
	if apierrors.IsNotFound(err) && !controllerutil.ContainsFinalizer(agent, sandbox.PreparationFinalizer) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("Sandbox cleanup ownership receipt is unavailable: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Cleanup uses the durable prior tuple, not current Team/policy lookup or
	// name-based rediscovery. Providers must support this after configuration
	// deletion, suspension, reauthorization and controller restart.
	native := receipt.NativeExecution
	// Started native receipts from before execution-mode recording retain their
	// controller-written marker. Current enablement is not cleanup authority.
	if started := object.Annotations[nativeStartAnnotation]; started != "" {
		if !sandboxworkload.ValidInputDigest(started) {
			return fmt.Errorf("Sandbox cleanup has an invalid native start marker")
		}
		native = true
	}
	inputs := copySandboxPrerequisites(receipt.Prerequisites)
	if native {
		if inputs.Placement != nil || inputs.AgentGeneration > agent.Generation {
			return fmt.Errorf("native Sandbox cleanup requires an unplaced historical native lifetime")
		}
		err = (&KubernetesNativeQuiescence{APIReader: r.customReader()}).CheckQuiesced(ctx, agent.DeepCopy(), inputs)
	} else {
		if r.SandboxPreparation == nil || r.SandboxPreparation.Prerequisites == nil ||
			r.SandboxPreparation.NativeExecution {
			return fmt.Errorf("%w: Sandbox cleanup requires the recorded non-native fence provider", sandboxpolicy.ErrUnavailable)
		}
		err = r.SandboxPreparation.Prerequisites.CheckQuiesced(ctx, agent.DeepCopy(), inputs)
	}
	if err != nil {
		return fmt.Errorf("Sandbox deletion fence is unconfirmed: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	namespace := &corev1.Namespace{}
	if err := r.customReader().Get(ctx, client.ObjectKey{Name: instanceNamespace(agent.Name)}, namespace); err != nil {
		return client.IgnoreNotFound(err)
	}
	if namespace.UID != receipt.Prerequisites.NamespaceUID || !sandboxBuiltinOwned(namespace, agent) {
		return fmt.Errorf("Sandbox cleanup namespace differs from the durable ownership receipt")
	}
	var pods corev1.PodList
	if err := r.customReader().List(ctx, &pods, client.InNamespace(namespace.Name)); err != nil {
		return err
	}
	if len(pods.Items) != 0 {
		return fmt.Errorf("Sandbox deletion waits for old Pods to terminate before namespace or storage removal")
	}
	return ctx.Err()
}

// Call only after grant revocation, fencing and namespace absence. Removing the
// Agent marker first makes retries safe if GC removes an empty state object
// between finalizer release and the following read.
func (r *AgentReconciler) finishSandboxPreparation(ctx context.Context, agent *api.Agent) error {
	if err := r.refreshSandboxCleanupAgent(ctx, agent); err != nil {
		return err
	}
	if !r.sandboxPreparationStateNeeded(agent) {
		return nil
	}
	object, receipt, err := r.readSandboxPreparationReceipt(ctx, agent)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if apierrors.IsNotFound(err) && controllerutil.ContainsFinalizer(agent, sandbox.PreparationFinalizer) {
		return fmt.Errorf("Sandbox preparation receipt disappeared before cleanup completion")
	}
	if object != nil {
		namespace := &corev1.Namespace{}
		if err := r.customReader().Get(ctx, client.ObjectKey{Name: instanceNamespace(agent.Name)}, namespace); err == nil {
			return fmt.Errorf("Sandbox namespace must be absent before releasing preparation state")
		} else if !apierrors.IsNotFound(err) {
			return err
		}
		if err := r.releaseNativeLifetimeState(ctx, agent, receipt.Prerequisites.NamespaceUID); err != nil {
			return err
		}
	}
	if controllerutil.ContainsFinalizer(agent, sandbox.PreparationFinalizer) {
		base := agent.DeepCopy()
		controllerutil.RemoveFinalizer(agent, sandbox.PreparationFinalizer)
		if err := r.Patch(ctx, agent, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
			return err
		}
	}
	if err := r.sandboxGrantJournal().Delete(ctx, agent.Name, agent.UID); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if object == nil {
		return nil
	}
	if controllerutil.ContainsFinalizer(object, sandbox.PreparationFinalizer) {
		base := object.DeepCopy()
		controllerutil.RemoveFinalizer(object, sandbox.PreparationFinalizer)
		if err := r.Patch(ctx, object, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
			return client.IgnoreNotFound(err)
		}
	}
	return client.IgnoreNotFound(r.Delete(ctx, object, client.Preconditions{UID: &object.UID, ResourceVersion: &object.ResourceVersion}))
}

func (r *AgentReconciler) refreshSandboxCleanupAgent(ctx context.Context, agent *api.Agent) error {
	current := &api.Agent{}
	if err := r.customReader().Get(ctx, client.ObjectKeyFromObject(agent), current); err != nil {
		return err
	}
	if current.UID != agent.UID || current.DeletionTimestamp.IsZero() {
		return fmt.Errorf("Sandbox cleanup requires the same terminating Agent UID")
	}
	*agent = *current
	return nil
}
