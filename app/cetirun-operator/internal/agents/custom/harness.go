// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package custom

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Azure/CetiRun/app/cetirun-operator/internal/agents"
)

type Harness struct{}

var webBasePathPattern = regexp.MustCompile(`^/ui/[a-z0-9][a-z0-9-]*/$`)

const (
	// GatewayModelLimitsEnvironment carries only platform-projected limits.
	GatewayModelLimitsEnvironment = "CETIRUN_GATEWAY_MODEL_LIMITS"
	gatewayModelLimitsMaxModels   = 256
	gatewayModelLimitsMaxBytes    = 64 * 1024
	gatewayModelLimitsMaxInteger  = int64(1<<53) - 1
)

type gatewayModelLimits struct {
	Version int                 `json:"version"`
	Models  []gatewayModelLimit `json:"models"`
}

type gatewayModelLimit struct {
	ID              string `json:"id"`
	ContextWindow   int64  `json:"contextWindow"`
	MaxOutputTokens int64  `json:"maxOutputTokens"`
}

// GatewayModelLimits serializes only complete, bounded limits supplied by the
// platform. A Custom runtime must reject an unlisted model rather than guess
// its context size. Duplicates use the smallest limits so no model name gets a
// wider budget than any route the Gateway may select.
func GatewayModelLimits(models []agents.GatewayModel) string {
	limits := map[string]gatewayModelLimit{}
	incomplete := map[string]bool{}
	for _, model := range models {
		if !validGatewayModelLimit(model) {
			if validGatewayModelID(model.ID) {
				incomplete[model.ID] = true
			}
			continue
		}
		limit := gatewayModelLimit{ID: model.ID, ContextWindow: model.ContextWindow, MaxOutputTokens: model.MaxTokens}
		if current, ok := limits[limit.ID]; !ok {
			limits[limit.ID] = limit
		} else {
			current.ContextWindow = min(current.ContextWindow, limit.ContextWindow)
			current.MaxOutputTokens = min(current.MaxOutputTokens, limit.MaxOutputTokens)
			limits[limit.ID] = current
		}
	}
	for id := range incomplete {
		delete(limits, id)
	}
	ids := make([]string, 0, len(limits))
	for id := range limits {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := gatewayModelLimits{Version: 1, Models: make([]gatewayModelLimit, 0, min(len(ids), gatewayModelLimitsMaxModels))}
	for _, id := range ids {
		if len(result.Models) == gatewayModelLimitsMaxModels {
			break
		}
		candidate := append(append([]gatewayModelLimit(nil), result.Models...), limits[id])
		raw, _ := json.Marshal(gatewayModelLimits{Version: 1, Models: candidate})
		if len(raw) > gatewayModelLimitsMaxBytes {
			break
		}
		result.Models = candidate
	}
	raw, _ := json.Marshal(result)
	return string(raw)
}

func validGatewayModelLimit(model agents.GatewayModel) bool {
	if !validGatewayModelID(model.ID) ||
		model.ContextWindow <= 0 || model.MaxTokens <= 0 ||
		model.ContextWindow > gatewayModelLimitsMaxInteger || model.MaxTokens > gatewayModelLimitsMaxInteger {
		return false
	}
	return true
}

func validGatewayModelID(id string) bool {
	return id != "" && strings.TrimSpace(id) == id && len(id) <= 256 && utf8.ValidString(id) && !strings.ContainsFunc(id, unicode.IsControl)
}

func (Harness) Describe() agents.Descriptor {
	return agents.Descriptor{Type: "custom", PortName: "http", HealthPath: "/healthz", ReadinessPath: "/healthz", StartupPath: "/healthz"}
}

func (Harness) Validate(agent agents.ResolvedAgent, bindings agents.Bindings) error {
	resolved, err := agents.ResolveCustom(agents.Request{
		Type: agent.Type, Image: agent.Image, Username: agent.Username, Port: agent.Port, WebUI: agent.WebUI,
	})
	if err != nil || resolved.HomeDir != agent.HomeDir {
		return fmt.Errorf("invalid resolved custom Agent")
	}
	if agent.WebUI && (!webBasePathPattern.MatchString(bindings.WebBasePath) || bindings.WebInstance == "") {
		return fmt.Errorf("Custom Web UI requires a platform path and instance identity")
	}
	return agents.ValidatePortableBindings(bindings)
}

func (h Harness) Build(agent agents.ResolvedAgent, bindings agents.Bindings) (agents.Contribution, error) {
	if err := h.Validate(agent, bindings); err != nil {
		return agents.Contribution{}, err
	}
	c := agents.Contribution{
		Agent: agent, ConfigFiles: map[string]string{}, Ports: []agents.PortBinding{},
		Environment: []agents.EnvironmentValue{
			{Name: "HOME", Value: agent.HomeDir},
		},
	}
	c.Environment = append(c.Environment,
		agents.EnvironmentValue{Name: "OPENAI_BASE_URL", Value: bindings.Model.BaseURL},
		agents.EnvironmentValue{Name: "OPENAI_API_KEY", CredentialSlot: "model"},
		agents.EnvironmentValue{Name: GatewayModelLimitsEnvironment, Value: GatewayModelLimits(bindings.Model.GatewayModels)})
	if agent.Port != nil {
		c.Environment = append(c.Environment, agents.EnvironmentValue{Name: "PORT", Value: strconv.Itoa(int(*agent.Port))})
		c.Ports = []agents.PortBinding{{Name: "http", Number: *agent.Port}}
		c.HTTP = &agents.HTTPBinding{PortName: "http", HealthPath: "/healthz", ReadinessPath: "/healthz", StartupPath: "/healthz"}
		if !agent.WebUI {
			c.HTTP.ChatPath = "/v1/chat/completions"
			c.HTTP.ModelsPath = "/v1/models"
		}
	}
	if agent.WebUI {
		c.Environment = append(c.Environment,
			agents.EnvironmentValue{Name: "CETIRUN_WEB_BASE_PATH", Value: bindings.WebBasePath},
			agents.EnvironmentValue{Name: "CETIRUN_WEB_INSTANCE", Value: bindings.WebInstance})
	}
	return c, nil
}
