// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package v1alpha1_test

import (
	"encoding/json"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"
)

func TestCreationExamplesDefaultToHermes(t *testing.T) {
	for _, path := range []string{
		"../../../adminclaw/deploy/agent-admin.yaml",
		"../../test/config/sample-alice.yaml",
		"../../test/config/sample-bob.yaml",
		"../../test/config/sample-team.yaml",
		"../../../../site/content/en/docs/getting-started/first-instance.md",
		"../../../../site/content/en/docs/guides/msteams.md",
	} {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			blocks := []string{string(raw)}
			if strings.HasSuffix(path, ".md") {
				blocks = nil
				for _, section := range strings.Split(string(raw), "```yaml\n")[1:] {
					block, _, _ := strings.Cut(section, "```")
					if strings.Contains(block, "kind: Agent\n") {
						blocks = append(blocks, block)
					}
				}
			}
			count := 0
			for _, block := range blocks {
				decoder := yamlutil.NewYAMLOrJSONDecoder(strings.NewReader(block), 4096)
				for {
					var example struct {
						Kind string `json:"kind"`
						Spec struct {
							Agent api.HarnessSpec `json:"agent"`
						} `json:"spec"`
					}
					if err := decoder.Decode(&example); err == io.EOF {
						break
					} else if err != nil {
						t.Fatal(err)
					}
					if example.Kind == "Agent" {
						count++
						if example.Spec.Agent.Type != api.AgentTypeHermes {
							t.Errorf("creation example selects %q, want hermes", example.Spec.Agent.Type)
						}
					}
				}
			}
			if count == 0 {
				t.Fatal("no Agent creation examples found")
			}
		})
	}
}

func TestAgentSerialization(t *testing.T) {
	instance := api.Agent{
		Spec: api.AgentSpec{
			Agent: api.HarnessSpec{
				Type:              api.AgentTypeCustom,
				Image:             ptr.To("registry.example.com/team/agent:1"),
				RegistrySecretRef: &api.RegistrySecretRef{Name: "registry-agent"},
				Username:          ptr.To("vibe"),
				Port:              ptr.To[int32](8080),
			},
			User:  api.UserSpec{Alias: "alice", DisplayName: "Alice"},
			Model: "independent-model",
		},
	}
	raw, err := json.Marshal(instance)
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{`"agentType"`, `"gatewayTokenSecretRef"`, `"password"`, `"registryAuth"`} {
		if strings.Contains(string(raw), absent) {
			t.Errorf("unexpected field %s", absent)
		}
	}
	var decoded api.Agent
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.Spec, instance.Spec) {
		t.Fatalf("round trip changed spec: %#v", decoded.Spec)
	}
	if raw, err = json.Marshal(api.HarnessSpec{Type: api.AgentTypeCustom}); err != nil || string(raw) != `{"type":"custom"}` {
		t.Fatalf("Go serialization must not invent defaults: %s, %v", raw, err)
	}
	if raw, err = json.Marshal(api.HarnessSpec{}); err != nil || string(raw) != `{}` {
		t.Fatalf("omitted type must allow API defaulting: %s, %v", raw, err)
	}
}

func TestAgentDeepCopy(t *testing.T) {
	instance := &api.Agent{
		Spec: api.AgentSpec{
			Agent: api.HarnessSpec{
				Type: api.AgentTypeCustom, Image: ptr.To("registry.example.com/agent:1"),
				Username: ptr.To("vibe"), Port: ptr.To[int32](8080),
				RegistrySecretRef: &api.RegistrySecretRef{Name: "source"},
			},
			AgentIdentity: &api.AgentIdentitySpec{Enabled: true},
		},
		Status: api.AgentStatus{
			MSTeams:       &api.MSTeamsStatus{AppNotFoundSince: &metav1.Time{}},
			AgentIdentity: &api.AgentIdentityStatus{LastVerifiedTime: &metav1.Time{}},
		},
	}
	copy := instance.DeepCopy()
	*copy.Spec.Agent.Image = "other"
	*copy.Spec.Agent.Username = "other"
	*copy.Spec.Agent.Port = 9090
	copy.Spec.Agent.RegistrySecretRef.Name = "other"
	copy.Spec.AgentIdentity.Enabled = false
	copy.Status.MSTeams.AppNotFoundSince.Time = metav1.Now().Time
	copy.Status.AgentIdentity.LastVerifiedTime.Time = metav1.Now().Time
	if *instance.Spec.Agent.Image != "registry.example.com/agent:1" ||
		*instance.Spec.Agent.Username != "vibe" ||
		*instance.Spec.Agent.Port != 8080 ||
		instance.Spec.Agent.RegistrySecretRef.Name != "source" ||
		!instance.Spec.AgentIdentity.Enabled ||
		!instance.Status.MSTeams.AppNotFoundSince.IsZero() ||
		!instance.Status.AgentIdentity.LastVerifiedTime.IsZero() {
		t.Fatal("deep copy shares mutable fields with the original")
	}
	list := (&api.AgentList{Items: []api.Agent{*instance}}).DeepCopy()
	*list.Items[0].Spec.Agent.Port = 9091
	if *instance.Spec.Agent.Port != 8080 {
		t.Fatal("list deepcopy shares agent fields")
	}
}

