// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import "time"

const catalogVersion = "2026-10-16"

type Owner struct {
	TenantID string `json:"-"`
	ObjectID string `json:"-"`
}

const (
	modeDelegated = "delegated"
	modeAgent     = "agent"
)

// IdentityBinding is derived from platform status and verified provider data.
type IdentityBinding struct {
	InstanceUID string `json:"instanceUid"`
	TenantID    string `json:"tenantId,omitempty"`
	PrincipalID string `json:"principalId,omitempty"`
	ClientID    string `json:"clientId,omitempty"`
}

// ResourceAuthorization records provider evidence independently of the request.
type ResourceAuthorization struct {
	State           string     `json:"state"`
	Source          string     `json:"source"`
	VerifiedAt      *time.Time `json:"verifiedAt,omitempty"`
	ValidUntil      *time.Time `json:"validUntil,omitempty"`
	Reason          string     `json:"reason,omitempty"`
	Scopes          []string   `json:"scopes,omitempty"`
	Actions         []string   `json:"actions,omitempty"`
	RequestIDs      []string   `json:"requestIds,omitempty"`
	BindingRevision uint64     `json:"bindingRevision"`
}

type Connection struct {
	Mode           string           `json:"mode,omitempty"`
	Identity       *IdentityBinding `json:"identity,omitempty"`
	ID             string           `json:"id"`
	Provider       string           `json:"provider"`
	AccountDisplay string           `json:"accountDisplay"`
	Capabilities   []string         `json:"capabilities"`
	Revision       uint64           `json:"revision"`
	Status         string           `json:"status"`
	CreatedAt      time.Time        `json:"createdAt"`
	UpdatedAt      time.Time        `json:"-"`
	Owner          Owner            `json:"-"`
	AccountID      string           `json:"-"`
}

type Constraints struct {
	SubscriptionIDs    []string `json:"subscriptionIds"`
	ResourceGroupNames []string `json:"resourceGroupNames"`
	RepositoryIDs      []string `json:"repositoryIds"`
	BaseBranches       []string `json:"baseBranches"`
	CalendarIDs        []string `json:"calendarIds"`
	RecipientAddresses []string `json:"recipientAddresses"`
	RecipientDomains   []string `json:"recipientDomains"`
	AllowInvites       bool     `json:"allowInvites"`
}

type AgentGrant struct {
	Authorization *ResourceAuthorization `json:"authorization,omitempty"`
	ID            string                 `json:"id"`
	InstanceUID   string                 `json:"instanceUid"`
	ConnectionID  string                 `json:"connectionId"`
	Tools         []string               `json:"tools"`
	Constraints   Constraints            `json:"constraints"`
	ExpiresAt     time.Time              `json:"expiresAt"`
	Revision      uint64                 `json:"revision"`
	Owner         Owner                  `json:"-"`
	Revoked       bool                   `json:"-"`
	CreatedAt     time.Time              `json:"-"`
	UpdatedAt     time.Time              `json:"-"`
}

type providerCredential struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
	// NonExpiring is set only for provider credential responses that explicitly
	// permit an access token without an expiry. It prevents a missing expiry
	// from becoming an implicit provider-agnostic exception.
	NonExpiring bool     `json:"nonExpiring,omitempty"`
	Scopes      []string `json:"scopes"`
}

type connectionTransaction struct {
	Provider  string `json:"provider"`
	StateHash string `json:"stateHash"`
	// BrowserBindingHash is present only for GitHub OAuth transactions. The
	// Portal holds the corresponding browser secret in an HttpOnly cookie.
	BrowserBindingHash string    `json:"browserBindingHash,omitempty"`
	CodeVerifier       string    `json:"codeVerifier"`
	Nonce              string    `json:"nonce,omitempty"`
	ExpectedAccountID  string    `json:"expectedAccountId,omitempty"`
	ExpiresAt          time.Time `json:"expiresAt"`
	Consumed           bool      `json:"consumed"`
}

type connectionRecord struct {
	Connection  Connection             `json:"connection"`
	Credential  *providerCredential    `json:"credential,omitempty"`
	Transaction *connectionTransaction `json:"transaction,omitempty"`
	Deleted     bool                   `json:"deleted"`
	// Display snapshots come from the trusted Agent resource. They are never
	// used to resolve ownership or authorize calls; instance UID remains binding.
	InstanceName      string `json:"instanceName,omitempty"`
	InstanceNamespace string `json:"instanceNamespace,omitempty"`
}

type grantRecord struct {
	Grant             AgentGrant `json:"grant"`
	InstanceName      string     `json:"instanceName,omitempty"`
	InstanceNamespace string     `json:"instanceNamespace,omitempty"`
}

type Agent struct {
	Deleting              bool
	RuntimeType           string
	Name                  string
	UID                   string
	Namespace             string
	ServiceAccount        string
	OwnerTenantID         string
	OwnerObjectID         string
	AgentIdentityObjectID string
	AgentIdentityClientID string
	WorkloadReady         bool
	AgentIdentityReady    bool
	UnsupportedRuntime    bool
}

type apiError struct {
	Error struct {
		Code          string `json:"code"`
		Message       string `json:"message"`
		CorrelationID string `json:"correlationId"`
	} `json:"error"`
}
