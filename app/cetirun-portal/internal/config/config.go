// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package config

import (
	"os"
	"slices"
	"strconv"
	"strings"
)

var defaultAllowedAgentTypes = []string{"hermes", "openclaw"}

// Config holds all runtime configuration for the portal.
type Config struct {
	// ListenAddr is the address the public HTTP server binds to.
	ListenAddr string
	// InternalListenAddr is the address for the internal-only HTTP server (cluster traffic only).
	// This server exposes /internal/auth for the Istio EnvoyFilter Lua ownership check.
	// It must NOT be reachable from outside the cluster (not exposed via Istio VirtualService).
	InternalListenAddr string
	// TailscaleEnabled controls whether the Tailscale feature is exposed in the portal.
	TailscaleEnabled bool
	// NoticeBanner is an optional message displayed as a banner in the portal UI.
	// Empty string means no banner.
	NoticeBanner string
	// MaxInstancesPerUser is the maximum number of Agents a single user may create.
	// 0 means unlimited. Defaults to 1.
	MaxInstancesPerUser int
	// AllowedAgentTypes controls type availability in the form and backend.
	AllowedAgentTypes []string
	// AgentIdentityEnabled requires cryptographically verified Entra owner
	// claims and exposes the per-instance authorization experience.
	AgentIdentityEnabled bool
	// EntraTenantID and PortalClientID identify the issuer and audience of the
	// ID token passed by oauth2-proxy to the Portal backend.
	EntraTenantID  string
	PortalClientID string
	// AgentIdentityBrokerURL is an internal cluster URL, never browser-visible.
	AgentIdentityBrokerURL      string
	PortalOrigin                string
	RegistryCredentialNamespace string
}

// Load reads configuration from environment variables.
func Load() Config {
	return Config{
		ListenAddr:                  envOrDefault("LISTEN_ADDR", ":8080"),
		InternalListenAddr:          envOrDefault("INTERNAL_LISTEN_ADDR", ":8081"),
		TailscaleEnabled:            os.Getenv("FEATURE_TAILSCALE_ENABLED") == "true",
		NoticeBanner:                os.Getenv("NOTICE_BANNER"),
		MaxInstancesPerUser:         envOrDefaultInt("MAX_INSTANCES_PER_USER", 1),
		AllowedAgentTypes:           envOrDefaultAgentTypes("ALLOWED_AGENT_TYPES", defaultAllowedAgentTypes),
		AgentIdentityEnabled:        os.Getenv("FEATURE_AGENT_IDENTITY_ENABLED") == "true",
		EntraTenantID:               envOrDefault("ENTRA_TENANT_ID", ""),
		PortalClientID:              envOrDefault("PORTAL_OIDC_CLIENT_ID", ""),
		AgentIdentityBrokerURL:      envOrDefault("AGENT_ID_BROKER_URL", "http://cetirun-agent-broker.cetirun-credentials.svc.cluster.local:8080"),
		PortalOrigin:                envOrDefault("PORTAL_ORIGIN", ""),
		RegistryCredentialNamespace: envOrDefault("REGISTRY_CREDENTIAL_NAMESPACE", "cetirun-registry-credentials"),
	}
}

func (c Config) AgentTypeAllowed(kind string) bool {
	allowed := c.AllowedAgentTypes
	if allowed == nil {
		allowed = defaultAllowedAgentTypes
	}
	return slices.Contains(allowed, kind)
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envOrDefaultInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return fallback
}

func envOrDefaultAgentTypes(key string, fallback []string) []string {
	v := os.Getenv(key)
	if strings.TrimSpace(v) == "" {
		return append([]string(nil), fallback...)
	}

	seen := map[string]bool{}
	agentTypes := []string{}
	for _, item := range strings.Split(v, ",") {
		agentType := strings.TrimSpace(strings.ToLower(item))
		if seen[agentType] || (agentType != "openclaw" && agentType != "hermes" && agentType != "custom") {
			continue
		}
		seen[agentType] = true
		agentTypes = append(agentTypes, agentType)
	}
	if len(agentTypes) == 0 {
		return append([]string(nil), fallback...)
	}
	return agentTypes
}
