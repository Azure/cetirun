// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"bytes"
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

const maxCredentialResponseBytes = 1 << 20

// AgentAuthorizationHandler exposes owner-authorized Portal operations and
// forwards them to the mesh-protected Agent Identity Broker.
type AgentAuthorizationHandler struct {
	Kube          *kube.Client
	BrokerBaseURL string
	PortalOrigin  string
	HTTPClient    *http.Client
}

func NewAgentAuthorizationHandler(kubeClient *kube.Client, brokerBaseURL, portalOrigin string) *AgentAuthorizationHandler {
	return &AgentAuthorizationHandler{
		Kube:          kubeClient,
		BrokerBaseURL: strings.TrimRight(brokerBaseURL, "/"),
		PortalOrigin:  strings.TrimRight(portalOrigin, "/"),
		HTTPClient:    &http.Client{Timeout: 20 * time.Second},
	}
}

func (h *AgentAuthorizationHandler) Status(w http.ResponseWriter, r *http.Request) {
	h.forwardOwnerRequest(w, r, http.MethodGet, false)
}

func (h *AgentAuthorizationHandler) Start(w http.ResponseWriter, r *http.Request) {
	if !h.validOrigin(r) {
		writeError(w, http.StatusForbidden, "invalid_origin", "Request origin is not allowed")
		return
	}
	h.forwardOwnerRequest(w, r, http.MethodPost, false)
}

func (h *AgentAuthorizationHandler) End(w http.ResponseWriter, r *http.Request) {
	if !h.validOrigin(r) {
		writeError(w, http.StatusForbidden, "invalid_origin", "Request origin is not allowed")
		return
	}
	h.forwardOwnerRequest(w, r, http.MethodDelete, false)
}

// Callback is intentionally unauthenticated by the Portal session. The
// Broker authenticates the one-time state and accepts form_post
// only. The Portal never parses or logs the authorization code.
func (h *AgentAuthorizationHandler) Callback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		writeError(w, http.StatusBadRequest, "invalid_callback", "Authorization callback must use form_post")
		return
	}
	h.forward(w, r, http.MethodPost, "/internal/v1/authorization/callback", nil, true)
}

func (h *AgentAuthorizationHandler) forwardOwnerRequest(w http.ResponseWriter, r *http.Request, method string, callback bool) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok || claims.TenantID == "" || claims.ObjectID == "" {
		writeError(w, http.StatusForbidden, "verified_owner_required", "Verified Microsoft Entra owner identity is required")
		return
	}
	name := r.PathValue("name")
	instance, err := h.Kube.GetInstanceByName(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusNotFound, "instance_not_found", "Instance not found")
		return
	}
	if !strings.EqualFold(instance.Annotations[cetirunv1alpha1.AnnotationOwnerTenantID], claims.TenantID) ||
		!strings.EqualFold(instance.Annotations[cetirunv1alpha1.AnnotationOwnerObjectID], claims.ObjectID) {
		writeError(w, http.StatusForbidden, "forbidden", "Only the instance owner may manage this connection")
		return
	}
	if instance.Spec.AgentIdentity == nil || !instance.Spec.AgentIdentity.Enabled {
		writeError(w, http.StatusConflict, "agent_identity_disabled", "Agent Identity is not enabled for this instance")
		return
	}
	headers := map[string]string{
		"X-CetiRun-Instance-UID":    string(instance.UID),
		"X-CetiRun-Instance-Name":   instance.Name,
		"X-CetiRun-Owner-Tenant-ID": claims.TenantID,
		"X-CetiRun-Owner-Object-ID": claims.ObjectID,
	}
	path := fmt.Sprintf("/internal/v1/instances/%s/authorization", url.PathEscape(string(instance.UID)))
	if method == http.MethodPost {
		headers["X-CetiRun-Owner-Login-Hint"] = claims.LoginHint
		path += "/start"
	}
	h.forward(w, r, method, path, headers, callback)
}

func (h *AgentAuthorizationHandler) forward(w http.ResponseWriter, incoming *http.Request, method, path string, headers map[string]string, form bool) {
	var body io.Reader
	if form {
		body = io.LimitReader(incoming.Body, 64<<10)
	} else if method == http.MethodPost {
		body = bytes.NewReader([]byte("{}"))
	}
	req, err := http.NewRequestWithContext(incoming.Context(), method, h.BrokerBaseURL+path, body)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Agent Identity service is unavailable")
		return
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if form {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.HTTPClient.Do(req)
	if err != nil {
		slog.Warn("Agent Identity Broker request failed", "path", path, "error", err)
		writeError(w, http.StatusServiceUnavailable, "dependency_unavailable", "Agent Identity service is unavailable")
		return
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxCredentialResponseBytes+1))
	if err != nil || len(payload) > maxCredentialResponseBytes {
		writeError(w, http.StatusBadGateway, "dependency_invalid", "Agent Identity service returned an invalid response")
		return
	}
	for _, key := range []string{"Content-Type", "Cache-Control", "Content-Security-Policy", "Referrer-Policy", "X-Content-Type-Options"} {
		if value := resp.Header.Get(key); value != "" {
			w.Header().Set(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(payload)
}

func (h *AgentAuthorizationHandler) validOrigin(r *http.Request) bool {
	origin := strings.TrimRight(r.Header.Get("Origin"), "/")
	return h.PortalOrigin != "" && origin == h.PortalOrigin
}