func TestWebUISerializationAndCopy(t *testing.T) {
	agent := &api.Agent{Spec: api.AgentSpec{Agent: api.HarnessSpec{Type: api.AgentTypeCustom,
		Port: ptr.To[int32](8080), WebUI: true,
	}}}
	copy := agent.DeepCopy()
	copy.Spec.Agent.WebUI = false
	*copy.Spec.Agent.Port = 9000
	if !agent.Spec.Agent.WebUI || *agent.Spec.Agent.Port != 8080 {
		t.Fatal("Web UI is shared by the deepcopy")
	}
	raw, err := json.Marshal(agent.Spec.Agent)
	if err != nil || string(raw) != `{"type":"custom","port":8080,"webUI":true}` {
		t.Fatalf("Web UI serialization failed: %s %v", raw, err)
	}
	for _, invalid := range []string{`{"webUI":{"port":8080}}`, `{"webUI":"true"}`, `{"webUI":1}`} {
		if err := json.Unmarshal([]byte(invalid), &api.HarnessSpec{}); err == nil {
			t.Fatalf("non-boolean Web UI accepted: %s", invalid)
		}
	}
}

func TestGeneratedAgentSchema(t *testing.T) {
	raw, err := os.ReadFile("../../config/crd/bases/cetirun.microsoft.com_agents.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.UnmarshalStrict(raw, &crd); err != nil {
		t.Fatal(err)
	}
	if crd.Spec.Scope != apiextensionsv1.ClusterScoped || crd.Spec.Names.Kind != "Agent" ||
		crd.Spec.Group != "cetirun.microsoft.com" || crd.Spec.Versions[0].Name != "v1alpha1" {
		t.Fatal("existing resource identity changed")
	}
	root := crd.Spec.Versions[0].Schema.OpenAPIV3Schema
	spec := root.Properties["spec"]
	agent := spec.Properties["agent"]
	if !slices.Contains(root.Required, "spec") || !slices.Contains(spec.Required, "agent") {
		t.Fatal("spec and agent must be required")
	}
	if slices.Contains(agent.Required, "type") || agent.Properties["type"].Default == nil ||
		string(agent.Properties["type"].Default.Raw) != `"hermes"` {
		t.Fatal("omitted type must default to hermes")
	}
	if _, exists := spec.Properties["agentType"]; exists {
		t.Fatal("legacy agentType must not remain in the schema")
	}
	if agent.Default != nil ||
		agent.Properties["image"].Default != nil || agent.Properties["port"].Default != nil {
		t.Fatal("schema must not infer an agent object, image or port")
	}
	if _, exists := root.Properties["status"].Properties["agent"]; exists {
		t.Fatal("unused harness status must not be exposed")
	}
	if username := agent.Properties["username"]; username.Default == nil ||
		string(username.Default.Raw) != `"vibe"` {
		t.Fatal("username must default to vibe")
	}
	webUI := agent.Properties["webUI"]
	if webUI.Type != "boolean" || webUI.Default == nil || string(webUI.Default.Raw) != "false" || len(webUI.Properties) != 0 {
		t.Fatal("webUI must be a boolean defaulting to false, without a second port")
	}
	fields := []string{"type", "image", "registrySecretRef", "username", "port", "webUI"}
	if len(agent.Properties) != len(fields) {
		t.Fatalf("unexpected public agent fields: %v", agent.Properties)
	}
	for _, name := range fields {
		if _, exists := agent.Properties[name]; !exists {
			t.Errorf("agent.%s missing", name)
		}
	}
	ref := agent.Properties["registrySecretRef"]
	if len(ref.Properties) != 1 || !slices.Contains(ref.Required, "name") ||
		*ref.Properties["name"].MaxLength != 253 {
		t.Fatal("registry reference must contain only a required bounded name")
	}
	if slices.Contains(spec.Required, "gatewayTokenSecretRef") || len(spec.XValidations) == 0 {
		t.Fatal("built-in credential requirement must be conditional")
	}
	for _, name := range []string{"model", "user", "team", "skillOverrides", "agentIdentity", "helm", "suspended"} {
		if _, exists := spec.Properties[name]; !exists {
			t.Errorf("independent spec.%s missing", name)
		}
	}
	foundCustomAgentIdentityGate := false
	for _, validation := range spec.XValidations {
		if strings.Contains(validation.Rule, "!has(self.agentIdentity.enabled)") &&
			strings.Contains(validation.Rule, "!self.agentIdentity.enabled") &&
			strings.Contains(validation.Rule, "self.agent.type != 'custom'") {
			foundCustomAgentIdentityGate = true
			break
		}
	}
	if !foundCustomAgentIdentityGate {
		t.Fatal("schema must reject Agent Identity for custom agents")
	}
	for _, column := range crd.Spec.Versions[0].AdditionalPrinterColumns {
		if column.Name == "Agent" {
			if column.JSONPath != ".spec.agent.type" {
				t.Fatal("Agent column must read the new field")
			}
			return
		}
	}
	t.Fatal("Agent printer column missing")
}
