// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
	"github.com/Azure/CetiRun/app/internal/chataccess"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// configureReconciledCustomRuntime keeps common Custom access on the typed
// Sandbox contract. Legacy Custom plans default desktop access, which does not
// exist in a reconciled Sandbox and must not create an unreachable route.
func (r *AgentReconciler) configureReconciledCustomRuntime(ctx context.Context, instance *api.Agent, prepared *helm.PreparedRelease) error {
	access, err := reconciledCustomAccess(instance, prepared)
	if err != nil {
		return err
	}
	resolution, err := r.resolvePolicy(ctx, instance)
	if err != nil {
		return err
	}
	ingressDisabled := resolution.policy.Spec.Network.Ingress.Mode == api.AgentPolicyIngressDisabled

	chatStatus, chatReason := metav1.ConditionFalse, "ChatAccessDisabled"
	if !ingressDisabled && r.ChatAccess.Enabled && len(r.ChatAccess.Callers) != 0 && instance.Spec.Agent.Port != nil && !instance.Spec.Agent.WebUI {
		chatReason = "ChatContractUnsupported"
		if prepared.SupportsPortalChat() {
			chatStatus, chatReason = metav1.ConditionTrue, "ChatAccessConfigured"
		}
	}
	if err := r.reconcileChatRoute(ctx, instance, chatStatus == metav1.ConditionTrue); err != nil {
		chatStatus, chatReason = metav1.ConditionFalse, "ChatRouteUnavailable"
	}
	setCondition(&instance.Status.Conditions, chataccess.Condition, chatStatus, chatReason,
		"Reports private Chat configuration, not model or network qualification")

	if !access.desktop && !access.terminal && !instance.Spec.Agent.WebUI {
		instance.Status.AccessURL = ""
		if ingressDisabled {
			if err := r.withdrawKubernetesSandboxRoute(ctx, instance); err != nil {
				return err
			}
			setCondition(&instance.Status.Conditions, api.ConditionIngressReady, metav1.ConditionTrue,
				"IngressDisabled", "Sandbox ingress is disabled")
			setCustomServiceConditions(instance, access, metav1.ConditionFalse, "IngressDisabled")
			return nil
		}
		if err := r.removeCustomPlatformRoute(ctx, instance); err != nil {
			return err
		}
		setCondition(&instance.Status.Conditions, api.ConditionIngressReady, metav1.ConditionFalse,
			"PlatformAccessDisabled", "Platform desktop, terminal and Agent Web UI are disabled")
		setCustomServiceConditions(instance, access, metav1.ConditionFalse, "PlatformAccessDisabled")
		return nil
	}

	if _, err := r.ensureVirtualService(ctx, instance, access); err != nil {
		return err
	}
	if ingress := meta.FindStatusCondition(instance.Status.Conditions, api.ConditionIngressReady); ingress != nil && ingress.Reason == "IngressDisabled" {
		instance.Status.AccessURL = ""
		setCustomServiceConditions(instance, access, metav1.ConditionFalse, "IngressDisabled")
		return nil
	}
	instance.Status.AccessURL = ""
	reason, message := "PlatformDesktopConfigured", "Approved platform desktop route configured"
	if access.terminal && !access.desktop {
		reason, message = "PlatformTerminalConfigured", "Approved platform terminal route configured"
	}
	if instance.Spec.Agent.WebUI {
		instance.Status.AccessURL = accessURLPathBased(instance.Name, r.DomainSuffix)
		reason, message = "PlatformWebUIConfigured", "Approved Agent Web UI route configured"
		if access.desktop || access.terminal {
			reason, message = "PlatformServicesConfigured", "Approved platform services and Agent Web UI routes configured"
		}
	}
	setCondition(&instance.Status.Conditions, api.ConditionIngressReady, metav1.ConditionTrue, reason, message)
	setCustomServiceConditions(instance, access, metav1.ConditionTrue, reason)
	if access.terminal {
		ready, err := r.customTerminalReady(ctx, instance)
		if err != nil {
			return err
		}
		if !ready {
			setCondition(&instance.Status.Conditions, api.ConditionTerminalConfigured, metav1.ConditionFalse,
				"PlatformTerminalPending", "Waiting for the owned terminal Deployment to become ready")
			return fmt.Errorf("common Custom terminal is not ready")
		}
	}
	return nil
}

func reconciledCustomAccess(instance *api.Agent, prepared *helm.PreparedRelease) (customAccess, error) {
	if prepared == nil {
		return customAccess{}, fmt.Errorf("common Custom release is unavailable")
	}
	runtime, ok := prepared.Values["runtime"].(map[string]any)
	if !ok {
		return customAccess{}, fmt.Errorf("common Custom runtime contract is missing")
	}
	contract, ok := runtime["agent"].(map[string]any)
	if !ok || contract["type"] != "custom" {
		return customAccess{}, fmt.Errorf("common Custom runtime contract is invalid")
	}
	webUI, ok := contract["webUI"].(bool)
	if !ok || webUI != instance.Spec.Agent.WebUI {
		return customAccess{}, fmt.Errorf("common Custom Web UI differs from its typed Harness contract")
	}
	desktop := false
	if settings, ok := prepared.Values["desktop"].(map[string]any); ok {
		desktop, _ = settings["enabled"].(bool)
	}
	terminal, terminalPort := false, int32(0)
	if sandbox, ok := prepared.Values["sandboxWorkload"].(map[string]any); ok {
		if settings, ok := sandbox["terminal"].(map[string]any); ok {
			terminal, _ = settings["enabled"].(bool)
			terminalPort, _ = sandboxTerminalNumber(settings["port"])
		}
	}
	if terminal && terminalPort == 0 {
		return customAccess{}, fmt.Errorf("common Custom terminal contract has no port")
	}
	return customAccess{desktop: desktop, terminal: terminal, terminalPort: uint32(terminalPort)}, nil
}
