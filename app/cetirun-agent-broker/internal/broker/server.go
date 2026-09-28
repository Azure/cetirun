// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const maxRequestBody = 64 << 10
const maxCallbacksPerSecond = 20
const maxCallbackLookupsPerSecond = 40

type Server struct {
	cfg     Config
	store   *kubeClient
	oauth   *oauthClient
	authSDK *authSDKClient
	mux     *http.ServeMux

	callbackMu                sync.Mutex
	callbackWindowStart       time.Time
	callbackCount             int
	callbackLookupWindowStart time.Time
	callbackLookupCount       int
	now                       func() time.Time
}

func NewServer(cfg Config) (*Server, error) {
	wrapper := newKeyVaultWrapper(cfg)
	store, err := newKubeClient(cfg, wrapper)
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg: cfg, store: store, oauth: newOAuthClient(cfg),
		authSDK: newAuthSDKClient(cfg),
		mux:     http.NewServeMux(),
	}
	s.routes()
	return s, nil
}

func (s *Server) Handler() http.Handler {
	return requestSecurityHeaders(s.mux)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	s.mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.authSDK.ready(ctx); err != nil {
			writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Auth SDK is unavailable", correlationID(r))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	s.mux.HandleFunc("GET /internal/v1/instances/{uid}/authorization", s.portalStatus)
	s.mux.HandleFunc("POST /internal/v1/instances/{uid}/authorization/start", s.portalStart)
	s.mux.HandleFunc("DELETE /internal/v1/instances/{uid}/authorization", s.portalEnd)
	s.mux.HandleFunc("POST /internal/v1/authorization/callback", s.callback)
	s.mux.HandleFunc("DELETE /operator/v1/instances/{uid}/authorization", s.operatorDeleteAuthorization)

	s.mux.HandleFunc("GET /v1/auth/status", s.agentStatus)
	s.mux.HandleFunc("POST /v1/auth/start-link", s.agentStartLink)
	s.mux.HandleFunc("POST /v1/credentials/issue", s.issueCredential)
}

func requestSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) portalAgent(r *http.Request) (Agent, error) {
	name := r.Header.Get("X-CetiRun-Instance-Name")
	agent, err := s.store.getAgent(r.Context(), name)
	if err != nil {
		return Agent{}, err
	}
	values := [3][2]string{
		{"uid", r.Header.Get("X-CetiRun-Instance-UID")},
		{"tenant", r.Header.Get("X-CetiRun-Owner-Tenant-ID")},
		{"owner", r.Header.Get("X-CetiRun-Owner-Object-ID")},
	}
	expected := [3]string{agent.UID, agent.OwnerTenantID, agent.OwnerObjectID}
	for index, pair := range values {
		matches := pair[1] != "" && subtle.ConstantTimeCompare([]byte(pair[1]), []byte(expected[index])) == 1
		if index > 0 {
			matches = sameIdentityID(pair[1], expected[index])
		}
		if !matches {
			return Agent{}, fmt.Errorf("%s binding does not match", pair[0])
		}
	}
	if r.PathValue("uid") != "" && subtle.ConstantTimeCompare([]byte(r.PathValue("uid")), []byte(agent.UID)) != 1 {
		return Agent{}, errors.New("instance UID does not match")
	}
	return agent, nil
}

func (s *Server) portalStatus(w http.ResponseWriter, r *http.Request) {
	correlationID := correlationID(r)
	agent, err := s.portalAgent(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "forbidden", "The instance owner binding is invalid", correlationID)
		return
	}
	record, _, err := s.store.getRecord(r.Context(), agent.UID)
	if errors.Is(err, errNotFound) {
		writeJSON(w, http.StatusOK, statusResponse{Status: "disconnected", CorrelationID: correlationID})
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Authorization state is unavailable", correlationID)
		return
	}
	if !recordMatchesAgent(record, agent) {
		writeAPIError(w, http.StatusConflict, "owner_binding_changed", "The instance identity changed; end the old connection first", correlationID)
		return
	}
	status := "disconnected"
	if record.Transaction != nil && !record.Transaction.Consumed && time.Now().Before(record.Transaction.ExpiresAt) {
		status = "connecting"
	}
	if !record.Revoked && record.Tokens != nil && record.Tokens.RefreshToken != "" {
		status = "connected"
	}
	writeJSON(w, http.StatusOK, statusResponse{Status: status, Account: record.AccountDisplay, LastRenewed: record.LastRenewed, LastUsed: record.LastUsed, CorrelationID: correlationID})
}

