// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Azure/CetiRun/app/cetirun-portal/internal/auth"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/config"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// --- Helper functions ---

func withClaims(r *http.Request, alias, displayName, email string) *http.Request {
	claims := auth.Claims{
		Alias:       alias,
		DisplayName: displayName,
		Email:       email,
	}
	ctx := auth.NewContext(r.Context(), claims)
	return r.WithContext(ctx)
}

func sampleInstance() *cetirunv1alpha1.Agent {
	reconcileTime := metav1.NewTime(time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC))
	return &cetirunv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "alice",
			CreationTimestamp: metav1.NewTime(time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)),
		},
		Spec: cetirunv1alpha1.AgentSpec{
			User: cetirunv1alpha1.UserSpec{
				Alias:       "alice",
				DisplayName: "Alice Smith",
				Team:        "aks",
				Email:       "alice@microsoft.com",
			},
		},
		Status: cetirunv1alpha1.AgentStatus{
			Phase:             "Ready",
			Namespace:         "cetirun-alice",
			AccessURL:         "https://alice.cetirun.example.com",
			LastReconcileTime: &reconcileTime,
			Conditions: []metav1.Condition{
				{
					Type:    "Ready",
					Status:  metav1.ConditionTrue,
					Reason:  "AllReady",
					Message: "All components ready",
				},
			},
		},
	}
}

func TestToInstanceResponse(t *testing.T) {
	instance := sampleInstance()
	// Change phase to Pending to avoid token fetch in test
	instance.Status.Phase = "Pending"

	h := &InstanceHandler{
		Kube: kube.NewClient(newFakeClient()),
	}

	resp, err := h.toInstanceResponse(context.Background(), instance)
	if err != nil {
		t.Fatalf("toInstanceResponse() error = %v", err)
	}

	if resp.Alias != "alice" {
		t.Errorf("Alias = %q, want %q", resp.Alias, "alice")
	}
	if resp.InstanceName != "alice" {
		t.Errorf("InstanceName = %q, want %q", resp.InstanceName, "alice")
	}
	if resp.DisplayName != "Alice Smith" {
		t.Errorf("DisplayName = %q, want %q", resp.DisplayName, "Alice Smith")
	}
	if resp.Team != "aks" {
		t.Errorf("Team = %q, want %q", resp.Team, "aks")
	}
	if resp.Phase != "Pending" {
		t.Errorf("Phase = %q, want %q", resp.Phase, "Pending")
	}
	if resp.AccessURL != "https://alice.cetirun.example.com" {
		t.Errorf("AccessURL = %q, want %q", resp.AccessURL, "https://alice.cetirun.example.com")
	}
	if resp.CreatedAt != "2024-01-15T10:00:00Z" {
		t.Errorf("CreatedAt = %q, want %q", resp.CreatedAt, "2024-01-15T10:00:00Z")
	}
	if resp.LastReconciled != "2024-01-15T12:00:00Z" {
		t.Errorf("LastReconciled = %q, want %q", resp.LastReconciled, "2024-01-15T12:00:00Z")
	}
	if len(resp.Conditions) != 1 {
		t.Fatalf("Conditions length = %d, want 1", len(resp.Conditions))
	}
	if resp.Conditions[0].Type != "Ready" {
		t.Errorf("Conditions[0].Type = %q, want %q", resp.Conditions[0].Type, "Ready")
	}
	if resp.Conditions[0].Status != "True" {
		t.Errorf("Conditions[0].Status = %q, want %q", resp.Conditions[0].Status, "True")
	}
}

func TestToInstanceResponse_NilLastReconcileTime(t *testing.T) {
	instance := sampleInstance()
	instance.Status.LastReconcileTime = nil
	instance.Status.Phase = "Pending"

	h := &InstanceHandler{
		Kube: kube.NewClient(newFakeClient()),
	}

	resp, err := h.toInstanceResponse(context.Background(), instance)
	if err != nil {
		t.Fatalf("toInstanceResponse() error = %v", err)
	}
	if resp.LastReconciled != "" {
		t.Errorf("LastReconciled = %q, want empty", resp.LastReconciled)
	}
}

func TestToInstanceResponse_NoConditions(t *testing.T) {
	instance := sampleInstance()
	instance.Status.Conditions = nil
	instance.Status.Phase = "Pending"

	h := &InstanceHandler{
		Kube: kube.NewClient(newFakeClient()),
	}

	resp, err := h.toInstanceResponse(context.Background(), instance)
	if err != nil {
		t.Fatalf("toInstanceResponse() error = %v", err)
	}
	if resp.Conditions != nil {
		t.Errorf("Conditions = %v, want nil", resp.Conditions)
	}
}

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	data := map[string]string{"key": "value"}
	writeJSON(w, http.StatusOK, data)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}

	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if body["key"] != "value" {
		t.Errorf("body key = %q, want %q", body["key"], "value")
	}
}

func TestWriteError_Handler(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, http.StatusBadRequest, "bad_request", "something went wrong")

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	var errResp ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if errResp.Error.Code != "bad_request" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "bad_request")
	}
	if errResp.Error.Message != "something went wrong" {
		t.Errorf("error message = %q, want %q", errResp.Error.Message, "something went wrong")
	}
}

// --- Integration tests using fake controller-runtime client ---

