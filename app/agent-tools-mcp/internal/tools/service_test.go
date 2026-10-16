// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/CetiRun/app/agent-tools-mcp/internal/brokerclient"
	"github.com/Azure/CetiRun/app/agent-tools-mcp/internal/identity"
	"github.com/Azure/CetiRun/app/agent-tools-mcp/internal/tools/providers"
)

type fakeKubernetes struct {
	mu      sync.Mutex
	caller  identity.Caller
	records map[string]operationSecret
	creates int
	updates int
}

func (k *fakeKubernetes) Authenticate(_ context.Context, bearer string) (identity.Caller, error) {
	if bearer != k.caller.Proof {
		return identity.Caller{}, errors.New("invalid proof")
	}
	return k.caller, nil
}

func (k *fakeKubernetes) Get(_ context.Context, path string, result any) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	record, found := k.records[secretName(path)]
	if !found {
		return errors.New("Kubernetes API returned HTTP 404")
	}
	*result.(*operationSecret) = record
	return nil
}

func (k *fakeKubernetes) Create(_ context.Context, _ string, value, _ any) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.creates++
	secret := persistedSecret(value.(operationSecret), "1")
	if _, found := k.records[secret.Metadata.Name]; found {
		return errors.New("Kubernetes API returned HTTP 409")
	}
	k.records[secret.Metadata.Name] = secret
	return nil
}

func (k *fakeKubernetes) Update(_ context.Context, path string, value, _ any) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.updates++
	name := secretName(path)
	current, found := k.records[name]
	if !found || value.(operationSecret).Metadata.ResourceVersion != current.Metadata.ResourceVersion {
		return errors.New("Kubernetes API returned HTTP 409")
	}
	next := persistedSecret(value.(operationSecret), strconv.Itoa(k.updates+1))
	k.records[name] = next
	return nil
}

func persistedSecret(secret operationSecret, version string) operationSecret {
	record := secret.StringData["record.json"]
	secret.StringData = nil
	secret.Data = map[string]string{"record.json": base64.StdEncoding.EncodeToString([]byte(record))}
	secret.Metadata.ResourceVersion = version
	return secret
}

func secretName(path string) string { return path[strings.LastIndex(path, "/")+1:] }

type fakeBroker struct {
	list         brokerclient.ListResponse
	listErr      error
	check        brokerclient.CheckResponse
	issue        brokerclient.IssueResponse
	checkErr     error
	issueErr     error
	checkCalls   int
	issueCalls   int
	issueRequest brokerclient.Request
}

func (b *fakeBroker) List(context.Context, string) (brokerclient.ListResponse, error) {
	return b.list, b.listErr
}

func (b *fakeBroker) Check(_ context.Context, _ brokerclient.Request) (brokerclient.CheckResponse, error) {
	b.checkCalls++
	return b.check, b.checkErr
}

func (b *fakeBroker) Issue(_ context.Context, request brokerclient.Request) (brokerclient.IssueResponse, error) {
	b.issueCalls++
	b.issueRequest = request
	return b.issue, b.issueErr
}

type fakeProvider struct {
	result map[string]any
	err    error
	calls  int
	tokens []string
}

func (p *fakeProvider) Execute(_ context.Context, _ string, _ string, _ map[string]any, _ brokerclient.Constraints, token string) (map[string]any, error) {
	p.calls++
	p.tokens = append(p.tokens, token)
	return p.result, p.err
}

func TestServiceWriteCASReplaysTerminalOutcomes(t *testing.T) {
	tests := []struct {
		name     string
		result   map[string]any
		err      error
		wantCode string
	}{
		{name: "succeeded", result: map[string]any{"status": "succeeded", "result": map[string]any{"id": "42"}}},
		{name: "failed", err: &providers.Error{Code: "permission_denied"}, wantCode: "permission_denied"},
		{name: "unknown", err: &providers.Error{Code: "dependency_unavailable"}, wantCode: "execution_unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, broker, provider, kube := newTestService(test.result, test.err)
			caller, err := service.Authenticate(context.Background(), "synthetic-workload-proof")
			if err != nil {
				t.Fatalf("Authenticate() error = %v", err)
			}
			arguments := writeArguments("operation-replay")
			first, firstErr := service.Call(context.Background(), caller, "github_issues_create", arguments)
			assertToolCode(t, firstErr, test.wantCode)
			second, secondErr := service.Call(context.Background(), caller, "github_issues_create", arguments)
			assertToolCode(t, secondErr, test.wantCode)
			if test.wantCode == "" && (first["status"] != "succeeded" || second["status"] != "succeeded") {
				t.Fatalf("replayed success = %#v, %#v", first, second)
			}
			if provider.calls != 1 || kube.creates != 2 || kube.updates != 1 {
				t.Fatalf("provider/Create/Update calls = %d/%d/%d, want 1/2/1", provider.calls, kube.creates, kube.updates)
			}
			if broker.checkCalls != 2 || broker.issueCalls != 2 || broker.issueRequest.GrantRevision == nil || *broker.issueRequest.GrantRevision != broker.check.Grant.Revision {
				t.Fatalf("broker calls or revisions were not refreshed: check=%d issue=%d request=%#v", broker.checkCalls, broker.issueCalls, broker.issueRequest)
			}
		})
	}
}

