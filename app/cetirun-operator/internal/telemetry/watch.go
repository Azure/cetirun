// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Recorder writes a record for each event that passes its filter, and for
// each change of an Agent, Team, AgentPolicy or Sandbox.
type Recorder struct {
	Writer *Writer
	Events EventFilter
	Log    logr.Logger

	mu sync.Mutex
	// last holds the record last written for each inventory object, by UID:
	// an update that changes none of the recorded fields writes nothing.
	last map[string]string
	// registered holds the inventory kinds whose handlers are added.
	registered map[string]bool
	// deleted holds the UIDs deleted while a snapshot runs, nil otherwise.
	deleted map[string]bool

	// snapshotMu lets one snapshot run at a time.
	snapshotMu sync.Mutex
	// newID returns a snapshot ID; random unless a test sets it.
	newID func() string
}

// inventory is one kind of inventory object.
type inventory struct {
	logType string
	object  client.Object
	record  func(client.Object) Record
}

var kinds = []inventory{
	{LogTypeAgent, &api.Agent{}, func(o client.Object) Record { return Record{Agent: AgentRecordOf(o.(*api.Agent))} }},
	{LogTypeTeam, &api.Team{}, func(o client.Object) Record { return Record{Team: TeamRecordOf(o.(*api.Team))} }},
	{LogTypeAgentPolicy, &api.AgentPolicy{}, func(o client.Object) Record {
		return Record{AgentPolicy: AgentPolicyRecordOf(o.(*api.AgentPolicy))}
	}},
	{LogTypeSandbox, &api.Sandbox{}, func(o client.Object) Record { return Record{Sandbox: SandboxRecordOf(o.(*api.Sandbox))} }},
}

// Register adds the recorder's handlers to the informers of the cache.
func (r *Recorder) Register(ctx context.Context, c cache.Cache) error {
	if r.Events != EventsNone {
		informer, err := c.GetInformer(ctx, &corev1.Event{})
		if err != nil {
			return fmt.Errorf("event informer: %w", err)
		}
		if _, err := informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
			AddFunc: func(object any) { r.event(object) },
			UpdateFunc: func(previous, object any) {
				// A resync delivers the same version again.
				if old, ok := previous.(*corev1.Event); ok {
					if event, ok := object.(*corev1.Event); ok && old.ResourceVersion == event.ResourceVersion {
						return
					}
				}
				r.event(object)
			},
		}); err != nil {
			return fmt.Errorf("event handler: %w", err)
		}
	}
	_, _, err := r.registerInventory(ctx, c)
	return err
}

// registerInventory adds handlers for the inventory kinds that are not
// registered yet, and returns how many it added and how many are missing. A
// kind whose CustomResourceDefinition is not installed, as when this program
// starts before the CetiRun platform, is left out and counted as missing;
// other errors stop the registration.
func (r *Recorder) registerInventory(ctx context.Context, c cache.Cache) (int, int, error) {
	added, missing := 0, 0
	for _, kind := range kinds {
		if r.isRegistered(kind.logType) {
			continue
		}
		informer, err := c.GetInformer(ctx, kind.object)
		if meta.IsNoMatchError(err) {
			missing++
			continue
		}
		if err != nil {
			return added, missing, fmt.Errorf("%s informer: %w", kind.logType, err)
		}
		if _, err := informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
			AddFunc:    func(object any) { r.inventory(kind, object, ActionUpsert) },
			UpdateFunc: func(_, object any) { r.inventory(kind, object, ActionUpsert) },
			DeleteFunc: func(object any) {
				if tombstone, ok := object.(toolscache.DeletedFinalStateUnknown); ok {
					object = tombstone.Obj
				}
				r.inventory(kind, object, ActionDelete)
			},
		}); err != nil {
			return added, missing, fmt.Errorf("%s handler: %w", kind.logType, err)
		}
		r.mu.Lock()
		if r.registered == nil {
			r.registered = map[string]bool{}
		}
		r.registered[kind.logType] = true
		r.mu.Unlock()
		added++
	}
	return added, missing, nil
}

func (r *Recorder) isRegistered(logType string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.registered[logType]
}

