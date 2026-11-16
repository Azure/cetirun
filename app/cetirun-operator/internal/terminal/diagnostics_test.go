// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package terminal

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	api "github.com/Azure/CetiRun/app/cetirun-operator/api/v1alpha1"
	"github.com/Azure/CetiRun/app/internal/terminalpolicy"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientexec "k8s.io/client-go/util/exec"
	"k8s.io/streaming/pkg/httpstream"
)

func TestStartupFailureCategoriesAreSanitized(t *testing.T) {
	const sensitive = "https://api.example.com/pods/private?token=sk-dummy Authorization: Bearer sk-dummy"
	resource := schema.GroupResource{Resource: "pods/exec"}
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{"no error", nil, "none"},
		{"waiting", ErrWaiting, "target-waiting"},
		{"no shell", terminalpolicy.ErrNoShell, "no-shell"},
		{"local denial", ErrDenied, "authorization-denied"},
		{"forbidden", apierrors.NewForbidden(resource, sensitive, errors.New(sensitive)), "authorization-denied"},
		{"unauthorized", apierrors.NewUnauthorized(sensitive), "authorization-denied"},
		{"deadline", context.DeadlineExceeded, "timeout"},
		{"cancellation", context.Canceled, "canceled"},
		{"API timeout", apierrors.NewTimeoutError(sensitive, 1), "timeout"},
		{"server timeout", apierrors.NewServerTimeout(resource, sensitive, 1), "timeout"},
		{"connection", &url.Error{Op: "Get", URL: sensitive, Err: &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}}, "connection-failed"},
		{"TLS", &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, "tls-failure"},
		{"hostname", x509.HostnameError{Host: sensitive}, "tls-failure"},
		{"upgrade", &httpstream.UpgradeFailureError{Cause: errors.New(sensitive)}, "exec-upgrade-failed"},
		{"wrapped upgrade denial", &httpstream.UpgradeFailureError{Cause: apierrors.NewForbidden(resource, sensitive, errors.New(sensitive))}, "authorization-denied"},
		{"not found", apierrors.NewNotFound(resource, sensitive), "not-found"},
		{"throttled", apierrors.NewTooManyRequests(sensitive, 1), "api-throttled"},
		{"unavailable", apierrors.NewServiceUnavailable(sensitive), "api-unavailable"},
		{"exit", clientexec.CodeExitError{Err: errors.New(sensitive), Code: 1}, "shell-probe-failed"},
		{"unknown", errors.New(sensitive), "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.err
			if err != nil {
				err = fmt.Errorf("%s: %w", sensitive, err)
			}
			category := startupFailureCategory(err)
			if category != test.want {
				t.Fatalf("category = %q, want %q", category, test.want)
			}
			var output bytes.Buffer
			d := startupDiagnostics{logger: slog.New(slog.NewJSONHandler(&output, nil)), now: time.Now}
			d.observe(startupObservation{stage: "shell-probe", category: category}, false)
			for _, forbidden := range []string{"sk-dummy", "api.example.com", "Authorization", "private"} {
				if strings.Contains(output.String(), forbidden) {
					t.Fatalf("diagnostic exposed %q", forbidden)
				}
			}
		})
	}
}

type startupBackend struct {
	fakeBackend
	agentError error
}

func (b *startupBackend) Agent(context.Context) (*api.Agent, error) {
	return b.agent, b.agentError
}

