// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/config"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/copilot"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
)

// fakeGitHubServer answers the device flow, the Copilot token exchange and
// the model list.
type fakeGitHubServer struct {
	polls        int
	answers      []map[string]any
	exchangeCode int
}

func (f *fakeGitHubServer) start(t *testing.T) *copilot.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/device/code", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code": "dev-secret", "user_code": "WXYZ-9876",
			"verification_uri": "https://github.com/login/device", "expires_in": 900, "interval": 5,
		})
	})
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		f.polls++
		answer := map[string]any{"error": "authorization_pending"}
		if len(f.answers) > 0 {
			answer, f.answers = f.answers[0], f.answers[1:]
		}
		_ = json.NewEncoder(w).Encode(answer)
	})
	server := httptest.NewServer(mux)
	mux.HandleFunc("GET /copilot_internal/v2/token", func(w http.ResponseWriter, r *http.Request) {
		if f.exchangeCode != 0 {
			w.WriteHeader(f.exchangeCode)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "copilot-token", "endpoints": map[string]string{"api": server.URL}})
	})
	mux.HandleFunc("GET /models", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5.5","name":"GPT-5.5","capabilities":{"type":"chat","limits":{"max_context_window_tokens":400000,"max_output_tokens":128000}}},{"id":"claude-sonnet-4.5","capabilities":{"type":"chat"}}]}`))
	})
	t.Cleanup(server.Close)
	return &copilot.Client{
		HTTP: server.Client(), GitHubURL: server.URL, GitHubAPIURL: server.URL,
		TrustAPIBase: func(u *url.URL) bool { return "http://"+u.Host == server.URL },
	}
}

type loginFixture struct {
	t     *testing.T
	h     *InstanceHandler
	fc    client.Client
	gh    *fakeGitHubServer
	clock time.Time
}

func copilotTokenSecret(uid string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: cetirunv1alpha1.CopilotTokenSecretName, Namespace: "cetirun-alice-gw",
		Labels: map[string]string{kube.AgentUIDLabel: uid},
	}}
}

func newLoginFixture(t *testing.T, objs ...client.Object) *loginFixture {
	t.Helper()
	f := &loginFixture{t: t, gh: &fakeGitHubServer{}, clock: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)}
	if objs == nil {
		objs = []client.Object{routedAgent(), copilotTokenSecret("uid-1")}
	}
	f.fc = newFakeClient(objs...)
	f.h = &InstanceHandler{Kube: kube.NewClient(f.fc), Config: gatewayConfig(), Copilot: f.gh.start(t), Now: func() time.Time { return f.clock }}
	return f
}

func (f *loginFixture) call(method, alias string) (*httptest.ResponseRecorder, CopilotLoginResponse) {
	f.t.Helper()
	body := ""
	if method != http.MethodGet {
		body = `{"uid":"uid-1"}`
	}
	return f.callWith(method, alias, body, nil)
}

// callWith sends body, if any, as the request's JSON body with the headers.
func (f *loginFixture) callWith(method, alias, body string, headers map[string]string) (*httptest.ResponseRecorder, CopilotLoginResponse) {
	f.t.Helper()
	return f.send(method, body, headers, func(r *http.Request) *http.Request {
		return withClaims(r, alias, alias, alias+"@example.com")
	})
}

// send makes the request as the principal that identify attaches.
func (f *loginFixture) send(method, body string, headers map[string]string, identify func(*http.Request) *http.Request) (*httptest.ResponseRecorder, CopilotLoginResponse) {
	f.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/api/v1/instances/alice-gw/copilot/login", reader)
	req.SetPathValue("name", "alice-gw")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	req = identify(req)
	w := httptest.NewRecorder()
	switch method {
	case http.MethodPost:
		f.h.StartCopilotLogin(w, req)
	case http.MethodGet:
		f.h.PollCopilotLogin(w, req)
	case http.MethodDelete:
		f.h.CancelCopilotLogin(w, req)
	}
	var resp CopilotLoginResponse
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			f.t.Fatalf("decode %s: %v", w.Body.String(), err)
		}
	}
	return w, resp
}

func (f *loginFixture) secret(name string) *corev1.Secret {
	f.t.Helper()
	secret := &corev1.Secret{}
	if err := f.fc.Get(context.Background(), client.ObjectKey{Namespace: "cetirun-alice-gw", Name: name}, secret); err != nil {
		f.t.Fatalf("get Secret %s: %v", name, err)
	}
	return secret
}

func (f *loginFixture) agent() *cetirunv1alpha1.Agent {
	f.t.Helper()
	agent := &cetirunv1alpha1.Agent{}
	if err := f.fc.Get(context.Background(), client.ObjectKey{Name: "alice-gw"}, agent); err != nil {
		f.t.Fatal(err)
	}
	return agent
}

