// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/auth"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
)

const (
	maxToolBrokerResponseBytes = 1 << 20
	maxToolRequestBytes        = 64 << 10
	toolCatalogVersion         = "2026-10-10"
	maxGrantLifetime           = 90 * 24 * time.Hour
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
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, h.BrokerBaseURL+"/api/v1/connections/callback", strings.NewReader(form.Encode()))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Connection callback unavailable")
		return
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
	body, ok := readConnectionStart(w, r)
	if !ok {
		return
	}
	h.forward(w, r, http.MethodPost, "/v1/connections/start", headers, body)
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
	h.forward(w, r, http.MethodPost, "/v1/connections/"+url.PathEscape(r.PathValue("id"))+"/reconnect", headers, nil)
}

func (h *ToolConnectionsHandler) ListToolGrants(w http.ResponseWriter, r *http.Request) {
	headers, ok := h.instanceOwnerHeaders(w, r, false, false)
	if !ok {
		return
	}
	h.forward(w, r, http.MethodGet, "/v1/instances/"+url.PathEscape(r.PathValue("name"))+"/tool-grants", headers, nil)
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

func (h *ToolConnectionsHandler) instanceOwnerHeaders(w http.ResponseWriter, r *http.Request, write, allowSandboxCleanup bool) (map[string]string, bool) {
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
	if instance.Spec.Agent.Type != cetirunv1alpha1.AgentTypeHermes && instance.Spec.Agent.Type != cetirunv1alpha1.AgentTypeOpenClaw {
		writeError(w, http.StatusBadRequest, "unsupported_agent_type", "Agent Tools MCP supports only Hermes and OpenClaw Agents")
		return nil, false
	}
	if instance.Spec.SandboxRef != "" && !allowSandboxCleanup {
		writeError(w, http.StatusBadRequest, "unsupported_sandbox", "Agent Tools MCP is not available to Sandbox Agents")
		return nil, false
	}
	headers["X-CetiRun-Instance-UID"] = string(instance.UID)
	return headers, true
}

type connectionStartRequest struct {
	Provider string `json:"provider"`
}

func readConnectionStart(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	var request connectionStartRequest
	if !decodeJSON(w, r, &request) {
		return nil, false
	}
	if request.Provider != "azure" && request.Provider != "microsoft" && request.Provider != "github" && request.Provider != "google" {
		writeError(w, http.StatusBadRequest, "invalid_provider", "Provider must be azure, microsoft, github, or google")
		return nil, false
	}
	body, err := json.Marshal(request)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Connection request is invalid")
		return nil, false
	}
	return body, true
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

func (h *ToolConnectionsHandler) forward(w http.ResponseWriter, incoming *http.Request, method, path string, headers map[string]string, body []byte) {
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
	if method == http.MethodPost && path == "/v1/connections/start" && resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		var start struct {
			ID               string `json:"id"`
			ConnectionID     string `json:"connectionId"`
			AuthorizationURL string `json:"authorizationUrl"`
			ExpiresAt        any    `json:"expiresAt"`
		}
		if err := json.Unmarshal(payload, &start); err != nil || start.AuthorizationURL == "" || start.ExpiresAt == nil {
			writeError(w, http.StatusBadGateway, "dependency_invalid", "Agent Tools service returned an invalid response")
			return
		}
		if start.ConnectionID == "" {
			start.ConnectionID = start.ID
		}
		if start.ConnectionID == "" {
			writeError(w, http.StatusBadGateway, "dependency_invalid", "Agent Tools service returned an invalid response")
			return
		}
		payload, err = json.Marshal(map[string]any{"connectionId": start.ConnectionID, "authorizationUrl": start.AuthorizationURL, "expiresAt": start.ExpiresAt})
		if err != nil {
			writeError(w, http.StatusBadGateway, "dependency_invalid", "Agent Tools service returned an invalid response")
			return
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
