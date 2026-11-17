// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import { createExporter, encodeRecord, MAX_LINE_BYTES, recordsFor } from "../../agents/openclaw/plugins/cetirun-session-log/exporter.mjs";

const target = { agentId: "main", sessionKey: "agent:main:main", sessionId: "session-1" };
const SECRET = "sk-synthetic0000000000000000000000";
const redact = (text) => text.replaceAll(SECRET, "sk-syn...0000");

function message(id, content, timestamp = 1_700_000_000_000) {
  return { type: "message", id, parentId: null, timestamp, message: content };
}

// A transcript store with the contract of readSessionTranscriptRawDelta: an
// opaque cursor of generation and last seq, pages after it, a reset when the
// transcript was replaced, and missing when it does not exist.
function transcriptStore() {
  const store = { generation: "g1", events: [], reads: [] };
  store.append = (...events) => {
    for (const event of events) store.events.push({ seq: store.events.length, event });
  };
  store.replace = (generation, events) => {
    store.generation = generation;
    store.events = [];
    store.append(...events);
  };
  store.readDelta = async (params) => {
    store.reads.push(params);
    if (store.missing) return { kind: "missing" };
    const bootstrap = Buffer.from(JSON.stringify({ generation: store.generation, lastSeq: -1 })).toString("base64url");
    const cursor = params.cursor ? JSON.parse(Buffer.from(params.cursor, "base64url").toString()) : { generation: store.generation, lastSeq: -1 };
    if (cursor.generation !== store.generation) return { kind: "reset", cursor: bootstrap, reason: "generation_mismatch" };
    if (store.requiredBytes && params.maxBytes < store.requiredBytes) {
      return { kind: "page", cursor: params.cursor ?? bootstrap, events: [], hasMore: true, requiredBytes: store.requiredBytes, serializedBytes: 0 };
    }
    const after = store.events.filter((item) => item.seq > cursor.lastSeq);
    const page = after.slice(0, params.maxEvents);
    const lastSeq = page.length ? page[page.length - 1].seq : cursor.lastSeq;
    return {
      kind: "page",
      cursor: Buffer.from(JSON.stringify({ generation: store.generation, lastSeq })).toString("base64url"),
      events: page, hasMore: after.length > page.length, serializedBytes: 0,
    };
  };
  return store;
}

// The exporter's clock: it starts before the events of the tests.
const START = "2023-11-14T00:00:00.000Z";

function exporterFor(dir, readDelta, options = {}) {
  return createExporter({ dir, readDelta, redact, now: () => START, ...options });
}

function temporaryDir(t) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "cetirun-session-log-"));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  return dir;
}

function readRecords(dir, name = "openclaw.jsonl") {
  const file = path.join(dir, name);
  if (!fs.existsSync(file)) return [];
  return fs.readFileSync(file, "utf8").split("\n").filter(Boolean).map((line) => JSON.parse(line));
}

const conversation = [
  { type: "session", id: "header", cwd: "/home/vibe" },
  { type: "model_change", id: "model", modelId: "gpt-test" },
  message("e-user", { role: "user", content: [{ type: "text", text: `deploy with key ${SECRET}` }, { type: "image", data: "aGVsbG8=" }] }),
  message("e-assistant", {
    role: "assistant", model: "gpt-test", provider: "openai", __openclaw: { runId: "run-1" },
    providerReplay: { encrypted: "replay-data" },
    content: [
      { type: "thinking", thinking: "private reasoning" },
      { type: "text", text: "Checking the cluster." },
      { type: "toolCall", id: "call-1", name: "exec", arguments: { command: "kubectl get pods" } },
    ],
  }),
  message("e-tool", { role: "toolResult", toolCallId: "call-1", toolName: "exec", isError: false, content: [{ type: "text", text: "pod-a Running" }], details: { exitCode: 0 } }),
  { type: "custom", id: "custom", customType: "memory", data: { note: "internal" } },
  { type: "compaction", id: "compaction", summary: "older turns" },
];

