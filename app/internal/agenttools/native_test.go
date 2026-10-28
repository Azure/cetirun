// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package agenttools_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Azure/CetiRun/app/internal/agenttools"
	"github.com/Azure/CetiRun/app/internal/agenttools/testfixture"
)

func TestNativeVerifierChecksCurrentPurposeAndAuthority(t *testing.T) {
	for _, scenario := range []string{"valid", "trusted-init", "denied", "missing-authority", "recreated-sa", "recreated-pod", "recreated-agent",
		"foreign-namespace-owner", "foreign-sa-namespace", "suspended", "deleting-agent", "deleting-pod",
		"held", "deleting-namespace", "wrong-audience", "helper-mount"} {
		t.Run(scenario, func(t *testing.T) {
			f := testfixture.NewNative(t)
			pod := f.Objects["/api/v1/namespaces/agents/pods/"+f.Identity.PodName].(map[string]any)
			spec := pod["spec"].(map[string]any)
			switch scenario {
			case "trusted-init":
				spec["initContainers"] = []any{
					map[string]any{"name": "managed-egress", "restartPolicy": "Always"},
					map[string]any{"name": "managed-capture"},
				}
			case "denied":
				f.Allowed = false
			case "missing-authority":
				delete(f.Objects, "/apis/admissionregistration.k8s.io/v1/validatingwebhookconfigurations/"+agenttools.NativePodWebhookConfiguration)
			case "recreated-sa":
				f.Objects["/api/v1/namespaces/agents/serviceaccounts/default"].(map[string]any)["metadata"].(map[string]any)["uid"] = "replacement-sa"
			case "recreated-pod":
				pod["metadata"].(map[string]any)["uid"] = "replacement-pod"
			case "recreated-agent":
				f.Objects["/apis/cetirun.microsoft.com/v1alpha1/agents/agent"].(map[string]any)["metadata"].(map[string]any)["uid"] = "replacement-agent"
			case "foreign-namespace-owner":
				f.Objects["/api/v1/namespaces/agents"].(map[string]any)["metadata"].(map[string]any)["ownerReferences"] = []any{}
			case "foreign-sa-namespace":
				f.Objects["/api/v1/namespaces/agents/serviceaccounts/default"].(map[string]any)["metadata"].(map[string]any)["namespace"] = "another-agent"
			case "deleting-agent":
				f.Objects["/apis/cetirun.microsoft.com/v1alpha1/agents/agent"].(map[string]any)["metadata"].(map[string]any)["deletionTimestamp"] = "2026-10-28T00:00:00Z"
			case "deleting-pod":
				pod["metadata"].(map[string]any)["deletionTimestamp"] = "2026-10-28T00:00:00Z"
			case "held":
				f.Objects["/apis/cetirun.microsoft.com/v1alpha1/agents/agent"].(map[string]any)["metadata"].(map[string]any)["finalizers"] = []string{"cetirun.microsoft.com/sandbox-hold"}
			case "suspended":
				f.Objects["/apis/cetirun.microsoft.com/v1alpha1/agents/agent"].(map[string]any)["spec"].(map[string]any)["suspended"] = true
			case "deleting-namespace":
				f.Objects["/api/v1/namespaces/agents"].(map[string]any)["metadata"].(map[string]any)["deletionTimestamp"] = "2026-10-28T00:00:00Z"
			case "wrong-audience":
				spec["volumes"].([]any)[0].(map[string]any)["projected"].(map[string]any)["sources"].([]any)[0].(map[string]any)["serviceAccountToken"].(map[string]any)["audience"] = "api://AzureADTokenExchange"
			case "helper-mount":
				spec["initContainers"] = spec["containers"]
			}
			verifier := agenttools.NativeVerifier{Get: f.Get, OperatorNamespace: "operator",
				Audience: "cetirun-connections-mcp", Transport: f.Transport}
			err := verifier.Verify(context.Background(), f.Identity)
			if scenario == "valid" || scenario == "trusted-init" {
				if err != nil || f.Calls != 1 {
					t.Fatalf("current native proof rejected: %v, calls=%d", err, f.Calls)
				}
			} else if err == nil {
				t.Fatal("invalid native proof was accepted")
			}
			if scenario == "missing-authority" && !errors.Is(err, agenttools.ErrNativeUnavailable) {
				t.Fatal("authority failure was not classified unavailable")
			}
		})
	}
}

type nativeRoundTrip func(*http.Request) (*http.Response, error)

func (f nativeRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNativeVerifierRejectsChangesDuringAuthorityReadback(t *testing.T) {
	f := testfixture.NewNative(t)
	verifier := agenttools.NativeVerifier{Get: f.Get, OperatorNamespace: "operator", Audience: "cetirun-connections-mcp"}
	verifier.Transport = nativeRoundTrip(func(r *http.Request) (*http.Response, error) {
		response, err := f.Transport.RoundTrip(r)
		f.Objects["/api/v1/namespaces/agents/serviceaccounts/default"].(map[string]any)["metadata"].(map[string]any)["uid"] = "replacement-sa"
		return response, err
	})
	if err := verifier.Verify(context.Background(), f.Identity); !errors.Is(err, agenttools.ErrNativeProofInvalid) {
		t.Fatalf("concurrent SA replacement = %v", err)
	}
}
