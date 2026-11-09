// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
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
	for _, kind := range kinds {
		informer, err := c.GetInformer(ctx, kind.object)
		if err != nil {
			return fmt.Errorf("%s informer: %w", kind.logType, err)
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
			return fmt.Errorf("%s handler: %w", kind.logType, err)
		}
	}
	return nil
}

// Snapshot writes every inventory object again, so that the latest state of
// each one stays within the retention of the logs.
func (r *Recorder) Snapshot(ctx context.Context, reader client.Reader) error {
	for _, kind := range kinds {
		list, err := listOf(kind.object)
		if err != nil {
			return err
		}
		if err := reader.List(ctx, list); err != nil {
			return fmt.Errorf("list %s: %w", kind.logType, err)
		}
		for _, object := range items(list) {
			r.inventory(kind, object, ActionSnapshot)
		}
	}
	return nil
}

// RunSnapshots writes a snapshot at each interval until the context ends.
func (r *Recorder) RunSnapshots(ctx context.Context, reader client.Reader, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.Snapshot(ctx, reader); err != nil {
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
	value, ok := object.(client.Object)
	if !ok {
		return
	}
	record := kind.record(value)
	encoded, err := json.Marshal(record)
	if err != nil {
		r.Log.Error(err, "encode an inventory record", "kind", kind.logType)
		return
	}
	key := string(value.GetUID())
	r.mu.Lock()
	if r.last == nil {
		r.last = map[string]string{}
	}
	unchanged := r.last[key] == string(encoded)
	if action == ActionDelete {
		delete(r.last, key)
	} else {
		r.last[key] = string(encoded)
	}
	r.mu.Unlock()
	if action == ActionUpsert && unchanged {
		return
	}
	record.Action = action
	if err := r.Writer.Write(kind.logType, record); err != nil {
		r.Log.Error(err, "write an inventory record", "kind", kind.logType)
	}
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
