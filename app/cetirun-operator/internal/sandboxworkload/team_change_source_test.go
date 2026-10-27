// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxworkload

import (
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxbinding"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestAgentSourceReferenceKeepsRunningProofForTeamChangeRequest(t *testing.T) {
	base := &api.Agent{ObjectMeta: metav1.ObjectMeta{Name: "agent", UID: "agent-uid", ResourceVersion: "1", Generation: 4,
		Annotations: map[string]string{sandboxbinding.AuthorizationAnnotation: `{"version":"v1","team":{"name":"old","uid":"team-old"},"sandbox":{"name":"box","uid":"box-uid"},"agentPolicy":{"name":"policy","uid":"policy-uid"}}`}},
		Spec: api.AgentSpec{Team: "old", SandboxRef: "box"}}
	want, err := AgentSourceReference(base)
	if err != nil {
		t.Fatal(err)
	}
	requested := base.DeepCopy()
	requested.Generation = 5
	requested.Spec.TeamChangeRequest = &api.TeamChangeRequest{RequestID: "team-change-0001", ExpectedUID: types.UID("agent-uid"), ExpectedResourceVersion: "1", TargetTeam: "new"}
	requested.Status.TeamChange = &api.TeamChangeStatus{TeamChangeRequest: *requested.Spec.TeamChangeRequest,
		Phase: api.TeamChangeValidating, SourceGeneration: 4}
	got, err := AgentSourceReference(requested)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("request-only Team change changed running source: got %#v, want %#v", got, want)
	}
}
