// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"reflect"
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
	if primary := defaults["model"].(map[string]any)["primary"]; primary != "cetirun/"+testRouterGatewayModel {
		t.Fatalf("primary model = %v, want the router's Gateway name", primary)
	}
	allowed := defaults["modelPolicy"].(map[string]any)["allow"].([]any)
	found := false
	for _, ref := range allowed {
		found = found || ref == "cetirun/"+testRouterGatewayModel
	}
	if !found {
		t.Fatalf("the router must be allowed: %v", allowed)
	}
}

// OpenClaw's doctor rewrites the model names of the provider openai that it
// thinks are retired, such as gpt-4.1-mini, so the Gateway is the provider
// cetirun, and each model states what OpenClaw's catalog gave the provider
// openai (issue #500).
func TestOpenClawNamesTheGatewayProviderCetirun(t *testing.T) {
	input := aiGatewayInput("openclaw", "openai")
	input["aiGateway"].(map[string]any)["models"] = []any{
		map[string]any{"id": "gpt-4.1-mini", "name": "gpt-4.1-mini", "contextWindow": 1047576, "maxTokens": 32768},
		map[string]any{"id": "gpt-6-sol", "name": "gpt-6-sol"},
	}
	input["aiGateway"].(map[string]any)["visionModels"] = []any{"gpt-6-sol"}
	input["secrets"].(map[string]any)["model"] = "gpt-4.1-mini"
	rendered, err := renderAIGateway(t, input)
	if err != nil {
		t.Fatal(err)
	}
	providers := openClawProviders(t, rendered)
	if len(providers) != 1 || providers["cetirun"] == nil {
		t.Fatalf("providers = %v, want only cetirun", providers)
	}
	provider := providers["cetirun"]
	if provider["api"] != "openai-completions" {
		t.Fatalf("provider api = %v", provider["api"])
	}
	zero := map[string]any{"input": float64(0), "output": float64(0), "cacheRead": float64(0), "cacheWrite": float64(0)}
	wantInput := map[string][]any{"gpt-4.1-mini": {"text"}, "gpt-6-sol": {"text", "image"}}
	for _, entry := range provider["models"].([]any) {
		model := entry.(map[string]any)
		id, _ := model["id"].(string)
		if model["api"] != "openai-completions" || !reflect.DeepEqual(model["cost"], zero) || !reflect.DeepEqual(model["input"], wantInput[id]) {
			t.Fatalf("model %s = %v", id, model)
		}
		if model["reasoning"] != false {
			t.Fatalf("model %s must state that it does not reason: %v", id, model)
		}
		if _, set := model["compat"]; set {
			t.Fatalf("model %s = %v", id, model)
		}
	}
	defaults := openClawAgentDefaults(t, rendered)
	if defaults["model"].(map[string]any)["primary"] != "cetirun/gpt-4.1-mini" || defaults["utilityModel"] != "cetirun/gpt-4.1-mini" {
		t.Fatalf("defaults = %v", defaults)
	}
	wantRefs := []any{"cetirun/gpt-4.1-mini", "cetirun/gpt-6-sol"}
	if allowed := defaults["modelPolicy"].(map[string]any)["allow"]; !reflect.DeepEqual(allowed, wantRefs) {
		t.Fatalf("allowed = %v", allowed)
	}
	for _, ref := range wantRefs {
		if _, ok := defaults["models"].(map[string]any)[ref.(string)]; !ok {
			t.Fatalf("models = %v, want %s", defaults["models"], ref)
		}
	}
}

