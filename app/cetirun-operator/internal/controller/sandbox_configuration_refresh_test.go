// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

import (
	"errors"
	"testing"
	"time"

	"github.com/Azure/CetiRun/app/cetirun-operator/internal/sandboxpolicy"
)

func TestConfigurationRefreshJitterAndRemainingFreshness(t *testing.T) {
	observed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		maxAge   time.Duration
		age      time.Duration
		lifetime time.Duration
	}{
		{"default", 0, 0, 30 * time.Second},
		{"explicit", time.Minute, 0, time.Minute},
		{"near expiry", 30 * time.Second, 29 * time.Second, time.Second},
		{"minimum", time.Nanosecond, 0, time.Nanosecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := make(map[time.Duration]bool)
			for range 32 {
				delay := configurationRefresh(tc.maxAge, observed.Add(tc.age), observed)
				lower, upper := max(time.Nanosecond, tc.lifetime/2), max(time.Nanosecond, tc.lifetime/2+tc.lifetime/8)
				if delay < lower || delay > upper || delay > tc.lifetime {
					t.Fatalf("refresh %s outside [%s,%s], lifetime %s", delay, lower, upper, tc.lifetime)
				}
				values[delay] = true
			}
			if tc.lifetime > time.Second && len(values) == 1 {
				t.Fatal("refresh intervals remained synchronized")
			}
		})
	}
}

func TestConfigurationSnapshotFreshnessBoundaries(t *testing.T) {
	observed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, age := range []time.Duration{-time.Nanosecond, 0, 30*time.Second - time.Nanosecond, 30 * time.Second, time.Minute} {
		err := configurationSnapshotTime(observed, observed.Add(age), 30*time.Second)
		wantUnavailable := age < 0 || age >= 30*time.Second
		if errors.Is(err, sandboxpolicy.ErrUnavailable) != wantUnavailable || !wantUnavailable && err != nil {
			t.Fatalf("freshness age %s: %v", age, err)
		}
	}
	if err := configurationSnapshotTime(time.Time{}, observed, 0); !errors.Is(err, sandboxpolicy.ErrUnavailable) {
		t.Fatal("zero observation must not become fresh", err)
	}
}
