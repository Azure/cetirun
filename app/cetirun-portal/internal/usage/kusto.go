// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// DefaultQueryTimeout bounds a query on the Kusto cluster, which also
	// stops it there. A Dev SKU cluster has little query capacity.
	DefaultQueryTimeout = 30 * time.Second
	// DefaultMaxResponseBytes bounds the response of a query.
	DefaultMaxResponseBytes = 16 << 20
)

// TokenSource gives access tokens of a resource.
type TokenSource interface {
	Token(ctx context.Context, resource string) (string, error)
}

// Kusto runs read-only queries on one database of an Azure Data Explorer
// cluster through its REST API. A query takes its values as query parameters
// (declare query_parameters), never as text put into the query.
type Kusto struct {
	// Endpoint is the query URI of the Kusto cluster, such as
	// https://<name>.<region>.kusto.windows.net. It is also the resource of
	// the access token.
	Endpoint string
	Database string
	Tokens   TokenSource
	HTTP     *http.Client
	// Timeout of a query; DefaultQueryTimeout when zero.
	Timeout time.Duration
	// MaxResponseBytes; DefaultMaxResponseBytes when zero.
	MaxResponseBytes int64
}

// Column is a column of a result table.
type Column struct {
	Name string `json:"ColumnName"`
	Type string `json:"ColumnType"`
}

// Table is a result table. Numbers are json.Number values; Kusto returns
// decimal values as strings, datetime values as RFC 3339 strings and dynamic
// values as JSON.
type Table struct {
	Columns []Column
	Rows    [][]any
}

// QueryError is an error that the Kusto cluster returned for a query.
type QueryError struct {
	Status  int
	Code    string
	Message string
}

func (e *QueryError) Error() string {
	text := "the Kusto query failed"
	if e.Status != 0 {
		text += fmt.Sprintf(" with HTTP %d", e.Status)
	}
	if e.Code != "" {
		text += ": " + e.Code
	}
	if e.Message != "" {
		text += ": " + e.Message
	}
	return text
}

// Query runs a query with its parameters and returns its result tables, in
// the order of the query's statements.
func (k *Kusto) Query(ctx context.Context, query string, parameters map[string]string) ([]Table, error) {
	timeout := k.Timeout
	if timeout <= 0 {
		timeout = DefaultQueryTimeout
	}
	limit := k.MaxResponseBytes
	if limit <= 0 {
		limit = DefaultMaxResponseBytes
	}
	ctx, cancel := context.WithTimeout(ctx, timeout+5*time.Second)
	defer cancel()
	token, err := k.Tokens.Token(ctx, k.Endpoint)
	if err != nil {
		return nil, err
	}
	if parameters == nil {
		parameters = map[string]string{}
	}
	properties, err := json.Marshal(map[string]any{
		"Options": map[string]any{
			"servertimeout":    formatTimeout(timeout),
			"request_readonly": true,
		},
		"Parameters": parameters,
	})
	if err != nil {
		return nil, errors.New("cannot encode the query parameters")
	}
	// As the Kusto SDKs send them: the request properties as a JSON string.
	body, err := json.Marshal(map[string]string{"db": k.Database, "csl": query, "properties": string(properties)})
	if err != nil {
		return nil, errors.New("cannot encode the query")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(k.Endpoint, "/")+"/v2/rest/query", bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("cannot create the Kusto request")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("x-ms-app", "cetirun-portal")
	client := k.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("the Kusto cluster is unavailable: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("cannot read the Kusto response: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("the Kusto response is larger than %d bytes", limit)
	}
	if response.StatusCode != http.StatusOK {
		return nil, requestError(response.StatusCode, data)
	}
	return parseFrames(data)
}

// formatTimeout writes a duration as a Kusto timespan, such as 00:00:30.
func formatTimeout(timeout time.Duration) string {
	seconds := int64(timeout / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return fmt.Sprintf("%02d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
}

// requestError reads the error of a refused request, such as a semantic
// error of the query or a missing role.
func requestError(status int, data []byte) error {
	var payload struct {
		Error struct {
			Code          string `json:"code"`
			Message       string `json:"message"`
			DetailMessage string `json:"@message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &payload)
	message := payload.Error.DetailMessage
	if message == "" {
		message = payload.Error.Message
	}
	return &QueryError{Status: status, Code: payload.Error.Code, Message: shorten(message)}
}

type frame struct {
	FrameType    string          `json:"FrameType"`
	TableKind    string          `json:"TableKind"`
	Columns      []Column        `json:"Columns"`
	Rows         json.RawMessage `json:"Rows"`
	HasErrors    bool            `json:"HasErrors"`
	Cancelled    bool            `json:"Cancelled"`
	OneAPIErrors []oneAPIError   `json:"OneApiErrors"`
}

type oneAPIError struct {
	Error struct {
		Code          string `json:"code"`
		Message       string `json:"message"`
		DetailMessage string `json:"@message"`
	} `json:"error"`
}

func (e oneAPIError) err() error {
	message := e.Error.DetailMessage
	if message == "" {
		message = e.Error.Message
	}
	return &QueryError{Code: e.Error.Code, Message: shorten(message)}
}

// parseFrames reads a response of the v2 REST API: a JSON array of frames.
// The primary results are its data tables; an error during the query comes
// in a row of a table, or in the last frame.
func parseFrames(data []byte) ([]Table, error) {
	var frames []json.RawMessage
	if err := json.Unmarshal(data, &frames); err != nil {
		return nil, errors.New("the Kusto response is not a list of frames")
	}
	var tables []Table
	completed := false
	for _, raw := range frames {
		var item frame
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, errors.New("the Kusto response has an invalid frame")
		}
		switch item.FrameType {
		case "DataTable":
			if item.TableKind != "PrimaryResult" {
				continue
			}
			table, err := parseTable(item)
			if err != nil {
				return nil, err
			}
			tables = append(tables, table)
		case "DataSetCompletion":
			completed = true
			if item.HasErrors || item.Cancelled {
				if len(item.OneAPIErrors) > 0 {
					return nil, item.OneAPIErrors[0].err()
				}
				return nil, &QueryError{Message: "the query did not complete"}
			}
		}
	}
	if !completed {
		return nil, errors.New("the Kusto response ended before the query completed")
	}
	return tables, nil
}

func parseTable(item frame) (Table, error) {
	var rows []json.RawMessage
	if len(item.Rows) > 0 {
		if err := json.Unmarshal(item.Rows, &rows); err != nil {
			return Table{}, errors.New("the Kusto response has invalid rows")
		}
	}
	table := Table{Columns: item.Columns, Rows: make([][]any, 0, len(rows))}
	for _, raw := range rows {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) > 0 && trimmed[0] == '{' {
			// An error in the data: the query failed after it started.
			var failure struct {
				OneAPIErrors []oneAPIError `json:"OneApiErrors"`
			}
			if json.Unmarshal(trimmed, &failure) == nil && len(failure.OneAPIErrors) > 0 {
				return Table{}, failure.OneAPIErrors[0].err()
			}
			return Table{}, &QueryError{Message: "the query failed while it returned its rows"}
		}
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.UseNumber()
		var values []any
		if err := decoder.Decode(&values); err != nil {
			return Table{}, errors.New("the Kusto response has an invalid row")
		}
		if len(values) != len(item.Columns) {
			return Table{}, errors.New("a row of the Kusto response does not match its columns")
		}
		table.Rows = append(table.Rows, values)
	}
	return table, nil
}

func shorten(message string) string {
	message = strings.TrimSpace(message)
	if len(message) > 500 {
		return message[:500]
	}
	return message
}
