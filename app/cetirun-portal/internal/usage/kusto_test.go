// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package usage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type staticToken struct {
	token string
	err   error
	asked []string
}

func (s *staticToken) Token(_ context.Context, resource string) (string, error) {
	s.asked = append(s.asked, resource)
	return s.token, s.err
}

// Frames of a v2 response, as a Kusto cluster returned them for a batch of
// two statements, with the data of the test instead of the probe's.
const twoTables = `[
{"FrameType":"DataSetHeader","IsProgressive":false,"Version":"v2.0","IsFragmented":false,"ErrorReportingPlacement":"InData"},
{"FrameType":"DataTable","TableId":0,"TableKind":"QueryProperties","TableName":"@ExtendedProperties","Columns":[{"ColumnName":"TableId","ColumnType":"int"},{"ColumnName":"Key","ColumnType":"string"},{"ColumnName":"Value","ColumnType":"dynamic"}],"Rows":[[1,"Visualization",{"Visualization":null}]]},
{"FrameType":"DataTable","TableId":1,"TableKind":"PrimaryResult","TableName":"PrimaryResult","Columns":[{"ColumnName":"Spend","ColumnType":"decimal"},{"ColumnName":"Calls","ColumnType":"long"},{"ColumnName":"Share","ColumnType":"real"},{"ColumnName":"Day","ColumnType":"datetime"},{"ColumnName":"Copilot","ColumnType":"bool"}],"Rows":[["1.25",7,0.5,"2026-11-26T00:00:00Z",true]]},
{"FrameType":"DataTable","TableId":2,"TableKind":"PrimaryResult","TableName":"PrimaryResult","Columns":[{"ColumnName":"Groups","ColumnType":"long"}],"Rows":[[3]]},
{"FrameType":"DataTable","TableId":3,"TableKind":"QueryCompletionInformation","TableName":"QueryCompletionInformation","Columns":[{"ColumnName":"Timestamp","ColumnType":"datetime"}],"Rows":[["2026-11-27T00:00:00Z"]]},
{"FrameType":"DataSetCompletion","HasErrors":false,"Cancelled":false}
]`

func kustoServer(t *testing.T, handler http.HandlerFunc) (*Kusto, *staticToken) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	tokens := &staticToken{token: "access-token"}
	return &Kusto{Endpoint: server.URL, Database: "CetiRunLogs", Tokens: tokens, HTTP: server.Client()}, tokens
}

func TestQuerySendsParametersAndReadsThePrimaryResults(t *testing.T) {
	query := "declare query_parameters(cluster: string); Gateway | where Cluster == cluster | take 1"
	client, tokens := kustoServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v2/rest/query" {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer access-token" || r.Header.Get("x-ms-app") != "cetirun-portal" {
			t.Errorf("headers %v", r.Header)
		}
		data, _ := io.ReadAll(r.Body)
		var body struct {
			Database   string `json:"db"`
			Query      string `json:"csl"`
			Properties string `json:"properties"`
		}
		if err := json.Unmarshal(data, &body); err != nil {
			t.Fatal(err)
		}
		var properties struct {
			Options    map[string]any    `json:"Options"`
			Parameters map[string]string `json:"Parameters"`
		}
		if err := json.Unmarshal([]byte(body.Properties), &properties); err != nil {
			t.Fatalf("properties are not a JSON string: %v", err)
		}
		if body.Database != "CetiRunLogs" || body.Query != query {
			t.Errorf("database %q, query %q", body.Database, body.Query)
		}
		if !reflect.DeepEqual(properties.Parameters, map[string]string{"cluster": `sm" | take 1`}) {
			t.Errorf("parameters %v", properties.Parameters)
		}
		if properties.Options["servertimeout"] != "00:00:30" || properties.Options["request_readonly"] != true {
			t.Errorf("options %v", properties.Options)
		}
		_, _ = w.Write([]byte(twoTables))
	})
	tables, err := client.Query(context.Background(), query, map[string]string{"cluster": `sm" | take 1`})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tokens.asked, []string{client.Endpoint}) {
		t.Fatalf("token resources %v", tokens.asked)
	}
	if len(tables) != 2 || len(tables[0].Rows) != 1 || len(tables[1].Rows) != 1 {
		t.Fatalf("tables %+v", tables)
	}
	want := []any{"1.25", json.Number("7"), json.Number("0.5"), "2026-11-26T00:00:00Z", true}
	if !reflect.DeepEqual(tables[0].Rows[0], want) || tables[0].Columns[0] != (Column{Name: "Spend", Type: "decimal"}) {
		t.Fatalf("first table %+v", tables[0])
	}
	if !reflect.DeepEqual(tables[1].Rows[0], []any{json.Number("3")}) {
		t.Fatalf("second table %+v", tables[1])
	}
}

