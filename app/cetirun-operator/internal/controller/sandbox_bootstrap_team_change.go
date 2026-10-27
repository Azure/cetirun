// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// refreshSandboxSkillsForTeamChange replaces only the read-only, Team-scoped
// skills projection after Team change has fenced and stopped the source. HOME
// remains recorded at Objects[3] throughout the handoff.
//
// The receipt remains authoritative until the old claim and PV are absent. A
// retry can therefore distinguish this recorded Team handoff from unexpected
// resource loss and safely resume after a controller restart.
func (r *AgentReconciler) refreshSandboxSkillsForTeamChange(ctx context.Context, agent *api.Agent) error {
	if agent == nil {
		return fmt.Errorf("Team change skills handoff requires an Agent")
	}
	// Bootstrap is optional. Without its Team-scoped skills publication there
	// is no storage handoff to perform, so the fenced Team change can continue.
	if r.SandboxPreparation == nil || r.SandboxPreparation.bootstrap == nil {
		return nil
	}
	state := agent.Status.TeamChange
	if state == nil || !state.Fenced || !state.Stopped || state.TargetTeam == "" ||
		agent.Spec.Team != state.TargetTeam || agent.Spec.SandboxRef != state.TargetSandbox {
		return fmt.Errorf("Team change skills handoff requires the committed, stopped target binding")
	}
	object, receipt, err := r.readSandboxBootstrap(ctx, agent)
	if err != nil {
		return fmt.Errorf("read Sandbox bootstrap for Team change: %w", err)
	}
	if !receipt.Reconciled || receipt.Skills == nil || len(receipt.Objects) != 7 {
		return nil
	}
	if receipt.Objects[3].Kind != "PersistentVolumeClaim" || receipt.Objects[3].UID == "" {
		return fmt.Errorf("Team change skills handoff requires a recorded retained HOME")
	}
	team := &api.Team{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Name: agent.Spec.Team}, team); err != nil {
		return fmt.Errorf("read target Team skills publication: %w", err)
	}
	publication, err := r.readSandboxSkillsPublication(ctx, r.SandboxPreparation.bootstrap.SkillsCatalogConfigMapName, team)
	if err != nil {
		return err
	}
	if sameSandboxSkillsPublication(*publication, *receipt.Skills) {
		return nil
	}

	desired := r.sandboxBootstrapObjects(agent, object, receipt)
	if err := r.deleteSandboxSkillsClaimForTeamChange(ctx, desired[5], receipt.Objects[5]); err != nil {
		return err
	}
	if err := r.deleteSandboxSkillsVolumeForTeamChange(ctx, desired[6], receipt.Objects[6]); err != nil {
		return err
	}
	// Both identities are gone. Only now record the selected target
	// publication, so ordinary bootstrap can recreate its exact static PV/PVC.
	receipt.Skills = publication
	receipt.Objects[5].UID, receipt.Objects[6].UID = "", ""
	return r.saveSandboxBootstrap(ctx, object, receipt)
}

func (r *AgentReconciler) deleteSandboxSkillsClaimForTeamChange(ctx context.Context, desired client.Object, recorded sandboxBootstrapObject) error {
	claim := &corev1.PersistentVolumeClaim{}
	key := client.ObjectKeyFromObject(desired)
	if err := r.customReader().Get(ctx, key, claim); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err := validateSandboxBootstrapObject(claim, desired, recorded.UID, true); err != nil {
		return err
	}
	if err := r.removeSandboxBootstrapFinalizer(ctx, claim); err != nil {
		return err
	}
	if err := r.Delete(ctx, claim, client.Preconditions{UID: &claim.UID, ResourceVersion: &claim.ResourceVersion}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return fmt.Errorf("Team change waits for the recorded skills claim to be removed")
}

func (r *AgentReconciler) deleteSandboxSkillsVolumeForTeamChange(ctx context.Context, desired client.Object, recorded sandboxBootstrapObject) error {
	volume := &corev1.PersistentVolume{}
	key := client.ObjectKeyFromObject(desired)
	if err := r.customReader().Get(ctx, key, volume); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err := validateSandboxBootstrapObject(volume, desired, recorded.UID, true); err != nil {
		return err
	}
	if volume.DeletionTimestamp.IsZero() {
		if err := r.Delete(ctx, volume, client.Preconditions{UID: &volume.UID, ResourceVersion: &volume.ResourceVersion}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	if err := r.removeSandboxBootstrapFinalizer(ctx, volume); err != nil {
		return err
	}
	return fmt.Errorf("Team change waits for the recorded skills volume to be removed")
}
