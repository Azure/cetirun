// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"reflect"
	"strings"
	"testing"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestCustomSkillConfiguration(t *testing.T) {
	global := map[string]any{"skills": map[string]any{
		"shared": true, "resourceGroup": "platform", "storageAccount": "globalskills",
		"containerName": "approved", "protocol": "nfs", "npm": []any{},
	}}
	team := &api.Team{Spec: api.TeamSpec{SkillStorage: &api.SkillStorageSpec{
		ResourceGroup: "team", StorageAccount: "teamskills", ContainerName: "team-skills",
		StorageSize: "200Gi", MountPath: "/native-discovery",
	}}}
	instance := &api.Agent{Spec: api.AgentSpec{Agent: api.HarnessSpec{Type: api.AgentTypeCustom}}}
	r := &AgentReconciler{CetiRunHelmValues: global}
	values, err := r.customOverrides(instance, team)
	if err != nil {
		t.Fatal(err)
	}
	skills := values["skills"].(map[string]any)
	if skills["resourceGroup"] != "team" || skills["storageAccount"] != "teamskills" ||
		skills["protocol"] != "nfs" || skills["storageSize"] != "200Gi" {
		t.Fatal("Custom lost shared Team/global skill settings")
	}
	if global["skills"].(map[string]any)["storageAccount"] != "globalskills" {
		t.Fatal("Custom mutated global settings")
	}
	native := map[string]any{"skills": deepMerge(nil, global["skills"])}
	if err := applySkillValues(native, team, instance.Spec.SkillOverrides, ""); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(skills, native["skills"]) {
		t.Fatal("native and Custom skill selection diverged")
	}
	r.SkillsStorageAccount = "legacyoverride"
	values, err = r.customOverrides(instance, team)
	if err != nil || values["skills"].(map[string]any)["storageAccount"] != "legacyoverride" {
		t.Fatal("explicit platform storage-account override was lost")
	}
	instance.Spec.Helm.Values = map[string]runtime.RawExtension{"skills": {Raw: []byte(`{"storageAccount":"foreign"}`)}}
	if _, err := r.customOverrides(instance, team); err == nil {
		t.Fatal("tenant redirected platform storage through raw Helm values")
	}
}

func TestCustomSkillSelectionCannotBeSilentlyIgnored(t *testing.T) {
	instance := &api.Agent{Spec: api.AgentSpec{Agent: api.HarnessSpec{Type: api.AgentTypeCustom}}}
	r := &AgentReconciler{}
	team := &api.Team{Spec: api.TeamSpec{Skills: []string{"npm:example-skill"}}}
	if _, err := r.customOverrides(instance, team); err == nil || !strings.Contains(err.Error(), "SkillsMaterializationPending") {
		t.Fatal("unmaterialized Team skills were silently ignored")
	}
	instance.Spec.SkillOverrides.Remove = []string{"npm:example-skill"}
	if _, err := r.customOverrides(instance, team); err != nil {
		t.Fatalf("removed Team skill still blocked shared context: %v", err)
	}
	instance.Spec.SkillOverrides.Add = []string{"another-skill"}
	if _, err := r.customOverrides(instance, team); err == nil || !strings.Contains(err.Error(), "SkillsMaterializationPending") {
		t.Fatal("unmaterialized per-Agent skills were silently ignored")
	}
}
