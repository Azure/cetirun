// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package aigateway

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// ComplexityRouterModel is LiteLLM's model value that makes a deployment a
// complexity router: requests for its model name are classified and sent to
// the model of the tier the classifier picks.
const ComplexityRouterModel = "auto_router/complexity_router"

// RouterRequest registers a complexity router as a database deployment.
type RouterRequest struct {
	// ID is the caller-chosen deployment ID.
	ID string
	// ModelName is the model name callers request.
	ModelName    string
	AccessGroups []string
	// Config is LiteLLM's complexity_router_config.
	Config map[string]any
	// DefaultModel is the model of requests that cannot be classified.
	DefaultModel string
	// InputCostPerToken and OutputCostPerToken price the router's own name,
	// which the Gateway's check for unpriced models requires. Each request is
	// charged at the price of the tier model it is sent to.
	InputCostPerToken  float64
	OutputCostPerToken float64
}

// RouterBody is the /model/new request body for req. It carries no
// credentials: a router has no endpoint of its own.
func RouterBody(req RouterRequest) map[string]any {
	params := map[string]any{
		"model":                    ComplexityRouterModel,
		"complexity_router_config": req.Config,
		"input_cost_per_token":     req.InputCostPerToken,
		"output_cost_per_token":    req.OutputCostPerToken,
	}
	if req.DefaultModel != "" {
		params["complexity_router_default_model"] = req.DefaultModel
	}
	return map[string]any{
		"model_name":     req.ModelName,
		"litellm_params": params,
		"model_info":     map[string]any{"id": req.ID, "access_groups": req.AccessGroups},
	}
}

// AddRouter registers a complexity router. The Gateway rejects a second
// deployment with the same ID; LiteLLM also keeps one router per model name,
// so a router replacing another of the same name is added after that one is
// deleted.
func (c *Client) AddRouter(ctx context.Context, req RouterRequest) error {
	if req.ID == "" || req.ModelName == "" || len(req.Config) == 0 {
		return fmt.Errorf("router ID, name and configuration are required")
	}
	if req.InputCostPerToken <= 0 || req.OutputCostPerToken <= 0 {
		return fmt.Errorf("router prices must be positive")
	}
	_, err := c.do(ctx, http.MethodPost, "/model/new", RouterBody(req), nil)
	if err != nil && strings.Contains(err.Error(), "complexity_router_config") {
		return fmt.Errorf("the AI Gateway refused the router configuration: %w", err)
	}
	return err
}
