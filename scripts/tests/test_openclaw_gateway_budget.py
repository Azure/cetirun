# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Offline tests for the OpenClaw image's patch for a spent AI Gateway budget.

The patch changes OpenClaw 2026.9.6's billing text and its cooldown bypass list
in two chunks of the published package, which these tests do not have. They
run the patch on synthetic chunks of the same shape.
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
BILLING_CHUNK = "dist/user-copy-DfLWtdTx.mjs"
BYPASS_CHUNK = "dist/usage-state-Bm5iXGhR.mjs"

BILLING = textwrap.dedent("""\
    /** Format billing copy with optional provider/model and credential context. */
    function formatBillingErrorMessage(provider, model, authMode) {
    \tconst providerName = provider?.trim();
    \treturn providerName ? `published ${providerName}` : "published";
    }
    const BILLING_ERROR_USER_MESSAGE = formatBillingErrorMessage();
    """)
BYPASS = textwrap.dedent("""\
    function isAuthCooldownBypassedForProvider(provider) {
    \tconst normalized = normalizeProviderId(provider ?? "");
    \treturn normalized === "openrouter" || normalized === "kilocode";
    }
    function resolveInlineProviderApiKeyUsageId(provider) {
    \treturn `inline-api-key:${normalizeProviderId(provider)}`;
    }
    """)

# Runs the installer's own functions on each synthetic chunk and reports what
# the changed functions return.
RUNNER = """
import vm from "node:vm";
import { pathToFileURL } from "node:url";
const installer = await import(pathToFileURL(process.argv[2]).href);
const [cases, root] = [JSON.parse(process.argv[3]), process.argv[4]];
const edits = { billing: installer.FILES[%(billing)s].edit, bypass: installer.FILES[%(bypass)s].edit };
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
    result.calls = kind === "billing"
      ? Object.fromEntries([["cetirun", "cetirun"], ["CetiRun", " CetiRun "], ["none", undefined], ["openai", "openai"]]
          .map(([label, provider]) => [label, changed(provider, "gpt-6", "api-key")]))
      : Object.fromEntries(["cetirun", "openrouter", "kilocode", "openai"].map((provider) => [provider, changed(provider)]));
  }
  results[name] = result;
}
console.log(JSON.stringify({
  copy: installer.BUDGET_COPY,
  files: Object.fromEntries(Object.entries(installer.FILES).map(([file, { sha256, edit }]) => [file, { sha256, edit: edit.name }])),
  unsupported: outcome(() => installer.apply(root)).error ?? null,
  results,
}));
""" % {"billing": json.dumps(BILLING_CHUNK), "bypass": json.dumps(BYPASS_CHUNK)}


@unittest.skipUnless(shutil.which("node"), "requires Node for the OpenClaw image's patch")
class OpenClawGatewayBudgetPatchTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cases = {
            "billing": {"source": BILLING, "kind": "billing"},
            "bypass": {"source": BYPASS, "kind": "bypass"},
            "changed billing": {"source": BILLING.replace("authMode)", "auth)"), "kind": "billing"},
            "repeated bypass": {"source": BYPASS * 2, "kind": "bypass"},
        }
        with tempfile.TemporaryDirectory() as directory:
            # A package whose chunks are at the pinned paths, but not the published ones.
            package = Path(directory) / "openclaw"
            for name, source in ((BILLING_CHUNK, BILLING), (BYPASS_CHUNK, BYPASS)):
                (package / name).parent.mkdir(parents=True, exist_ok=True)
                (package / name).write_text(source, encoding="utf-8", newline="\n")
            runner = Path(directory) / "runner.mjs"
            runner.write_text(RUNNER, encoding="utf-8")
            result = subprocess.run(["node", str(runner), str(INSTALLER), json.dumps(cases), str(package)],
                                    capture_output=True, text=True, encoding="utf-8", timeout=60, check=False)
            cls.left = {name: (package / name).read_text(encoding="utf-8") for name in (BILLING_CHUNK, BYPASS_CHUNK)}
        if result.returncode != 0:
            raise AssertionError(f"{result.stdout}\n{result.stderr}")
        cls.report = json.loads(result.stdout)

    def test_the_gateway_and_an_unnamed_provider_get_the_cetirun_text(self):
        copy = self.report["copy"]
        result = self.report["results"]["billing"]
        self.assertIs(result["verified"], True)
        calls = result["calls"]
        self.assertEqual([calls["cetirun"], calls["CetiRun"], calls["none"]], [copy, copy, copy])
        # Another provider keeps OpenClaw's own text.
        self.assertEqual(calls["openai"], "published openai")

    def test_the_gateway_key_keeps_no_cooldown_and_other_providers_keep_theirs(self):
        result = self.report["results"]["bypass"]
        self.assertIs(result["verified"], True)
        self.assertEqual(result["calls"], {"cetirun": True, "openrouter": True, "kilocode": True, "openai": False})

    def test_the_patch_applies_once_and_is_recognized_when_it_runs_again(self):
        for name in ("billing", "bypass"):
            with self.subTest(chunk=name):
                result = self.report["results"][name]
                self.assertIsNone(result["patched"])
                self.assertTrue(result["restored"])
                # The installer undoes its own change before it checks the published digest.
                self.assertIn("OpenClaw changed where CetiRun patches", result["again"])
                self.assertIn("was not changed", result["unpatched"])

    def test_a_changed_openclaw_source_fails_the_build(self):
        for name in ("changed billing", "repeated bypass"):
            with self.subTest(chunk=name):
                self.assertIn("OpenClaw changed where CetiRun patches", self.report["results"][name]["patched"])
        # Chunks at the pinned paths with other contents are refused and left as they are.
        self.assertEqual(self.report["unsupported"],
                         f"Unsupported OpenClaw source {BILLING_CHUNK}; review the pinned integration")
        self.assertEqual(self.left, {BILLING_CHUNK: BILLING, BYPASS_CHUNK: BYPASS})

    def test_only_the_two_published_chunks_are_changed(self):
        # OpenClaw's worker bundles in dist/worker stay as published.
        self.assertEqual(self.report["files"], {
            BILLING_CHUNK: {"sha256": "1b85f5fcfff49bdcb428f0b6fa21e786261ddc88f37a930b84ee5d391865a644",
                            "edit": "formatBillingErrorMessage"},
            BYPASS_CHUNK: {"sha256": "4abd9dab006d0b708e69e869ab8e56d92312cd074ac8ef7fc04b33999be9c60a",
                           "edit": "isAuthCooldownBypassedForProvider"},
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