func TestWatchShellReportsFailedStageAndPreservesDecisions(t *testing.T) {
	for _, test := range []struct {
		name         string
		agentError   error
		resolveError error
		probeError   error
		recheckError error
		changed      bool
		want         startupObservation
		fatal        bool
	}{
		{name: "agent read", agentError: context.DeadlineExceeded, want: startupObservation{stage: "agent-read", category: "timeout"}},
		{name: "agent denied", agentError: ErrDenied, want: startupObservation{stage: "agent-read", category: "authorization-denied"}, fatal: true},
		{name: "target waiting", resolveError: ErrWaiting, want: startupObservation{stage: "target-resolve", category: "target-waiting"}},
		{name: "target denied", resolveError: ErrDenied, want: startupObservation{stage: "target-resolve", category: "authorization-denied"}, fatal: true},
		{name: "probe transport", probeError: &httpstream.UpgradeFailureError{}, want: startupObservation{stage: "shell-probe", category: "exec-upgrade-failed"}},
		{name: "wrapped probe denial", probeError: &httpstream.UpgradeFailureError{Cause: ErrDenied}, want: startupObservation{stage: "shell-probe", category: "authorization-denied"}},
		{name: "probe denied", probeError: ErrDenied, want: startupObservation{stage: "shell-probe", category: "authorization-denied"}, fatal: true},
		{name: "no shell", probeError: terminalpolicy.ErrNoShell, want: startupObservation{stage: "shell-probe", category: "no-shell"}, fatal: true},
		{name: "recheck read", recheckError: context.DeadlineExceeded, want: startupObservation{stage: "target-recheck", category: "timeout"}},
		{name: "replacement", changed: true, want: startupObservation{stage: "target-recheck", category: "target-changed"}},
		{name: "both fail", probeError: terminalpolicy.ErrNoShell, recheckError: context.DeadlineExceeded,
			want: startupObservation{stage: "shell-probe", category: "no-shell", recheckCategory: "timeout"}},
		{name: "denial and replacement", probeError: ErrDenied, changed: true,
			want: startupObservation{stage: "shell-probe", category: "authorization-denied", recheckCategory: "target-changed"}},
		{name: "ready", want: startupObservation{stage: "ready", category: "none"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			backend := &startupBackend{agentError: test.agentError, fakeBackend: fakeBackend{agent: testAgent(),
				resolve: func(context.Context, Identity) (Snapshot, error) {
					calls++
					if calls == 1 {
						return Snapshot{PodUID: "pod", ContainerID: "container"}, test.resolveError
					}
					if test.changed {
						return Snapshot{PodUID: "replacement", ContainerID: "replacement"}, nil
					}
					return Snapshot{PodUID: "pod", ContainerID: "container"}, test.recheckError
				},
				detect: func(context.Context) (string, error) { return "sh", test.probeError },
			}}
			var ready bool
			observed := false
			err := watchShell(ctx, testConfig(), backend, time.Hour, func(value bool) { ready = value }, func(value startupObservation, fatal bool) {
				observed = true
				if value != test.want || fatal != test.fatal {
					t.Errorf("observation = %+v, fatal = %v; want %+v, %v", value, fatal, test.want, test.fatal)
				}
				cancel()
			})
			if !observed || ready != (test.want.stage == "ready") {
				t.Fatalf("observed = %v, ready = %v", observed, ready)
			}
			wantError := error(context.Canceled)
			if test.fatal {
				wantError = test.probeError
				if test.agentError != nil {
					wantError = test.agentError
				} else if test.resolveError != nil {
					wantError = test.resolveError
				}
			}
			if !errors.Is(err, wantError) {
				t.Fatalf("error = %v, want %v", err, wantError)
			}
		})
	}
}

