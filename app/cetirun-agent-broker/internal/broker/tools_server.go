// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"
)

const defaultGrantTTL = 30 * 24 * time.Hour
const maxGrantTTL = 90 * 24 * time.Hour
const connectionBindingHeader = "X-CetiRun-Connection-Binding"

type connectionStartRequest struct {
	Provider string `json:"provider"`
}
type grantRequest struct {
	ConnectionID string      `json:"connectionId"`
	Tools        []string    `json:"tools"`
	Constraints  Constraints `json:"constraints"`
	ExpiresAt    time.Time   `json:"expiresAt"`
}
type toolRequest struct {
	CallerProof        string         `json:"callerProof"`
	Tool               string         `json:"tool"`
	ConnectionID       string         `json:"connectionId"`
	Arguments          map[string]any `json:"arguments"`
	GrantRevision      uint64         `json:"grantRevision"`
	ConnectionRevision uint64         `json:"connectionRevision"`
	CatalogVersion     string         `json:"catalogVersion"`
}

type toolAuthorizationError struct {
	code   string
	status int
}

func (e toolAuthorizationError) Error() string { return e.code }

func toolAuthorizationFailure(code string, status int) error {
	return toolAuthorizationError{code: code, status: status}
}

func ownerFromPortal(r *http.Request) (Owner, error) {
	owner := Owner{TenantID: r.Header.Get("X-CetiRun-Owner-Tenant-ID"), ObjectID: r.Header.Get("X-CetiRun-Owner-Object-ID")}
	if !uuidPattern.MatchString(owner.TenantID) || !uuidPattern.MatchString(owner.ObjectID) {
		return Owner{}, errors.New("verified owner identity is required")
	}
	return owner, nil
}

func (s *Server) portalGrantAgent(r *http.Request, owner Owner) (Agent, error) {
	agent, err := s.store.getAgent(r.Context(), r.PathValue("name"))
	if err != nil || agent.Deleting || !sameOwner(Owner{agent.OwnerTenantID, agent.OwnerObjectID}, owner) || subtle.ConstantTimeCompare([]byte(agent.UID), []byte(r.Header.Get("X-CetiRun-Instance-UID"))) != 1 {
		return Agent{}, errors.New("instance ownership is invalid")
	}
	return agent, nil
}

func (s *Server) connectionsList(w http.ResponseWriter, r *http.Request) {
	correlation := correlationID(r)
	owner, err := ownerFromPortal(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "forbidden", "Verified owner identity is required", correlation)
		return
	}
	connections, err := s.store.listConnections(r.Context(), owner)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Connection storage is unavailable", correlation)
		return
	}
	for index := range connections {
		connections[index] = projectedConnection(connections[index])
	}
	writeJSON(w, http.StatusOK, connections)
}

func (s *Server) connectionStart(w http.ResponseWriter, r *http.Request) {
	correlation := correlationID(r)
	owner, err := ownerFromPortal(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "forbidden", "Verified owner identity is required", correlation)
		return
	}
	var input connectionStartRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if !validProvider(input.Provider) {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "The provider is not supported", correlation)
		return
	}
	browserBindingHash, err := githubBrowserBindingHash(input.Provider, r.Header.Get(connectionBindingHeader))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "GitHub sign-in requires a browser binding proof", correlation)
		return
	}
	state, verifier, nonce, err := authorizationRandomValues(randomValue)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Connection could not be started", correlation)
		return
	}
	provider := s.providers
	if provider == nil {
		provider = newProviderClient(s.cfg)
	}
	authorizationURL, err := provider.authorizationURL(input.Provider, state, verifier, nonce)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Provider OAuth is not configured", correlation)
		return
	}
	now := s.currentTime().UTC()
	record := &connectionRecord{Connection: Connection{ID: opaqueID(), Provider: input.Provider, Mode: modeDelegated, Capabilities: []string{}, Revision: 1, Status: "connecting", CreatedAt: now, UpdatedAt: now, Owner: owner}, Transaction: &connectionTransaction{Provider: input.Provider, StateHash: stateHash(state), BrowserBindingHash: browserBindingHash, CodeVerifier: verifier, Nonce: nonce, ExpiresAt: now.Add(s.cfg.TransactionTTL)}}
	if err := s.store.putConnection(r.Context(), record, ""); err != nil {
		status, code := http.StatusServiceUnavailable, "dependency_unavailable"
		if errors.Is(err, errConflict) {
			status, code = http.StatusConflict, "conflict"
		}
		writeAPIError(w, status, code, "Connection state changed; retry", correlation)
		return
	}
	writeJSON(w, http.StatusOK, connectionStartResponse(record, authorizationURL))
}

func (s *Server) connectionGet(w http.ResponseWriter, r *http.Request) {
	correlation := correlationID(r)
	owner, err := ownerFromPortal(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "forbidden", "Verified owner identity is required", correlation)
		return
	}
	record, _, err := s.store.getOwnedConnection(r.Context(), owner, r.PathValue("id"))
	if errors.Is(err, errNotFound) || (err == nil && (record.Deleted || !sameOwner(record.Connection.Owner, owner))) {
		writeAPIError(w, http.StatusNotFound, "not_found", "Connection not found", correlation)
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Connection storage is unavailable", correlation)
		return
	}
	writeJSON(w, http.StatusOK, record.Connection)
}

