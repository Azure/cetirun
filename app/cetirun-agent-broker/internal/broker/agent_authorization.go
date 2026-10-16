// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"time"
)

type toolIdentityRequest struct {
	Provider string `json:"provider"`
}

func identityAuthorizationSource(provider string) string {
	if provider == "azure" {
		return "direct_rbac"
	}
	return ""
}

func connectionMode(c Connection) string {
	if c.Mode == "" {
		return modeDelegated
	}
	return c.Mode
}

func connectionMatchesAgent(c Connection, agent Agent, tenant string) bool {
	if agent.UnsupportedRuntime {
		return false
	}
	switch connectionMode(c) {
	case modeDelegated:
		return c.Identity == nil
	case modeAgent:
		b := c.Identity
		return c.Provider == "azure" && b != nil && !agent.UnsupportedRuntime && agent.AgentIdentityReady &&
			b.InstanceUID == agent.UID && sameIdentityID(b.TenantID, tenant) && sameIdentityID(b.TenantID, agent.OwnerTenantID) &&
			sameIdentityID(b.ClientID, agent.AgentIdentityClientID) && sameIdentityID(b.PrincipalID, agent.AgentIdentityObjectID)
	default:
		return false
	}
}

func effectiveGrant(g AgentGrant, c Connection, now time.Time) bool {
	if !activeGrant(g, now) {
		return false
	}
	if connectionMode(c) == modeDelegated {
		return g.Authorization == nil
	}
	a := g.Authorization
	source := identityAuthorizationSource(c.Provider)
	return connectionMode(c) == modeAgent && source != "" && a != nil && a.State == "verified" && a.Source == source &&
		a.BindingRevision == c.Revision && a.VerifiedAt != nil && !a.VerifiedAt.After(now) && a.ValidUntil != nil && now.Before(*a.ValidUntil)
}

