// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/auth"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
)

const (
	maxToolBrokerResponseBytes = 1 << 20
	maxToolRequestBytes        = 64 << 10
	toolCatalogVersion         = "2026-10-16"
	maxGrantLifetime           = 90 * 24 * time.Hour
	githubBindingLifetime      = 10 * time.Minute
	githubBindingCookiePrefix  = "__Host-cetirun-github-oauth-"
	connectionBindingHeader    = "X-CetiRun-Connection-Binding"
)

// ToolConnectionsHandler exposes owner-scoped connection and grant management.
// It never receives or returns provider credentials; it only proxies bounded,
// validated control-plane requests to the protected Broker boundary.
type ToolConnectionsHandler struct {
	Kube          *kube.Client
	BrokerBaseURL string
	PortalOrigin  string
	HTTPClient    *http.Client
	Now           func() time.Time
	RuntimeTypes  []string
}

func NewToolConnectionsHandler(kubeClient *kube.Client, brokerBaseURL, portalOrigin string) *ToolConnectionsHandler {
	return &ToolConnectionsHandler{
		Kube:          kubeClient,
		BrokerBaseURL: strings.TrimRight(brokerBaseURL, "/"),
		PortalOrigin:  strings.TrimRight(portalOrigin, "/"),
		HTTPClient:    &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		Now:           time.Now,
	}
}

func (h *ToolConnectionsHandler) ListConnections(w http.ResponseWriter, r *http.Request) {
	headers, ok := h.ownerHeaders(w, r)
	if !ok {
		return
	}
	h.forward(w, r, http.MethodGet, "/v1/connections", headers, nil)
}

func (h *ToolConnectionsHandler) ConnectionCallback(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxToolRequestBytes)
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Invalid connection callback")
		return
	}
	form := url.Values{}
	for _, field := range []string{"state", "code", "error"} {
		if values := r.Form[field]; len(values) == 1 && len(values[0]) <= 8192 {
			form.Set(field, values[0])
		}
	}
	state := form.Get("state")
	cookieName, validState := githubBindingCookieName(state)
	binding := ""
	if validState {
		if cookie, err := r.Cookie(cookieName); err == nil && validConnectionBinding(cookie.Value) {
			binding = cookie.Value
		}
		clearGitHubBindingCookie(w, cookieName)
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, h.BrokerBaseURL+"/api/v1/connections/callback", strings.NewReader(form.Encode()))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Connection callback unavailable")
		return
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if binding != "" {
		request.Header.Set(connectionBindingHeader, binding)
	}
	response, err := h.HTTPClient.Do(request)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Connection callback unavailable")
		return
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxToolBrokerResponseBytes+1))
	if err != nil || len(payload) > maxToolBrokerResponseBytes || response.StatusCode >= 300 {
		writeError(w, http.StatusBadGateway, "dependency_invalid", "Invalid connection callback response")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(payload)
}

func (h *ToolConnectionsHandler) StartConnection(w http.ResponseWriter, r *http.Request) {
	headers, ok := h.writeOwnerHeaders(w, r)
	if !ok {
		return
	}
	body, provider, ok := readConnectionStart(w, r)
	if !ok {
		return
	}
	binding := ""
	if provider == "github" {
		var err error
		binding, err = newConnectionBinding()
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Connection setup unavailable")
			return
		}
		headers[connectionBindingHeader] = binding
	}
	h.forwardConnectionAuthorization(w, r, "/v1/connections/start", headers, body, binding, true)
}

func (h *ToolConnectionsHandler) GetConnection(w http.ResponseWriter, r *http.Request) {
	headers, ok := h.ownerHeaders(w, r)
	if !ok {
		return
	}
	h.forward(w, r, http.MethodGet, "/v1/connections/"+url.PathEscape(r.PathValue("id")), headers, nil)
}

func (h *ToolConnectionsHandler) DeleteConnection(w http.ResponseWriter, r *http.Request) {
	headers, ok := h.writeOwnerHeaders(w, r)
	if !ok {
		return
	}
	h.forward(w, r, http.MethodDelete, "/v1/connections/"+url.PathEscape(r.PathValue("id")), headers, nil)
}

func (h *ToolConnectionsHandler) ReconnectConnection(w http.ResponseWriter, r *http.Request) {
	headers, ok := h.writeOwnerHeaders(w, r)
	if !ok {
		return
	}
	binding, err := newConnectionBinding()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Connection setup unavailable")
		return
	}
	headers[connectionBindingHeader] = binding
	h.forwardConnectionAuthorization(w, r, "/v1/connections/"+url.PathEscape(r.PathValue("id"))+"/reconnect", headers, nil, binding, false)
}