func (s *Server) connectionDelete(w http.ResponseWriter, r *http.Request) {
	correlation := correlationID(r)
	owner, err := ownerFromPortal(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "forbidden", "Verified owner identity is required", correlation)
		return
	}
	for attempt := 0; attempt < 3; attempt++ {
		record, version, err := s.store.getOwnedConnection(r.Context(), owner, r.PathValue("id"))
		if errors.Is(err, errNotFound) || (err == nil && (record.Deleted || !sameOwner(record.Connection.Owner, owner))) {
			writeAPIError(w, http.StatusNotFound, "not_found", "Connection not found", correlation)
			return
		}
		if err != nil {
			writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Connection storage is unavailable", correlation)
			return
		}
		record.Deleted, record.Credential, record.Transaction = true, nil, nil
		record.Connection.Status, record.Connection.Capabilities, record.Connection.AccountDisplay, record.Connection.AccountID = "disconnected", nil, "", ""
		record.Connection.Revision++
		record.Connection.UpdatedAt = s.currentTime().UTC()
		if err := s.store.putConnection(r.Context(), record, version); errors.Is(err, errConflict) {
			continue
		} else if err != nil {
			writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Connection could not be disconnected", correlation)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeAPIError(w, http.StatusConflict, "conflict", "Connection state changed; retry", correlation)
}

func (s *Server) connectionReconnect(w http.ResponseWriter, r *http.Request) {
	correlation := correlationID(r)
	owner, err := ownerFromPortal(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "forbidden", "Verified owner identity is required", correlation)
		return
	}
	state, verifier, nonce, err := authorizationRandomValues(randomValue)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Connection could not be started", correlation)
		return
	}
	for attempt := 0; attempt < 3; attempt++ {
		record, version, err := s.store.getOwnedConnection(r.Context(), owner, r.PathValue("id"))
		if errors.Is(err, errNotFound) || (err == nil && (record.Deleted || !sameOwner(record.Connection.Owner, owner))) {
			writeAPIError(w, http.StatusNotFound, "not_found", "Connection not found", correlation)
			return
		}
		if err != nil {
			writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Connection storage is unavailable", correlation)
			return
		}
		if connectionMode(record.Connection) != modeDelegated {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "Independent identities do not use personal sign-in", correlation)
			return
		}
		browserBindingHash, err := githubBrowserBindingHash(record.Connection.Provider, r.Header.Get(connectionBindingHeader))
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "GitHub sign-in requires a browser binding proof", correlation)
			return
		}
		provider := s.providers
		if provider == nil {
			provider = newProviderClient(s.cfg)
		}
		authorizationURL, err := provider.authorizationURL(record.Connection.Provider, state, verifier, nonce)
		if err != nil {
			writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Provider OAuth is not configured", correlation)
			return
		}
		now := s.currentTime().UTC()
		record.Credential = nil
		record.Connection.Status = "connecting"
		record.Connection.Revision++
		record.Connection.UpdatedAt = now
		record.Transaction = &connectionTransaction{Provider: record.Connection.Provider, StateHash: stateHash(state), BrowserBindingHash: browserBindingHash, CodeVerifier: verifier, Nonce: nonce, ExpectedAccountID: record.Connection.AccountID, ExpiresAt: now.Add(s.cfg.TransactionTTL)}
		if err := s.store.putConnection(r.Context(), record, version); errors.Is(err, errConflict) {
			continue
		} else if err != nil {
			writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Connection could not be started", correlation)
			return
		}
		writeJSON(w, http.StatusOK, connectionStartResponse(record, authorizationURL))
		return
	}
	writeAPIError(w, http.StatusConflict, "conflict", "Connection state changed; retry", correlation)
}

func (s *Server) connectionCallback(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	if err := r.ParseForm(); err != nil {
		s.callbackPage(w, false)
		return
	}
	state, code := r.Form.Get("state"), r.Form.Get("code")
	if !validOAuthState(state) || !validAuthorizationCode(code) || r.Form.Get("error") != "" {
		s.callbackPage(w, false)
		return
	}
	record, version, err := s.store.findConnectionByState(r.Context(), state)
	if err != nil || record.Transaction == nil || !validProvider(record.Transaction.Provider) || record.Transaction.Consumed || s.currentTime().After(record.Transaction.ExpiresAt) {
		s.callbackPage(w, false)
		return
	}
	tx := *record.Transaction
	if tx.Provider == "github" && (!portalPrincipalAllowed(r.Header.Get("X-Forwarded-Client-Cert"), s.cfg) || !githubBrowserBindingMatches(tx, r.Header.Get(connectionBindingHeader))) {
		s.callbackPage(w, false)
		return
	}
	record.Transaction.Consumed = true
	if err := s.store.putConnection(r.Context(), record, version); err != nil {
		s.callbackPage(w, false)
		return
	}
	credential, display, accountID, err := s.finishConnectionCallback(r.Context(), record, tx, code)
	if err != nil {
		s.callbackPage(w, false)
		return
	}
	current, currentVersion, err := s.store.getConnection(r.Context(), record.Connection.ID)
	if err != nil || current.Deleted || current.Transaction == nil || !current.Transaction.Consumed || current.Transaction.StateHash != tx.StateHash {
		s.callbackPage(w, false)
		return
	}
	now := s.currentTime().UTC()
	if tx.ExpectedAccountID != "" && accountID != tx.ExpectedAccountID {
		current.Transaction = nil
		current.Credential = nil
		current.Connection.Status = "interaction_required"
		current.Connection.Revision++
		current.Connection.UpdatedAt = now
		_ = s.store.putConnection(r.Context(), current, currentVersion)
		s.callbackPage(w, false)
		return
	}
	current.Credential = &credential
	current.Transaction = nil
	current.Connection.AccountDisplay = display
	current.Connection.AccountID = accountID
	if current.Connection.Provider == "azure" || current.Connection.Provider == "microsoft" {
		current.Connection.Capabilities = configuredOBOCapabilities(s.cfg, current.Connection.Provider)
	} else {
		current.Connection.Capabilities = credential.Scopes
	}
	current.Connection.Status, current.Connection.Revision, current.Connection.UpdatedAt = "connected", current.Connection.Revision+1, now
	if err := s.store.putConnection(r.Context(), current, currentVersion); err != nil {
		s.callbackPage(w, false)
		return
	}
	s.callbackPage(w, true)
}

