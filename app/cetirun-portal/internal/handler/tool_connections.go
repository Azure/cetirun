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
	connectionBindingsReadOnly = "X-CetiRun-Connection-Bindings-Read-Only"
)

// ToolConnectionsHandler exposes the Azure Agent identity connection for the
// current owner and Agent. It is a bounded proxy to the mesh-protected Broker:
// provider credentials and owner identity headers from browsers are never used.
type ToolConnectionsHandler struct {
	Kube          *kube.Client
	BrokerBaseURL string
	PortalOrigin  string
	HTTPClient    *http.Client
	RuntimeTypes  []string
}

func NewToolConnectionsHandler(kubeClient *kube.Client, brokerBaseURL, portalOrigin string) *ToolConnectionsHandler {
	return &ToolConnectionsHandler{
		Kube:          kubeClient,
		BrokerBaseURL: strings.TrimRight(brokerBaseURL, "/"),
		PortalOrigin:  strings.TrimRight(portalOrigin, "/"),
		HTTPClient: &http.Client{
			Timeout:       20 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (h *ToolConnectionsHandler) ListConnections(w http.ResponseWriter, r *http.Request) {
	h.noStore(w)
	headers, ok := h.ownerHeaders(w, r)
	if !ok {
		return
	}
	h.forward(w, r, http.MethodGet, "/v1/connections", headers, nil)
}

// CreateToolIdentity creates or returns the Azure connection derived from the
// immutable Agent identity. The browser cannot supply a principal or tenant.
func (h *ToolConnectionsHandler) CreateToolIdentity(w http.ResponseWriter, r *http.Request) {
	h.noStore(w)
	headers, _, ok := h.instanceHeaders(w, r, true, false)
	if !ok {
		return
	}
	body, ok := readAzureIdentity(w, r)
	if !ok {
		return
	}
	path := "/v1/instances/" + url.PathEscape(r.PathValue("name")) + "/tool-identities"
	h.forward(w, r, http.MethodPost, path, headers, body)
}

func (h *ToolConnectionsHandler) ListConnectionBindings(w http.ResponseWriter, r *http.Request) {
	h.noStore(w)
	headers, readOnly, ok := h.instanceHeaders(w, r, false, true)
	if !ok {
		return
	}
	if readOnly {
		w.Header().Set(connectionBindingsReadOnly, "true")
	}
	path := "/v1/instances/" + url.PathEscape(r.PathValue("name")) + "/connection-bindings"
	h.forward(w, r, http.MethodGet, path, headers, nil)
}

func (h *ToolConnectionsHandler) CreateConnectionBinding(w http.ResponseWriter, r *http.Request) {
	h.noStore(w)
	headers, _, ok := h.instanceHeaders(w, r, true, false)
	if !ok {
		return
	}
	body, ok := readAzureBinding(w, r)
	if !ok {
		return
	}
	path := "/v1/instances/" + url.PathEscape(r.PathValue("name")) + "/connection-bindings"
	h.forward(w, r, http.MethodPost, path, headers, body)
}

// DeleteConnectionBinding disconnects this Agent only. The Connection and its
// Entra principal remain intact, and the Broker retains a revision tombstone.
func (h *ToolConnectionsHandler) DeleteConnectionBinding(w http.ResponseWriter, r *http.Request) {
	h.noStore(w)
	headers, _, ok := h.instanceHeaders(w, r, true, true)
	if !ok {
		return
	}
	revision := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	if revision == "" {
		writeError(w, http.StatusPreconditionRequired, "revision_required", "Binding revision is required for disconnection")
		return
	}
	headers["If-Match"] = revision
	path := fmt.Sprintf("/v1/instances/%s/connection-bindings/%s", url.PathEscape(r.PathValue("name")), url.PathEscape(r.PathValue("id")))
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
	}, true
}

// instanceHeaders resolves the Agent exactly once. The immutable UID and
// ownership records come from Kubernetes rather than request headers or body.
func (h *ToolConnectionsHandler) instanceHeaders(w http.ResponseWriter, r *http.Request, write, allowReadOnly bool) (map[string]string, bool, bool) {
	if write && !h.validOrigin(r) {
		writeError(w, http.StatusForbidden, "invalid_origin", "Request origin is not allowed")
		return nil, false, false
	}
	headers, ok := h.ownerHeaders(w, r)
	if !ok {
		return nil, false, false
	}
	instance, err := h.Kube.GetInstanceByName(r.Context(), r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusNotFound, "instance_not_found", "Instance not found")
		return nil, false, false
	}
	if !strings.EqualFold(instance.Annotations[cetirunv1alpha1.AnnotationOwnerTenantID], headers["X-CetiRun-Owner-Tenant-ID"]) ||
		!strings.EqualFold(instance.Annotations[cetirunv1alpha1.AnnotationOwnerObjectID], headers["X-CetiRun-Owner-Object-ID"]) {
		// Keep ownership non-enumerable, including for Portal administrators.
		writeError(w, http.StatusNotFound, "instance_not_found", "Instance not found")
		return nil, false, false
	}
	readOnly := !h.runtimeSupported(instance)
	if readOnly && !allowReadOnly {
		if instance.Spec.SandboxRef != "" {
			writeError(w, http.StatusBadRequest, "unsupported_sandbox", "Agent Tools MCP is not available to Sandbox Agents")
		} else {
			writeError(w, http.StatusBadRequest, "unsupported_agent_type", "Agent Tools MCP is not qualified for this runtime in this deployment")
		}
		return nil, false, false
	}
	headers["X-CetiRun-Instance-UID"] = string(instance.UID)
	headers["X-CetiRun-Instance-Name"] = instance.Name
	return headers, readOnly, true
}

func (h *ToolConnectionsHandler) runtimeSupported(instance *cetirunv1alpha1.Agent) bool {
	if instance.Spec.SandboxRef != "" || (instance.Spec.Agent.Type != cetirunv1alpha1.AgentTypeHermes && instance.Spec.Agent.Type != cetirunv1alpha1.AgentTypeOpenClaw) {
		return false
	}
	if h.RuntimeTypes == nil {
		return true
	}
	for _, runtimeType := range h.RuntimeTypes {
		if runtimeType == string(instance.Spec.Agent.Type) && (runtimeType == "hermes" || runtimeType == "openclaw") {
			return true
		}
	}
	return false
}

type azureIdentityRequest struct {
	Provider string `json:"provider"`
}

func readAzureIdentity(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	var request azureIdentityRequest
	if !decodeToolJSON(w, r, &request) {
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

type azureBindingRequest struct {
	ConnectionID string   `json:"connectionId"`
	Services     []string `json:"services"`
	Enabled      bool     `json:"enabled"`
}

func readAzureBinding(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	var request azureBindingRequest
	if !decodeToolJSON(w, r, &request) {
		return nil, false
	}
	if request.ConnectionID == "" || len(request.ConnectionID) > 128 || !request.Enabled || len(request.Services) != 1 || request.Services[0] != "azure_arm" {
		writeError(w, http.StatusBadRequest, "invalid_request", "An enabled Azure ARM connection binding is required")
		return nil, false
	}
	body, err := json.Marshal(request)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Connection binding request is invalid")
		return nil, false
	}
	return body, true
}

func decodeToolJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Request content type must be application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxToolRequestBytes)
	decoder := json.NewDecoder(r.Body)
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
	if resp.StatusCode >= http.StatusMultipleChoices && resp.StatusCode < http.StatusBadRequest {
		writeError(w, http.StatusBadGateway, "dependency_invalid", "Agent Tools service returned an invalid response")
		return
	}
	if contentType := resp.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(payload)
}

func (h *ToolConnectionsHandler) noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

func (h *ToolConnectionsHandler) validOrigin(r *http.Request) bool {
	origin := strings.TrimRight(r.Header.Get("Origin"), "/")
	return h.PortalOrigin != "" && origin == h.PortalOrigin
}
