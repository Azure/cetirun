// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	armBaseURL       = "https://management.azure.com"
	armHost          = "management.azure.com"
	maxAzureResponse = 2 << 20
	maxResultItems   = 100
	maxARMPages      = 20
)

var filterValuePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._()/ :+-]{0,255}$`)

func validateSubscriptionID(subscriptionID string) error {
	if !uuidPattern.MatchString(subscriptionID) {
		return errors.New("subscription ID is invalid")
	}
	return nil
}

func validateResourceFilters(filters resourceFilters) error {
	if len(filters.Subscriptions) == 0 || len(filters.Subscriptions) > 20 {
		return errors.New("between one and twenty subscriptions are required")
	}
	seen := map[string]bool{}
	for _, subscription := range filters.Subscriptions {
		if err := validateSubscriptionID(subscription); err != nil || seen[subscription] {
			return errors.New("subscription IDs are invalid or duplicated")
		}
		seen[subscription] = true
	}
	for _, value := range []string{filters.ResourceGroup, filters.Type, filters.Location, filters.State} {
		if value != "" && !filterValuePattern.MatchString(value) {
			return errors.New("resource filters are invalid")
		}
	}
	return nil
}

type azureClient struct {
	client *http.Client
}

// azureResult is deliberately bounded. A caller must inspect Truncated before
// presenting a list as exhaustive.
type azureResult struct {
	Items     []map[string]any
	Truncated bool
}

func newAzureClient(timeout time.Duration) *azureClient {
	return &azureClient{client: &http.Client{
		Timeout: timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			// Every ARM endpoint is validated before it is requested. Following a
			// downstream redirect would bypass that host/path boundary.
			return http.ErrUseLastResponse
		},
	}}
}

func (c *azureClient) subscriptions(ctx context.Context, authorization string) (azureResult, error) {
	type subscription struct {
		SubscriptionID string `json:"subscriptionId"`
		DisplayName    string `json:"displayName"`
		State          string `json:"state"`
	}
	items, truncated, err := c.listARM(ctx, authorization, armBaseURL+"/subscriptions?api-version=2022-12-01", "/subscriptions", func(response json.RawMessage) ([]map[string]any, error) {
		var page []subscription
		if err := json.Unmarshal(response, &page); err != nil {
			return nil, err
		}
		result := make([]map[string]any, 0, len(page))
		for _, item := range page {
			result = append(result, map[string]any{
				"subscriptionId": item.SubscriptionID,
				"displayName":    bounded(item.DisplayName, 256),
				"state":          bounded(item.State, 64),
			})
		}
		return result, nil
	})
	return azureResult{Items: items, Truncated: truncated}, err
}

func (c *azureClient) resourceGroups(ctx context.Context, authorization, subscriptionID string) (azureResult, error) {
	if err := validateSubscriptionID(subscriptionID); err != nil {
		return azureResult{}, err
	}
	endpoint := fmt.Sprintf("%s/subscriptions/%s/resourcegroups?api-version=2021-04-01", armBaseURL, subscriptionID)
	expectedPath := "/subscriptions/" + subscriptionID + "/resourcegroups"
	type resourceGroup struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Location string `json:"location"`
	}
	items, truncated, err := c.listARM(ctx, authorization, endpoint, expectedPath, func(response json.RawMessage) ([]map[string]any, error) {
		var page []resourceGroup
		if err := json.Unmarshal(response, &page); err != nil {
			return nil, err
		}
		result := make([]map[string]any, 0, len(page))
		for _, item := range page {
			result = append(result, map[string]any{"id": bounded(item.ID, 1024), "name": bounded(item.Name, 256), "location": bounded(item.Location, 128)})
		}
		return result, nil
	})
	return azureResult{Items: items, Truncated: truncated}, err
}

type resourceFilters struct {
	Subscriptions []string `json:"subscriptions"`
	ResourceGroup string   `json:"resourceGroup,omitempty"`
	Type          string   `json:"type,omitempty"`
	Location      string   `json:"location,omitempty"`
	State         string   `json:"state,omitempty"`
}

func (c *azureClient) resources(ctx context.Context, authorization string, filters resourceFilters, canonicalID string) (azureResult, error) {
	if err := validateResourceFilters(filters); err != nil {
		return azureResult{}, err
	}
	query := "Resources"
	if canonicalID != "" {
		resourceID, err := normalizeResourceID(canonicalID)
		if err != nil {
			return azureResult{}, err
		}
		query += " | where id =~ '" + kqlLiteral(resourceID) + "'"
	} else {
		clauses := []string{}
		for field, value := range map[string]string{"resourceGroup": filters.ResourceGroup, "type": filters.Type, "location": filters.Location} {
			if value == "" {
				continue
			}
			clauses = append(clauses, fmt.Sprintf("%s =~ '%s'", field, kqlLiteral(value)))
		}
		if filters.State != "" {
			clauses = append(clauses, "tostring(properties.extended.instanceView.powerState.code) =~ '"+kqlLiteral(filters.State)+"'")
		}
		sort.Strings(clauses)
		for _, clause := range clauses {
			query += " | where " + clause
		}
	}
	query += " | project id, name, type, location, resourceGroup, subscriptionId, kind, sku | limit 101"
	body := map[string]any{
		"subscriptions": filters.Subscriptions,
		"query":         query,
		"options":       map[string]any{"resultFormat": "objectArray", "$top": maxResultItems + 1},
	}
	var response struct {
		Data []map[string]any `json:"data"`
	}
	if err := c.call(ctx, http.MethodPost, armBaseURL+"/providers/Microsoft.ResourceGraph/resources?api-version=2022-10-01", authorization, body, &response); err != nil {
		return azureResult{}, err
	}
	items := make([]map[string]any, 0, min(len(response.Data), maxResultItems))
	for index, raw := range response.Data {
		if index >= maxResultItems {
			break
		}
		item := map[string]any{}
		for _, field := range []string{"id", "name", "type", "location", "resourceGroup", "subscriptionId", "kind"} {
			if value, ok := raw[field].(string); ok {
				item[field] = bounded(value, 1024)
			}
		}
		if sku, ok := raw["sku"].(map[string]any); ok {
			filtered := map[string]any{}
			for _, field := range []string{"name", "tier", "size", "family"} {
				if value, ok := sku[field].(string); ok {
					filtered[field] = bounded(value, 128)
				}
			}
			item["sku"] = filtered
		}
		items = append(items, item)
	}
	// A show request is an exact-ID lookup. It is intentionally never reported
	// as a partial list even if a malformed downstream response has duplicates.
	return azureResult{Items: items, Truncated: canonicalID == "" && len(response.Data) > maxResultItems}, nil
}

func (c *azureClient) listARM(ctx context.Context, authorization, endpoint, expectedPath string, decode func(json.RawMessage) ([]map[string]any, error)) ([]map[string]any, bool, error) {
	items := make([]map[string]any, 0, maxResultItems)
	for page := 0; page < maxARMPages; page++ {
		var response struct {
			Value    json.RawMessage `json:"value"`
			NextLink string          `json:"nextLink"`
		}
		if err := c.call(ctx, http.MethodGet, endpoint, authorization, nil, &response); err != nil {
			return nil, false, err
		}
		pageItems, err := decode(response.Value)
		if err != nil {
			return nil, false, errors.New("Azure Resource Manager returned invalid JSON")
		}
		for _, item := range pageItems {
			if len(items) == maxResultItems {
				return items, true, nil
			}
			items = append(items, item)
		}
		if response.NextLink == "" {
			return items, false, nil
		}
		if len(items) == maxResultItems || page+1 == maxARMPages {
			return items, true, nil
		}
		next, err := validARMNextLink(response.NextLink, expectedPath)
		if err != nil {
			return nil, false, err
		}
		endpoint = next
	}
	return items, true, nil
}

func validARMNextLink(raw, expectedPath string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != armHost || u.User != nil || u.Fragment != "" || u.RawFragment != "" || u.Opaque != "" || u.RawPath != "" || !strings.EqualFold(u.Path, expectedPath) {
		return "", errors.New("Azure Resource Manager returned an invalid continuation link")
	}
	return u.String(), nil
}

func (c *azureClient) call(ctx context.Context, method, endpoint, authorization string, body any, target any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return errors.New("Azure Resource Manager is unavailable")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAzureResponse+1))
	if err != nil || len(data) > maxAzureResponse {
		return errors.New("Azure response is invalid or too large")
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return errDownstreamForbidden
	case http.StatusTooManyRequests:
		return errThrottled
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Azure Resource Manager returned HTTP %d", resp.StatusCode)
	}
	if json.Unmarshal(data, target) != nil {
		return errors.New("Azure Resource Manager returned invalid JSON")
	}
	return nil
}

func kqlLiteral(value string) string { return strings.ReplaceAll(value, "'", "''") }

func bounded(value string, maximum int) string {
	if len(value) > maximum {
		return value[:maximum]
	}
	return value
}

var errThrottled = errors.New("downstream request was throttled")