func TestCopilotLoginStoresTokenAndModels(t *testing.T) {
	f := newLoginFixture(t)
	w, resp := f.call(http.MethodPost, "alice")
	if w.Code != http.StatusOK || resp.State != "pending" || resp.UserCode != "WXYZ-9876" ||
		resp.VerificationURI != "https://github.com/login/device" || resp.Interval != 5 {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "dev-secret") {
		t.Fatal("the device code must not reach the browser")
	}
	login := f.secret(copilotLoginSecretName)
	if login.Labels[kube.AgentUIDLabel] != "uid-1" || string(login.Data[loginDeviceCode]) != "dev-secret" {
		t.Fatalf("login Secret = %+v", login)
	}

	// Within the interval, polls do not reach GitHub.
	if _, resp = f.call(http.MethodGet, "alice"); resp.State != "pending" || f.gh.polls != 0 {
		t.Fatalf("early poll: %+v, GitHub polls %d", resp, f.gh.polls)
	}
	f.clock = f.clock.Add(6 * time.Second)
	if _, resp = f.call(http.MethodGet, "alice"); resp.State != "pending" || f.gh.polls != 1 {
		t.Fatalf("pending poll: %+v, GitHub polls %d", resp, f.gh.polls)
	}
	// The same second, from another replica or tab: no second GitHub poll.
	if _, resp = f.call(http.MethodGet, "alice"); resp.State != "pending" || f.gh.polls != 1 {
		t.Fatalf("repeated poll: %+v, GitHub polls %d", resp, f.gh.polls)
	}
	f.gh.answers = []map[string]any{{"error": "slow_down", "interval": 10}}
	f.clock = f.clock.Add(6 * time.Second)
	if _, resp = f.call(http.MethodGet, "alice"); resp.State != "pending" || resp.Interval != 10 {
		t.Fatalf("slow_down: %+v", resp)
	}
	f.gh.answers = []map[string]any{{"access_token": "gho_alice", "token_type": "bearer"}}
	f.clock = f.clock.Add(6 * time.Second)
	if _, resp = f.call(http.MethodGet, "alice"); resp.State != "pending" || f.gh.polls != 2 {
		t.Fatalf("slowed poll must wait 10s: %+v, GitHub polls %d", resp, f.gh.polls)
	}
	f.clock = f.clock.Add(5 * time.Second)
	_, resp = f.call(http.MethodGet, "alice")
	if resp.State != "succeeded" || !strings.Contains(resp.Message, "2 models") || resp.UserCode != "" {
		t.Fatalf("success: %+v", resp)
	}
	if token := f.secret(cetirunv1alpha1.CopilotTokenSecretName); string(token.Data[cetirunv1alpha1.CopilotTokenSecretKey]) != "gho_alice" ||
		token.Labels[kube.AgentUIDLabel] != "uid-1" {
		t.Fatalf("token Secret = %+v", token)
	}
	var models []cetirunv1alpha1.CopilotModel
	if err := json.Unmarshal([]byte(f.agent().Annotations[cetirunv1alpha1.AnnotationCopilotModels]), &models); err != nil ||
		len(models) != 2 || models[0].ID != "gpt-5.5" || models[0].ContextWindow != 400000 || models[1].ID != "claude-sonnet-4.5" {
		t.Fatalf("annotation models = %+v (%v)", models, err)
	}
	if f.agent().Annotations[cetirunv1alpha1.AnnotationCopilotSignedInAt] == "" {
		t.Fatal("the sign-in time must be recorded for the operator")
	}
	login = f.secret(copilotLoginSecretName)
	if _, ok := login.Data[loginDeviceCode]; ok || string(login.Data[loginState]) != "succeeded" {
		t.Fatalf("finished login Secret = %v", login.Data)
	}
	// The outcome stays readable without calling GitHub again.
	if _, resp = f.call(http.MethodGet, "alice"); resp.State != "succeeded" || f.gh.polls != 3 {
		t.Fatalf("after success: %+v, GitHub polls %d", resp, f.gh.polls)
	}
}

