// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// listCache is a stand-in cache: List returns the Agents and Teams it holds,
// fails for the kinds in failing, and calls during before it returns.
type listCache struct {
	cache.Cache
	agents  []api.Agent
	teams   []api.Team
	failing map[string]bool
	during  func()
	synced  bool
}

func (c *listCache) List(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
	if c.during != nil {
		c.during()
	}
	switch typed := list.(type) {
	case *api.AgentList:
		if c.failing[LogTypeAgent] {
			return errors.New("synthetic list failure")
		}
		typed.Items = append(typed.Items, c.agents...)
	case *api.TeamList:
		if c.failing[LogTypeTeam] {
			return errors.New("synthetic list failure")
		}
		typed.Items = append(typed.Items, c.teams...)
	}
	return nil
}

func (c *listCache) WaitForCacheSync(context.Context) bool {
	c.synced = true
	return true
}

func agentNamed(name string) api.Agent {
	return api.Agent{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-" + name)}}
}

// recorderFor returns a recorder of Agents and Teams that writes to out with a clock that advances one second a call.
func recorderFor(out *bytes.Buffer) *Recorder {
	writer := NewWriter(out)
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	writer.now = func() time.Time {
		clock = clock.Add(time.Second)
		return clock
	}
	return &Recorder{Writer: writer, Log: logr.Discard(), newID: func() string { return "snapshot-1" },
		registered: map[string]bool{LogTypeAgent: true, LogTypeTeam: true}}
}

func lines(t *testing.T, out *bytes.Buffer) []Record {
	t.Helper()
	var records []Record
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var record Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("not a record: %q", line)
		}
		records = append(records, record)
	}
	return records
}

func TestSnapshotMarksEachCompleteKind(t *testing.T) {
	var out bytes.Buffer
	r := recorderFor(&out)
	c := &listCache{agents: []api.Agent{agentNamed("a"), agentNamed("b")}}
	if err := r.Snapshot(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	records := lines(t, &out)
	if len(records) != 4 {
		t.Fatalf("want 2 Agent entries and 2 completion records, got %d: %s", len(records), out.String())
	}
	for _, entry := range records[:2] {
		if entry.LogType != LogTypeAgent || entry.Action != ActionSnapshot || entry.SnapshotID != "snapshot-1" {
			t.Fatalf("entry %+v", entry)
		}
	}
	agents := records[2]
	if agents.LogType != LogTypeSnapshot || agents.Snapshot == nil || agents.Snapshot.ID != "snapshot-1" ||
		agents.Snapshot.Kind != LogTypeAgent || agents.Snapshot.Objects != 2 {
		t.Fatalf("Agent completion record %+v", agents.Snapshot)
	}
	// The snapshot started before its entries were written.
	if agents.Snapshot.Started >= records[0].Time {
		t.Fatalf("started %s is not before the first entry %s", agents.Snapshot.Started, records[0].Time)
	}
	// A kind without objects is complete too: its objects are all gone.
	teams := records[3]
	if teams.Snapshot == nil || teams.Snapshot.Kind != LogTypeTeam || teams.Snapshot.Objects != 0 {
		t.Fatalf("Team completion record %+v", teams.Snapshot)
	}
	// Kinds whose resources are not installed are not snapshot.
	for _, record := range records {
		if record.Snapshot != nil && record.Snapshot.Kind == LogTypeSandbox {
			t.Fatal("a kind that is not registered got a completion record")
		}
	}
}

func TestAKindThatCannotBeListedIsNotMarkedComplete(t *testing.T) {
	var out bytes.Buffer
	r := recorderFor(&out)
	c := &listCache{agents: []api.Agent{agentNamed("a")}, failing: map[string]bool{LogTypeAgent: true}}
	if err := r.Snapshot(context.Background(), c); err == nil || !strings.Contains(err.Error(), "synthetic list failure") {
		t.Fatalf("want the list error, got %v", err)
	}
	for _, record := range lines(t, &out) {
		if record.LogType == LogTypeAgent || (record.Snapshot != nil && record.Snapshot.Kind == LogTypeAgent) {
			t.Fatalf("Agent record after a failed list: %+v", record)
		}
	}
	// The other kinds are still marked complete.
	if !strings.Contains(out.String(), `"kind":"`+LogTypeTeam+`"`) {
		t.Fatal("the Team snapshot was not completed")
	}
}

func TestAnObjectDeletedDuringASnapshotGetsNoEntryAfterItsDelete(t *testing.T) {
	var out bytes.Buffer
	r := recorderFor(&out)
	agentKind := kinds[0]
	deleted := agentNamed("gone")
	c := &listCache{agents: []api.Agent{agentNamed("kept"), deleted}}
	// The list holds the Agent, but its delete arrives before the entries are written.
	c.during = func() { r.inventory(agentKind, &deleted, ActionDelete) }
	if err := r.Snapshot(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	var sawDelete bool
	for _, record := range lines(t, &out) {
		if record.Agent != nil && record.Agent.UID == "uid-gone" {
			if record.Action == ActionDelete {
				sawDelete = true
			} else {
				t.Fatalf("a %s record of the deleted Agent", record.Action)
			}
		}
		if record.Snapshot != nil && record.Snapshot.Kind == LogTypeAgent && record.Snapshot.Objects != 1 {
			t.Fatalf("completion record counts %d Agents, want 1", record.Snapshot.Objects)
		}
	}
	if !sawDelete {
		t.Fatal("no delete record")
	}
	// After the snapshot, deletes are no longer collected.
	if r.deleted != nil {
		t.Fatal("deleted UIDs kept after the snapshot")
	}
}

func TestSnapshotsStartOnceTheCacheHasSynced(t *testing.T) {
	var out bytes.Buffer
	r := recorderFor(&out)
	c := &listCache{agents: []api.Agent{agentNamed("a")}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.RunSnapshots(ctx, c, time.Hour) }()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(syncedOutput(r, &out), LogTypeSnapshot) {
		if time.Now().After(deadline) {
			t.Fatal("no snapshot before the first interval")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !c.synced {
		t.Fatal("the snapshot did not wait for the cache")
	}
}

// syncedOutput reads out under the writer's lock.
func syncedOutput(r *Recorder, out *bytes.Buffer) string {
	r.Writer.mu.Lock()
	defer r.Writer.mu.Unlock()
	return out.String()
}
