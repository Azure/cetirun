// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Azure/CetiRun/app/internal/terminalpolicy"
	"github.com/gorilla/websocket"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	remoteprotocol "k8s.io/apimachinery/pkg/util/remotecommand"
	"k8s.io/client-go/rest"
)

func TestKataShellDetectionThroughExec(t *testing.T) {
	for _, test := range []struct {
		name    string
		shell   string
		failure string
		calls   int
	}{
		{name: "zsh", shell: "zsh", calls: 1},
		{name: "bash after missing zsh", shell: "bash", calls: 2},
		{name: "sh after missing zsh and bash", shell: "sh", calls: 3},
		{name: "no shells", calls: 3},
		{name: "unknown runtime failure", failure: "failed to exec in container: agent unavailable", calls: 1},
		{name: "different missing file", failure: "failed to exec in container: the file helper was not found", calls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var mu sync.Mutex
			var calls [][]string
			cfg := testConfig()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				query := r.URL.Query()
				command := query["command"]
				mu.Lock()
				calls = append(calls, command)
				mu.Unlock()
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/namespaces/"+cfg.Namespace+"/pods/"+cfg.PodName()+"/exec" ||
					query.Get("container") != cfg.Container || query.Get("stdout") != "true" || query.Get("stderr") != "true" ||
					query.Get("stdin") == "true" || query.Get("tty") == "true" || len(command) == 0 {
					t.Errorf("unexpected exec request: %s %s", r.Method, r.URL)
					http.Error(w, "invalid probe", http.StatusBadRequest)
					return
				}
				upgrader := websocket.Upgrader{Subprotocols: []string{remoteprotocol.StreamProtocolV5Name}}
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					t.Errorf("upgrade exec: %v", err)
					return
				}
				defer conn.Close()
				status := metav1.Status{Status: metav1.StatusSuccess}
				if command[0] != test.shell {
					// Kata fails executable lookup before the process starts. The exec
					// error channel carries an InternalError, not an exit code of 127.
					message := fmt.Sprintf("error executing command in container: the file %s was not found", command[0])
					if test.failure != "" {
						message = test.failure
					}
					status = apierrors.NewInternalError(errors.New(message)).ErrStatus
				}
				payload, err := json.Marshal(status)
				if err != nil {
					t.Errorf("encode exec status: %v", err)
					return
				}
				if err := conn.WriteMessage(websocket.BinaryMessage, append([]byte{remoteprotocol.StreamErr}, payload...)); err != nil {
					t.Errorf("write exec status: %v", err)
					return
				}
				if err := conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second)); err != nil {
					t.Errorf("close exec streams: %v", err)
				}
			}))
			defer server.Close()
			backend, err := NewKubernetes(cfg, &rest.Config{Host: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			shell, err := backend.DetectShell(ctx)
			switch {
			case test.failure != "":
				want := apierrors.NewInternalError(errors.New(test.failure)).Error()
				if err == nil || err.Error() != want || startupFailureCategory(err) != "unknown" {
					t.Fatalf("got %q, %v; want original runtime failure %q", shell, err, want)
				}
			case test.shell == "":
				if !errors.Is(err, terminalpolicy.ErrNoShell) {
					t.Fatalf("expected no-shell failure, got %q, %v", shell, err)
				}
			default:
				if err != nil || shell != test.shell {
					t.Fatalf("got %q, %v; want %q", shell, err, test.shell)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			var want [][]string
			for _, candidate := range terminalpolicy.ShellCandidates()[:test.calls] {
				want = append(want, candidate.Probe)
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("exec probes = %v; want %v", calls, want)
			}
		})
	}
}

func TestKataShellMissingClassification(t *testing.T) {
	missing := errors.New("error executing command in container: the file zsh was not found")
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "Kata lookup", err: missing, want: true},
		{name: "wrapped Kata lookup", err: fmt.Errorf("exec failed: %w", missing), want: true},
		{name: "no exec context", err: errors.New("the file zsh was not found")},
		{name: "other shell", err: errors.New("error executing command in container: the file bash was not found")},
		{name: "other executable", err: errors.New("error executing command in container: the file zsh-helper was not found")},
		{name: "unknown failure", err: errors.New("exec failed: zsh unavailable")},
		{name: "denied", err: fmt.Errorf("%w: %v", ErrDenied, missing)},
		{name: "forbidden", err: apierrors.NewForbidden(schema.GroupResource{Resource: "pods/exec"}, "pod", missing)},
		{name: "unauthorized", err: apierrors.NewUnauthorized(missing.Error())},
		{name: "missing pod", err: apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, missing.Error())},
		{name: "timeout", err: apierrors.NewTimeoutError(missing.Error(), 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := shellMissing(test.err, "zsh"); got != test.want {
				t.Fatalf("shellMissing(%v, zsh) = %t; want %t", test.err, got, test.want)
			}
		})
	}
}