func githubBrowserBindingHash(provider, proof string) (string, error) {
	if provider != "github" {
		return "", nil
	}
	if !validGitHubBrowserBindingProof(proof) {
		return "", errors.New("GitHub browser binding proof is invalid")
	}
	return stateHash(proof), nil
}

func githubBrowserBindingMatches(tx connectionTransaction, proof string) bool {
	if !validGitHubBrowserBindingProof(proof) || tx.BrowserBindingHash == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(stateHash(proof)), []byte(tx.BrowserBindingHash)) == 1
}

func validGitHubBrowserBindingProof(proof string) bool {
	if !validOAuthState(proof) {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(proof)
	return err == nil && len(decoded) == 32
}

func connectionStartResponse(record *connectionRecord, authorizationURL string) map[string]any {
	return map[string]any{
		"id":                     record.Connection.ID,
		"authorizationUrl":       authorizationURL,
		"expiresAt":              record.Transaction.ExpiresAt,
		"browserBindingRequired": record.Transaction.Provider == "github",
	}
}

func (s *Server) finishConnectionCallback(ctx context.Context, record *connectionRecord, tx connectionTransaction, code string) (providerCredential, string, string, error) {
	if tx.Provider == "azure" || tx.Provider == "microsoft" {
		oauth := s.oauth
		if oauth == nil {
			oauth = newOAuthClient(s.cfg)
		}
		tokens, err := oauth.exchangeCode(ctx, code, tx.CodeVerifier)
		if err != nil || tokens.RefreshToken == "" || tokens.IDToken == "" {
			return providerCredential{}, "", "", errors.New("Microsoft OAuth exchange failed")
		}
		display, err := oauth.verifier.verifyIDToken(ctx, tokens.IDToken, tx.Nonce, record.Connection.Owner.TenantID, record.Connection.Owner.ObjectID)
		if err != nil {
			return providerCredential{}, "", "", err
		}
		if err := s.authSDK.validate(ctx, tokens.AccessToken, record.Connection.Owner.TenantID, s.cfg.BlueprintClientID, s.cfg.AuthorizationClientID, record.Connection.Owner.ObjectID); err != nil {
			return providerCredential{}, "", "", err
		}
		return credentialFromOAuth(tokens), display, record.Connection.Owner.ObjectID, nil
	}
	provider := s.providers
	if provider == nil {
		provider = newProviderClient(s.cfg)
	}
	return provider.exchangeCode(ctx, tx.Provider, code, tx.CodeVerifier)
}

func (s *Server) grantsList(w http.ResponseWriter, r *http.Request) {
	correlation := correlationID(r)
	owner, err := ownerFromPortal(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "forbidden", "Verified owner identity is required", correlation)
		return
	}
	agent, err := s.portalGrantAgent(r, owner)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "forbidden", "The instance owner binding is invalid", correlation)
		return
	}
	grants, err := s.store.listGrants(r.Context(), owner, agent.UID)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Grant storage is unavailable", correlation)
		return
	}
	visible := make([]AgentGrant, 0, len(grants))
	for _, grant := range grants {
		if !grant.Revoked {
			visible = append(visible, grant)
		}
	}
	writeJSON(w, http.StatusOK, visible)
}

func (s *Server) grantCreate(w http.ResponseWriter, r *http.Request) {
	correlation := correlationID(r)
	owner, err := ownerFromPortal(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "forbidden", "Verified owner identity is required", correlation)
		return
	}
	agent, err := s.portalGrantAgent(r, owner)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "forbidden", "The instance owner binding is invalid", correlation)
		return
	}
	var input grantRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	connection, _, err := s.store.getConnectionForAgent(r.Context(), agent, input.ConnectionID)
	if errors.Is(err, errNotFound) || (err == nil && (connection.Deleted || !sameOwner(connection.Connection.Owner, owner))) {
		writeAPIError(w, http.StatusNotFound, "not_found", "Connection not found", correlation)
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Connection storage is unavailable", correlation)
		return
	}
	if !s.connectionMatchesAgent(connection.Connection, agent) {
		writeAPIError(w, http.StatusForbidden, "identity_changed", "The identity is not bound to this Agent", correlation)
		return
	}
	grant, err := buildGrant(input, owner, agent.UID, connection.Connection, s.currentTime())
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error(), correlation)
		return
	}
	existing, err := s.store.listGrants(r.Context(), owner, agent.UID)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Grant storage is unavailable", correlation)
		return
	}
	for _, item := range existing {
		if item.ConnectionID == grant.ConnectionID && activeGrant(item, s.currentTime()) {
			writeAPIError(w, http.StatusConflict, "conflict", "An active grant already exists for this connection", correlation)
			return
		}
	}
	// The immutable instance/connection key turns the Kubernetes Secret name
	// into an atomic uniqueness boundary. The preceding list retains backwards
	// compatibility with active grants created before this key existed; this
	// check closes the list-then-create race for new grants.
	resourceVersion := ""
	if prior, version, err := s.store.getGrant(r.Context(), grant.ID, agent); err == nil {
		if prior.Grant.ConnectionID != grant.ConnectionID || prior.Grant.InstanceUID != grant.InstanceUID || !sameOwner(prior.Grant.Owner, grant.Owner) || activeGrant(prior.Grant, s.currentTime()) {
			writeAPIError(w, http.StatusConflict, "conflict", "An active grant already exists for this connection", correlation)
			return
		}
		// Reusing a revoked or expired deterministic record preserves the old
		// revoke-then-create behavior without allowing a second active record.
		grant.Revision = prior.Grant.Revision + 1
		resourceVersion = version
	} else if !errors.Is(err, errNotFound) {
		writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Grant storage is unavailable", correlation)
		return
	}
	if err := s.store.putGrant(r.Context(), &grantRecord{Grant: grant, InstanceName: agent.Name, InstanceNamespace: agent.Namespace}, resourceVersion); err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, errConflict) {
			status = http.StatusConflict
		}
		writeAPIError(w, status, "conflict", "Grant state changed; retry", correlation)
		return
	}
	writeJSON(w, http.StatusOK, grant)
}

