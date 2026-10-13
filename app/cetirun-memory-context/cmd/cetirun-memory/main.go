// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/Azure/CetiRun/app/cetirun-memory-context/assets"
	"github.com/Azure/CetiRun/app/cetirun-memory-context/internal/memory"
)

var version = "dev"
var commit = "unknown"

func run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	if slices.Contains(args, "--help") || len(args) == 0 {
		fmt.Fprintln(out, "Usage: cetirun-memory COMMAND < request.json\nCommands: status init query capture revise prepare commit propose check advance select retire record-source-check record-use trace lint repair-index forget plan|apply import plan|apply\nAsset installer: install-assets DIRECTORY\nNative adapter: native-event (memory-adapter/v1)\nOwner-private MCP: mcp --stdio --owner-private\nLegacy context adapter: native-context hermes|pi\nBinding: CETIRUN_MEMORY_ROOT.")
		guide, err := assets.Files.ReadFile("skills/cetirun-memory/references/requests.md")
		if err != nil {
			return 8
		}
		_, err = out.Write(guide)
		if err != nil {
			return 8
		}
		return 0
	}
	if len(args) == 2 && args[0] == "native-context" && (args[1] == "hermes" || args[1] == "pi") {
		data, err := io.ReadAll(io.LimitReader(in, memory.MaxInput+1))
		if err != nil {
			return 2
		}
		canonical, _ := assets.Files.ReadFile("AGENTS.md")
		store := memory.New(memory.Config{Root: os.Getenv("CETIRUN_MEMORY_ROOT")})
		text := store.NativeContext(ctx, args[1], data, string(canonical))
		if len(text) > 8192 {
			return 2
		}
		if err := json.NewEncoder(out).Encode(map[string]string{"context": text}); err != nil {
			return 8
		}
		return 0
	}
	if len(args) == 1 && args[0] == "native-event" {
		data, err := io.ReadAll(io.LimitReader(in, memory.MaxInput+1))
		if err != nil || len(data) > memory.MaxInput {
			return 2
		}
		var event memory.NativeEvent
		if memory.Decode(data, &event) != nil {
			return 2
		}
		canonical, _ := assets.Files.ReadFile("AGENTS.md")
		store := memory.New(memory.Config{Root: os.Getenv("CETIRUN_MEMORY_ROOT")})
		response, code := store.NativeEvent(ctx, event, string(canonical))
		if err := json.NewEncoder(out).Encode(response); err != nil {
			return 8
		}
		return code
	}
	if slices.Equal(args, []string{"mcp", "--stdio", "--owner-private"}) {
		root := os.Getenv("CETIRUN_MEMORY_ROOT")
		cwd, err := os.Getwd()
		if err != nil || root != filepath.Join(os.Getenv("HOME"), ".cetirun/memory") {
			return 4
		}
		id := rand.Text()
		binding := memory.NativeBinding{Adapter: memory.AdapterVersion, Entrypoint: "custom", NativeVersion: "mcp/2025-06-18", SessionID: id, OpportunityID: id, CWD: cwd, Audience: "owner-private", Destination: memory.Destination{Kind: "owner-private"}, NoCapture: os.Getenv("CETIRUN_MEMORY_NO_CAPTURE") == "true", NoMemory: os.Getenv("CETIRUN_MEMORY_NO_MEMORY") == "true"}
		return runMCP(ctx, in, out, memory.New(memory.Config{Root: root}), binding)
	}
	if len(args) == 2 && args[0] == "install-assets" {
		executable, err := os.Executable()
		if err != nil {
			return 8
		}
		binary, err := os.ReadFile(executable)
		if err != nil {
			return 8
		}
		if err := memory.InstallAssets(args[1], assets.Files, binary); err != nil {
			fmt.Fprintln(errOut, "AssetInstallationFailed")
			return 8
		}
		return 0
	}
	commands := []string{"status", "init", "query", "capture", "prepare", "commit", "propose", "check", "advance", "select", "lint", "repair-index", "revise", "retire", "record-source-check", "record-use", "trace", "forget-plan", "forget-apply", "import-plan", "import-apply"}
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintf(out, "%s %s %s ABI %d\n", version, commit, memory.ContractVersion, memory.HelperABI)
		return 0
	}
	if len(args) == 2 && (args[0] == "forget" || args[0] == "import") {
		args = []string{args[0] + "-" + args[1]}
	}
	if len(args) != 1 || !slices.Contains(commands, args[0]) {
		fmt.Fprintln(errOut, "InvalidCommand")
		return 2
	}
	data, err := io.ReadAll(io.LimitReader(in, memory.MaxInput+1))
	if err != nil || len(data) > memory.MaxInput {
		fmt.Fprintln(errOut, "InvalidInput")
		return 2
	}
	var request memory.Request
	if memory.Decode(data, &request) != nil {
		fmt.Fprintln(errOut, "InvalidInput")
		return 2
	}
	root := os.Getenv("CETIRUN_MEMORY_ROOT")
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || filepath.Base(root) != "memory" || filepath.Base(filepath.Dir(root)) != ".cetirun" {
		fmt.Fprintln(errOut, "BindingUnavailable")
		return 4
	}
	store := memory.New(memory.Config{Root: root})
	response, code := store.Execute(ctx, args[0], request)
	if err := json.NewEncoder(out).Encode(response); err != nil {
		return 8
	}
	return code
}
func main() {
	syscall.Umask(0077)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
