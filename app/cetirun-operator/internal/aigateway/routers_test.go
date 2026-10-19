// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package aigateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAddRouterSendsTheComplexityRouter(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/model/new" || r.Header.Get("Authorization") != "Bearer sk-master" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"model_id":"x"}`))
	}))
	defer server.Close()
	client, err := New(server.URL, "sk-master", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	request := RouterRequest{
		ID: "cetirun-router-0123456789abcdef-01234567", ModelName: "cetirun-router-0123456789abcdef",
		AccessGroups: []string{"cetirun-router-0123456789abcdef"}, DefaultModel: "medium-model",
		Config:            map[string]any{"classifier_type": "heuristic", "tiers": map[string]any{"SIMPLE": "small-model"}},
		InputCostPerToken: 0.00001, OutputCostPerToken: 0.00005,
	}
	if err := client.AddRouter(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	params := body["litellm_params"].(map[string]any)
	info := body["model_info"].(map[string]any)
	if body["model_name"] != request.ModelName || params["model"] != "auto_router/complexity_router" ||
		params["complexity_router_default_model"] != "medium-model" || params["input_cost_per_token"] != 0.00001 ||
		params["output_cost_per_token"] != 0.00005 || params["complexity_router_config"].(map[string]any)["classifier_type"] != "heuristic" ||
		info["id"] != request.ID || info["access_groups"].([]any)[0] != request.ModelName {
		t.Fatalf("body = %v", body)
	}
	for _, field := range []string{"api_key", "api_base"} {
		if _, ok := params[field]; ok {
			t.Fatalf("a router has no endpoint; %s must not be sent", field)
		}
	}
	if _, ok := info["team_id"]; ok {
		t.Fatal("team_id makes a licensed team model")
	}
}

func TestAddRouterRejectsIncompleteRequests(t *testing.T) {
	client, err := New("http://gateway.example.com", "sk-master", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []RouterRequest{
		{ModelName: "m", Config: map[string]any{"a": 1}, InputCostPerToken: 1, OutputCostPerToken: 1},
		{ID: "i", ModelName: "m", InputCostPerToken: 1, OutputCostPerToken: 1},
		{ID: "i", ModelName: "m", Config: map[string]any{"a": 1}},
	} {
		if err := client.AddRouter(context.Background(), request); err == nil {
			t.Fatalf("request %+v was accepted", request)
		}
	}
}

func TestAddRouterReportsARefusedConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"complexity_router_config is invalid at tiers: bad"}}`))
	}))
	defer server.Close()
	client, err := New(server.URL, "sk-master", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	err = client.AddRouter(context.Background(), RouterRequest{ID: "i", ModelName: "m", Config: map[string]any{"a": 1}, InputCostPerToken: 1, OutputCostPerToken: 1})
	if err == nil || !strings.Contains(err.Error(), "refused the router configuration") || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("err = %v", err)
	}
}