func (s *Server) grantUpdate(w http.ResponseWriter, r *http.Request) {
	correlation := correlationID(r)
	owner, err := ownerFromPortal(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "forbidden", "Verified owner identity is required", correlation)
		return
	}
	agent, err := s.portalGrantAgent(r, owner)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "forbidden", "The instance owner binding is invalid", correlation)
		return
	}
	var input grantRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	expected, ok := ifMatchRevision(r)
	if !ok {
		writeAPIError(w, http.StatusPreconditionRequired, "revision_required", "If-Match revision is required", correlation)
		return
	}
	record, version, err := s.store.getGrant(r.Context(), r.PathValue("id"), agent)
	if errors.Is(err, errNotFound) || (err == nil && (!sameOwner(record.Grant.Owner, owner) || record.Grant.InstanceUID != agent.UID || record.Grant.Revoked)) {
		writeAPIError(w, http.StatusNotFound, "not_found", "Grant not found", correlation)
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Grant storage is unavailable", correlation)
		return
	}
	if record.Grant.Revision != expected {
		writeAPIError(w, http.StatusConflict, "conflict", "Grant revision changed; reload before updating", correlation)
		return
	}
	if input.ConnectionID != record.Grant.ConnectionID {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "A grant connection cannot be changed", correlation)
		return
	}
	connection, _, err := s.store.getConnectionForAgent(r.Context(), agent, input.ConnectionID)
	if err != nil || connection.Deleted || !sameOwner(connection.Connection.Owner, owner) {
		writeAPIError(w, http.StatusNotFound, "not_found", "Connection not found", correlation)
		return
	}
	if !s.connectionMatchesAgent(connection.Connection, agent) {
		writeAPIError(w, http.StatusForbidden, "identity_changed", "The identity is not bound to this Agent", correlation)
		return
	}
	updated, err := buildGrant(input, owner, agent.UID, connection.Connection, s.currentTime())
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error(), correlation)
		return
	}
	updated.ID, updated.CreatedAt, updated.Revision = record.Grant.ID, record.Grant.CreatedAt, record.Grant.Revision+1
	if err := s.store.putGrant(r.Context(), &grantRecord{Grant: updated, InstanceName: agent.Name, InstanceNamespace: agent.Namespace}, version); err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, errConflict) {
			status = http.StatusConflict
		}
		writeAPIError(w, status, "conflict", "Grant state changed; retry", correlation)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) grantDelete(w http.ResponseWriter, r *http.Request) {
	correlation := correlationID(r)
	owner, err := ownerFromPortal(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "forbidden", "Verified owner identity is required", correlation)
		return
	}
	agent, err := s.portalGrantAgent(r, owner)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "forbidden", "The instance owner binding is invalid", correlation)
		return
	}
	expected, ok := ifMatchRevision(r)
	if !ok {
		writeAPIError(w, http.StatusPreconditionRequired, "revision_required", "If-Match revision is required", correlation)
		return
	}
	record, version, err := s.store.getGrant(r.Context(), r.PathValue("id"), agent)
	if errors.Is(err, errNotFound) || (err == nil && (!sameOwner(record.Grant.Owner, owner) || record.Grant.InstanceUID != agent.UID || record.Grant.Revoked)) {
		writeAPIError(w, http.StatusNotFound, "not_found", "Grant not found", correlation)
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Grant storage is unavailable", correlation)
		return
	}
	if record.Grant.Revision != expected {
		writeAPIError(w, http.StatusConflict, "conflict", "Grant revision changed; reload before deleting", correlation)
		return
	}
	record.Grant.Revoked, record.Grant.Revision, record.Grant.UpdatedAt = true, record.Grant.Revision+1, s.currentTime().UTC()
	record.InstanceName, record.InstanceNamespace = agent.Name, agent.Namespace
	if err := s.store.putGrant(r.Context(), record, version); err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, errConflict) {
			status = http.StatusConflict
		}
		writeAPIError(w, status, "conflict", "Grant state changed; retry", correlation)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func buildGrant(input grantRequest, owner Owner, instanceUID string, connection Connection, now time.Time) (AgentGrant, error) {
	if input.ConnectionID == "" || len(input.Tools) == 0 || connection.Status != "connected" {
		return AgentGrant{}, errors.New("connection and explicit tools are required")
	}
	if input.ExpiresAt.IsZero() {
		input.ExpiresAt = now.Add(defaultGrantTTL)
	}
	if !input.ExpiresAt.After(now) || input.ExpiresAt.After(now.Add(maxGrantTTL)) {
		return AgentGrant{}, errors.New("expiresAt must be within 90 days")
	}
	tools := uniqueStrings(input.Tools)
	if len(tools) != len(input.Tools) {
		return AgentGrant{}, errors.New("tools must not contain duplicates")
	}
	for _, tool := range tools {
		if tool == "*" || !contains(connectionCapabilities(connection), tool) || !toolMatchesProvider(connection.Provider, tool) {
			return AgentGrant{}, errors.New("tools are not available for the connection")
		}
		if azureWriteTool(tool) && connectionMode(connection) != modeAgent {
			return AgentGrant{}, errors.New("Azure write tools require an independent Agent identity")
		}
	}
	if err := validateConstraints(tools, input.Constraints); err != nil {
		return AgentGrant{}, err
	}
	grant := AgentGrant{ID: grantKey(instanceUID, input.ConnectionID), InstanceUID: instanceUID, ConnectionID: input.ConnectionID, Tools: tools, Constraints: input.Constraints, ExpiresAt: input.ExpiresAt.UTC(), Revision: 1, Owner: owner, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	if connectionMode(connection) == modeAgent {
		if connection.Identity == nil || connection.Identity.InstanceUID != instanceUID || identityAuthorizationSource(connection.Provider) == "" {
			return AgentGrant{}, errors.New("independent identity binding is invalid")
		}
		for _, id := range input.Constraints.SubscriptionIDs {
			if !uuidPattern.MatchString(id) {
				return AgentGrant{}, errors.New("Azure subscription IDs must be UUIDs")
			}
		}
		if len(input.Constraints.SubscriptionIDs) > 20 {
			return AgentGrant{}, errors.New("at most 20 subscriptions may be requested")
		}
		grant.Authorization = &ResourceAuthorization{State: "pending", Source: "direct_rbac", BindingRevision: connection.Revision, Reason: "resource_owner_grant_required"}
	}
	return grant, nil
}

func validateConstraints(tools []string, constraints Constraints) error {
	all := [][]string{constraints.SubscriptionIDs, constraints.RepositoryIDs, constraints.BaseBranches, constraints.CalendarIDs, constraints.RecipientAddresses, constraints.RecipientDomains}
	for _, values := range all {
		for _, value := range values {
			if strings.TrimSpace(value) == "" || strings.Contains(value, "*") {
				return errors.New("constraints must use explicit values")
			}
		}
	}
	if hasPrefixTool(tools, "azure_") && len(constraints.SubscriptionIDs) == 0 {
		return errors.New("Azure tools require subscriptionIds")
	}
	if hasAzureWriteTool(tools) {
		if len(constraints.ResourceGroupNames) == 0 || len(constraints.ResourceGroupNames) > 20 {
			return errors.New("Azure write tools require 1 to 20 resourceGroupNames")
		}
		seen := map[string]bool{}
		for _, name := range constraints.ResourceGroupNames {
			if !safeResourceGroupName(name) || seen[strings.ToLower(name)] {
				return errors.New("resourceGroupNames must contain unique safe Azure resource group names")
			}
			seen[strings.ToLower(name)] = true
		}
	}
	if hasPrefixTool(tools, "github_") && len(constraints.RepositoryIDs) == 0 {
		return errors.New("GitHub tools require repositoryIds")
	}
	if hasTool(tools, "github_pull_requests_create") && len(constraints.BaseBranches) == 0 {
		return errors.New("Pull request creation requires baseBranches")
	}
	if hasPrefixTool(tools, "calendar_") && len(constraints.CalendarIDs) == 0 {
		return errors.New("Calendar tools require calendarIds")
	}
	if hasTool(tools, "mail_send") && len(constraints.RecipientAddresses) == 0 && len(constraints.RecipientDomains) == 0 {
		return errors.New("Mail sending requires recipient constraints")
	}
	return nil
}

func toolServiceTLSPrincipalAllowed(r *http.Request, cfg Config) bool {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		return false
	}
	leaf := r.TLS.PeerCertificates[0]
	if len(leaf.URIs) != 1 {
		return false
	}
	expected := "spiffe://cluster.local/ns/" + cfg.ToolServiceNamespace + "/sa/" + cfg.ToolServiceAccount
	return leaf.URIs[0].String() == expected
}

