// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.
package tools

import (
	"bytes"
	"context"
	"github.com/Azure/CetiRun/app/agent-tools-mcp/internal/brokerclient"
	"github.com/Azure/CetiRun/app/agent-tools-mcp/internal/identity"
	"github.com/Azure/CetiRun/app/agent-tools-mcp/internal/tools/providers"
	"github.com/Azure/CetiRun/app/internal/agenttools"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type fakeBroker struct {
	check       brokerclient.CheckResponse
	issueChange func(*brokerclient.IssueResponse)
	err         error
	issued      int
}

func (f *fakeBroker) List(context.Context, string) (brokerclient.ListResponse, error) {
	return brokerclient.ListResponse{InstanceUID: f.check.InstanceUID, Connections: []agenttools.Connection{f.check.Connection}, Bindings: []agenttools.Binding{f.check.Binding}, ContractVersion: agenttools.ContractVersion}, f.err
}
func (f *fakeBroker) Check(context.Context, brokerclient.Request) (brokerclient.CheckResponse, error) {
	return f.check, f.err
}
func (f *fakeBroker) Issue(_ context.Context, r brokerclient.Request) (brokerclient.IssueResponse, error) {
	f.issued++
	if r.BindingRevision == nil || *r.BindingRevision != f.check.Binding.Revision || r.ConnectionRevision == nil || *r.ConnectionRevision != f.check.Connection.Revision {
		panic("revision not forwarded")
	}
	response := brokerclient.IssueResponse{CheckResponse: f.check, AccessToken: "synthetic-private-token", ExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339)}
	if f.issueChange != nil {
		f.issueChange(&response)
	}
	return response, f.err
}

type fakeProvider struct {
	calls int
	err   error
}

func (f *fakeProvider) Execute(_ context.Context, _ providers.Request, token, id string) (providers.Result, error) {
	f.calls++
	if token != "synthetic-private-token" || id == "" {
		panic("missing credential or correlation")
	}
	return providers.Result{Status: 403, Body: map[string]any{"error": "ProviderDenied"}, RequestIDs: []string{"provider-request-id"}}, f.err
}
func serviceFixture() (*Service, *fakeBroker, *fakeProvider, identity.Caller, *bytes.Buffer) {
	caller := identity.Caller{InstanceUID: "agent-uid", Proof: "private-workload-proof"}
	broker := &fakeBroker{check: brokerclient.CheckResponse{InstanceUID: caller.InstanceUID, Service: "github", ContractVersion: agenttools.ContractVersion, Connection: agenttools.Connection{ID: "c", Mode: "delegated", Provider: "github", Status: "connected", AccountDisplay: "person@example.com", Services: []string{"github"}, Revision: 3}, Binding: agenttools.Binding{ID: "b", InstanceUID: caller.InstanceUID, ConnectionID: "c", Services: []string{"github"}, Enabled: true, Revision: 4}}}
	provider := &fakeProvider{}
	log := &bytes.Buffer{}
	return &Service{broker: broker, providers: provider, logger: slog.New(slog.NewJSONHandler(log, nil))}, broker, provider, caller, log
}
func arguments() map[string]any {
	return map[string]any{"connection_id": "c", "service": "github", "method": "POST", "path": "/repos/example/project/issues", "body": map[string]any{"title": "private-body"}}
}
func TestProviderPermissionsDecideWithoutOperationGrant(t *testing.T) {
	s, b, p, caller, log := serviceFixture()
	result, err := s.Call(context.Background(), caller, "service_request", arguments())
	if err != nil || result["status"] != 403 || p.calls != 1 || b.issued != 1 {
		t.Fatalf("provider request blocked: %v %#v", err, result)
	}
	for _, secret := range []string{"private-body", "private-workload-proof", "synthetic-private-token"} {
		if strings.Contains(log.String(), secret) {
			t.Fatal("audit leaked sensitive data")
		}
	}
	for _, expected := range []string{"agent-uid", "person@example.com", "provider-request-id", "correlation_id", "403"} {
		if !strings.Contains(log.String(), expected) {
			t.Fatalf("audit missing %s", expected)
		}
	}
}
func TestBindingAndIssuanceChangesStopExecution(t *testing.T) {
	for _, change := range []func(*brokerclient.IssueResponse){func(r *brokerclient.IssueResponse) { r.Binding.Enabled = false }, func(r *brokerclient.IssueResponse) { r.Binding.Revision++ }, func(r *brokerclient.IssueResponse) { r.Connection.Revision++ }, func(r *brokerclient.IssueResponse) { r.InstanceUID = "other-agent" }, func(r *brokerclient.IssueResponse) { r.Binding.Services = []string{"azure_arm"} }, func(r *brokerclient.IssueResponse) { r.ExpiresAt = time.Now().Add(-time.Hour).Format(time.RFC3339) }} {
		s, b, p, caller, _ := serviceFixture()
		b.issueChange = change
		if _, err := s.Call(context.Background(), caller, "service_request", arguments()); err == nil || p.calls != 0 {
			t.Fatal("changed authorization reached provider")
		}
	}
}
func TestIsolationAndPlatformRejectionAudit(t *testing.T) {
	s, b, p, caller, log := serviceFixture()
	b.check.Binding.InstanceUID = "other-agent"
	_, err := s.Call(context.Background(), caller, "service_request", arguments())
	if err == nil || p.calls != 0 || b.issued != 0 || !strings.Contains(log.String(), "authorization_changed") || err.(*ToolError).CorrelationID == "" {
		t.Fatal("cross-Agent connection accepted or rejection unaudited")
	}
}
func TestStableCatalogAndDynamicServiceDiscovery(t *testing.T) {
	s, b, _, caller, _ := serviceFixture()
	before, err := s.List(context.Background(), caller)
	if err != nil || len(before) != 4 {
		t.Fatal("generic catalog unavailable")
	}
	b.check.Binding.Enabled = false
	after, err := s.List(context.Background(), caller)
	if err != nil || len(after) != len(before) {
		t.Fatal("binding changes altered catalog")
	}
	result, err := s.Call(context.Background(), caller, "services_list", map[string]any{})
	if err != nil || len(result["services"].([]any)) != 0 {
		t.Fatal("disabled binding remained discoverable")
	}
}
