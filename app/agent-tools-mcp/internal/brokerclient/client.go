// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package brokerclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"github.com/Azure/CetiRun/app/internal/agenttools"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const ContractVersion = agenttools.ContractVersion

type Client struct {
	baseURL *url.URL
	http    *http.Client
}
type Request = agenttools.Request
type Connection = agenttools.Connection
type Binding = agenttools.Binding
type IdentityBinding = agenttools.IdentityBinding
type ListResponse = agenttools.ListResponse
type CheckResponse = agenttools.CheckResponse
type IssueResponse = agenttools.IssueResponse

type apiError struct {
	Error struct {
		Code          string `json:"code"`
		Message       string `json:"message"`
		CorrelationID string `json:"correlationId"`
	} `json:"error"`
}

type Error struct {
	Code string
}

func (err *Error) Error() string { return err.Code }

func New(baseURL *url.URL) (*Client, error) {
	if baseURL == nil || baseURL.Scheme != "https" || baseURL.Host == "" || baseURL.Path != "" || baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return nil, errors.New("Broker URL must be an HTTPS origin")
	}
	return &Client{baseURL: baseURL, http: &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func NewMutualTLS(baseURL *url.URL, caFile, certFile, keyFile string) (*Client, error) {
	client, err := New(baseURL)
	if err != nil {
		return nil, err
	}
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, errors.New("Broker client identity is unavailable")
	}
	ca, err := os.ReadFile(caFile)
	if err != nil {
		return nil, errors.New("Broker TLS trust is unavailable")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("Broker TLS trust is invalid")
	}
	client.http.Transport = &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{certificate}}}
	return client, nil
}

func (c *Client) List(ctx context.Context, callerProof string) (ListResponse, error) {
	var result ListResponse
	err := c.call(ctx, "/internal/v1/service-authorizations/list", Request{CallerProof: callerProof, ContractVersion: ContractVersion}, &result)
	return result, err
}

func (c *Client) Check(ctx context.Context, request Request) (CheckResponse, error) {
	var result CheckResponse
	request.ContractVersion = ContractVersion
	err := c.call(ctx, "/internal/v1/service-authorizations/check", request, &result)
	return result, err
}

func (c *Client) Issue(ctx context.Context, request Request) (IssueResponse, error) {
	var result IssueResponse
	request.ContractVersion = ContractVersion
	err := c.call(ctx, "/internal/v1/service-credentials/issue", request, &result)
	return result, err
}

func (c *Client) call(ctx context.Context, path string, input any, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return errors.New("cannot encode Broker request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL.String()+path, bytes.NewReader(body))
	if err != nil {
		return errors.New("cannot create Broker request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return &Error{Code: "dependency_unavailable"}
	}
	defer response.Body.Close()
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if readErr != nil || len(payload) > 1<<20 {
		return &Error{Code: "dependency_unavailable"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var brokerError apiError
		if json.Unmarshal(payload, &brokerError) == nil && brokerError.Error.Code != "" {
			return &Error{Code: safeCode(brokerError.Error.Code)}
		}
		return &Error{Code: "dependency_unavailable"}
	}
	if err := json.Unmarshal(payload, output); err != nil {
		return &Error{Code: "dependency_unavailable"}
	}
	return nil
}

func safeCode(code string) string {
	allowed := map[string]bool{"unauthenticated": true, "binding_required": true, "permission_denied": true, "interaction_required": true, "invalid_request": true, "authorization_changed": true, "throttled": true, "dependency_unavailable": true}
	if allowed[code] {
		return code
	}
	return "dependency_unavailable"
}

func NewRequest(proof, service, connectionID string, bindingRevision, connectionRevision *uint64) Request {
	return Request{CallerProof: strings.TrimSpace(proof), Service: service, ConnectionID: connectionID, BindingRevision: bindingRevision, ConnectionRevision: connectionRevision, ContractVersion: ContractVersion}
}
