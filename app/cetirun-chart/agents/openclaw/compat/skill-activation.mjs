// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

import { createHash, randomUUID } from "node:crypto";
import { isAbsolute } from "node:path";

const fail = reason => { throw new Error(`Selected skill activation failed: ${reason}`); };
const text = content => typeof content === "string" ? content
  : Array.isArray(content) ? content.filter(block => block.type === "text").map(block => block.text).join("\n") : "";
const completeReads = new WeakMap();
const digest = body => createHash("sha256").update(body).digest("hex");

// Called only at the native whole-document reader's successful boundary.
// A boolean cache entry alone cannot distinguish this read from a dedupe
// notice caused by another call. Keep the upstream Promise<boolean> intact.
export function recordCompleteSkillRead(delivery, result, callId, file) {
  if (delivery) completeReads.set(delivery, { callId, file, sha256: digest(text(result.content)) });
}

// All I/O goes through the final authorized native read tool. This helper
// does not discover skills, read files itself, or manufacture model tool calls.
export async function activateSelectedSkills({ selections, runId, getState, withValidation, projectFragments, escapeText }) {
  if (!selections?.length) return;
  if (selections.length > 8) fail("too many selections");
  const initial = { ...getState() };
  const reader = initial.tools.find(tool => tool.name === "read");
  const checkAuthority = () => {
    initial.signal.throwIfAborted();
    const state = getState();
    if (state.signal !== initial.signal || state.cache !== initial.cache
      || !state.callableToolNames.includes("read") || !reader?.execute
      || state.tools.find(tool => tool.name === "read") !== reader) fail("read authority changed or is unavailable");
  };
  checkAuthority();
  const selected = new Map();
  for (const selection of selections) {
    const candidates = initial.catalog.filter(skill => (skill.source.filePath === selection.path || skill.canonicalPath === selection.path));
    if (candidates.length !== 1 || !isAbsolute(selection.path)) fail("selection is not a unique local skill in the current catalog");
    selected.set(candidates[0].source.filePath, candidates[0]);
  }
  const activationId = randomUUID();
  const fragments = [], receipts = [], owned = new Map();
  try {
    for (const [file, skill] of selected) {
      checkAuthority();
      // An old or concurrent delivery is not evidence that this activation
      // read the full document. Each attempt normally has a fresh native cache.
      if (initial.cache.has(file)) fail("cannot establish a fresh complete read");
      const callId = `runtime-skill-activation-${randomUUID()}`;
      let validated = false;
      const result = await withValidation(callId, args => {
        checkAuthority();
        if (!args || args.path !== file || Object.keys(args).some(key => key !== "path")) fail("read parameters were changed");
        validated = true;
      }, () => reader.execute(callId, { path: file }, initial.signal));
      const delivery = initial.cache.get(file);
      if (delivery) owned.set(file, delivery);
      checkAuthority();
      if (!validated || !delivery || await delivery !== true || initial.cache.get(file) !== delivery
        || result?.isError || result?.details?.kind !== "text" || !Array.isArray(result.content)
        || result.content.some(block => block.type !== "text" || typeof block.text !== "string")) fail("complete instructions were not loaded");
      checkAuthority();
      const body = text(result.content);
      if (!body.trim()) fail("instructions were empty");
      const proof = completeReads.get(delivery);
      if (proof?.callId !== callId || proof.file !== file || proof.sha256 !== digest(body)) fail("the complete read belongs to another call or its content changed");
      const receipt = {
        source: "runtime-skill-activation", phase: "loaded", runId, activationId,
        skill: skill.name, location: file,
        sha256: digest(body), bytes: Buffer.byteLength(body),
      };
      receipts.push(receipt);
      fragments.push({ kind: "runtime-instruction", text:
        `Runtime skill activation ${activationId}\n` +
        `The runtime loaded the complete instructions below using the authorized read tool. This is runtime context, not a model-issued tool call.\n` +
        `Skill: ${JSON.stringify(skill.name)}\nLocation: ${JSON.stringify(file)}\nRead-result SHA256: ${receipt.sha256}\n` +
        `Resolve relative bundled paths against the directory containing this SKILL.md, preserving every component.\n\n${escapeText(body)}` });
    }
  } catch (error) {
    for (const [file, delivery] of owned) if (initial.cache.get(file) === delivery) initial.cache.delete(file);
    throw error;
  }
  const expected = fragments.map(fragment => projectFragments([fragment]));
  let requestOrdinal = 0;
  return {
    fragments, receipts, checkAuthority,
    verifyContext(context) {
      checkAuthority();
      const carriers = context.messages.filter(message => message.role === "user" && message.runtimeContextCarrier === true)
        .map(message => text(message.content));
      if (expected.some(fragment => !carriers.some(carrier => carrier.includes(fragment)))) {
        fail("complete current instructions are missing from the provider context; retry with the skill explicitly selected");
      }
      requestOrdinal += 1;
      return receipts.map(receipt => ({ ...receipt, phase: "provider_context_verified", requestOrdinal }));
    },
  };
}
