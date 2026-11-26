// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/auth"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// cetiAdminInstance is the Agent that the operator installs for CetiAdmin,
// with the annotations that the operator sets on it. Its owner is the
// verified user of cetiAdminRequest.
func cetiAdminInstance() *api.Agent {
	instance := sampleInstance()
	instance.Name, instance.UID, instance.Generation = "cetiadmin", "cetiadmin-agent-uid", 3
	instance.Spec.Agent.Type = api.AgentTypeHermes
	instance.Spec.Team, instance.Spec.SandboxRef, instance.Spec.Model = "cetiadmin", "", "auto"
	instance.Spec.Helm.Version = "0.2.1"
	instance.Spec.User.Email = ""
	instance.Status.ObservedGeneration = 3
	instance.Status.Namespace = "cetirun-cetiadmin"
	instance.Annotations = map[string]string{
		api.AnnotationOwnerTenantID:                  managementTenant,
		api.AnnotationOwnerObjectID:                  managementOwner,
		kube.AnnotationCetiAdminInstallationUID:      "cetiadmin-record-uid",
		"cetirun.microsoft.com/cetiadmin-source-uid": "cetiadmin-source-uid",
	}
	return instance
}

func cetiAdminRequest(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.SetPathValue("name", "cetiadmin")
	return withAgentIdentityClaims(r, "alice", managementTenant, managementOwner)
}

func adminCetiAdminRequest(method, body string) *http.Request {
	r := httptest.NewRequest(method, "/api/v1/admin/instances/cetiadmin", strings.NewReader(body))
	r.SetPathValue("name", "cetiadmin")
	return r.WithContext(auth.NewContext(r.Context(), auth.Claims{Alias: "administrator", Subject: "admin-subject", IsAdmin: true}))
}