func (h *ToolConnectionsHandler) ListToolGrants(w http.ResponseWriter, r *http.Request) {
	headers, ok := h.instanceOwnerHeaders(w, r, false, true)
	if !ok {
		return
	}
	// Listing historical grants remains available after a runtime migration so
	// the owner can obtain each revision and revoke it. Mutations remain gated.
	if h.instanceToolAccessReadOnly(r) {
		w.Header().Set("X-CetiRun-Tool-Grants-Read-Only", "true")
	}
	h.forward(w, r, http.MethodGet, "/v1/instances/"+url.PathEscape(r.PathValue("name"))+"/tool-grants", headers, nil)
}

// CreateToolIdentity creates the provider binding for the immutable Agent UID.
// The provider principal is derived by the Broker from the Agent resource; the
// browser can only select the supported provider.
func (h *ToolConnectionsHandler) CreateToolIdentity(w http.ResponseWriter, r *http.Request) {
	headers, ok := h.instanceOwnerHeaders(w, r, true, false)
	if !ok {
		return
	}
	body, ok := readToolIdentity(w, r)
	if !ok {
		return
	}
	path := "/v1/instances/" + url.PathEscape(r.PathValue("name")) + "/tool-identities"
	h.forward(w, r, http.MethodPost, path, headers, body)
}

func (h *ToolConnectionsHandler) CreateToolGrant(w http.ResponseWriter, r *http.Request) {
	headers, ok := h.instanceOwnerHeaders(w, r, true, false)
	if !ok {
		return
	}
	body, _, ok := h.readGrant(w, r, false)
	if !ok {
		return
	}
	h.forward(w, r, http.MethodPost, "/v1/instances/"+url.PathEscape(r.PathValue("name"))+"/tool-grants", headers, body)
}

func (h *ToolConnectionsHandler) UpdateToolGrant(w http.ResponseWriter, r *http.Request) {
	headers, ok := h.instanceOwnerHeaders(w, r, true, false)
	if !ok {
		return
	}
	body, revision, ok := h.readGrant(w, r, true)
	if !ok {
		return
	}
	headers["If-Match"] = revision
	path := fmt.Sprintf("/v1/instances/%s/tool-grants/%s", url.PathEscape(r.PathValue("name")), url.PathEscape(r.PathValue("id")))
	h.forward(w, r, http.MethodPut, path, headers, body)
}

func (h *ToolConnectionsHandler) DeleteToolGrant(w http.ResponseWriter, r *http.Request) {
	// Keep stale grants revocable after an Agent moves to a Sandbox runtime.
	headers, ok := h.instanceOwnerHeaders(w, r, true, true)
	if !ok {
		return
	}
	revision := strings.Trim(r.Header.Get("If-Match"), `"`)
	if revision == "" {
		writeError(w, http.StatusPreconditionRequired, "revision_required", "Grant revision is required for revocation")
		return
	}
	headers["If-Match"] = revision
	path := fmt.Sprintf("/v1/instances/%s/tool-grants/%s", url.PathEscape(r.PathValue("name")), url.PathEscape(r.PathValue("id")))
	h.forward(w, r, http.MethodDelete, path, headers, nil)
}

// VerifyToolGrant asks the Broker to obtain provider-derived authorization
// evidence. A local browser action is never treated as an approval.
func (h *ToolConnectionsHandler) VerifyToolGrant(w http.ResponseWriter, r *http.Request) {
	headers, ok := h.instanceOwnerHeaders(w, r, true, false)
	if !ok {
		return
	}
	revision := strings.Trim(r.Header.Get("If-Match"), `"`)
	if revision == "" {
		writeError(w, http.StatusPreconditionRequired, "revision_required", "Grant revision is required for verification")
		return
	}
	numericRevision, err := strconv.ParseUint(revision, 10, 64)
	if err != nil || numericRevision == 0 {
		writeError(w, http.StatusBadRequest, "revision_required", "Grant revision is required for verification")
		return
	}
	headers["If-Match"] = revision
	path := fmt.Sprintf("/v1/instances/%s/tool-grants/%s/verify", url.PathEscape(r.PathValue("name")), url.PathEscape(r.PathValue("id")))
	h.forward(w, r, http.MethodPost, path, headers, nil)
}