func TestGetInstance_NotFound(t *testing.T) {
	h := &InstanceHandler{
		Kube: kube.NewClient(newFakeClient()),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instance", nil)
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.GetInstance(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestGetInstance_Success(t *testing.T) {
	instance := sampleInstance()
	fc := newFakeClient(instance)
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instance", nil)
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.GetInstance(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp InstanceResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.Alias != "alice" {
		t.Errorf("Alias = %q, want %q", resp.Alias, "alice")
	}
	if resp.Phase != "Ready" {
		t.Errorf("Phase = %q, want %q", resp.Phase, "Ready")
	}
}

func TestCreateInstance_Success(t *testing.T) {
	fc := newFakeClient()
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	body := CreateInstanceRequest{Team: "aks"}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instance", bytes.NewReader(bodyBytes))
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.CreateInstance(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", w.Code, http.StatusCreated)
	}

	var resp InstanceResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.Alias != "alice" {
		t.Errorf("Alias = %q, want %q", resp.Alias, "alice")
	}
}

func TestCreateInstance_ModelRoundTrip(t *testing.T) {
	fc := newFakeClient()
	h := &InstanceHandler{Kube: kube.NewClient(fc)}

	body := CreateInstanceRequest{Model: " gpt-6-astra "}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instance", bytes.NewReader(bodyBytes))
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()
	h.CreateInstance(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body=%s)", w.Code, http.StatusCreated, w.Body.String())
	}
	var resp InstanceResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Model != "gpt-6-astra" {
		t.Errorf("response model = %q, want gpt-6-astra", resp.Model)
	}
	instance, err := h.Kube.GetInstance(context.Background(), "alice")
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if instance.Spec.Model != "gpt-6-astra" {
		t.Errorf("stored model = %q, want gpt-6-astra", instance.Spec.Model)
	}
}

func TestCreateInstance_ModelTooLongRejected(t *testing.T) {
	h := &InstanceHandler{Kube: kube.NewClient(newFakeClient())}
	bodyBytes, _ := json.Marshal(CreateInstanceRequest{Model: strings.Repeat("a", 257)})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instance", bytes.NewReader(bodyBytes))
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()
	h.CreateInstance(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body=%s)", w.Code, http.StatusBadRequest, w.Body.String())
	}
	var resp ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error.Code != "invalid_model" {
		t.Errorf("error code = %q, want invalid_model", resp.Error.Code)
	}
}

func TestCreateInstance_ReservedName(t *testing.T) {
	// Each reserved name collides with either an EnvoyFilter EXEMPT_PREFIXES entry
	// or a portal SPA top-level route. Creating an instance with one of these names
	// would let any authenticated user reach the pod via the exempted prefix.
	reserved := []string{"oauth2", "api", "internal", "healthz", "readyz", "assets", "portal", "instances", "admin", "ui"}
	for _, name := range reserved {
		t.Run(name, func(t *testing.T) {
			fc := newFakeClient()
			h := &InstanceHandler{Kube: kube.NewClient(fc)}

			body := CreateInstanceRequest{Team: "aks", InstanceName: name}
			bodyBytes, _ := json.Marshal(body)

			// Use a single-letter alias so the combined name is short enough to pass
			// the 63-char namespace check, isolating the reserved-name rejection.
			req := httptest.NewRequest(http.MethodPost, "/api/v1/instance", bytes.NewReader(bodyBytes))
			req = withClaims(req, "a", "A", "a@microsoft.com")
			w := httptest.NewRecorder()

			h.CreateInstance(w, req)

			// The combined name is "a-<reserved>", which is not itself reserved,
			// so this should succeed — proving that only bare reserved names are rejected.
			if w.Code != http.StatusCreated {
				t.Errorf("suffix form: status = %d, want %d (body=%s)", w.Code, http.StatusCreated, w.Body.String())
			}
		})
	}

	// Now exercise the actual rejection path: an alias that itself equals a reserved name,
	// with no suffix, so instanceName == reserved.
	for _, name := range reserved {
		t.Run("bare-"+name, func(t *testing.T) {
			fc := newFakeClient()
			h := &InstanceHandler{Kube: kube.NewClient(fc)}

			body := CreateInstanceRequest{Team: "aks"}
			bodyBytes, _ := json.Marshal(body)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/instance", bytes.NewReader(bodyBytes))
			req = withClaims(req, name, "X", name+"@microsoft.com")
			w := httptest.NewRecorder()

			h.CreateInstance(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d (body=%s)", w.Code, http.StatusBadRequest, w.Body.String())
			}
			var resp ErrorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode error body: %v (body=%s)", err, w.Body.String())
			}
			if resp.Error.Code != "reserved_name" {
				t.Errorf("error.code = %q, want %q (body=%s)", resp.Error.Code, "reserved_name", w.Body.String())
			}
		})
	}
}

func TestCreateInstance_NoTeam_OK(t *testing.T) {
	fc := newFakeClient()
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	// Empty team is allowed: portal users may create solo instances without a team.
	body := CreateInstanceRequest{}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instance", bytes.NewReader(bodyBytes))
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.CreateInstance(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d (body=%s)", w.Code, http.StatusCreated, w.Body.String())
	}
}

func TestCreateInstance_InvalidBody(t *testing.T) {
	fc := newFakeClient()
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instance", bytes.NewReader([]byte("invalid")))
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.CreateInstance(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestCreateInstance_AlreadyExists(t *testing.T) {
	instance := sampleInstance()
	fc := newFakeClient(instance)
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	body := CreateInstanceRequest{Team: "aks"}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instance", bytes.NewReader(bodyBytes))
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.CreateInstance(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d", w.Code, http.StatusConflict)
	}
}

func TestCreateInstance_CustomDisplayName(t *testing.T) {
	fc := newFakeClient()
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	body := CreateInstanceRequest{DisplayName: "Custom Name", Team: "aks"}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instance", bytes.NewReader(bodyBytes))
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.CreateInstance(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", w.Code, http.StatusCreated)
	}

	var resp InstanceResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.DisplayName != "Custom Name" {
		t.Errorf("DisplayName = %q, want %q", resp.DisplayName, "Custom Name")
	}
}

func TestCreateInstance_WithGatewayToken(t *testing.T) {
	fc := newFakeClient()
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	body := CreateInstanceRequest{Team: "aks", GatewayToken: "my-secret-token"}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instance", bytes.NewReader(bodyBytes))
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.CreateInstance(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", w.Code, http.StatusCreated)
	}

	var resp InstanceResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.Alias != "alice" {
		t.Errorf("Alias = %q, want %q", resp.Alias, "alice")
	}

	// Verify gateway-token secret was created in the instance namespace.
	secret := &corev1.Secret{}
	if err := fc.Get(context.Background(), client.ObjectKey{Namespace: "cetirun-alice", Name: "gateway-token"}, secret); err != nil {
		t.Fatalf("expected gateway-token secret to exist: %v", err)
	}
	if string(secret.Data["OPENCLAW_GATEWAY_TOKEN"]) != "my-secret-token" {
		t.Errorf("OPENCLAW_GATEWAY_TOKEN = %q, want %q", string(secret.Data["OPENCLAW_GATEWAY_TOKEN"]), "my-secret-token")
	}
}

func TestCreateInstance_WithTailscaleAuthKey(t *testing.T) {
	fc := newFakeClient()
	h := &InstanceHandler{
		Kube:   kube.NewClient(fc),
		Config: config.Config{TailscaleEnabled: true},
	}

	body := CreateInstanceRequest{Team: "aks", TailscaleAuthKey: "tskey-auth-xxxxx"}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instance", bytes.NewReader(bodyBytes))
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.CreateInstance(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", w.Code, http.StatusCreated)
	}

	var resp InstanceResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.Alias != "alice" {
		t.Errorf("Alias = %q, want %q", resp.Alias, "alice")
	}

	// Verify tailscale-auth secret was created.
	secret := &corev1.Secret{}
	if err := fc.Get(context.Background(), client.ObjectKey{Namespace: "cetirun-alice", Name: "tailscale-auth"}, secret); err != nil {
		t.Fatalf("expected tailscale-auth secret to exist: %v", err)
	}
	if string(secret.Data["TAILSCALE_AUTH_KEY"]) != "tskey-auth-xxxxx" {
		t.Errorf("TAILSCALE_AUTH_KEY = %q, want %q", string(secret.Data["TAILSCALE_AUTH_KEY"]), "tskey-auth-xxxxx")
	}

	// Verify the CR has Tailscale spec set.
	instance := &cetirunv1alpha1.Agent{}
	if err := fc.Get(context.Background(), client.ObjectKey{Name: "alice"}, instance); err != nil {
		t.Fatalf("expected instance to exist: %v", err)
	}
	if instance.Spec.Tailscale == nil {
		t.Fatal("expected Tailscale spec to be set")
	}
	if instance.Spec.Tailscale.AuthKeySecretRef != "tailscale-auth" {
		t.Errorf("AuthKeySecretRef = %q, want %q", instance.Spec.Tailscale.AuthKeySecretRef, "tailscale-auth")
	}
}

func TestToInstanceResponse_WithTailscaleAuthKey(t *testing.T) {
	instance := sampleInstance()
	instance.Status.Phase = "Ready"
	instance.Spec.GatewayTokenSecretRef = "gateway-token"
	instance.Spec.Tailscale = &cetirunv1alpha1.TailscaleSpec{
		AuthKeySecretRef: "tailscale-auth",
	}

	// Create the gateway-token and tailscale-auth secrets in the instance namespace.
	gwSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "cetirun-alice", Name: "gateway-token"},
		Data:       map[string][]byte{"OPENCLAW_GATEWAY_TOKEN": []byte("gw-token-val")},
	}
	tsSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "cetirun-alice", Name: "tailscale-auth"},
		Data:       map[string][]byte{"TAILSCALE_AUTH_KEY": []byte("tskey-auth-xxx")},
	}

	fc := newFakeClient(instance, gwSecret, tsSecret)
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	resp, err := h.toInstanceResponse(context.Background(), instance)
	if err != nil {
		t.Fatalf("toInstanceResponse() error = %v", err)
	}
	if resp.GatewayToken != "gw-token-val" {
		t.Errorf("GatewayToken = %q, want %q", resp.GatewayToken, "gw-token-val")
	}
	// AccessURL must remain exactly as the instance status (no token appended).
	if resp.AccessURL != "https://alice.cetirun.example.com" {
		t.Errorf("AccessURL = %q, want %q", resp.AccessURL, "https://alice.cetirun.example.com")
	}
	if resp.TailscaleAuthKey != "tskey-auth-xxx" {
		t.Errorf("TailscaleAuthKey = %q, want %q", resp.TailscaleAuthKey, "tskey-auth-xxx")
	}
}

