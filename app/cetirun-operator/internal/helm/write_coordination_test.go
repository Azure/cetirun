// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package helm

import (
	"context"
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
)

func TestMaintenanceQuiescenceNeedsItsCompletedWriter(t *testing.T) {
	c := &Client{}
	reads := 0
	current := &release.Release{Name: "example", Namespace: "test", Version: 7, Info: &release.Info{Status: release.StatusPendingInstall}}
	read := func(context.Context, string, string) (*release.Release, error) { reads++; return current, nil }
	if c.maintenanceQuiescent(context.Background(), "test", "example", read) == nil || reads != 0 {
		t.Fatal("an unobserved writer was accepted")
	}
	state, done, err := c.beginReleaseWrite("test", "example")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.beginReleaseWrite("test", "example"); err == nil {
		t.Fatal("two writers acquired the same release")
	}
	if c.maintenanceQuiescent(context.Background(), "test", "example", read) == nil || reads != 0 {
		t.Fatal("running SDK writer was treated as complete")
	}
	cfg := &action.Configuration{Releases: storage.Init(driver.NewMemory())}
	trackReleaseWrites(cfg, state)
	if err := cfg.Releases.Create(current); err != nil {
		t.Fatal(err)
	}
	if c.maintenanceQuiescent(context.Background(), "test", "example", read) == nil {
		t.Fatal("storage publication alone authorized handoff before SDK return")
	}
	done()
	if err := c.maintenanceQuiescent(context.Background(), "test", "example", read); err != nil {
		t.Fatal(err)
	}
	current.Version = 8
	if c.maintenanceQuiescent(context.Background(), "test", "example", read) == nil {
		t.Fatal("a replacement revision inherited the completion proof")
	}
	other := &Client{}
	if other.maintenanceQuiescent(context.Background(), "test", "example", read) == nil {
		t.Fatal("a new process adopted an unknown prior writer")
	}
}

func TestMaintenanceQuiescenceKeepsReleasesIndependent(t *testing.T) {
	c := &Client{}
	_, first, err := c.beginReleaseWrite("one", "example")
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	_, second, err := c.beginReleaseWrite("two", "example")
	if err != nil {
		t.Fatal("unrelated namespace writer was blocked")
	}
	defer second()
	_, third, err := c.beginReleaseWrite("one", "another")
	if err != nil {
		t.Fatal("unrelated release writer was blocked")
	}
	defer third()
}
