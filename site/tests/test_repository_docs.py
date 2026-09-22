# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Keep source-document entry points and planned concepts honest."""

import importlib.util
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[1] / "scripts/check_site.py"
SPEC = importlib.util.spec_from_file_location("check_site", SCRIPT)
check_site = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(check_site)


class RepositoryDocsTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.site = self.root / "site"
        self.site.mkdir()
        (self.root / "docs").mkdir()

    def write(self, name, text):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")
        return path

    def test_existing_docs_inventory(self):
        self.assertEqual(check_site.check_doc_inventory(SCRIPT.parents[1]), [])

    def test_unmapped_document_fails(self):
        self.write("docs/new-guide.md", "# New guide\n")
        self.assertIn(
            "unmapped documentation file: docs/new-guide.md",
            check_site.check_doc_inventory(self.site),
        )

    def test_unclassified_asset_fails(self):
        self.write("docs/new-diagram.svg", "<svg/>")
        self.assertIn(
            "unmapped documentation file: docs/new-diagram.svg",
            check_site.check_doc_inventory(self.site),
        )

    def test_existing_logo_is_classified(self):
        self.write("docs/logo.jpg", "fixture")
        self.assertEqual(check_site.check_doc_inventory(self.site), [])

    def test_inventory_rejects_symlink(self):
        self.write("README.md", "# Example\n")
        (self.root / "docs/link.md").symlink_to(self.root / "README.md")
        self.assertIn(
            "documentation symlink requires review: docs/link.md",
            check_site.check_doc_inventory(self.site),
        )

    def test_repository_links_and_fragments(self):
        self.write("DEVELOPMENT.md", "[Headers](scripts/README.md#license-headers)\n")
        self.write("scripts/README.md", "# Scripts\n\n## License headers\n")
        self.assertEqual(check_site.check_repository_links(self.root), [])

    def test_removed_pointer_fragment_fails(self):
        self.write("DEVELOPMENT.md", "[Headers](scripts/README.md#license-headers)\n")
        self.write("scripts/README.md", "# Scripts\n")
        self.assertIn(
            "DEVELOPMENT.md: missing repository fragment: scripts/README.md#license-headers",
            check_site.check_repository_links(self.root),
        )

    def test_missing_repository_target_fails(self):
        self.write("README.md", "[Missing](docs/missing.md)\n")
        self.assertIn(
            "README.md: missing repository target: docs/missing.md",
            check_site.check_repository_links(self.root),
        )

    def test_source_page_target_fragment(self):
        self.write(
            "docs/guides/msteams.md",
            "[Guide](../../site/content/en/docs/guides/msteams.md#setup)\n",
        )
        self.write(
            "site/content/en/docs/guides/msteams.md",
            '---\ntitle: "Guide"\n---\n\n## Setup\n',
        )
        self.assertEqual(check_site.check_repository_links(self.root), [])

    def test_fenced_examples_are_not_links_or_anchors(self):
        self.write(
            "README.md",
            "# Readme\n\n```markdown\n## Not a heading\n[Missing](missing.md)\n```\n",
        )
        self.assertEqual(check_site.check_repository_links(self.root), [])
        self.assertNotIn(
            "not-a-heading",
            check_site.markdown_anchors((self.root / "README.md").read_text()),
        )

    def test_tilde_fenced_example_is_ignored(self):
        self.write("README.md", "~~~md\n[Missing](missing.md)\n~~~\n")
        self.assertEqual(check_site.check_repository_links(self.root), [])

    def test_external_links_are_offline(self):
        self.write("README.md", "[Example](https://example.com/missing#fragment)\n")
        self.assertEqual(check_site.check_repository_links(self.root), [])

    def test_link_outside_repository_fails(self):
        self.write("README.md", "[Outside](../outside.md)\n")
        self.assertIn(
            "README.md: repository link escapes checkout: ../outside.md",
            check_site.check_repository_links(self.root),
        )

    def test_duplicate_and_explicit_anchors(self):
        anchors = check_site.markdown_anchors(
            "# Title\n## Example\n## Example\n## `Code` & text\n"
            '<a id="retained-anchor"></a>\n'
        )
        self.assertTrue(
            {"title", "example", "example-1", "code--text", "retained-anchor"}
            <= anchors
        )

    def test_missing_trademark_statement_fails(self):
        self.write("README.md", "# CetiRun\n")
        self.assertIn(
            "README.md: retain the Trademarks section and Microsoft guideline link",
            check_site.check_repository_contracts(self.root),
        )

    def test_trademark_statement(self):
        self.write(
            "README.md",
            "# CetiRun\n\n## Trademarks\n"
            "[Guidelines](https://www.microsoft.com/en-us/legal/"
            "intellectualproperty/trademarks/usage/general)\n",
        )
        self.assertEqual(check_site.check_repository_contracts(self.root), [])


