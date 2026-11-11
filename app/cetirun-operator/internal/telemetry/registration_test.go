// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package telemetry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// stubCache answers GetInformer: kinds in installed get an informer, other
// kinds a no-match error, unless failure is set.
type stubCache struct {
	cache.Cache
	mu        sync.Mutex
	installed map[string]bool
	failure   error
	informers map[string]*stubInformer
}

type stubInformer struct {
	cache.Informer
	handlers int
}

func (i *stubInformer) AddEventHandler(toolscache.ResourceEventHandler) (toolscache.ResourceEventHandlerRegistration, error) {
	i.handlers++
	return nil, nil
}

func (c *stubCache) GetInformer(_ context.Context, object client.Object, _ ...cache.InformerGetOption) (cache.Informer, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failure != nil {
		return nil, c.failure
	}
	name := kindName(object)
	if !c.installed[name] {
		return nil, &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "cetirun.microsoft.com", Kind: name}}
	}
	if c.informers == nil {
		c.informers = map[string]*stubInformer{}
	}
	if c.informers[name] == nil {
		c.informers[name] = &stubInformer{}
	}
	return c.informers[name], nil
}

// WaitForCacheSync and List: the cache holds no objects.
func (c *stubCache) WaitForCacheSync(context.Context) bool { return true }

func (c *stubCache) List(context.Context, client.ObjectList, ...client.ListOption) error { return nil }

func (c *stubCache) install(names ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, name := range names {
		c.installed[name] = true
	}
}

func kindName(object client.Object) string {
	for _, kind := range kinds {
		if fmt.Sprintf("%T", kind.object) == fmt.Sprintf("%T", object) {
			return kind.logType
		}
	}
	return "event"
}

func TestRegistrationWaitsForMissingResources(t *testing.T) {
	c := &stubCache{installed: map[string]bool{}}
	r := &Recorder{Writer: NewWriter(&bytes.Buffer{}), Events: EventsNone, Log: logr.Discard()}
	if err := r.Register(context.Background(), c); err != nil {
		t.Fatalf("Register without the CetiRun resources: %v", err)
	}
	for _, kind := range kinds {
		if r.isRegistered(kind.logType) {
			t.Fatalf("%s registered without its resource", kind.logType)
		}
	}
	// A snapshot reads nothing while no kind is registered.
	if err := r.Snapshot(context.Background(), nil); err != nil {
		t.Fatalf("Snapshot without the CetiRun resources: %v", err)
	}

	c.install(LogTypeAgent, LogTypeTeam, LogTypeAgentPolicy, LogTypeSandbox)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.RunRegistration(ctx, c, 10*time.Millisecond); err != nil {
		t.Fatalf("RunRegistration: %v", err)
	}
	for _, kind := range kinds {
		if !r.isRegistered(kind.logType) {
			t.Fatalf("%s not registered after its resource appeared", kind.logType)
		}
	}
	// A second pass adds no second handler.
	if _, _, err := r.registerInventory(ctx, c); err != nil {
		t.Fatal(err)
	}
	for name, informer := range c.informers {
		if informer.handlers != 1 {
			t.Fatalf("%s has %d handlers, want 1", name, informer.handlers)
		}
	}
}

func TestRegistrationStopsOnOtherErrors(t *testing.T) {
	c := &stubCache{failure: errors.New("forbidden")}
	r := &Recorder{Writer: NewWriter(&bytes.Buffer{}), Events: EventsNone, Log: logr.Discard()}
	if err := r.Register(context.Background(), c); err == nil {
		t.Fatal("Register hid an error that is not a missing resource")
	}
}

func TestResourcesInstalledLaterGetASnapshotAtOnce(t *testing.T) {
	c := &stubCache{installed: map[string]bool{}}
	var out bytes.Buffer
	r := &Recorder{Writer: NewWriter(&out), Events: EventsNone, Log: logr.Discard()}
	if err := r.Register(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.RunRegistration(ctx, c, 10*time.Millisecond) }()
	time.Sleep(50 * time.Millisecond)
	if strings.Contains(syncedOutput(r, &out), LogTypeSnapshot) {
		t.Fatal("a snapshot before the resources exist")
	}
	c.install(LogTypeAgent, LogTypeTeam, LogTypeAgentPolicy, LogTypeSandbox)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, kind := range kinds {
		if !strings.Contains(out.String(), `"kind":"`+kind.logType+`"`) {
			t.Fatalf("no completed %s snapshot once the resources appeared", kind.logType)
		}
	}
}

func TestResourcesInstalledBeforeTheFirstRetryGetASnapshotAtOnce(t *testing.T) {
	c := &stubCache{installed: map[string]bool{}}
	var out bytes.Buffer
	r := &Recorder{Writer: NewWriter(&out), Events: EventsNone, Log: logr.Discard()}
	if err := r.Register(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	// The snapshot at start finds no registered kind and writes nothing.
	if err := r.Snapshot(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	// The resources appear before the first retry, which registers every
	// kind at once without having reported any as missing.
	c.install(LogTypeAgent, LogTypeTeam, LogTypeAgentPolicy, LogTypeSandbox)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.RunRegistration(ctx, c, time.Hour); err != nil {
		t.Fatal(err)
	}
	for _, kind := range kinds {
		if !strings.Contains(out.String(), `"kind":"`+kind.logType+`"`) {
			t.Fatalf("no completed %s snapshot after the first retry registered it", kind.logType)
		}
	}
}

func TestKindsRegisteredAtStartGetNoSnapshotFromTheRetry(t *testing.T) {
	c := &stubCache{installed: map[string]bool{}}
	c.install(LogTypeAgent, LogTypeTeam, LogTypeAgentPolicy, LogTypeSandbox)
	var out bytes.Buffer
	r := &Recorder{Writer: NewWriter(&out), Events: EventsNone, Log: logr.Discard()}
	if err := r.Register(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.RunRegistration(ctx, c, time.Hour); err != nil {
		t.Fatal(err)
	}
	// The snapshot at start covers them; the retry adds none of its own.
	if strings.Contains(out.String(), LogTypeSnapshot) {
		t.Fatalf("the retry wrote a snapshot for kinds registered at start: %s", out.String())
	}
}
