// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import assert from "node:assert/strict";
import { chmodSync, existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, unlinkSync, utimesSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { test } from "node:test";
import { apply, original, patch, SOURCES } from "../../app/cetirun-chart/agents/openclaw/compat/apply-plugin-capture-tmp.mjs";
import { pluginCaptureDirectory } from "../../app/cetirun-chart/agents/openclaw/compat/plugin-capture-tmp.mjs";

function temporary(run) {
  const root = mkdtempSync(path.join(os.tmpdir(), "cetirun-capture-test-"));
  const previous = process.env.TMPDIR;
  process.env.TMPDIR = root;
  return Promise.resolve().then(() => run(root)).finally(() => {
    if (previous === undefined) delete process.env.TMPDIR;
    else process.env.TMPDIR = previous;
    rmSync(root, { recursive: true, force: true });
  });
}

test("capture storage is local, private, stable, and separated by state directory", () => temporary(root => {
  const first = pluginCaptureDirectory("/synthetic/home-one/.openclaw");
  assert.ok(first.startsWith(root + path.sep));
  assert.equal(first, pluginCaptureDirectory("/synthetic/home-one/../home-one/.openclaw"));
  assert.notEqual(first, pluginCaptureDirectory("/synthetic/home-two/.openclaw"));
  assert.equal(lstatSync(path.dirname(first)).mode & 0o777, 0o700);
  assert.equal(lstatSync(path.dirname(first)).uid, process.getuid());
}));

test("a pre-created link, file, or permissive capture root is rejected", () => temporary(root => {
  const parent = path.join(root, `cetirun-openclaw-captures-${process.getuid()}`);
  const victim = path.join(root, "untouched");
  mkdirSync(victim);
  writeFileSync(path.join(victim, "marker"), "preserve");
  symlinkSync(victim, parent);
  assert.throws(() => pluginCaptureDirectory("/state"), /Unsafe/);
  unlinkSync(parent);
  writeFileSync(parent, "not a directory");
  assert.throws(() => pluginCaptureDirectory("/state"), /Unsafe/);
  rmSync(parent);
  mkdirSync(parent, { mode: 0o700 });
  chmodSync(parent, 0o755);
  assert.throws(() => pluginCaptureDirectory("/state"), /Unsafe/);
  assert.equal(readFileSync(path.join(victim, "marker"), "utf8"), "preserve");
}));

test("the state-specific capture directory also rejects links, files, and unsafe permissions", () => temporary(root => {
  const stateRoot = pluginCaptureDirectory("/state");
  rmSync(stateRoot, { recursive: true });
  const victim = path.join(root, "untouched");
  mkdirSync(victim);
  writeFileSync(path.join(victim, "marker"), "preserve");
  symlinkSync(victim, stateRoot);
  assert.throws(() => pluginCaptureDirectory("/state"), /Unsafe/);
  unlinkSync(stateRoot);
  writeFileSync(stateRoot, "not a directory");
  assert.throws(() => pluginCaptureDirectory("/state"), /Unsafe/);
  rmSync(stateRoot);
  mkdirSync(stateRoot, { mode: 0o700 });
  chmodSync(stateRoot, 0o755);
  assert.throws(() => pluginCaptureDirectory("/state"), /Unsafe/);
  assert.equal(readFileSync(path.join(victim, "marker"), "utf8"), "preserve");
}));

test("patches round-trip and reject missing or ambiguous integration points", () => {
  for (const target of SOURCES) {
    const source = `// prefix\n${target.before}\n// suffix\n`;
    assert.equal(original(patch(source, target), target), source);
    assert.throws(() => patch(patch(source, target), target), /integration point/);
    assert.throws(() => patch(source + source, target), /integration point/);
    assert.throws(() => patch("", target), /integration point/);
  }
});

test("unrecognized upstream source fails without writing", () => temporary(root => {
  for (const target of SOURCES) {
    const file = path.join(root, target.file);
    mkdirSync(path.dirname(file), { recursive: true });
    writeFileSync(file, target.before);
  }
  for (const check of [true, false]) {
    assert.throws(() => apply(root, { check }), /Unsupported/);
    for (const target of SOURCES) assert.equal(readFileSync(path.join(root, target.file), "utf8"), target.before);
    assert.equal(existsSync(path.join(root, "dist/cetirun-plugin-capture-tmp.mjs")), false);
  }
}));

const nativeRoot = process.env.CETIRUN_OPENCLAW_SOURCE;
test("pinned package patch is checked before writes and is idempotent", { skip: !nativeRoot }, () => temporary(root => {
  const cloudFiles = ["worker.mjs", "sqlite-store.worker.mjs"].map(name => path.join(root, "dist/worker", name));
  mkdirSync(path.dirname(cloudFiles[0]), { recursive: true });
  for (const file of cloudFiles) writeFileSync(file, "// standalone cloud bundle must remain unchanged\n");
  for (const target of SOURCES) {
    const file = path.join(root, target.file);
    mkdirSync(path.dirname(file), { recursive: true });
    writeFileSync(file, readFileSync(path.join(nativeRoot, target.file)));
  }
  apply(root, { check: true });
  assert.equal(existsSync(path.join(root, "dist/cetirun-plugin-capture-tmp.mjs")), false);
  apply(root);
  const once = SOURCES.map(target => readFileSync(path.join(root, target.file), "utf8"));
  apply(root);
  assert.deepEqual(SOURCES.map(target => readFileSync(path.join(root, target.file), "utf8")), once);
  for (const file of cloudFiles) assert.equal(readFileSync(file, "utf8"), "// standalone cloud bundle must remain unchanged\n");
  const last = path.join(root, SOURCES.at(-1).file);
  writeFileSync(last, readFileSync(last, "utf8") + "\n// unexpected source edit\n");
  const damaged = readFileSync(last, "utf8");
  assert.throws(() => apply(root), /Unsupported/);
  assert.equal(readFileSync(last, "utf8"), damaged);
}));

// Run these against the patched pinned image, with its native dependencies.
const nativeRuntime = process.env.CETIRUN_OPENCLAW_CAPTURE_NATIVE === "1" && nativeRoot;
test("native snapshot retains source validation and digest across scratch locations", { skip: !nativeRuntime }, () => temporary(async root => {
  const { t: capture } = await import(pathToFileURL(path.join(nativeRoot, "dist/plugin-generation-artifact-VyjUi6ei.mjs")));
  const plugin = path.join(root, "plugin");
  mkdirSync(plugin);
  writeFileSync(path.join(plugin, "package.json"), '{"name":"synthetic-plugin","type":"module"}');
  const entry = path.join(plugin, "index.js");
  writeFileSync(entry, "export const value = 42;\n");
  const first = capture(plugin, entry, run => run());
  const second = capture(plugin, entry, run => run());
  try {
    assert.ok(first.boundaryRoot.startsWith(pluginCaptureDirectory(process.env.OPENCLAW_STATE_DIR) + path.sep));
    assert.notEqual(first.boundaryRoot, second.boundaryRoot);
    assert.equal(first.sourceDigest, second.sourceDigest);
    first.assertSourceCurrent();
    first.dispose();
    assert.equal(existsSync(first.boundaryRoot), false);
    assert.equal(existsSync(second.boundaryRoot), true);
    second.assertSourceCurrent();
    writeFileSync(entry, "export const value = 43;\n");
    assert.throws(() => second.assertSourceCurrent(), /source changed/);
  } finally {
    second.dispose();
  }
}));

test("native sweep preserves active captures and release reclaims them", { skip: !nativeRuntime }, () => temporary(async root => {
  const { n: retain, r: sweep } = await import(pathToFileURL(path.join(nativeRoot, SOURCES[0].file)));
  const state = path.join(root, "persistent-state");
  const first = retain(state);
  const second = retain(state);
  const capture = first.createDirectory();
  const instance = path.dirname(path.dirname(capture));
  const old = new Date(Date.now() - 2 * 3600000);
  utimesSync(instance, old, old);
  await sweep(state);
  assert.equal(existsSync(capture), true);
  first.release();
  assert.equal(existsSync(capture), true);
  second.release();
  assert.equal(existsSync(instance), false);
  assert.equal(existsSync(state), false);
}));