func (s *Server) portalStart(w http.ResponseWriter, r *http.Request) {
	correlationID := correlationID(r)
	agent, err := s.portalAgent(r)
	if err != nil || !agent.AgentIdentityReady || !uuidPattern.MatchString(agent.AgentIdentityClientID) {
		writeAPIError(w, http.StatusForbidden, "forbidden", "The instance owner binding is invalid", correlationID)
		return
	}
	state, verifier, nonce, err := authorizationRandomValues(randomValue)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "dependency_unavailable", "Authorization could not be started", correlationID)
		return
	}
	record, version, getErr := s.store.getRecord(r.Context(), agent.UID)
	if getErr != nil && !errors.Is(getErr, errNotFound) {
		writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Authorization state is unavailable", correlationID)
		return
	}
	if record == nil {
		record = &AuthorizationRecord{InstanceUID: agent.UID, InstanceName: agent.Name, OwnerTenantID: agent.OwnerTenantID, OwnerObjectID: agent.OwnerObjectID, AgentIdentityClientID: agent.AgentIdentityClientID, Generation: 1}
	}
	if !sameIdentityID(record.OwnerTenantID, agent.OwnerTenantID) ||
		!sameIdentityID(record.OwnerObjectID, agent.OwnerObjectID) ||
		!sameIdentityID(record.AgentIdentityClientID, agent.AgentIdentityClientID) {
		writeAPIError(w, http.StatusConflict, "owner_binding_changed", "The instance identity changed; end the old connection first", correlationID)
		return
	}
	// Re-read the Agent immediately before the CAS write. The Operator first
	// persists phase=Deleting and only then revokes this record, so a Start that
	// began against an old Ready object cannot resurrect a deletion tombstone.
	currentAgent, err := s.store.getAgent(r.Context(), agent.Name)
	if err != nil || !currentAgent.AgentIdentityReady || !recordMatchesAgent(record, currentAgent) {
		writeAPIError(w, http.StatusConflict, "agent_identity_changed", "The instance identity is no longer ready", correlationID)
		return
	}
	record.Revoked = false
	record.Transaction = &Transaction{StateHash: stateHash(state), CodeVerifier: verifier, Nonce: nonce, ExpiresAt: time.Now().Add(s.cfg.TransactionTTL)}
	if err := s.store.putRecord(r.Context(), record, version); err != nil {
		writeAPIError(w, http.StatusConflict, "conflict", "Authorization state changed; retry the request", correlationID)
		return
	}
	loginHint := strings.TrimSpace(r.Header.Get("X-CetiRun-Owner-Login-Hint"))
	writeJSON(w, http.StatusOK, map[string]any{"status": "connecting", "authorizationUrl": s.oauth.authorizationURL(state, nonce, pkceChallenge(verifier), loginHint), "expiresAt": record.Transaction.ExpiresAt, "correlationId": correlationID})
}

