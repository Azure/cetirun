// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
)

type releaseWriteState struct {
	mu                sync.Mutex
	seen              atomic.Int64
	completedRevision int
}

func (c *Client) releaseWrite(namespace, name string) *releaseWriteState {
	c.writesMu.Lock()
	defer c.writesMu.Unlock()
	if c.writes == nil {
		c.writes = map[string]*releaseWriteState{}
	}
	key := namespace + "/" + name
	if c.writes[key] == nil {
		c.writes[key] = &releaseWriteState{}
	}
	return c.writes[key]
}

func (c *Client) beginReleaseWrite(namespace, name string) (*releaseWriteState, func(), error) {
	state := c.releaseWrite(namespace, name)
	if !state.mu.TryLock() {
		return nil, nil, fmt.Errorf("a Helm writer for this release is still active")
	}
	state.seen.Store(0)
	return state, func() {
		if revision := state.seen.Load(); revision > 0 {
			state.completedRevision = int(revision)
		}
		state.mu.Unlock()
	}, nil
}

func trackReleaseWrites(cfg *action.Configuration, state *releaseWriteState) {
	cfg.Releases = storage.Init(&releaseWriteDriver{Driver: cfg.Releases.Driver, state: state})
}

// MaintenanceQuiescent proves that this process's SDK writer returned after
// creating the current revision. An unknown pending write from another process
// is deliberately not adopted as complete. Persisted Held checkpoints preserve
// an already-confirmed handoff across a controller restart.
func (c *Client) MaintenanceQuiescent(ctx context.Context, namespace, name string) error {
	return c.maintenanceQuiescent(ctx, namespace, name, c.CurrentRelease)
}

func (c *Client) maintenanceQuiescent(ctx context.Context, namespace, name string, read func(context.Context, string, string) (*release.Release, error)) error {
	state := c.releaseWrite(namespace, name)
	if !state.mu.TryLock() {
		return fmt.Errorf("the previous Helm writer is still active")
	}
	defer state.mu.Unlock()
	if state.completedRevision < 1 {
		return fmt.Errorf("the current revision has no completed SDK writer in this process")
	}
	current, err := read(ctx, namespace, name)
	if err != nil {
		return err
	}
	if current == nil || current.Version != state.completedRevision {
		return fmt.Errorf("the Helm revision changed after its writer completed")
	}
	return nil
}

type releaseWriteDriver struct {
	driver.Driver
	state *releaseWriteState
}

func (d *releaseWriteDriver) Create(key string, rel *release.Release) error {
	if rel == nil || rel.Version < 1 {
		return fmt.Errorf("invalid Helm writer revision")
	}
	d.state.seen.Store(int64(rel.Version))
	return d.Driver.Create(key, rel)
}