func TestToInstanceResponse_HermesAPIServerKey(t *testing.T) {
	instance := sampleInstance()
	instance.Spec.AgentType = cetirunv1alpha1.AgentTypeHermes
	instance.Status.Phase = "Ready"
	instance.Spec.GatewayTokenSecretRef = "gateway-token"

	gwSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "cetirun-alice", Name: "gateway-token"},
		Data: map[string][]byte{
			"OPENCLAW_GATEWAY_TOKEN": []byte("gw-token-val"),
			"API_SERVER_KEY":         []byte("api-server-key-val"),
			"CETIRUN_AUTH_KEY":       []byte("cetirun-auth-key-val"),
		},
	}

	fc := newFakeClient(instance, gwSecret)
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	resp, err := h.toInstanceResponse(context.Background(), instance)
	if err != nil {
		t.Fatalf("toInstanceResponse() error = %v", err)
	}
	if resp.GatewayToken != "gw-token-val" {
		t.Errorf("GatewayToken = %q, want %q", resp.GatewayToken, "gw-token-val")
	}
	if resp.APIServerKey != "api-server-key-val" {
		t.Errorf("APIServerKey = %q, want %q", resp.APIServerKey, "api-server-key-val")
	}
	if resp.CetiRunAuthKey != "cetirun-auth-key-val" {
		t.Errorf("CetiRunAuthKey = %q, want %q", resp.CetiRunAuthKey, "cetirun-auth-key-val")
	}
}

