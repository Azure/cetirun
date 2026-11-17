# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Offline regression tests: inject failures so a green check is meaningful."""

import importlib.util
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[1] / "scripts/check_site.py"
SPEC = importlib.util.spec_from_file_location("check_site", SCRIPT)
check_site = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(check_site)

BASE = "https://docs.example.com/cetirun/"
VALID_HTML = """<!doctype html><html lang="en"><head>
<title>Example</title><meta name="description" content="Example page">
</head><body><a href="#main-content">Skip to main content</a>
<main id="main-content"><h1>Example</h1><h2 id="details">Details</h2>
<input type="search" aria-label="Search"><img src="logo.jpg" alt="Logo">
</main></body></html>"""


class LinkTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def test_project_subpath(self):
        self.assertEqual(
            check_site.local_target(self.root, BASE + "docs/", BASE),
            self.root / "docs/index.html",
        )

    def test_same_origin_absolute_url_is_checked(self):
        self.assertEqual(
            check_site.local_target(self.root, BASE + "missing/", BASE),
            self.root / "missing/index.html",
        )

    def test_external_url_is_not_fetched(self):
        self.assertIsNone(
            check_site.local_target(self.root, "https://example.org/a", BASE)
        )

    def test_reject_similar_prefix(self):
        with self.assertRaisesRegex(ValueError, "outside site"):
            check_site.local_target(
                self.root, "https://docs.example.com/cetirun-other/", BASE
            )

    def test_reject_root_relative_escape(self):
        with self.assertRaisesRegex(ValueError, "outside site"):
            check_site.local_target(self.root, "https://docs.example.com/docs/", BASE)

    def test_reject_encoded_traversal(self):
        with self.assertRaisesRegex(ValueError, "escapes output"):
            check_site.local_target(self.root, BASE + "%2e%2e/private", BASE)

    def test_decode_fragment_path(self):
        self.assertEqual(
            check_site.local_target(self.root, BASE + "hello%20world/", BASE),
            self.root / "hello world/index.html",
        )

    def test_reject_script_scheme(self):
        with self.assertRaisesRegex(ValueError, "unsupported"):
            check_site.local_target(self.root, "javascript:alert(1)", BASE)

    def test_empty_build_fails(self):
        self.assertIn(
            "no generated home", check_site.check_generated(self.root, BASE)[0]
        )

    def test_broken_link_and_fragment_fail(self):
        html = VALID_HTML.replace(
            "</main>",
            '<a href="missing/">Missing</a><a href="#absent">Bad fragment</a></main>',
        )
        (self.root / "index.html").write_text(html)
        errors = "\n".join(check_site.check_generated(self.root, BASE))
        self.assertIn("missing target: missing/", errors)
        self.assertIn("missing fragment: #absent", errors)

    def test_external_script_fails(self):
        html = VALID_HTML.replace(
            "</body>", '<script src="https://cdn.example.org/a.js"></script></body>'
        )
        (self.root / "index.html").write_text(html)
        self.assertIn(
            "external runtime asset",
            "\n".join(check_site.check_generated(self.root, BASE)),
        )

    def test_page_url(self):
        self.assertEqual(
            check_site.page_url(Path("docs/a/index.html"), BASE), BASE + "docs/a/"
        )
        self.assertEqual(check_site.page_url(Path("index.html"), BASE), BASE)

    def test_root_site_url(self):
        base = "https://docs.example.com/"
        self.assertEqual(
            check_site.local_target(self.root, base + "docs/", base),
            self.root / "docs/index.html",
        )


class AccessibilityTests(unittest.TestCase):
    def errors(self, html):
        return check_site.Document(html).accessibility_errors()

    def test_valid_document(self):
        self.assertEqual(self.errors(VALID_HTML), [])

    def test_image_missing_alt(self):
        self.assertIn(
            "image has no alt attribute",
            self.errors(VALID_HTML.replace(' alt="Logo"', "")),
        )

    def test_duplicate_id(self):
        self.assertIn(
            "duplicate id: main-content",
            self.errors(VALID_HTML.replace('id="details"', 'id="main-content"')),
        )

    def test_missing_language(self):
        self.assertIn(
            "missing document language",
            self.errors(VALID_HTML.replace(' lang="en"', "")),
        )

    def test_heading_jump(self):
        self.assertIn(
            "heading jump: h1 to h3", self.errors(VALID_HTML.replace("h2", "h3"))
        )

    def test_multiple_h1(self):
        self.assertIn(
            "expected exactly one leading h1",
            self.errors(VALID_HTML.replace("h2", "h1")),
        )

    def test_missing_main(self):
        self.assertIn(
            "expected one main landmark, found 0",
            self.errors(
                VALID_HTML.replace("main ", "section ").replace("</main>", "</section>")
            ),
        )

    def test_missing_skip_link(self):
        self.assertIn(
            "missing working skip-to-content link",
            self.errors(VALID_HTML.replace('href="#main-content"', 'href="#details"')),
        )

    def test_input_needs_label(self):
        self.assertIn(
            "input has no accessible label",
            self.errors(VALID_HTML.replace(' aria-label="Search"', "")),
        )

    def test_label_can_follow_input(self):
        html = VALID_HTML.replace('aria-label="Search"', 'id="search"')
        html = html.replace("</main>", '<label for="search">Search</label></main>')
        self.assertEqual(self.errors(html), [])

    def test_contrast(self):
        self.assertAlmostEqual(check_site.contrast("#000000", "#ffffff"), 21)
        self.assertAlmostEqual(check_site.contrast("#ffffff", "#ffffff"), 1)


