// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"reflect"
	"testing"

	"github.com/Azure/CetiRun/app/cetirun-operator/internal/runtimebindings"
)

func TestCustomRetainedRestartChangesOnlyTheRequestedWorkload(t *testing.T) {
	input := customInput(8080)
	before, err := PrepareChart(testChart(t), input)
	if err != nil {
		t.Fatal(err)
	}
	bindings := input["customBindings"].(runtimebindings.Custom)
	original := bindings
	bindings.RestartRequestID = "request-00000001"
	input["customBindings"] = bindings
	after, err := PrepareChart(testChart(t), input)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := PrepareChart(testChart(t), input)
	if err != nil {
		t.Fatal(err)
	}
	if before.WorkloadHash == after.WorkloadHash || after.WorkloadHash != retry.WorkloadHash || after.PlanHash != retry.PlanHash {
		t.Fatal("restart did not produce a new, deterministic workload")
	}
	if err := after.ValidateCustom(bindings.Namespace.Name, "openclaw-example"); err != nil {
		t.Fatal(err)
	}
	bindings.RestartRequestID = ""
	if !reflect.DeepEqual(bindings, original) {
		t.Fatal("restart changed HOME, model identity, namespace or other bindings")
	}
}