func (s *Server) agentIdentityCreate(w http.ResponseWriter, r *http.Request) {
	correlation := correlationID(r)
	owner, err := ownerFromPortal(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "forbidden", "Verified owner is required", correlation)
		return
	}
	agent, err := s.portalGrantAgent(r, owner)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "forbidden", "Instance owner binding is invalid", correlation)
		return
	}
	var input toolIdentityRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Provider != "azure" {
		writeAPIError(w, http.StatusBadRequest, "unsupported_identity_mode", "Independent identity is not provisioned for this provider", correlation)
		return
	}
	binding := &IdentityBinding{InstanceUID: agent.UID, TenantID: s.cfg.TenantID, ClientID: agent.AgentIdentityClientID, PrincipalID: agent.AgentIdentityObjectID}
	c := Connection{Provider: input.Provider, Mode: modeAgent, Identity: binding}
	if !s.connectionMatchesAgent(c, agent) || !uuidPattern.MatchString(binding.ClientID) || !uuidPattern.MatchString(binding.PrincipalID) {
		writeAPIError(w, http.StatusConflict, "identity_not_ready", "Enable Agent Identity on a supported instance and wait for provisioning", correlation)
		return
	}
	// A deterministic binding key makes concurrent create requests idempotent.
	c.ID = "agent-" + stateHash(agent.UID + "/azure/" + strings.ToLower(binding.TenantID+"/"+binding.PrincipalID+"/"+binding.ClientID))[:40]
	existing, _, err := s.store.getConnectionForAgent(r.Context(), agent, c.ID)
	if err == nil {
		if existing.Deleted || !sameOwner(existing.Connection.Owner, owner) || !s.connectionMatchesAgent(existing.Connection, agent) {
			writeAPIError(w, http.StatusConflict, "identity_disconnected", "This identity binding has been disconnected", correlation)
			return
		}
		writeJSON(w, http.StatusOK, projectedConnection(existing.Connection))
		return
	}
	if !errors.Is(err, errNotFound) {
		writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Identity storage is unavailable", correlation)
		return
	}
	now := s.currentTime().UTC()
	c.Owner, c.Revision, c.Status, c.CreatedAt, c.UpdatedAt = owner, 1, "connected", now, now
	c.AccountDisplay, c.AccountID, c.Capabilities = "Agent "+agent.Name, binding.PrincipalID, connectionCapabilities(c)
	if err := s.store.putConnection(r.Context(), &connectionRecord{Connection: c, InstanceName: agent.Name, InstanceNamespace: agent.Namespace}, ""); err != nil {
		writeAPIError(w, http.StatusConflict, "conflict", "Identity state changed; reload before retrying", correlation)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) grantVerify(w http.ResponseWriter, r *http.Request) {
	correlation := correlationID(r)
	owner, err := ownerFromPortal(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "forbidden", "Verified owner is required", correlation)
		return
	}
	agent, err := s.portalGrantAgent(r, owner)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "forbidden", "Instance binding is invalid", correlation)
		return
	}
	expected, ok := ifMatchRevision(r)
	if !ok {
		writeAPIError(w, http.StatusPreconditionRequired, "revision_required", "If-Match revision is required", correlation)
		return
	}
	record, version, err := s.store.getGrant(r.Context(), r.PathValue("id"), agent)
	if err != nil || record.Grant.InstanceUID != agent.UID || !sameOwner(record.Grant.Owner, owner) || !activeGrant(record.Grant, s.currentTime()) {
		writeAPIError(w, http.StatusNotFound, "not_found", "Active request not found", correlation)
		return
	}
	if record.Grant.Revision != expected {
		writeAPIError(w, http.StatusConflict, "conflict", "Authorization revision changed", correlation)
		return
	}
	connection, _, err := s.store.getConnectionForAgent(r.Context(), agent, record.Grant.ConnectionID)
	if err != nil || connection.Deleted || connection.Connection.Status != "connected" || !sameOwner(connection.Connection.Owner, owner) || connectionMode(connection.Connection) != modeAgent || !s.connectionMatchesAgent(connection.Connection, agent) {
		writeAPIError(w, http.StatusConflict, "identity_changed", "Independent identity binding is unavailable", correlation)
		return
	}
	var evidence *ResourceAuthorization
	if connection.Connection.Provider == "azure" {
		actions, actionsErr := requiredARMActions(record.Grant.Tools)
		if actionsErr != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "Grant contains unsupported Azure tools", correlation)
			return
		}
		evidence = &ResourceAuthorization{State: "pending", Source: "direct_rbac", Reason: "provider_verification_unavailable", BindingRevision: connection.Connection.Revision}
		token, _, tokenErr := s.issueAgentCredential(r.Context(), connection.Connection, agent)
		if tokenErr == nil {
			arm := s.arm
			if arm == nil {
				arm = newARMAuthorizationClient(s.cfg.RequestTimeout)
			}
			scopes, requestIDs, verifyErr := arm.verify(r.Context(), token, connection.Connection.Identity.PrincipalID, record.Grant.Constraints.SubscriptionIDs, actions)
			if verifyErr == nil {
				now, until := s.currentTime().UTC(), record.Grant.ExpiresAt
				evidence.State, evidence.Reason = "verified", ""
				evidence.VerifiedAt, evidence.ValidUntil = &now, &until
				evidence.Scopes, evidence.Actions, evidence.RequestIDs = scopes, actions, requestIDs
			} else if errors.Is(verifyErr, errDownstreamForbidden) {
				evidence.Reason = "resource_owner_grant_required"
			}
		}
	} else {
		writeAPIError(w, http.StatusBadRequest, "unsupported_identity_mode", "Independent identity is not provisioned for this provider", correlation)
		return
	}
	// Recheck platform identity and binding after the external request. CAS also
	// prevents a concurrent edit/revoke from being overwritten by verification.
	currentAgent, agentErr := s.portalGrantAgent(r, owner)
	current, _, connectionErr := s.store.getConnectionForAgent(r.Context(), agent, connection.Connection.ID)
	if agentErr != nil || connectionErr != nil || !reflect.DeepEqual(agent, currentAgent) || current.Deleted || !reflect.DeepEqual(current.Connection, connection.Connection) || !activeGrant(record.Grant, s.currentTime()) {
		writeAPIError(w, http.StatusConflict, "authorization_changed", "Identity or authorization changed during verification", correlation)
		return
	}
	record.Grant.Authorization = evidence
	record.Grant.Revision++
	record.Grant.UpdatedAt = s.currentTime().UTC()
	record.InstanceName, record.InstanceNamespace = agent.Name, agent.Namespace
	if err := s.store.putGrant(r.Context(), record, version); err != nil {
		writeAPIError(w, http.StatusConflict, "conflict", "Authorization changed during verification", correlation)
		return
	}
	writeJSON(w, http.StatusOK, record.Grant)
}

func (s *Server) issueAgentCredential(ctx context.Context, connection Connection, agent Agent) (string, time.Time, error) {
	if connection.Provider != "azure" || connectionMode(connection) != modeAgent || !s.connectionMatchesAgent(connection, agent) {
		return "", time.Time{}, errors.New("independent identity binding is invalid")
	}
	client := s.oauth
	if client == nil {
		client = newOAuthClient(s.cfg)
	}
	started := s.currentTime()
	token, err := client.agentToken(ctx, connection.Identity.ClientID)
	if err != nil {
		return "", time.Time{}, err
	}
	// ExpiresIn is trusted token-endpoint metadata; access tokens remain opaque.
	expires := started.Add(time.Duration(token.ExpiresIn)*time.Second - 30*time.Second)
	if !expires.After(s.currentTime()) {
		return "", time.Time{}, errors.New("Agent credential is already expiring")
	}
	return token.AccessToken, expires, nil
}

// Deployment qualification is independent of the provider identity mode.
func (s *Server) connectionMatchesAgent(c Connection, agent Agent) bool {
	if s.cfg.ToolRuntimeTypes != nil && !contains(s.cfg.ToolRuntimeTypes, agent.RuntimeType) {
		return false
	}
	return connectionMatchesAgent(c, agent, s.cfg.TenantID)
}
