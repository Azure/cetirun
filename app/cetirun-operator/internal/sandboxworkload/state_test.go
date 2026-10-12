// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxworkload

import (
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

func TestPodExpectationObjectRejectsOlderExpectationVersions(t *testing.T) {
	expected := Expectation{
		Version:   ExpectationVersion,
		Agent:     ObjectIdentity{Name: "agent", UID: types.UID("agent-uid")},
		Namespace: ObjectIdentity{Name: "workloads", UID: types.UID("namespace-uid")},
	}
	if _, err := PodExpectationObject("operator-state", expected); err != nil {
		t.Fatal(err)
	}
	expected.Version = "v2"
	if _, err := PodExpectationObject("operator-state", expected); err == nil {
		t.Fatal("older expectation version was accepted")
	}
}
