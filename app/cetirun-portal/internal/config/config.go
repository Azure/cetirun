package config

import (
	"os"
	"strconv"
	"strings"
)

var defaultAllowedAgentTypes = []string{"openclaw", "hermes"}

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
	// MaxInstancesPerUser is the maximum number of OpenClawInstances a single user may create.
	// 0 means unlimited. Defaults to 1.
	MaxInstancesPerUser int
	// AllowedAgentTypes controls which agent types are offered in the portal create form.
	AllowedAgentTypes []string
}

// Load reads configuration from environment variables.
func Load() Config {
	return Config{
		ListenAddr:          envOrDefault("LISTEN_ADDR", ":8080"),
		InternalListenAddr:  envOrDefault("INTERNAL_LISTEN_ADDR", ":8081"),
		TailscaleEnabled:    os.Getenv("FEATURE_TAILSCALE_ENABLED") == "true",
		NoticeBanner:        os.Getenv("NOTICE_BANNER"),
		MaxInstancesPerUser: envOrDefaultInt("MAX_INSTANCES_PER_USER", 1),
		AllowedAgentTypes:   envOrDefaultAgentTypes("ALLOWED_AGENT_TYPES", defaultAllowedAgentTypes),
	}
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
		if seen[agentType] || (agentType != "openclaw" && agentType != "hermes") {
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
