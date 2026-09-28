// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

const (
	graphV1Base   = "https://graph.microsoft.com/v1.0"
	graphBetaBase = "https://graph.microsoft.com/beta"
	graphScope    = "https://graph.microsoft.com/.default"
	maxGraphPages = 100
)

// GraphClient provides raw HTTP access to the Microsoft Graph beta API.
// We use raw HTTP instead of msgraph-sdk-go because:
//   - The beta endpoint supports tenant-configured trusted issuers for FIC creation.
//   - Raw HTTP gives full control over beta-only fields like trustedIssuer@odata.bind.
type GraphClient struct {
	cred       azcore.TokenCredential
	httpClient *http.Client
}

// NewGraphClient creates a new Graph API client using the provided credential.
func NewGraphClient(cred azcore.TokenCredential) *GraphClient {
	return &GraphClient{
		cred: cred,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Application represents an Azure AD application registration.
type Application struct {
	ID             string `json:"id"`    // Object ID
	AppID          string `json:"appId"` // Client ID
	DisplayName    string `json:"displayName"`
	SignInAudience string `json:"signInAudience,omitempty"`
}

// FederatedIdentityCredential represents a FIC on an application.
type FederatedIdentityCredential struct {
	ID        string   `json:"id,omitempty"`
	Name      string   `json:"name"`
	Issuer    string   `json:"issuer"`
	Subject   string   `json:"subject"`
	Audiences []string `json:"audiences"`
}

// FICCreateRequest is the request body for creating a FIC with trustedIssuer@odata.bind.
type FICCreateRequest struct {
	Name                   string   `json:"name"`
	TrustedIssuerODataBind string   `json:"trustedIssuer@odata.bind"`
	Issuer                 string   `json:"issuer"`
	Subject                string   `json:"subject"`
	Audiences              []string `json:"audiences"`
}

// ServicePrincipal represents an Azure AD service principal.
type ServicePrincipal struct {
	ID                       string `json:"id"`
	AppID                    string `json:"appId"`
	DisplayName              string `json:"displayName,omitempty"`
	AgentIdentityBlueprintID string `json:"agentIdentityBlueprintId,omitempty"`
}

// AgentIdentityCreateRequest is the Graph contract for creating an Agent
// Identity under a deployment-owned Blueprint.
type AgentIdentityCreateRequest struct {
	ODataType                string   `json:"@odata.type"`
	DisplayName              string   `json:"displayName"`
	AgentIdentityBlueprintID string   `json:"agentIdentityBlueprintId"`
	Sponsors                 []string `json:"sponsors@odata.bind"`
}

// CreateAgentIdentity creates a service principal of the AgentIdentity type.
// The credential used by this client must represent the parent Blueprint.
func (c *GraphClient) CreateAgentIdentity(ctx context.Context, displayName, blueprintClientID, sponsorObjectID string) (*ServicePrincipal, error) {
	body := AgentIdentityCreateRequest{
		ODataType:                "#Microsoft.Graph.AgentIdentity",
		DisplayName:              displayName,
		AgentIdentityBlueprintID: blueprintClientID,
		Sponsors:                 []string{"https://graph.microsoft.com/v1.0/users/" + sponsorObjectID},
	}
	var result ServicePrincipal
	if err := c.doJSON(ctx, http.MethodPost, graphV1Base+"/servicePrincipals/microsoft.graph.agentIdentity", body, &result); err != nil {
		return nil, fmt.Errorf("create Agent Identity: %w", err)
	}
	if result.ID == "" || result.AppID == "" {
		return nil, fmt.Errorf("create Agent Identity returned an incomplete response")
	}
	return &result, nil
}

// GetAgentIdentitiesByDisplayName returns all exact-name service principals.
// The caller additionally verifies the Blueprint ID and fails closed on
// ambiguity. It follows Graph pagination only when each continuation remains
// on the expected Microsoft Graph v1.0 servicePrincipals collection.
func (c *GraphClient) GetAgentIdentitiesByDisplayName(ctx context.Context, displayName string) ([]ServicePrincipal, error) {
	escaped := strings.ReplaceAll(displayName, "'", "''")
	filter := fmt.Sprintf("displayName eq '%s'", escaped)
	nextURL := fmt.Sprintf("%s/servicePrincipals?$filter=%s&$select=id,appId,displayName,agentIdentityBlueprintId", graphV1Base, url.QueryEscape(filter))
	var result struct {
		Value    []ServicePrincipal `json:"value"`
		NextLink string             `json:"@odata.nextLink"`
	}
	identities := make([]ServicePrincipal, 0)
	for page := 0; nextURL != ""; page++ {
		if page == maxGraphPages {
			return nil, fmt.Errorf("get Agent Identities by displayName: pagination limit exceeded")
		}
		result = struct {
			Value    []ServicePrincipal `json:"value"`
			NextLink string             `json:"@odata.nextLink"`
		}{}
		if err := c.doJSON(ctx, http.MethodGet, nextURL, nil, &result); err != nil {
			return nil, fmt.Errorf("get Agent Identities by displayName: %w", err)
		}
		identities = append(identities, result.Value...)
		if result.NextLink == "" {
			break
		}
		validated, err := validateServicePrincipalNextLink(result.NextLink)
		if err != nil {
			return nil, fmt.Errorf("get Agent Identities by displayName: %w", err)
		}
		nextURL = validated
	}
	return identities, nil
}

func validateServicePrincipalNextLink(nextLink string) (string, error) {
	parsed, err := url.Parse(nextLink)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "graph.microsoft.com" || parsed.User != nil || parsed.Path != "/v1.0/servicePrincipals" || parsed.Fragment != "" {
		return "", fmt.Errorf("invalid Graph servicePrincipal continuation")
	}
	return parsed.String(), nil
}

// GetAgentIdentity retrieves an Agent Identity service principal by its
// immutable directory object ID. A missing principal is returned as nil, nil so
// callers can distinguish it from a transient directory failure.
func (c *GraphClient) GetAgentIdentity(ctx context.Context, objectID string) (*ServicePrincipal, error) {
	u := fmt.Sprintf("%s/servicePrincipals/%s?$select=id,appId,displayName,agentIdentityBlueprintId",
		graphV1Base, url.PathEscape(objectID))
	var result ServicePrincipal
	if err := c.doJSON(ctx, http.MethodGet, u, nil, &result); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get Agent Identity %s: %w", objectID, err)
	}
	return &result, nil
}

// GetAppByDisplayName searches for an application by exact displayName.
// Returns nil, nil if not found. If multiple apps exist with the same name, returns the first.
func (c *GraphClient) GetAppByDisplayName(ctx context.Context, displayName string) (*Application, error) {
	apps, err := c.GetAppsByDisplayName(ctx, displayName)
	if err != nil {
		return nil, err
	}
	if len(apps) == 0 {
		return nil, nil
	}
	return &apps[0], nil
}

// GetAppsByDisplayName returns applications matching the exact displayName.
// Returns only the first page of results (Graph API default page size, typically
// up to 100 items). This is sufficient for orphan cleanup since duplicate app
// registrations from the operator are unlikely to exceed a single page.
// Returns an empty slice if none found.
func (c *GraphClient) GetAppsByDisplayName(ctx context.Context, displayName string) ([]Application, error) {
	// Escape single quotes per OData rules (replace ' with '') to prevent
	// filter injection and ensure valid queries for names containing quotes.
	escaped := strings.ReplaceAll(displayName, "'", "''")
	filter := fmt.Sprintf("displayName eq '%s'", escaped)
	u := fmt.Sprintf("%s/applications?$filter=%s&$select=id,appId,displayName,signInAudience",
		graphBetaBase, url.QueryEscape(filter))

	var result struct {
		Value []Application `json:"value"`
	}
	if err := c.doJSON(ctx, http.MethodGet, u, nil, &result); err != nil {
		return nil, fmt.Errorf("get apps by displayName %q: %w", displayName, err)
	}
	if result.Value == nil {
		return []Application{}, nil
	}
	return result.Value, nil
}

// GetApp retrieves an application by its object ID.
func (c *GraphClient) GetApp(ctx context.Context, appObjectID string) (*Application, error) {
	u := fmt.Sprintf("%s/applications/%s?$select=id,appId,displayName,signInAudience",
		graphBetaBase, appObjectID)

	var app Application
	if err := c.doJSON(ctx, http.MethodGet, u, nil, &app); err != nil {
		return nil, fmt.Errorf("get app %s: %w", appObjectID, err)
	}
	return &app, nil
}

// CreateApp creates a new Azure AD application.
// serviceManagementReference is included when supplied by the deployment.
func (c *GraphClient) CreateApp(ctx context.Context, displayName, signInAudience, serviceManagementReference string) (*Application, error) {
	body := map[string]interface{}{
		"displayName":    displayName,
		"signInAudience": signInAudience,
	}
	if serviceManagementReference != "" {
		body["serviceManagementReference"] = serviceManagementReference
	}

	var app Application
	if err := c.doJSON(ctx, http.MethodPost, graphBetaBase+"/applications", body, &app); err != nil {
		return nil, fmt.Errorf("create app %q: %w", displayName, err)
	}
	return &app, nil
}

// ListFederatedCredentials lists all FICs on an application.
func (c *GraphClient) ListFederatedCredentials(ctx context.Context, appObjectID string) ([]FederatedIdentityCredential, error) {
	u := fmt.Sprintf("%s/applications/%s/federatedIdentityCredentials",
		graphBetaBase, appObjectID)

	var result struct {
		Value []FederatedIdentityCredential `json:"value"`
	}
	if err := c.doJSON(ctx, http.MethodGet, u, nil, &result); err != nil {
		return nil, fmt.Errorf("list FICs for app %s: %w", appObjectID, err)
	}
	return result.Value, nil
}

// CreateFederatedCredential creates a FIC on an application using the beta API
// with the configured trustedIssuer@odata.bind.
func (c *GraphClient) CreateFederatedCredential(ctx context.Context, appObjectID string, req FICCreateRequest) error {
	u := fmt.Sprintf("%s/applications/%s/federatedIdentityCredentials",
		graphBetaBase, appObjectID)

	if err := c.doJSON(ctx, http.MethodPost, u, req, nil); err != nil {
		return fmt.Errorf("create FIC on app %s: %w", appObjectID, err)
	}
	return nil
}

// CreateServicePrincipal creates a service principal for the given application client ID.
func (c *GraphClient) CreateServicePrincipal(ctx context.Context, appID string) (*ServicePrincipal, error) {
	body := map[string]interface{}{
		"appId": appID,
	}

	var sp ServicePrincipal
	if err := c.doJSON(ctx, http.MethodPost, graphBetaBase+"/servicePrincipals", body, &sp); err != nil {
		return nil, fmt.Errorf("create service principal for app %s: %w", appID, err)
	}
	return &sp, nil
}

// GetServicePrincipalByAppID looks up a service principal by its application (client) ID.
// Returns nil, nil if not found.
func (c *GraphClient) GetServicePrincipalByAppID(ctx context.Context, appID string) (*ServicePrincipal, error) {
	escaped := strings.ReplaceAll(appID, "'", "''")
	filter := fmt.Sprintf("appId eq '%s'", escaped)
	u := fmt.Sprintf("%s/servicePrincipals?$filter=%s&$select=id,appId&$top=1",
		graphBetaBase, url.QueryEscape(filter))

	var result struct {
		Value []ServicePrincipal `json:"value"`
	}
	if err := c.doJSON(ctx, http.MethodGet, u, nil, &result); err != nil {
		return nil, fmt.Errorf("get service principal by appId %s: %w", appID, err)
	}
	if len(result.Value) == 0 {
		return nil, nil
	}
	return &result.Value[0], nil
}

// DeleteServicePrincipal deletes a service principal by object ID. Returns nil if already deleted (404).
func (c *GraphClient) DeleteServicePrincipal(ctx context.Context, spObjectID string) error {
	u := fmt.Sprintf("%s/servicePrincipals/%s", graphBetaBase, spObjectID)
	if err := c.doJSON(ctx, http.MethodDelete, u, nil, nil); err != nil {
		if IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete service principal %s: %w", spObjectID, err)
	}
	return nil
}

// DeleteApp deletes an application by object ID. Returns nil if already deleted (404).
func (c *GraphClient) DeleteApp(ctx context.Context, appObjectID string) error {
	u := fmt.Sprintf("%s/applications/%s", graphBetaBase, appObjectID)
	if err := c.doJSON(ctx, http.MethodDelete, u, nil, nil); err != nil {
		if IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete app %s: %w", appObjectID, err)
	}
	return nil
}

// GraphError represents an error response from the Graph API.
type GraphError struct {
	StatusCode int
	Code       string
}

// RequestNotSentError identifies a failure before an HTTP request could reach
// Graph. Callers may safely retry a non-idempotent operation after this error.
type RequestNotSentError struct {
	err error
}

func (e *RequestNotSentError) Error() string {
	return e.err.Error()
}

func (e *RequestNotSentError) Unwrap() error {
	return e.err
}

// NewRequestNotSentError marks an error as occurring before a Graph request
// was sent. It is intended for internal callers and tests that model the same
// pre-request boundary as doJSON.
func NewRequestNotSentError(err error) error {
	return &RequestNotSentError{err: err}
}

// IsRequestNotSent reports whether an error occurred before an HTTP request
// was sent to Graph.
func IsRequestNotSent(err error) bool {
	var requestNotSent *RequestNotSentError
	return errors.As(err, &requestNotSent)
}

func (e *GraphError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("graph API error (HTTP %d)", e.StatusCode)
	}
	return fmt.Sprintf("graph API error (HTTP %d, code %s)", e.StatusCode, e.Code)
}