// RunRegistration registers the inventory kinds that were missing at start
// once their CustomResourceDefinitions exist. It checks at each interval
// until every kind is registered or the context ends.
func (r *Recorder) RunRegistration(ctx context.Context, c cache.Cache, interval time.Duration) error {
	reported := false
	for {
		added, missing, err := r.registerInventory(ctx, c)
		if err != nil {
			return err
		}
		if added > 0 {
			// A complete snapshot of the kinds registered now, not after the
			// snapshot interval: a snapshot taken before they were registered
			// left them out.
			if c.WaitForCacheSync(ctx) {
				if err := r.Snapshot(ctx, c); err != nil {
					r.Log.Error(err, "write the inventory snapshot")
				}
			}
		}
		if missing == 0 {
			if reported {
				r.Log.Info("the CetiRun resources are installed; inventory records start")
			}
			return nil
		}
		if !reported {
			r.Log.Info("CetiRun resources are not installed; writing events only until they are", "missingKinds", missing)
			reported = true
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

// Snapshot writes every inventory object again, so that the latest state of
// each one stays within the retention of the logs. Each entry carries the
// snapshot's ID; once every object of a kind is written, a LogTypeSnapshot
// record says that the kind's snapshot is complete. A query then takes the
// objects of the latest complete snapshot, and the changes after it started,
// as the current ones: an object deleted while this program was not running
// is absent from its next snapshot. A kind whose list or writes fail gets no
// completion record, and its previous snapshot stays the latest.
func (r *Recorder) Snapshot(ctx context.Context, reader client.Reader) error {
	r.snapshotMu.Lock()
	defer r.snapshotMu.Unlock()
	r.mu.Lock()
	r.deleted = map[string]bool{}
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.deleted = nil
		r.mu.Unlock()
	}()
	id := r.snapshotID()
	var failed error
	for _, kind := range kinds {
		if !r.isRegistered(kind.logType) {
			continue
		}
		list, err := listOf(kind.object)
		if err != nil {
			return err
		}
		started := r.Writer.now().UTC()
		if err := reader.List(ctx, list); err != nil {
			failed = errors.Join(failed, fmt.Errorf("list %s: %w", kind.logType, err))
			continue
		}
		written, complete := 0, true
		for _, object := range items(list) {
			wrote, err := r.writeInventory(kind, object, ActionSnapshot, id)
			if err != nil {
				failed = errors.Join(failed, fmt.Errorf("write a %s snapshot entry: %w", kind.logType, err))
				complete = false
				break
			}
			if wrote {
				written++
			}
		}
		if !complete {
			continue
		}
		if err := r.Writer.Write(LogTypeSnapshot, Record{Snapshot: &SnapshotRecord{
			ID: id, Kind: kind.logType, Started: started.Format(time.RFC3339Nano), Objects: written,
		}}); err != nil {
			failed = errors.Join(failed, fmt.Errorf("complete the %s snapshot: %w", kind.logType, err))
		}
	}
	return failed
}

func (r *Recorder) snapshotID() string {
	if r.newID != nil {
		return r.newID()
	}
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	return hex.EncodeToString(id)
}

// RunSnapshots writes a snapshot once the cache has synced, then at each
// interval until the context ends. The first one repairs the inventory after
// a time without this program: what was deleted meanwhile is absent from it.
func (r *Recorder) RunSnapshots(ctx context.Context, c cache.Cache, interval time.Duration) error {
	if c.WaitForCacheSync(ctx) {
		if err := r.Snapshot(ctx, c); err != nil {
			r.Log.Error(err, "write the inventory snapshot")
		}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.Snapshot(ctx, c); err != nil {
				r.Log.Error(err, "write the inventory snapshot")
			}
		}
	}
}

func (r *Recorder) event(object any) {
	event, ok := object.(*corev1.Event)
	if !ok || !r.Events.Keep(event) {
		return
	}
	if err := r.Writer.Write(LogTypeEvent, Record{Event: EventRecordOf(event)}); err != nil {
		r.Log.Error(err, "write an event record")
	}
}

func (r *Recorder) inventory(kind inventory, object any, action string) {
	if _, err := r.writeInventory(kind, object, action, ""); err != nil {
		r.Log.Error(err, "write an inventory record", "kind", kind.logType)
	}
}

// writeInventory writes the record of one object and says whether it wrote
// one. An upsert that changes no recorded field writes nothing. Neither does a
// snapshot entry of an object deleted since the snapshot started: it would
// follow the delete record and make the object current again. The lock is
// held while the record is written, so a delete and a snapshot entry of one
// object are written in the order they are decided.
func (r *Recorder) writeInventory(kind inventory, object any, action, snapshotID string) (bool, error) {
	value, ok := object.(client.Object)
	if !ok {
		return false, nil
	}
	record := kind.record(value)
	encoded, err := json.Marshal(record)
	if err != nil {
		return false, fmt.Errorf("encode a %s record: %w", kind.logType, err)
	}
	key := string(value.GetUID())
	r.mu.Lock()
	defer r.mu.Unlock()
	if action == ActionSnapshot && r.deleted[key] {
		return false, nil
	}
	if r.last == nil {
		r.last = map[string]string{}
	}
	unchanged := r.last[key] == string(encoded)
	if action == ActionDelete {
		delete(r.last, key)
		if r.deleted != nil {
			r.deleted[key] = true
		}
	} else {
		r.last[key] = string(encoded)
	}
	if action == ActionUpsert && unchanged {
		return false, nil
	}
	record.Action = action
	record.SnapshotID = snapshotID
	return true, r.Writer.Write(kind.logType, record)
}

func listOf(object client.Object) (client.ObjectList, error) {
	switch object.(type) {
	case *api.Agent:
		return &api.AgentList{}, nil
	case *api.Team:
		return &api.TeamList{}, nil
	case *api.AgentPolicy:
		return &api.AgentPolicyList{}, nil
	case *api.Sandbox:
		return &api.SandboxList{}, nil
	}
	return nil, fmt.Errorf("no list type for %T", object)
}

func items(list client.ObjectList) []client.Object {
	var result []client.Object
	switch typed := list.(type) {
	case *api.AgentList:
		for i := range typed.Items {
			result = append(result, &typed.Items[i])
		}
	case *api.TeamList:
		for i := range typed.Items {
			result = append(result, &typed.Items[i])
		}
	case *api.AgentPolicyList:
		for i := range typed.Items {
			result = append(result, &typed.Items[i])
		}
	case *api.SandboxList:
		for i := range typed.Items {
			result = append(result, &typed.Items[i])
		}
	}
	return result
}
