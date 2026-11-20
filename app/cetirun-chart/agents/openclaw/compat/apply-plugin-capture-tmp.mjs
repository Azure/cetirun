// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// OpenClaw 2026.9.6 puts ephemeral plugin source snapshots under its state
// directory. Redirect only their storage; retain capture validation, leases,
// worker custody, recovery, and cleanup. Local workers use the same chunk.
// Leave standalone dist/worker cloud bundles unchanged: CetiRun does not
// configure them, and their published artifact set does not include our helper.
import { createHash } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";

export const SOURCES = [
  {
    file: "dist/plugin-source-capture-directory-DMhuTSdY.mjs",
    sha256: "df58a5f67d4eb5b7ab69bc91ef0fd3352f015baa2f15d168d2edf4a8291ddeaf",
    before: 'function instanceDirectory(stateDir) {\n\treturn path.join(stateDir, "tmp", "plugin-captures");\n}',
    after: "function instanceDirectory(stateDir) {\n\treturn cetirunPluginCaptureDirectory(stateDir);\n}",
    helper: "./cetirun-plugin-capture-tmp.mjs",
  },
];

const count = (source, part) => source.split(part).length - 1;
const header = target => `import { pluginCaptureDirectory as cetirunPluginCaptureDirectory } from ${JSON.stringify(target.helper)};\n`;

export function original(source, target) {
  return source.startsWith(header(target)) && count(source, target.after) === 1
    ? source.slice(header(target).length).replace(target.after, target.before)
    : source;
}

export function patch(source, target) {
  if (count(source, target.before) !== 1 || count(source, target.after) !== 0) {
    throw new Error("OpenClaw plugin capture integration point changed; review the pinned source");
  }
  return header(target) + source.replace(target.before, target.after);
}

export function apply(root, { check = false } = {}) {
  // Validate every target before writing any file.
  const updates = SOURCES.map(target => {
    const file = path.join(root, target.file);
    const source = readFileSync(file, "utf8");
    const published = original(source, target);
    if (createHash("sha256").update(published).digest("hex") !== target.sha256) {
      throw new Error(`Unsupported OpenClaw plugin capture source: ${target.file}`);
    }
    return { file, source, changed: patch(published, target) };
  });
  if (!check) {
    writeFileSync(path.join(root, "dist/cetirun-plugin-capture-tmp.mjs"), readFileSync(new URL("./plugin-capture-tmp.mjs", import.meta.url)));
    for (const { file, source, changed } of updates) if (source !== changed) writeFileSync(file, changed);
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  if (!process.argv[2]) throw new Error("Provide the pinned OpenClaw package root");
  apply(process.argv[2], { check: process.argv.includes("--check") });
}