// TestToInstanceResponse_OpenClawCetiRunAuthKey verifies that OpenClaw
// instances also surface the in-pod cetirun-auth helper key (without
// exposing API_SERVER_KEY, which OpenClaw does not use).
func TestToInstanceResponse_OpenClawCetiRunAuthKey(t *testing.T) {
	instance := sampleInstance()
	instance.Spec.AgentType = cetirunv1alpha1.AgentTypeOpenClaw
	instance.Status.Phase = "Ready"
	instance.Spec.GatewayTokenSecretRef = "gateway-token"

	gwSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "cetirun-alice", Name: "gateway-token"},
		Data: map[string][]byte{
			"OPENCLAW_GATEWAY_TOKEN": []byte("gw-token-val"),
			"CETIRUN_AUTH_KEY":       []byte("openclaw-auth-key-val"),
		},
	}

	fc := newFakeClient(instance, gwSecret)
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	resp, err := h.toInstanceResponse(context.Background(), instance)
	if err != nil {
		t.Fatalf("toInstanceResponse() error = %v", err)
	}
	if resp.CetiRunAuthKey != "openclaw-auth-key-val" {
		t.Errorf("CetiRunAuthKey = %q, want %q", resp.CetiRunAuthKey, "openclaw-auth-key-val")
	}
	if resp.APIServerKey != "" {
		t.Errorf("APIServerKey should be empty for OpenClaw, got %q", resp.APIServerKey)
	}
}

func TestToInstanceResponse_TailscaleSecretMissing(t *testing.T) {
	instance := sampleInstance()
	instance.Status.Phase = "Ready"
	instance.Spec.GatewayTokenSecretRef = "gateway-token"
	instance.Spec.Tailscale = &cetirunv1alpha1.TailscaleSpec{
		AuthKeySecretRef: "tailscale-auth",
	}

	// Only create gateway-token, no tailscale-auth secret.
	gwSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "cetirun-alice", Name: "gateway-token"},
		Data:       map[string][]byte{"OPENCLAW_GATEWAY_TOKEN": []byte("gw-token-val")},
	}

	fc := newFakeClient(instance, gwSecret)
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	resp, err := h.toInstanceResponse(context.Background(), instance)
	if err != nil {
		t.Fatalf("toInstanceResponse() error = %v", err)
	}
	// GatewayToken should still be returned.
	if resp.GatewayToken != "gw-token-val" {
		t.Errorf("GatewayToken = %q, want %q", resp.GatewayToken, "gw-token-val")
	}
	// TailscaleAuthKey should be empty (failed to fetch, but no error returned).
	if resp.TailscaleAuthKey != "" {
		t.Errorf("TailscaleAuthKey = %q, want empty", resp.TailscaleAuthKey)
	}
}

func TestToInstanceResponse_NoTailscale(t *testing.T) {
	instance := sampleInstance()
	instance.Status.Phase = "Ready"
	instance.Spec.GatewayTokenSecretRef = "gateway-token"
	// Tailscale is nil (not enabled).

	gwSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "cetirun-alice", Name: "gateway-token"},
		Data:       map[string][]byte{"OPENCLAW_GATEWAY_TOKEN": []byte("gw-token-val")},
	}

	fc := newFakeClient(instance, gwSecret)
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	resp, err := h.toInstanceResponse(context.Background(), instance)
	if err != nil {
		t.Fatalf("toInstanceResponse() error = %v", err)
	}
	if resp.GatewayToken != "gw-token-val" {
		t.Errorf("GatewayToken = %q, want %q", resp.GatewayToken, "gw-token-val")
	}
	// AccessURL must remain exactly as the instance status (no token appended).
	if resp.AccessURL != "https://alice.cetirun.example.com" {
		t.Errorf("AccessURL = %q, want %q", resp.AccessURL, "https://alice.cetirun.example.com")
	}
	// TailscaleAuthKey should be empty when Tailscale is not configured.
	if resp.TailscaleAuthKey != "" {
		t.Errorf("TailscaleAuthKey = %q, want empty", resp.TailscaleAuthKey)
	}
}