func (h *ToolConnectionsHandler) ownerHeaders(w http.ResponseWriter, r *http.Request) (map[string]string, bool) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok || claims.TenantID == "" || claims.ObjectID == "" {
		writeError(w, http.StatusForbidden, "verified_owner_required", "Verified Portal identity is required")
		return nil, false
	}
	return map[string]string{
		"X-CetiRun-Owner-Tenant-ID": claims.TenantID,
		"X-CetiRun-Owner-Object-ID": claims.ObjectID,
		"X-CetiRun-Catalog-Version": toolCatalogVersion,
	}, true
}

func (h *ToolConnectionsHandler) writeOwnerHeaders(w http.ResponseWriter, r *http.Request) (map[string]string, bool) {
	if !h.validOrigin(r) {
		writeError(w, http.StatusForbidden, "invalid_origin", "Request origin is not allowed")
		return nil, false
	}
	return h.ownerHeaders(w, r)
}

func (h *ToolConnectionsHandler) instanceOwnerHeaders(w http.ResponseWriter, r *http.Request, write, allowUnsupportedRuntime bool) (map[string]string, bool) {
	var headers map[string]string
	var ok bool
	if write {
		headers, ok = h.writeOwnerHeaders(w, r)
	} else {
		headers, ok = h.ownerHeaders(w, r)
	}
	if !ok {
		return nil, false
	}
	instance, err := h.Kube.GetInstanceByName(r.Context(), r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusNotFound, "instance_not_found", "Instance not found")
		return nil, false
	}
	if !strings.EqualFold(instance.Annotations[cetirunv1alpha1.AnnotationOwnerTenantID], headers["X-CetiRun-Owner-Tenant-ID"]) ||
		!strings.EqualFold(instance.Annotations[cetirunv1alpha1.AnnotationOwnerObjectID], headers["X-CetiRun-Owner-Object-ID"]) {
		writeError(w, http.StatusNotFound, "instance_not_found", "Instance not found")
		return nil, false
	}
	if (instance.Spec.Agent.Type != cetirunv1alpha1.AgentTypeHermes && instance.Spec.Agent.Type != cetirunv1alpha1.AgentTypeOpenClaw) && !allowUnsupportedRuntime {
		writeError(w, http.StatusBadRequest, "unsupported_agent_type", "Agent Tools MCP supports only Hermes and OpenClaw Agents")
		return nil, false
	}
	if h.RuntimeTypes != nil && !allowUnsupportedRuntime {
		allowed := false
		for _, kind := range h.RuntimeTypes {
			if kind != "hermes" && kind != "openclaw" {
				allowed = false
				break
			}
			if kind == string(instance.Spec.Agent.Type) {
				allowed = true
			}
		}
		if !allowed {
			writeError(w, http.StatusBadRequest, "unsupported_agent_type", "Agent Tools MCP is not qualified for this runtime in this deployment")
			return nil, false
		}
	}
	if instance.Spec.SandboxRef != "" && !allowUnsupportedRuntime {
		writeError(w, http.StatusBadRequest, "unsupported_sandbox", "Agent Tools MCP is not available to Sandbox Agents")
		return nil, false
	}
	headers["X-CetiRun-Instance-UID"] = string(instance.UID)
	return headers, true
}

func (h *ToolConnectionsHandler) instanceToolAccessReadOnly(r *http.Request) bool {
	instance, err := h.Kube.GetInstanceByName(r.Context(), r.PathValue("name"))
	if err != nil {
		return false
	}
	if instance.Spec.Agent.Type != cetirunv1alpha1.AgentTypeHermes && instance.Spec.Agent.Type != cetirunv1alpha1.AgentTypeOpenClaw {
		return true
	}
	if instance.Spec.SandboxRef != "" {
		return true
	}
	if h.RuntimeTypes == nil {
		return false
	}
	for _, kind := range h.RuntimeTypes {
		if kind == string(instance.Spec.Agent.Type) && (kind == "hermes" || kind == "openclaw") {
			return false
		}
	}
	return true
}

type connectionStartRequest struct {
	Provider string `json:"provider"`
}

type toolIdentityRequest struct {
	Provider string `json:"provider"`
}

func readToolIdentity(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	var request toolIdentityRequest
	if !decodeJSON(w, r, &request) {
		return nil, false
	}
	if request.Provider != "azure" {
		writeError(w, http.StatusBadRequest, "unsupported_identity_provider", "Independent Agent identity currently supports only Azure")
		return nil, false
	}
	body, err := json.Marshal(request)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Tool identity request is invalid")
		return nil, false
	}
	return body, true
}