func TestCetiAdminIsMarkedAndKeepsTheVersionThatTheOperatorSets(t *testing.T) {
	ordinary := sampleInstance()
	ordinary.Name, ordinary.UID = "alice-research", "ordinary-agent-uid"
	ordinary.Spec.Helm.Version = "0.2.1"
	ordinary.Annotations = map[string]string{api.AnnotationOwnerTenantID: managementTenant, api.AnnotationOwnerObjectID: managementOwner}
	h := &InstanceHandler{Kube: kube.NewClient(newFakeClient(cetiAdminInstance(), ordinary, operatorDeployment("0.2.2")))}

	w := httptest.NewRecorder()
	h.ListInstances(w, cetiAdminRequest(http.MethodGet, "/api/v1/instances", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var raw []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil || len(raw) != 2 {
		t.Fatalf("list: %v %s", err, w.Body.String())
	}
	for _, instance := range raw {
		name := instance["instanceName"]
		cetiAdmin, marked := instance["cetiAdmin"]
		switch name {
		case "cetiadmin":
			// The operator upgrades CetiAdmin with the platform, so the Portal offers no upgrade.
			if cetiAdmin != true || instance["upgradeAvailable"] != false {
				t.Errorf("CetiAdmin: cetiAdmin=%v upgradeAvailable=%v", cetiAdmin, instance["upgradeAvailable"])
			}
		case "alice-research":
			if marked || instance["upgradeAvailable"] != true {
				t.Errorf("ordinary Agent: cetiAdmin present=%v upgradeAvailable=%v", marked, instance["upgradeAvailable"])
			}
		default:
			t.Errorf("unexpected Agent %v", name)
		}
	}
}

func TestCetiAdminDoesNotCountTowardTheLimitOfItsOwner(t *testing.T) {
	create := func(h *InstanceHandler, name string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.CreateInstance(w, cetiAdminRequest(http.MethodPost, "/api/v1/instance", `{"instanceName":"`+name+`","agent":{},"team":"aks"}`))
		return w
	}
	refusedByTheLimit := func(w *httptest.ResponseRecorder) bool {
		return w.Code == http.StatusForbidden && strings.Contains(w.Body.String(), "limit_exceeded")
	}
	config := gatewayTestConfig()
	config.MaxInstancesPerUser = 1

	h := &InstanceHandler{Kube: kube.NewClient(newGatewayFakeClient(cetiAdminInstance())), Config: config}
	if w := create(h, "research"); w.Code != http.StatusCreated {
		t.Fatalf("first Agent of the CetiAdmin owner: %d %s", w.Code, w.Body.String())
	}
	// The Agent that the owner created counts.
	if w := create(h, "second"); !refusedByTheLimit(w) {
		t.Fatalf("second Agent: %d %s, want limit_exceeded", w.Code, w.Body.String())
	}

	// Without the operator's marker, the same Agent is one of the owner's own Agents.
	unmarked := cetiAdminInstance()
	delete(unmarked.Annotations, kube.AnnotationCetiAdminInstallationUID)
	h = &InstanceHandler{Kube: kube.NewClient(newGatewayFakeClient(unmarked)), Config: config}
	if w := create(h, "research"); !refusedByTheLimit(w) {
		t.Fatalf("Agent without the marker: %d %s, want limit_exceeded", w.Code, w.Body.String())
	}
}

func TestPortalLeavesTheConfigurationOfCetiAdminToTheDeployment(t *testing.T) {
	for _, test := range []struct {
		name string
		call func(*InstanceHandler, *AdminInstanceHandler, string) *httptest.ResponseRecorder
	}{
		{"owner changes the model", func(h *InstanceHandler, _ *AdminInstanceHandler, version string) *httptest.ResponseRecorder {
			w := httptest.NewRecorder()
			h.UpdateInstanceByName(w, cetiAdminRequest(http.MethodPatch, "/api/v1/instances/cetiadmin", strings.TrimSuffix(version, "}")+`,"model":"gpt-6"}`))
			return w
		}},
		{"owner changes the display name", func(h *InstanceHandler, _ *AdminInstanceHandler, version string) *httptest.ResponseRecorder {
			w := httptest.NewRecorder()
			h.UpdateInstanceByName(w, cetiAdminRequest(http.MethodPatch, "/api/v1/instances/cetiadmin", strings.TrimSuffix(version, "}")+`,"displayName":"Operations"}`))
			return w
		}},
		{"owner deletes it", func(h *InstanceHandler, _ *AdminInstanceHandler, version string) *httptest.ResponseRecorder {
			w := httptest.NewRecorder()
			h.DeleteInstanceByName(w, cetiAdminRequest(http.MethodDelete, "/api/v1/instances/cetiadmin", version))
			return w
		}},
		{"owner upgrades it", func(h *InstanceHandler, _ *AdminInstanceHandler, version string) *httptest.ResponseRecorder {
			w := httptest.NewRecorder()
			h.UpgradeInstanceByName(w, cetiAdminRequest(http.MethodPost, "/api/v1/instances/cetiadmin/upgrade", version))
			return w
		}},
		{"administrator changes the model", func(_ *InstanceHandler, admin *AdminInstanceHandler, version string) *httptest.ResponseRecorder {
			w := httptest.NewRecorder()
			admin.UpdateInstance(w, adminCetiAdminRequest(http.MethodPatch, strings.TrimSuffix(version, "}")+`,"model":"gpt-6"}`))
			return w
		}},
		// CetiAdmin's status changes often, so a draft is often older than the Agent: the answer still names the cause.
		{"administrator saves a stale draft", func(_ *InstanceHandler, admin *AdminInstanceHandler, version string) *httptest.ResponseRecorder {
			w := httptest.NewRecorder()
			stale := strings.Replace(version, `"resourceVersion":"`, `"resourceVersion":"0`, 1)
			admin.UpdateInstance(w, adminCetiAdminRequest(http.MethodPatch, strings.TrimSuffix(stale, "}")+`,"displayName":"Operations"}`))
			return w
		}},
		{"owner saves a stale draft", func(h *InstanceHandler, _ *AdminInstanceHandler, version string) *httptest.ResponseRecorder {
			w := httptest.NewRecorder()
			stale := strings.Replace(version, `"resourceVersion":"`, `"resourceVersion":"0`, 1)
			h.UpdateInstanceByName(w, cetiAdminRequest(http.MethodPatch, "/api/v1/instances/cetiadmin", strings.TrimSuffix(stale, "}")+`,"displayName":"Operations"}`))
			return w
		}},
		{"administrator deletes it", func(_ *InstanceHandler, admin *AdminInstanceHandler, version string) *httptest.ResponseRecorder {
			w := httptest.NewRecorder()
			admin.DeleteInstance(w, adminCetiAdminRequest(http.MethodDelete, version))
			return w
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := newGatewayFakeClient(cetiAdminInstance(), operatorDeployment("0.2.2"))
			h := &InstanceHandler{Kube: kube.NewClient(c), Config: gatewayTestConfig()}
			admin := &AdminInstanceHandler{Kube: h.Kube, Config: h.Config}
			before := &api.Agent{}
			if err := c.Get(context.Background(), client.ObjectKey{Name: "cetiadmin"}, before); err != nil {
				t.Fatal(err)
			}
			w := test.call(h, admin, fmt.Sprintf(`{"uid":%q,"resourceVersion":%q}`, before.UID, before.ResourceVersion))
			var response ErrorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != http.StatusForbidden ||
				response.Error.Code != "cetiadmin_managed" || !strings.Contains(response.Error.Message, "cetiadmin values of the deployment") {
				t.Fatalf("response %d %s, want 403 cetiadmin_managed", w.Code, w.Body.String())
			}
			after := &api.Agent{}
			if err := c.Get(context.Background(), client.ObjectKey{Name: "cetiadmin"}, after); err != nil {
				t.Fatalf("CetiAdmin is gone: %v", err)
			}
			if after.ResourceVersion != before.ResourceVersion || after.Spec.Model != "auto" || after.Spec.Helm.Version != "0.2.1" ||
				after.Annotations[kube.AnnotationAgentDisplayName] != "" {
				t.Fatalf("CetiAdmin changed: resourceVersion %s -> %s, spec %+v", before.ResourceVersion, after.ResourceVersion, after.Spec)
			}
		})
	}
}

func TestTheAdministratorDetailMarksOnlyCetiAdmin(t *testing.T) {
	ordinary := sampleInstance()
	ordinary.Name, ordinary.UID = "alice-research", "ordinary-agent-uid"
	admin := &AdminInstanceHandler{Kube: kube.NewClient(newFakeClient(cetiAdminInstance(), ordinary))}
	for name, want := range map[string]bool{"cetiadmin": true, "alice-research": false} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/admin/instances/"+name, nil)
		r.SetPathValue("name", name)
		r = r.WithContext(auth.NewContext(r.Context(), auth.Claims{Alias: "administrator", Subject: "admin-subject", IsAdmin: true}))
		w := httptest.NewRecorder()
		admin.GetInstance(w, r)
		var detail map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
		cetiAdmin, marked := detail["cetiAdmin"]
		if want && cetiAdmin != true || !want && marked {
			t.Errorf("%s: cetiAdmin=%v present=%v, want %v", name, cetiAdmin, marked, want)
		}
	}
}

func TestTheOwnerCanStillRestartCetiAdmin(t *testing.T) {
	c := newGatewayFakeClient(cetiAdminInstance())
	h := &InstanceHandler{Kube: kube.NewClient(c), Config: gatewayTestConfig()}
	current, err := h.Kube.GetInstanceByName(context.Background(), "cetiadmin")
	if err != nil {
		t.Fatal(err)
	}
	r := cetiAdminRequest(http.MethodPost, "/api/v1/instances/cetiadmin/restart", fmt.Sprintf(`{"uid":%q,"resourceVersion":%q}`, current.UID, current.ResourceVersion))
	r.Header.Set("Idempotency-Key", "request-00000001")
	w := httptest.NewRecorder()
	h.RestartInstanceByName(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("restart: %d %s", w.Code, w.Body.String())
	}
	var response InstanceResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || !response.CetiAdmin || response.RestartRequestID != "request-00000001" {
		t.Fatalf("restart response: %v %s", err, w.Body.String())
	}
}
