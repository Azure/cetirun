// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
)

// preparationGateway is a bounded in-memory Gateway management API. The
// preparation fixture uses the production HTTP client so Gateway projection is
// covered without making network calls or serving model traffic.
type preparationGateway struct {
	mu   sync.Mutex
	keys map[string]map[string]any
}

func (g *preparationGateway) RoundTrip(request *http.Request) (*http.Response, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.keys == nil {
		g.keys = map[string]map[string]any{}
	}
	status, body := http.StatusOK, any(map[string]any{})
	payload := map[string]any{}
	if request.Body != nil {
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			return nil, err
		}
	}
	switch request.URL.Path {
	case "/model/info":
		binding := fmt.Sprintf("%x", sha256.Sum256([]byte("example-model")))[:16]
		body = map[string]any{"data": []any{map[string]any{
			"model_name": "example-model", "litellm_params": map[string]any{"model": "openai/example-model"},
			"model_info": map[string]any{"id": "cetirun-shared-" + binding + "-0000000000000001", "db_model": false,
				"access_groups": []string{"cetirun-model-" + binding}, "input_cost_per_token": 0.001, "output_cost_per_token": 0.001},
		}}}
	case "/config/block_requests_for_models_without_pricing":
		body = map[string]any{"enabled": true}
	case "/public/litellm_model_cost_map":
		body = map[string]any{"example-model": map[string]any{"input_cost_per_token": 0.001, "output_cost_per_token": 0.001}}
	case "/key/info":
		key, found := g.keys[request.URL.Query().Get("key")]
		if !found {
			status = http.StatusNotFound
		} else {
			body = map[string]any{"info": key}
		}
	case "/key/generate":
		value, _ := payload["key"].(string)
		if !aigateway.ValidKeyValue(value) {
			return nil, fmt.Errorf("fixture received invalid synthetic key")
		}
		delete(payload, "key")
		payload["spend"], payload["budget_reset_at"] = 0.0, "2027-01-01T00:00:00Z"
		g.keys[aigateway.Token(value)] = payload
	case "/key/update":
		token, _ := payload["key"].(string)
		key, found := g.keys[token]
		if !found {
			status = http.StatusNotFound
			break
		}
		for name, value := range payload {
			if name != "key" {
				key[name] = value
			}
		}
	case "/key/delete":
		if tokens, ok := payload["keys"].([]any); ok {
			for _, token := range tokens {
				value, ok := token.(string)
				if !ok {
					return nil, fmt.Errorf("fixture received invalid key token")
				}
				delete(g.keys, value)
			}
			break
		}
		aliases, ok := payload["key_aliases"].([]any)
		if !ok {
			return nil, fmt.Errorf("fixture received invalid key deletion request")
		}
		for _, alias := range aliases {
			value, ok := alias.(string)
			if !ok {
				return nil, fmt.Errorf("fixture received invalid key alias")
			}
			for token, key := range g.keys {
				if key["key_alias"] == value {
					delete(g.keys, token)
				}
			}
		}
	case "/team/new", "/user/new", "/team/member_add":
	default:
		return nil, fmt.Errorf("unexpected preparation Gateway request: %s %s", request.Method, request.URL.Path)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(string(raw))), Request: request}, nil
}
