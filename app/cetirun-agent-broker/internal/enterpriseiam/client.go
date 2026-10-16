// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package enterpriseiam

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	defaultTimeout        = 10 * time.Second
	maximumTimeout        = 30 * time.Second
	defaultMaxResponse    = int64(256 << 10)
	maximumResponse       = int64(1 << 20)
	defaultEvidenceMaxAge = 5 * time.Minute
	maximumEvidenceMaxAge = time.Hour
	clockSkew             = time.Minute
)

// Config configures an IAM service at one HTTPS origin. TLSConfig must
// contain a client certificate; bearer and delegated-user authentication are
// intentionally unsupported.
type Config struct {
	BaseURL        string
	TLSConfig      *tls.Config
	Timeout        time.Duration
	MaxResponse    int64
	EvidenceMaxAge time.Duration
}

// Client communicates with an enterprise IAM service using mutual TLS.
type Client struct {
	baseURL        string
	httpClient     *http.Client
	maxResponse    int64
	evidenceMaxAge time.Duration
	now            func() time.Time
}

// NewClient validates the service origin and creates a client which can only
// call the fixed protocol paths documented in README.md.
func NewClient(config Config) (*Client, error) {
	origin, err := url.Parse(config.BaseURL)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil ||
		origin.RawQuery != "" || origin.Fragment != "" || origin.Opaque != "" ||
		(origin.Path != "" && origin.Path != "/") {
		return nil, requestError(errorInvalidConfig)
	}
	if config.TLSConfig == nil || len(config.TLSConfig.Certificates) == 0 || config.TLSConfig.InsecureSkipVerify {
		return nil, requestError(errorInvalidConfig)
	}
	timeout := config.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	if timeout < 0 || timeout > maximumTimeout {
		return nil, requestError(errorInvalidConfig)
	}
	maxResponse := config.MaxResponse
	if maxResponse == 0 {
		maxResponse = defaultMaxResponse
	}
	if maxResponse <= 0 || maxResponse > maximumResponse {
		return nil, requestError(errorInvalidConfig)
	}
	evidenceMaxAge := config.EvidenceMaxAge
	if evidenceMaxAge == 0 {
		evidenceMaxAge = defaultEvidenceMaxAge
	}
	if evidenceMaxAge <= 0 || evidenceMaxAge > maximumEvidenceMaxAge {
		return nil, requestError(errorInvalidConfig)
	}
	tlsConfig := config.TLSConfig.Clone()
	if tlsConfig.MinVersion < tls.VersionTLS12 {
		tlsConfig.MinVersion = tls.VersionTLS12
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	return &Client{
		baseURL:        origin.Scheme + "://" + origin.Host,
		httpClient:     &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		maxResponse:    maxResponse,
		evidenceMaxAge: evidenceMaxAge,
		now:            time.Now,
	}, nil
}

// Submit creates or reads an idempotent permission request. Repeating an
// identical request ID and binding is safe; reusing the ID for another
// binding is a protocol error that the service must reject.
func (c *Client) Submit(ctx context.Context, request PermissionRequest) (Decision, error) {
	canonical, err := canonicalRequest(request, c.now())
	if err != nil {
		return Decision{}, err
	}
	return c.call(ctx, http.MethodPost, "/v1/permission-requests", canonical, canonical, "")
}

// Get returns the current decision and validates it against the caller's
// immutable request binding.
func (c *Client) Get(ctx context.Context, request PermissionRequest) (Decision, error) {
	canonical, err := canonicalRequest(request, c.now())
	if err != nil {
		return Decision{}, err
	}
	return c.call(ctx, http.MethodGet, "/v1/permission-requests/"+canonical.ID, canonical, nil, "")
}

// Cancel withdraws a pending request. The returned state must be revoked,
// which prevents it from being treated as executable authorization.
func (c *Client) Cancel(ctx context.Context, request PermissionRequest) (Decision, error) {
	canonical, err := canonicalRequest(request, c.now())
	if err != nil {
		return Decision{}, err
	}
	return c.call(ctx, http.MethodPost, "/v1/permission-requests/"+canonical.ID+":cancel", canonical, canonical, StateRevoked)
}

// Revoke withdraws an applied or approved request. The resource owner still
// remains responsible for any separately managed direct provider grant.
func (c *Client) Revoke(ctx context.Context, request PermissionRequest) (Decision, error) {
	canonical, err := canonicalRequest(request, c.now())
	if err != nil {
		return Decision{}, err
	}
	return c.call(ctx, http.MethodPost, "/v1/permission-requests/"+canonical.ID+":revoke", canonical, canonical, StateRevoked)
}

func (c *Client) call(ctx context.Context, method, path string, request PermissionRequest, payload any, requiredState State) (Decision, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return Decision{}, requestError(errorInvalidRequest)
		}
		body = bytes.NewReader(encoded)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return Decision{}, requestError(errorInvalidRequest)
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Idempotency-Key", request.ID)
	if payload != nil {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return Decision{}, requestError(errorUnavailable)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return Decision{}, &Error{Code: errorRejected, StatusCode: response.StatusCode}
	}
	limited := io.LimitReader(response.Body, c.maxResponse+1)
	encoded, err := io.ReadAll(limited)
	if err != nil {
		return Decision{}, requestError(errorMalformed)
	}
	if int64(len(encoded)) > c.maxResponse {
		return Decision{}, requestError(errorTooLarge)
	}
	var decision Decision
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	if err := decoder.Decode(&decision); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return Decision{}, requestError(errorMalformed)
	}
	if err := c.validateDecision(request, &decision, requiredState); err != nil {
		return Decision{}, err
	}
	return decision, nil
}

func (c *Client) validateDecision(request PermissionRequest, decision *Decision, requiredState State) error {
	if !validIdentifier(decision.ExternalRequestID) || !validState(decision.State) {
		return requestError(errorMalformed)
	}
	evidence, err := canonicalEvidence(decision.Evidence)
	if err != nil {
		return err
	}
	if !sameBinding(request, evidence) {
		return requestError(errorBinding)
	}
	now := c.now().UTC()
	if !evidence.ExpiresAt.After(now) || evidence.EvaluatedAt.After(now.Add(clockSkew)) || now.Sub(evidence.EvaluatedAt) > c.evidenceMaxAge {
		return requestError(errorStale)
	}
	if requiredState != "" && decision.State != requiredState {
		return requestError(errorRejected)
	}
	decision.Evidence = evidence
	return nil
}

func validState(state State) bool {
	switch state {
	case StatePending, StateApproved, StateApplied, StateDenied, StateRevoked:
		return true
	default:
		return false
	}
}

// ErrorCode reports the stable category for callers that need safe metrics or
// API error mapping. It intentionally does not expose remote response text.
func ErrorCode(err error) string {
	code, ok := errorCodeOf(err)
	if !ok {
		return ""
	}
	return string(code)
}

// IsExpired reports whether an error was caused by an expired local request.
func IsExpired(err error) bool {
	return ErrorCode(err) == string(errorExpired)
}
