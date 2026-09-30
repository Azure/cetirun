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

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/auth"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/config"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const managementTenant = "11111111-1111-1111-1111-111111111111"
const managementOwner = "22222222-2222-2222-2222-222222222222"

var managementImage = "registry.example.com/team/assistant@sha256:" + strings.Repeat("a", 64)
var replacementImage = "registry.example.com/team/assistant@sha256:" + strings.Repeat("b", 64)

func customManagementHandler(objects ...client.Object) *InstanceHandler {
	return &InstanceHandler{Kube: kube.NewClient(newFakeClient(objects...)), Config: config.Config{
		AllowedAgentTypes: []string{"hermes", "openclaw", "custom"},
	}}
}

func customManagementInstance() *api.Agent {
	instance := sampleInstance()
	instance.Spec.Agent = api.HarnessSpec{Type: api.AgentTypeCustom, Image: ptr.To(managementImage), Username: ptr.To("vibe"), Port: ptr.To(int32(8080))}
	instance.Spec.User.Team = ""
	instance.Annotations = map[string]string{api.AnnotationOwnerTenantID: managementTenant, api.AnnotationOwnerObjectID: managementOwner}
	return instance
}

func managementRequest(method, body string) *http.Request {
	r := httptest.NewRequest(method, "/api/v1/instances/alice", strings.NewReader(body))
	r.SetPathValue("name", "alice")
	return withAgentIdentityClaims(r, "alice", managementTenant, managementOwner)
}

func TestCustomCreateRoundTripWithoutNativeSideEffects(t *testing.T) {
	for _, port := range []string{"", `,"port":8080`} {
		h := customManagementHandler()
		fc := newFakeClient()
		h.Kube = kube.NewClient(fc)
		body := `{"agent":{"type":"custom","image":"` + managementImage + `","username":"assistant"` + port + `},"displayName":"Research Assistant"}`
		w := httptest.NewRecorder()
		h.CreateInstance(w, managementRequest(http.MethodPost, body))
		if w.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", w.Code, w.Body.String())
		}
		var response InstanceResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(w.Body.Bytes(), []byte(`"agentType"`)) || response.UID == "" || response.ResourceVersion == "" || response.Phase != "Pending" ||
			response.Agent.Type != api.AgentTypeCustom || *response.Agent.Username != "assistant" ||
			(port == "") != (response.Agent.Port == nil) || response.AccessURL != "" {
			t.Fatalf("invalid response: %s", w.Body.String())
		}
		stored, err := h.Kube.GetInstance(context.Background(), "alice")
		if err != nil {
			t.Fatal(err)
		}
		if stored.Spec.GatewayTokenSecretRef != "" || stored.Spec.Tailscale != nil || stored.Spec.MSTeams != nil || stored.Spec.AgentIdentity != nil ||
			stored.Annotations[api.AnnotationOwnerObjectID] != managementOwner {
			t.Fatal("Custom gained native configuration or lost its verified owner")
		}
		namespaces, secrets := &corev1.NamespaceList{}, &corev1.SecretList{}
		if err := fc.List(context.Background(), namespaces); err != nil {
			t.Fatal(err)
		}
		if err := fc.List(context.Background(), secrets); err != nil {
			t.Fatal(err)
		}
		if len(namespaces.Items) != 0 || len(secrets.Items) != 0 {
			t.Fatal("Custom creation provisioned native resources")
		}
	}
}

func TestCustomCreateUsesExistingPortalAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name     string
		user     string
		image    string
		team     string
		disabled bool
		code     int
	}{
		{"signed-in owner", "alice@example.com", managementImage, "", false, http.StatusCreated},
		{"another owner and image", "bob@example.com", replacementImage, "example-team", false, http.StatusCreated},
		{"missing identity", "", managementImage, "", false, http.StatusUnauthorized},
		{"type disabled", "alice@example.com", managementImage, "", true, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &InstanceHandler{Kube: kube.NewClient(newFakeClient(&api.Team{ObjectMeta: metav1.ObjectMeta{Name: "example-team"}})), Config: config.Config{
				AllowedAgentTypes: []string{"hermes", "openclaw", "custom"},
			}}
			if tc.disabled {
				h.Config.AllowedAgentTypes = []string{"hermes", "openclaw"}
			}
			body, err := json.Marshal(map[string]any{"agent": map[string]any{"type": "custom", "image": tc.image}, "team": tc.team})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodPost, "/api/v1/instances", bytes.NewReader(body))
			if tc.user != "" {
				r.Header.Set("X-Auth-Request-Preferred-Username", tc.user)
				r.Header.Set("X-Auth-Request-Email", tc.user)
			}
			w := httptest.NewRecorder()
			auth.Middleware(nil)(http.HandlerFunc(h.CreateInstance)).ServeHTTP(w, r)
			if w.Code != tc.code {
				t.Fatalf("status=%d want=%d: %s", w.Code, tc.code, w.Body.String())
			}
			instances, err := h.Kube.ListAllInstances(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if tc.code != http.StatusCreated {
				if len(instances) != 0 {
					t.Fatal("rejected request created an Agent")
				}
				return
			}
			if len(instances) != 1 {
				t.Fatalf("created %d Agents, want 1", len(instances))
			}
			stored := instances[0]
			if stored.Spec.User.Alias != strings.Split(tc.user, "@")[0] || stored.Spec.User.Team != tc.team ||
				stored.Annotations[api.AnnotationOwnerTenantID] != "" || stored.Annotations[api.AnnotationOwnerObjectID] != "" ||
				stored.Spec.AgentIdentity != nil || stored.Spec.GatewayTokenSecretRef != "" {
				t.Fatal("Custom creation changed the existing owner or native integration boundary")
			}
		})
	}
}

