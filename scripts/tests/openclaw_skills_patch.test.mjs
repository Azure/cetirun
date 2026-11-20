// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import assert from "node:assert/strict";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import vm from "node:vm";
import { test } from "node:test";
import { apply, GUIDANCE, original, patch, SOURCE_FILE } from "../../app/cetirun-chart/agents/openclaw/compat/apply-skill-paths.mjs";

// A synthetic function with the upstream integration point. The separate
// pinned-host suite verifies the actual provider request and native tools.
const source = [
  "function buildSkillsSection(params) {",
  "\tconst trimmed = params.skillsPrompt?.trim();",
  "\tif (!trimmed) return [];",
  "\treturn [",
  '\t\t"## Skills",',
  '\t\tparams.codeModeActive ? "native-code-reader" : `native-reader:${params.readToolName}`,',
  '\t\t"Up-front max one. Never invent paths.",',
  "\t\ttrimmed,",
  '\t\t""',
  "\t];",
  "}",
].join("\n") + "\n";

function render(params) {
  return vm.runInNewContext(patch(source) + "\nbuildSkillsSection")(params);
}

test("per-run guidance wraps an unchanged retained catalog and preserves reader routing", () => {
  const skillsPrompt = "<available_skills>\n<location>node://example/SKILL.md</location>\n<location_note>Use the node reader</location_note>\n</available_skills>";
  for (const codeModeActive of [false, true]) {
    const params = { skillsPrompt, codeModeActive, readToolName: "read" };
    const lines = render(params);
    assert.ok(lines.includes(skillsPrompt));
    assert.equal(params.skillsPrompt, skillsPrompt);
    assert.ok(lines.includes(codeModeActive ? "native-code-reader" : "native-reader:read"));
    for (const line of GUIDANCE) assert.equal(lines.filter(value => value === line).length, 1);
    assert.ok(lines.join("\n").includes("preserving every path component"));
    assert.ok(lines.join("\n").includes("Changing to that directory does not remove"));
    assert.ok(lines.join("\n").includes("do not turn a virtual skill location into a local shell path"));
  }
});

test("no skills means no additional guidance", () => {
  for (const skillsPrompt of [undefined, "", " \n\t "]) assert.equal(render({ skillsPrompt }).length, 0);
});

test("patch round-trip preserves unrelated source and rejects ambiguous integration points", () => {
  const input = "// unrelated prefix\n" + source + "// unrelated suffix\n";
  assert.equal(original(patch(input)), input);
  assert.equal(original(input), input);
  assert.throws(() => patch(patch(input)), /integration point changed/);
  assert.throws(() => patch("// missing integration point\n"), /integration point changed/);
  assert.throws(() => patch(source + source), /integration point changed/);
});

test("unknown or damaged native source is rejected without writes, including check mode", async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), "cetirun-skill-patch-"));
  try {
    const file = path.join(root, SOURCE_FILE);
    await mkdir(path.dirname(file), { recursive: true });
    for (const input of [source, patch(source), patch(source).replace("preserving every path component", "damaged")]) {
      await writeFile(file, input);
      for (const check of [false, true]) {
        assert.throws(() => apply(root, { check }), /Unsupported OpenClaw skill prompt source/);
        assert.equal(await readFile(file, "utf8"), input);
      }
    }
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("image applies skill guidance after its last package install", async () => {
  const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
  const dockerfile = await readFile(path.join(root, "app/cetirun-chart/agents/openclaw/Dockerfile"), "utf8");
  const install = "node /tmp/cetirun-openclaw-compat/apply-skill-paths.mjs /usr/local/share/npm-global/lib/node_modules/openclaw";
  assert.ok(dockerfile.indexOf(install) > dockerfile.lastIndexOf("npm install"));
  assert.ok(dockerfile.indexOf(install) < dockerfile.indexOf("rm -r /tmp/cetirun-openclaw-compat"));
});