func TestCopilotLoginOutcomes(t *testing.T) {
	t.Run("account without Copilot", func(t *testing.T) {
		f := newLoginFixture(t)
		f.gh.exchangeCode = http.StatusNotFound
		f.gh.answers = []map[string]any{{"access_token": "gho_nocopilot"}}
		f.call(http.MethodPost, "alice")
		f.clock = f.clock.Add(6 * time.Second)
		if _, resp := f.call(http.MethodGet, "alice"); resp.State != "failed" || !strings.Contains(resp.Message, "cannot use GitHub Copilot") {
			t.Fatalf("resp = %+v", resp)
		}
		if token := f.secret(cetirunv1alpha1.CopilotTokenSecretName); len(token.Data) != 0 {
			t.Fatal("a token without Copilot access must not be stored")
		}
		if _, ok := f.agent().Annotations[cetirunv1alpha1.AnnotationCopilotModels]; ok {
			t.Fatal("no models may be recorded")
		}
	})
	t.Run("denied", func(t *testing.T) {
		f := newLoginFixture(t)
		f.gh.answers = []map[string]any{{"error": "access_denied"}}
		f.call(http.MethodPost, "alice")
		f.clock = f.clock.Add(6 * time.Second)
		if _, resp := f.call(http.MethodGet, "alice"); resp.State != "denied" {
			t.Fatalf("resp = %+v", resp)
		}
	})
	t.Run("expired", func(t *testing.T) {
		f := newLoginFixture(t)
		f.call(http.MethodPost, "alice")
		f.clock = f.clock.Add(16 * time.Minute)
		if _, resp := f.call(http.MethodGet, "alice"); resp.State != "expired" || f.gh.polls != 0 {
			t.Fatalf("resp = %+v, GitHub polls %d", resp, f.gh.polls)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		f := newLoginFixture(t)
		f.call(http.MethodPost, "alice")
		if w, _ := f.call(http.MethodDelete, "alice"); w.Code != http.StatusNoContent {
			t.Fatalf("cancel: %d", w.Code)
		}
		f.clock = f.clock.Add(6 * time.Second)
		if _, resp := f.call(http.MethodGet, "alice"); resp.State != "none" || f.gh.polls != 0 {
			t.Fatalf("after cancel: %+v, GitHub polls %d", resp, f.gh.polls)
		}
	})
	t.Run("token Secret replaced by the operator meanwhile", func(t *testing.T) {
		f := newLoginFixture(t)
		f.gh.answers = []map[string]any{{"access_token": "gho_alice"}}
		f.call(http.MethodPost, "alice")
		token := f.secret(cetirunv1alpha1.CopilotTokenSecretName)
		token.Labels[kube.AgentUIDLabel] = "uid-other"
		if err := f.fc.Update(context.Background(), token); err != nil {
			t.Fatal(err)
		}
		f.clock = f.clock.Add(6 * time.Second)
		if _, resp := f.call(http.MethodGet, "alice"); resp.State != "failed" {
			t.Fatalf("resp = %+v", resp)
		}
		if token := f.secret(cetirunv1alpha1.CopilotTokenSecretName); len(token.Data) != 0 {
			t.Fatal("a Secret of another Agent UID must not receive the token")
		}
	})
}

func TestCopilotLoginPreconditions(t *testing.T) {
	notRouted := routedAgent()
	notRouted.Status.AIGateway.Routed = false
	noCopilot := routedAgent()
	noCopilot.Status.AIGateway.Copilot = nil
	deleting := routedAgent()
	deleting.Finalizers = []string{"test"}
	deleting.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	cases := []struct {
		name   string
		objs   []client.Object
		cfg    *config.Config
		alias  string
		status int
	}{
		{name: "feature off", cfg: &config.Config{}, alias: "alice", status: http.StatusNotFound},
		{name: "another user", alias: "bob", status: http.StatusForbidden},
		{name: "not routed", objs: []client.Object{notRouted, copilotTokenSecret("uid-1")}, alias: "alice", status: http.StatusConflict},
		{name: "copilot off", objs: []client.Object{noCopilot, copilotTokenSecret("uid-1")}, alias: "alice", status: http.StatusConflict},
		{name: "deleting", objs: []client.Object{deleting, copilotTokenSecret("uid-1")}, alias: "alice", status: http.StatusConflict},
		{name: "no token Secret yet", objs: []client.Object{routedAgent()}, alias: "alice", status: http.StatusConflict},
		{name: "token Secret of another Agent", objs: []client.Object{routedAgent(), copilotTokenSecret("uid-old")}, alias: "alice", status: http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLoginFixture(t, tc.objs...)
			if tc.cfg != nil {
				f.h.Config = *tc.cfg
			}
			if w, _ := f.call(http.MethodPost, tc.alias); w.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
		})
	}
}

