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
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Add(1)
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bad":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/redirect":
			http.Redirect(w, r, target.URL, http.StatusFound)
		case "/slow":
			time.Sleep(3 * time.Second)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	if err := probe(server.URL + "/healthz"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/bad", "/redirect", "/slow"} {
		if err := probe(server.URL + path); err == nil {
			t.Fatalf("accepted failed health endpoint %s", path)
		}
	}
	if redirected.Load() != 0 {
		t.Fatal("followed a redirect beyond the original loopback endpoint")
	}
	for _, endpoint := range []string{
		"https://127.0.0.1:8080/healthz", "http://localhost:8080/healthz", "http://example.test:8080/healthz",
		"http://127.0.0.1/healthz", "http://127.0.0.1:0/healthz", "http://127.0.0.1:65536/healthz",
		"http://127.0.0.1:18446744073709551616/healthz", "http://127.0.0.1:8080/healthz?query",
		"http://user@127.0.0.1:8080/healthz", "http://127.0.0.1:8080/healthz#fragment", "http://[bad",
	} {
		if err := probe(endpoint); err == nil {
			t.Fatalf("accepted invalid probe endpoint %q", endpoint)
		}
	}
}

func TestProbeDoesNotTreatHTTPOnlyReadinessAsMTLSReadiness(t *testing.T) {
	t.Setenv("CETIRUN_INGRESS_MTLS", "true")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	if err := run([]string{"--url", server.URL + "/healthz"}); err == nil {
		t.Fatal("HTTP success alone satisfied the mTLS readiness contract")
	}
}

func TestProbeCommandRejectsUnexpectedArgumentsAndDestinations(t *testing.T) {
	for _, arguments := range [][]string{nil, {"--url"}, {"install", "/tmp/other"}, {"--url", "http://127.0.0.1:8080/healthz", "extra"}} {
		if err := run(arguments); err == nil {
			t.Fatalf("accepted invalid probe invocation %v", arguments)
		}
	}
}