func TestWatchShellCancellationDoesNotReportStartupFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	backend := &startupBackend{agentError: context.Canceled}
	err := watchShell(ctx, testConfig(), backend, time.Hour, func(bool) {}, func(startupObservation, bool) {
		t.Error("normal shutdown was reported as a startup failure")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
}

func TestWatchShellRecoversAndProbesEachReplacement(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	current := Snapshot{PodUID: "pod", ContainerID: "container"}
	probes := 0
	backend := &fakeBackend{agent: testAgent(),
		resolve: func(context.Context, Identity) (Snapshot, error) { return current, nil },
		detect: func(context.Context) (string, error) {
			probes++
			if probes == 1 {
				return "", context.DeadlineExceeded
			}
			return "sh", nil
		},
	}
	var readiness []bool
	var currentReady bool
	err := watchShell(ctx, testConfig(), backend, time.Millisecond, func(value bool) { currentReady = value }, func(value startupObservation, fatal bool) {
		// Only sample completed observations. Cancellation can race with the
		// retry timer and permit one final read before watchShell returns.
		readiness = append(readiness, currentReady)
		if fatal {
			t.Error("transient error became fatal")
		}
		switch len(readiness) {
		case 1:
			if value != (startupObservation{stage: "shell-probe", category: "timeout"}) {
				t.Fatalf("unexpected failure: %+v", value)
			}
		case 3:
			if probes != 2 {
				t.Error("unchanged successful target was probed again")
			}
			current.ContainerID = "restarted"
		case 4:
			current = Snapshot{PodUID: "replacement", ContainerID: "replacement"}
		case 5:
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || probes != 4 || !reflect.DeepEqual(readiness, []bool{false, true, true, true, true}) {
		t.Fatalf("error=%v probes=%d readiness=%v", err, probes, readiness)
	}
}

func diagnosticRecords(t *testing.T, output *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}

func TestStartupDiagnosticsBoundRetriesTransitionsAndRecovery(t *testing.T) {
	var output bytes.Buffer
	now := time.Unix(100, 0)
	d := startupDiagnostics{logger: slog.New(slog.NewJSONHandler(&output, nil)), now: func() time.Time { return now }}
	failure := startupObservation{stage: "agent-read", category: "timeout"}
	ready := startupObservation{stage: "ready", category: "none"}
	d.observe(failure, false)
	for second := 1; second < 30; second++ {
		now = time.Unix(100+int64(second), 0)
		value := failure
		if second%2 == 1 {
			value = ready
		}
		d.observe(value, false)
	}
	if got := len(diagnosticRecords(t, &output)); got != 1 {
		t.Fatalf("alternating states bypassed rate limit: %d records", got)
	}
	now = time.Unix(130, 0)
	d.observe(ready, false)
	records := diagnosticRecords(t, &output)
	if len(records) != 2 || records[1]["msg"] != "Terminal ready" || records[1]["failed_attempts"] != float64(15) ||
		records[1]["elapsed_seconds"] != float64(30) || records[1]["state_changes"] != float64(29) {
		t.Fatalf("missing coalesced recovery: %+v", records)
	}
	now = now.Add(time.Minute)
	d.observe(ready, false)
	if len(diagnosticRecords(t, &output)) != 2 {
		t.Fatal("stable readiness generated more logs")
	}
	d.observe(failure, false)
	now = now.Add(time.Second)
	d.observe(startupObservation{stage: "shell-probe", category: "no-shell"}, true)
	records = diagnosticRecords(t, &output)
	if len(records) != 4 || records[3]["fatal"] != true || records[3]["failed_attempts"] != float64(2) {
		t.Fatalf("fatal failure was suppressed: %+v", records)
	}
}

func TestStartupDiagnosticsSocketAndRepeatedFailure(t *testing.T) {
	var output bytes.Buffer
	now := time.Unix(100, 0)
	d := startupDiagnostics{logger: slog.New(slog.NewJSONHandler(&output, nil)), now: func() time.Time { return now }}
	socketReady, socketChecks := false, 0
	observe := d.withSocket(func() bool { socketChecks++; return socketReady })
	failure := startupObservation{stage: "shell-probe", category: "timeout", recheckCategory: "timeout"}
	observe(failure, false)
	if socketChecks != 0 {
		t.Fatal("socket replaced a shell dependency failure")
	}
	for i := 1; i <= 30; i++ {
		now = now.Add(time.Second)
		observe(failure, false)
	}
	records := diagnosticRecords(t, &output)
	if len(records) != 2 || records[1]["failed_attempts"] != float64(31) || records[1]["recheck_category"] != "timeout" {
		t.Fatalf("repeated failure summary = %+v", records)
	}
	now = now.Add(30 * time.Second)
	observe(startupObservation{stage: "ready", category: "none"}, false)
	records = diagnosticRecords(t, &output)
	if records[len(records)-1]["stage"] != "socket-ready" || records[len(records)-1]["category"] != "socket-unavailable" {
		t.Fatal("missing socket failure")
	}
	socketReady = true
	now = now.Add(30 * time.Second)
	observe(startupObservation{stage: "ready", category: "none"}, false)
	records = diagnosticRecords(t, &output)
	if records[len(records)-1]["msg"] != "Terminal ready" {
		t.Fatal("socket recovery was not reported")
	}
}
