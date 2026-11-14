// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// CetiRun session log for OpenClaw. When OpenClaw updates the transcript of a
// session, the plugin reads the new persisted events with OpenClaw's
// transcript SDK and appends their records to
// <state dir>/cetirun-session-log/openclaw.jsonl, which the agent chart's
// logger copies to the container log. The chart loads it with logger.enabled.

import { AsyncLocalStorage } from "node:async_hooks";
import path from "node:path";
import { redactSensitiveText } from "openclaw/plugin-sdk/logging-core";
import { readSessionTranscriptRawDelta } from "openclaw/plugin-sdk/session-transcript-runtime";
import { createExporter } from "./exporter.mjs";

// The transcript SDK subpath is a JavaScript-only runtime export of this
// OpenClaw version; the image pins it.
const OPENCLAW_VERSION = "2026.9.6";

function register(api) {
  // Only a live activation exports; discovery, setup, and CLI metadata loads do not.
  if (api.registrationMode !== undefined && api.registrationMode !== "full") return;
  const log = (level, message) => api.logger?.[level]?.(`cetirun-session-log: ${message}`);
  if (typeof readSessionTranscriptRawDelta !== "function" || typeof api.runtime?.events?.onSessionTranscriptUpdate !== "function") {
    log("warn", `OpenClaw ${api.runtime?.version ?? "unknown"} has no transcript delta API; sessions are not exported`);
    return;
  }
  if (api.runtime.version !== OPENCLAW_VERSION) {
    log("warn", `written for OpenClaw ${OPENCLAW_VERSION}, running on ${api.runtime.version}`);
  }
  const stateDir = process.env.OPENCLAW_STATE_DIR || api.runtime.state.resolveStateDir(process.env);
  const outside = AsyncLocalStorage.snapshot();
  const exporter = createExporter({
    dir: path.join(stateDir, "cetirun-session-log"),
    readDelta: (params) => readSessionTranscriptRawDelta(params),
    // OpenClaw's built-in patterns and registered secret values, also when
    // its own log redaction is off.
    redact: (text) => redactSensitiveText(text, { mode: "tools" }),
    log,
  });
  api.runtime.events.onSessionTranscriptUpdate((update) => {
    // Updates can come from inside a turn, and reads there end before that
    // turn's input. Export in the context of the registration instead; the
    // retries and reads that follow keep it.
    outside(() => exporter.schedule(update?.target));
  });
  outside(() => exporter.resume());
}

export default { id: "cetirun-session-log", name: "CetiRun session log", register };
