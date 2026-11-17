// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// The session records of the CetiRun session log, from OpenClaw's persisted
// transcript events. The plugin entry reads the events with OpenClaw's
// transcript SDK; this module turns them into JSON lines, appends them to a
// file that the agent chart's logger follows, and keeps one resumable cursor
// for each session. It imports nothing from OpenClaw, so tests run it with Node.

import fs from "node:fs";
import path from "node:path";

export const RECORD_VERSION = 1;
// The container runtime splits longer log lines.
export const MAX_LINE_BYTES = 16000;
export const MAX_FILE_BYTES = 8 * 1024 * 1024;
const PAGE_EVENTS = 500;
const PAGE_BYTES = 8 * 1024 * 1024;
const MAX_PAGE_BYTES = 64 * 1024 * 1024;
const MAX_SAVED_SESSIONS = 5000;
const RETRY_MS = 30000;

function isRecord(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function timestamp(value, fallback) {
  if (typeof value === "number" && Number.isFinite(value)) return new Date(value).toISOString();
  if (typeof value === "string" && value) return value;
  return fallback;
}

// Text of a message content: a string, or text parts. Other parts are named, not copied.
export function textOf(content) {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content.map((part) => {
    if (typeof part === "string") return part;
    if (!isRecord(part)) return "";
    if (part.type === "text" && typeof part.text === "string") return part.text;
    return `[${typeof part.type === "string" ? part.type : "part"}]`;
  }).filter(Boolean).join("\n");
}

function argumentsText(value) {
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value ?? {});
  } catch {
    return "";
  }
}

// OpenClaw's Tool Search keeps most tools in a catalog, and the model runs
// them through the control tool tool_call. The transcript names tool_call, so
// the records name the tool that ran, and `via` names tool_call.
const TOOL_CALL = "tool_call";
const TOOL_SELECTORS = ["id", "toolId", "name"];
// A catalog id is <source>:<owner>:<tool name>, and a tool name has no colon.
const CATALOG_ID = /^(?:openclaw|mcp|client):[^:]+:([^:]+)$/;

function argumentsObject(value) {
  if (isRecord(value)) return value;
  if (typeof value !== "string") return undefined;
  try {
    const parsed = JSON.parse(value);
    return isRecord(parsed) ? parsed : undefined;
  } catch {
    return undefined;
  }
}