func readConnectionStart(w http.ResponseWriter, r *http.Request) ([]byte, string, bool) {
	var request connectionStartRequest
	if !decodeJSON(w, r, &request) {
		return nil, "", false
	}
	if request.Provider != "azure" && request.Provider != "microsoft" && request.Provider != "github" && request.Provider != "google" {
		writeError(w, http.StatusBadRequest, "invalid_provider", "Provider must be azure, microsoft, github, or google")
		return nil, "", false
	}
	body, err := json.Marshal(request)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Connection request is invalid")
		return nil, "", false
	}
	return body, request.Provider, true
}

type toolGrantRequest struct {
	ConnectionID string          `json:"connectionId"`
	Tools        []string        `json:"tools"`
	Constraints  json.RawMessage `json:"constraints"`
	ExpiresAt    time.Time       `json:"expiresAt"`
	Revision     json.RawMessage `json:"revision"`
}

type brokerGrantRequest struct {
	ConnectionID string          `json:"connectionId"`
	Tools        []string        `json:"tools"`
	Constraints  json.RawMessage `json:"constraints"`
	ExpiresAt    time.Time       `json:"expiresAt"`
}

func (h *ToolConnectionsHandler) readGrant(w http.ResponseWriter, r *http.Request, requireRevision bool) ([]byte, string, bool) {
	var request toolGrantRequest
	if !decodeJSON(w, r, &request) {
		return nil, "", false
	}
	if request.ConnectionID == "" || len(request.Tools) == 0 || len(request.Constraints) == 0 || request.ExpiresAt.IsZero() {
		writeError(w, http.StatusBadRequest, "invalid_request", "connectionId, tools, constraints, and expiresAt are required")
		return nil, "", false
	}
	now := h.now()
	if request.ExpiresAt.Before(now) || request.ExpiresAt.After(now.Add(maxGrantLifetime)) {
		writeError(w, http.StatusBadRequest, "invalid_expiry", "Grant expiry must be in the next 90 days")
		return nil, "", false
	}
	revision := ""
	if requireRevision {
		var numericRevision uint64
		if len(request.Revision) == 0 || bytes.Equal(request.Revision, []byte("null")) || json.Unmarshal(request.Revision, &numericRevision) != nil || numericRevision == 0 {
			writeError(w, http.StatusBadRequest, "revision_required", "Grant revision is required for updates")
			return nil, "", false
		}
		revision = fmt.Sprintf("%d", numericRevision)
	}
	body, err := json.Marshal(brokerGrantRequest{ConnectionID: request.ConnectionID, Tools: request.Tools, Constraints: request.Constraints, ExpiresAt: request.ExpiresAt})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Tool grant request is invalid")
		return nil, "", false
	}
	return body, revision, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Request content type must be application/json")
		return false
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxToolRequestBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Request body is invalid")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "Request body is invalid")
		return false
	}
	return true
}

type connectionAuthorizationResponse struct {
	ID                     string          `json:"id"`
	ConnectionID           string          `json:"connectionId"`
	AuthorizationURL       string          `json:"authorizationUrl"`
	ExpiresAt              json.RawMessage `json:"expiresAt"`
	BrowserBindingRequired bool            `json:"browserBindingRequired"`
}

// forwardConnectionAuthorization keeps browser-bound GitHub OAuth proof out
// of JSON and binds it to the callback state using an HttpOnly host cookie.
func (h *ToolConnectionsHandler) forwardConnectionAuthorization(w http.ResponseWriter, incoming *http.Request, path string, headers map[string]string, body []byte, binding string, normalizeStart bool) {
	h.forwardWithConnectionAuthorization(w, incoming, http.MethodPost, path, headers, body, binding, normalizeStart)
}

func (h *ToolConnectionsHandler) forward(w http.ResponseWriter, incoming *http.Request, method, path string, headers map[string]string, body []byte) {
	h.forwardWithConnectionAuthorization(w, incoming, method, path, headers, body, "", false)
}

