// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import "time"

type Owner struct {
	TenantID string `json:"-"`
	ObjectID string `json:"-"`
}

// IdentityBinding comes from current platform status, not request parameters.
type IdentityBinding struct {
	InstanceUID string `json:"instanceUid"`
	TenantID    string `json:"tenantId,omitempty"`
	PrincipalID string `json:"principalId,omitempty"`
	ClientID    string `json:"clientId,omitempty"`
}

type Connection struct {
	Mode           string           `json:"mode,omitempty"`
	Identity       *IdentityBinding `json:"identity,omitempty"`
	ID             string           `json:"id"`
	Provider       string           `json:"provider"`
	AccountDisplay string           `json:"accountDisplay"`
	Services       []string         `json:"services"`
	Scopes         []string         `json:"scopes"`
	Revision       uint64           `json:"revision"`
	Status         string           `json:"status"`
	CreatedAt      time.Time        `json:"createdAt"`
	UpdatedAt      time.Time        `json:"-"`
	Owner          Owner            `json:"-"`
	AccountID      string           `json:"-"`
}

// ConnectionBinding selects an identity and services, not provider operations.
type ConnectionBinding struct {
	ID           string    `json:"id"`
	InstanceUID  string    `json:"instanceUid"`
	ConnectionID string    `json:"connectionId"`
	Services     []string  `json:"services"`
	Enabled      bool      `json:"enabled"`
	Revision     uint64    `json:"revision"`
	Owner        Owner     `json:"-"`
	Revoked      bool      `json:"-"`
	CreatedAt    time.Time `json:"-"`
	UpdatedAt    time.Time `json:"-"`
}

type connectionRecord struct {
	Connection Connection `json:"connection"`
	Deleted    bool       `json:"deleted"`
	// Display snapshots come from the trusted Agent resource. They are never
	// used to resolve ownership or authorize calls; instance UID remains binding.
	InstanceName      string `json:"instanceName,omitempty"`
	InstanceNamespace string `json:"instanceNamespace,omitempty"`
}

type bindingRecord struct {
	Binding           ConnectionBinding `json:"binding"`
	InstanceName      string            `json:"instanceName,omitempty"`
	InstanceNamespace string            `json:"instanceNamespace,omitempty"`
}

const modeAgent = "agent"
