// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"crypto/subtle"
	"errors"
	"net/http"

	"github.com/Azure/CetiRun/app/internal/agenttools"
)

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

func (s *Server) portalBindingAgent(r *http.Request, owner Owner) (Agent, error) {
	agent, err := s.store.getServiceAgent(r.Context(), r.PathValue("name"))
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
	writeJSON(w, http.StatusOK, projectedConnection(record.Connection))
}

func connectionServices(connection Connection) []string {
	return agenttools.ProviderServices(connection.Provider)
}

func projectedConnection(connection Connection) Connection {
	connection.Mode = connectionMode(connection)
	connection.Services = connectionServices(connection)
	if connection.Scopes == nil {
		connection.Scopes = []string{}
	}
	return connection
}
