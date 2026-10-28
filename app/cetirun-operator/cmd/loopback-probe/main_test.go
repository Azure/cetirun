// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestProbeAcceptsOnlySuccessfulNumericLoopbackHTTP(t *testing.T) {
	var redirected atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Add(1)
	}))
	defer redirectTarget.Close()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/bad" {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if request.URL.Path == "/redirect" {
			http.Redirect(writer, request, redirectTarget.URL, http.StatusFound)
			return
		}
		if request.URL.Path == "/slow" {
			time.Sleep(timeout + time.Second)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	endpoint := server.URL
	if err := probe(endpoint + "/healthz"); err != nil {
		t.Fatal(err)
	}
	if err := probe(endpoint + "/bad"); err == nil {
		t.Fatal("accepted unhealthy response")
	}
	if err := probe(endpoint + "/redirect"); err == nil {
		t.Fatal("accepted redirect")
	}
	if redirected.Load() != 0 {
		t.Fatal("followed redirect beyond the original loopback endpoint")
	}
	if err := probe(endpoint + "/slow"); err == nil {
		t.Fatal("accepted an endpoint that exceeded the fixed timeout")
	}
	for _, value := range []string{"https://127.0.0.1:8080/healthz", "http://localhost:8080/healthz", "http://example.test:8080/healthz", "http://127.0.0.1/healthz", "http://127.0.0.1:0/healthz", "http://127.0.0.1:65536/healthz", "http://127.0.0.1:8080/healthz?query", "http://[bad"} {
		if err := probe(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}
