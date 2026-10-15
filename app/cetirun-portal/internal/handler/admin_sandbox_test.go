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
	"github.com/Azure/CetiRun/app/cetirun-portal/internal/kube"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func adminSandboxFixture() api.SandboxSpec {
	resources := func() api.SandboxContainerResources {
		return api.SandboxContainerResources{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi"), corev1.ResourceEphemeralStorage: resource.MustParse("128Mi")},
			Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi"), corev1.ResourceEphemeralStorage: resource.MustParse("1Gi")},
		}
	}
	return api.SandboxSpec{Type: api.SandboxKata, Resources: api.SandboxResources{Agent: resources(), Helpers: resources()}, Storage: api.SandboxStorage{
		Home:    api.SandboxHomeStorage{StorageClassName: "example-csi", Size: resource.MustParse("10Gi"), AccessMode: corev1.ReadWriteOncePod},
		Scratch: api.SandboxScratchStorage{SizeLimit: resource.MustParse("1Gi")},
	}}
}

func sandboxRequest(t *testing.T, method, name string, body any) *http.Request {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, "/api/v1/admin/sandboxes/"+name, bytes.NewReader(raw))
	r.SetPathValue("name", name)
	return r
}

func TestAdminSandboxCRUD(t *testing.T) {
	h := &AdminSandboxHandler{Kube: kube.NewClient(newFakeClient())}
	req := SandboxUpsertRequest{Name: "example", SandboxSpec: adminSandboxFixture()}
	w := httptest.NewRecorder()
	h.CreateSandbox(w, sandboxRequest(t, http.MethodPost, "", req))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var dto SandboxDTO
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.Name != req.Name || dto.UID == "" || dto.ResourceVersion == "" || dto.Ready != "Unknown" || dto.Storage.Home.Size.String() != "10Gi" {
		t.Fatalf("invalid DTO: %+v", dto)
	}
	w = httptest.NewRecorder()
	h.ListSandboxes(w, sandboxRequest(t, http.MethodGet, "", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"name":"example"`) {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	req.UID, req.ResourceVersion = dto.UID, dto.ResourceVersion
	req.Storage.Home.Size = resource.MustParse("20Gi")
	w = httptest.NewRecorder()
	h.UpdateSandbox(w, sandboxRequest(t, http.MethodPut, req.Name, req))
	if w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	// Replaying the previous version cannot overwrite the updated configuration.
	w = httptest.NewRecorder()
	h.UpdateSandbox(w, sandboxRequest(t, http.MethodPut, req.Name, req))
	if w.Code != http.StatusConflict {
		t.Fatalf("stale update accepted: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.GetSandbox(w, sandboxRequest(t, http.MethodGet, req.Name, nil))
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.Storage.Home.Size.String() != "20Gi" {
		t.Fatal("updated storage was not persisted")
	}
	w = httptest.NewRecorder()
	h.DeleteSandbox(w, sandboxRequest(t, http.MethodDelete, req.Name, map[string]string{"uid": dto.UID, "resourceVersion": dto.ResourceVersion}))
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
}

func TestAdminSandboxRejectsInvalidConfiguration(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*api.SandboxSpec)
	}{
		{"runtime", func(s *api.SandboxSpec) { s.Type = "unknown" }},
		{"missing limit", func(s *api.SandboxSpec) { delete(s.Resources.Helpers.Limits, corev1.ResourceCPU) }},
		{"unknown resource", func(s *api.SandboxSpec) { s.Resources.Agent.Requests["example.com/gpu"] = resource.MustParse("1") }},
		{"request over limit", func(s *api.SandboxSpec) { s.Resources.Agent.Requests[corev1.ResourceCPU] = resource.MustParse("2") }},
		{"nonpositive resource", func(s *api.SandboxSpec) { s.Resources.Agent.Requests[corev1.ResourceCPU] = resource.MustParse("0") }},
		{"StorageClass", func(s *api.SandboxSpec) { s.Storage.Home.StorageClassName = "" }},
		{"HOME size", func(s *api.SandboxSpec) { s.Storage.Home.Size = resource.MustParse("0") }},
		{"access mode", func(s *api.SandboxSpec) { s.Storage.Home.AccessMode = corev1.ReadWriteMany }},
		{"scratch size", func(s *api.SandboxSpec) { s.Storage.Scratch.SizeLimit = resource.MustParse("-1Gi") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := &AdminSandboxHandler{Kube: kube.NewClient(newFakeClient())}
			req := SandboxUpsertRequest{Name: "example", SandboxSpec: adminSandboxFixture()}
			test.change(&req.SandboxSpec)
			w := httptest.NewRecorder()
			h.CreateSandbox(w, sandboxRequest(t, http.MethodPost, "", req))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("invalid configuration accepted: %d %s", w.Code, w.Body.String())
			}
			boxes, err := h.Kube.ListSandboxes(context.Background())
			if err != nil || len(boxes) != 0 {
				t.Fatalf("rejected request wrote a Sandbox: %v", err)
			}
		})
	}
}

func TestConfigurationAdmissionErrorReachesAdmin(t *testing.T) {
	err := apierrors.NewInvalid(api.GroupVersion.WithKind("Sandbox").GroupKind(), "example", field.ErrorList{field.Invalid(field.NewPath("spec", "storage", "home", "storageClassName"), "example", "StorageClass is unavailable")})
	storage := newFakeClient()
	wrapped := interceptor.NewClient(storage.(client.WithWatch), interceptor.Funcs{Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error { return err }})
	h := &AdminSandboxHandler{Kube: kube.NewClient(wrapped)}
	w := httptest.NewRecorder()
	h.CreateSandbox(w, sandboxRequest(t, http.MethodPost, "", SandboxUpsertRequest{Name: "example", SandboxSpec: adminSandboxFixture()}))
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "StorageClass is unavailable") {
		t.Fatalf("validation detail hidden: %d %s", w.Code, w.Body.String())
	}
}

func TestSandboxReadinessRequiresCurrentObservation(t *testing.T) {
	box := &api.Sandbox{ObjectMeta: metav1.ObjectMeta{Name: "example", Generation: 2}, Status: api.SandboxConfigurationStatus{Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, ObservedGeneration: 1}}}}
	if toSandboxDTO(box).Ready != "Unknown" {
		t.Fatal("stale Ready was advertised")
	}
	box.Status.Conditions[0].ObservedGeneration = 2
	if toSandboxDTO(box).Ready != "True" {
		t.Fatal("current Ready was lost")
	}
}