func (s *Server) toolRequest(r *http.Request, requireVersions bool) (workloadIdentity, *connectionRecord, AgentGrant, toolRequest, error) {
	if !toolServiceTLSPrincipalAllowed(r, s.cfg) {
		return workloadIdentity{}, nil, AgentGrant{}, toolRequest{}, toolAuthorizationFailure("permission_denied", http.StatusForbidden)
	}
	var input toolRequest
	if !decodeJSONDiscard(r, &input) {
		return workloadIdentity{}, nil, AgentGrant{}, toolRequest{}, toolAuthorizationFailure("invalid_request", http.StatusBadRequest)
	}
	if input.CallerProof == "" {
		return workloadIdentity{}, nil, AgentGrant{}, toolRequest{}, toolAuthorizationFailure("unauthenticated", http.StatusUnauthorized)
	}
	return s.checkToolAuthorization(r.Context(), input, requireVersions)
}

func (s *Server) checkToolAuthorization(ctx context.Context, input toolRequest, requireVersions bool) (workloadIdentity, *connectionRecord, AgentGrant, toolRequest, error) {
	identity, err := s.store.verifyWorkloadProof(ctx, input.CallerProof, s.cfg.WorkloadTokenAudience)
	if err != nil {
		if errors.Is(err, errDependencyUnavailable) {
			return workloadIdentity{}, nil, AgentGrant{}, toolRequest{}, toolAuthorizationFailure("dependency_unavailable", http.StatusServiceUnavailable)
		}
		return workloadIdentity{}, nil, AgentGrant{}, toolRequest{}, toolAuthorizationFailure("unauthenticated", http.StatusUnauthorized)
	}
	if input.Tool == "" || input.ConnectionID == "" || input.Arguments == nil {
		return workloadIdentity{}, nil, AgentGrant{}, toolRequest{}, toolAuthorizationFailure("invalid_request", http.StatusBadRequest)
	}
	connection, _, err := s.store.getConnectionForAgent(ctx, identity.Agent, input.ConnectionID)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return workloadIdentity{}, nil, AgentGrant{}, toolRequest{}, toolAuthorizationFailure("grant_required", http.StatusForbidden)
		}
		return workloadIdentity{}, nil, AgentGrant{}, toolRequest{}, toolAuthorizationFailure("dependency_unavailable", http.StatusServiceUnavailable)
	}
	owner := Owner{TenantID: identity.Agent.OwnerTenantID, ObjectID: identity.Agent.OwnerObjectID}
	if !sameOwner(owner, connection.Connection.Owner) || !s.connectionMatchesAgent(connection.Connection, identity.Agent) {
		return workloadIdentity{}, nil, AgentGrant{}, toolRequest{}, toolAuthorizationFailure("permission_denied", http.StatusForbidden)
	}
	if connection.Deleted || connection.Connection.Status == "disconnected" {
		return workloadIdentity{}, nil, AgentGrant{}, toolRequest{}, toolAuthorizationFailure("grant_required", http.StatusForbidden)
	}
	if connection.Connection.Status != "connected" {
		return workloadIdentity{}, nil, AgentGrant{}, toolRequest{}, toolAuthorizationFailure("interaction_required", http.StatusUnauthorized)
	}
	grants, err := s.store.listGrants(ctx, owner, identity.Agent.UID)
	if err != nil {
		return workloadIdentity{}, nil, AgentGrant{}, toolRequest{}, toolAuthorizationFailure("dependency_unavailable", http.StatusServiceUnavailable)
	}
	var grant *AgentGrant
	for index := range grants {
		candidate := &grants[index]
		if candidate.ConnectionID == input.ConnectionID && candidate.InstanceUID == identity.Agent.UID && sameOwner(candidate.Owner, owner) && effectiveGrant(*candidate, connection.Connection, s.currentTime()) && contains(candidate.Tools, input.Tool) {
			if grant != nil {
				return workloadIdentity{}, nil, AgentGrant{}, toolRequest{}, toolAuthorizationFailure("grant_required", http.StatusForbidden)
			}
			grant = candidate
		}
	}
	if grant == nil {
		return workloadIdentity{}, nil, AgentGrant{}, toolRequest{}, toolAuthorizationFailure("grant_required", http.StatusForbidden)
	}
	if !contains(connectionCapabilities(connection.Connection), input.Tool) || !toolMatchesProvider(connection.Connection.Provider, input.Tool) || (azureWriteTool(input.Tool) && connectionMode(connection.Connection) != modeAgent) || validateToolArguments(input.Tool, input.Arguments, grant.Constraints) != nil {
		return workloadIdentity{}, nil, AgentGrant{}, toolRequest{}, toolAuthorizationFailure("permission_denied", http.StatusForbidden)
	}
	if requireVersions && (input.CatalogVersion != catalogVersion || input.GrantRevision != grant.Revision || input.ConnectionRevision != connection.Connection.Revision) {
		return workloadIdentity{}, nil, AgentGrant{}, toolRequest{}, toolAuthorizationFailure("authorization_changed", http.StatusConflict)
	}
	return identity, connection, *grant, input, nil
}

