# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Offline tests for the OpenClaw image's patch for a spent AI Gateway budget.

The patch changes OpenClaw 2026.9.6's billing text, the text of a failed turn,
and its cooldown bypass list in three chunks of the published package, which
these tests do not have. They run the patch on synthetic chunks of the same
shape.
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
# OpenClaw's chunk of user-facing text, its chunk of key usage state, and its
# chunk of the text of a failed turn.
TEXT_CHUNK = "dist/user-copy-DfLWtdTx.mjs"
COOLDOWN_CHUNK = "dist/usage-state-Bm5iXGhR.mjs"
REPLY_CHUNK = "dist/assistant-request-failure-copy-D0cfsUk2.mjs"

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
    const ASSISTANT_REQUEST_FAILURE_REASON = {
    \trate_limit: "rate limited",
    \tbilling: "provider billing issue",
    \tmodel_not_found: "model not found"
    };
    /** Render classified facts without exposing raw provider response text. */
    function renderAssistantRequestFailureCopy(facts) {
    \tconst provider = facts.provider?.trim();
    \tconst model = facts.model?.trim();
    \tconst target = provider && model ? `${provider}/${model}` : provider || model;
    \tconst reason = facts.reason ? ASSISTANT_REQUEST_FAILURE_REASON[facts.reason] : void 0;
    \tconst status = typeof facts.status === "number" ? `HTTP ${facts.status}` : void 0;
    \tconst details = [reason, status].filter(Boolean);
    \tconst summary = `published ${target ? `${target} request failed` : "LLM request failed"}${details.length > 0 ? ` (${details.join(", ")})` : ""}.`;
    \tif (facts.reason === "rate_limit") return `${summary} Try again shortly.`;
    \tif (facts.reason === "billing") return `${summary} Check ${provider ? `${provider} billing` : "provider billing"} and try again.`;
    \treturn summary;
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
}

# Runs the installer's own functions on each synthetic chunk and reports what
# the changed functions return.
RUNNER = """
import vm from "node:vm";
import { pathToFileURL } from "node:url";
const installer = await import(pathToFileURL(process.argv[2]).href);
const [cases, root] = [JSON.parse(process.argv[3]), process.argv[4]];
const edits = {
  text: installer.FILES[%(text)s].edit,
  cooldown: installer.FILES[%(cooldown)s].edit,
  reply: installer.FILES[%(reply)s].edit,
};
const replyFacts = %(facts)s;
const outcome = (run) => { try { return { value: run() }; } catch (error) { return { error: error.message }; } };
const results = {};
for (const [name, { source, kind }] of Object.entries(cases)) {
  const edit = edits[kind];
  const patched = outcome(() => installer.patch(source, edit));
  const result = { patched: patched.error ?? null, unpatched: outcome(() => installer.verify(source, edit)).error ?? null };
  if (patched.value !== undefined) {
    result.verified = outcome(() => installer.verify(patched.value, edit)).error ?? true;
    result.restored = installer.original(patched.value, edit) === source;
    result.again = outcome(() => installer.patch(patched.value, edit)).error ?? null;
    const context = { normalizeProviderId: (value) => String(value).trim().toLowerCase() };
    vm.runInNewContext(patched.value, context);
    const changed = context[edit.name];
    const calls = {
      text: () => Object.fromEntries([["cetirun", "cetirun"], ["CetiRun", " CetiRun "], ["none", undefined], ["openai", "openai"]]
        .map(([label, provider]) => [label, changed(provider, "gpt-6", "api-key")])),
      cooldown: () => Object.fromEntries(["cetirun", "openrouter", "kilocode", "openai"].map((provider) => [provider, changed(provider)])),
      reply: () => Object.fromEntries(Object.entries(replyFacts).map(([label, facts]) => [label, changed(facts)])),
    };
    result.calls = calls[kind]();
  }
  results[name] = result;
}
console.log(JSON.stringify({
  copy: installer.BUDGET_COPY,
  files: Object.fromEntries(Object.entries(installer.FILES).map(([file, { sha256, edit }]) => [file, { sha256, edit: edit.name }])),
  unsupported: outcome(() => installer.apply(root)).error ?? null,
  results,
}));
""" % {"text": json.dumps(TEXT_CHUNK), "cooldown": json.dumps(COOLDOWN_CHUNK), "reply": json.dumps(REPLY_CHUNK),
       "facts": json.dumps(REPLY_FACTS)}


