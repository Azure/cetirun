// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestAuthorizationRandomValuesFailClosed(t *testing.T) {
	for failAt := 1; failAt <= 3; failAt++ {
		calls := 0
		_, _, _, err := authorizationRandomValues(func(_ int) (string, error) {
			calls++
			if calls == failAt {
				return "", errors.New("random source failed")
			}
			return "random", nil
		})
		if err == nil || calls != failAt {
			t.Fatalf("random failure %d was not propagated: calls=%d err=%v", failAt, calls, err)
		}
	}
}

func TestOperatorPrincipalIsExact(t *testing.T) {
	cfg := Config{OperatorNamespace: "cetirun-system", OperatorServiceAccount: "cetirun-operator"}
	valid := `URI=spiffe://cluster.local/ns/cetirun-system/sa/cetirun-operator`
	if !operatorPrincipalAllowed(valid, cfg) {
		t.Fatal("exact operator principal was rejected")
	}
	for _, value := range []string{
		`URI=spiffe://cluster.local/ns/cetirun-system/sa/other`,
		`URI=spiffe://cluster.local/ns/other/sa/cetirun-operator`,
		valid + `,URI=spiffe://cluster.local/ns/cetirun-system/sa/other`,
	} {
		if operatorPrincipalAllowed(value, cfg) {
			t.Fatalf("unexpected operator principal was accepted: %q", value)
		}
	}
}

func TestCallbackInputValidationAndValidTransactionRateLimit(t *testing.T) {
	if !validOAuthState(strings.Repeat("a", 43)) || validOAuthState("short") || validOAuthState(strings.Repeat("!", 43)) {
		t.Fatal("OAuth state validation contract is incorrect")
	}
	if !validAuthorizationCode("opaque.code-value_1") || validAuthorizationCode("") || validAuthorizationCode("bad\ncode") || validAuthorizationCode(strings.Repeat("a", 8193)) {
		t.Fatal("authorization code validation contract is incorrect")
	}
	server := &Server{}
	now := time.Now()
	validState := strings.Repeat("a", 43)
	record := &AuthorizationRecord{Transaction: &Transaction{StateHash: stateHash(validState), ExpiresAt: now.Add(time.Minute)}}
	for index := 0; index < maxCallbacksPerSecond*2; index++ {
		valid, admitted := server.callbackAdmission(record, stateHash(strings.Repeat("b", 43)), now)
		if valid || admitted {
			t.Fatal("an unissued state was admitted")
		}
	}
	for index := 0; index < maxCallbacksPerSecond; index++ {
		valid, admitted := server.callbackAdmission(record, stateHash(validState), now)
		if !valid || !admitted {
			t.Fatalf("callback %d was rejected before the limit", index)
		}
	}
	if valid, admitted := server.callbackAdmission(record, stateHash(validState), now); !valid || admitted {
		t.Fatal("callback above the per-second limit was accepted")
	}
	if valid, admitted := server.callbackAdmission(record, stateHash(validState), now.Add(time.Second)); !valid || !admitted {
		t.Fatal("callback limit did not reset")
	}
	for index := 0; index < maxCallbackLookupsPerSecond; index++ {
		if !server.allowCallbackLookup(now) {
			t.Fatalf("lookup %d was rejected before the limit", index)
		}
	}
	if server.allowCallbackLookup(now) {
		t.Fatal("lookup above the per-second limit was accepted")
	}
	if !server.allowCallbackLookup(now.Add(time.Second)) {
		t.Fatal("lookup limit did not reset")
	}
}

func TestCallbackCannotPersistAfterReconnectReplacesTransaction(t *testing.T) {
	stateA, stateB := strings.Repeat("a", 43), strings.Repeat("b", 43)
	callbackA := &AuthorizationRecord{Generation: 7, Transaction: &Transaction{StateHash: stateHash(stateA), Consumed: true}}
	// Callback A consumed its state, then Connect/Reconnect persisted B in the
	// same generation before A completed the token exchange.
	current := &AuthorizationRecord{Generation: 7, Transaction: &Transaction{StateHash: stateHash(stateB), Consumed: false}}
	if callbackCanPersist(callbackA, current, stateHash(stateA)) {
		t.Fatal("stale callback could overwrite a newer Connect/Reconnect transaction")
	}
	current.Transaction.Consumed = true
	if callbackCanPersist(callbackA, current, stateHash(stateA)) {
		t.Fatal("stale callback could overwrite a newer consumed transaction")
	}
	current.Transaction.StateHash = stateHash(stateA)
	if !callbackCanPersist(callbackA, current, stateHash(stateA)) {
		t.Fatal("current consumed callback transaction was rejected")
	}
}

