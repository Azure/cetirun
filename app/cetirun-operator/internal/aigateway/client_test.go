// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package aigateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type recordedRequest struct {
	method, path, query, auth string
	body                      map[string]any
}

func testServer(t *testing.T, handler func(recordedRequest) (int, string)) (*Client, *[]recordedRequest) {
	t.Helper()
	var requests []recordedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		req := recordedRequest{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, auth: r.Header.Get("Authorization")}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &req.body); err != nil {
				t.Fatalf("request body is not JSON: %v", err)
			}
		}
		requests = append(requests, req)
		status, body := handler(req)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	client, err := New(server.URL+"/", "sk-master", nil)
	if err != nil {
		t.Fatal(err)
	}
	return client, &requests
}

func TestNewKeyValueHasGatewayFormAndIsValid(t *testing.T) {
	a, err := NewKeyValue()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewKeyValue()
	if !ValidKeyValue(a) || !ValidKeyValue(b) || a == b {
		t.Fatalf("key values must be valid and distinct")
	}
	for _, invalid := range []string{"", "sk-", "nosk" + a[3:], a + "x", "sk-" + strings.Repeat("!", 43)} {
		if ValidKeyValue(invalid) {
			t.Errorf("ValidKeyValue(%q) = true", invalid)
		}
	}
}

func TestTokenIsSHA256Hex(t *testing.T) {
	// printf %s sk-test | sha256sum
	if got := Token("sk-test"); got != "f3abf2a6cc4f00987743db5f544ba345b4899ae31f326d8ee9c4816de153c9e0" {
		t.Fatalf("Token = %s", got)
	}
}

func TestGetKeyUsesTokenAndMasterKey(t *testing.T) {
	client, requests := testServer(t, func(recordedRequest) (int, string) {
		return 200, `{"key":"abc","info":{"key_alias":"cetirun-a","models":["cetirun-shared"],"max_budget":10,"budget_duration":"1mo","blocked":null}}`
	})
	key, err := client.GetKey(context.Background(), "abc")
	if err != nil || key == nil {
		t.Fatalf("GetKey = %v, %v", key, err)
	}
	if key.Alias != "cetirun-a" || len(key.Models) != 1 || key.Models[0] != "cetirun-shared" || key.MaxBudget == nil || *key.MaxBudget != 10 || key.Blocked {
		t.Fatalf("unexpected key %+v", key)
	}
	r := (*requests)[0]
	if r.method != http.MethodGet || r.path != "/key/info" || r.query != "key=abc" || r.auth != "Bearer sk-master" {
		t.Fatalf("unexpected request %+v", r)
	}
}

func TestGetKeyNotFoundIsNil(t *testing.T) {
	client, _ := testServer(t, func(recordedRequest) (int, string) {
		return 404, `{"error":{"message":"Key not found in database"}}`
	})
	key, err := client.GetKey(context.Background(), "abc")
	if key != nil || err != nil {
		t.Fatalf("GetKey = %v, %v; want nil, nil", key, err)
	}
}

func TestGenerateKeySendsRestrictedKey(t *testing.T) {
	client, requests := testServer(t, func(recordedRequest) (int, string) { return 200, `{}` })
	err := client.GenerateKey(context.Background(), GenerateRequest{
		Key: "sk-value", Alias: "cetirun-a", Models: []string{"cetirun-shared"}, MaxBudget: 10,
		BudgetDuration: "1mo", Metadata: map[string]string{"cetirun_agent": "a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := (*requests)[0].body
	if body["key"] != "sk-value" || body["key_alias"] != "cetirun-a" || body["max_budget"] != float64(10) || body["budget_duration"] != "1mo" {
		t.Fatalf("unexpected body %v", body)
	}
	if routes, _ := body["allowed_routes"].([]any); len(routes) != 1 || routes[0] != LLMAPIRoutes {
		t.Fatalf("allowed_routes = %v", body["allowed_routes"])
	}
}

func TestGenerateAndUpdateRefuseEmptyModelList(t *testing.T) {
	client, requests := testServer(t, func(recordedRequest) (int, string) { return 200, `{}` })
	if err := client.GenerateKey(context.Background(), GenerateRequest{Key: "sk-v", Alias: "a"}); err == nil {
		t.Fatal("GenerateKey accepted an empty model list")
	}
	if err := client.UpdateKeyModels(context.Background(), "abc", nil); err == nil {
		t.Fatal("UpdateKeyModels accepted an empty model list")
	}
	if len(*requests) != 0 {
		t.Fatalf("no request may be sent, got %d", len(*requests))
	}
}

func TestGenerateKeyAliasConflict(t *testing.T) {
	client, _ := testServer(t, func(recordedRequest) (int, string) {
		return 400, `{"error":{"message":"Key with alias 'cetirun-a' already exists. Unique key aliases across all keys are required."}}`
	})
	err := client.GenerateKey(context.Background(), GenerateRequest{Key: "sk-v", Alias: "cetirun-a", Models: []string{NoModels}})
	if !errors.Is(err, ErrAliasExists) {
		t.Fatalf("err = %v, want ErrAliasExists", err)
	}
}

func TestUpdateKeyModelsSendsOnlyModels(t *testing.T) {
	client, requests := testServer(t, func(recordedRequest) (int, string) { return 200, `{}` })
	if err := client.UpdateKeyModels(context.Background(), "abc", []string{"cetirun-shared"}); err != nil {
		t.Fatal(err)
	}
	body := (*requests)[0].body
	if len(body) != 2 || body["key"] != "abc" {
		t.Fatalf("update must carry only the token and models, got %v", body)
	}
}

func TestDeleteKeyByAliasTreatsNotFoundAsDeleted(t *testing.T) {
	calls := 0
	client, requests := testServer(t, func(recordedRequest) (int, string) {
		calls++
		if calls == 1 {
			return 200, `{"deleted_keys":["cetirun-a"]}`
		}
		return 404, `{"error":{"message":"{'error': 'No keys found'}"}}`
	})
	for i := 0; i < 2; i++ {
		if err := client.DeleteKeyByAlias(context.Background(), "cetirun-a"); err != nil {
			t.Fatalf("delete %d: %v", i, err)
		}
	}
	if aliases, _ := (*requests)[0].body["key_aliases"].([]any); len(aliases) != 1 || aliases[0] != "cetirun-a" {
		t.Fatalf("unexpected body %v", (*requests)[0].body)
	}
}

func TestErrorsNeverCarryKeyMaterial(t *testing.T) {
	client, _ := testServer(t, func(recordedRequest) (int, string) {
		return 401, `{"error":{"message":"Authentication Error, Invalid proxy server token passed. Received API Key = sk-...beWw, Key Hash (Token) =50d1"}}`
	})
	_, err := client.GetKey(context.Background(), "abc")
	if err == nil || strings.Contains(err.Error(), "beWw") || strings.Contains(err.Error(), "sk-master") {
		t.Fatalf("error leaks key material: %v", err)
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct{ url, key string }{
		{"", "sk-m"}, {"ftp://gateway:4000", "sk-m"}, {"http://user:pw@gateway:4000", "sk-m"},
		{"http://gateway:4000?x=1", "sk-m"}, {"http://gateway:4000", " "},
	} {
		if _, err := New(tc.url, tc.key, nil); err == nil {
			t.Errorf("New(%q) accepted invalid configuration", tc.url)
		}
	}
}
