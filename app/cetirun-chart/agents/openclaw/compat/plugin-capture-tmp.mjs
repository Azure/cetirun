// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { createHash } from "node:crypto";
import { lstatSync, mkdirSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";

// Plugin generations are process-owned scratch copies, not persisted state.
// Keep their thousands of small writes off a network-backed Agent HOME.
function ensurePrivateDirectory(root, uid) {
  try {
    mkdirSync(root, { mode: 0o700 });
  } catch (error) {
    if (error.code !== "EEXIST") throw error;
  }
  // Both creation and the upstream sweeper use this entry point. Never follow
  // a pre-created link or sweep a directory owned by another local user.
  const stat = lstatSync(root);
  if (!stat.isDirectory() || stat.uid !== uid || (stat.mode & 0o777) !== 0o700) {
    throw new Error("Unsafe OpenClaw plugin capture temporary directory");
  }
  return root;
}

export function pluginCaptureDirectory(stateDir) {
  const uid = process.getuid();
  const root = ensurePrivateDirectory(path.join(tmpdir(), `cetirun-openclaw-captures-${uid}`), uid);
  const stateKey = createHash("sha256").update(path.resolve(stateDir)).digest("hex");
  return ensurePrivateDirectory(path.join(root, stateKey), uid);
}
