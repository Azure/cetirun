// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const (
	cetiAdminOne = "system:serviceaccount:cetirun-system:cetiadmin-one"
	cetiAdminTwo = "system:serviceaccount:cetirun-system:cetiadmin-two"
)

func cetiAdminTestClient(username string, objects ...client.Object) client.Client {
	return cetiAdminTestClientForTokens(map[string]string{"projected-service-account-token": username}, objects...)
}

func cetiAdminTestClientForTokens(usernames map[string]string, objects ...client.Object) client.Client {
	builder := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(objects...)
	return builder.WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, object client.Object, options ...client.CreateOption) error {
		if review, ok := object.(*authnv1.TokenReview); ok {
			username, authenticated := usernames[review.Spec.Token]
			review.Status = authnv1.TokenReviewStatus{
				Authenticated: authenticated,
				User:          authnv1.UserInfo{Username: username},
				Audiences:     []string{"cetirun-governance-actions"},
			}
			return nil
		}
		return c.Create(ctx, object, options...)
	}}).Build()
}

func cetiAdminRequest(path string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer projected-service-account-token")
	return req
}

func cetiAdminSubmitRequest(token, key, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/governance-actions", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	return req
}

func cetiAdminReadyAgent() *cetirunv1alpha1.Agent {
	return &cetirunv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "example", UID: types.UID("agent-uid"), Generation: 3},
		Status: cetirunv1alpha1.AgentStatus{Governance: &cetirunv1alpha1.GovernanceStatus{
			Phase: "Ready", ObservedGeneration: 3, Authorization: &cetirunv1alpha1.GovernanceAuthorizationStatus{Revision: "authorization-revision"},
		}},
	}
}

func TestCetiAdminGovernanceObservationRedactsAgent(t *testing.T) {
	now := metav1.NewTime(time.Date(2026, 10, 7, 8, 30, 0, 0, time.UTC))
	agent := &cetirunv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "example", UID: types.UID("agent-uid")},
		Spec: cetirunv1alpha1.AgentSpec{
			Team: "sensitive-team",
			User: cetirunv1alpha1.UserSpec{Alias: "sensitive-owner"},
		},
		Status: cetirunv1alpha1.AgentStatus{
			Phase:      "Ready",
			Namespace:  "sensitive-namespace",
			AccessURL:  "https://private.example.invalid",
			Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Reconciled", Message: "sensitive condition message"}},
			Governance: &cetirunv1alpha1.GovernanceStatus{
				Phase: "Ready", Coverage: "Enforced",
				Projections: cetirunv1alpha1.GovernanceProjectionsStatus{
					Sandbox:          cetirunv1alpha1.GovernanceRevisionProjection{ExpectedRevision: "sandbox-revision"},
					Gateway:          cetirunv1alpha1.GovernanceRevisionProjection{ExpectedRevision: "gateway-revision"},
					CredentialPolicy: cetirunv1alpha1.GovernanceRevisionProjection{ExpectedRevision: "credential-revision"},
				},
			},
			AIGateway: &cetirunv1alpha1.AIGatewayStatus{
				Phase: "Ready", Blocked: true, BudgetUSD: "10.00", BudgetDuration: "1mo", SpendUSD: "2.50",
				BudgetResetAt: &now, LastVerifiedTime: &now,
				KeyAlias: "sensitive-key-alias", KeyID: "sensitive-key-id", KeySecretName: "sensitive-secret",
				Team: "sensitive-team", Models: []string{"sensitive-model"}, AccessGroups: []string{"sensitive-access-group"},
			},
			AgentIdentity: &cetirunv1alpha1.AgentIdentityStatus{ClientID: "sensitive-client-id", ObjectID: "sensitive-object-id"},
		},
	}
	h := NewCetiAdminHandler(kube.NewClient(cetiAdminTestClient(cetiAdminOne, agent)), "cetirun-governance-actions", []string{cetiAdminOne})
	req := cetiAdminRequest("/internal/v1/agents/example/governance")
	req.SetPathValue("name", "example")
	w := httptest.NewRecorder()

	h.GetAgentGovernanceObservation(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got cetiAdminGovernanceObservation
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "example" || got.Phase != "Ready" || got.Governance.Phase != "Ready" || got.Governance.Coverage != "Enforced" ||
		len(got.Governance.Conditions) != 1 || got.Governance.Conditions[0].Reason != "Reconciled" ||
		got.Governance.Projections.GatewayPolicyRevision != "gateway-revision" || got.Gateway == nil || !got.Gateway.Blocked || got.Gateway.SpendUSD != "2.50" {
		t.Fatalf("unexpected observation: %s", w.Body.String())
	}
	for _, forbidden := range []string{"namespace", "accessUrl", "secret", "keyId", "keyAlias", "team", "models", "objectId", "clientId", "message", "prompt", "log", "sensitive-"} {
		if strings.Contains(strings.ToLower(w.Body.String()), strings.ToLower(forbidden)) {
			t.Fatalf("redacted response contains %q: %s", forbidden, w.Body.String())
		}
	}
}

