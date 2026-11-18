// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// The CetiRun AI Gateway is the only model provider of a CetiRun OpenClaw
// Agent: OpenClaw's provider cetirun. When the Agent's Gateway budget is
// spent, OpenClaw 2026.9.6 shows its own text for a billing error, which tells
// the user to top up the provider's billing dashboard, and stops using the
// Gateway key for ten minutes, so a raised budget waits that long. The failed
// turn in the conversation gets another text of OpenClaw, which tells the user
// to check the provider billing.
//
// This gives a billing error of the provider cetirun a text about the CetiRun
// budget, and one without a provider too: in this image, it comes from the
// Gateway. The failed turn gets the same text. It also adds cetirun to the
// providers whose key cooldowns their provider manages, as OpenClaw does for
// openrouter and kilocode: OpenClaw keeps no auth or billing cooldown for the
// Gateway key, and the Gateway refuses each request over budget itself, before
// any model call.
//
// It changes three chunks of the published package, each pinned by its digest,
// and checks each change by running the changed function. The bundles in
// dist/worker, which OpenClaw sends to cloud workers that CetiRun does not
// configure, stay as published: a cloud worker that installs OpenClaw from
// npm must match them.
//
// Usage: node apply-gateway-budget.mjs OPENCLAW_PACKAGE_ROOT [--check]

import { createHash } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import vm from "node:vm";

export const PROVIDER = "cetirun";
export const BUDGET_COPY =
  "⚠️ This Agent is out of budget on the CetiRun AI Gateway, so the Gateway refuses its model requests. " +
  "Ask a CetiRun administrator to raise the budget, or wait until the budget resets.";

const BILLING = {
  name: "formatBillingErrorMessage",
  before: "function formatBillingErrorMessage(provider, model, authMode) {\n",
  after: "function formatBillingErrorMessage(provider, model, authMode) {\n" +
    `\tif (!provider?.trim() || provider.trim().toLowerCase() === ${JSON.stringify(PROVIDER)}) ` +
    `return ${JSON.stringify(BUDGET_COPY)};\n`,
};

const BYPASS = {
  name: "isAuthCooldownBypassedForProvider",
  before: 'return normalized === "openrouter" || normalized === "kilocode";',
  after: `return normalized === "openrouter" || normalized === "kilocode" || normalized === ${JSON.stringify(PROVIDER)};`,
  // The function's one import, as OpenClaw's normalizeProviderId treats these names.
  context: { normalizeProviderId: (value) => String(value).trim().toLowerCase() },
};

// The text of a failed turn. A turn that OpenClaw saved without its provider
// reads "LLM request failed (provider billing issue, HTTP 402). Check provider
// billing and try again."
const REPLY_BILLING =
  '\tif (facts.reason === "billing") return `${summary} Check ${provider ? `${provider} billing` : "provider billing"} and try again.`;\n';
const REPLY = {
  name: "renderAssistantRequestFailureCopy",
  before: REPLY_BILLING,
  after: `\tif (facts.reason === "billing" && (!provider || provider.toLowerCase() === ${JSON.stringify(PROVIDER)})) ` +
    `return ${JSON.stringify(BUDGET_COPY)};\n` + REPLY_BILLING,
  // The module constant that the function reads for these checks, with OpenClaw's values.
  context: {
    ASSISTANT_REQUEST_FAILURE_REASON: { billing: "provider billing issue", rate_limit: "rate limited", model_not_found: "model not found" },
  },
};

// OpenClaw 2026.9.6, as published (sha512-Ie0kyQSC... in the Dockerfile).
export const FILES = {
  "dist/user-copy-DfLWtdTx.mjs": {
    sha256: "1b85f5fcfff49bdcb428f0b6fa21e786261ddc88f37a930b84ee5d391865a644",
    edit: BILLING,
  },
  "dist/usage-state-Bm5iXGhR.mjs": {
    sha256: "4abd9dab006d0b708e69e869ab8e56d92312cd074ac8ef7fc04b33999be9c60a",
    edit: BYPASS,
  },
  "dist/assistant-request-failure-copy-D0cfsUk2.mjs": {
    sha256: "7ec3637305985d3e929398f886c463ec800419649dab85e01f646afc83475ebc",
    edit: REPLY,
  },
};

