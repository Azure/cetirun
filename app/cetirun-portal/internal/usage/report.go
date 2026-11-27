// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package usage reads the AI Gateway usage of this CetiRun cluster from the
// logs database of Azure Data Explorer, which the ADX-Mon collection fills.
// The Portal reads it with a workload identity that has the Viewer role on
// that database.
package usage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// State of the usage report.
type State string

const (
	// StateChecking means that the Portal has not yet confirmed that the
	// logs database holds no AI Gateway rows of another cluster.
	StateChecking State = "checking"
	// StateReady means that the check passed.
	StateReady State = "ready"
	// StateOff means that the logs database also holds AI Gateway rows of
	// another cluster. The report stays off until the Portal restarts.
	StateOff State = "off"
)

// Status is the state of the usage report and why.
type Status struct {
	State  State
	Reason string
}

// Querier runs a query with parameters on the logs database.
type Querier interface {
	Query(ctx context.Context, query string, parameters map[string]string) ([]Table, error)
}

// Settings name the logs database and this cluster in it.
type Settings struct {
	// Endpoint is the query URI of the Kusto cluster.
	Endpoint string
	// Database is the logs database, which holds the AI Gateway rows and the
	// usage views.
	Database string
	// Cluster is the name of this CetiRun cluster in the Cluster column, the
	// clusterName of the cetirun-observability chart.
	Cluster string
}

var (
	endpointPattern = regexp.MustCompile(`^https://[A-Za-z0-9.-]+(:[0-9]{1,5})?/?$`)
	databasePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,99}$`)
	clusterPattern  = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
)

// Validate checks the settings without calling Azure.
func (s Settings) Validate() error {
	switch {
	case !endpointPattern.MatchString(s.Endpoint):
		return errors.New("the ADX endpoint must be the query URI of the Kusto cluster, such as https://<name>.<region>.kusto.windows.net")
	case !databasePattern.MatchString(s.Database):
		return errors.New("the ADX database must be the name of the logs database: letters and digits")
	case !clusterPattern.MatchString(s.Cluster):
		return errors.New("the cluster name must be the DNS label that names this cluster in the Cluster column")
	}
	return nil
}

// Report is the usage report's access to the logs database.
type Report struct {
	settings Settings
	querier  Querier
	logger   *slog.Logger
	status   atomic.Pointer[Status]
	// retryDelay is the wait after the failed check with this number.
	retryDelay func(attempt int) time.Duration
}

// New returns a report that reads the logs database through the querier.
func New(settings Settings, querier Querier, logger *slog.Logger) *Report {
	if logger == nil {
		logger = slog.Default()
	}
	report := &Report{settings: settings, querier: querier, logger: logger, retryDelay: retryDelay}
	report.status.Store(&Status{State: StateChecking, Reason: "the Portal has not checked the logs database yet"})
	return report
}

// FromEnvironment returns a report that reads the logs database with the
// Pod's workload identity.
func FromEnvironment(settings Settings, getenv func(string) string, logger *slog.Logger) (*Report, error) {
	if err := settings.Validate(); err != nil {
		return nil, err
	}
	identity, err := WorkloadIdentityFromEnvironment(getenv)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: DefaultQueryTimeout + 30*time.Second}
	identity.HTTP = client
	settings.Endpoint = strings.TrimSuffix(settings.Endpoint, "/")
	kusto := &Kusto{Endpoint: settings.Endpoint, Database: settings.Database, Tokens: identity, HTTP: client}
	return New(settings, kusto, logger), nil
}

// Status returns the current state of the report.
func (r *Report) Status() Status {
	return *r.status.Load()
}

// otherClusterQuery finds an AI Gateway row of another cluster. Kusto grants
// a role on a whole database, so the Portal's identity can read every row of
// it: the report reads only a database that holds this cluster's rows alone.
const otherClusterQuery = `declare query_parameters(cluster: string);
Gateway
| where Cluster != cluster
| take 1
| project Cluster`

// Check looks for AI Gateway rows of another cluster in the logs database.
// It returns an error only when the database cannot be read.
func (r *Report) Check(ctx context.Context) error {
	tables, err := r.querier.Query(ctx, otherClusterQuery, map[string]string{"cluster": r.settings.Cluster})
	if err == nil && (len(tables) != 1 || len(tables[0].Columns) != 1) {
		err = errors.New("the check of the logs database returned an unexpected result")
	}
	if err != nil {
		r.status.Store(&Status{State: StateChecking, Reason: err.Error()})
		return err
	}
	if len(tables[0].Rows) > 0 {
		other := fmt.Sprint(tables[0].Rows[0][0])
		r.status.Store(&Status{State: StateOff, Reason: fmt.Sprintf(
			"the logs database %s also holds AI Gateway rows of the cluster %q, and the Portal's identity can read every row of it; remove the identity's Viewer role on that database, and use the usage report of the cluster %q only with a logs database to which no other cluster writes",
			r.settings.Database, other, r.settings.Cluster)})
		return nil
	}
	r.status.Store(&Status{State: StateReady})
	return nil
}

// Start checks the logs database in the background. A check that cannot
// read the database is repeated after a growing delay until it succeeds or
// the context ends.
func (r *Report) Start(ctx context.Context) {
	go func() {
		for attempt := 0; ; attempt++ {
			err := r.Check(ctx)
			if err == nil {
				r.logResult()
				return
			}
			if ctx.Err() != nil {
				return
			}
			delay := r.retryDelay(attempt)
			r.logger.Warn("Usage report: cannot check the logs database yet", "error", err, "retryIn", delay.String())
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}

func (r *Report) logResult() {
	status := r.Status()
	switch status.State {
	case StateReady:
		r.logger.Info("Usage report reads the AI Gateway usage from Azure Data Explorer",
			"endpoint", r.settings.Endpoint, "database", r.settings.Database, "cluster", r.settings.Cluster)
	case StateOff:
		r.logger.Error("Usage report is off", "reason", status.Reason)
	}
}

// retryDelay doubles from 30 seconds to at most 10 minutes.
func retryDelay(attempt int) time.Duration {
	delay := 30 * time.Second
	for i := 0; i < attempt && delay < 10*time.Minute; i++ {
		delay *= 2
	}
	return min(delay, 10*time.Minute)
}
