// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	remoteconsts "k8s.io/apimachinery/pkg/util/remotecommand"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	clientexec "k8s.io/client-go/util/exec"
	"k8s.io/streaming/pkg/httpstream"
	"k8s.io/streaming/pkg/httpstream/spdy"
)

func TestKubernetesShellProbeFallsBackToSPDY(t *testing.T) {
	var gets, posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/namespaces/cetirun-example/pods/cetirun-example-0/exec" ||
			r.URL.Query().Get("container") != "cetirun" ||
			!reflect.DeepEqual(r.URL.Query()["command"], []string{"zsh", "--version"}) ||
			r.URL.Query().Get("stdout") != "true" || r.URL.Query().Get("stderr") != "true" ||
			r.URL.Query().Get("stdin") == "true" || r.URL.Query().Get("tty") == "true" {
			t.Errorf("unexpected exec target or probe options: %s", r.URL)
		}
		switch r.Method {
		case http.MethodGet:
			gets.Add(1)
			http.Error(w, "WebSocket upgrade unavailable", http.StatusMethodNotAllowed)
		case http.MethodPost:
			posts.Add(1)
			serveProbeSPDY(t, w, r)
		default:
			t.Errorf("unexpected method: %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	backend, err := NewKubernetes(testConfig(), &rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shell, err := backend.DetectShell(ctx)
	if err != nil || shell != "zsh" || gets.Load() != 1 || posts.Load() != 1 {
		t.Fatalf("shell = %q, error = %v, GETs = %d, POSTs = %d", shell, err, gets.Load(), posts.Load())
	}
}

// Complete a real noninteractive exec handshake and its three SPDY streams.
func serveProbeSPDY(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	if _, err := httpstream.Handshake(r, w, []string{remoteconsts.StreamProtocolV4Name}); err != nil {
		t.Error(err)
		return
	}
	type incoming struct {
		stream  httpstream.Stream
		replied <-chan struct{}
	}
	streams := make(chan incoming, 3)
	connection := spdy.NewResponseUpgrader().UpgradeResponse(w, r, func(stream httpstream.Stream, replied <-chan struct{}) error {
		streams <- incoming{stream, replied}
		return nil
	})
	if connection == nil {
		t.Error("SPDY upgrade failed")
		return
	}
	defer connection.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	opened := make(map[string]httpstream.Stream)
	for len(opened) < 3 {
		select {
		case item := <-streams:
			select {
			case <-item.replied:
			case <-ctx.Done():
				t.Error("SPDY stream reply timed out")
				return
			}
			opened[item.stream.Headers().Get(corev1.StreamType)] = item.stream
		case <-ctx.Done():
			t.Error("SPDY streams timed out")
			return
		}
	}
	for _, kind := range []string{corev1.StreamTypeStdout, corev1.StreamTypeStderr, corev1.StreamTypeError} {
		stream := opened[kind]
		if stream == nil {
			t.Errorf("missing stream %s", kind)
			return
		}
		if kind == corev1.StreamTypeError {
			if err := json.NewEncoder(stream).Encode(metav1.Status{Status: metav1.StatusSuccess}); err != nil {
				t.Error(err)
			}
		}
		_ = stream.Close()
	}
	select {
	case <-connection.CloseChan():
	case <-ctx.Done():
		t.Error("SPDY client did not finish")
	}
}

func TestKubernetesExecFallbackFailsClosed(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !reflect.DeepEqual(r.URL.Query()["command"], []string{"sh", "-i"}) ||
			r.URL.Query().Get("stdin") != "true" || r.URL.Query().Get("tty") != "true" {
			t.Errorf("unexpected interactive options: %s", r.URL)
		}
		if r.Method == http.MethodGet {
			http.Error(w, "WebSocket upgrade unavailable", http.StatusMethodNotAllowed)
			return
		}
		posts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(apierrors.NewForbidden(schema.GroupResource{Resource: "pods/exec"}, "example", errors.New("denied")).Status())
	}))
	defer server.Close()
	backend, err := NewKubernetes(testConfig(), &rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = backend.Exec(ctx, "sh", Streams{Out: io.Discard})
	// The SPDY transport returns a plain error for a rejected upgrade. It must
	// still fail the exec rather than treating fallback as authorization.
	if err == nil || !strings.Contains(err.Error(), "is forbidden: denied") || posts.Load() != 1 {
		t.Fatalf("error = %v, POSTs = %d; want denial after fallback", err, posts.Load())
	}
}

func TestKubernetesExecDoesNotReplayEstablishedWebSocket(t *testing.T) {
	for _, success := range []bool{true, false} {
		t.Run(map[bool]string{true: "success", false: "command failure"}[success], func(t *testing.T) {
			var gets, posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					posts.Add(1)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				gets.Add(1)
				upgrader := websocket.Upgrader{Subprotocols: []string{remoteconsts.StreamProtocolV5Name}}
				connection, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer connection.Close()
				status := metav1.Status{Status: metav1.StatusSuccess}
				if !success {
					status = metav1.Status{Status: metav1.StatusFailure, Reason: remoteconsts.NonZeroExitCodeReason,
						Details: &metav1.StatusDetails{Causes: []metav1.StatusCause{{Type: remoteconsts.ExitCodeCauseType, Message: "42"}}}}
				}
				message, err := json.Marshal(status)
				if err != nil {
					t.Error(err)
					return
				}
				if err := connection.WriteMessage(websocket.BinaryMessage, append([]byte{remoteconsts.StreamErr}, message...)); err != nil {
					t.Error(err)
				}
				_ = connection.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			}))
			defer server.Close()
			backend, err := NewKubernetes(testConfig(), &rest.Config{Host: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = backend.Exec(ctx, "sh", Streams{Out: io.Discard})
			var exit clientexec.ExitError
			if (success && err != nil) || (!success && (!errors.As(err, &exit) || exit.ExitStatus() != 42)) ||
				gets.Load() != 1 || posts.Load() != 0 {
				t.Fatalf("error = %v, GETs = %d, POSTs = %d", err, gets.Load(), posts.Load())
			}
		})
	}
}

func TestStartupCategoryFromWebSocketTransport(t *testing.T) {
	for _, code := range []int{http.StatusMethodNotAllowed, http.StatusForbidden} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reason := metav1.StatusReasonMethodNotAllowed
			if code == http.StatusForbidden {
				reason = metav1.StatusReasonForbidden
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(metav1.Status{Status: metav1.StatusFailure,
				Reason: reason, Code: int32(code), Message: "synthetic private detail"})
		}))
		t.Cleanup(server.Close)
		executor, err := remotecommand.NewWebSocketExecutor(&rest.Config{Host: server.URL}, http.MethodGet, server.URL)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: io.Discard})
		cancel()
		want := "exec-upgrade-failed"
		if code == http.StatusForbidden {
			want = "authorization-denied"
		}
		if got := startupFailureCategory(err); got != want {
			t.Errorf("HTTP %d: category = %q, want %q (error type %T)", code, got, want, err)
		}
	}
}