func (s *Server) portalEnd(w http.ResponseWriter, r *http.Request) {
	correlationID := correlationID(r)
	agent, err := s.portalAgent(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "forbidden", "The instance owner binding is invalid", correlationID)
		return
	}
	// Always persist a generation tombstone, including when no record exists
	// yet. This gives End and an overlapping Start/callback one shared CAS
	// linearization point instead of allowing the other request to recreate a
	// connection immediately after End observed a missing Secret.
	if err := s.store.revokeRecord(r.Context(), agent); err != nil {
		status := http.StatusServiceUnavailable
		code, message := "dependency_unavailable", "Authorization could not be ended"
		if errors.Is(err, errConflict) {
			status, code, message = http.StatusConflict, "conflict", "Authorization state changed; retry the request"
		}
		writeAPIError(w, status, code, message, correlationID)
		return
	}
	writeJSON(w, http.StatusOK, statusResponse{Status: "disconnected", CorrelationID: correlationID})
}

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	if err := r.ParseForm(); err != nil {
		s.callbackFailure(w, r, "parse_form", err)
		return
	}
	state, code := r.Form.Get("state"), r.Form.Get("code")
	if !validOAuthState(state) || !validAuthorizationCode(code) || r.Form.Get("error") != "" {
		s.callbackFailure(w, r, "validate_response", nil)
		return
	}
	// This callback is publicly reachable through the Istio bypass. Limit the
	// Secret label lookup before it can be amplified by unauthenticated input.
	// Valid transactions have a separate, stricter admission budget below.
	if !s.allowCallbackLookup(s.currentTime()) {
		slog.Warn("Agent authorization callback failed", "correlationId", correlationID(r), "stage", "lookup_rate_limit")
		w.Header().Set("Retry-After", "1")
		http.Error(w, "Too many authorization callbacks", http.StatusTooManyRequests)
		return
	}
	record, version, err := s.store.findRecordByState(r.Context(), state)
	if err != nil {
		s.callbackFailure(w, r, "find_transaction", err)
		return
	}
	// Only a state that matches an outstanding server-side transaction may use
	// the stricter callback capacity. The coarser lookup limit above protects the
	// Kubernetes API without allowing arbitrary states to consume this budget.
	callbackStateHash := stateHash(state)
	valid, admitted := s.callbackAdmission(record, callbackStateHash, s.currentTime())
	if !valid {
		s.callbackFailure(w, r, "validate_transaction", nil)
		return
	}
	if !admitted {
		slog.Warn("Agent authorization callback failed", "correlationId", correlationID(r), "stage", "rate_limit")
		w.Header().Set("Retry-After", "1")
		http.Error(w, "Too many authorization callbacks", http.StatusTooManyRequests)
		return
	}
	verifier, nonce := record.Transaction.CodeVerifier, record.Transaction.Nonce
	record.Transaction.Consumed = true
	if err := s.store.putRecord(r.Context(), record, version); err != nil {
		s.callbackFailure(w, r, "consume_transaction", err)
		return
	}
	tokens, err := s.oauth.exchangeCode(r.Context(), code, verifier)
	if err != nil {
		s.callbackFailure(w, r, "exchange_code", err)
		return
	}
	if tokens.RefreshToken == "" || tokens.IDToken == "" {
		s.callbackFailure(w, r, "validate_token_response", nil)
		return
	}
	account, err := s.oauth.verifier.verifyIDToken(r.Context(), tokens.IDToken, nonce, record.OwnerTenantID, record.OwnerObjectID)
	if err != nil {
		s.callbackFailure(w, r, "validate_id_token", err)
		return
	}
	if err := s.authSDK.validate(r.Context(), tokens.AccessToken, record.OwnerTenantID, s.cfg.BlueprintClientID, s.cfg.AuthorizationClientID, record.OwnerObjectID); err != nil {
		s.callbackFailure(w, r, "validate_access_token", err)
		return
	}
	current, currentVersion, err := s.store.getRecord(r.Context(), record.InstanceUID)
	if err != nil {
		s.callbackFailure(w, r, "reload_transaction", err)
		return
	}
	if !callbackCanPersist(record, current, callbackStateHash) {
		s.callbackFailure(w, r, "validate_consumed_transaction", nil)
		return
	}
	agent, err := s.store.getAgent(r.Context(), current.InstanceName)
	if err != nil {
		s.callbackFailure(w, r, "load_agent", err)
		return
	}
	if !agent.AgentIdentityReady || !recordMatchesAgent(current, agent) {
		s.callbackFailure(w, r, "validate_agent_binding", nil)
		return
	}
	now := time.Now().UTC()
	current.Tokens = &TokenCache{AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, ExpiresAt: now.Add(time.Duration(tokens.ExpiresIn) * time.Second)}
	current.AccountDisplay = account
	current.LastRenewed = &now
	current.Transaction = nil
	if err := s.store.putRecord(r.Context(), current, currentVersion); err != nil {
		s.callbackFailure(w, r, "persist_tokens", err)
		return
	}
	s.callbackPage(w, true)
}