func (s *Server) toolAuthorizationsList(w http.ResponseWriter, r *http.Request) {
	if !toolServiceTLSPrincipalAllowed(r, s.cfg) {
		writeAPIError(w, http.StatusForbidden, "permission_denied", "Verified tool service identity is required", correlationID(r))
		return
	}
	var input toolRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	identity, err := s.store.verifyWorkloadProof(r.Context(), input.CallerProof, s.cfg.WorkloadTokenAudience)
	if err != nil {
		if errors.Is(err, errDependencyUnavailable) {
			writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Workload proof could not be verified", correlationID(r))
		} else {
			writeAPIError(w, http.StatusUnauthorized, "unauthenticated", "Workload proof is invalid", correlationID(r))
		}
		return
	}
	grants, err := s.store.listGrants(r.Context(), Owner{identity.Agent.OwnerTenantID, identity.Agent.OwnerObjectID}, identity.Agent.UID)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Authorization storage is unavailable", correlationID(r))
		return
	}
	connections := []Connection{}
	tools := []string{"connections_status"}
	active := []AgentGrant{}
	for _, grant := range grants {
		owner := Owner{TenantID: identity.Agent.OwnerTenantID, ObjectID: identity.Agent.OwnerObjectID}
		if grant.InstanceUID != identity.Agent.UID || !sameOwner(grant.Owner, owner) || !activeGrant(grant, s.currentTime()) {
			continue
		}
		connection, _, err := s.store.getConnectionForAgent(r.Context(), identity.Agent, grant.ConnectionID)
		if err != nil || connection.Deleted || connection.Connection.Status != "connected" || !sameOwner(connection.Connection.Owner, owner) || !s.connectionMatchesAgent(connection.Connection, identity.Agent) || !effectiveGrant(grant, connection.Connection, s.currentTime()) {
			continue
		}
		grant.Tools = allowedTools(grant.Tools, connectionCapabilities(connection.Connection))
		if len(grant.Tools) == 0 {
			continue
		}
		active = append(active, grant)
		connections = append(connections, projectedConnection(connection.Connection))
		tools = append(tools, grant.Tools...)
	}
	writeJSON(w, http.StatusOK, map[string]any{"instanceUid": identity.Agent.UID, "connections": dedupeConnections(connections), "grants": active, "tools": uniqueStrings(tools), "catalogVersion": catalogVersion})
}

func (s *Server) toolAuthorizationsCheck(w http.ResponseWriter, r *http.Request) {
	identity, connection, grant, _, err := s.toolRequest(r, false)
	if err != nil {
		writeToolAuthorizationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"instanceUid": identity.Agent.UID, "connection": projectedConnection(connection.Connection), "grant": grant, "catalogVersion": catalogVersion})
}

