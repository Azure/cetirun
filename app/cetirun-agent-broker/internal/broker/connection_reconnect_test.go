// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

type reconnectAttempt struct {
	id, state, binding string
}

func reconnectFixture(t *testing.T, mode string) *authorizationFixture {
	t.Helper()
	var f *authorizationFixture
	if mode == modeInstallation {
		f, _, _ = githubAppSecurityFixture(t)
	} else {
		f = personalOAuthFixture(t, "google")
		f.server.cfg.GoogleClientID, f.server.cfg.GoogleClientSecret = "client", "sk-dummy"
	}
	f.server.cfg.PortalNamespace, f.server.cfg.PortalServiceAccount = "portal", "portal"
	f.server.cfg.TransactionTTL = 10 * time.Minute
	f.server.providers = &providerClient{cfg: f.server.cfg, client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case "https://oauth2.googleapis.com/token", "https://github.com/login/oauth/access_token":
			code := r.FormValue("code")
			if r.FormValue("grant_type") == "refresh_token" {
				code = "refresh"
			}
			return securityJSONResponse(t, http.StatusOK, map[string]any{
				"access_token": "synthetic-" + code, "refresh_token": "synthetic-refresh-" + code,
				"expires_in": 3600, "scope": "scope-" + code,
			}), nil
		case "https://openidconnect.googleapis.com/v1/userinfo":
			return securityJSONResponse(t, http.StatusOK, map[string]any{"sub": "original-account", "email": "original@example.com"}), nil
		case "https://api.github.com/user":
			return securityJSONResponse(t, http.StatusOK, map[string]any{"id": 42, "login": "octocat"}), nil
		default:
			t.Fatalf("unexpected provider request: %s", r.URL)
			return nil, nil
		}
	})}}
	return f
}

