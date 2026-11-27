// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	clientAssertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
	defaultAuthority    = "https://login.microsoftonline.com/"
	// tokenRefreshMargin renews an access token this long before it expires.
	tokenRefreshMargin = 5 * time.Minute
	maxTokenResponse   = 64 << 10
)

// WorkloadIdentity gets Microsoft Entra access tokens for the Pod's managed
// identity. It exchanges the service account token that the AKS workload
// identity webhook projects into the Pod for an access token of a resource,
// without an Azure SDK.
type WorkloadIdentity struct {
	ClientID  string
	TenantID  string
	TokenFile string
	// Authority is the Entra endpoint, such as https://login.microsoftonline.com/.
	Authority string
	HTTP      *http.Client

	mu     sync.Mutex
	tokens map[string]cachedToken
	now    func() time.Time
}

type cachedToken struct {
	value     string
	refreshAt time.Time
}

// WorkloadIdentityFromEnvironment reads the variables that the workload
// identity webhook sets in a Pod whose service account names a client ID.
func WorkloadIdentityFromEnvironment(getenv func(string) string) (*WorkloadIdentity, error) {
	identity := &WorkloadIdentity{
		ClientID:  strings.TrimSpace(getenv("AZURE_CLIENT_ID")),
		TenantID:  strings.TrimSpace(getenv("AZURE_TENANT_ID")),
		TokenFile: strings.TrimSpace(getenv("AZURE_FEDERATED_TOKEN_FILE")),
		Authority: strings.TrimSpace(getenv("AZURE_AUTHORITY_HOST")),
	}
	var missing []string
	for _, item := range []struct{ name, value string }{
		{"AZURE_CLIENT_ID", identity.ClientID},
		{"AZURE_TENANT_ID", identity.TenantID},
		{"AZURE_FEDERATED_TOKEN_FILE", identity.TokenFile},
	} {
		if item.value == "" {
			missing = append(missing, item.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("the workload identity webhook did not set %s: the Pod needs the label azure.workload.identity/use and its service account the annotation azure.workload.identity/client-id", strings.Join(missing, ", "))
	}
	if identity.Authority == "" {
		identity.Authority = defaultAuthority
	}
	if parsed, err := url.Parse(identity.Authority); err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, errors.New("AZURE_AUTHORITY_HOST must be an https URL")
	}
	return identity, nil
}

// Token returns an access token for the resource, such as the query URI of a
// Kusto cluster. It keeps each token until shortly before it expires.
func (w *WorkloadIdentity) Token(ctx context.Context, resource string) (string, error) {
	scope := strings.TrimSuffix(resource, "/") + "/.default"
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now
	if w.now != nil {
		now = w.now
	}
	if cached, ok := w.tokens[scope]; ok && now().Before(cached.refreshAt) {
		return cached.value, nil
	}
	assertion, err := os.ReadFile(w.TokenFile)
	if err != nil || len(strings.TrimSpace(string(assertion))) == 0 {
		return "", errors.New("the projected service account token of the workload identity cannot be read")
	}
	form := url.Values{
		"client_id":             {w.ClientID},
		"scope":                 {scope},
		"grant_type":            {"client_credentials"},
		"client_assertion_type": {clientAssertionType},
		"client_assertion":      {strings.TrimSpace(string(assertion))},
	}
	endpoint := strings.TrimSuffix(w.Authority, "/") + "/" + url.PathEscape(w.TenantID) + "/oauth2/v2.0/token"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.New("cannot create the token request of the workload identity")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := w.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("Microsoft Entra ID is unavailable: %w", err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, maxTokenResponse))
	var result struct {
		AccessToken      string `json:"access_token"`
		ExpiresIn        int64  `json:"expires_in"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if json.Unmarshal(data, &result) != nil || response.StatusCode != http.StatusOK || result.AccessToken == "" {
		return "", fmt.Errorf("the token exchange of the workload identity failed with HTTP %d%s", response.StatusCode, entraError(result.Error, result.ErrorDescription))
	}
	lifetime := time.Duration(result.ExpiresIn) * time.Second
	refreshAt := now().Add(lifetime - tokenRefreshMargin)
	if lifetime <= 2*tokenRefreshMargin {
		refreshAt = now().Add(lifetime / 2)
	}
	if w.tokens == nil {
		w.tokens = map[string]cachedToken{}
	}
	w.tokens[scope] = cachedToken{value: result.AccessToken, refreshAt: refreshAt}
	return result.AccessToken, nil
}

// entraError names the error that Microsoft Entra ID returned, such as
// AADSTS700213 for a missing federated credential, without its trace and
// correlation IDs.
func entraError(code, description string) string {
	if code == "" {
		return ""
	}
	if index := strings.Index(description, "Trace ID:"); index >= 0 {
		description = description[:index]
	}
	description = strings.TrimSpace(description)
	if len(description) > 400 {
		description = description[:400]
	}
	if description == "" {
		return ": " + code
	}
	return ": " + code + ": " + description
}
