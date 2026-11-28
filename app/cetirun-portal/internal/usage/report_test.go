// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package usage

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeQuerier struct {
	mu         sync.Mutex
	queries    []string
	parameters []map[string]string
	answers    []func() ([]Table, error)
}

func (f *fakeQuerier) Query(_ context.Context, query string, parameters map[string]string) ([]Table, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, query)
	f.parameters = append(f.parameters, parameters)
	answer := f.answers[0]
	if len(f.answers) > 1 {
		f.answers = f.answers[1:]
	}
	return answer()
}

func clusters(names ...string) func() ([]Table, error) {
	return func() ([]Table, error) {
		table := Table{Columns: []Column{{Name: "Cluster", Type: "string"}}, Rows: [][]any{}}
		for _, name := range names {
			table.Rows = append(table.Rows, []any{name})
		}
		return []Table{table}, nil
	}
}

var settings = Settings{Endpoint: "https://adx.example.com", Database: "CetiRunLogs", Cluster: "sm"}

func TestCheckTurnsTheReportOffForRowsOfAnotherCluster(t *testing.T) {
	querier := &fakeQuerier{answers: []func() ([]Table, error){clusters("e2e")}}
	report := New(settings, querier, nil)
	if report.Status().State != StateChecking {
		t.Fatalf("initial state %+v", report.Status())
	}
	if err := report.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := report.Status()
	if status.State != StateOff || !strings.Contains(status.Reason, `rows of the cluster "e2e"`) ||
		!strings.Contains(status.Reason, "remove the identity's Viewer role") || !strings.Contains(status.Reason, `the cluster "sm" only`) {
		t.Fatalf("status %+v", status)
	}
	if !reflect.DeepEqual(querier.parameters, []map[string]string{{"cluster": "sm"}}) {
		t.Fatalf("parameters %v", querier.parameters)
	}
	if !strings.HasPrefix(querier.queries[0], "declare query_parameters(cluster: string);") || strings.Contains(querier.queries[0], `"sm"`) {
		t.Fatalf("the cluster is not a query parameter: %q", querier.queries[0])
	}
	// The views keep the rows of another cluster longer than the Gateway
	// table, and only the coverage view keeps the heartbeats of an AI Gateway
	// without calls, so the check reads them too, and leaves out a missing one.
	for _, source := range []string{"union isfuzzy=true", "(Gateway | where Cluster != cluster | take 1",
		"(GatewayDailyUsage | where Cluster != cluster | take 1", "(GatewayDailyUsageV2 | where Cluster != cluster | take 1",
		"(GatewayCoverage | where Cluster != cluster | take 1"} {
		if !strings.Contains(querier.queries[0], source) {
			t.Fatalf("the check lacks %q: %q", source, querier.queries[0])
		}
	}
}

func TestCheckIsReadyForTheRowsOfOneCluster(t *testing.T) {
	report := New(settings, &fakeQuerier{answers: []func() ([]Table, error){clusters()}}, nil)
	if err := report.Check(context.Background()); err != nil || report.Status() != (Status{State: StateReady}) {
		t.Fatalf("status %+v, %v", report.Status(), err)
	}
}

func TestCheckKeepsCheckingWhenTheDatabaseCannotBeRead(t *testing.T) {
	for name, answer := range map[string]func() ([]Table, error){
		"error":        func() ([]Table, error) { return nil, &QueryError{Status: 403, Code: "Forbidden"} },
		"no table":     func() ([]Table, error) { return nil, nil },
		"other result": func() ([]Table, error) { return []Table{{Columns: []Column{{Name: "a"}, {Name: "b"}}}}, nil },
	} {
		t.Run(name, func(t *testing.T) {
			report := New(settings, &fakeQuerier{answers: []func() ([]Table, error){answer}}, nil)
			if err := report.Check(context.Background()); err == nil || report.Status().State != StateChecking || report.Status().Reason != err.Error() {
				t.Fatalf("status %+v, %v", report.Status(), err)
			}
		})
	}
}

type syncBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buffer.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buffer.String()
}

