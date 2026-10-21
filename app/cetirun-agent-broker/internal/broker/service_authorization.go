// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/CetiRun/app/internal/agenttools"
)

type serviceRequest = agenttools.Request

func connectionView(c Connection) agenttools.Connection {
	c = projectedConnection(c)
	var identity *agenttools.IdentityBinding
	if c.Identity != nil {
		identity = &agenttools.IdentityBinding{InstanceUID: c.Identity.InstanceUID, TenantID: c.Identity.TenantID, PrincipalID: c.Identity.PrincipalID, ClientID: c.Identity.ClientID}
	}
	return agenttools.Connection{ID: c.ID, Mode: connectionMode(c), Identity: identity, Provider: c.Provider, AccountDisplay: c.AccountDisplay, Services: c.Services, Scopes: c.Scopes, Revision: c.Revision, Status: c.Status, CreatedAt: c.CreatedAt}
}

func bindingView(b ConnectionBinding) agenttools.Binding {
	return agenttools.Binding{ID: b.ID, InstanceUID: b.InstanceUID, ConnectionID: b.ConnectionID, Services: b.Services, Enabled: b.Enabled && !b.Revoked, Revision: b.Revision}
}

func serviceAuthorization(identity workloadIdentity, connection *connectionRecord, binding ConnectionBinding, service string) agenttools.CheckResponse {
	return agenttools.CheckResponse{ExecutorAccountID: connection.Connection.AccountID, InstanceUID: identity.Agent.UID, Connection: connectionView(connection.Connection), Binding: bindingView(binding), Service: service, ContractVersion: agenttools.ContractVersion}
}

func (s *Server) checkServiceAuthorization(ctx context.Context, input serviceRequest, requireVersions bool) (workloadIdentity, *connectionRecord, ConnectionBinding, error) {
	fail := func(code string, status int) (workloadIdentity, *connectionRecord, ConnectionBinding, error) {
		return workloadIdentity{}, nil, ConnectionBinding{}, toolAuthorizationFailure(code, status)
	}
	identity, err := s.store.verifyWorkloadProof(ctx, input.CallerProof, s.cfg.WorkloadTokenAudience)
	if err != nil {
		if errors.Is(err, errDependencyUnavailable) {
			return fail("dependency_unavailable", 503)
		}
		return fail("unauthenticated", 401)
	}
	if input.ContractVersion != agenttools.ContractVersion {
		return fail("authorization_changed", 409)
	}
	service, ok := agenttools.LookupService(input.Service)
	if !ok || input.ConnectionID == "" {
		return fail("invalid_request", 400)
	}
	owner := Owner{identity.Agent.OwnerTenantID, identity.Agent.OwnerObjectID}
	// Require the encrypted binding for this exact Agent before selecting
	// the connection used to acquire a provider credential.
	record, _, err := s.store.getBinding(ctx, bindingKey(identity.Agent.UID, input.ConnectionID), identity.Agent)
	if errors.Is(err, errNotFound) {
		return fail("binding_required", 403)
	}
	if err != nil {
		return fail("dependency_unavailable", 503)
	}
	binding := record.Binding
	if !activeBinding(binding, s.currentTime()) || binding.InstanceUID != identity.Agent.UID || binding.ConnectionID != input.ConnectionID || !sameOwner(binding.Owner, owner) || !contains(binding.Services, input.Service) {
		return fail("permission_denied", 403)
	}
	connection, _, err := s.store.getConnectionForAgent(ctx, identity.Agent, input.ConnectionID)
	if errors.Is(err, errNotFound) {
		return fail("binding_required", 403)
	}
	if err != nil {
		return fail("dependency_unavailable", 503)
	}
	c := connection.Connection
	if !s.cfg.connectionModeEnabled(c) {
		return fail("dependency_unavailable", 503)
	}
	if !sameOwner(c.Owner, owner) || !s.connectionMatchesAgent(c, identity.Agent) || c.Provider != service.Provider || !contains(connectionServices(c), input.Service) {
		return fail("permission_denied", 403)
	}
	if connection.Deleted || c.Status == "disconnected" {
		return fail("binding_required", 403)
	}
	if c.Status != "connected" {
		return fail("interaction_required", 401)
	}
	if requireVersions && (input.BindingRevision == nil || *input.BindingRevision != binding.Revision || input.ConnectionRevision == nil || *input.ConnectionRevision != c.Revision) {
		return fail("authorization_changed", 409)
	}
	return identity, connection, binding, nil
}