func TestQueryReportsTheErrorOfARefusedRequest(t *testing.T) {
	client, _ := kustoServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"General_BadRequest","message":"Request is invalid and cannot be executed.","@message":"Semantic error: SEM0100: 'take' operator: Failed to resolve table or column expression named 'Gateway'"}}`))
	})
	_, err := client.Query(context.Background(), "Gateway | take 1", nil)
	var queryError *QueryError
	if !errors.As(err, &queryError) || queryError.Status != http.StatusBadRequest || queryError.Code != "General_BadRequest" || !strings.Contains(queryError.Message, "SEM0100") {
		t.Fatalf("refused request: %#v", err)
	}
}

func TestQueryReportsErrorsDuringTheQuery(t *testing.T) {
	for name, response := range map[string]string{
		"in the data":   `[{"FrameType":"DataSetHeader"},{"FrameType":"DataTable","TableKind":"PrimaryResult","Columns":[{"ColumnName":"x","ColumnType":"long"}],"Rows":[[1],{"OneApiErrors":[{"error":{"code":"LimitsExceeded","message":"Request is invalid","@message":"Query execution has exceeded the allowed limits"}}]}]},{"FrameType":"DataSetCompletion","HasErrors":true,"OneApiErrors":[{"error":{"code":"LimitsExceeded","@message":"Query execution has exceeded the allowed limits"}}]}]`,
		"at completion": `[{"FrameType":"DataSetHeader"},{"FrameType":"DataSetCompletion","HasErrors":true,"OneApiErrors":[{"error":{"code":"LimitsExceeded","@message":"Query execution has exceeded the allowed limits"}}]}]`,
		"cancelled":     `[{"FrameType":"DataSetHeader"},{"FrameType":"DataSetCompletion","HasErrors":false,"Cancelled":true}]`,
	} {
		t.Run(name, func(t *testing.T) {
			client, _ := kustoServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(response)) })
			_, err := client.Query(context.Background(), "Gateway | take 1", nil)
			var queryError *QueryError
			if !errors.As(err, &queryError) {
				t.Fatalf("error during the query: %#v", err)
			}
			if name != "cancelled" && (queryError.Code != "LimitsExceeded" || !strings.Contains(queryError.Message, "exceeded the allowed limits")) {
				t.Fatalf("error during the query: %v", err)
			}
		})
	}
}

func TestQueryRefusesIncompleteOrLargeResponses(t *testing.T) {
	client, _ := kustoServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"FrameType":"DataSetHeader"},{"FrameType":"DataTable","TableKind":"PrimaryResult","Columns":[{"ColumnName":"x","ColumnType":"long"}],"Rows":[[1]]}]`))
	})
	if _, err := client.Query(context.Background(), "print x = 1", nil); err == nil || !strings.Contains(err.Error(), "before the query completed") {
		t.Fatalf("incomplete response: %v", err)
	}
	large, _ := kustoServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(twoTables)) })
	large.MaxResponseBytes = 100
	if _, err := large.Query(context.Background(), "print x = 1", nil); err == nil || !strings.Contains(err.Error(), "larger than 100 bytes") {
		t.Fatalf("large response: %v", err)
	}
	mismatched, _ := kustoServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"FrameType":"DataTable","TableKind":"PrimaryResult","Columns":[{"ColumnName":"x","ColumnType":"long"}],"Rows":[[1,2]]},{"FrameType":"DataSetCompletion"}]`))
	})
	if _, err := mismatched.Query(context.Background(), "print x = 1", nil); err == nil || !strings.Contains(err.Error(), "does not match its columns") {
		t.Fatalf("row with more values than columns: %v", err)
	}
}

func TestQueryStopsWithoutAToken(t *testing.T) {
	calls := 0
	client, tokens := kustoServer(t, func(http.ResponseWriter, *http.Request) { calls++ })
	tokens.err = errors.New("no token")
	if _, err := client.Query(context.Background(), "print x = 1", nil); err == nil || err.Error() != "no token" || calls != 0 {
		t.Fatalf("query without a token: %v, %d requests", err, calls)
	}
}