@unittest.skipUnless(shutil.which("node"), "requires Node for the OpenClaw image's patch")
class OpenClawGatewayBudgetPatchTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cases = {
            "text": {"source": TEXT_SOURCE, "kind": "text"},
            "cooldown": {"source": COOLDOWN_SOURCE, "kind": "cooldown"},
            "reply": {"source": REPLY_SOURCE, "kind": "reply"},
            "changed text": {"source": TEXT_SOURCE.replace("authMode)", "auth)"), "kind": "text"},
            "repeated cooldown": {"source": COOLDOWN_SOURCE * 2, "kind": "cooldown"},
            "changed reply": {"source": REPLY_SOURCE.replace("and try again.", "and retry."), "kind": "reply"},
        }
        with tempfile.TemporaryDirectory() as directory:
            # A package whose chunks are at the pinned paths, but not the published ones.
            package = Path(directory) / "openclaw"
            for name, source in ((TEXT_CHUNK, TEXT_SOURCE), (COOLDOWN_CHUNK, COOLDOWN_SOURCE), (REPLY_CHUNK, REPLY_SOURCE)):
                (package / name).parent.mkdir(parents=True, exist_ok=True)
                (package / name).write_text(source, encoding="utf-8", newline="\n")
            runner = Path(directory) / "runner.mjs"
            runner.write_text(RUNNER, encoding="utf-8")
            result = subprocess.run(["node", str(runner), str(INSTALLER), json.dumps(cases), str(package)],
                                    capture_output=True, text=True, encoding="utf-8", timeout=60, check=False)
            cls.left = {name: (package / name).read_text(encoding="utf-8") for name in (TEXT_CHUNK, COOLDOWN_CHUNK, REPLY_CHUNK)}
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

    def test_the_patch_applies_once_and_is_recognized_when_it_runs_again(self):
        for name in ("text", "cooldown", "reply"):
            with self.subTest(chunk=name):
                result = self.report["results"][name]
                self.assertIsNone(result["patched"])
                self.assertTrue(result["restored"])
                # The installer undoes its own change before it checks the published digest.
                self.assertIn("OpenClaw changed where CetiRun patches", result["again"])
                self.assertIn("was not changed", result["unpatched"])

    def test_a_changed_openclaw_source_fails_the_build(self):
        for name in ("changed text", "repeated cooldown", "changed reply"):
            with self.subTest(chunk=name):
                self.assertIn("OpenClaw changed where CetiRun patches", self.report["results"][name]["patched"])
        # Chunks at the pinned paths with other contents are refused and left as they are.
        self.assertEqual(self.report["unsupported"],
                         f"Unsupported OpenClaw source {TEXT_CHUNK}; review the pinned integration")
        self.assertEqual(self.left, {TEXT_CHUNK: TEXT_SOURCE, COOLDOWN_CHUNK: COOLDOWN_SOURCE, REPLY_CHUNK: REPLY_SOURCE})

    def test_only_the_three_published_chunks_are_changed(self):
        # OpenClaw's worker bundles in dist/worker stay as published.
        self.assertEqual(self.report["files"], {
            TEXT_CHUNK: {"sha256": "1b85f5fcfff49bdcb428f0b6fa21e786261ddc88f37a930b84ee5d391865a644",
                         "edit": "formatBillingErrorMessage"},
            COOLDOWN_CHUNK: {"sha256": "4abd9dab006d0b708e69e869ab8e56d92312cd074ac8ef7fc04b33999be9c60a",
                             "edit": "isAuthCooldownBypassedForProvider"},
            REPLY_CHUNK: {"sha256": "7ec3637305985d3e929398f886c463ec800419649dab85e01f646afc83475ebc",
                          "edit": "renderAssistantRequestFailureCopy"},
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

    def test_the_image_patches_openclaw_after_its_last_package_install(self):
        dockerfile = (OPENCLAW / "Dockerfile").read_text(encoding="utf-8")
        patch = "node /tmp/cetirun-openclaw-compat/apply-gateway-budget.mjs /usr/local/share/npm-global/lib/node_modules/openclaw"
        self.assertIn(patch, dockerfile)
        self.assertGreater(dockerfile.index(patch), dockerfile.rindex("npm install"))


if __name__ == "__main__":
    unittest.main()