// An Agent bound to a verified owner accepts sign-in requests only from that
// tenant and object ID: another principal with the same alias can neither
// start, read nor cancel its sign-in.
func TestCopilotLoginRequiresTheBoundOwner(t *testing.T) {
	const tenant, owner, other = "11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222", "33333333-3333-3333-3333-333333333333"
	bound := routedAgent()
	if bound.Annotations == nil {
		bound.Annotations = map[string]string{}
	}
	bound.Annotations[cetirunv1alpha1.AnnotationOwnerTenantID] = tenant
	bound.Annotations[cetirunv1alpha1.AnnotationOwnerObjectID] = owner
	f := newLoginFixture(t, bound, copilotTokenSecret("uid-1"))
	as := func(objectID string) func(*http.Request) *http.Request {
		return func(r *http.Request) *http.Request { return withAgentIdentityClaims(r, "alice", tenant, objectID) }
	}
	bodyFor := func(method string) string {
		if method == http.MethodGet {
			return ""
		}
		return `{"uid":"uid-1"}`
	}
	exists := func() bool {
		t.Helper()
		return f.fc.Get(context.Background(), client.ObjectKey{Namespace: "cetirun-alice-gw", Name: copilotLoginSecretName}, &corev1.Secret{}) == nil
	}

	for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		if w, _ := f.send(method, bodyFor(method), nil, as(other)); w.Code != http.StatusForbidden {
			t.Fatalf("%s by a same-alias principal: status %d, want 403", method, w.Code)
		}
	}
	if exists() {
		t.Fatal("a refused start must not create a sign-in")
	}

	// The bound owner starts a sign-in; the other principal still cannot read
	// or cancel it.
	if w, resp := f.send(http.MethodPost, bodyFor(http.MethodPost), nil, as(owner)); w.Code != http.StatusOK || resp.State != copilotLoginPending {
		t.Fatalf("start by the owner: status %d %s", w.Code, w.Body.String())
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		if w, _ := f.send(method, bodyFor(method), nil, as(other)); w.Code != http.StatusForbidden {
			t.Fatalf("%s of the owner's sign-in by a same-alias principal: status %d, want 403", method, w.Code)
		}
	}
	if state := string(f.secret(copilotLoginSecretName).Data[loginState]); state != copilotLoginPending {
		t.Fatalf("the owner's sign-in must stay pending, got %q", state)
	}
}

// A page left open while its Agent was deleted and created again under the
// same name names the old Agent's UID: it must not start or cancel the new
// Agent's sign-in.
func TestCopilotLoginChangesOnlyTheAgentItWasMadeFor(t *testing.T) {
	f := newLoginFixture(t)
	errorCodeOf := func(w *httptest.ResponseRecorder) string {
		t.Helper()
		var resp ErrorResponse
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return resp.Error.Code
	}
	for _, tc := range []struct {
		name, body string
		headers    map[string]string
		status     int
		code       string
	}{
		{name: "no body", status: http.StatusBadRequest, code: "invalid_body"},
		{name: "no UID", body: `{}`, status: http.StatusPreconditionRequired, code: "version_required"},
		{name: "unknown field", body: `{"uid":"uid-1","name":"alice-gw"}`, status: http.StatusBadRequest, code: "invalid_body"},
		{name: "the replaced Agent", body: `{"uid":"uid-0"}`, status: http.StatusConflict, code: "instance_changed"},
		{name: "another site", body: `{"uid":"uid-1"}`, headers: map[string]string{"Sec-Fetch-Site": "cross-site"}, status: http.StatusForbidden, code: "invalid_origin"},
	} {
		t.Run("start: "+tc.name, func(t *testing.T) {
			w, _ := f.callWith(http.MethodPost, "alice", tc.body, tc.headers)
			if w.Code != tc.status || errorCodeOf(w) != tc.code {
				t.Fatalf("status %d %s, want %d %s", w.Code, w.Body.String(), tc.status, tc.code)
			}
			if err := f.fc.Get(context.Background(), client.ObjectKey{Namespace: "cetirun-alice-gw", Name: copilotLoginSecretName}, &corev1.Secret{}); err == nil {
				t.Fatal("a refused start must not create a sign-in")
			}
		})
	}

	// The current Agent's own page starts a sign-in.
	if w, resp := f.call(http.MethodPost, "alice"); w.Code != http.StatusOK || resp.State != copilotLoginPending {
		t.Fatalf("start: status %d %s", w.Code, w.Body.String())
	}
	if w, _ := f.callWith(http.MethodDelete, "alice", `{"uid":"uid-0"}`, nil); w.Code != http.StatusConflict {
		t.Fatalf("cancel for the replaced Agent: status %d, want 409", w.Code)
	}
	if w, _ := f.callWith(http.MethodDelete, "alice", `{}`, nil); w.Code != http.StatusPreconditionRequired {
		t.Fatalf("cancel without a UID: status %d, want 428", w.Code)
	}
	if state := string(f.secret(copilotLoginSecretName).Data[loginState]); state != copilotLoginPending {
		t.Fatalf("a refused cancel must leave the sign-in pending, got %q", state)
	}
	if w, _ := f.call(http.MethodDelete, "alice"); w.Code != http.StatusNoContent {
		t.Fatalf("cancel: status %d", w.Code)
	}
	if data := f.secret(copilotLoginSecretName).Data; len(data) != 0 {
		t.Fatalf("cancel must clear the sign-in, got %v", data)
	}
}