class PlannedContentTests(unittest.TestCase):
    def test_planned_page_has_visible_status(self):
        self.assertEqual(
            check_site.check_planned_content(
                "**Status: planned.** No installation guide.\n", {"status": "planned"}
            ),
            [],
        )

    def test_missing_visible_status_fails(self):
        self.assertIn(
            "planned page needs a visible Status: planned label",
            check_site.check_planned_content("A target.\n", {"status": "planned"}),
        )

    def test_planned_setup_examples_fail(self):
        for language in ("bash", "sh", "console", "yaml", "json", "powershell"):
            with self.subTest(language=language):
                errors = check_site.check_planned_content(
                    f"**Status: planned.**\n\n```{language}\nexample\n```\n",
                    {"status": "planned"},
                )
                self.assertIn(
                    "planned concepts must not include executable setup examples",
                    errors,
                )

    def test_text_diagram_is_allowed(self):
        self.assertEqual(
            check_site.check_planned_content(
                "**Status: planned.**\n\n```text\nAgent -> Gateway\n```\n",
                {"status": "planned"},
            ),
            [],
        )

    def test_current_guides_keep_examples(self):
        self.assertEqual(
            check_site.check_planned_content(
                "```bash\nhelm template example\n```\n", {}
            ),
            [],
        )

    def test_home_template_keeps_define_first(self):
        home = SCRIPT.parents[1] / "layouts/home.html"
        self.assertEqual(home.read_text().splitlines()[0], '{{ define "main" }}')


class HugoReuseTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.hugo = self.root / ".bin/hugo"
        self.hugo.parent.mkdir()
        self.hugo.write_text("#!/bin/sh\necho 'hugo v0.164.0+extended linux/arm64'\n")
        self.hugo.chmod(0o755)
        os.utime(self.hugo, (1, 1))
        (self.root / "go.mod").write_text("module example.com/docs\n")
        fake_go = self.root / ".bin/go"
        fake_go.write_text(
            "#!/bin/sh\necho 'Go installation blocked by fixture'\nexit 99\n"
        )
        fake_go.chmod(0o755)

    def run_make(self):
        return subprocess.run(
            ["make", "-f", str(SCRIPT.parents[1] / "Makefile"), str(self.hugo)],
            check=False,
            cwd=self.root,
            env={**os.environ, "PATH": f"{self.hugo.parent}:{os.environ['PATH']}"},
            capture_output=True,
            text=True,
        )

    def test_newer_manifest_reuses_matching_hugo_without_go(self):
        result = self.run_make()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("Reusing Hugo 0.164.0 extended", result.stdout)

    def test_wrong_hugo_version_is_not_reused(self):
        self.hugo.write_text("#!/bin/sh\necho 'hugo v0.100.0+extended linux/arm64'\n")
        os.utime(self.hugo, (1, 1))
        result = self.run_make()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Go installation blocked by fixture", result.stdout)


if __name__ == "__main__":
    unittest.main()
