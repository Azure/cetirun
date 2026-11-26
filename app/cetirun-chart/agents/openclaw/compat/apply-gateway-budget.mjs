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
// When a provider refuses the Gateway's own credentials, the Gateway answers
// 502 with one of its error codes (CREDENTIALS_COPIES). OpenClaw retried a 502
// eight times in 90 seconds as a temporary provider failure, and then said to
// try again shortly; it shows no text of the provider. This gives each code a
// text that says who can fix the credentials, for the failed turn and the
// error of the run, and ends the turn at the first such error, without a retry
// or a fallback, as OpenClaw does for its own final errors.
//
// It changes four chunks of the published package, each pinned by its digest,
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
// The Gateway's error codes when a provider refused its credentials (cetirun_ai_gateway/guard.py), and the
// text of each: a shared model's provider, which an administrator configures, or an Agent's GitHub Copilot
// route, which the owner's sign-in configures.
export const CREDENTIALS_COPIES = {
  cetirun_provider_credentials:
    "⚠️ The provider of this model refused the credentials of the CetiRun AI Gateway, so the model did not answer. " +
    "The key of this Agent is not the cause. Ask a CetiRun administrator to fix the provider configuration of the model.",
  cetirun_copilot_credentials:
    "⚠️ The GitHub Copilot route of this model refused its credentials, so the model did not answer. " +
    "The key of this Agent is not the cause. The owner of this Agent can sign in to GitHub Copilot again in the Portal. " +
    "If that does not help, ask a CetiRun administrator to examine the route.",
};
const CREDENTIALS_CODES = Object.keys(CREDENTIALS_COPIES);

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
// The module constants that the function reads for these checks, with OpenClaw's values.
const REPLY_CONTEXT = {
  ASSISTANT_REQUEST_FAILURE_REASON: {
    billing: "provider billing issue", rate_limit: "rate limited", model_not_found: "model not found",
    server_error: "provider internal error", timeout: "request timed out",
  },
  STORAGE_FAILURE_COPY: {},
};
const REPLY = {
  name: "renderAssistantRequestFailureCopy",
  before: REPLY_BILLING,
  after: `\tif (facts.reason === "billing" && (!provider || provider.toLowerCase() === ${JSON.stringify(PROVIDER)})) ` +
    `return ${JSON.stringify(BUDGET_COPY)};\n` + REPLY_BILLING,
  context: REPLY_CONTEXT,
};

// The same function renders the failed turn (from its saved error code) and the error of the run (from the
// error's facts), with the error code of the answer as facts.code, before it looks at the reason: OpenClaw
// takes a 502 whose type names a server error for a timeout.
const UNFINISHED_TOOL_CALL =
  '\tif (facts.code === "incomplete_tool_call") return "⚠️ The provider returned an unfinished tool call. ' +
  'Earlier actions may have completed; verify their results before continuing.";\n';
const CREDENTIALS = {
  name: "renderAssistantRequestFailureCopy",
  before: UNFINISHED_TOOL_CALL,
  after: UNFINISHED_TOOL_CALL + Object.entries(CREDENTIALS_COPIES)
    .map(([code, copy]) => `\tif (facts.code === ${JSON.stringify(code)}) return ${JSON.stringify(copy)};\n`).join(""),
  context: REPLY_CONTEXT,
};

// OpenClaw's final errors: no same-model retry, profile rotation, model fallback, cooldown or resubmission.
const TERMINAL_CHECK = "\treturn Boolean(message?.errorCode && TERMINAL_ASSISTANT_ERROR_CODES.has(message.errorCode))";
const TERMINAL = {
  name: "isTerminalAssistantError",
  before: TERMINAL_CHECK,
  after: `\treturn ${JSON.stringify(CREDENTIALS_CODES)}.includes(message?.errorCode) || ` +
    TERMINAL_CHECK.slice("\treturn ".length),
  // The function's other checks, as OpenClaw's answer a message without a final error code.
  context: {
    TERMINAL_ASSISTANT_ERROR_CODES: new Set(["provider_post_dispatch_ambiguity"]),
    resolveResponsesOutputIdentityRetry: () => undefined,
    isProviderRefusalAssistantError: () => false,
  },
};