function count(text, part) {
  return text.split(part).length - 1;
}

/** The source as published: a source this already changed gets its change undone. */
export function original(source, edit) {
  return count(source, edit.after) === 1 ? source.replace(edit.after, edit.before) : source;
}

/** The changed source; the place to change must be in the published source exactly once. */
export function patch(source, edit) {
  if (count(source, edit.before) !== 1 || count(source, edit.after) !== 0) {
    throw new Error(`OpenClaw changed where CetiRun patches ${edit.name}; review the pinned integration`);
  }
  return source.replace(edit.before, edit.after);
}

/** The changed function, run on its own. */
function changedFunction(source, edit) {
  const at = source.indexOf(edit.after);
  const start = at < 0 ? -1 : source.lastIndexOf(`function ${edit.name}(`, at);
  const end = start < 0 ? -1 : source.indexOf("\n}\n", start);
  if (start < 0 || end < 0 || at + edit.after.length > end) {
    throw new Error(`${edit.name} was not changed`);
  }
  return vm.runInNewContext(`${source.slice(start, end + 2)}\n${edit.name}`, { ...edit.context });
}

/** Run the changed function of a changed source: the Gateway gets CetiRun's text and no cooldown. */
export function verify(source, edit) {
  const changed = changedFunction(source, edit);
  if (edit === BILLING) {
    for (const args of [[PROVIDER, "gpt-6", "api-key"], [" CetiRun ", "gpt-6", "token"], [undefined], [""]]) {
      if (changed(...args) !== BUDGET_COPY) throw new Error(`billing text for ${JSON.stringify(args)} is not CetiRun's`);
    }
    if (changed("openai", "gpt-6", "api-key") === BUDGET_COPY) throw new Error("another provider's billing text changed");
  } else if (edit === REPLY) {
    const billing = { reason: "billing", status: 402 };
    for (const facts of [billing, { ...billing, provider: PROVIDER, model: "gpt-6" }, { ...billing, provider: " CetiRun " }]) {
      if (changed(facts) !== BUDGET_COPY) throw new Error(`failed-turn text for ${JSON.stringify(facts)} is not CetiRun's`);
    }
    if (changed({ ...billing, provider: "openai", model: "gpt-6" }) === BUDGET_COPY) {
      throw new Error("another provider's failed-turn text changed");
    }
    for (const facts of [{ reason: "rate_limit", status: 429 }, { reason: "model_not_found", status: 404 }]) {
      if (changed(facts) === BUDGET_COPY) throw new Error(`the failed-turn text for ${JSON.stringify(facts)} changed`);
    }
  } else {
    for (const [name, expected] of [[PROVIDER, true], ["openrouter", true], ["kilocode", true], ["openai", false]]) {
      if (changed(name) !== expected) throw new Error(`the cooldown of ${name} is not left to the provider as expected`);
    }
  }
}

export function apply(root, { check = false } = {}) {
  for (const [file, { sha256, edit }] of Object.entries(FILES)) {
    const path = join(root, file);
    const published = original(readFileSync(path, "utf8"), edit);
    if (createHash("sha256").update(published).digest("hex") !== sha256) {
      throw new Error(`Unsupported OpenClaw source ${file}; review the pinned integration`);
    }
    const changed = patch(published, edit);
    verify(changed, edit);
    if (!check) {
      writeFileSync(path, changed);
      verify(readFileSync(path, "utf8"), edit);
    }
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const root = process.argv[2];
  if (!root) throw new Error("Provide the pinned OpenClaw package root");
  apply(root, { check: process.argv.includes("--check") });
}
