// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const LOGGER = fileURLToPath(new URL("../../configs/session-logger.mjs", import.meta.url));

function setup(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "cetirun-session-logger-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const dir = path.join(root, "home", ".openclaw", "cetirun-session-log");
  fs.mkdirSync(dir, { recursive: true });
  return { root, dir, current: path.join(dir, "openclaw.jsonl"), rotated: path.join(dir, "openclaw.jsonl.1") };
}

function record(id) {
  return `${JSON.stringify({ v: 1, id, kind: "user", content: `text ${id}` })}\n`;
}

// Start the logger with a state file; it prints one status line, then records.
function start(t, files, stateName = "state.json", env = {}) {
  const child = spawn(process.execPath, [LOGGER], {
    env: {
      ...process.env, AGENT_TYPE: "openclaw", SCAN_INTERVAL: "1",
      CETIRUN_SESSION_LOG_DIR: files.dir, CETIRUN_SESSION_LOGGER_STATE: path.join(files.root, "tmp", stateName), ...env,
    },
    stdio: ["ignore", "pipe", "pipe"],
  });
  const output = { lines: [], errors: "" };
  let buffer = "";
  child.stdout.on("data", (chunk) => {
    buffer += chunk;
    const parts = buffer.split("\n");
    buffer = parts.pop();
    output.lines.push(...parts.map((line) => JSON.parse(line)));
  });
  child.stderr.on("data", (chunk) => {
    output.errors += chunk;
  });
  // "close" comes after the last output.
  const closed = new Promise((resolve) => child.once("close", (code, signal) => resolve({ code, signal })));
  // Stop without the final read; the state is saved after each read.
  const stop = () => {
    if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
    return closed;
  };
  t.after(stop);
  output.stop = stop;
  output.signal = (name) => {
    child.kill(name);
    return closed;
  };
  output.ids = () => output.lines.filter((line) => line.kind !== "logger").map((line) => line.id);
  return output;
}

async function waitFor(check, timeoutMs = 8000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (check()) return;
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  assert.fail("condition not met in time");
}

test("copies the retained files from their start, then each new line once", async (t) => {
  const files = setup(t);
  fs.writeFileSync(files.rotated, record("a") + record("b"));
  fs.writeFileSync(files.current, record("c"));
  const logger = start(t, files);
  await waitFor(() => logger.ids().length === 3);
  assert.equal(logger.lines[0].kind, "logger");
  fs.appendFileSync(files.current, record("d"));
  await waitFor(() => logger.ids().length === 4);
  await new Promise((resolve) => setTimeout(resolve, 1500));
  assert.deepEqual(logger.ids(), ["a", "b", "c", "d"]);
  assert.equal(logger.errors, "");
});

test("a line is copied only when it is complete", async (t) => {
  const files = setup(t);
  fs.writeFileSync(files.current, record("a"));
  const logger = start(t, files);
  await waitFor(() => logger.ids().length === 1);
  const partial = record("b");
  fs.appendFileSync(files.current, partial.slice(0, 10));
  await new Promise((resolve) => setTimeout(resolve, 1500));
  assert.deepEqual(logger.ids(), ["a"]);
  fs.appendFileSync(files.current, partial.slice(10));
  await waitFor(() => logger.ids().length === 2);
  assert.deepEqual(logger.ids(), ["a", "b"]);
});

test("a rotation is followed without losing or repeating a line", async (t) => {
  const files = setup(t);
  fs.writeFileSync(files.current, record("a"));
  const logger = start(t, files);
  await waitFor(() => logger.ids().length === 1);
  // The plugin appends, then rotates, then writes a new file, between two polls.
  fs.appendFileSync(files.current, record("b"));
  fs.renameSync(files.current, files.rotated);
  fs.writeFileSync(files.current, record("c"));
  await waitFor(() => logger.ids().length === 3);
  await new Promise((resolve) => setTimeout(resolve, 1500));
  assert.deepEqual(logger.ids(), ["a", "b", "c"]);
});

test("a restart of the container continues from its position", async (t) => {
  const files = setup(t);
  fs.writeFileSync(files.current, record("a") + record("b"));
  const first = start(t, files);
  await waitFor(() => first.ids().length === 2);
  await first.stop();
  fs.appendFileSync(files.current, record("c"));
  const second = start(t, files);
  await waitFor(() => second.ids().length === 1);
  await new Promise((resolve) => setTimeout(resolve, 1500));
  assert.deepEqual(second.ids(), ["c"]);
});

test("a new Pod copies the retained files again, with the same ids", async (t) => {
  const files = setup(t);
  fs.writeFileSync(files.rotated, record("a"));
  fs.writeFileSync(files.current, record("b"));
  const first = start(t, files, "first.json");
  await waitFor(() => first.ids().length === 2);
  await first.stop();
  // /tmp is new in a new Pod: no state.
  const second = start(t, files, "second.json");
  await waitFor(() => second.ids().length === 2);
  assert.deepEqual(second.ids(), first.ids());
});

test("a file that does not exist yet is waited for", async (t) => {
  const files = setup(t);
  const logger = start(t, files);
  await new Promise((resolve) => setTimeout(resolve, 1500));
  assert.deepEqual(logger.ids(), []);
  fs.writeFileSync(files.current, record("a"));
  await waitFor(() => logger.ids().length === 1);
});

test("after two rotations between two reads, the newer rotated file is copied whole", async (t) => {
  const files = setup(t);
  fs.writeFileSync(files.current, record("a"));
  const first = start(t, files);
  await waitFor(() => first.ids().length === 1);
  await first.stop();
  // The file that the state names is gone; a new file can get its inode number.
  fs.renameSync(files.current, files.rotated);
  fs.writeFileSync(files.current, record("b"));
  fs.renameSync(files.current, files.rotated);
  fs.writeFileSync(files.current, record("c"));
  const second = start(t, files);
  await waitFor(() => second.ids().length === 2);
  await new Promise((resolve) => setTimeout(resolve, 1500));
  assert.deepEqual(second.ids(), ["b", "c"]);
});

test("a new file with the inode number of the old one is not taken for it", async (t) => {
  const files = setup(t);
  fs.writeFileSync(files.current, record("a"));
  const first = start(t, files);
  await waitFor(() => first.ids().length === 1);
  await first.stop();
  fs.rmSync(files.current);
  fs.writeFileSync(files.rotated, record("b"));
  fs.writeFileSync(files.current, record("c"));
  // The new file got the inode number of the old one, as after two rotations.
  const stateFile = path.join(files.root, "tmp", "state.json");
  const state = JSON.parse(fs.readFileSync(stateFile, "utf8"));
  fs.writeFileSync(stateFile, JSON.stringify({ ...state, ino: fs.statSync(files.current).ino }));
  const second = start(t, files);
  await waitFor(() => second.ids().length === 2);
  assert.deepEqual(second.ids(), ["b", "c"]);
});

test("a stop signal ends the logger after one last read", async (t) => {
  const files = setup(t);
  fs.writeFileSync(files.current, record("a"));
  // No regular read comes before the signal.
  const logger = start(t, files, "state.json", { SCAN_INTERVAL: "60" });
  await waitFor(() => logger.ids().length === 1);
  fs.appendFileSync(files.current, record("b"));
  const sent = Date.now();
  assert.deepEqual(await logger.signal("SIGTERM"), { code: 0, signal: null });
  assert.ok(Date.now() - sent < 6000, "the logger stops within the final read delay");
  assert.deepEqual(logger.ids(), ["a", "b"]);
  assert.equal(logger.errors, "");
});
