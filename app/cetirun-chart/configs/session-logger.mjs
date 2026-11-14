// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// session-logger: copies the records of the CetiRun session log to stdout,
// where the cluster's log collection captures them. The runtime's CetiRun
// session log plugin appends them to /home/vibe/.<agent>/cetirun-session-log/
// <agent>.jsonl and rotates that file to <agent>.jsonl.1; this container has
// /home/vibe/.<agent> mounted read-only. The read position is kept in /tmp, so
// a restart of this container continues where it stopped. In a new Pod, it
// copies the retained files again from their start: each record has a stable
// id for removing such repeats.

import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";

const agentType = process.env.AGENT_TYPE === "hermes" ? "hermes" : "openclaw";
const dir = process.env.CETIRUN_SESSION_LOG_DIR || path.join("/home/vibe", `.${agentType}`, "cetirun-session-log");
const current = path.join(dir, `${agentType}.jsonl`);
const rotated = `${current}.1`;
const stateFile = process.env.CETIRUN_SESSION_LOGGER_STATE || "/tmp/cetirun-session-logger/state.json";
const intervalMs = Math.max(1, Number.parseInt(process.env.SCAN_INTERVAL ?? "30", 10) || 30) * 1000;
// At a Pod stop, the runtime stops at the same time: wait this long for its last records.
const FINAL_READ_DELAY_MS = 2000;
const CHUNK = 1024 * 1024;
// The plugin's records are at most 16000 bytes long.
const HEAD_BYTES = 16384;

function stat(file) {
  try {
    return fs.statSync(file);
  } catch (error) {
    if (error.code === "ENOENT") return undefined;
    throw error;
  }
}

// A hash of the first line of file. With the inode number, it identifies the
// file: a new file can get the inode number of a deleted one.
function headOf(file) {
  let fd;
  try {
    fd = fs.openSync(file, "r");
  } catch (error) {
    if (error.code === "ENOENT") return undefined;
    throw error;
  }
  try {
    const buffer = Buffer.alloc(HEAD_BYTES);
    const data = buffer.subarray(0, fs.readSync(fd, buffer, 0, buffer.length, 0));
    const end = data.indexOf(0x0a);
    return crypto.createHash("sha256").update(end < 0 ? data : data.subarray(0, end)).digest("hex");
  } finally {
    fs.closeSync(fd);
  }
}

function loadState() {
  try {
    const state = JSON.parse(fs.readFileSync(stateFile, "utf8"));
    if (typeof state.ino === "number" && typeof state.offset === "number" && state.offset >= 0 &&
      typeof state.head === "string") return state;
  } catch {
    // No usable state: start from the retained files.
  }
  return undefined;
}

function saveState(state) {
  fs.mkdirSync(path.dirname(stateFile), { recursive: true });
  const temporary = `${stateFile}.tmp`;
  fs.writeFileSync(temporary, JSON.stringify(state));
  fs.renameSync(temporary, stateFile);
}

// Write all of buffer to stdout, also when the pipe takes it in parts.
function writeAll(buffer) {
  let written = 0;
  while (written < buffer.length) {
    try {
      written += fs.writeSync(1, buffer, written, buffer.length - written);
    } catch (error) {
      if (error.code !== "EAGAIN") throw error;
      Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 20);
    }
  }
}

// Copy the complete lines of file from offset up to end; return the offset
// after the last complete line. A line that is still being written waits.
function copy(file, offset, end) {
  if (end <= offset) return offset;
  let fd;
  try {
    fd = fs.openSync(file, "r");
  } catch (error) {
    if (error.code === "ENOENT") return offset;
    throw error;
  }
  try {
    let position = offset; // the next byte to read
    let start = offset; // the file offset of the first byte of pending
    let pending = Buffer.alloc(0);
    while (position < end) {
      const buffer = Buffer.alloc(Math.min(CHUNK, end - position));
      const read = fs.readSync(fd, buffer, 0, buffer.length, position);
      if (read === 0) break;
      position += read;
      const data = Buffer.concat([pending, buffer.subarray(0, read)]);
      const last = data.lastIndexOf(0x0a);
      if (last < 0) {
        pending = data;
        continue;
      }
      writeAll(data.subarray(0, last + 1));
      start += last + 1;
      pending = data.subarray(last + 1);
    }
    return start;
  } finally {
    fs.closeSync(fd);
  }
}

let state = loadState();

// Whether file, with stat info, is the file that state describes. Nothing of
// a file is copied before its first line is complete.
function isStateFile(file, info) {
  return info.ino === state.ino && (state.offset === 0 || headOf(file) === state.head);
}

function poll() {
  const now = stat(current);
  if (!now) return;
  if (!state) {
    // First start in this Pod: copy what the plugin kept, oldest first.
    const old = stat(rotated);
    if (old) copy(rotated, 0, old.size);
    state = { ino: now.ino, offset: 0, head: "" };
  }
  if (!isStateFile(current, now)) {
    // The plugin rotated the file. Finish the one that became <agent>.jsonl.1;
    // after a second rotation, that is a newer file, which is copied whole.
    const old = stat(rotated);
    if (old && isStateFile(rotated, old)) copy(rotated, state.offset, old.size);
    else if (old) copy(rotated, 0, old.size);
    state = { ino: now.ino, offset: 0, head: "" };
  }
  if (now.size < state.offset) state = { ino: now.ino, offset: 0, head: "" };
  const offset = copy(current, state.offset, now.size);
  if (offset !== state.offset || !fs.existsSync(stateFile)) {
    if (state.offset === 0 && offset > 0) state.head = headOf(current) ?? "";
    state.offset = offset;
    saveState(state);
  }
}

function pollSafely() {
  try {
    poll();
  } catch (error) {
    process.stderr.write(`session-logger: ${error?.message ?? error}\n`);
  }
}

let timer;
function loop() {
  pollSafely();
  timer = setTimeout(loop, intervalMs);
}

// This process is the container's first process, which ignores a stop signal
// that it does not handle.
let stopping = false;
function stop() {
  if (stopping) return;
  stopping = true;
  clearTimeout(timer);
  setTimeout(() => {
    pollSafely();
    process.exit(0);
  }, FINAL_READ_DELAY_MS);
}
process.on("SIGTERM", stop);
process.on("SIGINT", stop);

writeAll(Buffer.from(`${JSON.stringify({ v: 1, kind: "logger", runtime: agentType, message: `follows ${current}` })}\n`));
loop();
