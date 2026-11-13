// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxcoordination

import (
	"context"
	"errors"
	"fmt"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ConfigurationChangeFencedCondition is written only by RecoverConfigurationChange.
// It advances the source resourceVersion without changing its writable
// configuration, so a delayed full-object Update pinned to the old version can
// no longer commit.
const ConfigurationChangeFencedCondition = "ConfigurationChangeFenced"

// RecoverConfigurationChange safely releases a recorded Changing configuration
// update whose submission outcome is unknown. It never submits the recorded
// update. If no terminal outcome is visible, it writes one conditional status
// fence using the original UID/resourceVersion and then observes the source
// again. The fence and the old Update can therefore never both commit.
//
// Callers must retain and supply the exact original before and after images.
// A failed, conflicted, or lost fence response is not enough to release the
// reservation; release happens only after a later authoritative observation.
func (s Store) RecoverConfigurationChange(ctx context.Context, before, after client.Object) (ChangeTicket, *ChangeOutcome, error) {
	change, err := newConfigurationChange(before, after, false)
	if err != nil {
		return ChangeTicket{}, nil, err
	}
	ticket, found, err := s.configurationTicket(ctx, change)
	if err != nil {
		return ChangeTicket{}, nil, err
	}
	if !found || ticket.Change.Phase != Changing {
		return ticket, nil, ErrConflict
	}
	if err := s.CheckChange(ctx, ticket); err != nil {
		return ticket, nil, err
	}

	// An existing committed write or any version that already excludes the old
	// CAS request needs no fence. The exact-image adapter accepts only the exact
	// desired content or unchanged writable content at a newer version.
	observer, err := NewKubernetesChangeAdapter(nil, s.APIReader, before, after)
	if err != nil {
		return ticket, nil, err
	}
	if _, observeErr := observer.ObserveChange(ctx, ticket); observeErr == nil {
		outcome, err := s.observeAndReleaseChange(ctx, ticket, observer)
		return ticket, outcome, err
	}

	// Only the exact original object can receive the fence. In particular, do
	// not rebase after a concurrent status refresh: the recovery must preserve
	// that newer status rather than overwrite it with an older complete image.
	current, err := change.readBefore(ctx, s.APIReader)
	if err != nil {
		return ticket, nil, err
	}
	if err := setConfigurationChangeFence(current.object, ticket); err != nil {
		return ticket, nil, err
	}
	if s.Client == nil {
		return ticket, nil, fmt.Errorf("%w: a configuration status writer is required", ErrUnavailable)
	}
	if err := s.CheckChange(ctx, ticket); err != nil {
		return ticket, nil, err
	}
	if err := ctx.Err(); err != nil {
		return ticket, nil, err
	}
	fenceErr := s.Client.Status().Update(ctx, current.object)
	if fenceErr != nil {
		fenceErr = classifyMutationFailure(SubmissionStageStatusUpdate, fenceErr)
	}

	// A response error may have happened after the API server accepted the
	// fence. Re-read through the recovery adapter in all cases; it alone proves
	// the old conditional Update is excluded. Preserve a deterministic fence
	// failure when the reread cannot establish that proof.
	if _, observeErr := observer.ObserveChange(ctx, ticket); observeErr == nil {
		outcome, err := s.observeAndReleaseChange(ctx, ticket, observer)
		return ticket, outcome, err
	} else if fenceErr != nil {
		return ticket, nil, fenceErr
	} else if errors.Is(observeErr, ErrConflict) {
		return ticket, nil, observeErr
	}
	return ticket, nil, fmt.Errorf("%w: configuration status fence was not confirmed by an authoritative observation", ErrUnavailable)
}

func setConfigurationChangeFence(object client.Object, ticket ChangeTicket) error {
	condition := metav1.Condition{
		Type:               ConfigurationChangeFencedCondition,
		Status:             metav1.ConditionTrue,
		Reason:             ConfigurationChangeFencedCondition,
		Message:            "Coordinated configuration update fenced: " + ticket.Change.ID,
		LastTransitionTime: metav1.Now(),
	}
	switch object := object.(type) {
	case *api.Team:
		apimeta.SetStatusCondition(&object.Status.Conditions, condition)
	case *api.Sandbox:
		apimeta.SetStatusCondition(&object.Status.Conditions, condition)
	case *api.AgentPolicy:
		apimeta.SetStatusCondition(&object.Status.Conditions, condition)
	default:
		return fmt.Errorf("%w: only typed Team, Sandbox and AgentPolicy configurations are supported", ErrUnavailable)
	}
	return nil
}