function selectorOf(params) {
  const value = params.id ?? params.toolId ?? params.name;
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

function toolNameOf(selector) {
  const match = CATALOG_ID.exec(selector);
  return match ? match[1] : selector;
}

// The tool that tool_call arguments select, read as OpenClaw 2026.9.6 reads
// them. A selector only inside args or input moves to the top. With a
// non-empty args or input, the selector is id, toolId, or name, in this order.
// Without them, OpenClaw takes the first of these whose value is in the tool
// catalog of the run, which the transcript does not keep. So one selector, or
// one catalog id among them, names the tool; otherwise only the result does.
function toolCallTarget(value) {
  let params = argumentsObject(value);
  if (!params) return undefined;
  if (!TOOL_SELECTORS.some((key) => Object.hasOwn(params, key))) {
    const nested = params.args ?? params.input;
    const hoisted = isRecord(nested) ? selectorOf(nested) : undefined;
    if (hoisted === undefined) return undefined;
    const { args: _args, input: _input, ...rest } = params;
    params = { ...rest, ...nested, id: hoisted };
  }
  const input = params.args ?? params.input;
  if (input != null && !(isRecord(input) && Object.keys(input).length === 0)) {
    const selector = selectorOf(params);
    return selector === undefined ? undefined : toolNameOf(selector);
  }
  const values = TOOL_SELECTORS.map((key) => params[key]).filter((item) => item != null);
  const names = values.filter((item) => typeof item === "string" && item.trim()).map((item) => item.trim());
  if (values.length === 1) return names.length === 1 ? toolNameOf(names[0]) : undefined;
  const catalogIds = names.filter((item) => CATALOG_ID.test(item));
  return catalogIds.length === 1 ? toolNameOf(catalogIds[0]) : undefined;
}

// The tool that a tool_call result came from: OpenClaw keeps it in the
// result's details. A call that failed before it found a tool has none.
function toolCallResultTool(details) {
  const tool = isRecord(details) ? details.tool : undefined;
  return isRecord(tool) && typeof tool.name === "string" && tool.name ? tool.name : undefined;
}

// The records of one transcript event. Only the messages of the conversation
// count: user input, assistant output, tool calls, and tool results. Session
// headers, model and thinking changes, compaction summaries, custom entries,
// reasoning blocks, and provider replay data are not copied.
export function recordsFor(event, target, redact, now = () => new Date().toISOString()) {
  if (!isRecord(event) || event.type !== "message" || !isRecord(event.message)) return [];
  const message = event.message;
  const eventId = typeof event.id === "string" && event.id ? event.id : undefined;
  if (!eventId) return [];
  const base = {
    v: RECORD_VERSION,
    ts: timestamp(event.timestamp, timestamp(message.timestamp, now())),
    runtime: "openclaw",
    agentId: target.agentId,
    sessionKey: target.sessionKey,
    sessionId: target.sessionId,
  };
  const runId = isRecord(message.__openclaw) && typeof message.__openclaw.runId === "string" ? message.__openclaw.runId : undefined;
  if (runId) base.runId = runId;
  const id = (suffix) => `openclaw:${target.agentId}:${target.sessionId}:${eventId}${suffix}`;
  const records = [];
  if (message.role === "user") {
    records.push({ ...base, id: id(""), kind: "user", content: redact(textOf(message.content)) });
  } else if (message.role === "assistant") {
    const parts = Array.isArray(message.content) ? message.content : [message.content];
    const text = parts.filter((part) => typeof part === "string" || (isRecord(part) && part.type === "text"))
      .map((part) => (typeof part === "string" ? part : part.text)).filter((value) => typeof value === "string" && value).join("\n");
    const model = typeof message.model === "string" ? message.model : undefined;
    if (text) records.push({ ...base, id: id(""), kind: "assistant", ...(model ? { model } : {}), content: redact(text) });
    parts.forEach((part, index) => {
      if (!isRecord(part) || part.type !== "toolCall") return;
      const name = typeof part.name === "string" ? part.name : "";
      const ran = name === TOOL_CALL ? toolCallTarget(part.arguments) : undefined;
      records.push({
        ...base, id: id(`:${index}`), kind: "tool_call", ...(model ? { model } : {}),
        tool: ran ?? name, ...(ran ? { via: TOOL_CALL } : {}),
        ...(typeof part.id === "string" ? { toolCallId: part.id } : {}),
        content: redact(argumentsText(part.arguments)),
      });
    });
  } else if (message.role === "toolResult") {
    const name = typeof message.toolName === "string" ? message.toolName : "";
    const ran = name === TOOL_CALL ? toolCallResultTool(message.details) : undefined;
    records.push({
      ...base, id: id(""), kind: "tool_result",
      tool: ran ?? name, ...(ran ? { via: TOOL_CALL } : {}),
      ...(typeof message.toolCallId === "string" ? { toolCallId: message.toolCallId } : {}),
      isError: message.isError === true,
      content: redact(textOf(message.content)),
    });
  }
  return records;
}

function cutUtf8(text, maxBytes) {
  const bytes = Buffer.from(text, "utf8");
  if (bytes.length <= maxBytes) return text;
  let end = Math.max(0, maxBytes);
  // Do not cut inside a UTF-8 sequence.
  while (end > 0 && (bytes[end] & 0xc0) === 0x80) end -= 1;
  return bytes.subarray(0, end).toString("utf8");
}

// One JSON line, at most MAX_LINE_BYTES with its newline. A longer content is cut and marked.
export function encodeRecord(record, maxLineBytes = MAX_LINE_BYTES) {
  let content = typeof record.content === "string" ? record.content : "";
  let truncated = false;
  for (let attempt = 0; attempt < 8; attempt += 1) {
    const line = `${JSON.stringify({ ...record, content, truncated })}\n`;
    const size = Buffer.byteLength(line, "utf8");
    if (size <= maxLineBytes) return line;
    const over = size - maxLineBytes;
    const contentBytes = Buffer.byteLength(content, "utf8");
    content = cutUtf8(content, Math.max(0, contentBytes - over - 64 * (attempt + 1)));
    truncated = true;
  }
  return `${JSON.stringify({ ...record, content: "", truncated: true })}\n`;
}

// An append-only JSONL file with one rotated predecessor: <name> and <name>.1.
export function createLogFile({ dir, name, maxFileBytes = MAX_FILE_BYTES }) {
  const file = path.join(dir, name);
  const rotated = `${file}.1`;
  return {
    file,
    rotated,
    append(lines) {
      if (lines.length === 0) return;
      fs.mkdirSync(dir, { recursive: true, mode: 0o700 });
      for (const line of lines) {
        const size = Buffer.byteLength(line, "utf8");
        let current = 0;
        try {
          current = fs.statSync(file).size;
        } catch (error) {
          if (error.code !== "ENOENT") throw error;
        }
        if (current > 0 && current + size > maxFileBytes) {
          try {
            fs.renameSync(file, rotated);
          } catch (error) {
            // Another writer rotated it.
            if (error.code !== "ENOENT") throw error;
          }
        }
        fs.appendFileSync(file, line, { mode: 0o600 });
      }
    },
  };
}

function readJson(file) {
  try {
    return JSON.parse(fs.readFileSync(file, "utf8"));
  } catch (error) {
    if (error.code === "ENOENT") return undefined;
    return undefined;
  }
}

// The time of a transcript event in milliseconds, or NaN when it has none.
export function eventTime(event) {
  for (const value of [event?.timestamp, event?.message?.timestamp]) {
    if (typeof value === "number" && Number.isFinite(value)) return value;
    if (typeof value === "string" && value) {
      const parsed = Date.parse(value);
      if (!Number.isNaN(parsed)) return parsed;
    }
  }
  return Number.NaN;
}

function writeJsonAtomic(file, value) {
  const temporary = `${file}.${process.pid}.tmp`;
  fs.writeFileSync(temporary, JSON.stringify(value), { mode: 0o600 });
  fs.renameSync(temporary, file);
}

// Exports the transcripts of the sessions that OpenClaw updates. A record is
// written before its cursor moves, so a restart can write a record again but
// never skips one; its stable id makes such a repeat easy to remove. Only the
// messages from the exporter's first start on are exported: a session from
// before, such as one from before an upgrade, keeps its older messages to
// itself. The HOME initialization deletes the state when the logger is off,
// so after the logger is on again, the exporter starts anew.
export function createExporter({
  dir, readDelta, redact, log = () => {}, maxFileBytes = MAX_FILE_BYTES, maxLineBytes = MAX_LINE_BYTES,
  now = () => new Date().toISOString(), retryMs = RETRY_MS,
}) {
  const output = createLogFile({ dir, name: "openclaw.jsonl", maxFileBytes });
  const cursorFile = path.join(dir, "cursors.json");
  const saved = readJson(cursorFile);
  const sessions = new Map(isRecord(saved) && isRecord(saved.sessions) ? Object.entries(saved.sessions) : []);
  const savedSince = isRecord(saved) && typeof saved.since === "string" ? Date.parse(saved.since) : Number.NaN;
  const since = Number.isNaN(savedSince) ? (Date.parse(now()) || Date.now()) : savedSince;
  const pending = new Map();
  const retrying = new Map();
  let running = Promise.resolve();
  let retryTimer;

  const key = (target) => `${target.agentId}/${target.sessionId}`;

  function persist() {
    if (sessions.size > MAX_SAVED_SESSIONS) {
      const oldest = [...sessions.entries()].sort((a, b) => String(a[1].updatedAt).localeCompare(String(b[1].updatedAt)));
      for (const [name] of oldest.slice(0, sessions.size - MAX_SAVED_SESSIONS)) sessions.delete(name);
    }
    fs.mkdirSync(dir, { recursive: true, mode: 0o700 });
    writeJsonAtomic(cursorFile, { version: 1, since: new Date(since).toISOString(), sessions: Object.fromEntries(sessions) });
  }

  if (Number.isNaN(savedSince)) {
    // Keep the start time before the first export, so a restart does not move it.
    try {
      persist();
    } catch (error) {
      log("warn", `the start of the session log was not saved: ${error?.message ?? error}`);
    }
  }

  async function drain(target) {
    const name = key(target);
    let maxBytes = PAGE_BYTES;
    for (let pages = 0; pages < 1000; pages += 1) {
      const entry = sessions.get(name);
      const result = await readDelta({
        agentId: target.agentId, sessionKey: target.sessionKey, sessionId: target.sessionId,
        ...(entry?.cursor ? { cursor: entry.cursor } : {}), maxEvents: PAGE_EVENTS, maxBytes,
      });
      if (!isRecord(result) || result.kind === "missing") {
        if (sessions.delete(name)) persist();
        return;
      }
      if (result.kind === "reset") {
        // The transcript was replaced: export it again from its start. The
        // records of events that it kept have the ids that they had before.
        log("info", `session transcript ${target.sessionId} was replaced (${result.reason ?? "reset"}); exporting it again`);
        sessions.set(name, { ...target, cursor: result.cursor, updatedAt: now() });
        persist();
        continue;
      }
      if (result.kind !== "page") return;
      const events = Array.isArray(result.events) ? result.events : [];
      if (events.length === 0 && result.hasMore && typeof result.requiredBytes === "number") {
        if (result.requiredBytes > MAX_PAGE_BYTES || maxBytes >= result.requiredBytes) {
          log("warn", `session transcript ${target.sessionId} has an event of ${result.requiredBytes} bytes; it cannot be exported`);
          return;
        }
        maxBytes = result.requiredBytes;
        continue;
      }
      // Events without a time are exported: losing a record is worse than an early one.
      const lines = events.filter((item) => !(eventTime(item?.event) < since))
        .flatMap((item) => recordsFor(item?.event, target, redact, now))
        .map((record) => encodeRecord(record, maxLineBytes));
      output.append(lines);
      sessions.set(name, { ...target, cursor: result.cursor, updatedAt: now() });
      persist();
      maxBytes = PAGE_BYTES;
      if (!result.hasMore) return;
    }
  }

  // A failed session waits retryMs before its next read, unless an update comes first.
  function retryLater(target) {
    retrying.set(key(target), target);
    if (retryTimer) return;
    retryTimer = setTimeout(() => {
      retryTimer = undefined;
      for (const [name, value] of retrying) pending.set(name, value);
      retrying.clear();
      kick();
    }, retryMs);
    retryTimer.unref?.();
  }

  function kick() {
    running = running.then(async () => {
      while (pending.size > 0) {
        const [name, target] = pending.entries().next().value;
        pending.delete(name);
        try {
          await drain(target);
        } catch (error) {
          log("warn", `session transcript ${target.sessionId} was not exported: ${error?.message ?? error}`);
          retryLater(target);
        }
      }
    });
    return running;
  }

  return {
    // Queue a session for export; one export runs at a time.
    schedule(target) {
      if (!isRecord(target) || !target.agentId || !target.sessionId || !target.sessionKey) return running;
      pending.set(key(target), { agentId: target.agentId, sessionKey: target.sessionKey, sessionId: target.sessionId });
      return kick();
    },
    // Continue the sessions that have a saved cursor, after a restart.
    resume() {
      for (const entry of sessions.values()) {
        if (entry.agentId && entry.sessionId && entry.sessionKey) {
          pending.set(key(entry), { agentId: entry.agentId, sessionKey: entry.sessionKey, sessionId: entry.sessionId });
        }
      }
      return kick();
    },
    idle() {
      return running;
    },
    files: { output: output.file, rotated: output.rotated, cursors: cursorFile },
  };
}