func (s *Server) callbackFailure(w http.ResponseWriter, r *http.Request, stage string, err error) {
	attributes := []any{"correlationId", correlationID(r), "stage", stage}
	status := "failed"
	message := "Sign-in could not be completed. Close this window and retry from the Portal."
	if err != nil {
		category := fmt.Sprintf("%T", err)
		if failure, ok := authSDKValidationFailureCategory(err); ok {
			category = failure
		} else if failure, ok := oauthTokenFailureCategory(err); ok {
			category = failure
		} else if failure, ok := idTokenFailureCategory(err); ok {
			category = failure
			if failure == string(idTokenOwner) || failure == string(idTokenOwnerMissing) {
				status = "owner_mismatch"
				message = "Sign in with the same Microsoft Entra account used by the Portal, then try again."
			}
		}
		attributes = append(attributes, "category", category)
	}
	slog.Warn("Agent authorization callback failed", attributes...)
	s.callbackPageResult(w, status, message)
}

func (s *Server) callbackPage(w http.ResponseWriter, success bool) {
	status := "failed"
	message := "Sign-in could not be completed. Close this window and retry from the Portal."
	if success {
		status = "complete"
		message = "Sign-in complete. You may close this window."
	}
	s.callbackPageResult(w, status, message)
}

func (s *Server) callbackPageResult(w http.ResponseWriter, status, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'")
	t := template.Must(template.New("callback").Parse(`<!doctype html><html><head><meta charset="utf-8"><title>Microsoft Entra connection</title></head><body><p>{{.Message}}</p><script>if(window.opener){window.opener.postMessage({type:"cetirun-agent-authorization",status:{{.Status}}},{{.Origin}});}window.close();</script></body></html>`))
	_ = t.Execute(w, map[string]string{"Status": status, "Origin": s.cfg.PortalOrigin, "Message": message})
}

func (s *Server) workloadAgent(r *http.Request) (Agent, error) {
	principal, err := workloadPrincipal(r.Header.Get("X-Forwarded-Client-Cert"))
	if err != nil {
		return Agent{}, err
	}
	agent, err := s.store.getAgentByWorkload(r.Context(), principal[0], principal[1])
	if err != nil {
		return Agent{}, err
	}
	if !agent.AgentIdentityReady || !uuidPattern.MatchString(agent.AgentIdentityClientID) {
		return Agent{}, errors.New("Agent Identity is not ready")
	}
	return agent, nil
}

func recordMatchesAgent(record *AuthorizationRecord, agent Agent) bool {
	if record == nil {
		return false
	}
	return record.InstanceUID != "" && subtle.ConstantTimeCompare([]byte(record.InstanceUID), []byte(agent.UID)) == 1 &&
		record.InstanceName != "" && subtle.ConstantTimeCompare([]byte(record.InstanceName), []byte(agent.Name)) == 1 &&
		sameIdentityID(record.OwnerTenantID, agent.OwnerTenantID) &&
		sameIdentityID(record.OwnerObjectID, agent.OwnerObjectID) &&
		sameIdentityID(record.AgentIdentityClientID, agent.AgentIdentityClientID)
}

func sameIdentityID(actual, expected string) bool {
	return actual != "" && expected != "" && strings.EqualFold(actual, expected)
}

func workloadPrincipal(xfcc string) ([2]string, error) {
	var principals [][2]string
	for _, entry := range strings.Split(xfcc, ",") {
		for _, part := range strings.Split(entry, ";") {
			part = strings.TrimSpace(part)
			if !strings.HasPrefix(part, "URI=") {
				continue
			}
			raw := strings.Trim(strings.TrimPrefix(part, "URI="), `"`)
			u, err := url.Parse(raw)
			if err != nil || u.Scheme != "spiffe" || u.Host != "cluster.local" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				continue
			}
			segments := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(segments) == 4 && segments[0] == "ns" && segments[2] == "sa" && dnsLabel(segments[1]) && dnsLabel(segments[3]) {
				principals = append(principals, [2]string{segments[1], segments[3]})
			}
		}
	}
	if len(principals) != 1 {
		return [2]string{}, errors.New("exactly one verified workload principal is required")
	}
	return principals[0], nil
}

