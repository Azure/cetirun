// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxworkload

import (
	"reflect"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestExecutionDigestSeparatesInputsFromObservations(t *testing.T) {
	object := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "input", Namespace: "example", UID: "input-uid", ResourceVersion: "1",
		OwnerReferences: []metav1.OwnerReference{AgentOwner("agent", "agent-uid")}},
		Data: map[string]string{"input": "synthetic"}}
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
	if err != nil {
		t.Fatal(err)
	}
	current := &unstructured.Unstructured{Object: raw}
	before := current.DeepCopy()
	first, err := ExecutionObjectDigest(current)
	if err != nil || !reflect.DeepEqual(current, before) {
		t.Fatal("digest mutated the unstructured source:", err)
	}
	current.SetResourceVersion("2")
	current.SetLabels(map[string]string{"example.com/unrelated": "changed"})
	current.SetAnnotations(map[string]string{"example.com/unrelated": "changed"})
	current.Object["status"] = map[string]any{"observedGeneration": int64(3)}
	second, err := ExecutionObjectDigest(current)
	if err != nil || first != second {
		t.Fatal("observation metadata changed effective digest:", err)
	}
	for _, mutation := range []string{"data", "owner", "control annotation", "control label", "deletion", "lost"} {
		t.Run(mutation, func(t *testing.T) {
			changed := current.DeepCopy()
			switch mutation {
			case "data":
				changed.Object["data"] = map[string]any{"input": "different"}
			case "owner":
				changed.SetOwnerReferences([]metav1.OwnerReference{AgentOwner("other", "other-uid")})
			case "control annotation":
				changed.SetAnnotations(map[string]string{"meta.helm.sh/release-name": "other"})
			case "control label":
				changed.SetLabels(map[string]string{ProtectedNamespaceLabel: "false"})
			case "deletion":
				now := metav1.Now()
				changed.SetDeletionTimestamp(&now)
			case "lost":
				changed.Object["status"] = map[string]any{"phase": "Lost"}
			}
			got, err := ExecutionObjectDigest(changed)
			if err == nil && got == first {
				t.Fatal("effective control, ownership or lifecycle drift was ignored")
			}
		})
	}
}

func TestExecutionDigestRejectsSecretsAndMetadataOnlyEvidence(t *testing.T) {
	metadata := metav1.ObjectMeta{Name: "input", UID: "input-uid", ResourceVersion: "1"}
	if _, err := ExecutionObjectDigest(&corev1.Secret{ObjectMeta: metadata, Data: map[string][]byte{"example": []byte("synthetic")}}); err == nil {
		t.Fatal("Secret contents accepted as execution inputs")
	}
	if _, err := ExecutionObjectDigest(&metav1.PartialObjectMetadata{ObjectMeta: metadata}); err == nil {
		t.Fatal("metadata alone accepted as effective inputs")
	}
}

func TestTeamEffectiveSelectionExcludesUnrelatedSlots(t *testing.T) {
	team := &api.Team{ObjectMeta: metav1.ObjectMeta{Name: "team", UID: "team-uid", ResourceVersion: "1"},
		Spec: api.TeamSpec{NormalAgentPolicyRef: "normal", AdminAgentPolicyRef: "admin",
			SandboxRefs: []string{"selected", "spare"}, DefaultSandboxRef: "selected"}}
	first, err := ExecutionTeamInputs(team)
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := first.DigestFor("selected", api.AgentPolicyClassNormal)
	if err != nil {
		t.Fatal(err)
	}
	team.Spec.DefaultSandboxRef, team.Spec.AdminAgentPolicyRef = "spare", "other-admin"
	team.ResourceVersion = "2"
	second, err := ExecutionTeamInputs(team)
	if err != nil {
		t.Fatal(err)
	}
	current, err := second.DigestFor("selected", api.AgentPolicyClassNormal)
	if err != nil || current != pinned {
		t.Fatal("creation default or other class invalidated normal selection:", err)
	}
	team.Spec.NormalAgentPolicyRef = "other-normal"
	third, err := ExecutionTeamInputs(team)
	if err != nil {
		t.Fatal(err)
	}
	current, err = third.DigestFor("selected", api.AgentPolicyClassNormal)
	if err != nil || current == pinned {
		t.Fatal("selected policy rebind did not invalidate execution:", err)
	}
}