func TestCetiAdminGovernanceObservationRejectsNonAllowlistedServiceAccount(t *testing.T) {
	// There is deliberately no Agent in this client. A 403 rather than a 404
	// proves authentication fails before the handler reads the requested Agent.
	h := NewCetiAdminHandler(kube.NewClient(cetiAdminTestClient(cetiAdminTwo)), "cetirun-governance-actions", []string{cetiAdminOne})
	req := cetiAdminRequest("/internal/v1/agents/example/governance")
	req.SetPathValue("name", "example")
	w := httptest.NewRecorder()

	h.GetAgentGovernanceObservation(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-allowlisted ServiceAccount status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestCetiAdminSubmitIsIdempotentPerRequester(t *testing.T) {
	const key = "retry-safe-key"
	const body = `{"target":{"kind":"Agent","name":"example"},"operation":"Suspend","reason":"bounded failure","expiresInSeconds":600}`
	client := cetiAdminTestClientForTokens(map[string]string{"token-one": cetiAdminOne}, cetiAdminReadyAgent())
	h := NewCetiAdminHandler(kube.NewClient(client), "cetirun-governance-actions", []string{cetiAdminOne})
	h.Now = func() time.Time { return time.Date(2026, 10, 7, 8, 30, 0, 0, time.UTC) }

	first := httptest.NewRecorder()
	h.Submit(first, cetiAdminSubmitRequest("token-one", key, body))
	if first.Code != http.StatusCreated {
		t.Fatalf("first submit status=%d body=%s", first.Code, first.Body.String())
	}
	var created cetirunv1alpha1.GovernanceAction
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Name != cetiAdminActionName(cetiAdminOne, key) || strings.Contains(first.Body.String(), key) {
		t.Fatalf("idempotency key was not safely derived: %s", first.Body.String())
	}

	retry := httptest.NewRecorder()
	h.Submit(retry, cetiAdminSubmitRequest("token-one", key, body))
	if retry.Code != http.StatusOK {
		t.Fatalf("idempotent retry status=%d body=%s", retry.Code, retry.Body.String())
	}
	var returned cetirunv1alpha1.GovernanceAction
	if err := json.Unmarshal(retry.Body.Bytes(), &returned); err != nil {
		t.Fatal(err)
	}
	if returned.Name != created.Name || returned.Spec.RequestedAt != created.Spec.RequestedAt || returned.Spec.ExpiresAt != created.Spec.ExpiresAt {
		t.Fatalf("retry did not return the original action: first=%s retry=%s", first.Body.String(), retry.Body.String())
	}

	different := httptest.NewRecorder()
	h.Submit(different, cetiAdminSubmitRequest("token-one", key, `{"target":{"kind":"Agent","name":"example"},"operation":"Resume","reason":"bounded failure","expiresInSeconds":600}`))
	if different.Code != http.StatusConflict {
		t.Fatalf("same key with different request status=%d body=%s", different.Code, different.Body.String())
	}
}

func TestCetiAdminSubmitAllowsEmergencySuspendWhileGovernanceIsHeld(t *testing.T) {
	agent := cetiAdminReadyAgent()
	agent.Status.Governance.Phase = "Partial"
	client := cetiAdminTestClient(cetiAdminOne, agent)
	h := NewCetiAdminHandler(kube.NewClient(client), "cetirun-governance-actions", []string{cetiAdminOne})

	suspend := httptest.NewRecorder()
	h.Submit(suspend, cetiAdminSubmitRequest(
		"projected-service-account-token",
		"held-suspend",
		`{"target":{"kind":"Agent","name":"example"},"operation":"Suspend","reason":"emergency hold","expiresInSeconds":600}`,
	))
	if suspend.Code != http.StatusCreated {
		t.Fatalf("held suspend status=%d body=%s", suspend.Code, suspend.Body.String())
	}

	resume := httptest.NewRecorder()
	h.Submit(resume, cetiAdminSubmitRequest(
		"projected-service-account-token",
		"held-resume",
		`{"target":{"kind":"Agent","name":"example"},"operation":"Resume","reason":"resume held workload","expiresInSeconds":600}`,
	))
	if resume.Code != http.StatusConflict || !strings.Contains(resume.Body.String(), `"code":"governance_not_ready"`) {
		t.Fatalf("held resume status=%d body=%s", resume.Code, resume.Body.String())
	}
}

func TestCetiAdminSubmitIdempotencyKeyIsIsolatedByRequester(t *testing.T) {
	const key = "same-key-different-requester"
	const body = `{"target":{"kind":"Agent","name":"example"},"operation":"Suspend","reason":"bounded failure","expiresInSeconds":600}`
	client := cetiAdminTestClientForTokens(map[string]string{"token-one": cetiAdminOne, "token-two": cetiAdminTwo}, cetiAdminReadyAgent())
	k := kube.NewClient(client)
	one := NewCetiAdminHandler(k, "cetirun-governance-actions", []string{cetiAdminOne, cetiAdminTwo})
	two := NewCetiAdminHandler(k, "cetirun-governance-actions", []string{cetiAdminOne, cetiAdminTwo})
	for _, tc := range []struct {
		handler *CetiAdminHandler
		token   string
	}{
		{one, "token-one"},
		{two, "token-two"},
	} {
		response := httptest.NewRecorder()
		tc.handler.Submit(response, cetiAdminSubmitRequest(tc.token, key, body))
		if response.Code != http.StatusCreated {
			t.Fatalf("requester %q status=%d body=%s", tc.token, response.Code, response.Body.String())
		}
	}
	actions, err := k.ListGovernanceActions(context.Background())
	if err != nil || len(actions) != 2 || actions[0].Name == actions[1].Name {
		t.Fatalf("same key was not isolated by requester: actions=%#v err=%v", actions, err)
	}
}

func TestCetiAdminSubmitRequiresTypedAgentTargetAndIdempotencyKey(t *testing.T) {
	h := NewCetiAdminHandler(kube.NewClient(cetiAdminTestClient(cetiAdminOne, cetiAdminReadyAgent())), "cetirun-governance-actions", []string{cetiAdminOne})
	for _, tc := range []struct {
		name string
		key  string
		body string
	}{
		{"missing key", "", `{"target":{"kind":"Agent","name":"example"},"operation":"Suspend","reason":"bounded failure"}`},
		{"legacy string target", "key", `{"target":"example","operation":"Suspend","reason":"bounded failure"}`},
		{"non-agent target", "key", `{"target":{"kind":"Team","name":"example"},"operation":"Suspend","reason":"bounded failure"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			h.Submit(response, cetiAdminSubmitRequest("projected-service-account-token", tc.key, tc.body))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestCetiAdminGetActionRequiresOriginalRequester(t *testing.T) {
	action := &cetirunv1alpha1.GovernanceAction{
		ObjectMeta: metav1.ObjectMeta{Name: "action-one"},
		Spec:       cetirunv1alpha1.GovernanceActionSpec{Requester: cetirunv1alpha1.GovernanceActionRequester{Kind: "CetiAdmin", Subject: cetiAdminOne}},
	}
	h := NewCetiAdminHandler(kube.NewClient(cetiAdminTestClient(cetiAdminTwo, action)), "cetirun-governance-actions", []string{cetiAdminOne, cetiAdminTwo})
	req := cetiAdminRequest("/internal/v1/governance-actions/action-one")
	req.SetPathValue("name", "action-one")
	w := httptest.NewRecorder()

	h.Get(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("different trusted ServiceAccount read status=%d body=%s", w.Code, w.Body.String())
	}
}
