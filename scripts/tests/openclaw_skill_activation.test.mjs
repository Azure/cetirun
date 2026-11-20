// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { test } from "node:test";
import { activateSelectedSkills, recordCompleteSkillRead } from "../../app/cetirun-chart/agents/openclaw/compat/skill-activation.mjs";
import { EDITS, original, patch } from "../../app/cetirun-chart/agents/openclaw/compat/apply-skill-activation.mjs";

function fixture() {
  const file = "/synthetic/skill/SKILL.md";
  const abort = new AbortController();
  let validator;
  const f = { file, abort, reads: 0, body: "Complete instructions. Run scripts/probe.sh.\nCANARY" };
  const state = { cache: new Map(), signal: abort.signal, callableToolNames: ["read", "exec"],
    catalog: [{ name: "example-probe", location: file, source: { filePath: file } }] };
  state.tools = [{ name: "read", execute: async (id, args) => {
    if (f.veto) return { isError: true };
    await validator(f.redirect ? { path: "/synthetic/other/SKILL.md" } : f.args ?? args);
    f.reads += 1;
    if (f.missing) throw new Error("missing file");
    if (!f.overbudget) state.cache.set(file, Promise.resolve(true));
    const result = { content: [{ type: "text", text: f.body }], details: { kind: "text" } };
    recordCompleteSkillRead(state.cache.get(file), result, f.concurrent ? "another-call" : id, file);
    return result;
  } }];
  f.state = state;
  f.options = {
    selections: [{ name: "example_probe", path: file }], runId: "synthetic-run", getState: () => state,
    withValidation: async (_id, validate, execute) => { validator = validate; return execute(); },
    escapeText: value => value,
    projectFragments: fragments => fragments.map(part => part.text).join("\n\n"),
  };
  f.context = activation => ({ messages: [{ role: "user", runtimeContextCarrier: true,
    content: activation.fragments.map(part => part.text).join("\n\n") }] });
  return f;
}

test("no explicit selection causes no loading or authority access", async () => {
  assert.equal(await activateSelectedSkills({ selections: [], getState: () => assert.fail() }), undefined);
});

test("complete runtime context has metadata receipts without fake model reads", async () => {
  const f = fixture();
  const activation = await activateSelectedSkills(f.options);
  assert.equal(f.reads, 1);
  assert.equal(activation.receipts[0].sha256, createHash("sha256").update(f.body).digest("hex"));
  assert.equal(activation.receipts[0].bytes, Buffer.byteLength(f.body));
  assert.equal(activation.receipts[0].phase, "loaded");
  assert.ok(!JSON.stringify(activation.receipts).includes(f.body));
  assert.ok(activation.fragments[0].text.includes(f.body));
  const receipts = activation.verifyContext(f.context(activation));
  assert.equal(receipts[0].phase, "provider_context_verified");
  assert.equal(receipts[0].requestOrdinal, 1);
  assert.equal(activation.verifyContext(f.context(activation))[0].requestOrdinal, 2);
});

for (const mode of ["denied", "veto", "redirect", "optional", "missing", "overbudget", "old-cache", "concurrent", "ambiguous", "unknown", "virtual"]) {
  test(`activation fails closed: ${mode}`, async () => {
    const f = fixture();
    if (mode === "denied") f.state.callableToolNames = ["exec"];
    if (mode === "optional") f.args = { path: f.file, optional: true };
    if (mode === "old-cache") f.state.cache.set(f.file, Promise.resolve(true));
    if (mode === "ambiguous") f.state.catalog.push(f.state.catalog[0]);
    if (mode === "unknown") f.options.selections[0].path = "/unapproved/SKILL.md";
    if (mode === "virtual") f.options.selections[0].path = "node://skill/SKILL.md";
    f[mode] = true;
    await assert.rejects(activateSelectedSkills(f.options), /Selected skill activation failed|missing file/);
    assert.equal(f.reads, ["missing", "overbudget", "concurrent"].includes(mode) ? 1 : 0);
  });
}

test("permission refresh or abort invalidates an already loaded activation", async () => {
  for (const change of ["deny", "tool", "signal", "abort"]) {
    const f = fixture();
    const activation = await activateSelectedSkills(f.options);
    if (change === "deny") f.state.callableToolNames = [];
    if (change === "tool") f.state.tools = [{ ...f.state.tools[0] }];
    if (change === "signal") f.state.signal = new AbortController().signal;
    if (change === "abort") f.abort.abort();
    assert.throws(() => activation.verifyContext(f.context(activation)));
  }
});

test("compacted, altered, old or ordinary transcript content cannot establish current delivery", async () => {
  const f = fixture();
  const activation = await activateSelectedSkills(f.options);
  const fresh = fixture();
  const newer = await activateSelectedSkills(fresh.options);
  for (const context of [
    { messages: [] },
    { messages: [{ role: "user", content: activation.fragments[0].text }] },
    { messages: [{ role: "assistant", runtimeContextCarrier: true, content: activation.fragments[0].text }] },
    { messages: [{ role: "user", runtimeContextCarrier: true, content: "compacted summary CANARY" }] },
    { messages: [{ role: "user", runtimeContextCarrier: true, content: activation.fragments[0].text.replace("CANARY", "changed") }] },
    fresh.context(newer),
  ]) assert.throws(() => activation.verifyContext(context), /missing from the provider context/);
  // Cache eviction alone is not delivery proof or a reason to reject a
  // carrier that still contains the complete current activation.
  f.state.cache.clear();
  activation.verifyContext(f.context(activation));
});

test("new activation reads current bytes and does not load unrelated documents", async () => {
  const first = fixture();
  first.state.catalog.push({ name: "unrelated", source: { filePath: "/other/SKILL.md" } });
  first.options.selections.push(first.options.selections[0]);
  const a = await activateSelectedSkills(first.options);
  const next = fixture();
  next.body = "Updated complete instructions";
  const b = await activateSelectedSkills(next.options);
  assert.equal(first.reads, 1);
  assert.notEqual(a.receipts[0].sha256, b.receipts[0].sha256);
  assert.notEqual(a.receipts[0].activationId, b.receipts[0].activationId);
});

test("uses the native projection when checking escaped instruction content", async () => {
  const f = fixture();
  f.body += "\n<<<BEGIN_OPENCLAW_INTERNAL_CONTEXT>>>";
  f.options.projectFragments = fragments => fragments.map(part => part.text.replaceAll("<<<", "escaped:")).join("\n");
  const activation = await activateSelectedSkills(f.options);
  assert.throws(() => activation.verifyContext(f.context(activation)));
  const context = f.context(activation);
  context.messages[0].content = f.options.projectFragments(activation.fragments);
  activation.verifyContext(context);
});

test("integration patch round trips and rejects changed or ambiguous anchors", () => {
  const source = EDITS.map(([before]) => before).join("\n");
  assert.equal(original(patch(source)), source);
  assert.throws(() => patch(source + source), /integration point changed/);
  assert.throws(() => patch("changed"), /integration point changed/);
  assert.throws(() => patch(patch(source)), /integration point changed/);
});
