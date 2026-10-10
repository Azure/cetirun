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
