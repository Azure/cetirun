// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package broker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func operatorRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("X-Forwarded-Client-Cert", "URI=spiffe://cluster.local/ns/cetirun-system/sa/cetirun-operator")
	return request
}

func TestCredentialPolicyIsStrictCanonicalJSON(t *testing.T) {
	valid := `{"revision":"` + testCredentialPolicyRevision + `","services":["azure-resource-manager"]}`
	if _, err := parseCredentialPolicy([]byte(valid)); err != nil {
		t.Fatalf("canonical policy rejected: %v", err)
	}
	for _, raw := range []string{
		` {"revision":"` + testCredentialPolicyRevision + `","services":["azure-resource-manager"]}`,
		`{"services":["azure-resource-manager"],"revision":"` + testCredentialPolicyRevision + `"}`,
		`{"revision":"` + testCredentialPolicyRevision + `","services":["azure-resource-manager"],"extra":true}`,
		`{"revision":"sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","services":["azure-resource-manager"]}`,
		`{"revision":"` + testCredentialPolicyRevision + `","services":["azure-resource-manager","azure-resource-manager"]}`,
	} {
		if _, err := parseCredentialPolicy([]byte(raw)); err == nil {
			t.Fatalf("non-canonical policy accepted: %s", raw)
		}
	}
}

func TestOperatorCredentialPolicyLifecycle(t *testing.T) {
	store, fixture, closeKube := newRecordTestStore(t)
	defer closeKube()
	fixture.policy = nil
	// Aggregate readiness depends on this read-back, so the control-plane API
	// must accept a provisioned identity while the Agent is still pending.
	fixture.agentPhase = "Provisioning"
	server := &Server{cfg: Config{OperatorNamespace: "cetirun-system", OperatorServiceAccount: "cetirun-operator"}, store: store, mux: http.NewServeMux()}
	server.routes()
	policy := `{"revision":"` + testCredentialPolicyRevision + `","services":["azure-resource-manager"]}`

	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, operatorRequest(http.MethodPut, "/operator/v1/instances/"+testAgentUID+"/credential-policy", policy))
	if response.Code != http.StatusOK {
		t.Fatalf("PUT status = %d: %s", response.Code, response.Body.String())
	}
	var put credentialPolicyResponse
	if err := json.NewDecoder(response.Body).Decode(&put); err != nil || put.InstanceUID != testAgentUID || put.Revision != testCredentialPolicyRevision || !credentialPolicyAllows(CredentialPolicy{Services: put.Services}, "azure-resource-manager") {
		t.Fatalf("unexpected PUT response: %#v, %v", put, err)
	}
	if fixture.policy == nil || fixture.policy.Metadata.Labels["agentidentity.cetirun.microsoft.com/instance-uid"] != testAgentUID || len(fixture.policy.Metadata.OwnerReferences) != 1 {
		t.Fatal("PUT did not persist an Agent-owned policy ConfigMap")
	}

	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, operatorRequest(http.MethodGet, "/operator/v1/instances/"+testAgentUID+"/credential-policy", ""))
	if response.Code != http.StatusOK || response.Body.String() != `{"instanceUid":"`+testAgentUID+`","revision":"`+testCredentialPolicyRevision+`","services":["azure-resource-manager"]}`+"\n" {
		t.Fatalf("GET exact read-back failed: %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, operatorRequest(http.MethodDelete, "/operator/v1/instances/"+testAgentUID+"/credential-policy", ""))
	if response.Code != http.StatusNoContent || fixture.policy != nil {
		t.Fatalf("DELETE failed: %d", response.Code)
	}
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, operatorRequest(http.MethodDelete, "/operator/v1/instances/"+testAgentUID+"/credential-policy", ""))
	if response.Code != http.StatusNoContent {
		t.Fatalf("idempotent DELETE status = %d", response.Code)
	}
}

