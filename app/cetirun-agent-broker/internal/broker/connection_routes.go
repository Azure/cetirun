// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import "net/http"

func (s *Server) TLSHandler() http.Handler {
	return requestSecurityHeaders(s.auditManagement(s.tlsMux))
}

func (s *Server) portalOnly(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !portalPrincipalAllowed(r.Header.Get("X-Forwarded-Client-Cert"), s.cfg) {
			writeAPIError(w, http.StatusForbidden, "forbidden", "Verified Portal workload identity is required", correlationID(r))
			return
		}
		if owner, err := ownerFromPortal(r); err == nil {
			auditOwner(r, owner)
		}
		next(w, r)
	})
}

func (s *Server) toolOnly(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !toolServiceTLSPrincipalAllowed(r, s.cfg) {
			writeAPIError(w, http.StatusForbidden, "permission_denied", "Verified tool service TLS identity is required", correlationID(r))
			return
		}
		next(w, r)
	})
}

func portalPrincipalAllowed(xfcc string, cfg Config) bool {
	principal, err := workloadPrincipal(xfcc)
	return err == nil && principal == [2]string{cfg.PortalNamespace, cfg.PortalServiceAccount}
}

func (s *Server) connectionRoutes() {
	s.tlsMux = http.NewServeMux()
	if !s.cfg.ConnectionBindingsEnabled {
		return
	}
	s.mux.Handle("GET /v1/connections", s.portalOnly(s.connectionsList))
	s.mux.Handle("GET /v1/connections/{id}", s.portalOnly(s.connectionGet))
	s.mux.Handle("POST /v1/instances/{name}/tool-identities", s.portalOnly(s.agentIdentityCreate))
	s.mux.Handle("GET /v1/instances/{name}/connection-bindings", s.portalOnly(s.bindingsList))
	s.mux.Handle("POST /v1/instances/{name}/connection-bindings", s.portalOnly(s.bindingCreate))
	s.mux.Handle("PUT /v1/instances/{name}/connection-bindings/{id}", s.portalOnly(s.bindingUpdate))
	s.mux.Handle("DELETE /v1/instances/{name}/connection-bindings/{id}", s.portalOnly(s.bindingDelete))
	s.tlsMux.Handle("POST /internal/v1/service-authorizations/list", s.toolOnly(s.toolAuthorizationsList))
	s.tlsMux.Handle("POST /internal/v1/service-authorizations/check", s.toolOnly(s.toolAuthorizationsCheck))
	s.tlsMux.Handle("POST /internal/v1/service-credentials/issue", s.toolOnly(s.toolCredentialsIssue))
}