func TestServiceWriteDoesNotRepeatOperationIDAfterRegrant(t *testing.T) {
	service, broker, provider, kube := newTestService(map[string]any{"status": "succeeded", "result": map[string]any{"id": "42"}}, nil)
	caller, err := service.Authenticate(context.Background(), "synthetic-workload-proof")
	if err != nil {
		t.Fatal(err)
	}
	arguments := writeArguments("operation-regrant")
	if _, err := service.Call(context.Background(), caller, "github_issues_create", arguments); err != nil {
		t.Fatalf("initial Call() error = %v", err)
	}
	expireStoredOperation(t, kube)
	if result, err := service.Call(context.Background(), caller, "github_issues_create", arguments); err != nil || result["status"] != "succeeded" {
		t.Fatalf("expired terminal Call() = %#v, %v", result, err)
	}
	broker.check.Grant.ID = "grant-recreated"
	broker.issue.CheckResponse = broker.check
	if _, err := service.Call(context.Background(), caller, "github_issues_create", arguments); toolErrorCode(err) != "execution_unknown" {
		t.Fatalf("regranted Call() error = %v, want execution_unknown", err)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls)
	}
}

func expireStoredOperation(t *testing.T, kube *fakeKubernetes) {
	t.Helper()
	kube.mu.Lock()
	defer kube.mu.Unlock()
	for name, secret := range kube.records {
		raw, err := base64.StdEncoding.DecodeString(secret.Data["record.json"])
		if err != nil {
			t.Fatal(err)
		}
		var record operationRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			t.Fatal(err)
		}
		record.ExpiresAt = time.Now().Add(-8 * 24 * time.Hour).UTC().Format(time.RFC3339)
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		secret.Data["record.json"] = base64.StdEncoding.EncodeToString(encoded)
		kube.records[name] = secret
		return
	}
	t.Fatal("operation record was not created")
}

func TestServiceRejectsInvalidAuthorizationBindings(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*fakeBroker)
	}{
		{name: "check connection id", mutate: func(b *fakeBroker) { b.check.Connection.ID = "other" }},
		{name: "check provider", mutate: func(b *fakeBroker) { b.check.Connection.Provider = "azure" }},
		{name: "check capability", mutate: func(b *fakeBroker) { b.check.Connection.Capabilities = []string{"github_comments_create"} }},
		{name: "check grant instance", mutate: func(b *fakeBroker) { b.check.Grant.InstanceUID = "other" }},
		{name: "check grant connection", mutate: func(b *fakeBroker) { b.check.Grant.ConnectionID = "other" }},
		{name: "check grant tool", mutate: func(b *fakeBroker) { b.check.Grant.Tools = []string{"github_comments_create"} }},
		{name: "check grant expiry", mutate: func(b *fakeBroker) { b.check.Grant.ExpiresAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339) }},
		{name: "issue instance", mutate: func(b *fakeBroker) { b.issue.InstanceUID = "other" }},
		{name: "issue revision", mutate: func(b *fakeBroker) { b.issue.Grant.Revision++ }},
		{name: "issue constraint", mutate: func(b *fakeBroker) { b.issue.Grant.Constraints.RepositoryIDs = []string{"99"} }},
		{name: "issue capability", mutate: func(b *fakeBroker) { b.issue.Connection.Capabilities = []string{"github_comments_create"} }},
		{name: "issue mode", mutate: func(b *fakeBroker) { b.issue.Connection.Mode = "agent" }},
		{name: "issue identity", mutate: func(b *fakeBroker) {
			b.issue.Connection.Identity = &brokerclient.IdentityBinding{InstanceUID: "instance-1", TenantID: "tenant-1", PrincipalID: "principal-1", ClientID: "client-1"}
		}},
		{name: "issue evidence", mutate: func(b *fakeBroker) {
			b.issue.Grant.Authorization = &brokerclient.ResourceAuthorization{State: "verified", Source: "direct_rbac", BindingRevision: 7}
		}},
		{name: "issue grant expiry", mutate: func(b *fakeBroker) {
			b.issue.Grant.ExpiresAt = time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
		}},
		{name: "credential expiry", mutate: func(b *fakeBroker) { b.issue.ExpiresAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, broker, provider, _ := newTestService(map[string]any{"status": "succeeded"}, nil)
			test.mutate(broker)
			_, err := service.Call(context.Background(), identity.Caller{Proof: "synthetic-workload-proof", InstanceUID: "instance-1"}, "github_issues_create", writeArguments("operation-bindings"))
			if toolErrorCode(err) != "authorization_changed" {
				t.Fatalf("Call() error = %v, want authorization_changed", err)
			}
			if provider.calls != 0 {
				t.Fatalf("provider calls = %d, want 0", provider.calls)
			}
		})
	}
}

