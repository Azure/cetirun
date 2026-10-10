// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import "time"

const catalogVersion = "2026-10-10"

type Owner struct {
	TenantID string `json:"-"`
	ObjectID string `json:"-"`
}

type Connection struct {
	ID             string    `json:"id"`
	Provider       string    `json:"provider"`
	AccountDisplay string    `json:"accountDisplay"`
	Capabilities   []string  `json:"capabilities"`
	Revision       uint64    `json:"revision"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"-"`
	Owner          Owner     `json:"-"`
	AccountID      string    `json:"-"`
}

type Constraints struct {
	SubscriptionIDs    []string `json:"subscriptionIds"`
	RepositoryIDs      []string `json:"repositoryIds"`
	BaseBranches       []string `json:"baseBranches"`
	CalendarIDs        []string `json:"calendarIds"`
	RecipientAddresses []string `json:"recipientAddresses"`
	RecipientDomains   []string `json:"recipientDomains"`
	AllowInvites       bool     `json:"allowInvites"`
}

type AgentGrant struct {
	ID           string      `json:"id"`
	InstanceUID  string      `json:"instanceUid"`
	ConnectionID string      `json:"connectionId"`
	Tools        []string    `json:"tools"`
	Constraints  Constraints `json:"constraints"`
	ExpiresAt    time.Time   `json:"expiresAt"`
	Revision     uint64      `json:"revision"`
	Owner        Owner       `json:"-"`
	Revoked      bool        `json:"-"`
	CreatedAt    time.Time   `json:"-"`
	UpdatedAt    time.Time   `json:"-"`
}

type providerCredential struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Scopes       []string  `json:"scopes"`
}

type connectionTransaction struct {
	Provider          string    `json:"provider"`
	StateHash         string    `json:"stateHash"`
	CodeVerifier      string    `json:"codeVerifier"`
	Nonce             string    `json:"nonce,omitempty"`
	ExpectedAccountID string    `json:"expectedAccountId,omitempty"`
	ExpiresAt         time.Time `json:"expiresAt"`
	Consumed          bool      `json:"consumed"`
}

type connectionRecord struct {
	Connection  Connection             `json:"connection"`
	Credential  *providerCredential    `json:"credential,omitempty"`
	Transaction *connectionTransaction `json:"transaction,omitempty"`
	Deleted     bool                   `json:"deleted"`
}

type grantRecord struct {
	Grant AgentGrant `json:"grant"`
}

type Agent struct {
	Name                  string
	UID                   string
	Namespace             string
	ServiceAccount        string
	OwnerTenantID         string
	OwnerObjectID         string
	AgentIdentityClientID string
	WorkloadReady         bool
	AgentIdentityReady    bool
}

type apiError struct {
	Error struct {
		Code          string `json:"code"`
		Message       string `json:"message"`
		CorrelationID string `json:"correlationId"`
	} `json:"error"`
}
