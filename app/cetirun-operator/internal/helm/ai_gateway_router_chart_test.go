// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"testing"
)

const testRouterGatewayModel = "cetirun-router-0123456789abcdef"

// routerInput is an Agent on the Gateway whose default is a Team model
// router: the operator lists it with its internal Gateway name as the ID and
// the router's name as the display name.
func routerInput(agentType string) map[string]any {
	input := aiGatewayInput(agentType, "openai")
	input["aiGateway"].(map[string]any)["models"] = []any{
		map[string]any{"id": "gpt-6-sol", "name": "gpt-6-sol", "contextWindow": 272000, "maxTokens": 128000},
		map[string]any{"id": testRouterGatewayModel, "name": "auto", "contextWindow": 128000, "maxTokens": 16384},
	}
	input["secrets"].(map[string]any)["model"] = testRouterGatewayModel
	return input
}

func TestOpenClawListsAModelRouterByItsName(t *testing.T) {
	rendered, err := renderAIGateway(t, routerInput("openclaw"))
	if err != nil {
		t.Fatal(err)
	}
	var router map[string]any
	for _, entry := range openClawProvider(t, rendered)["models"].([]any) {
		if model := entry.(map[string]any); model["id"] == testRouterGatewayModel {
			router = model
		}
	}
	if router == nil || router["name"] != "auto" || router["contextWindow"] != float64(128000) || router["maxTokens"] != float64(16384) {
		t.Fatalf("router entry = %v", router)
	}
	defaults := openClawAgentDefaults(t, rendered)
	if primary := defaults["model"].(map[string]any)["primary"]; primary != "openai/"+testRouterGatewayModel {
		t.Fatalf("primary model = %v, want the router's Gateway name", primary)
	}
	allowed := defaults["modelPolicy"].(map[string]any)["allow"].([]any)
	found := false
	for _, ref := range allowed {
		found = found || ref == "openai/"+testRouterGatewayModel
	}
	if !found {
		t.Fatalf("the router must be allowed: %v", allowed)
	}
}

func TestHermesListsAModelRouterByItsGatewayName(t *testing.T) {
	hermes := renderHermes(t, routerInput("hermes"))
	provider := hermes.config["providers"].(map[string]any)["cetirun-gateway"].(map[string]any)
	models := provider["models"].(map[string]any)
	router, ok := models[testRouterGatewayModel].(map[string]any)
	if !ok || router["context_length"] != float64(128000) || provider["default_model"] != testRouterGatewayModel {
		t.Fatalf("Hermes provider = %v", provider)
	}
}
