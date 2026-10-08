// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/agents"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
	"github.com/Azure/CetiRun/app/internal/registryauth"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

func runtimeRequest(instance *api.Agent, registry string) map[string]any {
	agent := instance.Spec.Agent
	request := map[string]any{
		"type": string(agent.Type), "registry": registry, "uiPrefix": "/ui/" + instance.Name,
		"tailscaleEnabled": instance.Spec.Tailscale != nil,
	}
	if agent.Image != nil {
		request["image"] = *agent.Image
	}
	if agent.Username != nil {
		request["username"] = *agent.Username
	}
	if agent.Port != nil {
		request["port"] = *agent.Port
	}
	if agent.RegistrySecretRef != nil {
		name, _ := registryauth.PullSecretName(instance.UID)
		request["pullSecretName"] = name
	}
	if agent.WebUI {
		request["webUI"] = agent.WebUI
	}
	return request
}

func (r *AgentReconciler) rejectAgentConfig(ctx context.Context, instance *api.Agent, err error) (ctrl.Result, error) {
	setCondition(&instance.Status.Conditions, api.ConditionAgentConfigReady,
		metav1.ConditionFalse, "AgentConfigInvalid", err.Error())
	return r.failWith(ctx, instance, "AgentConfigInvalid", err.Error())
}

// User and deployment overrides cannot inject a prepared plan or replace its
// primary image/port. Unrelated deployment settings keep their existing scope.
func validateRuntimeOverrides(values map[string]any, agentType string) error {
	for _, key := range []string{"runtime", "agentRequest", "imagePullSecrets", "customBindings"} {
		if _, ok := values[key]; ok {
			return fmt.Errorf("helm.values.%s is reserved for the platform", key)
		}
	}
	// Gateway routing is a controller projection of Policy. Permitting a
	// Helm value to replace any part of it would let an Agent bypass the
	// virtual key, select an unapproved provider/model, or send traffic around
	// the Gateway. Keep these paths reserved even while the Gateway is disabled
	// so a value cannot become an escape hatch after it is enabled.
	if _, ok := values["aiGateway"]; ok {
		return fmt.Errorf("helm.values.aiGateway is reserved for the platform")
	}
	if secrets, ok := values["secrets"].(map[string]any); ok {
		for _, field := range []string{"provider", "authMode", "apiBase", "apiKey", "model"} {
			if _, present := secrets[field]; present {
				return fmt.Errorf("helm.values.secrets.%s is reserved for AI Gateway routing", field)
			}
		}
	}
	if annotations, ok := values["podAnnotations"].(map[string]any); ok {
		for _, field := range []string{"cetirun.microsoft.com/registry-binding-only", aiGatewayKeyIDPodAnnotation} {
			if _, present := annotations[field]; present {
				return fmt.Errorf("platform rollout annotations are platform-owned")
			}
		}
	}
	if _, ok := values["image"]; ok {
		return fmt.Errorf("helm.values.image must use spec.agent.image")
	}
	if extraEnv, ok := values["extraEnv"].([]any); ok {
		for _, item := range extraEnv {
			entry, _ := item.(map[string]any)
			if entry["name"] == "CETIRUN_AGENT_BROKER_URL" {
				return fmt.Errorf("helm.values.extraEnv contains a platform-reserved environment variable")
			}
		}
	}
	if native, ok := values[agentType].(map[string]any); ok {
		if _, ok := native["image"]; ok {
			return fmt.Errorf("native image overrides must use spec.agent.image")
		}
		if env, ok := native["env"].(map[string]any); ok {
			if _, ok := env["API_SERVER_PORT"]; ok {
				return fmt.Errorf("native port overrides must use spec.agent.port")
			}
		}
		if config, ok := native["config"].(map[string]any); ok {
			if _, ok := config["model"]; ok {
				return fmt.Errorf("native model overrides must use spec.model and Policy policy")
			}
			if gateway, ok := config["gateway"].(map[string]any); ok {
				if _, ok := gateway["port"]; ok {
					return fmt.Errorf("native port overrides must use spec.agent.port")
				}
			}
		}
	}
	if service, ok := values["service"].(map[string]any); ok {
		for _, field := range []string{"port", "targetPort"} {
			if _, ok := service[field]; ok {
				return fmt.Errorf("service.%s must use spec.agent.port", field)
			}
		}
	}
	return nil
}

func (r *AgentReconciler) prepareAgent(instance *api.Agent, team *api.Team) (*helm.PreparedRelease, error) {
	values, err := buildHelmValues(instance, team, r.DomainSuffix, r.SkillsStorageAccount, r.CetiRunHelmValues, r.ChartRegistry, r.DefaultModel)
	if err != nil {
		return nil, err
	}
	return r.HelmClient.Prepare(instanceNamespace(instance.Name), values, instance.Spec.Helm.Version)
}

// pinAgentDefaults changes only omitted fields. The reconciler writes this with
// resourceVersion and returns before acting on the new generation.
func pinAgentDefaults(spec *api.HarnessSpec, resolved agents.ResolvedAgent) bool {
	changed := false
	if spec.Image == nil && resolved.Image != "" {
		spec.Image = &resolved.Image
		changed = true
	}
	if spec.Port == nil && resolved.Port != nil {
		port := *resolved.Port
		spec.Port = &port
		changed = true
	}
	return changed
}
