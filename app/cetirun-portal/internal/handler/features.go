// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"net/http"

	"github.com/Azure/CetiRun/app/cetirun-portal/internal/config"
)

// FeaturesHandler handles /api/features requests.
type FeaturesHandler struct {
	Config config.Config
}

// FeaturesResponse is the JSON response for GET /api/features.
type FeaturesResponse struct {
	TailscaleEnabled     bool     `json:"tailscaleEnabled"`
	AgentIdentityEnabled bool     `json:"agentIdentityEnabled"`
	MaxInstancesPerUser  int      `json:"maxInstancesPerUser"`
	AllowedAgentTypes    []string `json:"allowedAgentTypes"`
}

// GetFeatures handles GET /api/features.
func (h *FeaturesHandler) GetFeatures(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, FeaturesResponse{
		TailscaleEnabled:     h.Config.TailscaleEnabled,
		AgentIdentityEnabled: h.Config.AgentIdentityEnabled,
		MaxInstancesPerUser:  h.Config.MaxInstancesPerUser,
		AllowedAgentTypes:    h.Config.AllowedAgentTypes,
	})
}
