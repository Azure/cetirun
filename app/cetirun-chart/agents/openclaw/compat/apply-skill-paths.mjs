// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// OpenClaw 2026.9.6's catalog has a generic relative-path rule, but a model
// can still drop scripts/ after changing to the skill directory. Put explicit
// read-before-use and complete-path guidance in the per-run Skills section,
// outside the catalog that an existing session may retain in skillsSnapshot.
// Do not rewrite shell commands, skill files, or saved session state.

import { createHash } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

export const SOURCE_FILE = "dist/system-prompt-params-DdCWVctq.mjs";
export const SOURCE_SHA256 = "5101538ccfc71d70e0c409b2f1655253539ef6d7ad44574eae78c79058cfb211";
export const GUIDANCE = [
  "Before using a skill's bundled files, read its SKILL.md at the listed location with the available skill-reading tool. Do not infer a command from the skill name or description.",
  "For filesystem skill locations, resolve relative script and asset paths against the directory containing SKILL.md, preserving every path component. Changing to that directory does not remove a scripts/, references/, or assets/ prefix.",
  "For example only: /opt/skills/example/SKILL.md plus scripts/check.sh means /opt/skills/example/scripts/check.sh, never /opt/skills/example/check.sh. Derive the path from the actual installed location and quote shell paths when needed; do not copy this example location.",
  "Keep non-filesystem locations on the route specified by their location_note; do not turn a virtual skill location into a local shell path. Existing tool permissions still apply. Report read or execution failures instead of claiming the skill ran successfully.",
];

const BEFORE = '\t\t"Up-front max one. Never invent paths.",\n';
const AFTER = BEFORE + GUIDANCE.map(line => `\t\t${JSON.stringify(line)},\n`).join("");
const count = (text, part) => text.split(part).length - 1;

export function original(source) {
  return count(source, AFTER) === 1 ? source.replace(AFTER, BEFORE) : source;
}

export function patch(source) {
  if (count(source, BEFORE) !== 1 || count(source, AFTER) !== 0) {
    throw new Error("OpenClaw skill guidance integration point changed; review the pinned source");
  }
  return source.replace(BEFORE, AFTER);
}

export function apply(root, { check = false } = {}) {
  const file = join(root, SOURCE_FILE);
  const source = readFileSync(file, "utf8");
  const published = original(source);
  if (createHash("sha256").update(published).digest("hex") !== SOURCE_SHA256) {
    throw new Error("Unsupported OpenClaw skill prompt source; review the pinned integration");
  }
  const changed = patch(published);
  if (!check && changed !== source) writeFileSync(file, changed);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  if (!process.argv[2]) throw new Error("Provide the pinned OpenClaw package root");
  apply(process.argv[2], { check: process.argv.includes("--check") });
}