test("exports user input, assistant output, and tool calls and results, and nothing else", async (t) => {
  const dir = temporaryDir(t);
  const store = transcriptStore();
  store.append(...conversation);
  const exporter = exporterFor(dir, store.readDelta);
  await exporter.schedule(target);
  const records = readRecords(dir);
  assert.deepEqual(records.map((record) => record.kind), ["user", "assistant", "tool_call", "tool_result"]);
  assert.deepEqual(records.map((record) => record.id), [
    "openclaw:main:session-1:e-user", "openclaw:main:session-1:e-assistant",
    "openclaw:main:session-1:e-assistant:2", "openclaw:main:session-1:e-tool",
  ]);
  for (const record of records) {
    assert.equal(record.v, 1);
    assert.equal(record.runtime, "openclaw");
    assert.equal(record.sessionKey, target.sessionKey);
    assert.equal(record.truncated, false);
  }
  assert.equal(records[0].content, "deploy with key sk-syn...0000\n[image]");
  assert.equal(records[1].content, "Checking the cluster.");
  assert.equal(records[1].runId, "run-1");
  assert.equal(records[1].model, "gpt-test");
  assert.deepEqual([records[2].tool, records[2].toolCallId, JSON.parse(records[2].content)], ["exec", "call-1", { command: "kubectl get pods" }]);
  assert.deepEqual([records[3].tool, records[3].isError, records[3].content], ["exec", false, "pod-a Running"]);
  assert.ok(!("via" in records[2]) && !("via" in records[3]));
  const text = fs.readFileSync(path.join(dir, "openclaw.jsonl"), "utf8");
  for (const absent of [SECRET, "private reasoning", "replay-data", "older turns", "internal", "exitCode"]) {
    assert.ok(!text.includes(absent), `${absent} must not be exported`);
  }
});

// Tools that OpenClaw 2026.9.6 runs through Tool Search: the transcript has a
// tool_call call, and a tool_call result with the tool that ran in its details.
const throughToolCall = (index, args) => ({ type: "toolCall", id: `call-${index}`, name: "tool_call", arguments: args });
const SESSION_STATUS = { id: "openclaw:core:session_status", name: "session_status", source: "openclaw" };

test("a tool that runs through tool_call is named in its call record", () => {
  const records = recordsFor(message("e-calls", {
    role: "assistant", model: "gpt-test",
    content: [
      throughToolCall(0, { id: "session_status", args: {} }),
      throughToolCall(1, { id: "openclaw:cetirun-memory-context:cetirun_memory", args: { command: "query" } }),
      throughToolCall(2, { args: { id: "mcp:docs:search_docs", query: "pods" } }),
      throughToolCall(3, JSON.stringify({ toolId: "browser" })),
      throughToolCall(4, { name: "web_fetch", args: { url: "https://example.com" } }),
      throughToolCall(5, { args: {} }),
      { type: "toolCall", id: "call-6", name: "process", arguments: { action: "poll", id: "proc-1" } },
    ],
  }), target, redact);
  assert.deepEqual(records.map((record) => [record.tool, record.via]), [
    ["session_status", "tool_call"], ["cetirun_memory", "tool_call"], ["search_docs", "tool_call"],
    ["browser", "tool_call"], ["web_fetch", "tool_call"], ["tool_call", undefined], ["process", undefined],
  ]);
  // The content keeps the arguments as the model sent them.
  assert.deepEqual(JSON.parse(records[1].content), { id: "openclaw:cetirun-memory-context:cetirun_memory", args: { command: "query" } });
});

test("a tool_call result names the tool that ran, unless the call failed before it found one", () => {
  const [ran] = recordsFor(message("e-ran", {
    role: "toolResult", toolCallId: "call-0", toolName: "tool_call", isError: false,
    content: [{ type: "text", text: JSON.stringify({ tool: SESSION_STATUS, result: { content: [] } }, null, 2) }],
    details: { tool: SESSION_STATUS, result: { content: [] } },
  }), target, redact);
  assert.deepEqual([ran.tool, ran.via, ran.toolCallId, ran.isError], ["session_status", "tool_call", "call-0", false]);
  const [failed] = recordsFor(message("e-failed", {
    role: "toolResult", toolCallId: "call-1", toolName: "tool_call", isError: true,
    content: [{ type: "text", text: "{\"status\": \"error\", \"tool\": \"tool_call\", \"error\": \"Unknown tool id: no_such_tool.\"}" }],
    details: { status: "error", tool: "tool_call", error: "Unknown tool id: no_such_tool." },
  }), target, redact);
  assert.deepEqual([failed.tool, failed.via, failed.isError], ["tool_call", undefined, true]);
});