func (s *Server) toolCredentialsIssue(w http.ResponseWriter, r *http.Request) {
	identity, connection, grant, input, err := s.toolRequest(r, true)
	if err != nil {
		writeToolAuthorizationError(w, r, err)
		return
	}
	token, expiresAt, err := s.issueToolCredential(r.Context(), connection, grant, input.Tool, identity.Agent)
	if err != nil {
		if connectionMode(connection.Connection) == modeAgent {
			writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Agent credential acquisition failed; personal sign-in is not used", correlationID(r))
			return
		}
		writeAPIError(w, http.StatusUnauthorized, "interaction_required", "Reconnect the provider account in the CetiRun Portal", correlationID(r))
		return
	}
	currentIdentity, current, currentGrant, _, err := s.checkToolAuthorization(r.Context(), input, true)
	if err != nil {
		var authorizationError toolAuthorizationError
		if errors.As(err, &authorizationError) && authorizationError.code == "dependency_unavailable" {
			writeToolAuthorizationError(w, r, err)
			return
		}
		writeAPIError(w, http.StatusConflict, "authorization_changed", "Authorization changed; recheck before issuing", correlationID(r))
		return
	}
	if !reflect.DeepEqual(identity, currentIdentity) || !reflect.DeepEqual(connection.Connection, current.Connection) || !reflect.DeepEqual(grant, currentGrant) || !s.currentTime().Before(expiresAt) {
		writeAPIError(w, http.StatusConflict, "authorization_changed", "Authorization changed; recheck before issuing", correlationID(r))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"instanceUid": currentIdentity.Agent.UID, "connection": projectedConnection(current.Connection), "grant": currentGrant, "catalogVersion": catalogVersion, "accessToken": token, "expiresAt": expiresAt})
}

func (s *Server) issueToolCredential(ctx context.Context, connection *connectionRecord, grant AgentGrant, tool string, agent Agent) (string, time.Time, error) {
	if !s.connectionMatchesAgent(connection.Connection, agent) {
		return "", time.Time{}, errors.New("identity binding changed")
	}
	if connectionMode(connection.Connection) == modeAgent {
		return s.issueAgentCredential(ctx, connection.Connection, agent)
	}
	if connection.Credential == nil {
		return "", time.Time{}, errors.New("credential is unavailable")
	}
	credential := *connection.Credential
	if credential.NonExpiring && (connection.Connection.Provider != "github" || !credential.ExpiresAt.IsZero()) {
		return "", time.Time{}, errors.New("provider credential has an invalid non-expiring marker")
	}
	if !credential.NonExpiring && credential.ExpiresAt.IsZero() {
		return "", time.Time{}, errors.New("provider credential is missing an expiry")
	}
	if !credential.NonExpiring && time.Until(credential.ExpiresAt) < 2*time.Minute {
		provider := s.providers
		if provider == nil {
			provider = newProviderClient(s.cfg)
		}
		refreshed, err := provider.refresh(ctx, connection.Connection.Provider, credential)
		if err != nil {
			return "", time.Time{}, err
		}
		latest, version, err := s.store.getConnection(ctx, connection.Connection.ID)
		if err != nil || latest.Deleted || latest.Connection.Revision != connection.Connection.Revision {
			return "", time.Time{}, errors.New("connection changed")
		}
		latest.Credential = &refreshed
		if err := s.store.putConnection(ctx, latest, version); err != nil {
			return "", time.Time{}, err
		}
		credential = refreshed
	}
	if !credential.NonExpiring && time.Now().After(credential.ExpiresAt) {
		return "", time.Time{}, errors.New("provider credential expired")
	}
	if connection.Connection.Provider == "azure" || connection.Connection.Provider == "microsoft" {
		if !agent.AgentIdentityReady || agent.AgentIdentityClientID == "" {
			return "", time.Time{}, errors.New("Agent Identity is not ready")
		}
		service := "MicrosoftGraph"
		if providerForTool(tool) == "azure" {
			service = "AzureResourceManager"
		}
		header, err := s.authSDK.authorizationHeaderFor(ctx, credential.AccessToken, agent.AgentIdentityClientID, service)
		if err != nil {
			return "", time.Time{}, err
		}
		token, err := accessTokenFromAuthorizationHeader(header)
		if err != nil {
			return "", time.Time{}, err
		}
		expiresAt, err := jwtExpiry(token)
		if err != nil {
			return "", time.Time{}, err
		}
		if !microsoftTokenAllowsTool(token, tool) {
			return "", time.Time{}, errors.New("downstream token does not include the required scope")
		}
		return token, expiresAt, nil
	}
	if connection.Connection.Provider == "github" {
		// GitHub OAuth Apps may issue non-expiring user tokens. This is CetiRun's
		// internal MCP issuance validity, not an upstream token expiry: the Agent
		// runtime never receives this access token. Keep it short so MCP returns
		// for grant and revocation checks.
		expiresAt := s.currentTime().UTC().Add(5 * time.Minute)
		if !credential.NonExpiring && credential.ExpiresAt.Before(expiresAt) {
			expiresAt = credential.ExpiresAt
		}
		if grant.ExpiresAt.Before(expiresAt) {
			expiresAt = grant.ExpiresAt
		}
		if !expiresAt.After(s.currentTime()) {
			return "", time.Time{}, errors.New("provider credential is expiring")
		}
		return credential.AccessToken, expiresAt, nil
	}
	return credential.AccessToken, credential.ExpiresAt, nil
}

