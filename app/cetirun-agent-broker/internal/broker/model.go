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
	Name                            string
	UID                             string
	Generation                      int64
	Namespace                       string
	ServiceAccount                  string
	OwnerTenantID                   string
	OwnerObjectID                   string
	AgentIdentityClientID           string
	Deleting                        bool
	Suspended                       bool
	AgentIdentityEnabled            bool
	AgentIdentityBound              bool
	AgentIdentityReady              bool
	AgentIdentityObservedGeneration int64
	PolicyObservedGeneration        int64
	PolicyPhase                     string
	// DesiredCredentialPolicyRevision is the policy revision calculated by the
	// Operator. CredentialPolicyRevision is the revision which the Operator
	// read back from this Broker after applying it. Both must agree before a
	// workload can receive a delegated credential.
	DesiredCredentialPolicyRevision string
	CredentialPolicyRevision        string
}

// CredentialPolicy is the deliberately small, typed contract the Operator
// projects into the Broker. It contains no user or downstream credential.
type CredentialPolicy struct {
	Revision string   `json:"revision"`
	Services []string `json:"services"`
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
