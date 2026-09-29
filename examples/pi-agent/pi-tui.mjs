#!/usr/bin/env node
// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import path from "node:path";

export async function main(args = process.argv.slice(2)) {
  if (args.includes("--help")) {
    console.log("Usage: pi-tui [--new]\nOpen Pi's native TUI in HOME/workspace. Resume the most recent native session by default; --new starts a new one. Use /login github-copilot and /model inside Pi. HTTP chat history is separate.");
    return;
  }
  if (args.some((arg) => arg !== "--new")) throw new Error("Unsupported TUI argument; use --help");
  if (!process.env.HOME || !process.stdin.isTTY || !process.stdout.isTTY) {
    throw new Error("Pi TUI requires HOME and an interactive terminal");
  }
  process.umask(0o077);
  process.env.PI_TELEMETRY = "0";
  // Offline catalog/update checks do not disable explicit OAuth or model calls.
  process.env.PI_OFFLINE = "1";
  process.env.PI_CODING_AGENT_DIR = path.join(process.env.HOME, ".pi", "agent");
  const {
    createAgentSessionFromServices, createAgentSessionRuntime, InteractiveMode, SessionManager,
  } = await import("@earendil-works/pi-coding-agent");
  const { createPiServices, PI_TOOLS } = await import("./pi-runtime.mjs");
  const initial = await createPiServices(process.env.HOME);
  process.chdir(initial.cwd);

  // Pi reuses this factory for /new, /resume and /fork, so each session keeps
  // the same approved context and private authentication directory.
  const createRuntime = async ({ cwd, sessionManager, sessionStartEvent }) => {
    const services = await createPiServices(process.env.HOME, cwd);
    const result = await createAgentSessionFromServices({
      services, sessionManager, sessionStartEvent, tools: PI_TOOLS,
    });
    return { ...result, services, diagnostics: services.diagnostics };
  };
  const sessionManager = args.includes("--new")
    ? SessionManager.create(initial.cwd)
    : SessionManager.continueRecent(initial.cwd);
  const runtime = await createAgentSessionRuntime(createRuntime, {
    cwd: initial.cwd,
    agentDir: initial.agentDir,
    sessionManager,
  });
  const tui = new InteractiveMode(runtime, {
    startupDiagnostics: [{
      type: "info",
      message: "Use /login github-copilot only after approving Pi's model-policy updates on your Copilot account.",
    }],
  });
  await tui.run();
}

if (import.meta.main) {
  main().catch(() => {
    console.error("Pi TUI startup failed; check the terminal, HOME and approved context. Use --help for usage.");
    process.exitCode = 1;
  });
}
