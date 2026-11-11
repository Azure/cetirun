# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Catch process-oriented prose without restricting useful technical warnings."""

import importlib.util
import os
import re
import tempfile
import unittest
from pathlib import Path
from unittest import mock

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


def rules(text):
    return {finding.rule.name for finding in check_site.page_style_findings(text)}


def sentence(words):
    return " ".join(["Word"] + ["word"] * (words - 2)) + " end."


PAGE = '---\ntitle: "{title}"\ndescription: "A page."\nweight: 1\n---\n\n{body}\n'


class StyleRuleTests(unittest.TestCase):
    def test_enforced_rules_fail_the_check(self):
        for text in (
            "Don't delete the namespace.",
            "It’s ready.",
            "You're the owner.",
            "Spin up a cluster.",
            "Shut down the Agent.",
            "Find out the cause.",
            "Fill in the form.",
            "This increment adds a flag.",
            "If the checkout includes the root Makefile, run it.",
            "The Portal is being used.",
            "The flag currently has no effect.",
            "Support arrives soon.",
            "As of this release, the field is necessary.",
            "Simply restart the Agent.",
            "Of course, the Agent restarts.",
            "For details, read [here](../security/).",
            "For details, [click here](../security/).",
            "Refer to [this page](../security/).",
            "Let's start the Agent.",
            "Then let's encrypt the disk.",
        ):
            with self.subTest(text=text):
                self.assertTrue(check_site.check_editorial_style(text, {}))

    def test_similar_text_is_allowed(self):
        for text in (
            "Do not delete the namespace. The Team's skills stay. Its status is Ready.",
            "Run the setup. The shutdown takes a minute. Start the Agent.",
            "Sign in to the Portal.",
            "Run `don't-stop` and `spin up`.",
            "Select **Don't save**.",
            "The Agent started just before the change.",
            "It does not accept any more requests.",
            "Read the [Security](../security/) page. Follow [Documentation](site/).",
            "Use Let's Encrypt. Let’s Encrypt's rate limits apply.",
            "It needs Let's\nEncrypt and the public DNS resolvers.",
            "Select **Fill in\nlater** to continue.",
            "## Upgrade the chart {#upgrade-soon}\n\nRun the upgrade.",
            "Refer to [the\nguide](../soon/).",
        ):
            with self.subTest(text=text):
                self.assertEqual(check_site.check_editorial_style(text, {}), [])

    def test_quoted_text_is_not_checked(self):
        for text in (
            'Write "configure", not "set up".',
            'For example, write "A local render checks\nonly the templates", not '
            '"A local render does not examine Azure".',
            "The Portal shows “Don't close this page”.",
        ):
            with self.subTest(text=text):
                self.assertEqual(check_site.page_style_findings(text), [])
        stray = 'A 5" screen.\nSpin up a cluster.\nThen select "Go".'
        self.assertTrue(check_site.check_editorial_style(stray, {}))
        for after, line in (
            ('Select "a long quoted label" first.\nDon\'t stop.', 2),
            ('Select "a label that\nwraps" first.\nDon\'t stop.', 3),
        ):
            with self.subTest(text=after):
                findings = check_site.page_style_findings(after)
                self.assertEqual([finding.line for finding in findings], [line])

    def test_empty_name_list_matches_nothing(self):
        self.assertIsNone(check_site.names_pattern([]).search("Any text."))

    def test_phrases_match_across_line_breaks(self):
        self.assertTrue(check_site.check_editorial_style("Spin\nup a cluster.", {}))
        self.assertIn("dev-narrative", rules("This\nrepository change adds a flag."))

    def test_pending_and_review_rules_only_report(self):
        text = (
            "Turn off the flag. We recommend a backup. "
            "The logger now exports nothing.\n"
            "The helper examines the image. The Agent no longer starts.\n"
        )
        self.assertEqual(check_site.check_editorial_style(text, {}), [])
        self.assertEqual(
            {
                (finding.rule.name, finding.rule.level)
                for finding in check_site.page_style_findings(text)
            },
            {
                ("phrasal-verb", "pending"),
                ("first-person", "pending"),
                ("dev-narrative", "pending"),
                ("examine-subject", "review"),
                ("timeless", "review"),
            },
        )

    def test_stricter_rule_wins_an_overlap(self):
        findings = check_site.page_style_findings("The logger now exports nothing.")
        self.assertEqual(
            [(finding.rule.name, finding.rule.level) for finding in findings],
            [("dev-narrative", "pending")],
        )

    def test_promoted_rule_fails_the_check(self):
        promoted = tuple(
            rule._replace(level="error") if rule.name == "first-person" else rule
            for rule in check_site.STYLE_RULES
        )
        with mock.patch.object(check_site, "STYLE_RULES", promoted):
            self.assertTrue(check_site.check_editorial_style("We recommend it.", {}))
        self.assertEqual(check_site.check_editorial_style("We recommend it.", {}), [])

    def test_us_is_not_first_person(self):
        self.assertNotIn("first-person", rules("The budget is in US dollars."))
        self.assertIn("first-person", rules("Tell us about the error."))

    def test_findings_report_the_source_line(self):
        text = PAGE.format(title="Example", body="Intro.\n\n```bash\ndon't\n```\n")
        text += "\nDon't stop.\n"
        findings = check_site.page_style_findings(text)
        self.assertEqual(
            [item.line for item in findings if item.rule.name == "contraction"],
            [text.splitlines().index("Don't stop.") + 1],
        )

    def test_title_and_description_are_checked(self):
        text = PAGE.format(title="Spin up a cluster", body="Body.").replace(
            "A page.", "This page describes clusters."
        )
        findings = {
            (finding.rule.name, finding.line)
            for finding in check_site.page_style_findings(text)
        }
        self.assertIn(("phrasal-verb", 2), findings)
        self.assertIn(("description", 3), findings)

    def test_announcing_openings(self):
        for opening in (
            "This page describes the Helm values.",
            "An introduction to CetiRun.",
            "This guide explains the steps.",
        ):
            with self.subTest(opening=opening):
                self.assertIn("description", rules(f"{opening}\n\n## Next\n\nText.\n"))
        for text in (
            "This guide prepares the Entra resources.\n",
            "Configure storage.\n\nThis page describes it.\n",
        ):
            with self.subTest(text=text):
                self.assertNotIn("description", rules(text))

    def test_sentence_case(self):
        for heading, words in (
            ("Get Started", ["Started"]),
            ("Create and govern the Blueprint", ["Blueprint"]),
            ("Agent Connections and Identity", ["Connections", "Identity"]),
            ("Run `make` Targets", ["Targets"]),
            ("Get started", []),
            ("Models through the AI Gateway", []),
            ("Example: Configure the Team", []),
            ("2. Allow the routers in an Agent Policy", []),
            ("Select **Allowed Shared Models**", []),
            ("Kata-based Agents and the Team's skills", []),
            ("Application E2E lifecycle", []),
            ("Observability with ADX-Mon", []),
            ("Use Let's Encrypt", []),
            ("Enable Portal Chat", []),
            ("Use Let’s Encrypt", []),
            ("The Decision-1 deployment", []),
            ("Enable Policy-bound Teams", ["Policy"]),
        ):
            with self.subTest(heading=heading):
                self.assertEqual(check_site.sentence_case_words(heading), words)
        text = PAGE.format(title="Examples", body="Text.\n\n## Get Started {#start}")
        self.assertEqual(
            [
                (finding.line, finding.text)
                for finding in check_site.page_style_findings(text)
                if finding.rule.name == "heading-case"
            ],
            [(9, "Get Started (Started)")],
        )

    def test_sentence_length_limits(self):
        self.assertNotIn("step-length", rules("1. " + sentence(20)))
        self.assertIn("step-length", rules("1. " + sentence(21)))
        self.assertNotIn("sentence-length", rules("- " + sentence(25)))
        self.assertIn("sentence-length", rules("- " + sentence(26)))
        self.assertIn("sentence-length", rules(sentence(26)))
        arrow = sentence(25).replace(" end.", " → end.")
        self.assertNotIn("sentence-length", rules(arrow))

    def test_sentence_findings_report_their_line(self):
        text = f"Short one.\n{sentence(26)}\nShort two.\n1. Step one.\n{sentence(21)}"
        self.assertEqual(
            [
                (finding.rule.name, finding.line)
                for finding in check_site.page_style_findings(text)
            ],
            [("sentence-length", 2), ("step-length", 5)],
        )

    def test_paragraph_length(self):
        seven = "One. Two, e.g. Three. Four. Select **Five**. **Six** ends. Seven. End."
        self.assertNotIn("paragraph-length", rules(seven.replace(" End.", "")))
        self.assertIn("paragraph-length", rules(seven))
        quotes = "> One. Two. Three. Four.\n>\n> Five. Six. Seven.\n"
        self.assertNotIn("paragraph-length", rules(quotes))

    def test_negative_paragraph(self):
        self.assertIn("negative-paragraph", rules("It is not. It does not. It runs."))
        for text in (
            "It is not new. It stops. It runs.",
            "It is not new. It does not stop.",
            "It is not new. It does not stop. It runs. It ends.",
        ):
            with self.subTest(text=text):
                self.assertNotIn("negative-paragraph", rules(text))

    def test_repository_urls(self):
        self.assertIn(
            "repo-url", rules("[App](https://github.com/Azure/cetirun/tree/main/app)")
        )
        for text in (
            '[App]({{< repo "app" >}})',
            "[CetiRun](https://github.com/Azure/cetirun)",
            "Run `curl https://github.com/Azure/cetirun/raw/main/x`.",
        ):
            with self.subTest(text=text):
                self.assertNotIn("repo-url", rules(text))

    def test_term_case(self):
        self.assertIn("term-case", rules("Open the Portal. Then the portal is ready."))
        self.assertNotIn("term-case", rules("The portal is ready. Portal pages load."))

    def test_page_length(self):
        self.assertNotIn("page-length", rules("Text.\n" * 400))
        self.assertIn("page-length", rules("Text.\n" * 401))

    def test_rule_table(self):
        for rule in check_site.STYLE_RULES:
            with self.subTest(rule=rule.name, level=rule.level):
                self.assertIn(rule.level, check_site.STYLE_LEVELS)
                if rule.pattern:
                    re.compile(rule.pattern)
                else:
                    self.assertIs(check_site.style_rule(rule.name), rule)


class SiteStyleTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.site = Path(temp.name)
        self.navigation("Using Agents")

    def write(self, name, text):
        path = self.site / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")

    def navigation(self, group):
        self.write(
            "data/navigation.toml",
            '[[modules]]\nid = "guide"\ntitle = "User Guide"\npage = "/docs/a"\n'
            f'[[modules.groups]]\ntitle = "{group}"\npages = ["/docs/a", "/docs/b"]\n',
        )

    def page(self, name, title, body=""):
        self.write(f"content/en/docs/{name}.md", PAGE.format(title=title, body=body))

    def findings(self):
        report = check_site.cross_page_style_findings(self.site)
        return {
            (page, finding.rule.name, finding.line)
            for page, findings in report.items()
            for finding in findings
        }

    def test_duplicate_titles(self):
        self.page("a", "Using agents")
        self.page("b", "Using Agents")
        self.assertLessEqual(
            {
                ("content/en/docs/a.md", "duplicate-title", 2),
                ("content/en/docs/b.md", "duplicate-title", 2),
            },
            self.findings(),
        )

    def test_link_text_names_the_target(self):
        body = "## Install the tools\n\n## Remove the tools {#rm}\n"
        self.page("b", "Run local checks", body)
        self.page(
            "a",
            "Using Agents",
            "Refer to [Testing](../b/).\n"
            "Refer to [Run local checks](../b/).\n"
            "Refer to [Install the tools](../b/#install-the-tools).\n"
            "Refer to [Run local checks](../b/#install-the-tools).\n"
            "- [Tools](../b/#install-the-tools)\n"
            "Read the [local checks](../b/) first.\n"
            "See [run `local` checks](../b/).\n"
            "Refer to [Remove the tools](../b/#rm).\n",
        )
        self.assertEqual(
            {line for _, name, line in self.findings() if name == "link-title"},
            {7, 11},
        )

    def test_navigation_titles(self):
        self.page("a", "Using agents")
        self.page("b", "Other")
        self.assertIn(("data/navigation.toml", "heading-case", 6), self.findings())
        self.navigation("Get Started")
        self.assertIn(("data/navigation.toml", "heading-case", 6), self.findings())
        self.page("a", "Get started")
        self.navigation("Get started")
        self.assertEqual(self.findings(), set())

    def test_cross_page_errors_fail_the_content_check(self):
        policy_root = os.environ.get("CETIRUN_POLICY_ROOT")
        rule = check_site.style_rule("duplicate-title")
        fake = {
            f"content/en/docs/{name}.md": [
                check_site.StyleFinding(rule._replace(level=level), 2, name)
            ]
            for name, level in (("a", "error"), ("b", "pending"))
        }
        with (
            mock.patch.object(
                check_site, "cross_page_style_findings", return_value=fake
            ),
            mock.patch.object(check_site, "page_style_findings", return_value=[]),
        ):
            errors = check_site.check_content(
                SITE, Path(policy_root) if policy_root else None
            )
        self.assertIn(
            f"content/en/docs/a.md: line 2: {rule.advice} (duplicate-title): a", errors
        )
        self.assertFalse([error for error in errors if "docs/b.md" in error])


if __name__ == "__main__":
    unittest.main()
