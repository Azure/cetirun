// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package tools

import (
	"encoding/json"
	"testing"
)

func TestCatalogHasFixedCompleteToolSet(t *testing.T) {
	if got := len(Catalog()); got != 20 {
		t.Fatalf("catalog size = %d, want 20", got)
	}
	seen := map[string]bool{}
	for _, spec := range Catalog() {
		if seen[spec.Name] {
			t.Fatalf("duplicate tool %q", spec.Name)
		}
		seen[spec.Name] = true
	}
}

func TestAzureWriteSchemasAreStrictAndMarkedWrite(t *testing.T) {
	create, found := Find("azure_resource_groups_create")
	if !found || !create.Write {
		t.Fatal("Azure resource group create is not a write tool")
	}
	validCreate := json.RawMessage(`{"connection_id":"conn-demo","subscription_id":"11111111-1111-1111-1111-111111111111","resource_group_name":"agent-test-rg","location":"eastus","operation_id":"operation-1"}`)
	arguments, err := DecodeArguments(validCreate, create.InputSchema)
	if err != nil || ValidateToolArguments(create.Name, arguments) != nil {
		t.Fatalf("valid create arguments rejected: %v", err)
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"connection_id":"conn-demo","subscription_id":"11111111-1111-1111-1111-111111111111","resource_group_name":"../outside","location":"eastus","operation_id":"operation-1"}`),
		json.RawMessage(`{"connection_id":"conn-demo","subscription_id":"11111111-1111-1111-1111-111111111111","resource_group_name":"agent-test-rg.","location":"eastus","operation_id":"operation-1"}`),
		json.RawMessage(`{"connection_id":"conn-demo","subscription_id":"11111111-1111-1111-1111-111111111111","resource_group_name":"agent-test-rg","location":"east us","operation_id":"operation-1"}`),
	} {
		if arguments, err := DecodeArguments(raw, create.InputSchema); err == nil && ValidateToolArguments(create.Name, arguments) == nil {
			t.Fatalf("unsafe create arguments accepted: %s", raw)
		}
	}

	tags, found := Find("azure_resource_group_tags_update")
	if !found || !tags.Write {
		t.Fatal("Azure resource group tags update is not a write tool")
	}
	tooMany := "{"
	for index := 0; index < 51; index++ {
		if index > 0 {
			tooMany += ","
		}
		tooMany += `"tag` + string(rune('a'+index%26)) + `-` + string(rune('a'+index/26)) + `":"value"`
	}
	tooMany += "}"
	raw := json.RawMessage(`{"connection_id":"conn-demo","subscription_id":"11111111-1111-1111-1111-111111111111","resource_group_name":"agent-test-rg","tags":` + tooMany + `,"operation_id":"operation-1"}`)
	if _, err := DecodeArguments(raw, tags.InputSchema); err == nil {
		t.Fatal("tag map above the maximum was accepted")
	}
}

func TestPullRequestSchemaRejectsLegacyAndUnknownArguments(t *testing.T) {
	spec, found := Find("github_pull_requests_create")
	if !found {
		t.Fatal("pull request schema is missing")
	}
	valid := json.RawMessage(`{"connection_id":"conn-demo","repository_id":"123","title":"Example","head":"feature/example","base_branch":"main","operation_id":"operation-1"}`)
	if _, err := DecodeArguments(valid, spec.InputSchema); err != nil {
		t.Fatalf("valid arguments rejected: %v", err)
	}
	legacy := json.RawMessage(`{"connection_id":"conn-demo","repository_id":"123","title":"Example","head":"feature/example","base":"main","operation_id":"operation-1"}`)
	if _, err := DecodeArguments(legacy, spec.InputSchema); err == nil {
		t.Fatal("legacy base argument was accepted")
	}
}

func TestCalendarSchemaRejectsLongOrReversedRanges(t *testing.T) {
	spec, found := Find("calendar_events_list")
	if !found {
		t.Fatal("calendar list schema is missing")
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"connection_id":"conn-demo","calendar_id":"primary","start":"2026-10-01T09:00:00+08:00","end":"2026-11-02T09:00:00+08:00"}`),
		json.RawMessage(`{"connection_id":"conn-demo","calendar_id":"primary","start":"2026-10-02T09:00:00+08:00","end":"2026-10-01T09:00:00+08:00"}`),
	} {
		arguments, err := DecodeArguments(raw, spec.InputSchema)
		if err != nil || ValidateToolArguments("calendar_events_list", arguments) == nil {
			t.Fatalf("invalid range %s was accepted", raw)
		}
	}
}
