// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func teamVersionRequest(t *testing.T, method, name string, body any) *http.Request {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, "/api/v1/admin/teams/"+name, bytes.NewReader(raw))
	r.SetPathValue("name", name)
	return r
}

func TestAdminTeamGenerationPreconditions(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		for _, scenario := range []string{"status-refresh", "legacy-current", "legacy-stale", "changed-generation", "replacement", "missing-uid", "missing-rv", "zero", "negative", "null", "fraction", "string", "overflow", "header-only", "header-matching-body", "header-mismatching-body", "header-duplicate", "header-zero", "header-empty", "header-fraction", "header-overflow"} {
			t.Run(method+"/"+scenario, func(t *testing.T) {
				h := newAdminTeamHandler(&api.Team{ObjectMeta: metav1.ObjectMeta{Name: "example", Generation: 3}})
				current, err := h.Kube.GetTeam(context.Background(), "example")
				if err != nil {
					t.Fatal(err)
				}
				body := map[string]any{"uid": string(current.UID), "resourceVersion": "older-status-version", "generation": current.Generation}
				var headers []string
				want := http.StatusOK
				if method == http.MethodDelete {
					want = http.StatusNoContent
				}
				switch scenario {
				case "legacy-current":
					delete(body, "generation")
					body["resourceVersion"] = current.ResourceVersion
				case "legacy-stale":
					delete(body, "generation")
					want = http.StatusConflict
				case "changed-generation":
					body["generation"] = current.Generation - 1
					want = http.StatusConflict
				case "replacement":
					body["uid"] = "previous-uid"
					want = http.StatusConflict
				case "missing-uid":
					delete(body, "uid")
					want = http.StatusPreconditionRequired
				case "missing-rv":
					delete(body, "resourceVersion")
					want = http.StatusPreconditionRequired
				case "zero":
					body["generation"] = 0
					want = http.StatusBadRequest
				case "negative":
					body["generation"] = -1
					want = http.StatusBadRequest
				case "null":
					body["generation"] = nil
					want = http.StatusBadRequest
				case "fraction":
					body["generation"] = 1.5
					want = http.StatusBadRequest
				case "string":
					body["generation"] = "3"
					want = http.StatusBadRequest
				case "overflow":
					body["generation"] = json.Number("9223372036854775808")
					want = http.StatusBadRequest
				case "header-only":
					delete(body, "generation")
					headers = []string{"3"}
				case "header-matching-body":
					headers = []string{"3"}
				case "header-mismatching-body":
					headers = []string{"2"}
					want = http.StatusBadRequest
				case "header-duplicate":
					headers = []string{"3", "3"}
					want = http.StatusBadRequest
				case "header-zero":
					headers = []string{"0"}
					want = http.StatusBadRequest
				case "header-empty":
					headers = []string{""}
					want = http.StatusBadRequest
				case "header-fraction":
					headers = []string{"1.5"}
					want = http.StatusBadRequest
				case "header-overflow":
					headers = []string{"9223372036854775808"}
					want = http.StatusBadRequest
				}
				if method == http.MethodPut {
					body["agentPolicyRef"], body["sandboxRefs"], body["defaultSandboxRef"] = "standard-policy", []string{"standard"}, "standard"
					body["role"] = "Reviewer"
				}
				w := httptest.NewRecorder()
				request := teamVersionRequest(t, method, current.Name, body)
				for _, value := range headers {
					request.Header.Add(teamGenerationHeader, value)
				}
				if method == http.MethodPut {
					h.UpdateTeam(w, request)
				} else {
					h.DeleteTeam(w, request)
				}
				if w.Code != want {
					t.Fatalf("got %d, want %d: %s", w.Code, want, w.Body.String())
				}
			})
		}
	}
}