// IsNotFound returns true if the error is a 404 GraphError.
func IsNotFound(err error) bool {
	var ge *GraphError
	if errors.As(err, &ge) {
		return ge.StatusCode == http.StatusNotFound
	}
	return false
}

// IsConflict returns true if the error is a 409 GraphError (resource already exists).
func IsConflict(err error) bool {
	var ge *GraphError
	if errors.As(err, &ge) {
		return ge.StatusCode == http.StatusConflict
	}
	return false
}

// doJSON performs an authenticated HTTP request to the Graph API.
func (c *GraphClient) doJSON(ctx context.Context, method, url string, reqBody interface{}, respBody interface{}) error {
	var bodyReader io.Reader
	if reqBody != nil {
		data, err := json.Marshal(reqBody)
		if err != nil {
			return NewRequestNotSentError(fmt.Errorf("marshal request body: %w", err))
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return NewRequestNotSentError(fmt.Errorf("create request: %w", err))
	}

	// Acquire token.
	token, err := c.cred.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{graphScope},
	})
	if err != nil {
		return NewRequestNotSentError(fmt.Errorf("acquire graph token: %w", err))
	}

	req.Header.Set("Authorization", "Bearer "+token.Token)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var graphResponse struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(respData, &graphResponse)
		return &GraphError{
			StatusCode: resp.StatusCode,
			Code:       safeGraphErrorCode(graphResponse.Error.Code),
		}
	}

	if respBody != nil && len(respData) > 0 {
		if err := json.Unmarshal(respData, respBody); err != nil {
			return fmt.Errorf("unmarshal response: %w", err)
		}
	}

	return nil
}

func safeGraphErrorCode(value string) string {
	if value == "" || len(value) > 100 {
		return ""
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '.' || character == '-' {
			continue
		}
		return ""
	}
	return value
}