class ContentTests(unittest.TestCase):
    def test_frontmatter(self):
        metadata = check_site.frontmatter(
            '---\ntitle: "Test"\ndescription: "A test"\nweight: 10\n---\nBody'
        )
        self.assertEqual(metadata["title"], "Test")
        self.assertEqual(metadata["weight"], "10")

    def test_missing_frontmatter(self):
        self.assertEqual(check_site.frontmatter("# No metadata"), {})

    def test_current_styles_contrast(self):
        self.assertEqual(
            check_site.check_styles(
                SCRIPT.parents[1] / "assets/scss/_styles_project.scss"
            ),
            [],
        )

    # A light and a dark palette that pass each rule. A test changes one color
    # of one theme, so a rule that reads the other theme does not pass.
    LIGHT, DARK = ":root", 'html[data-bs-theme="dark"]'
    PALETTES = {
        LIGHT: {
            "bg": "#ffffff",
            "panel": "#ffffff",
            "soft": "#eeeeee",
            "text": "#111111",
            "muted": "#555555",
            "border": "#dddddd",
            "link": "#0000aa",
            "link-hover": "#000066",
            "accent": "#111111",
            "on-accent": "#ffffff",
            "warning-bg": "#fff4d6",
            "warning-text": "#663c00",
            "code-value": "#00308f",
        },
        DARK: {
            "bg": "#000000",
            "panel": "#111111",
            "soft": "#1a1a1a",
            "text": "#eeeeee",
            "muted": "#aaaaaa",
            "border": "#222222",
            "link": "#99bbff",
            "link-hover": "#ffffff",
            "accent": "#eeeeee",
            "on-accent": "#111111",
            "warning-bg": "#2a2110",
            "warning-text": "#f2c36b",
            "code-value": "#9fc1ff",
        },
    }

    def styles_with(self, theme: str | None = None, **changes: str) -> list[str]:
        """Check generated styles: the passing palettes, with tokens of one theme changed.

        The Bootstrap variables keep the colors of the passing palettes.
        """
        changed = {name.replace("_", "-"): color for name, color in changes.items()}
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        path = Path(temp.name) / "_styles_project.scss"
        path.write_text(
            "".join(
                f"{name} {{\n"
                + "".join(
                    f"  --cr-{token}: {color};\n"
                    for token, color in (palette | (changed if name == theme else {})).items()
                )
                + "}\n"
                for name, palette in self.PALETTES.items()
            )
            + ":focus-visible {} .cr-skip:focus {}\n"
            + "@media (max-width: 767px) {} @media (prefers-reduced-motion: reduce) {}\n"
        )
        path.with_name("_variables_project.scss").write_text(
            "".join(
                f"${variable}{suffix}: {palette[token]};\n"
                for suffix, palette in zip(("", "-dark"), self.PALETTES.values())
                for variable, token in check_site.BOOTSTRAP_COLORS.items()
            )
        )
        return check_site.check_styles(path)

    def test_generated_palettes_pass(self):
        self.assertEqual(self.styles_with(), [])

    def test_warning_label_must_be_readable_on_its_background(self):
        self.assertEqual(
            self.styles_with(self.LIGHT, warning_text="#a87900"),
            [f"{self.LIGHT}: insufficient warning-text/warning-bg contrast"],
        )

    def test_text_and_links_must_be_readable_in_a_warning(self):
        self.assertEqual(
            self.styles_with(self.LIGHT, warning_bg="#7a4a00"),
            [
                f"{self.LIGHT}: insufficient {text}/warning-bg contrast"
                for text in ("text", "link", "warning-text")
            ],
        )

    def test_code_values_must_be_readable_in_a_code_block(self):
        value = "#7e7e7e"
        dark = self.PALETTES[self.DARK]
        # The color is readable on the page, but not on the surface of a code block.
        self.assertGreaterEqual(check_site.contrast(value, dark["panel"]), 4.5)
        self.assertLess(check_site.contrast(value, dark["soft"]), 4.5)
        self.assertEqual(
            self.styles_with(self.DARK, code_value=value),
            [f"{self.DARK}: insufficient code-value/soft contrast"],
        )

    def test_bootstrap_colors_must_equal_the_tokens(self):
        # The changed colors stay readable, so the difference is the only finding.
        self.assertEqual(
            self.styles_with(self.LIGHT, muted="#444444"),
            [f"{self.LIGHT}: $body-secondary-color is not the --cr-muted color"],
        )
        self.assertEqual(
            self.styles_with(self.DARK, border="#333333"),
            [f"{self.DARK}: $border-color-dark is not the --cr-border color"],
        )

    def test_current_content_and_pointers(self):
        policy_root = os.environ.get("CETIRUN_POLICY_ROOT")
        self.assertEqual(
            check_site.check_content(
                SCRIPT.parents[1], Path(policy_root) if policy_root else None
            ),
            [],
        )

    def test_current_toolchain(self):
        self.assertEqual(check_site.check_toolchain(SCRIPT.parents[1]), [])


