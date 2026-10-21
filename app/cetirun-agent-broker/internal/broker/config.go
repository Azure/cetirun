// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"errors"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type Config struct {
	ConnectionBindingsEnabled bool
	PortalNamespace           string
	PortalServiceAccount      string
	ToolServiceNamespace      string
	ToolServiceAccount        string
	ToolRuntimeTypes          []string
	WorkloadTokenAudience     string
	TLSListenAddress          string
	TLSCertFile               string
	TLSKeyFile                string
	TLSClientCAFile           string

	ListenAddress          string
	TenantID               string
	BlueprintClientID      string
	AuthorizationClientID  string
	RedirectURI            string
	PortalOrigin           string
	PortalBaseURL          string
	KeyVaultKeyURL         string
	EncryptionClientID     string
	AuthSDKURL             string
	StoreNamespace         string
	FederatedTokenFile     string
	KubernetesTokenFile    string
	KubernetesCAFile       string
	OperatorNamespace      string
	OperatorServiceAccount string
	TransactionTTL         time.Duration
	RequestTimeout         time.Duration
}

func LoadConfig() (Config, error) {
	cfg := Config{
		PortalNamespace:        os.Getenv("PORTAL_NAMESPACE"),
		PortalServiceAccount:   os.Getenv("PORTAL_SERVICE_ACCOUNT"),
		ToolServiceNamespace:   os.Getenv("TOOL_SERVICE_NAMESPACE"),
		ToolServiceAccount:     os.Getenv("TOOL_SERVICE_ACCOUNT"),
		ToolRuntimeTypes:       []string{"hermes", "openclaw"},
		WorkloadTokenAudience:  env("WORKLOAD_TOKEN_AUDIENCE", "agent-tools-mcp"),
		TLSListenAddress:       env("TLS_LISTEN_ADDR", ":8443"),
		TLSCertFile:            os.Getenv("TLS_CERT_FILE"),
		TLSKeyFile:             os.Getenv("TLS_KEY_FILE"),
		TLSClientCAFile:        os.Getenv("TLS_CLIENT_CA_FILE"),
		ListenAddress:          env("LISTEN_ADDR", ":8080"),
		TenantID:               os.Getenv("ENTRA_TENANT_ID"),
		BlueprintClientID:      os.Getenv("AGENT_ID_BLUEPRINT_CLIENT_ID"),
		AuthorizationClientID:  os.Getenv("AGENT_ID_AUTHORIZATION_CLIENT_ID"),
		RedirectURI:            os.Getenv("AGENT_ID_REDIRECT_URI"),
		PortalOrigin:           strings.TrimRight(os.Getenv("PORTAL_ORIGIN"), "/"),
		PortalBaseURL:          strings.TrimRight(os.Getenv("PORTAL_BASE_URL"), "/"),
		KeyVaultKeyURL:         os.Getenv("KEY_VAULT_KEY_URL"),
		EncryptionClientID:     os.Getenv("AZURE_CLIENT_ID"),
		AuthSDKURL:             strings.TrimRight(env("ENTRA_SDK_URL", "http://127.0.0.1:5000"), "/"),
		StoreNamespace:         env("AUTHORIZATION_STORE_NAMESPACE", "cetirun-credentials"),
		FederatedTokenFile:     os.Getenv("AZURE_FEDERATED_TOKEN_FILE"),
		KubernetesTokenFile:    env("KUBERNETES_TOKEN_FILE", "/var/run/cetirun/kubernetes/token"),
		KubernetesCAFile:       env("KUBERNETES_CA_FILE", "/var/run/cetirun/kubernetes/ca.crt"),
		OperatorNamespace:      os.Getenv("OPERATOR_NAMESPACE"),
		OperatorServiceAccount: os.Getenv("OPERATOR_SERVICE_ACCOUNT"),
		TransactionTTL:         10 * time.Minute,
		RequestTimeout:         15 * time.Second,
	}

	enabled, err := strconv.ParseBool(env("CONNECTION_BINDINGS_ENABLED", "false"))
	if err != nil {
		return Config{}, errors.New("CONNECTION_BINDINGS_ENABLED must be a boolean")
	}
	cfg.ConnectionBindingsEnabled = enabled
	if enabled {
		if !dnsLabel(cfg.PortalNamespace) || !dnsLabel(cfg.PortalServiceAccount) || !dnsLabel(cfg.ToolServiceNamespace) || !dnsLabel(cfg.ToolServiceAccount) || strings.TrimSpace(cfg.WorkloadTokenAudience) == "" {
			return Config{}, errors.New("connection bindings require trusted Portal, tool service, and token audience configuration")
		}
		if cfg.TLSCertFile == "" || cfg.TLSKeyFile == "" || cfg.TLSClientCAFile == "" {
			return Config{}, errors.New("connection bindings require mutual TLS certificate, key and client CA")
		}
	}
	if !enabled && (cfg.TLSCertFile != "" || cfg.TLSKeyFile != "" || cfg.TLSClientCAFile != "") {
		return Config{}, errors.New("TLS service credentials require connection bindings to be enabled")
	}
	for _, value := range []string{cfg.TenantID, cfg.BlueprintClientID, cfg.AuthorizationClientID, cfg.EncryptionClientID} {
		if !uuidPattern.MatchString(value) {
			return Config{}, errors.New("Agent Identity UUID configuration is missing or invalid")
		}
	}
	if cfg.FederatedTokenFile == "" || cfg.PortalOrigin == "" || cfg.PortalBaseURL == "" ||
		!dnsLabel(cfg.OperatorNamespace) || !dnsLabel(cfg.OperatorServiceAccount) {
		return Config{}, errors.New("workload token and Portal configuration are required")
	}
	redirect, err := url.Parse(cfg.RedirectURI)
	if err != nil || redirect.Scheme != "https" || redirect.Host == "" || redirect.RawQuery != "" || redirect.Fragment != "" {
		return Config{}, errors.New("Agent Identity redirect URI must be an absolute HTTPS URL")
	}
	portal, err := url.Parse(cfg.PortalOrigin)
	if err != nil || portal.Scheme != "https" || portal.Host == "" || portal.Path != "" {
		return Config{}, errors.New("Portal origin must be an HTTPS origin")
	}
	keyHost, _, _, keyOK := parseKeyID(cfg.KeyVaultKeyURL, false)
	if !keyOK || !strings.HasSuffix(strings.ToLower(keyHost), ".vault.azure.net") {
		return Config{}, errors.New("Key Vault key URL is invalid")
	}
	return cfg, nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func (c Config) connectionModeEnabled(connection Connection) bool {
	return c.ConnectionBindingsEnabled && connection.Provider == "azure" && connection.Mode == modeAgent
}