func TestDeleteInstance_Success(t *testing.T) {
	instance := sampleInstance()
	fc := newFakeClient(instance)
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/instance", nil)
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.DeleteInstance(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
}

func TestDeleteInstance_NotFound(t *testing.T) {
	fc := newFakeClient()
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/instance", nil)
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.DeleteInstance(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

// --- UpdateInstance tests ---

func TestUpdateInstance_Success(t *testing.T) {
	instance := sampleInstance()
	fc := newFakeClient(instance)
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	body := UpdateInstanceRequest{
		DisplayName: strPtr("Alice Updated"),
		Team:        strPtr("platform"),
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/instance", bytes.NewReader(bodyBytes))
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.UpdateInstance(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp InstanceResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.DisplayName != "Alice Updated" {
		t.Errorf("DisplayName = %q, want %q", resp.DisplayName, "Alice Updated")
	}
	if resp.Team != "platform" {
		t.Errorf("Team = %q, want %q", resp.Team, "platform")
	}
}

func TestUpdateInstance_ModelRoundTripAndClear(t *testing.T) {
	instance := sampleInstance()
	instance.Spec.Model = "gpt-5.4"
	fc := newFakeClient(instance)
	h := &InstanceHandler{Kube: kube.NewClient(fc)}

	for _, model := range []string{"gpt-6-astra", ""} {
		bodyBytes, _ := json.Marshal(UpdateInstanceRequest{Model: &model})
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/instance", bytes.NewReader(bodyBytes))
		req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
		w := httptest.NewRecorder()
		h.UpdateInstance(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("model %q: status = %d, want %d (body=%s)", model, w.Code, http.StatusOK, w.Body.String())
		}
		var resp InstanceResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("model %q: decode response: %v", model, err)
		}
		if resp.Model != model {
			t.Errorf("model %q: response model = %q", model, resp.Model)
		}
	}

	updated, err := h.Kube.GetInstance(context.Background(), "alice")
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if updated.Spec.Model != "" {
		t.Errorf("stored model = %q, want deployment-default empty override", updated.Spec.Model)
	}
}

func TestUpdateInstance_ModelTooLongRejected(t *testing.T) {
	h := &InstanceHandler{Kube: kube.NewClient(newFakeClient(sampleInstance()))}
	bodyBytes, _ := json.Marshal(UpdateInstanceRequest{Model: strPtr(strings.Repeat("a", 257))})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/instance", bytes.NewReader(bodyBytes))
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()
	h.UpdateInstance(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body=%s)", w.Code, http.StatusBadRequest, w.Body.String())
	}
	var resp ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error.Code != "invalid_model" {
		t.Errorf("error code = %q, want invalid_model", resp.Error.Code)
	}
}

func TestUpdateInstanceByName_ModelRoundTrip(t *testing.T) {
	instance := sampleInstance()
	instance.Name = "alice-work"
	fc := newFakeClient(instance)
	h := &InstanceHandler{Kube: kube.NewClient(fc)}

	bodyBytes, _ := json.Marshal(UpdateInstanceRequest{Model: strPtr("gpt-6-astra")})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/instances/alice-work", bytes.NewReader(bodyBytes))
	req.SetPathValue("name", "alice-work")
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()
	h.UpdateInstanceByName(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body=%s)", w.Code, http.StatusOK, w.Body.String())
	}
	var resp InstanceResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Model != "gpt-6-astra" {
		t.Errorf("response model = %q, want gpt-6-astra", resp.Model)
	}
}

func TestUpdateInstance_NotFound(t *testing.T) {
	fc := newFakeClient()
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	body := UpdateInstanceRequest{DisplayName: strPtr("X")}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/instance", bytes.NewReader(bodyBytes))
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.UpdateInstance(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestUpdateInstance_InvalidBody(t *testing.T) {
	fc := newFakeClient()
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/instance", bytes.NewReader([]byte("invalid")))
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.UpdateInstance(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestUpdateInstance_EmptyTeamRejected(t *testing.T) {
	instance := sampleInstance()
	fc := newFakeClient(instance)
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	body := UpdateInstanceRequest{Team: strPtr("")}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/instance", bytes.NewReader(bodyBytes))
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.UpdateInstance(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestUpdateInstance_EmptyGatewayTokenRejected(t *testing.T) {
	instance := sampleInstance()
	fc := newFakeClient(instance)
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	body := UpdateInstanceRequest{GatewayToken: strPtr("")}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/instance", bytes.NewReader(bodyBytes))
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.UpdateInstance(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestUpdateInstance_EmptyBodyRejected(t *testing.T) {
	instance := sampleInstance()
	fc := newFakeClient(instance)
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	bodyBytes, _ := json.Marshal(UpdateInstanceRequest{})

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/instance", bytes.NewReader(bodyBytes))
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.UpdateInstance(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	var resp ErrorResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error.Code != "no_fields" {
		t.Errorf("error code = %q, want %q", resp.Error.Code, "no_fields")
	}
}

func TestUpdateInstance_WithGatewayToken(t *testing.T) {
	instance := sampleInstance()
	fc := newFakeClient(instance)
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	body := UpdateInstanceRequest{GatewayToken: strPtr("new-token-val")}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/instance", bytes.NewReader(bodyBytes))
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.UpdateInstance(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	// Verify gateway-token secret.
	secret := &corev1.Secret{}
	if err := fc.Get(context.Background(), client.ObjectKey{Namespace: "cetirun-alice", Name: "gateway-token"}, secret); err != nil {
		t.Fatalf("expected gateway-token secret to exist: %v", err)
	}
	if string(secret.Data["OPENCLAW_GATEWAY_TOKEN"]) != "new-token-val" {
		t.Errorf("OPENCLAW_GATEWAY_TOKEN = %q, want %q", string(secret.Data["OPENCLAW_GATEWAY_TOKEN"]), "new-token-val")
	}
}

func TestUpdateInstance_EmptyDisplayNameRejected(t *testing.T) {
	instance := sampleInstance()
	fc := newFakeClient(instance)
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	body := UpdateInstanceRequest{DisplayName: strPtr("")}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/instance", bytes.NewReader(bodyBytes))
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.UpdateInstance(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestUpdateInstance_WithTailscaleAuthKey(t *testing.T) {
	instance := sampleInstance()
	fc := newFakeClient(instance)
	h := &InstanceHandler{
		Kube:   kube.NewClient(fc),
		Config: config.Config{TailscaleEnabled: true},
	}

	body := UpdateInstanceRequest{TailscaleAuthKey: strPtr("tskey-new")}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/instance", bytes.NewReader(bodyBytes))
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.UpdateInstance(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	// Verify tailscale-auth secret was created.
	secret := &corev1.Secret{}
	if err := fc.Get(context.Background(), client.ObjectKey{Namespace: "cetirun-alice", Name: "tailscale-auth"}, secret); err != nil {
		t.Fatalf("expected tailscale-auth secret to exist: %v", err)
	}
	if string(secret.Data["TAILSCALE_AUTH_KEY"]) != "tskey-new" {
		t.Errorf("TAILSCALE_AUTH_KEY = %q, want %q", string(secret.Data["TAILSCALE_AUTH_KEY"]), "tskey-new")
	}
}

func TestUpdateInstance_DisableTailscale(t *testing.T) {
	instance := sampleInstance()
	instance.Spec.Tailscale = &cetirunv1alpha1.TailscaleSpec{AuthKeySecretRef: "tailscale-auth"}
	fc := newFakeClient(instance)
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	body := UpdateInstanceRequest{TailscaleAuthKey: strPtr("")}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/instance", bytes.NewReader(bodyBytes))
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.UpdateInstance(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	// Verify the CR no longer has Tailscale spec.
	updated := &cetirunv1alpha1.Agent{}
	if err := fc.Get(context.Background(), client.ObjectKey{Name: "alice"}, updated); err != nil {
		t.Fatalf("failed to get updated instance: %v", err)
	}
	if updated.Spec.Tailscale != nil {
		t.Error("expected Tailscale spec to be nil after disabling")
	}
}

func strPtr(s string) *string { return &s }

// --- Helper: operator deployment ---

func operatorDeployment(chartVersion string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "cetirun-system",
			Name:      "cetirun-operator",
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "operator"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "operator"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "manager",
							Image: "cetirun-operator:latest",
							Args:  []string{"--leader-elect", "--chart-version=" + chartVersion},
						},
					},
				},
			},
		},
	}
}

// --- UpgradeInstance handler tests ---

func TestUpgradeInstance_Success(t *testing.T) {
	instance := sampleInstance()
	instance.Spec.Helm.Version = "0.1.17"
	deploy := operatorDeployment("0.1.18")
	fc := newFakeClient(instance, deploy)
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instance/upgrade", nil)
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.UpgradeInstance(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp InstanceResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.Alias != "alice" {
		t.Errorf("Alias = %q, want %q", resp.Alias, "alice")
	}

	// Verify the CR was updated.
	updated := &cetirunv1alpha1.Agent{}
	if err := fc.Get(context.Background(), client.ObjectKey{Name: "alice"}, updated); err != nil {
		t.Fatalf("failed to get updated instance: %v", err)
	}
	if updated.Spec.Helm.Version != "0.1.18" {
		t.Errorf("version = %q, want %q", updated.Spec.Helm.Version, "0.1.18")
	}
}

func TestUpgradeInstance_NotFound(t *testing.T) {
	deploy := operatorDeployment("0.1.18")
	fc := newFakeClient(deploy)
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instance/upgrade", nil)
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.UpgradeInstance(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestUpgradeInstance_NoOperator(t *testing.T) {
	instance := sampleInstance()
	instance.Spec.Helm.Version = "0.1.17"
	fc := newFakeClient(instance)
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/instance/upgrade", nil)
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.UpgradeInstance(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

// --- UpgradeAvailable in toInstanceResponse ---

func TestToInstanceResponse_UpgradeAvailable(t *testing.T) {
	instance := sampleInstance()
	instance.Status.Phase = "Pending"
	instance.Spec.Helm.Version = "0.1.17"
	deploy := operatorDeployment("0.1.18")
	fc := newFakeClient(instance, deploy)
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	resp, err := h.toInstanceResponse(context.Background(), instance)
	if err != nil {
		t.Fatalf("toInstanceResponse() error = %v", err)
	}
	if !resp.UpgradeAvailable {
		t.Error("expected UpgradeAvailable=true when versions differ")
	}
}

func TestToInstanceResponse_UpgradeNotAvailable_SameVersion(t *testing.T) {
	instance := sampleInstance()
	instance.Status.Phase = "Pending"
	instance.Spec.Helm.Version = "0.1.18"
	deploy := operatorDeployment("0.1.18")
	fc := newFakeClient(instance, deploy)
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	resp, err := h.toInstanceResponse(context.Background(), instance)
	if err != nil {
		t.Fatalf("toInstanceResponse() error = %v", err)
	}
	if resp.UpgradeAvailable {
		t.Error("expected UpgradeAvailable=false when versions match")
	}
}

func TestToInstanceResponse_UpgradeNotAvailable_EmptyVersion(t *testing.T) {
	instance := sampleInstance()
	instance.Status.Phase = "Pending"
	instance.Spec.Helm.Version = "" // not yet backfilled
	deploy := operatorDeployment("0.1.18")
	fc := newFakeClient(instance, deploy)
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	resp, err := h.toInstanceResponse(context.Background(), instance)
	if err != nil {
		t.Fatalf("toInstanceResponse() error = %v", err)
	}
	if resp.UpgradeAvailable {
		t.Error("expected UpgradeAvailable=false when instance version is empty")
	}
}

// --- GetInstanceStatus tests ---

func TestGetInstanceStatus_NotFound(t *testing.T) {
	h := &InstanceHandler{
		Kube: kube.NewClient(newFakeClient()),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instance/status", nil)
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.GetInstanceStatus(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestGetInstanceStatus_Success(t *testing.T) {
	instance := sampleInstance()
	instance.Status.Phase = "Ready"
	instance.Status.Conditions = []metav1.Condition{
		{
			Type:    "Ready",
			Status:  metav1.ConditionTrue,
			Reason:  "AllReady",
			Message: "All components ready",
		},
		{
			Type:    "HelmDeployed",
			Status:  metav1.ConditionTrue,
			Reason:  "Deployed",
			Message: "Helm chart deployed",
		},
	}

	// Create a pod in the user namespace.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "cetirun-alice",
			Name:      "openclaw-alice-0",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "openclaw",
					Ready:        true,
					RestartCount: 2,
					State:        corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				},
			},
		},
	}

	fc := newFakeClient(instance, pod)
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instance/status", nil)
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.GetInstanceStatus(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp StatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	// Conditions
	if len(resp.Conditions) != 2 {
		t.Fatalf("Conditions length = %d, want 2", len(resp.Conditions))
	}
	if resp.Conditions[0].Type != "Ready" {
		t.Errorf("Conditions[0].Type = %q, want %q", resp.Conditions[0].Type, "Ready")
	}
	if resp.Conditions[0].Status != "True" {
		t.Errorf("Conditions[0].Status = %q, want %q", resp.Conditions[0].Status, "True")
	}

	// Pods
	if len(resp.Pods) != 1 {
		t.Fatalf("Pods length = %d, want 1", len(resp.Pods))
	}
	if resp.Pods[0].Name != "openclaw-alice-0" {
		t.Errorf("Pods[0].Name = %q, want %q", resp.Pods[0].Name, "openclaw-alice-0")
	}
	if resp.Pods[0].Phase != "Running" {
		t.Errorf("Pods[0].Phase = %q, want %q", resp.Pods[0].Phase, "Running")
	}
	if !resp.Pods[0].Ready {
		t.Error("expected Pods[0].Ready=true")
	}
	if resp.Pods[0].RestartCount != 2 {
		t.Errorf("Pods[0].RestartCount = %d, want 2", resp.Pods[0].RestartCount)
	}

	// Events and NsEvents should be empty slices (not null).
	if resp.Events == nil {
		t.Error("Events should be empty slice, not nil")
	}
	if resp.NsEvents == nil {
		t.Error("NsEvents should be empty slice, not nil")
	}
}

func TestGetInstanceStatus_EmptySlicesWhenNoData(t *testing.T) {
	instance := sampleInstance()
	instance.Status.Conditions = nil

	fc := newFakeClient(instance)
	h := &InstanceHandler{
		Kube: kube.NewClient(fc),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instance/status", nil)
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.GetInstanceStatus(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp StatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	// All fields should be empty arrays, not null.
	if resp.Conditions == nil {
		t.Error("Conditions should be empty slice, not nil")
	}
	if len(resp.Conditions) != 0 {
		t.Errorf("Conditions length = %d, want 0", len(resp.Conditions))
	}
	if resp.Events == nil {
		t.Error("Events should be empty slice, not nil")
	}
	if resp.NsEvents == nil {
		t.Error("NsEvents should be empty slice, not nil")
	}
	if resp.Pods == nil {
		t.Error("Pods should be empty slice, not nil")
	}
}

// --- DownloadTeamsApp tests ---

func TestDownloadTeamsApp_TeamsDisabled(t *testing.T) {
	instance := sampleInstance()
	// No MSTeams spec = Teams disabled.
	fc := newFakeClient(instance)
	h := &InstanceHandler{Kube: kube.NewClient(fc)}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instance/teams-app", nil)
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.DownloadTeamsApp(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestDownloadTeamsApp_TeamsNotReady(t *testing.T) {
	instance := sampleInstance()
	instance.Spec.MSTeams = &cetirunv1alpha1.MSTeamsSpec{Enabled: true}
	instance.Status.MSTeams = &cetirunv1alpha1.MSTeamsStatus{
		State:   "Provisioning",
		Message: "Working on it",
	}
	fc := newFakeClient(instance)
	h := &InstanceHandler{Kube: kube.NewClient(fc)}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instance/teams-app", nil)
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.DownloadTeamsApp(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d", w.Code, http.StatusConflict)
	}
}

func TestDownloadTeamsApp_Success(t *testing.T) {
	instance := sampleInstance()
	instance.Spec.MSTeams = &cetirunv1alpha1.MSTeamsSpec{Enabled: true}
	instance.Status.MSTeams = &cetirunv1alpha1.MSTeamsStatus{
		State: "Ready",
		AppID: "00000000-0000-0000-0000-000000000003",
	}
	fc := newFakeClient(instance)
	h := &InstanceHandler{Kube: kube.NewClient(fc)}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instance/teams-app", nil)
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.DownloadTeamsApp(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/zip")
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want %q", cc, "no-store")
	}
	if w.Body.Len() == 0 {
		t.Error("response body is empty, expected ZIP content")
	}

	// Verify the ZIP contains a valid manifest with substituted values.
	zipReader, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatalf("failed to open ZIP: %v", err)
	}

	var foundManifest, foundColor, foundOutline bool
	for _, f := range zipReader.File {
		switch f.Name {
		case "manifest.json":
			foundManifest = true
			func() {
				rc, err := f.Open()
				if err != nil {
					t.Fatalf("failed to open manifest in ZIP: %v", err)
				}
				defer rc.Close()

				var manifestObj map[string]any
				if err := json.NewDecoder(rc).Decode(&manifestObj); err != nil {
					t.Fatalf("manifest is not valid JSON: %v", err)
				}

				if id, _ := manifestObj["id"].(string); id != "00000000-0000-0000-0000-000000000003" {
					t.Errorf("manifest id = %q, want %q", id, "00000000-0000-0000-0000-000000000003")
				}
				nameObj, _ := manifestObj["name"].(map[string]any)
				if nameObj == nil {
					t.Fatal("manifest name object is nil")
				}
				shortName, _ := nameObj["short"].(string)
				if shortName != "CetiRun Bot" {
					t.Errorf("manifest name.short = %q, want %q", shortName, "CetiRun Bot")
				}
				fullName, _ := nameObj["full"].(string)
				if fullName != "CetiRun Teams Bot for Alice Smith" {
					t.Errorf("manifest name.full = %q, want %q", fullName, "CetiRun Teams Bot for Alice Smith")
				}
				descObj, _ := manifestObj["description"].(map[string]any)
				if descObj == nil {
					t.Fatal("manifest description object is nil")
				}
				shortDesc, _ := descObj["short"].(string)
				if shortDesc != "CetiRun Teams Bot for Alice Smith" {
					t.Errorf("manifest description.short = %q, want %q", shortDesc, "CetiRun Teams Bot for Alice Smith")
				}
				fullDesc, _ := descObj["full"].(string)
				wantFullDesc := "CetiRun is an open-source managed runtime for hosting AI agents and AI applications."
				if fullDesc != wantFullDesc {
					t.Errorf("manifest description.full = %q, want %q", fullDesc, wantFullDesc)
				}
			}()
		case "color.png":
			foundColor = true
		case "outline.png":
			foundOutline = true
		}
	}
	if !foundManifest {
		t.Error("ZIP missing manifest.json")
	}
	if !foundColor {
		t.Error("ZIP missing color.png")
	}
	if !foundOutline {
		t.Error("ZIP missing outline.png")
	}
}

func TestDownloadTeamsApp_NoInstance(t *testing.T) {
	fc := newFakeClient()
	h := &InstanceHandler{Kube: kube.NewClient(fc)}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instance/teams-app", nil)
	req = withClaims(req, "alice", "Alice Smith", "alice@microsoft.com")
	w := httptest.NewRecorder()

	h.DownloadTeamsApp(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestListInstances(t *testing.T) {
	alice1 := sampleInstance()
	alice1.Name = "alice-work"

	alice2 := sampleInstance()
	alice2.Name = "alice-exp"
	alice2.Spec.User.Alias = "alice"

	bob := sampleInstance()
	bob.Name = "bob-default"
	bob.Spec.User.Alias = "bob"

	h := &InstanceHandler{Kube: kube.NewClient(newFakeClientWith(alice1, alice2, bob))}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	req = withClaims(req, "alice", "Alice", "alice@example.com")
	w := httptest.NewRecorder()
	h.ListInstances(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp []map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp) != 2 {
		t.Errorf("got %d instances, want 2 (only alice's)", len(resp))
	}
}

func TestGetInstanceByName_Forbidden(t *testing.T) {
	inst := sampleInstance()
	inst.Name = "alice-work"
	inst.Spec.User.Alias = "alice"

	h := &InstanceHandler{Kube: kube.NewClient(newFakeClientWith(inst))}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/alice-work", nil)
	req.SetPathValue("name", "alice-work")
	req = withClaims(req, "bob", "Bob", "bob@example.com")
	w := httptest.NewRecorder()
	h.GetInstanceByName(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestInternalAuth_Allow(t *testing.T) {
	inst := sampleInstance()
	inst.Name = "alice"
	inst.Spec.User.Alias = "alice"
	h := &InstanceHandler{Kube: kube.NewClient(newFakeClientWith(inst))}

	req := httptest.NewRequest(http.MethodGet, "/internal/auth", nil)
	req.Header.Set("X-Internal-Instance", "alice")
	req.Header.Set("X-Internal-Alias", "alice")
	w := httptest.NewRecorder()
	h.InternalAuth(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestInternalAuth_Deny(t *testing.T) {
	inst := sampleInstance()
	inst.Name = "alice"
	inst.Spec.User.Alias = "alice"
	h := &InstanceHandler{Kube: kube.NewClient(newFakeClientWith(inst))}

	req := httptest.NewRequest(http.MethodGet, "/internal/auth", nil)
	req.Header.Set("X-Internal-Instance", "alice")
	req.Header.Set("X-Internal-Alias", "bob")
	w := httptest.NewRecorder()
	h.InternalAuth(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestInternalAuth_NotFound(t *testing.T) {
	h := &InstanceHandler{Kube: kube.NewClient(newFakeClient())}
	req := httptest.NewRequest(http.MethodGet, "/internal/auth", nil)
	req.Header.Set("X-Internal-Instance", "nobody")
	req.Header.Set("X-Internal-Alias", "bob")
	w := httptest.NewRecorder()
	h.InternalAuth(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (instance not found treated as deny)", w.Code)
	}
}

func TestToInstanceResponse_TeamsStatusWithoutVersionGate(t *testing.T) {
	tests := []struct {
		name    string
		version string
		teams   bool
	}{
		{"teams enabled initial version", "0.1.0", true},
		{"teams enabled below former minimum", "0.1.22", true},
		{"teams enabled former minimum", "0.1.23", true},
		{"teams disabled initial version", "0.1.0", false},
		{"teams enabled empty version", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instance := sampleInstance()
			instance.Status.Phase = "Pending" // avoid token fetch
			instance.Spec.Helm.Version = tt.version
			if tt.teams {
				instance.Spec.MSTeams = &cetirunv1alpha1.MSTeamsSpec{Enabled: true}
			}

			h := &InstanceHandler{Kube: kube.NewClient(newFakeClient())}
			resp, err := h.toInstanceResponse(context.Background(), instance)
			if err != nil {
				t.Fatalf("toInstanceResponse() error = %v", err)
			}
			if resp.TeamsEnabled != tt.teams {
				t.Errorf("TeamsEnabled = %v, want %v", resp.TeamsEnabled, tt.teams)
			}
			body, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			if _, exists := payload["teamsUpgradeRequired"]; exists {
				t.Error("response must not include the removed Teams version gate")
			}
		})
	}
}
