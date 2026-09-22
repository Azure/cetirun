# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Catch process-oriented prose without restricting useful technical warnings."""

import importlib.util
import unittest
from pathlib import Path

SITE = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "check_site", SITE / "scripts/check_site.py"
)
check_site = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(check_site)


class EditorialTests(unittest.TestCase):
    def test_short_titles(self):
        for title in ("Architecture", "Security", "Guides", "Create an instance"):
            self.assertEqual(
                check_site.check_editorial_style(
                    "A useful introduction.", {"title": title}
                ),
                [],
            )

    def test_review_style_titles_are_rejected(self):
        for title in (
            "Current architecture",
            "Security and data handling",
            "Guidances",
        ):
            self.assertTrue(check_site.check_editorial_style("Body.", {"title": title}))

    def test_development_narrative_is_rejected(self):
        for sentence in (
            "The current implementation supports agents.",
            "This page describes the current source.",
            "This checkout has no installer.",
            "We are refactoring the portal.",
            "The runtime is being rewritten.",
        ):
            with self.subTest(sentence=sentence):
                self.assertTrue(
                    check_site.check_editorial_style(
                        sentence, {"title": "Architecture"}
                    )
                )

    def test_operational_current_is_allowed(self):
        self.assertEqual(
            check_site.check_editorial_style(
                "Check the current Kubernetes context. The logger follows files from their current end.",
                {"title": "Operations"},
            ),
            [],
        )

    def test_real_limits_are_allowed(self):
        self.assertEqual(
            check_site.check_editorial_style(
                "The chart does not install these entries. Deleting the namespace removes PVCs. "
                "Retain on a StatefulSet does not prevent namespace deletion.",
                {"title": "Storage"},
            ),
            [],
        )

    def test_code_examples_are_not_style_checked(self):
        self.assertEqual(
            check_site.check_editorial_style(
                "```text\ncurrent implementation\nWe are refactoring\n```\n",
                {"title": "Documentation"},
            ),
            [],
        )

    def test_current_pages(self):
        for path in (SITE / "content/en/docs").rglob("*.md"):
            with self.subTest(page=path.name):
                text = path.read_text()
                self.assertEqual(
                    check_site.check_editorial_style(
                        text, check_site.frontmatter(text)
                    ),
                    [],
                )

    def test_requested_titles(self):
        for relative, expected in (
            ("concepts/architecture.md", "Architecture"),
            ("security/_index.md", "Security"),
            ("user-guide/_index.md", "User Guide"),
            ("operations/_index.md", "Operations"),
            ("reference/_index.md", "Reference"),
            ("contributing/_index.md", "Contributing"),
        ):
            metadata = check_site.frontmatter(
                (SITE / "content/en/docs" / relative).read_text()
            )
            self.assertEqual(metadata["title"], expected)


if __name__ == "__main__":
    unittest.main()
