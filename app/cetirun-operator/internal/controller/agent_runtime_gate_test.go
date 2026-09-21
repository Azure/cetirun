// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
)

func TestAgentRuntimeGate(t *testing.T) {
	tests := []struct {
		name  string
		agent api.HarnessSpec
		valid bool
	}{
		{"openclaw", api.HarnessSpec{Type: api.AgentTypeOpenClaw}, true},
		{"hermes", api.HarnessSpec{Type: api.AgentTypeHermes, Username: ptr.To("vibe")}, true},
		{"missing type", api.HarnessSpec{}, false},
		{"unknown type", api.HarnessSpec{Type: "unknown"}, false},
		{"custom", api.HarnessSpec{Type: api.AgentTypeCustom, Image: ptr.To("registry.example.com/agent:1")}, false},
		{"image override", api.HarnessSpec{Type: api.AgentTypeHermes, Image: ptr.To("registry.example.com/agent:1")}, false},
		{"port override", api.HarnessSpec{Type: api.AgentTypeOpenClaw, Port: ptr.To[int32](8080)}, false},
		{"registry reference", api.HarnessSpec{Type: api.AgentTypeOpenClaw, RegistrySecretRef: &api.RegistrySecretRef{Name: "source"}}, false},
		{"username override", api.HarnessSpec{Type: api.AgentTypeOpenClaw, Username: ptr.To("alice")}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instance := newTestInstance("alice", "Example User", "", "gateway-token")
			instance.Spec.Agent = tt.agent
			values, err := buildHelmValues(instance, nil, "example.com", "", nil, "")
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v, error=%v", tt.valid, err)
			}
			if !tt.valid && values != nil {
				t.Fatal("unsupported agent must not render built-in Helm values")
			}
		})
	}
}

func TestAgentTypeCannotBeOverriddenByHelm(t *testing.T) {
	instance := newTestInstance("alice", "Example User", "", "gateway-token")
	instance.Spec.Agent.Type = api.AgentTypeHermes
	values, err := buildHelmValues(instance, nil, "example.com", "", map[string]interface{}{"agentType": "openclaw"}, "")
	if err != nil || values["agentType"] != "hermes" {
		t.Fatalf("deployment defaults must not override instance type: %v, %v", values["agentType"], err)
	}
	for _, raw := range []string{`"openclaw"`, `"custom"`, `null`, `{}`} {
		instance.Spec.Helm.Values["agentType"] = runtime.RawExtension{Raw: []byte(raw)}
		if _, err := buildHelmValues(instance, nil, "example.com", "", nil, ""); err == nil {
			t.Errorf("conflicting helm.values.agentType accepted: %s", raw)
		}
	}
}
