// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/config"
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
	corev1 "k8s.io/api/core/v1"
)

func TestCustomNativeUpdatesHaveNoSideEffects(t *testing.T) {
	for _, byName := range []bool{false, true} {
		for _, body := range []string{`{"gatewayToken":"sk-dummy"}`, `{"tailscaleAuthKey":"sk-dummy"}`, `{"teamsEnabled":true}`, `{"teamsEnabled":false}`} {
			body = strings.Replace(body, "{", `{"uid":"agent-alice","resourceVersion":"1",`, 1)
			instance := sampleInstance()
			instance.Spec.Agent.Type = api.AgentTypeCustom
			fc := newFakeClient(instance)
			h := &InstanceHandler{Kube: kube.NewClient(fc), Config: config.Config{TailscaleEnabled: true}}
			request := withClaims(httptest.NewRequest(http.MethodPatch, "/api/v1/instance", bytes.NewBufferString(body)), "alice", "Example", "alice@example.com")
			request.SetPathValue("name", instance.Name)
			response := httptest.NewRecorder()
			if byName {
				h.UpdateInstanceByName(response, request)
			} else {
				h.UpdateInstance(response, request)
			}
			if response.Code != http.StatusBadRequest || !bytes.Contains(response.Body.Bytes(), []byte("unsupported_custom_feature")) {
				t.Fatalf("byName=%v body=%s status=%d", byName, body, response.Code)
			}
			namespaces, secrets := &corev1.NamespaceList{}, &corev1.SecretList{}
			if err := fc.List(context.Background(), namespaces); err != nil {
				t.Fatal(err)
			}
			if err := fc.List(context.Background(), secrets); err != nil {
				t.Fatal(err)
			}
			if len(namespaces.Items) != 0 || len(secrets.Items) != 0 {
				t.Fatal("rejected Custom update created native resources")
			}
		}
	}
}

func TestCustomOrdinaryUpdateRemainsAvailable(t *testing.T) {
	instance := sampleInstance()
	instance.Spec.Agent.Type = api.AgentTypeCustom
	fc := newFakeClient(instance)
	h := &InstanceHandler{Kube: kube.NewClient(fc), Config: config.Config{}}
	request := withClaims(httptest.NewRequest(http.MethodPatch, "/api/v1/instance", bytes.NewBufferString(`{"uid":"agent-alice","resourceVersion":"1","displayName":"Updated"}`)), "alice", "Example", "alice@example.com")
	response := httptest.NewRecorder()
	h.UpdateInstance(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("ordinary Custom update failed: %d %s", response.Code, response.Body.String())
	}
}