func authorizationRandomValues(generate func(int) (string, error)) (string, string, string, error) {
	state, err := generate(32)
	if err != nil {
		return "", "", "", err
	}
	verifier, err := generate(48)
	if err != nil {
		return "", "", "", err
	}
	nonce, err := generate(32)
	if err != nil {
		return "", "", "", err
	}
	return state, verifier, nonce, nil
}

func validOAuthState(value string) bool {
	if len(value) != 43 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func validAuthorizationCode(value string) bool {
	if len(value) == 0 || len(value) > 8192 {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func (s *Server) allowCallback(now time.Time) bool {
	s.callbackMu.Lock()
	defer s.callbackMu.Unlock()
	if s.callbackWindowStart.IsZero() || now.Sub(s.callbackWindowStart) >= time.Second {
		s.callbackWindowStart = now
		s.callbackCount = 0
	}
	if s.callbackCount >= maxCallbacksPerSecond {
		return false
	}
	s.callbackCount++
	return true
}

func (s *Server) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Server) allowCallbackLookup(now time.Time) bool {
	s.callbackMu.Lock()
	defer s.callbackMu.Unlock()
	if s.callbackLookupWindowStart.IsZero() || now.Sub(s.callbackLookupWindowStart) >= time.Second {
		s.callbackLookupWindowStart = now
		s.callbackLookupCount = 0
	}
	if s.callbackLookupCount >= maxCallbackLookupsPerSecond {
		return false
	}
	s.callbackLookupCount++
	return true
}

func (s *Server) callbackAdmission(record *AuthorizationRecord, callbackStateHash string, now time.Time) (bool, bool) {
	if record == nil || record.Transaction == nil || record.Transaction.Consumed || now.After(record.Transaction.ExpiresAt) ||
		subtle.ConstantTimeCompare([]byte(record.Transaction.StateHash), []byte(callbackStateHash)) != 1 {
		return false, false
	}
	return true, s.allowCallback(now)
}

func callbackCanPersist(callbackRecord, current *AuthorizationRecord, callbackStateHash string) bool {
	return callbackRecord != nil && current != nil && current.Generation == callbackRecord.Generation && !current.Revoked &&
		current.Transaction != nil && current.Transaction.Consumed &&
		subtle.ConstantTimeCompare([]byte(current.Transaction.StateHash), []byte(callbackStateHash)) == 1
}

func operatorPrincipalAllowed(xfcc string, cfg Config) bool {
	principal, err := workloadPrincipal(xfcc)
	return err == nil && principal == [2]string{cfg.OperatorNamespace, cfg.OperatorServiceAccount}
}

func (s *Server) operatorDeleteAuthorization(w http.ResponseWriter, r *http.Request) {
	if !operatorPrincipalAllowed(r.Header.Get("X-Forwarded-Client-Cert"), s.cfg) {
		writeAPIError(w, http.StatusForbidden, "forbidden", "Verified operator workload identity is required", correlationID(r))
		return
	}
	agent, err := s.store.getAgentByUID(r.Context(), r.PathValue("uid"))
	if err != nil || agent.UID != r.PathValue("uid") {
		writeAPIError(w, http.StatusNotFound, "instance_not_found", "Instance not found", correlationID(r))
		return
	}
	if err := s.store.revokeRecord(r.Context(), agent); err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Authorization state could not be deleted", correlationID(r))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) agentStatus(w http.ResponseWriter, r *http.Request) {
	correlationID := correlationID(r)
	agent, err := s.workloadAgent(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "forbidden", "Verified instance workload identity is required", correlationID)
		return
	}
	record, _, err := s.store.getRecord(r.Context(), agent.UID)
	if err != nil || !recordMatchesAgent(record, agent) || record.Revoked || record.Tokens == nil {
		writeAPIError(w, http.StatusUnauthorized, "authorization_required", "Connect Microsoft Entra in the CetiRun Portal", correlationID)
		return
	}
	writeJSON(w, http.StatusOK, statusResponse{Status: "connected", Account: record.AccountDisplay, LastUsed: record.LastUsed, CorrelationID: correlationID})
}

func (s *Server) agentStartLink(w http.ResponseWriter, r *http.Request) {
	correlationID := correlationID(r)
	agent, err := s.workloadAgent(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "forbidden", "Verified instance workload identity is required", correlationID)
		return
	}
	// The Portal performs the owner check before it can start OAuth. No token or
	// mutable identity selector is included in this URL.
	link := strings.TrimRight(s.cfg.PortalBaseURL, "/") + "/instances/" + url.PathEscape(agent.Name) + "/overview?connect=entra"
	writeJSON(w, http.StatusOK, map[string]string{"actionUrl": link, "correlationId": correlationID})
}

