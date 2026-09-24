// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"fmt"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/agents"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/helm"
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
	for _, key := range []string{"runtime", "agentRequest"} {
		if _, ok := values[key]; ok {
			return fmt.Errorf("helm.values.%s is reserved for the platform", key)
		}
	}
	if _, ok := values["image"]; ok {
		return fmt.Errorf("helm.values.image must use spec.agent.image")
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
	if spec.Port == nil && resolved.Port != 0 {
		spec.Port = &resolved.Port
		changed = true
	}
	return changed
}