// OpenClaw 2026.9.6, as published (sha512-Ie0kyQSC... in the Dockerfile).
export const FILES = {
  "dist/user-copy-DfLWtdTx.mjs": {
    sha256: "1b85f5fcfff49bdcb428f0b6fa21e786261ddc88f37a930b84ee5d391865a644",
    edits: [BILLING],
  },
  "dist/usage-state-Bm5iXGhR.mjs": {
    sha256: "4abd9dab006d0b708e69e869ab8e56d92312cd074ac8ef7fc04b33999be9c60a",
    edits: [BYPASS],
  },
  "dist/assistant-request-failure-copy-D0cfsUk2.mjs": {
    sha256: "7ec3637305985d3e929398f886c463ec800419649dab85e01f646afc83475ebc",
    edits: [REPLY, CREDENTIALS],
  },
  "dist/retry-MEyN9HUz.mjs": {
    sha256: "13873c585c13ccde6665fc12885c54f648ea98563a1a2f9a78294d43456fdcba",
    edits: [TERMINAL],
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

/** Run the changed function of a changed source: the Gateway gets CetiRun's text and no cooldown or retry. */
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
  } else if (edit === CREDENTIALS) {
    for (const [code, copy] of Object.entries(CREDENTIALS_COPIES)) {
      for (const facts of [{ code }, { code, status: 502 }, { code, reason: "timeout", status: 502, provider: PROVIDER, model: "gpt-6" }]) {
        if (changed(facts) !== copy) throw new Error(`the text for ${JSON.stringify(facts)} is not CetiRun's`);
      }
    }
    const texts = Object.values(CREDENTIALS_COPIES);
    for (const facts of [{ code: "502", reason: "server_error", status: 502 }, { reason: "billing", status: 402, provider: "openai" }]) {
      if (texts.includes(changed(facts))) throw new Error(`the text for ${JSON.stringify(facts)} changed`);
    }
  } else if (edit === TERMINAL) {
    for (const code of CREDENTIALS_CODES) {
      if (changed({ errorCode: code }) !== true) throw new Error(`the error ${code} does not end the turn`);
    }
    for (const [message, expected] of [[{ errorCode: "provider_post_dispatch_ambiguity" }, true], [{ errorCode: "502" }, false], [{}, false], [undefined, false]]) {
      if (changed(message) !== expected) throw new Error(`OpenClaw's own check of ${JSON.stringify(message)} changed`);
    }
  } else {
    for (const [name, expected] of [[PROVIDER, true], ["openrouter", true], ["kilocode", true], ["openai", false]]) {
      if (changed(name) !== expected) throw new Error(`the cooldown of ${name} is not left to the provider as expected`);
    }
  }
}

export function apply(root, { check = false } = {}) {
  for (const [file, { sha256, edits }] of Object.entries(FILES)) {
    const path = join(root, file);
    const published = edits.reduceRight((source, edit) => original(source, edit), readFileSync(path, "utf8"));
    if (createHash("sha256").update(published).digest("hex") !== sha256) {
      throw new Error(`Unsupported OpenClaw source ${file}; review the pinned integration`);
    }
    const changed = edits.reduce((source, edit) => patch(source, edit), published);
    for (const edit of edits) verify(changed, edit);
    if (!check) {
      writeFileSync(path, changed);
      const written = readFileSync(path, "utf8");
      for (const edit of edits) verify(written, edit);
    }
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const root = process.argv[2];
  if (!root) throw new Error("Provide the pinned OpenClaw package root");
  apply(root, { check: process.argv.includes("--check") });
}
