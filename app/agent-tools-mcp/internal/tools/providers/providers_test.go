// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Azure/CetiRun/app/agent-tools-mcp/internal/brokerclient"
)

func TestGraphDateConvertsOffsetsToSupportedUTCZone(t *testing.T) {
	value := graphDate("2026-10-10T10:30:00+08:00")
	if value["dateTime"] != "2026-10-10T02:30:00" || value["timeZone"] != "UTC" {
		t.Fatalf("unexpected Graph date %#v", value)
	}
}

func TestGraphCalendarReportsProviderPagination(t *testing.T) {
	client := New(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"value":[],"@odata.nextLink":"https://graph.microsoft.com/v1.0/next"}`))}, nil
	})})
	result, err := client.Execute(context.Background(), "microsoft", "calendar_events_list", map[string]any{"calendar_id": "calendar-a", "start": "2026-10-10T00:00:00Z", "end": "2026-10-11T00:00:00Z"}, brokerclient.Constraints{CalendarIDs: []string{"calendar-a"}}, "synthetic-token")
	if err != nil || result["truncated"] != true {
		t.Fatalf("pagination hidden: %#v, %v", result, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestGitHubContentsResolvesStableIDToCanonicalRepository(t *testing.T) {
	requests := 0
	client := New(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.Header.Get("Authorization") != "Bearer synthetic-provider-token" {
			t.Fatal("provider request omitted authorization")
		}
		switch request.URL.Path {
		case "/repositories/9007199254740993":
			return jsonResponse(`{"id":9007199254740993,"full_name":"octo/demo"}`), nil
		case "/repos/octo/demo/contents/docs/readme.md":
			return jsonResponse(`{"path":"docs/readme.md","sha":"synthetic","encoding":"base64","content":"aGVsbG8="}`), nil
		default:
			t.Fatalf("unexpected endpoint %s", request.URL.String())
			return nil, nil
		}
	})})
	result, err := client.Execute(context.Background(), "github", "github_contents_get", map[string]any{"repository_id": "9007199254740993", "path": "docs/readme.md"}, brokerclient.Constraints{RepositoryIDs: []string{"9007199254740993"}}, "synthetic-provider-token")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result["content"] != "hello" || result["truncated"] != false || requests != 2 {
		t.Fatalf("unexpected result %#v after %d requests", result, requests)
	}
}

func TestGitHubContentsRejectsTraversalWithoutProviderCall(t *testing.T) {
	calls := 0
	client := New(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, nil })})
	_, err := client.Execute(context.Background(), "github", "github_contents_get", map[string]any{"repository_id": "123", "path": "../secret"}, brokerclient.Constraints{RepositoryIDs: []string{"123"}}, "synthetic-provider-token")
	if typed, ok := err.(*Error); !ok || typed.Code != "invalid_request" {
		t.Fatalf("error = %v, want invalid_request", err)
	}
	if calls != 0 {
		t.Fatalf("provider calls = %d, want 0", calls)
	}
}

func TestRequestIDCaptureUsesOnlySafeProviderHeaders(t *testing.T) {
	requestIDs := []string{}
	ctx := WithRequestIDCapture(context.Background(), func(value string) { requestIDs = append(requestIDs, value) })
	headers := make(http.Header)
	headers.Set("X-Ms-Request-Id", "azure-request-1")
	headers.Set("X-GitHub-Request-Id", "github/request:2")
	headers.Set("Request-Id", "unsafe value\nnext")
	captureRequestID(ctx, headers)
	if !reflect.DeepEqual(requestIDs, []string{"azure-request-1", "github/request:2"}) {
		t.Fatalf("request IDs = %#v", requestIDs)
	}
}

func TestGitHubRejectsRedirectFromInjectedClient(t *testing.T) {
	calls := 0
	client := New(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://example.invalid/redirect"}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})})
	_, err := client.Execute(context.Background(), "github", "github_repositories_get", map[string]any{"repository_id": "123"}, brokerclient.Constraints{RepositoryIDs: []string{"123"}}, "synthetic-provider-token")
	if typed, ok := err.(*Error); !ok || typed.Code != "dependency_unavailable" {
		t.Fatalf("error = %v, want dependency_unavailable", err)
	}
	if calls != 1 {
		t.Fatalf("redirect requests = %d, want 1", calls)
	}
}

func TestGitHubRevokedOAuthRequiresReconnect(t *testing.T) {
	for _, test := range []struct {
		name, endpoint, code string
		status               int
	}{
		{"revoked GitHub token", "https://api.github.com/repositories/123", "interaction_required", http.StatusUnauthorized},
		{"GitHub repository or organization restriction", "https://api.github.com/repositories/123", "permission_denied", http.StatusForbidden},
		{"Azure behavior preserved", "https://management.azure.com/subscriptions", "permission_denied", http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := New(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
			})})
			err := client.request(context.Background(), http.MethodGet, test.endpoint, nil, "synthetic-provider-token", nil)
			if typed, ok := err.(*Error); !ok || typed.Code != test.code || typed.Unknown {
				t.Fatalf("error = %#v, want %s without uncertain execution", err, test.code)
			}
		})
	}
}

func TestGitHubWriteWithEmptySuccessIsUnknown(t *testing.T) {
	step := 0
	client := New(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		step++
		if step == 1 {
			return jsonResponse(`{"id":123,"full_name":"octo/demo"}`), nil
		}
		if request.URL.Path != "/repos/octo/demo/issues" {
			t.Fatalf("unexpected endpoint %s", request.URL.Path)
		}
		return jsonResponse(`{}`), nil
	})})
	_, err := client.Execute(context.Background(), "github", "github_issues_create", map[string]any{"repository_id": "123", "title": "Synthetic"}, brokerclient.Constraints{RepositoryIDs: []string{"123"}}, "synthetic-provider-token")
	if typed, ok := err.(*Error); !ok || !typed.Unknown || typed.Code != "dependency_unavailable" {
		t.Fatalf("error = %#v, want unknown dependency_unavailable", err)
	}
}

func TestInvalidWriteResponseIsUnknown(t *testing.T) {
	for _, payload := range []string{"", "not-json"} {
		t.Run(payload, func(t *testing.T) {
			client := New(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(payload), nil
			})})
			var result map[string]any
			err := client.request(context.Background(), http.MethodPost, "https://example.invalid/write", map[string]string{"value": "synthetic"}, "synthetic-provider-token", &result)
			if typed, ok := err.(*Error); !ok || !typed.Unknown || typed.Code != "dependency_unavailable" {
				t.Fatalf("error = %#v, want unknown dependency_unavailable", err)
			}
		})
	}
}

func TestAzureResourceShowUsesExactResourceGraphQuery(t *testing.T) {
	resourceID := "/subscriptions/123/resourceGroups/demo/providers/Microsoft.Test/widgets/widget-1"
	client := New(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/providers/Microsoft.ResourceGraph/resources" || request.URL.Query().Get("api-version") != "2021-03-01" {
			t.Fatalf("unexpected endpoint %s %s", request.Method, request.URL.String())
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || !strings.Contains(body["query"].(string), "id =~ '"+resourceID+"'") {
			t.Fatalf("unexpected query %#v (%v)", body, err)
		}
		return jsonResponse(`{"data":[{"id":"` + resourceID + `","name":"widget-1"}]}`), nil
	})})
	result, err := client.Execute(context.Background(), "azure", "azure_resource_show", map[string]any{"resource_id": resourceID}, brokerclient.Constraints{SubscriptionIDs: []string{"123"}}, "synthetic-provider-token")
	if err != nil || result["resource"].(map[string]any)["id"] != resourceID {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestAzureResourceShowRejectsUnsafeResourceIDWithoutProviderCall(t *testing.T) {
	calls := 0
	client := New(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, nil })})
	_, err := client.Execute(context.Background(), "azure", "azure_resource_show", map[string]any{"resource_id": "/subscriptions/123/resourceGroups/demo/providers/Microsoft.Test/widgets/../secret"}, brokerclient.Constraints{SubscriptionIDs: []string{"123"}}, "synthetic-provider-token")
	if typed, ok := err.(*Error); !ok || typed.Code != "permission_denied" || calls != 0 {
		t.Fatalf("error = %v, calls = %d", err, calls)
	}
}

func TestAzureResourceShowBindsReturnedResourceID(t *testing.T) {
	resourceID := "/subscriptions/123/providers/Microsoft.Test/widgets/widget-1"
	client := New(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(`{"data":[{"id":"/subscriptions/123/providers/Microsoft.Test/widgets/other"}]}`), nil
	})})
	_, err := client.Execute(context.Background(), "azure", "azure_resource_show", map[string]any{"resource_id": resourceID}, brokerclient.Constraints{SubscriptionIDs: []string{"123"}}, "synthetic-provider-token")
	if typed, ok := err.(*Error); !ok || typed.Code != "permission_denied" {
		t.Fatalf("error = %v, want permission_denied", err)
	}
}

func TestAzureResourceGroupCreateChecksAbsenceAndUsesFixedPUT(t *testing.T) {
	const subscription = "11111111-1111-1111-1111-111111111111"
	const resourceGroup = "Agent-Test-RG"
	calls := 0
	client := New(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			if request.Method != http.MethodGet || request.URL.String() != "https://management.azure.com/subscriptions/"+subscription+"/resourceGroups/"+resourceGroup+"?api-version=2021-04-01" {
				t.Fatalf("unexpected existence request %s %s", request.Method, request.URL)
			}
			return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{"x-ms-request-id": []string{"absence-check"}}, Body: io.NopCloser(strings.NewReader(`{"error":"not found"}`))}, nil
		case 2:
			if request.Method != http.MethodPut || request.URL.String() != "https://management.azure.com/subscriptions/"+subscription+"/resourceGroups/"+resourceGroup+"?api-version=2021-04-01" {
				t.Fatalf("unexpected create request %s %s", request.Method, request.URL)
			}
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || !reflect.DeepEqual(body, map[string]any{"location": "eastus"}) {
				t.Fatalf("create body = %#v, error = %v", body, err)
			}
			return &http.Response{StatusCode: http.StatusCreated, Header: http.Header{"x-ms-request-id": []string{"create-request"}, "x-ms-correlation-request-id": []string{"create-correlation"}}, Body: io.NopCloser(strings.NewReader(`{"id":"/subscriptions/` + subscription + `/resourceGroups/` + resourceGroup + `","name":"` + resourceGroup + `","location":"eastus","properties":{"provisioningState":"Succeeded"}}`))}, nil
		default:
			t.Fatalf("unexpected provider request %d", calls)
			return nil, nil
		}
	})})
	result, err := client.Execute(context.Background(), "azure", "azure_resource_groups_create", map[string]any{"subscription_id": subscription, "resource_group_name": resourceGroup, "location": "eastus", "operation_id": "create-rg-1"}, brokerclient.Constraints{SubscriptionIDs: []string{subscription}, ResourceGroupNames: []string{"agent-test-rg"}}, "synthetic-provider-token")
	if err != nil || calls != 2 {
		t.Fatalf("result = %#v, error = %v, calls = %d", result, err, calls)
	}
	writeResult := result["result"].(map[string]any)
	if result["status"] != "succeeded" || writeResult["resource_group_id"] != "/subscriptions/"+subscription+"/resourceGroups/"+resourceGroup || writeResult["provisioning_state"] != "Succeeded" || writeResult["request_id"] != "create-request" || writeResult["correlation_id"] != "create-correlation" {
		t.Fatalf("unexpected create result %#v", result)
	}
}

func TestAzureResourceGroupCreateRejectsExistingGroupWithoutPUT(t *testing.T) {
	calls := 0
	client := New(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Method != http.MethodGet {
			t.Fatalf("unexpected request %s", request.Method)
		}
		return jsonResponse(`{"id":"existing"}`), nil
	})})
	_, err := client.Execute(context.Background(), "azure", "azure_resource_groups_create", map[string]any{"subscription_id": "123", "resource_group_name": "agent-test-rg", "location": "eastus", "operation_id": "create-rg-1"}, brokerclient.Constraints{SubscriptionIDs: []string{"123"}, ResourceGroupNames: []string{"agent-test-rg"}}, "synthetic-provider-token")
	if typed, ok := err.(*Error); !ok || typed.Code != "invalid_request" || typed.Unknown || calls != 1 {
		t.Fatalf("error = %#v, calls = %d", err, calls)
	}
}

func TestAzureResourceGroupTagsUpdateUsesFixedMergePayload(t *testing.T) {
	const subscription = "123"
	const resourceGroup = "agent-test-rg"
	client := New(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPatch || request.URL.String() != "https://management.azure.com/subscriptions/123/resourceGroups/agent-test-rg/providers/Microsoft.Resources/tags/default?api-version=2021-04-01" {
			t.Fatalf("unexpected tags request %s %s", request.Method, request.URL)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || !reflect.DeepEqual(body, map[string]any{"operation": "Merge", "properties": map[string]any{"tags": map[string]any{"purpose": "agent-audit", "environment": "test"}}}) {
			t.Fatalf("tags body = %#v, error = %v", body, err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"x-ms-request-id": []string{"tags-request"}, "x-ms-correlation-request-id": []string{"tags-correlation"}}, Body: io.NopCloser(strings.NewReader(`{"id":"/subscriptions/123/resourceGroups/agent-test-rg/providers/Microsoft.Resources/tags/default"}`))}, nil
	})})
	result, err := client.Execute(context.Background(), "azure", "azure_resource_group_tags_update", map[string]any{"subscription_id": subscription, "resource_group_name": resourceGroup, "tags": map[string]any{"purpose": "agent-audit", "environment": "test"}, "operation_id": "tag-rg-01"}, brokerclient.Constraints{SubscriptionIDs: []string{subscription}, ResourceGroupNames: []string{resourceGroup}}, "synthetic-provider-token")
	if err != nil || result["result"].(map[string]any)["request_id"] != "tags-request" || result["result"].(map[string]any)["correlation_id"] != "tags-correlation" {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
}

func TestAzureWriteRejectsOutOfScopeOrInvalidArgumentsWithoutProviderCall(t *testing.T) {
	for _, test := range []struct {
		name string
		tool string
		args map[string]any
	}{
		{"subscription", "azure_resource_groups_create", map[string]any{"subscription_id": "outside", "resource_group_name": "agent-test-rg", "location": "eastus", "operation_id": "create-rg-1"}},
		{"resource group", "azure_resource_groups_create", map[string]any{"subscription_id": "123", "resource_group_name": "other-rg", "location": "eastus", "operation_id": "create-rg-1"}},
		{"traversal", "azure_resource_groups_create", map[string]any{"subscription_id": "123", "resource_group_name": "../agent-test-rg", "location": "eastus", "operation_id": "create-rg-1"}},
		{"trailing dot", "azure_resource_groups_create", map[string]any{"subscription_id": "123", "resource_group_name": "agent-test-rg.", "location": "eastus", "operation_id": "create-rg-1"}},
		{"tag bounds", "azure_resource_group_tags_update", map[string]any{"subscription_id": "123", "resource_group_name": "agent-test-rg", "tags": tooManyTags(), "operation_id": "tag-rg-01"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := New(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, nil })})
			_, err := client.Execute(context.Background(), "azure", test.tool, test.args, brokerclient.Constraints{SubscriptionIDs: []string{"123"}, ResourceGroupNames: []string{"agent-test-rg"}}, "synthetic-provider-token")
			if err == nil || calls != 0 {
				t.Fatalf("error = %v, provider calls = %d", err, calls)
			}
		})
	}
}

func tooManyTags() map[string]any {
	tags := make(map[string]any, 51)
	for index := 0; index < 51; index++ {
		tags["tag-"+strconv.Itoa(index)] = "value"
	}
	return tags
}

func TestGmailMessageRejectsHeaderInjectionAndEncodesSubject(t *testing.T) {
	if _, err := gmailMessage(map[string]any{"to": []any{"person@example.com"}, "subject": "safe\r\nBcc: injected@example.com", "body": "Synthetic"}); err == nil {
		t.Fatal("CR/LF subject was accepted")
	}
	raw, err := gmailMessage(map[string]any{"to": []any{"person@example.com"}, "subject": "日本語", "body": "Synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	message, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || !strings.Contains(string(message), "Subject: =?UTF-8?q?") || strings.Contains(string(message), "\nBcc:") {
		t.Fatalf("unexpected encoded message %q (%v)", message, err)
	}
}

func TestGmailMessageGetDecodesBoundedPlainText(t *testing.T) {
	body := base64.RawURLEncoding.EncodeToString([]byte("Plain text body"))
	client := New(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Query().Get("format") != "full" {
			t.Fatalf("message format = %q, want full", request.URL.Query().Get("format"))
		}
		return jsonResponse(`{"id":"message-1","payload":{"mimeType":"multipart/alternative","headers":[{"name":"Subject","value":"=?UTF-8?q?Example?="}],"parts":[{"mimeType":"text/plain","body":{"data":"` + body + `"}}]}}`), nil
	})})
	result, err := client.Execute(context.Background(), "google", "mail_messages_get", map[string]any{"message_id": "message-1"}, brokerclient.Constraints{}, "synthetic-provider-token")
	if err != nil || result["message"].(map[string]any)["body"] != "Plain text body" || result["message"].(map[string]any)["subject"] != "Example" {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	oversize := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", maxMailBodyBytes+1)))
	if _, err := gmailTextBody(map[string]any{"mimeType": "text/plain", "body": map[string]any{"data": oversize}}); err == nil {
		t.Fatal("oversized body was accepted")
	}
}

func TestPageTruncatedAtBoundedLimit(t *testing.T) {
	if !pageTruncated(maxItems, false, false) {
		t.Fatal("a full bounded page was reported complete")
	}
	if pageTruncated(maxItems-1, false, false) {
		t.Fatal("a partial final page was reported truncated")
	}
}

func TestProviderErrorsDoNotIncludeAccessToken(t *testing.T) {
	client := New(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("synthetic-provider-token transport failure")
	})})
	_, err := client.Execute(context.Background(), "azure", "azure_subscriptions_list", map[string]any{}, brokerclient.Constraints{}, "synthetic-provider-token")
	if err == nil || strings.Contains(err.Error(), "synthetic-provider-token") {
		t.Fatalf("provider error exposed token: %v", err)
	}
}

func jsonResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}