func (s *Server) issueCredential(w http.ResponseWriter, r *http.Request) {
	correlationID := correlationID(r)
	var input struct {
		Service string `json:"service"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Service != "azure-resource-manager" {
		writeAPIError(w, http.StatusBadRequest, "unsupported_service", "The requested credential service is not supported", correlationID)
		return
	}
	agent, err := s.workloadAgent(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "forbidden", "Verified instance workload identity is required", correlationID)
		return
	}
	record, version, err := s.store.getRecord(r.Context(), agent.UID)
	if err != nil || !recordMatchesAgent(record, agent) || record.Revoked || record.Tokens == nil || record.Tokens.RefreshToken == "" {
		writeAPIError(w, http.StatusUnauthorized, "authorization_required", "Connect Microsoft Entra in the CetiRun Portal", correlationID)
		return
	}
	generation := record.Generation
	if time.Until(record.Tokens.ExpiresAt) < 2*time.Minute {
		tokens, refreshErr := s.oauth.refresh(r.Context(), record.Tokens.RefreshToken)
		if refreshErr != nil {
			writeAPIError(w, http.StatusUnauthorized, "interaction_required", "Reconnect Microsoft Entra in the CetiRun Portal", correlationID)
			return
		}
		if validateErr := s.authSDK.validate(r.Context(), tokens.AccessToken, record.OwnerTenantID, s.cfg.BlueprintClientID, s.cfg.AuthorizationClientID, record.OwnerObjectID); validateErr != nil {
			writeAPIError(w, http.StatusUnauthorized, "interaction_required", "Reconnect Microsoft Entra in the CetiRun Portal", correlationID)
			return
		}
		now := time.Now().UTC()
		record.Tokens.AccessToken, record.Tokens.ExpiresAt = tokens.AccessToken, now.Add(time.Duration(tokens.ExpiresIn)*time.Second)
		if tokens.RefreshToken != "" {
			record.Tokens.RefreshToken = tokens.RefreshToken
		}
		record.LastRenewed = &now
		if err := s.store.putRecord(r.Context(), record, version); err != nil {
			if errors.Is(err, errConflict) {
				latest, _, reloadErr := s.store.getRecord(r.Context(), agent.UID)
				if errors.Is(reloadErr, errNotFound) ||
					(reloadErr == nil && (latest.Revoked || latest.Generation != generation)) {
					writeAPIError(w, http.StatusUnauthorized, "authorization_required", "The connection was ended", correlationID)
					return
				}
				if reloadErr == nil && recordMatchesAgent(latest, agent) &&
					latest.Tokens != nil && latest.Tokens.AccessToken != "" &&
					latest.Tokens.RefreshToken != "" && time.Until(latest.Tokens.ExpiresAt) >= 2*time.Minute {
					// A parallel request already refreshed this live authorization
					// generation. Adopt its cache rather than rejecting normal
					// concurrent tool calls. Revocation and generation changes were
					// rejected above before any downstream credential is requested.
					record = latest
					goto tokenReady
				}
			}
			writeAPIError(w, http.StatusConflict, "conflict", "Authorization state changed; retry", correlationID)
			return
		}
	}

tokenReady:
	if err := s.authSDK.validate(r.Context(), record.Tokens.AccessToken, record.OwnerTenantID, s.cfg.BlueprintClientID, s.cfg.AuthorizationClientID, record.OwnerObjectID); err != nil {
		writeAPIError(w, http.StatusUnauthorized, "interaction_required", "Reconnect Microsoft Entra in the CetiRun Portal", correlationID)
		return
	}
	authorization, err := s.authSDK.authorizationHeader(r.Context(), record.Tokens.AccessToken, agent.AgentIdentityClientID)
	if err != nil {
		s.writeDependencyError(w, err, correlationID)
		return
	}
	token, err := accessTokenFromAuthorizationHeader(authorization)
	if err != nil {
		s.writeDependencyError(w, err, correlationID)
		return
	}
	current, currentVersion, err := s.store.getRecord(r.Context(), agent.UID)
	if err != nil || current.Revoked || current.Generation != generation {
		writeAPIError(w, http.StatusUnauthorized, "authorization_required", "The connection was ended", correlationID)
		return
	}
	now := time.Now().UTC()
	current.LastUsed = &now
	if err := s.store.putRecord(r.Context(), current, currentVersion); err != nil {
		if errors.Is(err, errConflict) {
			latest, _, reloadErr := s.store.getRecord(r.Context(), agent.UID)
			if errors.Is(reloadErr, errNotFound) || (reloadErr == nil && (latest.Revoked || latest.Generation != generation)) {
				writeAPIError(w, http.StatusUnauthorized, "authorization_required", "The connection was ended", correlationID)
				return
			}
			if reloadErr == nil && recordMatchesAgent(latest, agent) {
				// Another request updated the same live authorization generation.
				// Its successful CAS is a sufficient final revocation barrier; the
				// LastUsed timestamp is audit metadata and need not make otherwise
				// valid parallel credential issuance fail.
				goto credentialReady
			}
		}
		writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Authorization state could not be updated", correlationID)
		return
	}

credentialReady:
	// This response deliberately crosses the Broker/workload boundary. The
	// supported CLI must keep the token in process memory and must never print,
	// persist, or place it in model context. Disconnect prevents future issuance
	// but cannot revoke a bearer token that has already crossed this boundary.
	writeJSON(w, http.StatusOK, map[string]string{
		"tokenType":     "Bearer",
		"accessToken":   token,
		"correlationId": correlationID,
	})
}

func accessTokenFromAuthorizationHeader(authorization string) (string, error) {
	const prefix = "Bearer "
	if !strings.HasPrefix(authorization, prefix) {
		return "", errors.New("Auth SDK returned an invalid credential")
	}
	token := strings.TrimPrefix(authorization, prefix)
	if token == "" || len(token) > 32<<10 || strings.ContainsAny(token, " \t\r\n") {
		return "", errors.New("Auth SDK returned an invalid credential")
	}
	return token, nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "The request body is invalid", correlationID(r))
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "The request body must contain one JSON value", correlationID(r))
		return false
	}
	return true
}

func (s *Server) writeDependencyError(w http.ResponseWriter, err error, correlationID string) {
	switch {
	case errors.Is(err, errDownstreamForbidden):
		writeAPIError(w, http.StatusForbidden, "forbidden", "The delegated credential request was denied", correlationID)
	default:
		slog.Warn("Agent Identity dependency failed", "correlationId", correlationID, "category", fmt.Sprintf("%T", err))
		writeAPIError(w, http.StatusServiceUnavailable, "dependency_unavailable", "The Agent Identity dependency is unavailable", correlationID)
	}
}

func correlationID(r *http.Request) string {
	if value := r.Header.Get("X-Correlation-ID"); value != "" && len(value) <= 128 {
		return value
	}
	value, err := randomValue(16)
	if err != nil {
		return "unavailable"
	}
	return value
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeAPIError(w http.ResponseWriter, status int, code, message, correlationID string) {
	value := apiError{}
	value.Error.Code, value.Error.Message, value.Error.CorrelationID = code, message, correlationID
	writeJSON(w, status, value)
}
