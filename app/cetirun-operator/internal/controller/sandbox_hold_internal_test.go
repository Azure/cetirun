// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	istiov1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"

	cetirunv1alpha1 "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/aigateway"
)

func TestDeleteSandboxBuiltinRevokesGatewayAfterAgentIdentityFailure(t *testing.T) {
	agent := aiGatewayTestAgent(testAIGatewayTeam)
	now := metav1.Now()
	agent.DeletionTimestamp = &now
	agent.Spec.AgentIdentity = &cetirunv1alpha1.AgentIdentitySpec{Enabled: true}
	agent.Status.AgentIdentity = &cetirunv1alpha1.AgentIdentityStatus{
		Phase:                    cetirunv1alpha1.AgentIdentityPhaseReady,
		ClientID:                 testAgentIdentityClientID,
		ObjectID:                 testAgentIdentityObjectID,
		DisplayName:              "cetirun-agent-" + string(agent.UID),
		ObservedGeneration:       agent.Generation,
		CredentialPolicyRevision: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	agent.Status.AIGateway = &cetirunv1alpha1.AIGatewayStatus{
		Phase:    cetirunv1alpha1.AIGatewayPhaseReady,
		KeyAlias: aiGatewayKeyAlias(agent),
		Copilot:  &cetirunv1alpha1.AIGatewayCopilotStatus{Registered: []string{"recorded-route"}},
	}

	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodDelete || !strings.HasSuffix(request.URL.Path, "/credential-policy") {
			t.Fatalf("unexpected Broker request: %s %s", request.Method, request.URL.Path)
		}
		http.Error(w, "synthetic Broker failure", http.StatusServiceUnavailable)
	}))
	t.Cleanup(broker.Close)

	gateway := newFakeAIGateway()
	gateway.models["recorded-route"] = aigateway.ModelRequest{
		ID: "recorded-route", ModelName: "copilot/example", AccessGroups: []string{copilotGroup()},
	}
	scheme := agentIdentityTestScheme(t)
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := istiov1beta1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(agent).WithObjects(agent).Build()
	reconciler := &AgentReconciler{
		Client:                  kube,
		EventRecorder:           record.NewFakeRecorder(10),
		AgentIdentityBrokerURL:  broker.URL,
		AgentIdentityHTTPClient: broker.Client(),
		AIGateway:               aiGatewayTestConfig(),
		aiGatewayClientFactory: func(context.Context) (aiGatewayKeyClient, error) {
			return gateway, nil
		},
	}

	_, err := reconciler.deleteSandboxBuiltin(context.Background(), agent)
	if err == nil || !strings.Contains(err.Error(), "clean up sandbox-held Agent Identity") {
		t.Fatalf("Agent Identity cleanup failure was not retained: %v", err)
	}
	if !slices.Equal(gateway.deletedAliases, []string{aiGatewayKeyAlias(agent)}) {
		t.Fatalf("AI Gateway key alias was not revoked after Broker failure: %v", gateway.deletedAliases)
	}
	if !slices.Equal(gateway.deletedModels, []string{"recorded-route"}) {
		t.Fatalf("Copilot registration was not revoked after Broker failure: %v", gateway.deletedModels)
	}

	stored := &cetirunv1alpha1.Agent{}
	if err := kube.Get(context.Background(), types.NamespacedName{Name: agent.Name}, stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.AgentIdentity == nil || stored.Status.AgentIdentity.Phase != cetirunv1alpha1.AgentIdentityPhaseDeleting ||
		stored.Status.AgentIdentity.CredentialPolicyRevision != "" {
		t.Fatalf("Agent Identity deletion gate was not persisted: %#v", stored.Status.AgentIdentity)
	}
	if stored.Status.AIGateway == nil || stored.Status.AIGateway.Phase != cetirunv1alpha1.AIGatewayPhaseDeleting {
		t.Fatalf("AI Gateway deletion gate was not persisted: %#v", stored.Status.AIGateway)
	}
}