func TestStartRetriesUntilTheDatabaseAnswers(t *testing.T) {
	failure := func() ([]Table, error) { return nil, errors.New("the Kusto cluster is unavailable") }
	querier := &fakeQuerier{answers: []func() ([]Table, error){failure, failure, clusters()}}
	var logs syncBuffer
	report := New(settings, querier, slog.New(slog.NewTextHandler(&logs, nil)))
	var delays []int
	report.retryDelay = func(attempt int) time.Duration {
		delays = append(delays, attempt)
		return time.Millisecond
	}
	report.Start(context.Background())
	deadline := time.Now().Add(5 * time.Second)
	for report.Status().State != StateReady {
		if time.Now().After(deadline) {
			t.Fatalf("the report did not become ready: %+v", report.Status())
		}
		time.Sleep(time.Millisecond)
	}
	for !strings.Contains(logs.String(), "Usage report reads the AI Gateway usage") {
		if time.Now().After(deadline) {
			t.Fatalf("no ready log: %s", logs.String())
		}
		time.Sleep(time.Millisecond)
	}
	if strings.Count(logs.String(), "cannot check the logs database yet") != 2 || !reflect.DeepEqual(delays, []int{0, 1}) {
		t.Fatalf("retries %v, logs %s", delays, logs.String())
	}
}

func TestStartEndsWithItsContext(t *testing.T) {
	failure := func() ([]Table, error) { return nil, errors.New("unavailable") }
	querier := &fakeQuerier{answers: []func() ([]Table, error){failure}}
	report := New(settings, querier, slog.New(slog.NewTextHandler(&syncBuffer{}, nil)))
	report.retryDelay = func(int) time.Duration { return time.Hour }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	report.Start(ctx)
	go func() {
		for {
			querier.mu.Lock()
			asked := len(querier.queries)
			querier.mu.Unlock()
			if asked > 0 {
				close(done)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	<-done
	cancel()
	time.Sleep(20 * time.Millisecond)
	querier.mu.Lock()
	defer querier.mu.Unlock()
	if len(querier.queries) != 1 {
		t.Fatalf("checks after the context ended: %d", len(querier.queries))
	}
}

func TestRetryDelayDoublesUpToTenMinutes(t *testing.T) {
	got := []time.Duration{retryDelay(0), retryDelay(1), retryDelay(4), retryDelay(5), retryDelay(50)}
	want := []time.Duration{30 * time.Second, time.Minute, 8 * time.Minute, 10 * time.Minute, 10 * time.Minute}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("delays %v, want %v", got, want)
	}
}

func TestSettingsAndEnvironment(t *testing.T) {
	for name, invalid := range map[string]Settings{
		"endpoint with a path": {Endpoint: "https://adx.example.com/v2", Database: "CetiRunLogs", Cluster: "sm"},
		"http endpoint":        {Endpoint: "http://adx.example.com", Database: "CetiRunLogs", Cluster: "sm"},
		"database":             {Endpoint: "https://adx.example.com", Database: "Ceti-Run", Cluster: "sm"},
		"cluster":              {Endpoint: "https://adx.example.com", Database: "CetiRunLogs", Cluster: "SM"},
	} {
		if err := invalid.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
		if _, err := FromEnvironment(invalid, identityEnvironment(nil), nil); err == nil {
			t.Errorf("%s was accepted from the environment", name)
		}
	}
	if _, err := FromEnvironment(settings, identityEnvironment(nil), nil); err == nil || !strings.Contains(err.Error(), "AZURE_CLIENT_ID") {
		t.Fatalf("missing workload identity: %v", err)
	}
	report, err := FromEnvironment(Settings{Endpoint: "https://adx.example.com/", Database: "CetiRunLogs", Cluster: "sm"}, identityEnvironment(map[string]string{
		"AZURE_CLIENT_ID": "client", "AZURE_TENANT_ID": "tenant", "AZURE_FEDERATED_TOKEN_FILE": "/token",
	}), nil)
	if err != nil || report.Status().State != StateChecking {
		t.Fatalf("report from the environment: %v", err)
	}
	kusto, ok := report.querier.(*Kusto)
	if !ok || kusto.Endpoint != "https://adx.example.com" || kusto.Database != "CetiRunLogs" {
		t.Fatalf("Kusto client %+v", report.querier)
	}
}