func TestOpenClawRefusesAGatewayModelWithTheProviderPrefix(t *testing.T) {
	for _, id := range []string{"openai/gpt-4.1-mini", "cetirun/gpt-4.1-mini"} {
		input := aiGatewayInput("openclaw", "openai")
		input["aiGateway"].(map[string]any)["models"] = []any{map[string]any{"id": id, "name": id}}
		if _, err := renderAIGateway(t, input); err == nil {
			t.Fatalf("the chart rendered the model %s", id)
		}
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

// visionInput is routerInput with the router marked as accepting images, and
// the other model not.
func visionInput(agentType string, vision ...any) map[string]any {
	input := routerInput(agentType)
	// The operator sends an empty list, not null, when no model accepts images.
	input["aiGateway"].(map[string]any)["visionModels"] = append([]any{}, vision...)
	return input
}

// A model the operator lists as accepting images gets them with a request:
// OpenClaw takes input text and image, Hermes supports_vision. For the
// others, OpenClaw takes input text only, as it has no catalog for the
// Gateway provider, and Hermes still asks the model to describe an image. A
// release the operator prepares
// renders the configuration with the operator's renderer, standalone Helm
// with the chart's; both read the same template.
func TestAgentsSendImagesToTheModelsThatAcceptThem(t *testing.T) {
	for _, renderer := range []struct {
		name   string
		render func(*testing.T, map[string]any) (map[string]string, error)
	}{{"standalone", renderStandalone}, {"prepared", renderAIGateway}} {
		t.Run(renderer.name, func(t *testing.T) {
			rendered, err := renderer.render(t, visionInput("openclaw", testRouterGatewayModel))
			if err != nil {
				t.Fatal(err)
			}
			inputs := map[string]any{}
			for _, entry := range openClawProvider(t, rendered)["models"].([]any) {
				model := entry.(map[string]any)
				inputs[model["id"].(string)] = model["input"]
			}
			if !reflect.DeepEqual(inputs[testRouterGatewayModel], []any{"text", "image"}) || !reflect.DeepEqual(inputs["gpt-6-sol"], []any{"text"}) {
				t.Fatalf("OpenClaw inputs = %v", inputs)
			}
			rendered, err = renderer.render(t, visionInput("hermes", testRouterGatewayModel))
			if err != nil {
				t.Fatal(err)
			}
			hermes := parseHermes(t, rendered)
			models := hermes.config["providers"].(map[string]any)["cetirun-gateway"].(map[string]any)["models"].(map[string]any)
			router, other := models[testRouterGatewayModel].(map[string]any), models["gpt-6-sol"].(map[string]any)
			if router["supports_vision"] != true || router["context_length"] != float64(128000) {
				t.Fatalf("Hermes router = %v", router)
			}
			if _, set := other["supports_vision"]; set {
				t.Fatalf("Hermes model = %v", other)
			}
			// No model accepts images: the configurations are those of a chart
			// without the value.
			for _, agentType := range []string{"openclaw", "hermes"} {
				with, err := renderer.render(t, visionInput(agentType))
				if err != nil {
					t.Fatal(err)
				}
				without := routerInput(agentType)
				delete(without["aiGateway"].(map[string]any), "visionModels")
				before, err := renderer.render(t, without)
				if err != nil {
					t.Fatal(err)
				}
				if with["cetirun/templates/configmap.yaml"] != before["cetirun/templates/configmap.yaml"] {
					t.Fatalf("%s: an empty list changed the configuration", agentType)
				}
			}
		})
	}
}

// The chart refuses what the operator never sends.
func TestVisionModelsMustBeModelIDs(t *testing.T) {
	for name, value := range map[string]any{
		"an empty ID": []any{""},
		"not a list":  testRouterGatewayModel,
		"not strings": []any{7},
	} {
		input := routerInput("openclaw")
		input["aiGateway"].(map[string]any)["visionModels"] = value
		if _, err := renderAIGateway(t, input); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// reasoningInput is routerInput with the router marked as reasoning, and the
// other model not.
func reasoningInput(agentType string, reasoning ...any) map[string]any {
	input := routerInput(agentType)
	// The operator sends an empty list, not null, when no model reasons.
	input["aiGateway"].(map[string]any)["reasoningModels"] = append([]any{}, reasoning...)
	return input
}

// A model the operator lists as reasoning gets OpenClaw's reasoning levels,
// and OpenClaw sends the effort to the Gateway: reasoning true and
// compat.supportsReasoningEffort true, which OpenClaw does not assume for a
// Gateway address. The others get reasoning false, so OpenClaw offers only
// the level off. Hermes' configuration does not change. Both renderers read
// the same template.
func TestOpenClawReasonsWithTheModelsThatReason(t *testing.T) {
	for _, renderer := range []struct {
		name   string
		render func(*testing.T, map[string]any) (map[string]string, error)
	}{{"standalone", renderStandalone}, {"prepared", renderAIGateway}} {
		t.Run(renderer.name, func(t *testing.T) {
			rendered, err := renderer.render(t, reasoningInput("openclaw", testRouterGatewayModel))
			if err != nil {
				t.Fatal(err)
			}
			models := map[string]map[string]any{}
			for _, entry := range openClawProvider(t, rendered)["models"].([]any) {
				model := entry.(map[string]any)
				models[model["id"].(string)] = model
			}
			router, other := models[testRouterGatewayModel], models["gpt-6-sol"]
			if router["reasoning"] != true || !reflect.DeepEqual(router["compat"], map[string]any{"supportsReasoningEffort": true}) {
				t.Fatalf("OpenClaw router = %v", router)
			}
			if _, set := other["compat"]; other["reasoning"] != false || set {
				t.Fatalf("OpenClaw model = %v", other)
			}
			// No model reasons: every model states it.
			rendered, err = renderer.render(t, reasoningInput("openclaw"))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range openClawProvider(t, rendered)["models"].([]any) {
				if model := entry.(map[string]any); model["reasoning"] != false {
					t.Fatalf("OpenClaw model = %v", model)
				}
			}
			// Hermes does not read the value.
			with, err := renderer.render(t, reasoningInput("hermes", testRouterGatewayModel))
			if err != nil {
				t.Fatal(err)
			}
			without := routerInput("hermes")
			before, err := renderer.render(t, without)
			if err != nil {
				t.Fatal(err)
			}
			if with["cetirun/templates/configmap.yaml"] != before["cetirun/templates/configmap.yaml"] {
				t.Fatal("Hermes: the reasoning models changed the configuration")
			}
		})
	}
}

// A default model that the Gateway does not list for the Agent, such as the
// placeholder of an Agent without models, does not reason.
func TestOpenClawUnlistedDefaultModelDoesNotReason(t *testing.T) {
	input := reasoningInput("openclaw", testRouterGatewayModel)
	input["secrets"].(map[string]any)["model"] = "no-models-available"
	rendered, err := renderAIGateway(t, input)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range openClawProvider(t, rendered)["models"].([]any) {
		if model := entry.(map[string]any); model["id"] == "no-models-available" && model["reasoning"] != false {
			t.Fatalf("placeholder = %v", model)
		} else if model["id"] == "no-models-available" {
			return
		}
	}
	t.Fatal("the placeholder is not listed")
}

// The chart refuses what the operator never sends.
func TestReasoningModelsMustBeModelIDs(t *testing.T) {
	for name, value := range map[string]any{
		"an empty ID": []any{""},
		"not a list":  testRouterGatewayModel,
		"not strings": []any{7},
	} {
		input := routerInput("openclaw")
		input["aiGateway"].(map[string]any)["reasoningModels"] = value
		if _, err := renderAIGateway(t, input); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
