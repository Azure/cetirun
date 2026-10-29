// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Azure/CetiRun/app/internal/agenttools/testfixture"
)

type nativeIdentityTransport func(*http.Request) (*http.Response, error)

func (f nativeIdentityTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNativeAuthenticationRequiresCurrentCommonAuthority(t *testing.T) {
	for _, scenario := range []string{"current", "long-pod", "dotted-pod", "invalid-pod", "oversized-pod",
		"dotted-sa", "long-sa", "max-sa", "empty-sa", "upper-sa", "empty-label-sa",
		"leading-hyphen-sa", "trailing-dot-sa", "slash-sa", "oversized-sa",
		"denied", "recreated-sa", "missing-projection", "entra-audience", "terminated", "not-ready"} {
		t.Run(scenario, func(t *testing.T) {
			f := testfixture.NewNative(t)
			agent := f.Objects["/apis/cetirun.microsoft.com/v1alpha1/agents/agent"].(map[string]any)
			status := agent["status"].(map[string]any)
			status["policy"] = map[string]string{"phase": "Applied"}
			revision := "sha256:" + strings.Repeat("1", 64)
			status["agentIdentity"] = map[string]string{"credentialPolicyRevision": revision}
			audience := "cetirun-connections-mcp"
			switch scenario {
			case "long-pod":
				f.SetPodName(strings.Repeat("a", 63) + "-0")
			case "dotted-pod":
				f.SetPodName("approved.workload-0")
			case "invalid-pod":
				f.SetPodName("a..b-0")
			case "oversized-pod":
				f.SetPodName(strings.Repeat("a", 254))
			case "dotted-sa":
				f.SetServiceAccountName("approved.identity")
			case "long-sa":
				f.SetServiceAccountName(strings.Repeat("a", 64))
			case "max-sa":
				f.SetServiceAccountName(strings.Repeat("a", 253))
			case "empty-sa":
				f.SetServiceAccountName("")
			case "upper-sa":
				f.SetServiceAccountName("Uppercase")
			case "empty-label-sa":
				f.SetServiceAccountName("a..b")
			case "leading-hyphen-sa":
				f.SetServiceAccountName("-a")
			case "trailing-dot-sa":
				f.SetServiceAccountName("a.")
			case "slash-sa":
				f.SetServiceAccountName("a/b")
			case "oversized-sa":
				f.SetServiceAccountName(strings.Repeat("a", 254))
			case "denied":
				f.Allowed = false
			case "recreated-sa":
				f.Objects["/api/v1/namespaces/agents/serviceaccounts/default"].(map[string]any)["metadata"].(map[string]any)["uid"] = "replacement"
			case "missing-projection":
				f.Objects["/api/v1/namespaces/agents/pods/"+f.Identity.PodName].(map[string]any)["spec"].(map[string]any)["volumes"] = []any{}
			case "entra-audience":
				audience = "api://AzureADTokenExchange"
			case "terminated":
				f.Objects["/api/v1/namespaces/agents/pods/"+f.Identity.PodName].(map[string]any)["status"].(map[string]any)["phase"] = "Failed"
			case "not-ready":
				f.Objects["/api/v1/namespaces/agents/pods/"+f.Identity.PodName].(map[string]any)["status"].(map[string]any)["conditions"] = []any{}
			}
			token := filepath.Join(t.TempDir(), "synthetic-server-proof")
			if err := os.WriteFile(token, []byte("synthetic-server-proof"), 0600); err != nil {
				t.Fatal(err)
			}
			kubernetes := &Kubernetes{baseURL: "https://kubernetes.example.com", tokenFile: token,
				audience: "cetirun-connections-mcp", operatorNamespace: "operator", nativeAdmissionTransport: f.Transport}
			kubernetes.client = &http.Client{Transport: nativeIdentityTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/apis/authentication.k8s.io/v1/tokenreviews" {
					data, _ := json.Marshal(map[string]any{"status": map[string]any{
						"authenticated": true, "audiences": []string{audience},
						"user": map[string]any{"username": "system:serviceaccount:agents:" + f.Identity.ServiceAccountName, "uid": f.Identity.ServiceAccountUID,
							"extra": map[string][]string{"authentication.kubernetes.io/pod-name": {f.Identity.PodName},
								"authentication.kubernetes.io/pod-uid": {f.Identity.PodUID}}},
					}})
					return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), Header: http.Header{}}, nil
				}
				if response, ok := f.Response(r.URL.Path); ok {
					return response, nil
				}
				t.Fatalf("unexpected native source request %s", r.URL.Path)
				return nil, nil
			})}
			caller, err := kubernetes.Authenticate(context.Background(), "synthetic-native-proof")
			if scenario == "current" || scenario == "dotted-sa" || scenario == "long-sa" || scenario == "max-sa" ||
				scenario == "long-pod" || scenario == "dotted-pod" {
				if err != nil || !caller.PolicyManaged || caller.InstanceUID != f.Identity.AgentUID ||
					caller.CredentialPolicyRevision != revision || f.Calls != 1 {
					t.Fatalf("native proof was not verified: %#v %v", caller, err)
				}
			} else if err == nil || caller != (Caller{}) {
				t.Fatalf("unqualified native proof was accepted: %#v %v", caller, err)
			}
		})
	}
}

func TestServiceAccountIdentityKeepsNamespaceLabelsStrict(t *testing.T) {
	for _, namespace := range []string{"agents", "agents.example", strings.Repeat("a", 64), "Uppercase", ""} {
		for _, name := range []string{"approved.identity", strings.Repeat("a", 64)} {
			actualNamespace, actualName, valid := serviceAccountIdentity("system:serviceaccount:" + namespace + ":" + name)
			if valid != (namespace == "agents") {
				t.Fatalf("namespace %q with valid ServiceAccount %q: accepted=%t", namespace, name, valid)
			}
			if valid && (actualNamespace != namespace || actualName != name) {
				t.Fatal("ServiceAccount identity was truncated or rewritten")
			}
		}
	}
}