func TestAuthorizationRevocationCreatesGenerationBarrier(t *testing.T) {
	record := &AuthorizationRecord{
		Generation:     7,
		Revoked:        false,
		Transaction:    &Transaction{StateHash: "state"},
		Tokens:         &TokenCache{AccessToken: "access", RefreshToken: "refresh"},
		AccountDisplay: "user@example.invalid",
	}
	revokeAuthorizationRecord(record)
	if record.Generation != 8 || !record.Revoked || record.Transaction != nil || record.Tokens != nil || record.AccountDisplay != "" {
		t.Fatalf("authorization record was not safely tombstoned: %#v", record)
	}
}

func TestAuthorizationURLUsesServerSideCodeFlow(t *testing.T) {
	cfg := Config{TenantID: "11111111-1111-1111-1111-111111111111", BlueprintClientID: "22222222-2222-2222-2222-222222222222", AuthorizationClientID: "33333333-3333-3333-3333-333333333333", RedirectURI: "https://portal.example.com/api/v1/agent-authorization/callback", RequestTimeout: time.Second}
	raw := newOAuthClient(cfg).authorizationURL("state", "nonce", "challenge", "owner@example.com")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("response_type") != "code" || q.Get("response_mode") != "form_post" || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("unexpected OAuth URL: %s", raw)
	}
	if q.Get("scope") != "api://22222222-2222-2222-2222-222222222222/access_agent offline_access openid profile" {
		t.Fatalf("unexpected scope: %q", q.Get("scope"))
	}
	if q.Get("login_hint") != "owner@example.com" {
		t.Fatalf("unexpected login hint: %q", q.Get("login_hint"))
	}
	if q.Get("prompt") != "" {
		t.Fatalf("authorization URL must allow SSO instead of forcing account selection: %q", q.Get("prompt"))
	}
	if strings.Contains(raw, "client_secret") {
		t.Fatal("OAuth URL must not contain a client secret")
	}
}

func TestAuthorizationURLAcceptsOpaqueEntraLoginHint(t *testing.T) {
	cfg := Config{TenantID: "11111111-1111-1111-1111-111111111111", BlueprintClientID: "22222222-2222-2222-2222-222222222222", AuthorizationClientID: "33333333-3333-3333-3333-333333333333", RedirectURI: "https://portal.example.com/api/v1/agent-authorization/callback", RequestTimeout: time.Second}
	hint := "opaque-base64-login-hint=="
	raw := newOAuthClient(cfg).authorizationURL("state", "nonce", "challenge", hint)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("login_hint") != hint {
		t.Fatalf("opaque Entra login hint was not preserved: %q", u.Query().Get("login_hint"))
	}
}

func TestAuthorizationURLRejectsUnsafeLoginHint(t *testing.T) {
	cfg := Config{TenantID: "11111111-1111-1111-1111-111111111111", BlueprintClientID: "22222222-2222-2222-2222-222222222222", AuthorizationClientID: "33333333-3333-3333-3333-333333333333", RedirectURI: "https://portal.example.com/api/v1/agent-authorization/callback", RequestTimeout: time.Second}
	for _, hint := range []string{"", "owner@example.com\nextra", strings.Repeat("a", 1025)} {
		raw := newOAuthClient(cfg).authorizationURL("state", "nonce", "challenge", hint)
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if u.Query().Get("login_hint") != "" {
			t.Fatalf("unsafe login hint was included: %q", hint)
		}
	}
}

func TestCallbackPageReportsCompletionOutcome(t *testing.T) {
	server := &Server{cfg: Config{PortalOrigin: "https://portal.example.com"}}
	for _, test := range []struct {
		name    string
		success bool
		status  string
		message string
	}{
		{name: "success", success: true, status: "complete", message: "Sign-in complete"},
		{name: "failure", success: false, status: "failed", message: "Sign-in could not be completed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			server.callbackPage(response, test.success)
			body := response.Body.String()
			if !strings.Contains(body, `status:"`+test.status+`"`) || !strings.Contains(body, test.message) {
				t.Fatalf("callback page did not report %s outcome: %s", test.name, body)
			}
			if !strings.Contains(body, `"https://portal.example.com"`) {
				t.Fatal("callback page did not restrict postMessage to the Portal origin")
			}
		})
	}
}