func (s *Server) toolAuthorizationsList(w http.ResponseWriter, r *http.Request) {
	var input serviceRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.ContractVersion != agenttools.ContractVersion {
		writeAPIError(w, 409, "authorization_changed", "Service contract changed", correlationID(r))
		return
	}
	identity, err := s.store.verifyWorkloadProof(r.Context(), input.CallerProof, s.cfg.WorkloadTokenAudience)
	if err != nil {
		code, status := "unauthenticated", 401
		if errors.Is(err, errDependencyUnavailable) {
			code, status = "dependency_unavailable", 503
		}
		writeAPIError(w, status, code, "Workload proof could not be verified", correlationID(r))
		return
	}
	owner := Owner{identity.Agent.OwnerTenantID, identity.Agent.OwnerObjectID}
	bindings, err := s.store.listBindings(r.Context(), owner, identity.Agent.UID, identity.Agent.Name)
	if err != nil {
		writeAPIError(w, 503, "dependency_unavailable", "Binding storage is unavailable", correlationID(r))
		return
	}
	result := agenttools.ListResponse{InstanceUID: identity.Agent.UID, Connections: []agenttools.Connection{}, Bindings: []agenttools.Binding{}, ContractVersion: agenttools.ContractVersion}
	for _, binding := range bindings {
		if !activeBinding(binding, s.currentTime()) || binding.InstanceUID != identity.Agent.UID || binding.ID != bindingKey(identity.Agent.UID, binding.ConnectionID) || !sameOwner(binding.Owner, owner) {
			continue
		}
		connection, _, err := s.store.getConnectionForAgent(r.Context(), identity.Agent, binding.ConnectionID)
		if errors.Is(err, errNotFound) {
			continue
		}
		if err != nil {
			writeAPIError(w, 503, "dependency_unavailable", "Connection storage is unavailable", correlationID(r))
			return
		}
		if connection.Deleted || !sameOwner(connection.Connection.Owner, owner) || !s.connectionMatchesAgent(connection.Connection, identity.Agent) {
			continue
		}
		result.Connections = append(result.Connections, connectionView(connection.Connection))
		result.Bindings = append(result.Bindings, bindingView(binding))
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) toolAuthorizationsCheck(w http.ResponseWriter, r *http.Request) {
	var input serviceRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	identity, connection, binding, err := s.checkServiceAuthorization(r.Context(), input, false)
	if err != nil {
		writeToolAuthorizationError(w, r, err)
		return
	}
	auditTarget(r, connection.Connection.ID, identity.Agent.UID, binding.ID)
	writeJSON(w, http.StatusOK, serviceAuthorization(identity, connection, binding, input.Service))
}

func (s *Server) toolCredentialsIssue(w http.ResponseWriter, r *http.Request) {
	var input serviceRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	identity, connection, binding, err := s.checkServiceAuthorization(r.Context(), input, true)
	if err != nil {
		writeToolAuthorizationError(w, r, err)
		return
	}
	auditTarget(r, connection.Connection.ID, identity.Agent.UID, binding.ID)
	token, expiresAt, err := s.issueServiceCredential(r.Context(), connection, input.Service, identity.Agent)
	if err != nil {
		code, status := "dependency_unavailable", 503
		writeAPIError(w, status, code, "Provider credential is unavailable", correlationID(r))
		return
	}
	currentIdentity, current, currentBinding, err := s.checkServiceAuthorization(r.Context(), input, true)
	if err != nil || !reflect.DeepEqual(identity, currentIdentity) || !reflect.DeepEqual(connection.Connection, current.Connection) || !reflect.DeepEqual(binding, currentBinding) || !s.currentTime().Before(expiresAt) {
		writeAPIError(w, 409, "authorization_changed", "Connection or binding changed during credential acquisition", correlationID(r))
		return
	}
	writeJSON(w, http.StatusOK, agenttools.IssueResponse{CheckResponse: serviceAuthorization(currentIdentity, current, currentBinding, input.Service), AccessToken: token, ExpiresAt: expiresAt.UTC().Format(time.RFC3339)})
}

func writeToolAuthorizationError(w http.ResponseWriter, r *http.Request, err error) {
	var typed toolAuthorizationError
	if errors.As(err, &typed) {
		writeAPIError(w, typed.status, typed.code, "Service authorization was denied", correlationID(r))
		return
	}
	writeAPIError(w, 503, "dependency_unavailable", "Service authorization is unavailable", correlationID(r))
}

func bindingKey(uid, connectionID string) string {
	return "binding-" + stateHash(uid + "\x00" + connectionID)[:40]
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
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

func ifMatchRevision(r *http.Request) (uint64, bool) {
	revision, err := strconv.ParseUint(strings.Trim(r.Header.Get("If-Match"), `"`), 10, 64)
	return revision, err == nil && revision > 0
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

func (s *Server) issueServiceCredential(ctx context.Context, connection *connectionRecord, serviceID string, agent Agent) (string, time.Time, error) {
	profile, ok := agenttools.LookupService(serviceID)
	if !ok || profile.Provider != connection.Connection.Provider {
		return "", time.Time{}, errors.New("invalid service")
	}
	return s.issueAgentCredential(ctx, connection.Connection, agent)
}
