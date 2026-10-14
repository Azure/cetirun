// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxadmission

import (
	"context"
	"fmt"
	"reflect"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandbox"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxplatform"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// Snapshot state belongs to one request. Cleanup and unrelated legacy requests
// never consult the platform source; policy compatibility checks load it lazily.
func (h *Handler) withPlatformSnapshot(ctx context.Context, request admission.Request,
	handle func(*Handler, context.Context, admission.Request) admission.Response,
) admission.Response {
	current := *h
	current.platformSnapshot = nil
	ctx, cancel := context.WithTimeout(ctx, h.requestTimeout)
	defer cancel()
	response := handle(&current, ctx, request)
	if !response.Allowed || current.platformSnapshot == nil {
		return response
	}
	if err := current.verifyPlatformSnapshot(ctx); err != nil {
		return dependencyError(ctx, err)
	}
	return response
}

func (h *Handler) platformRegistrations(ctx context.Context) (map[api.SandboxType]sandbox.Registration, error) {
	if h.platformSource == nil {
		return h.registrations, ctx.Err()
	}
	if h.platformSnapshot == nil {
		snapshot, err := h.platformSource.Snapshot(ctx)
		if err != nil {
			return nil, err
		}
		if err := sandboxplatform.CheckFreshness(snapshot.ObservedAt, time.Now(), 0); err != nil {
			return nil, err
		}
		h.platformSnapshot = &snapshot
	}
	return h.platformSnapshot.Registrations, ctx.Err()
}

func (h *Handler) checkTeamExecutionProfiles(ctx context.Context, spec api.TeamSpec, resources sandbox.Resources, registrations map[api.SandboxType]sandbox.Registration) error {
	for _, name := range spec.SandboxRefs {
		box := resources.Sandboxes[name]
		if box == nil {
			return fmt.Errorf("referenced Sandbox %q is missing", name)
		}
		selected, errs := sandbox.NormalizeSandbox(box.Spec)
		if len(errs) != 0 {
			return errs.ToAggregate()
		}
		if err := sandbox.CheckExecutionProfile(registrations[selected.Type].Capabilities, h.config.NativeExecution); err != nil {
			return err
		}
		if err := h.checkNativeNetworkBinding(ctx, selected.Type, resources.AgentPolicies[spec.AgentPolicyRef],
			registrations[selected.Type].Capabilities); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) checkNativeNetworkBinding(ctx context.Context, kind api.SandboxType, policy *api.AgentPolicy, capabilities sandbox.Capabilities) error {
	if capabilities.Profile != sandbox.ProfileNativeSingleStart {
		return nil
	}
	if policy == nil {
		return fmt.Errorf("native network validation requires the selected AgentPolicy")
	}
	if len(policy.Spec.Network.BlockedCIDRs) == 0 && len(policy.Spec.Network.BlockedPorts) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if h.platformSnapshot == nil {
		return fmt.Errorf("%w: native network restrictions require a current platform topology", sandboxpolicy.ErrUnavailable)
	}
	platform, ok := h.platformSnapshot.Platforms[kind]
	if !ok {
		return fmt.Errorf("%w: the selected native network platform is missing", sandboxpolicy.ErrUnavailable)
	}
	if err := sandboxpolicy.ValidatePolicyConfiguration(policy, capabilities, platform); err != nil {
		return err
	}
	return ctx.Err()
}

func (h *Handler) verifyPlatformSnapshot(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	previous := *h.platformSnapshot
	if err := sandboxplatform.CheckFreshness(previous.ObservedAt, time.Now(), 0); err != nil {
		return err
	}
	current, err := h.platformSource.Snapshot(ctx)
	if err != nil {
		return err
	}
	if err := sandboxplatform.CheckFreshness(current.ObservedAt, time.Now(), 0); err != nil {
		return err
	}
	previous.ObservedAt, current.ObservedAt = time.Time{}, time.Time{}
	if !reflect.DeepEqual(previous, current) {
		return fmt.Errorf("%w: Sandbox admission platform changed during validation", sandboxplatform.ErrSourceChanged)
	}
	return ctx.Err()
}