func TestValidAuthorizationFailsClosedForIdentityEvidence(t *testing.T) {
	now := time.Now().UTC()
	valid := brokerclient.CheckResponse{
		InstanceUID: "instance-1", CatalogVersion: brokerclient.CatalogVersion,
		Connection: brokerclient.Connection{Mode: "agent", ID: "connection-1", Provider: "azure", Capabilities: []string{"azure_subscriptions_list"}, Revision: 7, Status: "connected", Identity: &brokerclient.IdentityBinding{InstanceUID: "instance-1", TenantID: "tenant-1", PrincipalID: "principal-1", ClientID: "client-1"}},
		Grant:      brokerclient.Grant{ID: "grant-1", InstanceUID: "instance-1", ConnectionID: "connection-1", Tools: []string{"azure_subscriptions_list"}, ExpiresAt: now.Add(time.Hour).Format(time.RFC3339), Revision: 11, Authorization: &brokerclient.ResourceAuthorization{State: "verified", Source: "direct_rbac", BindingRevision: 7, VerifiedAt: time.Now().Add(-time.Minute).Format(time.RFC3339), ValidUntil: now.Add(time.Hour).Format(time.RFC3339)}},
	}
	tests := []struct {
		name   string
		mutate func(*brokerclient.CheckResponse)
	}{
		{name: "valid", mutate: func(*brokerclient.CheckResponse) {}},
		{name: "unknown mode", mutate: func(value *brokerclient.CheckResponse) { value.Connection.Mode = "other" }},
		{name: "wrong identity uid", mutate: func(value *brokerclient.CheckResponse) { value.Connection.Identity.InstanceUID = "other" }},
		{name: "missing principal", mutate: func(value *brokerclient.CheckResponse) { value.Connection.Identity.PrincipalID = "" }},
		{name: "pending evidence", mutate: func(value *brokerclient.CheckResponse) { value.Grant.Authorization.State = "pending" }},
		{name: "expired evidence", mutate: func(value *brokerclient.CheckResponse) {
			value.Grant.Authorization.ValidUntil = now.Add(-time.Hour).Format(time.RFC3339)
		}},
		{name: "missing evidence expiry", mutate: func(value *brokerclient.CheckResponse) { value.Grant.Authorization.ValidUntil = "" }},
		{name: "missing verification time", mutate: func(value *brokerclient.CheckResponse) { value.Grant.Authorization.VerifiedAt = "" }},
		{name: "future verification time", mutate: func(value *brokerclient.CheckResponse) {
			value.Grant.Authorization.VerifiedAt = now.Add(time.Hour).Format(time.RFC3339)
		}},
		{name: "binding revision", mutate: func(value *brokerclient.CheckResponse) { value.Grant.Authorization.BindingRevision++ }},
		{name: "legacy identity", mutate: func(value *brokerclient.CheckResponse) { value.Connection.Mode = ""; value.Grant.Authorization = nil }},
		{name: "legacy evidence", mutate: func(value *brokerclient.CheckResponse) {
			value.Connection.Mode = "delegated"
			value.Connection.Identity = nil
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			value.Connection.Identity = cloneIdentity(valid.Connection.Identity)
			value.Grant.Authorization = cloneAuthorization(valid.Grant.Authorization)
			test.mutate(&value)
			got := validAuthorization(value, identity.Caller{InstanceUID: "instance-1"}, "azure_subscriptions_list", "connection-1")
			if got != (test.name == "valid") {
				t.Fatalf("validAuthorization() = %t", got)
			}
		})
	}
}

