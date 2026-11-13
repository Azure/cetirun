// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package terminal

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"net"
	"time"

	"github.com/Azure/CetiRun/app/internal/terminalpolicy"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/httpstream"
	clientexec "k8s.io/client-go/util/exec"
)

// Only fixed stages and categories enter diagnostics. Never retain or log an
// underlying error: API and transport errors can include credentials and URLs.
type startupObservation struct {
	stage           string
	category        string
	recheckCategory string
}

func startupFailureCategory(err error) string {
	if err == nil {
		return "none"
	}
	// client-go's upgrade wrapper exposes Cause but does not implement Unwrap.
	// Inspect the typed cause for diagnostics only; retry/exit policy is unchanged.
	upgradeFailure := httpstream.IsUpgradeFailure(err)
	var upgrade *httpstream.UpgradeFailureError
	if errors.As(err, &upgrade) && upgrade.Cause != nil {
		err = upgrade.Cause
	}
	if errors.Is(err, terminalpolicy.ErrNoShell) {
		return "no-shell"
	}
	if denied(err) {
		return "authorization-denied"
	}
	if errors.Is(err, ErrWaiting) {
		return "target-waiting"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var network net.Error
	if errors.Is(err, context.DeadlineExceeded) || apierrors.IsTimeout(err) ||
		apierrors.IsServerTimeout(err) || (errors.As(err, &network) && network.Timeout()) {
		return "timeout"
	}
	var verification *tls.CertificateVerificationError
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var certificate x509.CertificateInvalidError
	var record tls.RecordHeaderError
	if errors.As(err, &verification) || errors.As(err, &authority) || errors.As(err, &hostname) ||
		errors.As(err, &certificate) || errors.As(err, &record) {
		return "tls-failure"
	}
	if errors.As(err, &network) {
		return "connection-failed"
	}
	if apierrors.IsNotFound(err) {
		return "not-found"
	}
	if apierrors.IsTooManyRequests(err) {
		return "api-throttled"
	}
	if apierrors.IsServiceUnavailable(err) {
		return "api-unavailable"
	}
	if upgradeFailure || httpstream.IsHTTPSProxyError(err) {
		return "exec-upgrade-failed"
	}
	var exit clientexec.ExitError
	if errors.As(err, &exit) {
		return "shell-probe-failed"
	}
	return "unknown"
}

const startupLogInterval = 30 * time.Second

// A single watchShell goroutine owns this state. Health handlers do not read or
// update it. Even alternating failures and recoveries share one rate limit.
type startupDiagnostics struct {
	logger         *slog.Logger
	now            func() time.Time
	observed       bool
	last           startupObservation
	lastLog        time.Time
	pending        bool
	changes        int
	failedAttempts int
	waitingSince   time.Time
}

func (d *startupDiagnostics) observe(value startupObservation, fatal bool) {
	now := d.now()
	first := !d.observed
	if first || value != d.last {
		d.pending = true
		d.changes++
	}
	d.observed, d.last = true, value
	ready := value.stage == "ready"
	if !ready {
		if d.failedAttempts == 0 {
			d.waitingSince = now
		}
		d.failedAttempts++
		d.pending = true
	}
	if !fatal && (!d.pending || (!first && now.Sub(d.lastLog) < startupLogInterval)) {
		return
	}
	elapsed := time.Duration(0)
	if d.failedAttempts > 0 {
		elapsed = now.Sub(d.waitingSince)
	}
	attributes := []any{
		"stage", value.stage, "category", value.category,
		"failed_attempts", d.failedAttempts, "elapsed_seconds", int64(elapsed.Seconds()),
		"state_changes", d.changes, "fatal", fatal,
	}
	if value.recheckCategory != "" {
		attributes = append(attributes, "recheck_category", value.recheckCategory)
	}
	if ready {
		d.logger.Info("Terminal ready", attributes...)
		d.failedAttempts = 0
	} else {
		d.logger.Warn("Terminal startup dependency unavailable", attributes...)
	}
	d.lastLog, d.pending, d.changes = now, false, 0
}

func (d *startupDiagnostics) withSocket(socketReady func() bool) func(startupObservation, bool) {
	return func(value startupObservation, fatal bool) {
		if value.stage == "ready" && !socketReady() {
			value = startupObservation{stage: "socket-ready", category: "socket-unavailable"}
		}
		d.observe(value, fatal)
	}
}
