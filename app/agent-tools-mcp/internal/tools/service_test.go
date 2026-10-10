// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
	check        brokerclient.CheckResponse
	issue        brokerclient.IssueResponse
	checkErr     error
	issueErr     error
	checkCalls   int
	issueCalls   int
	issueRequest brokerclient.Request
}

func (b *fakeBroker) List(context.Context, string) (brokerclient.ListResponse, error) {
	return brokerclient.ListResponse{}, nil
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
