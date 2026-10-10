// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Azure/CetiRun/app/agent-tools-mcp/internal/identity"
	"github.com/Azure/CetiRun/app/agent-tools-mcp/internal/tools"
)

type fakeService struct {
	authCalls int
	listCalls int
	calls     int
	result    map[string]any
	err       error
}

func (f *fakeService) Authenticate(_ context.Context, bearer string) (identity.Caller, error) {
	f.authCalls++
	if bearer != "synthetic-workload-proof" {
		return identity.Caller{}, context.Canceled
	}
	return identity.Caller{Proof: bearer, InstanceUID: "00000000-0000-0000-0000-000000000001"}, nil
}
func (f *fakeService) List(context.Context, identity.Caller) ([]tools.Spec, error) {
	f.listCalls++
	return []tools.Spec{}, nil
}
func (f *fakeService) Call(context.Context, identity.Caller, string, map[string]any) (map[string]any, error) {
	f.calls++
	if f.result == nil {
		return map[string]any{}, f.err
	}
	return f.result, f.err
}

func TestToolsCallRejectsUnknownArgumentsBeforeService(t *testing.T) {
	fake := &fakeService{}
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"azure_subscriptions_list","arguments":{"connection_id":"conn-demo","extra":"no"}}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer synthetic-workload-proof")
	request.Header.Set("MCP-Protocol-Version", protocolVersion)
	response := httptest.NewRecorder()
	New(fake).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if fake.calls != 0 {
		t.Fatalf("service calls = %d, want 0", fake.calls)
	}
	if !strings.Contains(response.Body.String(), `"isError":true`) || !strings.Contains(response.Body.String(), `invalid_request`) {
		t.Fatalf("unexpected response %s", response.Body.String())
	}
}

func TestMCPRequiresBearerProof(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	New(&fakeService{}).ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "unauthenticated") {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
	}
}

func TestInitializeNegotiatesSupportedVersionAfterAuthentication(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2026-01-01"}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer synthetic-workload-proof")
	response := httptest.NewRecorder()
	New(&fakeService{}).ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"protocolVersion":"2025-11-25"`) {
		t.Fatalf("unexpected negotiation response: %d %s", response.Code, response.Body.String())
	}
}

func TestToolsCallReturnsSanitizedBusinessData(t *testing.T) {
	fake := &fakeService{result: map[string]any{"value": "completed", "access_token": "synthetic-access-token"}}
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"azure_subscriptions_list","arguments":{"connection_id":"conn-demo"}}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer synthetic-workload-proof")
	request.Header.Set("MCP-Protocol-Version", protocolVersion)
	response := httptest.NewRecorder()
	New(fake).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "synthetic-access-token") {
		t.Fatalf("response exposed a token: %s", response.Body.String())
	}
	var payload struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			Structured map[string]any `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Result.Content) != 1 || payload.Result.Content[0].Text != untrustedExternalDataNotice+`{"value":"completed"}` || payload.Result.Structured["value"] != "completed" {
		t.Fatalf("unexpected tool result %#v", payload.Result)
	}
}

func TestToolsCallRejectsOversizedBusinessData(t *testing.T) {
	fake := &fakeService{result: map[string]any{"value": strings.Repeat("x", maxToolResultBytes)}}
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"azure_subscriptions_list","arguments":{"connection_id":"conn-demo"}}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer synthetic-workload-proof")
	request.Header.Set("MCP-Protocol-Version", protocolVersion)
	response := httptest.NewRecorder()
	New(fake).ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"isError":true`) || !strings.Contains(response.Body.String(), "dependency_unavailable") {
		t.Fatalf("oversized response = %d %s", response.Code, response.Body.String())
	}
}

func TestMCPHTTPGuards(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		headers map[string]string
		status  int
	}{
		{name: "strict trailing payload", body: `{"jsonrpc":"2.0","id":1,"method":"initialize"}{}`, headers: map[string]string{"Authorization": "Bearer synthetic-workload-proof"}, status: http.StatusBadRequest},
		{name: "protocol header after initialize", body: `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, headers: map[string]string{"Authorization": "Bearer synthetic-workload-proof"}, status: http.StatusBadRequest},
		{name: "cross origin", body: `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, headers: map[string]string{"Authorization": "Bearer synthetic-workload-proof", "Origin": "https://untrusted.example.com"}, status: http.StatusForbidden},
		{name: "unsupported accept", body: `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, headers: map[string]string{"Authorization": "Bearer synthetic-workload-proof", "Accept": "text/plain"}, status: http.StatusNotAcceptable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			for key, value := range test.headers {
				request.Header.Set(key, value)
			}
			response := httptest.NewRecorder()
			New(&fakeService{}).ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.status, response.Body.String())
			}
		})
	}
}

func TestMCPNotificationHasNoResponse(t *testing.T) {
	fake := &fakeService{}
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer synthetic-workload-proof")
	request.Header.Set("MCP-Protocol-Version", protocolVersion)
	response := httptest.NewRecorder()
	New(fake).ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || response.Body.Len() != 0 {
		t.Fatalf("notification response = %d %q", response.Code, response.Body.String())
	}
}

func TestMCPExecutableMethodsRequireID(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		headers map[string]string
	}{
		{name: "null id", body: `{"jsonrpc":"2.0","id":null,"method":"initialize"}`},
		{name: "object id", body: `{"jsonrpc":"2.0","id":{},"method":"initialize"}`},
		{name: "boolean id", body: `{"jsonrpc":"2.0","id":true,"method":"initialize"}`},
		{name: "initialize", body: `{"jsonrpc":"2.0","method":"initialize"}`},
		{name: "tools list", body: `{"jsonrpc":"2.0","method":"tools/list"}`, headers: map[string]string{"MCP-Protocol-Version": protocolVersion}},
		{name: "tools call", body: `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"azure_subscriptions_list","arguments":{"connection_id":"conn-demo"}}}`, headers: map[string]string{"MCP-Protocol-Version": protocolVersion}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeService{}
			request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer synthetic-workload-proof")
			for key, value := range test.headers {
				request.Header.Set(key, value)
			}
			response := httptest.NewRecorder()
			New(fake).ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || fake.authCalls != 0 || fake.listCalls != 0 || fake.calls != 0 {
				t.Fatalf("status/auth/list/call = %d/%d/%d/%d, want 400/0/0/0", response.Code, fake.authCalls, fake.listCalls, fake.calls)
			}
		})
	}
}