func TestManagementRejectsOldUnknownAndNativeCustomFields(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"agent":null}`, `{"agentType":"hermes"}`, `{"agent":{},"agentType":"hermes"}`,
		`{"agent":{},"unknown":true}`, `{"agent":{"unknown":true}}`,
		`{"agent":{"type":"hermes","type":"openclaw"}}`,
		`{"agent":{},"registryAuth":{"password":"sk-dummy"}}`,
		`{"agent":{"registrySecretRef":{"name":"unowned"}}}`,
		`{"agent":{"type":"custom","image":"` + managementImage + `"},"gatewayToken":""}`,
		`{"agent":{"type":"custom","image":"` + managementImage + `"},"teamsEnabled":false}`,
		`{"agent":{"type":"custom","image":"` + managementImage + `"},"tailscaleAuthKey":null}`,
		`{"agent":{"type":"custom","image":"` + managementImage + `"},"agentIdentityEnabled":true}`,
	} {
		t.Run(body, func(t *testing.T) {
			fc := newFakeClient()
			h := customManagementHandler()
			h.Kube = kube.NewClient(fc)
			w := httptest.NewRecorder()
			h.CreateInstance(w, managementRequest(http.MethodPost, body))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d: %s", w.Code, w.Body.String())
			}
			for _, list := range []client.ObjectList{&api.AgentList{}, &corev1.NamespaceList{}, &corev1.SecretList{}} {
				if err := fc.List(context.Background(), list); err != nil {
					t.Fatal(err)
				}
				data, _ := json.Marshal(list)
				var result struct{ Items []json.RawMessage }
				_ = json.Unmarshal(data, &result)
				if len(result.Items) != 0 {
					t.Fatal("rejected request created resources")
				}
			}
		})
	}
}

func TestDefaultBuiltinCreationUsesExistingPortalIdentity(t *testing.T) {
	for _, kind := range []string{"hermes", "openclaw"} {
		h := customManagementHandler()
		r := managementRequest(http.MethodPost, `{"agent":{"type":"`+kind+`"}}`)
		r = withClaims(r, "alice", "Alice", "alice@example.com")
		w := httptest.NewRecorder()
		h.CreateInstance(w, r)
		if w.Code != http.StatusCreated {
			t.Fatalf("default %s creation failed: %d %s", kind, w.Code, w.Body.String())
		}
	}
}

func TestCustomPatchContractAndPreconditions(t *testing.T) {
	for _, tc := range []struct {
		body string
		code int
	}{
		{`{"agent":{"port":9090}}`, http.StatusPreconditionRequired},
		{`{"uid":"old-uid","resourceVersion":"1","agent":{"port":9090}}`, http.StatusConflict},
		{`{"uid":"agent-alice","resourceVersion":"old","agent":{"port":9090}}`, http.StatusConflict},
		{`{"uid":"agent-alice","resourceVersion":"1","agent":{"type":"hermes"}}`, http.StatusBadRequest},
		{`{"uid":"agent-alice","resourceVersion":"1","agent":{"username":"other"}}`, http.StatusBadRequest},
		{`{"uid":"agent-alice","resourceVersion":"1","agent":{"image":null}}`, http.StatusBadRequest},
		{`{"uid":"agent-alice","resourceVersion":"1","agent":{"image":"` + replacementImage + `"}}`, http.StatusConflict},
		{`{"uid":"agent-alice","resourceVersion":"1","agent":{"registrySecretRef":null}}`, http.StatusBadRequest},
		{`{"uid":"agent-alice","resourceVersion":"1","agent":null}`, http.StatusBadRequest},
		{`{"uid":"agent-alice","resourceVersion":"1","agent":{"port":null}}`, http.StatusOK},
		{`{"uid":"agent-alice","resourceVersion":"1","agent":{"port":9090}}`, http.StatusOK},
	} {
		t.Run(tc.body, func(t *testing.T) {
			h := customManagementHandler(customManagementInstance())
			w := httptest.NewRecorder()
			h.UpdateInstanceByName(w, managementRequest(http.MethodPatch, tc.body))
			if w.Code != tc.code {
				t.Fatalf("status=%d want=%d: %s", w.Code, tc.code, w.Body.String())
			}
			stored, err := h.Kube.GetInstance(context.Background(), "alice")
			if err != nil || stored.UID != "agent-alice" || *stored.Spec.Agent.Image != managementImage {
				t.Fatalf("identity or image changed: %v", err)
			}
			if tc.code != http.StatusOK && *stored.Spec.Agent.Port != 8080 {
				t.Fatal("rejected patch changed the port")
			}
			if tc.code == http.StatusOK && strings.Contains(tc.body, `"port":null`) && stored.Spec.Agent.Port != nil {
				t.Fatal("explicit null did not clear the HTTP port")
			}
		})
	}
}

func TestVerifiedOwnerCannotBeReplacedByMatchingAlias(t *testing.T) {
	for _, byName := range []bool{true, false} {
		h := customManagementHandler(customManagementInstance())
		r := managementRequest(http.MethodGet, "")
		r = withAgentIdentityClaims(r, "alice", managementTenant, "33333333-3333-3333-3333-333333333333")
		w := httptest.NewRecorder()
		if byName {
			h.GetInstanceByName(w, r)
		} else {
			h.GetInstance(w, r)
		}
		if w.Code != http.StatusForbidden {
			t.Fatalf("byName=%v: status=%d", byName, w.Code)
		}
	}
}

func TestDisabledTypePreservesOwnerReadAndDelete(t *testing.T) {
	h := customManagementHandler(customManagementInstance())
	h.Config.AllowedAgentTypes = []string{"hermes", "openclaw"}
	w := httptest.NewRecorder()
	h.GetInstanceByName(w, managementRequest(http.MethodGet, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("owner lost read access after type was disabled: %d", w.Code)
	}
	w = httptest.NewRecorder()
	h.UpdateInstanceByName(w, managementRequest(http.MethodPatch, `{"uid":"agent-alice","resourceVersion":"1","agent":{"port":9090}}`))
	if w.Code != http.StatusForbidden {
		t.Fatalf("runtime update ignored the disabled type: %d", w.Code)
	}
	w = httptest.NewRecorder()
	h.DeleteInstanceByName(w, managementRequest(http.MethodDelete, `{"uid":"agent-alice","resourceVersion":"1"}`))
	if w.Code != http.StatusAccepted {
		t.Fatalf("owner lost delete access after type was disabled: %d %s", w.Code, w.Body.String())
	}
}

func TestCustomManagementPreservesOwnerChecks(t *testing.T) {
	for _, operation := range []string{"read", "update", "delete", "upgrade"} {
		t.Run(operation, func(t *testing.T) {
			instance := customManagementInstance()
			instance.Annotations = nil
			h := customManagementHandler(instance)
			r := managementRequest(http.MethodPost, `{"uid":"agent-alice","resourceVersion":"1","agent":{"port":9090}}`)
			r = withClaims(r, "bob", "Bob", "bob@example.com")
			w := httptest.NewRecorder()
			switch operation {
			case "read":
				h.GetInstanceByName(w, r)
			case "update":
				h.UpdateInstanceByName(w, r)
			case "delete":
				h.DeleteInstanceByName(w, r)
			case "upgrade":
				h.UpgradeInstanceByName(w, r)
			}
			if w.Code != http.StatusForbidden {
				t.Fatalf("status=%d: %s", w.Code, w.Body.String())
			}
			stored, err := h.Kube.GetInstance(context.Background(), "alice")
			if err != nil || *stored.Spec.Agent.Port != 8080 || !stored.DeletionTimestamp.IsZero() {
				t.Fatalf("another user's request changed the Agent: %v", err)
			}
		})
	}
}

func TestCustomOwnerCanUpdateWithoutAdditionalPolicy(t *testing.T) {
	instance := customManagementInstance()
	instance.Annotations = nil
	h := customManagementHandler(instance)
	r := managementRequest(http.MethodPatch, `{"uid":"agent-alice","resourceVersion":"1","agent":{"port":9090}}`)
	r = withClaims(r, "alice", "Alice", "alice@example.com")
	w := httptest.NewRecorder()
	h.UpdateInstanceByName(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d: %s", w.Code, w.Body.String())
	}
	stored, err := h.Kube.GetInstance(context.Background(), "alice")
	if err != nil || *stored.Spec.Agent.Port != 9090 {
		t.Fatalf("owner's port update was not applied: %v", err)
	}
}
