# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Keep executable documentation aligned with the CetiRun resource API."""

import re
import unittest
from pathlib import Path

SITE = Path(__file__).resolve().parents[1]
REPO = SITE.parent


class ApiDocumentationTests(unittest.TestCase):
    def test_yaml_examples_use_registered_kinds(self):
        examples = []
        for page in (SITE / "content/en/docs").rglob("*.md"):
            for block in re.findall(
                r"^```ya?ml\n(.*?)^```", page.read_text(), re.MULTILINE | re.DOTALL
            ):
                if "apiVersion: cetirun.microsoft.com/" not in block:
                    continue
                examples.append(page)
                with self.subTest(page=page.name):
                    self.assertIn("apiVersion: cetirun.microsoft.com/v1alpha1\n", block)
                    self.assertRegex(block, r"(?m)^kind: (Agent|Team|Sandbox|AgentPolicy)$")
                    self.assertNotRegex(block, r"(?m)^  namespace:")
                    if re.search(r"(?m)^kind: Agent$", block):
                        self.assertRegex(block, r"(?m)^  agent:")
                        self.assertNotRegex(block, r"(?m)^  agentType:")
        self.assertGreaterEqual(len(examples), 3)

    def test_no_legacy_resource_commands_or_source_links(self):
        for page in (SITE / "content/en/docs").rglob("*.md"):
            with self.subTest(page=page.name):
                self.assertNotRegex(
                    page.read_text(),
                    r"\bopenclawinstances?\b|\bopenclawteams?\b|"
                    r"openclawinstance_controller\.go|cetirun\.microsoft\.com_openclaw",
                )

    def test_reference_names_match_packaged_crds(self):
        reference = (SITE / "content/en/docs/reference/crds.md").read_text()
        for kind, plural, short in (
            ("Agent", "agents", "cra"),
            ("Team", "teams", "crt"),
            ("Sandbox", "sandboxes", "crs"),
            ("AgentPolicy", "agentpolicies", "crp"),
        ):
            with self.subTest(kind=kind):
                name = f"cetirun.microsoft.com_{plural}.yaml"
                source = REPO / "app/cetirun-operator/config/crd/bases" / name
                packaged = REPO / "app/cetirun-operator/chart/templates/crds" / name
                self.assertEqual(source.read_bytes(), packaged.read_bytes())
                schema = source.read_text()
                self.assertIn(f"    kind: {kind}\n", schema)
                self.assertIn(f"    plural: {plural}\n", schema)
                self.assertIn(f"    - {short}\n", schema)
                self.assertIn(
                    f"| `{kind}` | `{plural}` | `{plural}.cetirun.microsoft.com` | `{short}` |",
                    reference,
                )

    def test_installation_documents_platform_and_crd_boundaries(self):
        install = (SITE / "content/en/docs/getting-started/platform.md").read_text()
        reference = (SITE / "content/en/docs/reference/crds.md").read_text()
        self.assertIn("scripts/cetirun-platform-deploy.sh", install)
        self.assertIn("PORTAL_AUTH_NAMESPACE=cetirun-system", install)
        self.assertIn("Helm upgrades the CRDs", install)
        self.assertIn("does not convert, adopt, or delete", reference)

    def test_portal_creation_labels_match_source(self):
        guide = (SITE / "content/en/docs/getting-started/first-instance.md").read_text()
        source = REPO / "app/cetirun-portal/web/src"
        for label, relative in (
            ("New Agent", "pages/InstanceListPage.tsx"),
            ("Create Agent", "components/CreateForm.tsx"),
        ):
            with self.subTest(label=label):
                self.assertIn(f"**{label}**", guide)
                self.assertIn(label, (source / relative).read_text())

    def test_registry_pull_settings_have_distinct_upgrade_guidance(self):
        install = (SITE / "content/en/docs/getting-started/platform.md").read_text()
        reference = (SITE / "content/en/docs/reference/helm.md").read_text()
        harness = (SITE / "content/en/docs/reference/agent-harness.md").read_text()
        for text in (install, reference):
            self.assertIn("Standalone Agent", text)
            self.assertIn("Operator-managed Agents", text)
            self.assertIn("empty list", text)
        for setting in ("cetirun.helmValues.imagePullSecrets", "spec.helm.values.imagePullSecrets", "--reuse-values", "AgentConfigInvalid"):
            self.assertIn(setting, install)
        self.assertIn("upgrading-runtime-registry-configuration", reference)
        self.assertIn("upgrading-runtime-registry-configuration", harness)
        self.assertNotIn("the controller does not distribute them for you", install)

    def test_harness_reference_and_navigation(self):
        reference = SITE / "content/en/docs/reference/agent-harness.md"
        self.assertTrue(reference.is_file())
        text = reference.read_text()
        self.assertIn("`hermes` (default)", text)
        self.assertIn("`AgentRuntimeNotImplemented`", text)
        for field in ("type", "image", "registrySecretRef", "username", "port"):
            with self.subTest(field=field):
                self.assertIn(f"| `{field}` |", text)
        self.assertIn(
            "[Agent Harness](agent-harness/)",
            (SITE / "content/en/docs/reference/_index.md").read_text(),
        )
        pointer = (REPO / "docs/agent-harness-contract.md").read_text()
        self.assertIn("site/content/en/docs/reference/agent-harness.md", pointer)
        self.assertNotIn("```yaml", pointer)

    def test_crd_reference_describes_available_harness_paths(self):
        text = (SITE / "content/en/docs/reference/crds.md").read_text()
        self.assertNotIn("It rejects Custom, registry references", text)
        for contract in (
            "common Sandbox contract",
            "`customRuntime`",
            "non-default usernames",
            "Portal-managed source Secret",
            "`RegistryCredentialsReady`",
            "| `registryCredentials` |",
        ):
            with self.subTest(contract=contract):
                self.assertIn(contract, text)
        self.assertIn("does not prove", text)


if __name__ == "__main__":
    unittest.main()