func TestCallbackPageReportsOwnerMismatch(t *testing.T) {
	server := &Server{cfg: Config{PortalOrigin: "https://portal.example.com"}}
	response := httptest.NewRecorder()
	server.callbackPageResult(response, "owner_mismatch", "Sign in with the same Microsoft Entra account used by the Portal, then try again.")
	body := response.Body.String()
	if !strings.Contains(body, `status:"owner_mismatch"`) || !strings.Contains(body, "same Microsoft Entra account") {
		t.Fatalf("callback page did not report owner mismatch: %s", body)
	}
}

func TestWorkloadPrincipal(t *testing.T) {
	got, err := workloadPrincipal(`By=spiffe://cluster.local/ns/istio-system/sa/istio;Hash=abc;URI=spiffe://cluster.local/ns/cetirun-alice/sa/openclaw-alice-cetirun`)
	if err != nil {
		t.Fatal(err)
	}
	if got != [2]string{"cetirun-alice", "openclaw-alice-cetirun"} {
		t.Fatalf("unexpected principal: %#v", got)
	}
	for _, raw := range []string{"", "URI=https://cluster.local/ns/cetirun-a/sa/openclaw-a-cetirun", "URI=spiffe://evil/ns/cetirun-a/sa/openclaw-a-cetirun"} {
		if _, err := workloadPrincipal(raw); err == nil {
			t.Fatalf("accepted invalid XFCC %q", raw)
		}
	}
	for _, raw := range []string{
		"URI=spiffe://cluster.local/ns/cetirun-a/sa/openclaw-a-cetirun;URI=spiffe://cluster.local/ns/cetirun-b/sa/openclaw-b-cetirun",
		"URI=spiffe://cluster.local/ns/cetirun-a/sa/openclaw-a-cetirun,URI=spiffe://cluster.local/ns/cetirun-b/sa/openclaw-b-cetirun",
	} {
		if _, err := workloadPrincipal(raw); err == nil {
			t.Fatalf("accepted ambiguous XFCC %q", raw)
		}
	}
}

func TestAuthorizationRecordMustMatchCurrentAgent(t *testing.T) {
	agent := Agent{
		Name: "agent-a", UID: "uid-a",
		OwnerTenantID: "tenant-a", OwnerObjectID: "owner-a",
		AgentIdentityClientID: "client-a",
	}
	record := &AuthorizationRecord{
		InstanceName: "agent-a", InstanceUID: "uid-a",
		OwnerTenantID: "tenant-a", OwnerObjectID: "owner-a",
		AgentIdentityClientID: "client-a",
	}
	if !recordMatchesAgent(record, agent) {
		t.Fatal("matching authorization record was rejected")
	}
	record.OwnerTenantID = "TENANT-A"
	record.OwnerObjectID = "OWNER-A"
	record.AgentIdentityClientID = "CLIENT-A"
	if !recordMatchesAgent(record, agent) {
		t.Fatal("case-only identity ID differences were rejected")
	}
	record.OwnerObjectID = "owner-b"
	if recordMatchesAgent(record, agent) {
		t.Fatal("authorization record for an old owner was accepted")
	}
}

func TestAccessTokenFromAuthorizationHeader(t *testing.T) {
	token, err := accessTokenFromAuthorizationHeader("Bearer synthetic-token")
	if err != nil || token != "synthetic-token" {
		t.Fatalf("valid bearer credential rejected: token=%q err=%v", token, err)
	}
	for _, value := range []string{
		"", "Basic synthetic-token", "bearer synthetic-token", "Bearer ",
		"Bearer token with spaces", "Bearer token\nsecond-header",
		"Bearer " + strings.Repeat("a", (32<<10)+1),
	} {
		if _, err := accessTokenFromAuthorizationHeader(value); err == nil {
			t.Fatalf("invalid bearer credential accepted: %q", value)
		}
	}
}