func TestCredentialIssueRejectsStaleOrInactiveAgentState(t *testing.T) {
	tests := map[string]func(*recordAPIFixture){
		"suspended":         func(f *recordAPIFixture) { f.suspended = true },
		"deleting":          func(f *recordAPIFixture) { f.deleting = true },
		"identity disabled": func(f *recordAPIFixture) { f.identityDisabled = true },
		"stale identity": func(f *recordAPIFixture) {
			f.agentGeneration, f.identityObservedGeneration, f.policyObservedGeneration = 8, 7, 8
		},
		"stale policy": func(f *recordAPIFixture) {
			f.agentGeneration, f.identityObservedGeneration, f.policyObservedGeneration = 8, 8, 7
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			store, fixture, closeKube := newRecordTestStore(t)
			defer closeKube()
			mutate(fixture)
			server := &Server{store: store}
			request := httptest.NewRequest(http.MethodPost, "/v1/credentials/issue", strings.NewReader(`{"service":"azure-resource-manager"}`))
			request.Header.Set("X-Forwarded-Client-Cert", "URI=spiffe://cluster.local/ns/cetirun-owner/sa/openclaw-agent-a-cetirun")
			response := httptest.NewRecorder()
			server.issueCredential(response, request)
			if response.Code != http.StatusConflict && response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), "accessToken") {
				t.Fatalf("inactive or stale Agent status = %d: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestCredentialIssueFailsClosedWhenPolicyChangesDuringFinalAuditWrite(t *testing.T) {
	store, fixture, closeKube := newRecordTestStore(t)
	defer closeKube()
	if err := store.putRecord(context.Background(), connectedRecord(), ""); err != nil {
		t.Fatal(err)
	}
	fixture.onSecretPut = func() {
		fixture.desiredRevision = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		fixture.appliedRevision = fixture.desiredRevision
	}
	authSDK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Validate":
			_ = json.NewEncoder(w).Encode(map[string]any{"tid": testOwnerTenantID, "oid": testOwnerObjectID, "aud": testBlueprintID, "azp": testAuthorizationID, "scp": "access_agent"})
		case "/AuthorizationHeader/AzureResourceManager":
			_ = json.NewEncoder(w).Encode(map[string]string{"authorizationHeader": "Bearer delegated-token"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer authSDK.Close()
	server := &Server{cfg: Config{BlueprintClientID: testBlueprintID, AuthorizationClientID: testAuthorizationID}, store: store, authSDK: &authSDKClient{baseURL: authSDK.URL, client: authSDK.Client()}}
	request := httptest.NewRequest(http.MethodPost, "/v1/credentials/issue", strings.NewReader(`{"service":"azure-resource-manager"}`))
	request.Header.Set("X-Forwarded-Client-Cert", "URI=spiffe://cluster.local/ns/cetirun-owner/sa/openclaw-agent-a-cetirun")
	response := httptest.NewRecorder()
	server.issueCredential(response, request)
	if response.Code != http.StatusConflict || strings.Contains(response.Body.String(), "delegated-token") {
		t.Fatalf("changed policy issued a token: %d %s", response.Code, response.Body.String())
	}
}

func TestStaleCredentialPolicyPutCannotOverwriteNewDesiredRevisionAfterConflict(t *testing.T) {
	store, fixture, closeKube := newRecordTestStore(t)
	defer closeKube()
	newRevision := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	fixture.policy.Data["policy.json"] = `{"revision":"` + newRevision + `","services":["azure-resource-manager"]}`
	fixture.policyPutConflicts = 1
	fixture.onPolicyPutConflict = func() {
		fixture.desiredRevision = newRevision
		fixture.appliedRevision = newRevision
	}
	server := &Server{cfg: Config{OperatorNamespace: "cetirun-system", OperatorServiceAccount: "cetirun-operator"}, store: store, mux: http.NewServeMux()}
	server.routes()
	oldPolicy := `{"revision":"` + testCredentialPolicyRevision + `","services":["azure-resource-manager"]}`
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, operatorRequest(http.MethodPut, "/operator/v1/instances/"+testAgentUID+"/credential-policy", oldPolicy))
	if response.Code != http.StatusConflict {
		t.Fatalf("stale PUT status = %d: %s", response.Code, response.Body.String())
	}
	stored, _, err := store.getCredentialPolicy(context.Background(), Agent{Name: testAgentName, UID: testAgentUID})
	if err != nil || stored.Revision != newRevision {
		t.Fatalf("stale PUT overwrote current policy: %#v, %v", stored, err)
	}
}

func TestCredentialIssueFailsClosedWhenPolicyChangesDuringExchange(t *testing.T) {
	store, fixture, closeKube := newRecordTestStore(t)
	defer closeKube()
	if err := store.putRecord(context.Background(), connectedRecord(), ""); err != nil {
		t.Fatal(err)
	}
	authSDK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Validate":
			_ = json.NewEncoder(w).Encode(map[string]any{"tid": testOwnerTenantID, "oid": testOwnerObjectID, "aud": testBlueprintID, "azp": testAuthorizationID, "scp": "access_agent"})
		case "/AuthorizationHeader/AzureResourceManager":
			fixture.desiredRevision = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			fixture.appliedRevision = fixture.desiredRevision
			_ = json.NewEncoder(w).Encode(map[string]string{"authorizationHeader": "Bearer delegated-token"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer authSDK.Close()
	server := &Server{cfg: Config{BlueprintClientID: testBlueprintID, AuthorizationClientID: testAuthorizationID}, store: store, authSDK: &authSDKClient{baseURL: authSDK.URL, client: authSDK.Client()}}
	request := httptest.NewRequest(http.MethodPost, "/v1/credentials/issue", strings.NewReader(`{"service":"azure-resource-manager"}`))
	request.Header.Set("X-Forwarded-Client-Cert", "URI=spiffe://cluster.local/ns/cetirun-owner/sa/openclaw-agent-a-cetirun")
	response := httptest.NewRecorder()
	server.issueCredential(response, request)
	if response.Code != http.StatusConflict || strings.Contains(response.Body.String(), "delegated-token") {
		t.Fatalf("changed policy issued a token: %d %s", response.Code, response.Body.String())
	}
}