func beginReconnect(t *testing.T, f *authorizationFixture) reconnectAttempt {
	t.Helper()
	proof := strings.Repeat("b", 43)
	r := httptest.NewRequest(http.MethodPost, "/v1/connections/"+f.connection.Connection.ID+"/reconnect", nil)
	setPortalRequestHeaders(r, f.grant.Owner, proof)
	w := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(w, r)
	var response struct {
		ID  string `json:"authorizationId"`
		URL string `json:"authorizationUrl"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.ID == "" {
		t.Fatalf("start reconnect: %d %s", w.Code, w.Body)
	}
	location, err := url.Parse(response.URL)
	if err != nil || location.Query().Get("state") == "" {
		t.Fatal("reconnect returned no authorization state")
	}
	return reconnectAttempt{id: response.ID, state: location.Query().Get("state"), binding: proof}
}

func finishReconnect(t *testing.T, f *authorizationFixture, attempt reconnectAttempt, code string) string {
	t.Helper()
	query := url.Values{"state": {attempt.state}, "code": {code}}
	if code == "" {
		query.Set("error", "access_denied")
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/connections/callback?"+query.Encode(), nil)
	setPortalCallbackHeaders(r, attempt.binding)
	f.server.Handler().ServeHTTP(httptest.NewRecorder(), r)
	record, _, err := f.server.store.getOAuthRecord(context.Background(), "authorization", attempt.id)
	if err != nil || record.Transaction == nil {
		t.Fatal("callback result unavailable:", err)
	}
	return record.Transaction.Status
}

func reconnectConnection(t *testing.T, f *authorizationFixture) *connectionRecord {
	t.Helper()
	record, _, err := f.server.store.getConnection(context.Background(), f.connection.Connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestReconnectLatestAttemptWins(t *testing.T) {
	for _, mode := range []string{modeDelegated, modeInstallation} {
		t.Run(mode, func(t *testing.T) {
			f := reconnectFixture(t, mode)
			before := reconnectConnection(t, f)
			binding := f.secrets[toolRecordName("binding", f.grant.ID)]
			a := beginReconnect(t, f)
			original := f.server
			replica, store := *original, *original.store
			replica.store = &store
			replica.mux, replica.tlsMux = http.NewServeMux(), http.NewServeMux()
			replica.routes()
			f.server = &replica
			b := beginReconnect(t, f)
			if got := finishReconnect(t, f, b, "code-b"); got != "complete" {
				t.Fatalf("latest callback status = %s", got)
			}
			latest := reconnectConnection(t, f)
			if latest.Credential.AccessToken != "synthetic-code-b" || !reflect.DeepEqual(latest.Connection.GitHub, before.Connection.GitHub) || !reflect.DeepEqual(latest.References, before.References) {
				t.Fatal("latest reconnect failed to replace credentials while preserving installation and bindings")
			}
			f.server = original
			if got := finishReconnect(t, f, a, "code-a"); got != "failed" {
				t.Errorf("superseded callback status = %s, want failed", got)
			}
			if !reflect.DeepEqual(latest, reconnectConnection(t, f)) {
				t.Error("superseded callback replaced the latest connection")
			}
			if !reflect.DeepEqual(binding, f.secrets[toolRecordName("binding", f.grant.ID)]) {
				t.Error("reconnect modified an Agent binding")
			}
		})
	}
}

func TestReconnectLatestPendingRejectsOlder(t *testing.T) {
	for _, mode := range []string{modeDelegated, modeInstallation} {
		t.Run(mode, func(t *testing.T) {
			f := reconnectFixture(t, mode)
			a, b := beginReconnect(t, f), beginReconnect(t, f)
			before := reconnectConnection(t, f)
			if finishReconnect(t, f, a, "code-a") != "failed" || !reflect.DeepEqual(before, reconnectConnection(t, f)) {
				t.Fatal("older callback changed a connection with a newer pending attempt")
			}
			if finishReconnect(t, f, b, "code-b") != "complete" {
				t.Fatal("latest pending attempt could not complete")
			}
		})
	}
}

func TestReconnectFailureDoesNotReviveOlderAttempt(t *testing.T) {
	for _, mode := range []string{modeDelegated, modeInstallation} {
		for _, failure := range []string{"cancel", "provider failure"} {
			t.Run(mode+"/"+failure, func(t *testing.T) {
				f := reconnectFixture(t, mode)
				before := reconnectConnection(t, f)
				a, b := beginReconnect(t, f), beginReconnect(t, f)
				code := ""
				if failure == "provider failure" {
					code = "code-b"
					base := f.server.providers.client.Transport
					f.server.providers.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
						if r.FormValue("code") == "code-b" {
							return securityJSONResponse(t, http.StatusBadRequest, map[string]string{"error": "invalid_grant"}), nil
						}
						return base.RoundTrip(r)
					})
				}
				if finishReconnect(t, f, b, code) != "failed" || !reflect.DeepEqual(before, reconnectConnection(t, f)) {
					t.Fatal("failed reconnect changed the usable connection")
				}
				if finishReconnect(t, f, a, "code-a") != "failed" || !reflect.DeepEqual(before, reconnectConnection(t, f)) {
					t.Fatal("failed latest reconnect revived an older attempt")
				}
			})
		}
	}
}

func TestReconnectOlderCancellationPreservesLatest(t *testing.T) {
	for _, mode := range []string{modeDelegated, modeInstallation} {
		t.Run(mode, func(t *testing.T) {
			f := reconnectFixture(t, mode)
			a, b := beginReconnect(t, f), beginReconnect(t, f)
			before := reconnectConnection(t, f)
			if finishReconnect(t, f, a, "") != "failed" || !reflect.DeepEqual(before, reconnectConnection(t, f)) {
				t.Fatal("older cancellation cleared the latest attempt")
			}
			if finishReconnect(t, f, b, "code-b") != "complete" {
				t.Fatal("latest attempt failed after an older cancellation")
			}
		})
	}
}

func TestReconnectRechecksAfterProviderExchange(t *testing.T) {
	for _, mode := range []string{modeDelegated, modeInstallation} {
		t.Run(mode, func(t *testing.T) {
			f := reconnectFixture(t, mode)
			a := beginReconnect(t, f)
			base := f.server.providers.client.Transport
			var latest *connectionRecord
			f.server.providers.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.FormValue("code") == "code-a" {
					b := beginReconnect(t, f)
					if finishReconnect(t, f, b, "code-b") != "complete" {
						t.Fatal("newer callback failed during the older exchange")
					}
					latest = reconnectConnection(t, f)
				}
				return base.RoundTrip(r)
			})
			if finishReconnect(t, f, a, "code-a") != "failed" || latest == nil || !reflect.DeepEqual(latest, reconnectConnection(t, f)) {
				t.Fatal("callback did not recheck authorization after the provider exchange")
			}
		})
	}
}

func TestReconnectCommitConflictRechecksAuthorization(t *testing.T) {
	for _, mode := range []string{modeDelegated, modeInstallation} {
		for _, newer := range []bool{false, true} {
			name := "unrelated update"
			if newer {
				name = "new reconnect"
			}
			t.Run(mode+"/"+name, func(t *testing.T) {
				f := reconnectFixture(t, mode)
				a := beginReconnect(t, f)
				base := f.server.store.client.Transport
				injected := false
				var b reconnectAttempt
				f.server.store.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if !injected && r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/"+toolRecordName("connection", f.connection.Connection.ID)) {
						injected = true
						if newer {
							b = beginReconnect(t, f)
						} else {
							current, version, err := f.server.store.getConnection(context.Background(), f.connection.Connection.ID)
							if err != nil {
								t.Fatal(err)
							}
							current.Connection.Revision++
							if err := f.server.store.putConnection(context.Background(), current, version); err != nil {
								t.Fatal(err)
							}
						}
					}
					return base.RoundTrip(r)
				})
				status := finishReconnect(t, f, a, "code-a")
				if !injected {
					t.Fatal("commit conflict was not exercised")
				}
				if newer {
					if status != "failed" || reconnectConnection(t, f).CurrentAuthorizationID != b.id || finishReconnect(t, f, b, "code-b") != "complete" {
						t.Fatal("conflict retry did not preserve the newer authorization")
					}
				} else if status != "complete" || reconnectConnection(t, f).Credential.AccessToken != "synthetic-code-a" {
					t.Fatal("unrelated storage update invalidated the current authorization")
				}
			})
		}
	}
}

func expireReconnectCredential(t *testing.T, f *authorizationFixture) {
	t.Helper()
	record, version, err := f.server.store.getConnection(context.Background(), f.connection.Connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	record.Credential.ExpiresAt = time.Now().Add(-time.Minute)
	if err := f.server.store.putConnection(context.Background(), record, version); err != nil {
		t.Fatal(err)
	}
}

func refreshReconnectCredential(t *testing.T, f *authorizationFixture) (string, error) {
	t.Helper()
	record := reconnectConnection(t, f)
	if connectionMode(record.Connection) == modeInstallation {
		credential, err := f.server.githubUserCredential(context.Background(), record)
		return credential.AccessToken, err
	}
	token, _, err := f.server.issueServiceCredential(context.Background(), record, "google_gmail", agentFromDocument(f.agent))
	return token, err
}

func TestReconnectSurvivesCredentialRefresh(t *testing.T) {
	for _, mode := range []string{modeDelegated, modeInstallation} {
		t.Run(mode, func(t *testing.T) {
			f := reconnectFixture(t, mode)
			expireReconnectCredential(t, f)
			a := beginReconnect(t, f)
			if token, err := refreshReconnectCredential(t, f); err != nil || token != "synthetic-refresh" {
				t.Fatalf("refresh failed: %v", err)
			}
			if reconnectConnection(t, f).CurrentAuthorizationID != a.id || finishReconnect(t, f, a, "code-a") != "complete" {
				t.Fatal("refresh invalidated the pending authorization")
			}
		})
	}
}

func TestReconnectWinsOverInFlightRefresh(t *testing.T) {
	for _, mode := range []string{modeDelegated, modeInstallation} {
		t.Run(mode, func(t *testing.T) {
			f := reconnectFixture(t, mode)
			expireReconnectCredential(t, f)
			a := beginReconnect(t, f)
			base := f.server.providers.client.Transport
			completed := false
			f.server.providers.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.FormValue("grant_type") == "refresh_token" {
					if finishReconnect(t, f, a, "code-a") != "complete" {
						t.Fatal("reconnect failed during refresh")
					}
					completed = true
				}
				return base.RoundTrip(r)
			})
			token, err := refreshReconnectCredential(t, f)
			current := reconnectConnection(t, f)
			if !completed || current.Credential.AccessToken != "synthetic-code-a" || current.RefreshLease != nil || (err == nil && token != current.Credential.AccessToken) {
				t.Fatal("in-flight refresh overwrote or returned a credential superseded by reconnect")
			}
		})
	}
}

func TestReconnectWithoutCurrentAuthorizationFailsClosed(t *testing.T) {
	for _, mode := range []string{modeDelegated, modeInstallation} {
		t.Run(mode, func(t *testing.T) {
			f := reconnectFixture(t, mode)
			a := beginReconnect(t, f)
			current, version, err := f.server.store.getConnection(context.Background(), f.connection.Connection.ID)
			if err != nil {
				t.Fatal(err)
			}
			current.CurrentAuthorizationID = ""
			if err := f.server.store.putConnection(context.Background(), current, version); err != nil {
				t.Fatal(err)
			}
			if finishReconnect(t, f, a, "code-a") != "failed" || !reflect.DeepEqual(current, reconnectConnection(t, f)) {
				t.Fatal("legacy reconnect without a current authorization was accepted")
			}
		})
	}
}

func TestReconnectCancellationConflictPreservesNewAttempt(t *testing.T) {
	for _, mode := range []string{modeDelegated, modeInstallation} {
		t.Run(mode, func(t *testing.T) {
			f := reconnectFixture(t, mode)
			a := beginReconnect(t, f)
			base := f.server.store.client.Transport
			var b reconnectAttempt
			injected := false
			f.server.store.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if !injected && r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/"+toolRecordName("connection", f.connection.Connection.ID)) {
					injected = true
					b = beginReconnect(t, f)
				}
				return base.RoundTrip(r)
			})
			if finishReconnect(t, f, a, "") != "failed" || !injected || reconnectConnection(t, f).CurrentAuthorizationID != b.id {
				t.Fatal("cancellation retry cleared a newer attempt")
			}
			if finishReconnect(t, f, b, "code-b") != "complete" {
				t.Fatal("newer attempt failed after cancellation conflict")
			}
		})
	}
}

func TestReconnectStartRegistrationFailureDoesNotReturnAuthorization(t *testing.T) {
	f := reconnectFixture(t, modeDelegated)
	before := reconnectConnection(t, f)
	base := f.server.store.client.Transport
	f.server.store.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/"+toolRecordName("connection", f.connection.Connection.ID)) {
			return securityJSONResponse(t, http.StatusServiceUnavailable, map[string]string{"message": "synthetic storage failure"}), nil
		}
		return base.RoundTrip(r)
	})
	r := httptest.NewRequest(http.MethodPost, "/v1/connections/"+f.connection.Connection.ID+"/reconnect", nil)
	setPortalRequestHeaders(r, f.grant.Owner, strings.Repeat("b", 43))
	w := httptest.NewRecorder()
	f.server.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "authorizationUrl") || !reflect.DeepEqual(before, reconnectConnection(t, f)) {
		t.Fatal("failed registration returned a sign-in URL or changed the connection")
	}
}

func TestReconnectPendingDoesNotAllowUnboundLoginToReplaceCredential(t *testing.T) {
	f := reconnectFixture(t, modeDelegated)
	a := beginReconnect(t, f)
	before := reconnectConnection(t, f)
	tx := connectionTransaction{Provider: before.Connection.Provider}
	credential := providerCredential{AccessToken: "synthetic-unbound-token"}
	id, err := f.server.store.completeAccount(context.Background(), f.grant.Owner, tx, credential, "original@example.com", before.Connection.AccountID, time.Now())
	if err != nil || id != before.Connection.ID || !reflect.DeepEqual(before, reconnectConnection(t, f)) {
		t.Fatal("healthy repeat login disturbed pending reconnect")
	}
	current, version, err := f.server.store.getConnection(context.Background(), before.Connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	current.Connection.Status = "interaction_required"
	if err := f.server.store.putConnection(context.Background(), current, version); err != nil {
		t.Fatal(err)
	}
	_, err = f.server.store.completeAccount(context.Background(), f.grant.Owner, tx, credential, "original@example.com", before.Connection.AccountID, time.Now())
	if !errors.Is(err, errConflict) || !reflect.DeepEqual(current, reconnectConnection(t, f)) {
		t.Fatal("unbound login replaced credentials during reconnect")
	}
	if finishReconnect(t, f, a, "code-a") != "complete" {
		t.Fatal("current reconnect could not recover the connection")
	}
}
