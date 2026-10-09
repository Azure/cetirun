// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sandboxadmission

import (
	"testing"

	authenticationv1 "k8s.io/api/authentication/v1"
)

func TestHandlerOwnsImmutableCopyOfAdministratorConfiguration(t *testing.T) {
	f := newFixture(t)
	config := testConfig()
	handler, err := NewHandler(f.client, config)
	if err != nil {
		t.Fatal(err)
	}
	config.AdminUsers[0] = "attacker"
	config.AdminGroups[0] = "attacker-group"
	if handler.config.administrator(authenticationv1.UserInfo{Username: "attacker", Groups: []string{"attacker-group"}}) {
		t.Fatal("caller mutation expanded administration")
	}
}

func TestConfigRejectsRemovedTeamAssignments(t *testing.T) {
	_, err := ParseConfig(`{"adminUsers":["administrator"],"controllerUsername":"system:serviceaccount:operator-test:controller","teamAssignments":[]}`)
	if err == nil {
		t.Fatal("removed Team assignment configuration was accepted")
	}
}

func TestPlatformSourceConfigurationRequiresControllerNamespace(t *testing.T) {
	for _, test := range []struct {
		namespace, name string
		valid           bool
	}{
		{"", "", true},
		{"operator-test", "sandbox-platform", true},
		{"", "sandbox-platform", false},
		{"operator-test", "", false},
		{"workload", "sandbox-platform", false},
		{"Operator-test", "sandbox-platform", false},
		{"operator-test", " platform", false},
		{"operator-test", "bad/name", false},
	} {
		config := testConfig()
		config.PlatformNamespace, config.PlatformConfigMapName = test.namespace, test.name
		if err := config.Validate(); (err == nil) != test.valid {
			t.Errorf("source %q/%q valid=%t: %v", test.namespace, test.name, test.valid, err)
		}
	}
	config, err := ParseConfig(`{"adminUsers":["administrator","system:serviceaccount:operator-test:portal"],"controllerUsername":"system:serviceaccount:operator-test:controller","platformNamespace":"operator-test","platformConfigMapName":"sandbox-platform"}`)
	if err != nil || config.PlatformNamespace != "operator-test" || config.PlatformConfigMapName != "sandbox-platform" {
		t.Fatalf("explicit source did not roundtrip: %+v, %v", config, err)
	}
}