test("a long content is cut to the line limit and marked", () => {
  const [record] = recordsFor(message("e-long", { role: "user", content: "\u00e4".repeat(40000) }), target, (value) => value);
  const line = encodeRecord(record);
  assert.ok(Buffer.byteLength(line) <= MAX_LINE_BYTES);
  const parsed = JSON.parse(line);
  assert.equal(parsed.truncated, true);
  assert.ok(parsed.content.length > 1000);
  assert.ok(!parsed.content.includes("\ufffd"));
});

test("a restart continues from the saved cursor without repeating records", async (t) => {
  const dir = temporaryDir(t);
  const store = transcriptStore();
  store.append(...conversation.slice(0, 3));
  await exporterFor(dir, store.readDelta).schedule(target);
  store.append(message("e-second", { role: "user", content: "second question" }));
  // A new process: no update arrives for the session, but its saved cursor is resumed.
  await exporterFor(dir, store.readDelta).resume();
  assert.deepEqual(readRecords(dir).map((record) => record.id), ["openclaw:main:session-1:e-user", "openclaw:main:session-1:e-second"]);
});

test("a replaced transcript is exported again with the ids that its events had", async (t) => {
  const dir = temporaryDir(t);
  const store = transcriptStore();
  store.append(...conversation);
  const exporter = exporterFor(dir, store.readDelta);
  await exporter.schedule(target);
  store.replace("g2", [conversation[2], message("e-after", { role: "user", content: "after the rewrite" })]);
  await exporter.schedule(target);
  const ids = readRecords(dir).map((record) => record.id);
  assert.deepEqual(ids.slice(4), ["openclaw:main:session-1:e-user", "openclaw:main:session-1:e-after"]);
  assert.equal(ids.filter((id) => id === "openclaw:main:session-1:e-user").length, 2);
});

test("the cursor of a session that no longer exists is dropped", async (t) => {
  const dir = temporaryDir(t);
  const store = transcriptStore();
  store.append(...conversation.slice(0, 3));
  const exporter = exporterFor(dir, store.readDelta);
  await exporter.schedule(target);
  assert.ok(JSON.parse(fs.readFileSync(path.join(dir, "cursors.json"), "utf8")).sessions["main/session-1"]);
  store.missing = true;
  await exporter.schedule(target);
  assert.deepEqual(JSON.parse(fs.readFileSync(path.join(dir, "cursors.json"), "utf8")).sessions, {});
});

test("the file rotates at its limit and keeps one predecessor", async (t) => {
  const dir = temporaryDir(t);
  const store = transcriptStore();
  for (let index = 0; index < 40; index += 1) store.append(message(`e-${index}`, { role: "user", content: `question ${index}` }));
  await exporterFor(dir, store.readDelta, { maxFileBytes: 2000 }).schedule(target);
  const current = readRecords(dir);
  const rotated = readRecords(dir, "openclaw.jsonl.1");
  assert.ok(rotated.length > 0);
  assert.ok(fs.statSync(path.join(dir, "openclaw.jsonl")).size <= 2000);
  assert.ok(fs.statSync(path.join(dir, "openclaw.jsonl.1")).size <= 2000);
  const ids = [...rotated, ...current].map((record) => record.id);
  assert.equal(ids[ids.length - 1], "openclaw:main:session-1:e-39");
  for (let index = 1; index < ids.length; index += 1) {
    assert.ok(Number(ids[index].split("e-")[1]) === Number(ids[index - 1].split("e-")[1]) + 1);
  }
});

test("a failed read is retried later", async (t) => {
  const dir = temporaryDir(t);
  const store = transcriptStore();
  store.append(...conversation.slice(0, 3));
  let failures = 1;
  const readDelta = async (params) => {
    if (failures-- > 0) throw new Error("database is locked");
    return store.readDelta(params);
  };
  const warnings = [];
  const exporter = exporterFor(dir, readDelta, { retryMs: 20, log: (level, text) => warnings.push(`${level}: ${text}`) });
  await exporter.schedule(target);
  assert.deepEqual(readRecords(dir), []);
  assert.match(warnings[0], /^warn: session transcript session-1 was not exported: database is locked/);
  await new Promise((resolve) => setTimeout(resolve, 100));
  await exporter.idle();
  assert.deepEqual(readRecords(dir).map((record) => record.kind), ["user"]);
});

