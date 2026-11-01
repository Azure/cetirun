// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Azure/CetiRun/app/cetirun-memory-context/internal/memory"
)

func TestHelpWorksWithoutBindingsAndDocumentsPayloads(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"capture", "--help"}, {"install-assets", "--help"}, {"forget", "plan", "--help"}} {
		var out, errOut bytes.Buffer
		if code := run(context.Background(), args, strings.NewReader(""), &out, &errOut); code != 0 {
			t.Fatalf("%v: %s", args, errOut.String())
		}
		for _, word := range []string{"expectedGeneration", "requestContext", "temporalClass", "queryTerms"} {
			if !strings.Contains(out.String(), word) {
				t.Fatalf("help missing %s", word)
			}
		}
	}
}

func TestMCPInitializeAcceptsNewerClientInfo(t *testing.T) {
	store := memory.New(memory.Config{Root: t.TempDir()})
	input := strings.Join([]string{
		`{"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{"roots":{"listChanged":true},"elicitation":{"form":{},"url":{}}},"clientInfo":{"name":"example-client","title":"Example","version":"9.1.0","description":"A synthetic MCP client","websiteUrl":"https://example.com/client"}},"jsonrpc":"2.0","id":0}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"capabilities":{}}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if code := runMCP(context.Background(), strings.NewReader(input), &out, store, memory.NativeBinding{}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	replies := map[string]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var reply map[string]any
		if err := json.Unmarshal([]byte(line), &reply); err != nil {
			t.Fatal(err)
		}
		replies[fmt.Sprint(reply["id"])] = reply
	}
	if replies["0"]["result"] == nil || replies["1"]["result"] == nil {
		t.Fatalf("newer client could not initialize: %s", out.String())
	}
	if replies["2"]["error"] == nil {
		t.Fatalf("initialize without a protocol version or client was accepted: %s", out.String())
	}
}

func TestMCPDescribesMemoryPayloads(t *testing.T) {
	store := memory.New(memory.Config{Root: t.TempDir()})
	var out bytes.Buffer
	input := "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}\n"
	if code := runMCP(context.Background(), strings.NewReader(input), &out, store, memory.NativeBinding{}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var reply struct {
		Result struct {
			Tools []struct {
				InputSchema struct {
					Properties struct {
						Payload struct {
							Properties map[string]json.RawMessage `json:"properties"`
						} `json:"payload"`
					} `json:"properties"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Result.Tools) != 1 {
		t.Fatalf("unexpected tool list: %s", out.String())
	}
	properties := reply.Result.Tools[0].InputSchema.Properties.Payload.Properties
	for _, field := range []string{"queryTerms", "note", "observation", "procedure", "ref"} {
		if len(properties[field]) == 0 {
			t.Fatalf("tool payload does not describe %s", field)
		}
	}
}