func cloneIdentity(value *brokerclient.IdentityBinding) *brokerclient.IdentityBinding {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func TestGitHubIndependentIdentityIsNotAccepted(t *testing.T) {
	service, broker, provider, _ := newTestService(map[string]any{"status": "succeeded"}, nil)
	broker.check.Connection.Mode = "agent"
	broker.check.Connection.Identity = &brokerclient.IdentityBinding{InstanceUID: "instance-1"}
	broker.check.Grant.Authorization = &brokerclient.ResourceAuthorization{State: "verified", Source: "github_app_installation", BindingRevision: broker.check.Connection.Revision}
	_, err := service.Call(context.Background(), identity.Caller{Proof: "synthetic-workload-proof", InstanceUID: "instance-1"}, "github_issues_create", writeArguments("operation-old-installation"))
	if toolErrorCode(err) != "authorization_changed" || provider.calls != 0 {
		t.Fatalf("legacy installation authorization = %v, provider calls = %d", err, provider.calls)
	}
}

func cloneAuthorization(value *brokerclient.ResourceAuthorization) *brokerclient.ResourceAuthorization {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func TestConnectionsStatusShowsModeAndSafeExecutorIdentity(t *testing.T) {
	service, broker, _, _ := newTestService(map[string]any{}, nil)
	broker.list = brokerclient.ListResponse{InstanceUID: "instance-1", CatalogVersion: brokerclient.CatalogVersion, Connections: []brokerclient.Connection{
		{ID: "agent", Provider: "azure", Mode: "agent", AccountDisplay: "must-not-be-used-for-agent", Identity: &brokerclient.IdentityBinding{InstanceUID: "instance-1", TenantID: "tenant-1", PrincipalID: "principal-1", ClientID: "client-1"}},
		{ID: "legacy", Provider: "github", AccountDisplay: "octocat"},
	}}
	result, err := service.Call(context.Background(), identity.Caller{Proof: "synthetic-workload-proof", InstanceUID: "instance-1"}, "connections_status", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	connections := result["connections"].([]any)
	agent := connections[0].(map[string]any)
	if agent["mode"] != "agent" || agent["executor_identity"].(map[string]any)["principal_id"] != "principal-1" {
		t.Fatalf("agent status = %#v", agent)
	}
	delegated := connections[1].(map[string]any)
	if delegated["mode"] != "delegated" || delegated["executor_identity"].(map[string]any)["type"] != "delegated" {
		t.Fatalf("delegated status = %#v", delegated)
	}
}

func TestServiceExecutionAuditDoesNotLogArgumentsOrCredentials(t *testing.T) {
	service, _, _, _ := newTestService(map[string]any{"status": "succeeded"}, nil)
	var output bytes.Buffer
	service.logger = slog.New(slog.NewJSONHandler(&output, nil))
	_, err := service.Call(context.Background(), identity.Caller{Proof: "synthetic-workload-proof", InstanceUID: "instance-1"}, "github_issues_create", writeArguments("operation-audit"))
	if err != nil {
		t.Fatal(err)
	}
	audit := output.String()
	for _, expected := range []string{`"agent_uid":"instance-1"`, `"mode":"delegated"`, `"provider":"github"`, `"connection_id":"connection-1"`, `"executor_principal":"delegated"`, `"grant_id":"grant-1"`, `"grant_revision":11`, `"tool":"github_issues_create"`, `"result":"succeeded"`} {
		if !strings.Contains(audit, expected) {
			t.Fatalf("audit missing %s: %s", expected, audit)
		}
	}
	for _, sensitive := range []string{"synthetic-access-token", "Synthetic issue", "synthetic-workload-proof", "repository_id"} {
		if strings.Contains(audit, sensitive) {
			t.Fatalf("audit leaked %q: %s", sensitive, audit)
		}
	}
}

func TestServiceRejectsChangedAgentEvidenceAndAuditsAgentPrincipal(t *testing.T) {
	service, broker, provider, _ := newTestService(map[string]any{"status": "succeeded"}, nil)
	expiresAt := time.Now().UTC().Add(time.Hour)
	check := agentCheckResponse(expiresAt)
	issue := agentCheckResponse(expiresAt)
	broker.check = check
	broker.issue.CheckResponse = issue
	broker.issue.ExpiresAt = expiresAt.Format(time.RFC3339)
	broker.issue.AccessToken = "agent-token-must-not-appear"

	// Provider-derived evidence changed after Check: fail before execution.
	broker.issue.Grant.Authorization.Scopes = []string{"/subscriptions/other"}
	_, err := service.Call(context.Background(), identity.Caller{Proof: "synthetic-workload-proof", InstanceUID: "instance-1"}, "azure_subscriptions_list", map[string]any{"connection_id": "connection-1"})
	if toolErrorCode(err) != "authorization_changed" || provider.calls != 0 {
		t.Fatalf("changed evidence result = %v, calls = %d", err, provider.calls)
	}

	broker.issue.CheckResponse = agentCheckResponse(expiresAt)
	var output bytes.Buffer
	service.logger = slog.New(slog.NewJSONHandler(&output, nil))
	_, err = service.Call(context.Background(), identity.Caller{Proof: "synthetic-workload-proof", InstanceUID: "instance-1"}, "azure_subscriptions_list", map[string]any{"connection_id": "connection-1"})
	if err != nil {
		t.Fatal(err)
	}
	audit := output.String()
	if !strings.Contains(audit, `"mode":"agent"`) || !strings.Contains(audit, `"executor_principal":"principal-1"`) || strings.Contains(audit, "agent-token-must-not-appear") {
		t.Fatalf("agent audit = %s", audit)
	}
}

func agentCheckResponse(expiresAt time.Time) brokerclient.CheckResponse {
	return brokerclient.CheckResponse{
		InstanceUID: "instance-1", CatalogVersion: brokerclient.CatalogVersion,
		Connection: brokerclient.Connection{Mode: "agent", ID: "connection-1", Provider: "azure", Capabilities: []string{"azure_subscriptions_list"}, Revision: 7, Status: "connected", Identity: &brokerclient.IdentityBinding{InstanceUID: "instance-1", TenantID: "tenant-1", PrincipalID: "principal-1", ClientID: "client-1"}},
		Grant:      brokerclient.Grant{ID: "grant-1", InstanceUID: "instance-1", ConnectionID: "connection-1", Tools: []string{"azure_subscriptions_list"}, ExpiresAt: expiresAt.Format(time.RFC3339), Revision: 11, Authorization: &brokerclient.ResourceAuthorization{State: "verified", Source: "direct_rbac", BindingRevision: 7, VerifiedAt: time.Now().Add(-time.Minute).Format(time.RFC3339), ValidUntil: expiresAt.Format(time.RFC3339)}},
	}
}

func TestServiceWriteRevocationDoesNotCallProvider(t *testing.T) {
	service, broker, provider, _ := newTestService(map[string]any{"status": "succeeded"}, nil)
	broker.issueErr = &brokerclient.Error{Code: "authorization_changed"}
	_, err := service.Call(context.Background(), identity.Caller{Proof: "synthetic-workload-proof", InstanceUID: "instance-1"}, "github_issues_create", writeArguments("operation-revoked"))
	if toolErrorCode(err) != "authorization_changed" {
		t.Fatalf("Call() error = %v, want authorization_changed", err)
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", provider.calls)
	}
}

func newTestService(result map[string]any, providerErr error) (*Service, *fakeBroker, *fakeProvider, *fakeKubernetes) {
	now := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	check := brokerclient.CheckResponse{
		InstanceUID:    "instance-1",
		CatalogVersion: brokerclient.CatalogVersion,
		Connection:     brokerclient.Connection{ID: "connection-1", Provider: "github", Capabilities: []string{"github_issues_create"}, Revision: 7, Status: "connected"},
		Grant:          brokerclient.Grant{ID: "grant-1", InstanceUID: "instance-1", ConnectionID: "connection-1", Tools: []string{"github_issues_create"}, Constraints: brokerclient.Constraints{RepositoryIDs: []string{"123"}}, ExpiresAt: now, Revision: 11},
	}
	broker := &fakeBroker{check: check, issue: brokerclient.IssueResponse{CheckResponse: check, AccessToken: "synthetic-access-token", ExpiresAt: now}}
	provider := &fakeProvider{result: result, err: providerErr}
	kube := &fakeKubernetes{caller: identity.Caller{Proof: "synthetic-workload-proof", InstanceUID: "instance-1"}, records: map[string]operationSecret{}}
	return &Service{kubernetes: kube, broker: broker, providers: provider, operations: &operations{kubernetes: kube, namespace: "operations"}}, broker, provider, kube
}

func writeArguments(operationID string) map[string]any {
	return map[string]any{"connection_id": "connection-1", "repository_id": "123", "title": "Synthetic issue", "operation_id": operationID}
}

func assertToolCode(t *testing.T, err error, want string) {
	t.Helper()
	if got := toolErrorCode(err); got != want {
		t.Fatalf("error code = %q, want %q (%v)", got, want, err)
	}
}

func toolErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if typed, ok := err.(*ToolError); ok {
		return typed.Code
	}
	return fmt.Sprintf("unexpected:%v", err)
}
