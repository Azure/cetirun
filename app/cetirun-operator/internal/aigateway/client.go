// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package aigateway is a minimal client for the virtual-key management API of
// the CetiRun AI Gateway (LiteLLM). The operator uses it to give each Agent its
// own key.
//
// Key values never appear in URLs, error messages, or logs. The Gateway stores
// a key as the SHA-256 hash of its value, so lookups, updates, and deletes use
// that hash (the "token") or the key alias instead of the value.
package aigateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// NoModels is LiteLLM's model entry that grants no model access. An empty
// model list grants every model, so a key without access groups must carry
// this entry instead.
const NoModels = "no-default-models"

// LLMAPIRoutes restricts a key to inference routes, so it cannot call the
// management API even for its own key.
const LLMAPIRoutes = "llm_api_routes"

// DefaultTimeout bounds each Gateway call.
const DefaultTimeout = 15 * time.Second

// ErrAliasExists reports that a key with the requested alias already exists.
var ErrAliasExists = errors.New("AI Gateway key alias already exists")

// APIError is a non-2xx Gateway response. Message is truncated and has any
// key-like substrings removed.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("AI Gateway returned HTTP %d: %s", e.StatusCode, e.Message)
}

// Key is the non-secret subset of a virtual key's settings.
type Key struct {
	Alias          string
	Models         []string
	MaxBudget      *float64
	BudgetDuration string
	Blocked        bool
}

// GenerateRequest creates a key with a caller-chosen value, which lets the
// caller persist the value before the create request and recover by lookup
// when the outcome of the request is unknown.
type GenerateRequest struct {
	Key            string
	Alias          string
	Models         []string
	MaxBudget      float64
	BudgetDuration string
	Metadata       map[string]string
}

// Client calls the Gateway management API with the master key.
type Client struct {
	base      *url.URL
	masterKey string
	http      *http.Client
}

// New returns a client for the Gateway at baseURL.
func New(baseURL, masterKey string, httpClient *http.Client) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" ||
		base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("AI Gateway URL is invalid")
	}
	if strings.TrimSpace(masterKey) == "" {
		return nil, fmt.Errorf("AI Gateway master key is empty")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultTimeout}
	}
	return &Client{base: base, masterKey: strings.TrimSpace(masterKey), http: httpClient}, nil
}

// NewKeyValue returns a new random key value in the Gateway's required form.
func NewKeyValue() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate virtual key: %w", err)
	}
	return "sk-" + base64.RawURLEncoding.EncodeToString(buf), nil
}

// ValidKeyValue reports whether value has the form NewKeyValue produces.
func ValidKeyValue(value string) bool {
	return keyValuePattern.MatchString(value)
}

var keyValuePattern = regexp.MustCompile(`^sk-[A-Za-z0-9_-]{43}$`)

// Token returns the Gateway's identifier for a key value: its SHA-256 hash.
func Token(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// GetKey returns the key identified by token, or nil when it does not exist.
func (c *Client) GetKey(ctx context.Context, token string) (*Key, error) {
	query := url.Values{"key": []string{token}}
	var response struct {
		Info struct {
			KeyAlias       string   `json:"key_alias"`
			Models         []string `json:"models"`
			MaxBudget      *float64 `json:"max_budget"`
			BudgetDuration string   `json:"budget_duration"`
			Blocked        *bool    `json:"blocked"`
		} `json:"info"`
	}
	status, err := c.do(ctx, http.MethodGet, "/key/info?"+query.Encode(), nil, &response)
	if status == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	info := response.Info
	return &Key{
		Alias: info.KeyAlias, Models: info.Models, MaxBudget: info.MaxBudget,
		BudgetDuration: info.BudgetDuration, Blocked: info.Blocked != nil && *info.Blocked,
	}, nil
}

// GenerateKey creates a key. It returns ErrAliasExists when the alias is taken.
func (c *Client) GenerateKey(ctx context.Context, req GenerateRequest) error {
	if !strings.HasPrefix(req.Key, "sk-") {
		return fmt.Errorf("virtual key value must start with sk-")
	}
	if len(req.Models) == 0 {
		return fmt.Errorf("virtual key model list must not be empty; an empty list grants every model")
	}
	body := map[string]any{
		"key":            req.Key,
		"key_alias":      req.Alias,
		"models":         req.Models,
		"allowed_routes": []string{LLMAPIRoutes},
		"metadata":       req.Metadata,
	}
	if req.MaxBudget > 0 {
		body["max_budget"] = req.MaxBudget
	}
	if req.BudgetDuration != "" {
		body["budget_duration"] = req.BudgetDuration
	}
	status, err := c.do(ctx, http.MethodPost, "/key/generate", body, nil)
	var apiErr *APIError
	if status == http.StatusBadRequest && errors.As(err, &apiErr) && strings.Contains(apiErr.Message, "already exists") {
		return ErrAliasExists
	}
	return err
}

// UpdateKeyModels replaces the model list of the key identified by token and
// leaves every other setting, including the budget, unchanged.
func (c *Client) UpdateKeyModels(ctx context.Context, token string, models []string) error {
	if len(models) == 0 {
		return fmt.Errorf("virtual key model list must not be empty; an empty list grants every model")
	}
	_, err := c.do(ctx, http.MethodPost, "/key/update", map[string]any{"key": token, "models": models}, nil)
	return err
}

// DeleteKeys deletes the keys identified by tokens. Keys that do not exist
// are treated as already deleted.
func (c *Client) DeleteKeys(ctx context.Context, tokens []string) error {
	if len(tokens) == 0 {
		return nil
	}
	status, err := c.do(ctx, http.MethodPost, "/key/delete", map[string]any{"keys": tokens}, nil)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}

// DeleteKeyByAlias deletes the key with the given alias. A key that does not
// exist is treated as already deleted.
func (c *Client) DeleteKeyByAlias(ctx context.Context, alias string) error {
	status, err := c.do(ctx, http.MethodPost, "/key/delete", map[string]any{"key_aliases": []string{alias}}, nil)
	if status == http.StatusNotFound {
		return nil
	}
	return err
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("encode AI Gateway request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, reader)
	if err != nil {
		return 0, fmt.Errorf("create AI Gateway request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.masterKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("call AI Gateway %s: %w", strings.SplitN(path, "?", 2)[0], redactError(err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("read AI Gateway response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, &APIError{StatusCode: resp.StatusCode, Message: errorMessage(raw)}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode AI Gateway response: %w", err)
		}
	}
	return resp.StatusCode, nil
}

var keyLike = regexp.MustCompile(`sk-[A-Za-z0-9_.*-]*`)

// errorMessage extracts LiteLLM's error message, bounded and with key-like
// text removed. LiteLLM echoes a masked key in some messages; removing the
// remainder keeps even partial key material out of conditions and logs.
func errorMessage(raw []byte) string {
	var parsed struct {
		Error struct {
			Message any `json:"message"`
		} `json:"error"`
		Detail any `json:"detail"`
	}
	message := strings.TrimSpace(string(raw))
	if json.Unmarshal(raw, &parsed) == nil {
		switch {
		case parsed.Error.Message != nil:
			message = fmt.Sprint(parsed.Error.Message)
		case parsed.Detail != nil:
			message = fmt.Sprint(parsed.Detail)
		}
	}
	message = keyLike.ReplaceAllString(message, "sk-[redacted]")
	if len(message) > 200 {
		message = message[:200] + "..."
	}
	return message
}

// redactError keeps transport errors from carrying request URLs, which hold
// only hashes but are not needed in conditions.
func redactError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}