test("an event larger than a page is read with the size that it needs", async (t) => {
  const dir = temporaryDir(t);
  const store = transcriptStore();
  store.append(...conversation.slice(0, 3));
  store.requiredBytes = 9 * 1024 * 1024;
  await exporterFor(dir, store.readDelta).schedule(target);
  assert.deepEqual(store.reads.map((read) => read.maxBytes), [8 * 1024 * 1024, 9 * 1024 * 1024]);
  assert.deepEqual(readRecords(dir).map((record) => record.kind), ["user"]);
});

test("updates without a complete session identity are ignored", async (t) => {
  const dir = temporaryDir(t);
  const store = transcriptStore();
  const exporter = exporterFor(dir, store.readDelta);
  await exporter.schedule(undefined);
  await exporter.schedule({ agentId: "main", sessionId: "session-1" });
  assert.deepEqual(store.reads, []);
});

const OLD = Date.parse("2023-11-13T00:00:00.000Z");

test("messages from before the first start stay out, also after a restart", async (t) => {
  const dir = temporaryDir(t);
  const store = transcriptStore();
  store.append(message("e-old", { role: "user", content: "before the session log" }, OLD), message("e-new", { role: "user", content: "after it" }));
  await exporterFor(dir, store.readDelta).schedule(target);
  assert.deepEqual(readRecords(dir).map((record) => record.id), ["openclaw:main:session-1:e-new"]);
  assert.equal(JSON.parse(fs.readFileSync(path.join(dir, "cursors.json"), "utf8")).since, START);
  // A restart keeps the start: a later clock does not hide messages that came after it.
  store.append(message("e-later", { role: "user", content: "one hour later" }, Date.parse("2023-11-14T23:00:00.000Z")));
  await exporterFor(dir, store.readDelta, { now: () => "2023-11-15T00:00:00.000Z" }).resume();
  assert.deepEqual(readRecords(dir).map((record) => record.id).slice(1), ["openclaw:main:session-1:e-later"]);
});

test("the start is saved before the first export", (t) => {
  const dir = temporaryDir(t);
  exporterFor(dir, transcriptStore().readDelta);
  assert.deepEqual(JSON.parse(fs.readFileSync(path.join(dir, "cursors.json"), "utf8")), { version: 1, since: START, sessions: {} });
});

test("saved cursors without a start are kept, and the start is now", async (t) => {
  const dir = temporaryDir(t);
  const store = transcriptStore();
  store.append(message("e-1", { role: "user", content: "exported earlier" }), message("e-2", { role: "user", content: "not yet exported" }));
  const cursor = Buffer.from(JSON.stringify({ generation: "g1", lastSeq: 0 })).toString("base64url");
  fs.writeFileSync(path.join(dir, "cursors.json"), JSON.stringify({ version: 1, sessions: { "main/session-1": { ...target, cursor } } }));
  await exporterFor(dir, store.readDelta).resume();
  assert.deepEqual(readRecords(dir).map((record) => record.id), ["openclaw:main:session-1:e-2"]);
  assert.equal(JSON.parse(fs.readFileSync(path.join(dir, "cursors.json"), "utf8")).since, START);
});

test("a replaced transcript keeps its messages from before the start out", async (t) => {
  const dir = temporaryDir(t);
  const store = transcriptStore();
  const old = message("e-old", { role: "user", content: "before the session log" }, OLD);
  const kept = message("e-new", { role: "user", content: "after it" });
  store.append(old, kept);
  const exporter = exporterFor(dir, store.readDelta);
  await exporter.schedule(target);
  store.replace("g2", [old, kept, message("e-newer", { role: "user", content: "after the rewrite" })]);
  await exporter.schedule(target);
  assert.deepEqual(readRecords(dir).map((record) => record.id), [
    "openclaw:main:session-1:e-new", "openclaw:main:session-1:e-new", "openclaw:main:session-1:e-newer",
  ]);
});

test("a message without a time is exported", async (t) => {
  const dir = temporaryDir(t);
  const store = transcriptStore();
  store.append({ type: "message", id: "e-untimed", message: { role: "user", content: "no time" } });
  await exporterFor(dir, store.readDelta).schedule(target);
  assert.deepEqual(readRecords(dir).map((record) => record.id), ["openclaw:main:session-1:e-untimed"]);
});