class StyleReportTests(unittest.TestCase):
    def run_script(self, *arguments):
        return subprocess.run(
            [sys.executable, "-B", str(SCRIPT), *arguments],
            capture_output=True,
            text=True,
            check=False,
        )

    def test_style_report_needs_no_build(self):
        result = self.run_script("--style-report")
        self.assertEqual(result.returncode, 0, result.stderr)
        for rule in check_site.STYLE_RULES:
            self.assertRegex(
                result.stdout, rf"(?m)^{rule.name} +{rule.level} +\d+ +\d+$"
            )

    def test_site_check_still_needs_a_build(self):
        result = self.run_script()
        self.assertEqual(result.returncode, 2)
        self.assertIn("--public and --base-url are required", result.stderr)

    def test_report_names_pending_rules_without_findings(self):
        pending = [rule for rule in check_site.STYLE_RULES if rule.level == "pending"]
        finding = check_site.StyleFinding(pending[0], 3, "text")
        report = check_site.format_style_report({"content/en/docs/a.md": [finding]})
        self.assertIn("content/en/docs/a.md\n  pending: line 3:", report)
        ready = report.splitlines()[-1]
        self.assertNotIn(pending[0].name, ready)
        for rule in pending:
            if rule.name != pending[0].name:
                self.assertIn(rule.name, ready)


class ToolchainTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.repo = Path(temp.name)
        self.site = self.repo / "site"
        self.files = {
            ".go-version": "1.26.8\n",
            "site/go.mod": "module example.com/docs\n\ngo 1.26.8\n",
            "site/README.md": "Use Go 1.26.8.\n",
            "site/content/en/docs/contributing/site.md": "Use Go 1.26.8.\n",
            "site/hugo.toml": '[module]\nproxy = ""\n',
            ".github/workflows/cetirun-docs-validation.yml": (
                "with:\n  go-version-file: .go-version\n"
            ),
        }
        for name, text in self.files.items():
            path = self.repo / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(text, encoding="utf-8")

    def test_consistent_toolchain(self):
        self.assertEqual(check_site.check_toolchain(self.site), [])

    def test_module_version_drift(self):
        (self.site / "go.mod").write_text("go 1.26.5\n", encoding="utf-8")
        self.assertIn(
            "site/go.mod must match",
            "\n".join(check_site.check_toolchain(self.site)),
        )

    def test_documented_version_drift(self):
        for name in ("README.md", "content/en/docs/contributing/site.md"):
            with self.subTest(name=name):
                path = self.site / name
                original = path.read_text(encoding="utf-8")
                path.write_text("Use Go 1.26.5.\n", encoding="utf-8")
                self.assertIn(
                    f"site/{name}: document Go 1.26.8",
                    "\n".join(check_site.check_toolchain(self.site)),
                )
                path.write_text(original, encoding="utf-8")

    def test_missing_version_instruction(self):
        (self.site / "README.md").write_text("Install Go.\n", encoding="utf-8")
        self.assertIn(
            "document Go 1.26.8",
            "\n".join(check_site.check_toolchain(self.site)),
        )

    def test_workflow_uses_canonical_version(self):
        workflow = self.repo / ".github/workflows/cetirun-docs-validation.yml"
        workflow.write_text("with:\n  go-version: '1.26.5'\n", encoding="utf-8")
        self.assertIn(
            "documentation CI must use",
            "\n".join(check_site.check_toolchain(self.site)),
        )

    def test_missing_toolchain_source(self):
        (self.repo / ".go-version").unlink()
        self.assertEqual(
            check_site.check_toolchain(self.site),
            ["missing toolchain source: .go-version"],
        )

    def test_unpinned_go_version(self):
        (self.repo / ".go-version").write_text("stable\n", encoding="utf-8")
        self.assertEqual(
            check_site.check_toolchain(self.site),
            [".go-version must pin an exact Go release"],
        )

    def test_hugo_must_not_override_offline_proxy(self):
        (self.site / "hugo.toml").write_text(
            '[module]\nproxy = "https://proxy.example.com"\n', encoding="utf-8"
        )
        self.assertIn(
            "Hugo module proxy must inherit GOPROXY",
            "\n".join(check_site.check_toolchain(self.site)),
        )

    def test_hugo_default_direct_proxy_is_not_offline(self):
        (self.site / "hugo.toml").write_text("[module]\n", encoding="utf-8")
        self.assertIn(
            "Hugo module proxy must inherit GOPROXY",
            "\n".join(check_site.check_toolchain(self.site)),
        )


if __name__ == "__main__":
    unittest.main()
