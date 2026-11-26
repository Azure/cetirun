# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Offline tests for the OpenClaw image's patch for errors of the AI Gateway.

The patch changes OpenClaw 2026.9.6's billing text, the text of a failed turn,
its cooldown bypass list, and its check for final errors, in four chunks of the
published package, which these tests do not have. They run the patch on
synthetic chunks of the same shape.
"""

import json
import shutil
import subprocess
import tempfile
import textwrap
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
OPENCLAW = ROOT / "app/cetirun-chart/agents/openclaw"
INSTALLER = OPENCLAW / "compat/apply-gateway-budget.mjs"
# OpenClaw's chunk of user-facing text, its chunk of key usage state, its
# chunk of the text of a failed turn, and its chunk of retry decisions.
TEXT_CHUNK = "dist/user-copy-DfLWtdTx.mjs"
COOLDOWN_CHUNK = "dist/usage-state-Bm5iXGhR.mjs"
REPLY_CHUNK = "dist/assistant-request-failure-copy-D0cfsUk2.mjs"
RETRY_CHUNK = "dist/retry-MEyN9HUz.mjs"
CODES = ("cetirun_provider_credentials", "cetirun_copilot_credentials")

TEXT_SOURCE = textwrap.dedent("""\
    /** Format billing copy with optional provider/model and credential context. */
    function formatBillingErrorMessage(provider, model, authMode) {
    \tconst providerName = provider?.trim();
    \treturn providerName ? `published ${providerName}` : "published";
    }
    const BILLING_ERROR_USER_MESSAGE = formatBillingErrorMessage();
    """)
COOLDOWN_SOURCE = textwrap.dedent("""\
    function isAuthCooldownBypassedForProvider(provider) {
    \tconst normalized = normalizeProviderId(provider ?? "");
    \treturn normalized === "openrouter" || normalized === "kilocode";
    }
    function resolveInlineProviderApiKeyUsageId(provider) {
    \treturn `inline-api-key:${normalizeProviderId(provider)}`;
    }
    """)
REPLY_SOURCE = textwrap.dedent("""\
    const STORAGE_FAILURE_COPY = { SQLITE_BUSY: "published storage" };
    const ASSISTANT_REQUEST_FAILURE_REASON = {
    \trate_limit: "rate limited",
    \tbilling: "provider billing issue",
    \tmodel_not_found: "model not found",
    \tserver_error: "provider internal error",
    \ttimeout: "request timed out"
    };
    /** Render classified facts without exposing raw provider response text. */
    function renderAssistantRequestFailureCopy(facts) {
    \tif (facts.storageFailure) return `published ${STORAGE_FAILURE_COPY[facts.storageFailure]}`;
    \tif (facts.code === "incomplete_tool_call") return "⚠️ The provider returned an unfinished tool call. Earlier actions may have completed; verify their results before continuing.";
    \tconst provider = facts.provider?.trim();
    \tconst model = facts.model?.trim();
    \tconst target = provider && model ? `${provider}/${model}` : provider || model;
    \tconst reason = facts.reason ? ASSISTANT_REQUEST_FAILURE_REASON[facts.reason] : void 0;
    \tconst status = typeof facts.status === "number" ? `HTTP ${facts.status}` : void 0;
    \tconst details = [reason, status].filter(Boolean);
    \tconst summary = `published ${target ? `${target} request failed` : "LLM request failed"}${details.length > 0 ? ` (${details.join(", ")})` : ""}.`;
    \tif (facts.reason === "rate_limit" || facts.reason === "server_error" || facts.reason === "timeout") return `${summary} Try again shortly.`;
    \tif (facts.reason === "billing") return `${summary} Check ${provider ? `${provider} billing` : "provider billing"} and try again.`;
    \treturn summary;
    }
    """)
RETRY_SOURCE = textwrap.dedent("""\
    const TERMINAL_ASSISTANT_ERROR_CODES = new Set(["published_final_code"]);
    function resolveResponsesOutputIdentityRetry(message) {
    \treturn message.identity;
    }
    function isProviderRefusalAssistantError(message) {
    \treturn message?.refusal === true;
    }
    function isTerminalAssistantError(message) {
    \treturn Boolean(message?.errorCode && TERMINAL_ASSISTANT_ERROR_CODES.has(message.errorCode)) || message != null && resolveResponsesOutputIdentityRetry(message) === "stop" || isProviderRefusalAssistantError(message);
    }
    """)
# The facts of the failed turns that the changed reply function gets.
REPLY_FACTS = {
    "cetirun": {"reason": "billing", "status": 402, "provider": "cetirun", "model": "gpt-6"},
    "CetiRun": {"reason": "billing", "status": 402, "provider": " CetiRun "},
    "none": {"reason": "billing", "status": 402},
    "openai": {"reason": "billing", "status": 402, "provider": "openai", "model": "gpt-6"},
    "rate limit": {"reason": "rate_limit", "status": 429},
    # A failure that is not a billing error, and that OpenClaw does not answer before the billing check.
    "model not found": {"reason": "model_not_found", "status": 404, "provider": "cetirun"},
    # OpenClaw takes a 502 whose type names a server error for a timeout, and saves a failed turn without
    # its provider and reason.
    "provider credentials": {"code": CODES[0], "reason": "timeout", "status": 502, "provider": "cetirun", "model": "gpt-6"},
    "saved provider credentials": {"code": CODES[0]},
    "copilot credentials": {"code": CODES[1], "reason": "server_error", "status": 502, "provider": "cetirun"},
    "another 502": {"code": "502", "reason": "server_error", "status": 502, "provider": "cetirun", "model": "gpt-6"},
    "unfinished tool call": {"code": "incomplete_tool_call"},
}
# The messages of failed requests that the changed check of final errors gets.
RETRY_MESSAGES = {
    "provider credentials": {"errorCode": CODES[0]},
    "copilot credentials": {"errorCode": CODES[1]},
    "another 502": {"errorCode": "502"},
    "no code": {},
    "OpenClaw's own final code": {"errorCode": "published_final_code"},
    "refusal": {"refusal": True},
    "output identity": {"identity": "stop"},
}

# Runs the installer's own functions on each synthetic chunk and reports what
# the changed functions return.
RUNNER = """
import vm from "node:vm";
import { pathToFileURL } from "node:url";
const installer = await import(pathToFileURL(process.argv[2]).href);
const [cases, root] = [JSON.parse(process.argv[3]), process.argv[4]];
const edits = {
  text: installer.FILES[%(text)s].edits,
  cooldown: installer.FILES[%(cooldown)s].edits,
  reply: installer.FILES[%(reply)s].edits.slice(0, 1),
  credentials: installer.FILES[%(reply)s].edits.slice(1),
  "reply and credentials": installer.FILES[%(reply)s].edits,
  terminal: installer.FILES[%(retry)s].edits,
};
const [replyFacts, retryMessages] = [%(facts)s, %(messages)s];
const outcome = (run) => { try { return { value: run() }; } catch (error) { return { error: error.message }; } };
const results = {};
for (const [name, { source, kind }] of Object.entries(cases)) {
  const kindEdits = edits[kind];
  const patched = outcome(() => kindEdits.reduce((changed, edit) => installer.patch(changed, edit), source));
  const result = { patched: patched.error ?? null, unpatched: outcome(() => kindEdits.forEach((edit) => installer.verify(source, edit))).error ?? null };
  if (patched.value !== undefined) {
    result.verified = outcome(() => kindEdits.forEach((edit) => installer.verify(patched.value, edit))).error ?? true;
    result.restored = kindEdits.reduceRight((changed, edit) => installer.original(changed, edit), patched.value) === source;
    result.again = outcome(() => installer.patch(patched.value, kindEdits[0])).error ?? null;
    const context = { normalizeProviderId: (value) => String(value).trim().toLowerCase() };
    vm.runInNewContext(patched.value, context);
    const changed = context[kindEdits[0].name];
    const reply = () => Object.fromEntries(Object.entries(replyFacts).map(([label, facts]) => [label, changed(facts)]));
    const calls = {
      text: () => Object.fromEntries([["cetirun", "cetirun"], ["CetiRun", " CetiRun "], ["none", undefined], ["openai", "openai"]]
        .map(([label, provider]) => [label, changed(provider, "gpt-6", "api-key")])),
      cooldown: () => Object.fromEntries(["cetirun", "openrouter", "kilocode", "openai"].map((provider) => [provider, changed(provider)])),
      reply, credentials: reply, "reply and credentials": reply,
      terminal: () => Object.fromEntries(Object.entries(retryMessages).map(([label, message]) => [label, changed(message)])),
    };
    result.calls = calls[kind]();
  }
  results[name] = result;
}
console.log(JSON.stringify({
  copy: installer.BUDGET_COPY,
  credentials: installer.CREDENTIALS_COPIES,
  files: Object.fromEntries(Object.entries(installer.FILES).map(([file, { sha256, edits }]) => [file, { sha256, edits: edits.map((edit) => edit.name) }])),
  unsupported: outcome(() => installer.apply(root)).error ?? null,
  results,
}));
""" % {"text": json.dumps(TEXT_CHUNK), "cooldown": json.dumps(COOLDOWN_CHUNK), "reply": json.dumps(REPLY_CHUNK),
       "retry": json.dumps(RETRY_CHUNK), "facts": json.dumps(REPLY_FACTS), "messages": json.dumps(RETRY_MESSAGES)}

SOURCES = {TEXT_CHUNK: TEXT_SOURCE, COOLDOWN_CHUNK: COOLDOWN_SOURCE, REPLY_CHUNK: REPLY_SOURCE, RETRY_CHUNK: RETRY_SOURCE}


@unittest.skipUnless(shutil.which("node"), "requires Node for the OpenClaw image's patch")
class OpenClawGatewayBudgetPatchTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cases = {
            "text": {"source": TEXT_SOURCE, "kind": "text"},
            "cooldown": {"source": COOLDOWN_SOURCE, "kind": "cooldown"},
            "reply": {"source": REPLY_SOURCE, "kind": "reply"},
            "credentials": {"source": REPLY_SOURCE, "kind": "credentials"},
            "reply and credentials": {"source": REPLY_SOURCE, "kind": "reply and credentials"},
            "terminal": {"source": RETRY_SOURCE, "kind": "terminal"},
            "changed text": {"source": TEXT_SOURCE.replace("authMode)", "auth)"), "kind": "text"},
            "repeated cooldown": {"source": COOLDOWN_SOURCE * 2, "kind": "cooldown"},
            "changed reply": {"source": REPLY_SOURCE.replace("and try again.", "and retry."), "kind": "reply"},
            "changed credentials": {"source": REPLY_SOURCE.replace("unfinished tool call", "incomplete tool call"),
                                    "kind": "credentials"},
            "changed terminal": {"source": RETRY_SOURCE.replace("Boolean(message?.errorCode", "Boolean(message?.code"),
                                 "kind": "terminal"},
        }
        with tempfile.TemporaryDirectory() as directory:
            # A package whose chunks are at the pinned paths, but not the published ones.
            package = Path(directory) / "openclaw"
            for name, source in SOURCES.items():
                (package / name).parent.mkdir(parents=True, exist_ok=True)
                (package / name).write_text(source, encoding="utf-8", newline="\n")
            runner = Path(directory) / "runner.mjs"
            runner.write_text(RUNNER, encoding="utf-8")
            result = subprocess.run(["node", str(runner), str(INSTALLER), json.dumps(cases), str(package)],
                                    capture_output=True, text=True, encoding="utf-8", timeout=60, check=False)
            cls.left = {name: (package / name).read_text(encoding="utf-8") for name in SOURCES}
        if result.returncode != 0:
            raise AssertionError(f"{result.stdout}\n{result.stderr}")
        cls.report = json.loads(result.stdout)

    def test_the_gateway_and_an_unnamed_provider_get_the_cetirun_text(self):
        copy = self.report["copy"]
        result = self.report["results"]["text"]
        self.assertIs(result["verified"], True)
        calls = result["calls"]
        self.assertEqual([calls["cetirun"], calls["CetiRun"], calls["none"]], [copy, copy, copy])
        # Another provider keeps OpenClaw's own text.
        self.assertEqual(calls["openai"], "published openai")

    def test_the_gateway_key_keeps_no_cooldown_and_other_providers_keep_theirs(self):
        result = self.report["results"]["cooldown"]
        self.assertIs(result["verified"], True)
        self.assertEqual(result["calls"], {"cetirun": True, "openrouter": True, "kilocode": True, "openai": False})

    def test_a_failed_turn_of_the_gateway_or_of_no_provider_gets_the_cetirun_text(self):
        # A turn that OpenClaw saved without its provider read "LLM request failed
        # (provider billing issue, HTTP 402). Check provider billing and try again."
        copy = self.report["copy"]
        result = self.report["results"]["reply"]
        self.assertIs(result["verified"], True)
        calls = result["calls"]
        self.assertEqual([calls["cetirun"], calls["CetiRun"], calls["none"]], [copy, copy, copy])
        # Another provider, and a failure that is not a billing error, keep OpenClaw's text.
        self.assertEqual(calls["openai"],
                         "published openai/gpt-6 request failed (provider billing issue, HTTP 402). "
                         "Check openai billing and try again.")
        self.assertEqual(calls["rate limit"], "published LLM request failed (rate limited, HTTP 429). Try again shortly.")
        self.assertEqual(calls["model not found"], "published cetirun request failed (model not found, HTTP 404).")

    def test_refused_credentials_get_the_cetirun_text_of_their_code(self):
        # OpenClaw said "LLM request failed (provider internal error, HTTP 502). This is usually temporary —
        # try again shortly." The run's error has the facts; a saved turn has only the code.
        texts = self.report["credentials"]
        self.assertEqual(set(texts), set(CODES))
        for name in ("credentials", "reply and credentials"):
            with self.subTest(edits=name):
                result = self.report["results"][name]
                self.assertIs(result["verified"], True)
                calls = result["calls"]
                self.assertEqual([calls["provider credentials"], calls["saved provider credentials"],
                                  calls["copilot credentials"]], [texts[CODES[0]], texts[CODES[0]], texts[CODES[1]]])
                # Another 502 and OpenClaw's own codes keep OpenClaw's text.
                self.assertEqual(calls["another 502"],
                                 "published cetirun/gpt-6 request failed (provider internal error, HTTP 502). Try again shortly.")
                self.assertTrue(calls["unfinished tool call"].startswith("⚠️ The provider returned an unfinished tool call."))
        # Both changes of the reply function hold together.
        calls = self.report["results"]["reply and credentials"]["calls"]
        self.assertEqual(calls["cetirun"], self.report["copy"])

    def test_refused_credentials_end_the_turn_without_a_retry(self):
        result = self.report["results"]["terminal"]
        self.assertIs(result["verified"], True)
        self.assertEqual(result["calls"], {
            "provider credentials": True, "copilot credentials": True,
            # OpenClaw's own decisions stay.
            "another 502": False, "no code": False, "OpenClaw's own final code": True, "refusal": True,
            "output identity": True,
        })

    def test_the_patch_applies_once_and_is_recognized_when_it_runs_again(self):
        for name in ("text", "cooldown", "reply", "credentials", "reply and credentials", "terminal"):
            with self.subTest(chunk=name):
                result = self.report["results"][name]
                self.assertIsNone(result["patched"])
                self.assertTrue(result["restored"])
                # The installer undoes its own change before it checks the published digest.
                self.assertIn("OpenClaw changed where CetiRun patches", result["again"])
                self.assertIn("was not changed", result["unpatched"])

    def test_a_changed_openclaw_source_fails_the_build(self):
        for name in ("changed text", "repeated cooldown", "changed reply", "changed credentials", "changed terminal"):
            with self.subTest(chunk=name):
                self.assertIn("OpenClaw changed where CetiRun patches", self.report["results"][name]["patched"])
        # Chunks at the pinned paths with other contents are refused and left as they are.
        self.assertEqual(self.report["unsupported"],
                         f"Unsupported OpenClaw source {TEXT_CHUNK}; review the pinned integration")
        self.assertEqual(self.left, SOURCES)

    def test_only_the_four_published_chunks_are_changed(self):
        # OpenClaw's worker bundles in dist/worker stay as published.
        self.assertEqual(self.report["files"], {
            TEXT_CHUNK: {"sha256": "1b85f5fcfff49bdcb428f0b6fa21e786261ddc88f37a930b84ee5d391865a644",
                         "edits": ["formatBillingErrorMessage"]},
            COOLDOWN_CHUNK: {"sha256": "4abd9dab006d0b708e69e869ab8e56d92312cd074ac8ef7fc04b33999be9c60a",
                             "edits": ["isAuthCooldownBypassedForProvider"]},
            REPLY_CHUNK: {"sha256": "7ec3637305985d3e929398f886c463ec800419649dab85e01f646afc83475ebc",
                          "edits": ["renderAssistantRequestFailureCopy", "renderAssistantRequestFailureCopy"]},
            RETRY_CHUNK: {"sha256": "13873c585c13ccde6665fc12885c54f648ea98563a1a2f9a78294d43456fdcba",
                          "edits": ["isTerminalAssistantError"]},
        })

    def test_the_text_names_the_budget_and_does_not_read_as_a_temporary_limit(self):
        # OpenClaw classifies its own billing text again in some paths: "out of
        # budget" keeps it a billing error, and none of these words make it a
        # rate limit that OpenClaw retries.
        copy = self.report["copy"]
        self.assertIn("CetiRun AI Gateway", copy)
        self.assertIn("out of budget", copy)
        self.assertIn("CetiRun administrator", copy)
        for word in ("limit", "quota", "retry", "try again", "wait a", "temporar", "cooldown", "daily", "weekly",
                     "monthly", "rate", "too many", "billing dashboard", "top up", "credits"):
            self.assertNotIn(word, copy.lower())

    def test_the_credentials_texts_say_who_fixes_them_and_read_as_no_other_failure(self):
        texts = self.report["credentials"]
        self.assertIn("CetiRun administrator to fix the provider configuration", texts[CODES[0]])
        self.assertIn("sign in to GitHub Copilot again in the Portal", texts[CODES[1]])
        for code, text in texts.items():
            with self.subTest(code=code):
                # The Control UI shows a text that starts with this mark without its own prefix; it shows 400 characters.
                self.assertTrue(text.startswith("⚠️ "))
                self.assertLessEqual(len(text), 400)
                self.assertIn("The key of this Agent is not the cause.", text)
                # OpenClaw's text patterns for an authentication error, a temporary failure, a billing error.
                for word in ("authenticat", "unauthorized", "forbidden", "api key", "invalid", "expired", "401", "403",
                             "retry", "try again", "temporar", "billing", "quota", "limit"):
                    self.assertNotIn(word, text.lower())

    def test_the_image_patches_openclaw_after_its_last_package_install(self):
        dockerfile = (OPENCLAW / "Dockerfile").read_text(encoding="utf-8")
        patch = "node /tmp/cetirun-openclaw-compat/apply-gateway-budget.mjs /usr/local/share/npm-global/lib/node_modules/openclaw"
        self.assertIn(patch, dockerfile)
        self.assertGreater(dockerfile.index(patch), dockerfile.rindex("npm install"))


if __name__ == "__main__":
    unittest.main()
