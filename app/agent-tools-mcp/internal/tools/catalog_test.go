// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package tools

import (
	"encoding/json"
	"testing"
)

func TestCatalogHasFixedCompleteToolSet(t *testing.T) {
	if got := len(Catalog()); got != 18 {
		t.Fatalf("catalog size = %d, want 18", got)
	}
	seen := map[string]bool{}
	for _, spec := range Catalog() {
		if seen[spec.Name] {
			t.Fatalf("duplicate tool %q", spec.Name)
		}
		seen[spec.Name] = true
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
