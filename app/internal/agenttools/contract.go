// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package agenttools defines the trusted service and Broker wire contract.
package agenttools

import "time"

const ContractVersion = "2026-10-19"

// Service fixes the token destination and audience. Workloads cannot override it.
// Request paths are relative to BasePath, with a leading slash.
type Service struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Origin   string `json:"origin"`
	BasePath string `json:"basePath"`
	Audience string `json:"audience,omitempty"`
}

func Services() []Service {
	return []Service{
		{ID: "azure_arm", Provider: "azure", Origin: "https://management.azure.com", BasePath: "/", Audience: "https://management.azure.com/"},
	}
}

func LookupService(id string) (Service, bool) {
	for _, service := range Services() {
		if service.ID == id {
			return service, true
		}
	}
	return Service{}, false
}

func ProviderServices(provider string) []string {
	result := []string{}
	for _, service := range Services() {
		if service.Provider == provider {
			result = append(result, service.ID)
		}
	}
	return result
}

type IdentityBinding struct {
	InstanceUID string `json:"instanceUid"`
	TenantID    string `json:"tenantId,omitempty"`
	PrincipalID string `json:"principalId,omitempty"`
	ClientID    string `json:"clientId,omitempty"`
}

// Connection exposes credential-free account and service metadata.
type Connection struct {
	ID             string           `json:"id"`
	Mode           string           `json:"mode"`
	Identity       *IdentityBinding `json:"identity,omitempty"`
	Provider       string           `json:"provider"`
	AccountDisplay string           `json:"accountDisplay"`
	Services       []string         `json:"services"`
	Scopes         []string         `json:"scopes"`
	Revision       uint64           `json:"revision"`
	Status         string           `json:"status"`
	CreatedAt      time.Time        `json:"createdAt"`
}

// Binding associates exactly one trusted Agent with a Connection and services.
// It does not authorize provider operations or resources.
type Binding struct {
	ID           string   `json:"id"`
	InstanceUID  string   `json:"instanceUid"`
	ConnectionID string   `json:"connectionId"`
	Services     []string `json:"services"`
	Enabled      bool     `json:"enabled"`
	Revision     uint64   `json:"revision"`
}

type Request struct {
	CallerProof        string  `json:"callerProof"`
	ConnectionID       string  `json:"connectionId"`
	Service            string  `json:"service"`
	BindingRevision    *uint64 `json:"bindingRevision,omitempty"`
	ConnectionRevision *uint64 `json:"connectionRevision,omitempty"`
	ContractVersion    string  `json:"contractVersion"`
}

type ListResponse struct {
	InstanceUID     string       `json:"instanceUid"`
	Connections     []Connection `json:"connections"`
	Bindings        []Binding    `json:"bindings"`
	ContractVersion string       `json:"contractVersion"`
}

type CheckResponse struct {
	// Stable provider account ID, only included in internal check/issue responses.
	ExecutorAccountID string     `json:"executorAccountId,omitempty"`
	InstanceUID       string     `json:"instanceUid"`
	Connection        Connection `json:"connection"`
	Binding           Binding    `json:"binding"`
	Service           string     `json:"service"`
	ContractVersion   string     `json:"contractVersion"`
}

type IssueResponse struct {
	CheckResponse
	AccessToken string `json:"accessToken"`
	ExpiresAt   string `json:"expiresAt"`
}
