// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package v1alpha1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestExecutionGenerationAndIntentDigest(t *testing.T) {
	request := &TeamChangeRequest{RequestID: "team-change-execution-0001", ExpectedUID: "agent-uid", ExpectedResourceVersion: "1", TargetTeam: "target"}
	base := &Agent{ObjectMeta: metav1.ObjectMeta{Name: "agent", UID: "agent-uid", ResourceVersion: "1", Generation: 9,
		Annotations: map[string]string{SandboxAuthorizationAnnotation: "binding", SandboxReauthorizeAnnotation: "refresh"},
		Finalizers:  []string{SandboxHoldFinalizer, SandboxPreparationFinalizer, SandboxBootstrapFinalizer}},
		Spec:   AgentSpec{Team: "source", SandboxRef: "box", TeamChangeRequest: request},
		Status: AgentStatus{ObservedGeneration: 7}}

	tests := []struct {
		name  string
		state *TeamChangeStatus
		want  int64
	}{
		{"initial request", nil, 7},
		{"validating", &TeamChangeStatus{TeamChangeRequest: *request, Phase: TeamChangeValidating, SourceGeneration: 5}, 5},
		{"stopping", &TeamChangeStatus{TeamChangeRequest: *request, Phase: TeamChangeStopping, SourceGeneration: 5}, 5},
		{"failed before stop", &TeamChangeStatus{TeamChangeRequest: *request, Phase: TeamChangeFailed, SourceGeneration: 5}, 5},
		{"failed after stop", &TeamChangeStatus{TeamChangeRequest: *request, Phase: TeamChangeFailed, SourceGeneration: 5, Stopped: true}, 9},
		{"committing", &TeamChangeStatus{TeamChangeRequest: *request, Phase: TeamChangeCommitting, SourceGeneration: 5}, 9},
		{"starting", &TeamChangeStatus{TeamChangeRequest: *request, Phase: TeamChangeStarting, SourceGeneration: 5}, 9},
		{"completed", &TeamChangeStatus{TeamChangeRequest: *request, Phase: TeamChangeCompleted, SourceGeneration: 5}, 9},
		{"different request", &TeamChangeStatus{TeamChangeRequest: TeamChangeRequest{RequestID: "different-request-0001"}, Phase: TeamChangeStopping, SourceGeneration: 5}, 7},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent := base.DeepCopy()
			agent.Status.TeamChange = test.state
			if got := agent.ExecutionGeneration(); got != test.want {
				t.Fatalf("ExecutionGeneration() = %d, want %d", got, test.want)
			}
		})
	}
	failed := base.DeepCopy()
	failed.Generation, failed.Status.ObservedGeneration = 9, 8
	failed.Status.TeamChange = &TeamChangeStatus{TeamChangeRequest: TeamChangeRequest{RequestID: "rejected-request-0001"},
		Phase: TeamChangeFailed, SourceGeneration: 5, ObservedGeneration: 8}
	failed.Spec.TeamChangeRequest = &TeamChangeRequest{RequestID: "replacement-request-0001"}
	if got := failed.ExecutionGeneration(); got != 5 {
		t.Fatalf("replacement after failed request generation = %d, want retained source 5", got)
	}
	cleared := failed.DeepCopy()
	cleared.Spec.TeamChangeRequest = nil
	if got := cleared.ExecutionGeneration(); got != 5 {
		t.Fatalf("cleared failed request generation = %d, want retained source 5", got)
	}
	cleared.Generation += 4 // A pause/resume or upgrade may add unrelated generation bumps.
	if got := cleared.ExecutionGeneration(); got != 5 {
		t.Fatalf("later request-only generation = %d, want retained source 5", got)
	}
	completed := base.DeepCopy()
	completed.Spec.TeamChangeRequest = nil
	completed.Status.TeamChange = &TeamChangeStatus{TeamChangeRequest: *request, Phase: TeamChangeCompleted, SourceGeneration: 5, ObservedGeneration: 9}
	completed.Generation++
	if got := completed.ExecutionGeneration(); got != 9 {
		t.Fatalf("cleared completed request generation = %d, want completed target epoch 9", got)
	}
	completed.Spec.TeamChangeRequest = &TeamChangeRequest{RequestID: "next-team-change-0001"}
	completed.Generation += 3
	if got := completed.ExecutionGeneration(); got != 9 {
		t.Fatalf("replacement after completed request generation = %d, want ready proof 9", got)
	}
	legacyCompleted := completed.DeepCopy()
	legacyCompleted.Status.TeamChange.ObservedGeneration = 0
	if got := legacyCompleted.ExecutionGeneration(); got != legacyCompleted.Generation {
		t.Fatalf("missing completed target epoch generation = %d, want current %d", got, legacyCompleted.Generation)
	}

	got, err := base.ExecutionIntentDigest()
	if err != nil {
		t.Fatal(err)
	}
	if want := legacyExecutionIntentDigest(t, base); got != want {
		t.Fatalf("ExecutionIntentDigest() = %s, want legacy canonical %s", got, want)
	}
	for name, mutate := range map[string]func(*Agent){
		"team change request": func(agent *Agent) { agent.Spec.TeamChangeRequest.TargetTeam = "other" },
		"unrelated status":    func(agent *Agent) { agent.Status.ObservedGeneration++ },
	} {
		t.Run(name, func(t *testing.T) {
			agent := base.DeepCopy()
			mutate(agent)
			digest, err := agent.ExecutionIntentDigest()
			if err != nil || digest != got {
				t.Fatalf("request/status-only update changed digest: digest=%q err=%v", digest, err)
			}
		})
	}
	for name, mutate := range map[string]func(*Agent){
		"spec":          func(agent *Agent) { agent.Spec.Model = "changed" },
		"authorization": func(agent *Agent) { agent.Annotations[SandboxAuthorizationAnnotation] = "new-binding" },
		"hold": func(agent *Agent) {
			agent.Finalizers = slices.DeleteFunc(agent.Finalizers, func(value string) bool { return value == SandboxHoldFinalizer })
		},
	} {
		t.Run(name, func(t *testing.T) {
			agent := base.DeepCopy()
			mutate(agent)
			digest, err := agent.ExecutionIntentDigest()
			if err != nil || digest == got {
				t.Fatalf("execution input did not change digest: digest=%q err=%v", digest, err)
			}
		})
	}
}

func legacyExecutionIntentDigest(t *testing.T, agent *Agent) string {
	t.Helper()
	annotations := make(map[string]string, 2)
	for _, key := range []string{SandboxAuthorizationAnnotation, SandboxReauthorizeAnnotation} {
		if value, found := agent.Annotations[key]; found {
			annotations[key] = value
		}
	}
	spec := agent.Spec
	spec.TeamChangeRequest = nil
	intent := struct {
		Spec         AgentSpec
		Annotations  map[string]string
		Held         bool
		Prepared     bool
		Bootstrapped bool `json:",omitempty"`
	}{
		Spec:         spec,
		Annotations:  annotations,
		Held:         slices.Contains(agent.Finalizers, SandboxHoldFinalizer),
		Prepared:     slices.Contains(agent.Finalizers, SandboxPreparationFinalizer),
		Bootstrapped: slices.Contains(agent.Finalizers, SandboxBootstrapFinalizer),
	}
	raw, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