func validateToolArguments(tool string, arguments map[string]any, constraints Constraints) error {
	if tool == "azure_subscriptions_list" && len(constraints.SubscriptionIDs) == 0 {
		return errors.New("subscription constraints are required")
	}
	if providerForTool(tool) == "azure" && tool != "azure_subscriptions_list" {
		subscription := stringArgument(arguments, "subscription_id")
		if subscription == "" {
			subscription = subscriptionFromResourceID(stringArgument(arguments, "resource_id"))
		}
		if !contains(constraints.SubscriptionIDs, subscription) {
			return errors.New("subscription is not allowed")
		}
	}
	if azureWriteTool(tool) && !containsFold(constraints.ResourceGroupNames, stringArgument(arguments, "resource_group_name")) {
		return errors.New("resource group is not allowed")
	}
	if providerForTool(tool) == "github" {
		if !contains(constraints.RepositoryIDs, stringArgument(arguments, "repository_id")) {
			return errors.New("repository is not allowed")
		}
		if tool == "github_pull_requests_create" && !contains(constraints.BaseBranches, stringArgument(arguments, "base_branch")) {
			return errors.New("base branch is not allowed")
		}
	}
	if strings.HasPrefix(tool, "calendar_") {
		if !contains(constraints.CalendarIDs, stringArgument(arguments, "calendar_id")) {
			return errors.New("calendar is not allowed")
		}
		if tool == "calendar_events_create" && !constraints.AllowInvites && hasInvitees(arguments) {
			return errors.New("invites are not allowed")
		}
	}
	if tool == "mail_send" {
		for _, address := range recipientAddresses(arguments) {
			if !contains(constraints.RecipientAddresses, address) && !contains(constraints.RecipientDomains, domainOf(address)) {
				return errors.New("recipient is not allowed")
			}
		}
	}
	return nil
}

func writeToolAuthorizationError(w http.ResponseWriter, r *http.Request, err error) {
	var authorizationError toolAuthorizationError
	if errors.As(err, &authorizationError) {
		writeAPIError(w, authorizationError.status, authorizationError.code, "Tool authorization was denied", correlationID(r))
		return
	}
	writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Tool authorization could not be evaluated", correlationID(r))
}
func decodeJSONDiscard(r *http.Request, target any) bool {
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxRequestBody))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil && errors.Is(decoder.Decode(&struct{}{}), io.EOF)
}

func opaqueID() string {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		panic("cryptographic random source failed")
	}
	return hex.EncodeToString(data)
}

func grantKey(instanceUID, connectionID string) string {
	return "grant-" + stateHash(instanceUID + "\x00" + connectionID)[:40]
}
func validProvider(provider string) bool {
	return provider == "azure" || provider == "microsoft" || provider == "github" || provider == "google"
}
func toolMatchesProvider(provider, tool string) bool {
	expected := providerForTool(tool)
	return expected == provider || (expected == "messaging" && (provider == "microsoft" || provider == "google"))
}
func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
func containsFold(values []string, expected string) bool {
	for _, value := range values {
		if strings.EqualFold(value, expected) {
			return true
		}
	}
	return false
}
func uniqueStrings(values []string) []string {
	result := []string{}
	for _, value := range values {
		if value != "" && !contains(result, value) {
			result = append(result, value)
		}
	}
	return result
}
func allowedTools(tools, capabilities []string) []string {
	allowed := make([]string, 0, len(tools))
	for _, tool := range tools {
		if contains(capabilities, tool) {
			allowed = append(allowed, tool)
		}
	}
	return allowed
}
func hasTool(tools []string, tool string) bool { return contains(tools, tool) }
func hasPrefixTool(tools []string, prefix string) bool {
	for _, tool := range tools {
		if strings.HasPrefix(tool, prefix) {
			return true
		}
	}
	return false
}
func azureWriteTool(tool string) bool {
	return tool == "azure_resource_groups_create" || tool == "azure_resource_group_tags_update"
}
func hasAzureWriteTool(tools []string) bool {
	for _, tool := range tools {
		if azureWriteTool(tool) {
			return true
		}
	}
	return false
}
func safeResourceGroupName(name string) bool {
	if len(name) == 0 || len(name) > 90 || strings.Contains(name, "..") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, "/") {
		return false
	}
	for index := 0; index < len(name); index++ {
		character := name[index]
		alphaNumeric := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
		if index == 0 {
			if !alphaNumeric {
				return false
			}
			continue
		}
		if !alphaNumeric && !strings.ContainsRune("_.()-", rune(character)) {
			return false
		}
	}
	return true
}
func ifMatchRevision(r *http.Request) (uint64, bool) {
	value := strings.Trim(r.Header.Get("If-Match"), `"`)
	revision, err := strconv.ParseUint(value, 10, 64)
	return revision, err == nil && revision > 0
}
func stringArgument(arguments map[string]any, name string) string {
	value, _ := arguments[name].(string)
	return value
}
func subscriptionFromResourceID(resourceID string) string {
	parts := strings.Split(strings.Trim(resourceID, "/"), "/")
	if len(parts) >= 2 && strings.EqualFold(parts[0], "subscriptions") {
		return parts[1]
	}
	return ""
}
func domainOf(address string) string {
	_, domain, ok := strings.Cut(strings.ToLower(strings.TrimSpace(address)), "@")
	if !ok || domain == "" {
		return ""
	}
	return domain
}
func recipientAddresses(arguments map[string]any) []string {
	recipients := []string{}
	for _, field := range []string{"to", "cc", "bcc"} {
		switch value := arguments[field].(type) {
		case string:
			recipients = append(recipients, strings.ToLower(strings.TrimSpace(value)))
		case []any:
			for _, entry := range value {
				if address, ok := entry.(string); ok {
					recipients = append(recipients, strings.ToLower(strings.TrimSpace(address)))
				}
			}
		}
	}
	return recipients
}
func hasInvitees(arguments map[string]any) bool {
	return len(recipientAddresses(map[string]any{"to": arguments["attendees"]})) > 0
}
func dedupeConnections(connections []Connection) []Connection {
	result := []Connection{}
	seen := map[string]bool{}
	for _, connection := range connections {
		if !seen[connection.ID] {
			seen[connection.ID] = true
			result = append(result, connection)
		}
	}
	return result
}

func jwtExpiry(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, errors.New("downstream token has no verifiable expiry")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, errors.New("downstream token has invalid expiry")
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Exp <= time.Now().Unix() {
		return time.Time{}, errors.New("downstream token is expired")
	}
	return time.Unix(claims.Exp, 0).UTC(), nil
}
