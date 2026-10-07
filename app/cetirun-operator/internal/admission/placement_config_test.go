// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxadmission

import (
	"context"
	"strings"
	"testing"
)

func TestPlacementConfigRequiresLiteralName(t *testing.T) {
	for _, name := range []string{"", "sandbox-placement", "pilot.placement"} {
		config := testConfig()
		config.PlacementConfigMapName = name
		if err := config.Validate(); err != nil {
			t.Fatalf("valid placement name %q: %v", name, err)
		}
	}
	for _, name := range []string{" ", "Invalid", "../placement", "state/placement", strings.Repeat("x", 254)} {
		config := testConfig()
		config.PlacementConfigMapName = name
		if err := config.Validate(); err == nil {
			t.Fatalf("invalid placement name %q was accepted", name)
		}
	}
	config, err := ParseConfig(`{"adminUsers":["administrator"],"controllerUsername":"system:serviceaccount:operator-test:controller","placementConfigMapName":"sandbox-placement"}`)
	if err != nil || config.PlacementConfigMapName != "sandbox-placement" {
		t.Fatalf("placement selection failed to roundtrip: %v", err)
	}
}

func TestPlacementServerRequiresProtectedPodStateNamespace(t *testing.T) {
	config := testConfig()
	config.PlacementConfigMapName = "sandbox-placement"
	for _, namespace := range []string{"", "agent-workload", "another-operator"} {
		err := Run(context.Background(), nil, config, ServerOptions{PodStateNamespace: namespace})
		if err == nil || !strings.Contains(err.Error(), "configured controller state namespace") {
			t.Fatalf("placement serving with namespace %q did not reject before startup: %v", namespace, err)
		}
	}
}
