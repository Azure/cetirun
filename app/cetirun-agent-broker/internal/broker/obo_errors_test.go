// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAuthorizationHeaderClassifiesFailures(t *testing.T) {
	const privateDetail = "synthetic-private-sdk-detail"
	for _, test := range []struct {
		name       string
		status     int
		body       string
		transport  error
		code       string
		wantStatus int
	}{
		{name: "unauthorized empty", status: 401, code: "permission_denied", wantStatus: 403},
		{name: "unauthorized text", status: 401, body: privateDetail, code: "permission_denied", wantStatus: 403},
		{name: "forbidden JSON", status: 403, body: `{"error":"` + privateDetail + `"}`, code: "permission_denied", wantStatus: 403},
		{name: "forbidden malformed JSON", status: 403, body: `{"error":`, code: "permission_denied", wantStatus: 403},
		{name: "throttled", status: 429, body: privateDetail, code: "throttled", wantStatus: 429},
		{name: "server error", status: 500, body: privateDetail},
		{name: "redirect", status: 302, body: privateDetail},
		{name: "transport", transport: errors.New(privateDetail)},
		{name: "timeout", transport: context.DeadlineExceeded},
		{name: "invalid JSON", status: 200, body: `{"authorizationHeader":`},
		{name: "missing bearer", status: 200, body: `{"authorizationHeader":"` + privateDetail + `"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := &authSDKClient{baseURL: "http://127.0.0.1:5000", client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				if test.transport != nil {
					return nil, test.transport
				}
				return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body)), Header: make(http.Header)}, nil
			})}}
			header, err := client.authorizationHeaderFor(context.Background(), "synthetic-user-token", testFirstAgentID, "MicrosoftGraph")
			if err == nil || header != "" || calls != 1 {
				t.Fatalf("failed OBO returned a header or retried: error=%v calls=%d", err, calls)
			}
			var classified toolAuthorizationError
			if test.code != "" {
				if !errors.As(err, &classified) || classified.code != test.code || classified.status != test.wantStatus {
					t.Fatalf("OBO error = %v, want %d/%s", err, test.wantStatus, test.code)
				}
			} else if errors.As(err, &classified) {
				t.Fatalf("dependency failure was classified as authorization: %v", err)
			}
			if strings.Contains(err.Error(), privateDetail) || strings.Contains(err.Error(), "synthetic-user-token") {
				t.Fatal("OBO error exposed private SDK data")
			}
		})
	}
}

func TestIssuanceClassifiesOBOFailuresWithoutChangingConnection(t *testing.T) {
	for _, service := range []struct{ provider, service, downstream string }{
		{"azure", "azure_arm", "AzureResourceManager"},
		{"microsoft", "microsoft_graph", "MicrosoftGraph"},
	} {
		for _, test := range []struct {
			status     int
			code       string
			wantStatus int
		}{
			{401, "permission_denied", 403},
			{403, "permission_denied", 403},
			{429, "throttled", 429},
			{500, "dependency_unavailable", 503},
		} {
			t.Run(service.service+"/"+http.StatusText(test.status), func(t *testing.T) {
				fixture := newAuthorizationFixture(t, service.provider, service.service)
				fixture.enableEntra(t)
				before, _, err := fixture.server.store.getConnection(context.Background(), fixture.connection.Connection.ID)
				if err != nil {
					t.Fatal(err)
				}
				var logs bytes.Buffer
				fixture.server.logger = slog.New(slog.NewJSONHandler(&logs, nil))
				calls := 0
				fixture.server.authSDK = &authSDKClient{baseURL: "http://127.0.0.1:5000", client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
					calls++
					if request.URL.Path != "/AuthorizationHeader/"+service.downstream {
						t.Fatalf("unexpected OBO endpoint %s", request.URL.Path)
					}
					return securityJSONResponse(t, test.status, map[string]string{"error": "synthetic-private-sdk-detail"}), nil
				})}}
				response := fixture.request(t, "/internal/v1/service-credentials/issue")
				var payload apiError
				if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
					t.Fatal(err)
				}
				if response.Code != test.wantStatus || payload.Error.Code != test.code || payload.Error.CorrelationID == "" || calls != 1 {
					t.Fatalf("issuance = %d/%s calls=%d, want %d/%s and one SDK call", response.Code, payload.Error.Code, calls, test.wantStatus, test.code)
				}
				for _, private := range []string{"synthetic-private-sdk-detail", "synthetic-provider-token", "synthetic-refresh-token", "synthetic-workload-proof", `"accessToken"`} {
					if strings.Contains(response.Body.String(), private) || strings.Contains(logs.String(), private) {
						t.Fatal("issuance failure exposed private data")
					}
				}
				after, _, err := fixture.server.store.getConnection(context.Background(), fixture.connection.Connection.ID)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("OBO failure changed stored connection or credential: %v", err)
				}
			})
		}
	}
}

func TestIssuancePreservesRefreshInvalidGrant(t *testing.T) {
	fixture := newAuthorizationFixture(t, "google", "google_gmail")
	before, version, err := fixture.server.store.getConnection(context.Background(), fixture.connection.Connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	before.Credential.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	if err := fixture.server.store.putConnection(context.Background(), before, version); err != nil {
		t.Fatal(err)
	}
	fixture.server.providers = &providerClient{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return securityJSONResponse(t, http.StatusBadRequest, map[string]string{"error": "invalid_grant"}), nil
	})}}
	response := fixture.request(t, "/internal/v1/service-credentials/issue")
	var payload apiError
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusUnauthorized || payload.Error.Code != "interaction_required" || strings.Contains(response.Body.String(), `"accessToken"`) {
		t.Fatalf("refresh rejection = %d/%s, want 401/interaction_required without credential", response.Code, payload.Error.Code)
	}
	after, _, err := fixture.server.store.getConnection(context.Background(), fixture.connection.Connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Connection.Status != "interaction_required" || after.Connection.Revision != before.Connection.Revision+1 || !reflect.DeepEqual(before.Credential, after.Credential) {
		t.Fatal("explicit invalid_grant did not retain the credential and mark the connection for interaction")
	}
}
