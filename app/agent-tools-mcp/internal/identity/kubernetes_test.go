// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestAuthenticateValidatesCurrentWorkloadBinding(t *testing.T) {
	for _, test := range []struct {
		name    string
		mutate  func(map[string]any, map[string]any, map[string]any, map[string]any)
		allowed bool
	}{
		{name: "pod bound without Entra identity", allowed: true},
		{name: "wrong audience", mutate: func(review, pod, account, agent map[string]any) { review["audiences"] = []string{"other"} }},
		{name: "no audiences", mutate: func(review, pod, account, agent map[string]any) { delete(review, "audiences") }},
		{name: "recreated pod", mutate: func(review, pod, account, agent map[string]any) { pod["metadata"].(map[string]any)["uid"] = "new-pod" }},
		{name: "recreated account", mutate: func(review, pod, account, agent map[string]any) {
			account["metadata"].(map[string]any)["uid"] = "new-account"
		}},
		{name: "deleting pod", mutate: func(review, pod, account, agent map[string]any) {
			pod["metadata"].(map[string]any)["deletionTimestamp"] = "2026-10-10T00:00:00Z"
		}},
		{name: "suspended agent", mutate: func(review, pod, account, agent map[string]any) { agent["spec"] = map[string]any{"suspended": true} }},
		{name: "deleting agent", mutate: func(review, pod, account, agent map[string]any) {
			agent["metadata"].(map[string]any)["deletionTimestamp"] = "2026-10-10T00:00:00Z"
		}},
		{name: "other instance account", mutate: func(review, pod, account, agent map[string]any) { agent["metadata"].(map[string]any)["name"] = "other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			review := map[string]any{"authenticated": true, "audiences": []string{"agent-tools-mcp"}, "user": map[string]any{
				"username": "system:serviceaccount:cetirun-agent-a:openclaw-agent-a-cetirun", "uid": "account-uid",
				"extra": map[string]any{"authentication.kubernetes.io/pod-name": []string{"agent-pod"}, "authentication.kubernetes.io/pod-uid": []string{"pod-uid"}},
			}}
			pod := map[string]any{"metadata": map[string]any{"uid": "pod-uid", "namespace": "cetirun-agent-a"}, "spec": map[string]any{"serviceAccountName": "openclaw-agent-a-cetirun"}}
			account := map[string]any{"metadata": map[string]any{"uid": "account-uid"}}
			agent := map[string]any{"metadata": map[string]any{"uid": "instance-uid", "name": "agent-a"}, "status": map[string]any{"phase": "Ready", "namespace": "cetirun-agent-a"}}
			if test.mutate != nil {
				test.mutate(review, pod, account, agent)
			}
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				var result any
				switch request.URL.Path {
				case "/apis/authentication.k8s.io/v1/tokenreviews":
					var input tokenReview
					if err := json.NewDecoder(request.Body).Decode(&input); err != nil || input.Spec.Token != "caller-proof" || !audienceMatches(input.Spec.Audiences, "agent-tools-mcp") {
						t.Error("invalid TokenReview request")
					}
					result = map[string]any{"status": review}
				case "/api/v1/namespaces/cetirun-agent-a/pods/agent-pod":
					result = pod
				case "/api/v1/namespaces/cetirun-agent-a/serviceaccounts/openclaw-agent-a-cetirun":
					result = account
				case "/apis/cetirun.microsoft.com/v1alpha1/agents":
					result = map[string]any{"items": []any{agent}}
				default:
					t.Errorf("unexpected path %s", request.URL.Path)
					http.NotFound(writer, request)
					return
				}
				_ = json.NewEncoder(writer).Encode(result)
			}))
			defer server.Close()
			tokenPath := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(tokenPath, []byte("server-proof"), 0600); err != nil {
				t.Fatal(err)
			}
			client := &Kubernetes{baseURL: server.URL, tokenFile: tokenPath, client: server.Client(), audience: "agent-tools-mcp"}
			caller, err := client.Authenticate(context.Background(), "caller-proof")
			if test.allowed && (err != nil || caller.InstanceUID != "instance-uid") {
				t.Fatalf("valid workload rejected: %v", err)
			}
			if !test.allowed && err == nil {
				t.Fatal("invalid workload accepted")
			}
		})
	}
}