func (h *ToolConnectionsHandler) forwardWithConnectionAuthorization(w http.ResponseWriter, incoming *http.Request, method, path string, headers map[string]string, body []byte, binding string, normalizeStart bool) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(incoming.Context(), method, h.BrokerBaseURL+path, reader)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Agent Tools service is unavailable")
		return
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.HTTPClient.Do(req)
	if err != nil {
		slog.Warn("Agent Tools Broker request failed", "path", path, "error", err)
		writeError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Agent Tools service is unavailable")
		return
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxToolBrokerResponseBytes+1))
	if err != nil || len(payload) > maxToolBrokerResponseBytes {
		writeError(w, http.StatusBadGateway, "dependency_invalid", "Agent Tools service returned an invalid response")
		return
	}
	if method == http.MethodPost && resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices && (normalizeStart || binding != "") {
		var authorization connectionAuthorizationResponse
		if err := json.Unmarshal(payload, &authorization); err != nil || authorization.AuthorizationURL == "" || len(authorization.ExpiresAt) == 0 || bytes.Equal(authorization.ExpiresAt, []byte("null")) {
			writeError(w, http.StatusBadGateway, "dependency_invalid", "Agent Tools service returned an invalid response")
			return
		}
		if authorization.ConnectionID == "" {
			authorization.ConnectionID = authorization.ID
		}
		if normalizeStart && authorization.ConnectionID == "" {
			writeError(w, http.StatusBadGateway, "dependency_invalid", "Agent Tools service returned an invalid response")
			return
		}
		if authorization.BrowserBindingRequired {
			if !validConnectionBinding(binding) || !h.setGitHubBindingCookie(w, authorization.AuthorizationURL, authorization.ExpiresAt, binding) {
				writeError(w, http.StatusBadGateway, "dependency_invalid", "Agent Tools service returned an invalid response")
				return
			}
			// The proof and the fact that a binding is required are both internal
			// control-plane details. Never expose either to browser JavaScript.
			payload, err = json.Marshal(struct {
				ConnectionID     string          `json:"connectionId,omitempty"`
				AuthorizationURL string          `json:"authorizationUrl"`
				ExpiresAt        json.RawMessage `json:"expiresAt"`
			}{authorization.ConnectionID, authorization.AuthorizationURL, authorization.ExpiresAt})
			if err != nil {
				writeError(w, http.StatusBadGateway, "dependency_invalid", "Agent Tools service returned an invalid response")
				return
			}
		} else if normalizeStart {
			payload, err = json.Marshal(struct {
				ConnectionID     string          `json:"connectionId"`
				AuthorizationURL string          `json:"authorizationUrl"`
				ExpiresAt        json.RawMessage `json:"expiresAt"`
			}{authorization.ConnectionID, authorization.AuthorizationURL, authorization.ExpiresAt})
			if err != nil {
				writeError(w, http.StatusBadGateway, "dependency_invalid", "Agent Tools service returned an invalid response")
				return
			}
		}
	}
	for _, key := range []string{"Content-Type", "Cache-Control", "Content-Security-Policy", "Referrer-Policy", "X-Content-Type-Options"} {
		if value := resp.Header.Get(key); value != "" {
			w.Header().Set(key, value)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(payload)
}

func newConnectionBinding() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func validConnectionBinding(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == value
}

func githubBindingCookieName(state string) (string, bool) {
	if !validConnectionBinding(state) {
		return "", false
	}
	sum := sha256.Sum256([]byte(state))
	return githubBindingCookiePrefix + base64.RawURLEncoding.EncodeToString(sum[:]), true
}

func githubAuthorizationState(authorizationURL string) (string, bool) {
	parsed, err := url.ParseRequestURI(authorizationURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", false
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || len(query["state"]) != 1 || !validConnectionBinding(query.Get("state")) {
		return "", false
	}
	return query.Get("state"), true
}

func (h *ToolConnectionsHandler) setGitHubBindingCookie(w http.ResponseWriter, authorizationURL string, expiresAt json.RawMessage, binding string) bool {
	state, ok := githubAuthorizationState(authorizationURL)
	if !ok {
		return false
	}
	var expiryText string
	if json.Unmarshal(expiresAt, &expiryText) != nil {
		return false
	}
	expiresAtTime, err := time.Parse(time.RFC3339, expiryText)
	if err != nil {
		return false
	}
	lifetime := expiresAtTime.Sub(h.now())
	if lifetime <= 0 || lifetime > githubBindingLifetime {
		return false
	}
	seconds := int((lifetime + time.Second - 1) / time.Second)
	if seconds < 1 || seconds > int(githubBindingLifetime/time.Second) {
		return false
	}
	cookieName, _ := githubBindingCookieName(state)
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    binding,
		Path:     "/",
		Expires:  expiresAtTime.UTC(),
		MaxAge:   seconds,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return true
}

func clearGitHubBindingCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(1, 0).UTC(),
		MaxAge:   -1,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *ToolConnectionsHandler) validOrigin(r *http.Request) bool {
	origin := strings.TrimRight(r.Header.Get("Origin"), "/")
	return h.PortalOrigin != "" && origin == h.PortalOrigin
}

func (h *ToolConnectionsHandler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}
