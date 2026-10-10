// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package config

import (
	"errors"
	"net/url"
	"os"
	"strings"
)

const DefaultAudience = "agent-tools-mcp"

type Config struct {
	ListenAddress             string
	TLSCertFile               string
	TLSKeyFile                string
	BrokerBaseURL             *url.URL
	WorkloadTokenAudience     string
	PortalBaseURL             *url.URL
	KubernetesTokenFile       string
	KubernetesCAFile          string
	OperationRecordsNamespace string
}

func Load() (Config, error) {
	broker, err := httpsOrigin(os.Getenv("AGENT_BROKER_BASE_URL"))
	if err != nil {
		return Config{}, errors.New("AGENT_BROKER_BASE_URL must be an absolute HTTPS origin")
	}
	var portal *url.URL
	if raw := strings.TrimSpace(os.Getenv("PORTAL_BASE_URL")); raw != "" {
		portal, err = httpsOrigin(raw)
		if err != nil {
			return Config{}, errors.New("PORTAL_BASE_URL must be an absolute HTTPS origin when configured")
		}
	}
	audience := strings.TrimSpace(os.Getenv("WORKLOAD_TOKEN_AUDIENCE"))
	if audience == "" {
		audience = DefaultAudience
	}
	namespace := strings.TrimSpace(os.Getenv("OPERATION_RECORDS_NAMESPACE"))
	if !dnsLabel(namespace) {
		return Config{}, errors.New("OPERATION_RECORDS_NAMESPACE is required and must be a DNS label")
	}
	listen := strings.TrimSpace(os.Getenv("LISTEN_ADDRESS"))
	if listen == "" {
		listen = ":8443"
	}
	certFile, keyFile := strings.TrimSpace(os.Getenv("TLS_CERT_FILE")), strings.TrimSpace(os.Getenv("TLS_KEY_FILE"))
	if certFile == "" || keyFile == "" {
		return Config{}, errors.New("TLS_CERT_FILE and TLS_KEY_FILE are required")
	}
	return Config{
		ListenAddress:             listen,
		TLSCertFile:               certFile,
		TLSKeyFile:                keyFile,
		BrokerBaseURL:             broker,
		WorkloadTokenAudience:     audience,
		PortalBaseURL:             portal,
		KubernetesTokenFile:       valueOr("KUBERNETES_TOKEN_FILE", "/var/run/cetirun/kubernetes/token"),
		KubernetesCAFile:          valueOr("KUBERNETES_CA_FILE", "/var/run/cetirun/kubernetes/ca.crt"),
		OperationRecordsNamespace: namespace,
	}, nil
}

func httpsOrigin(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("not an HTTPS origin")
	}
	return u, nil
}

func valueOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func dnsLabel(value string) bool {
	if len(value) == 0 || len(value) > 63 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' {
			continue
		}
		return false
	}
	return true
}
