// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import "time"

type AuthorizationRecord struct {
	InstanceUID           string       `json:"instanceUid"`
	InstanceName          string       `json:"instanceName"`
	OwnerTenantID         string       `json:"ownerTenantId"`
	OwnerObjectID         string       `json:"ownerObjectId"`
	AgentIdentityClientID string       `json:"agentIdentityClientId"`
	Generation            uint64       `json:"generation"`
	Revoked               bool         `json:"revoked"`
	Transaction           *Transaction `json:"transaction,omitempty"`
	Tokens                *TokenCache  `json:"tokens,omitempty"`
	AccountDisplay        string       `json:"accountDisplay,omitempty"`
	LastRenewed           *time.Time   `json:"lastRenewed,omitempty"`
	LastUsed              *time.Time   `json:"lastUsed,omitempty"`
}

type Transaction struct {
	StateHash    string    `json:"stateHash"`
	CodeVerifier string    `json:"codeVerifier"`
	Nonce        string    `json:"nonce"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Consumed     bool      `json:"consumed"`
}

type TokenCache struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

type Agent struct {
	Name                  string
	UID                   string
	Namespace             string
	ServiceAccount        string
	OwnerTenantID         string
	OwnerObjectID         string
	AgentIdentityClientID string
	AgentIdentityReady    bool
}

type statusResponse struct {
	Status        string     `json:"status"`
	Account       string     `json:"account,omitempty"`
	LastRenewed   *time.Time `json:"lastRenewed,omitempty"`
	LastUsed      *time.Time `json:"lastUsed,omitempty"`
	ActionURL     string     `json:"actionUrl,omitempty"`
	CorrelationID string     `json:"correlationId"`
}

type apiError struct {
	Error struct {
		Code          string `json:"code"`
		Message       string `json:"message"`
		CorrelationID string `json:"correlationId"`
	} `json:"error"`
}
