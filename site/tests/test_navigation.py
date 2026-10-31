# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Verify module-scoped navigation and the single short Roadmap contract."""

import copy
import importlib.util
import tempfile
import unittest
from pathlib import Path

import tomllib

SITE = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "check_site", SITE / "scripts/check_site.py"
)
check_site = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(check_site)
MODULES = tomllib.loads((SITE / "data/navigation.toml").read_text())["modules"]


class NavigationTests(unittest.TestCase):
    def document(self, module_id, page, base="/cetirun/"):
        module = next(item for item in MODULES if item["id"] == module_id)
        top = []
        for item in MODULES:
            active = ' aria-current="true"' if item == module else ""
            href = base + item["page"].lstrip("/") + "/"
            top.append(
                f'<a data-nav-top="{item["id"]}" href="{href}"{active}>{item["title"]}</a>'
            )
        top.append(
            '<a data-nav-top="github" href="https://github.com/Azure/cetirun">GitHub</a>'
        )
        side = []
        for path in check_site.navigation_pages(module):
            active = ' aria-current="page"' if path == page else ""
            side.append(f'<a href="{base}{path.lstrip("/")}/"{active}>Page</a>')
        return check_site.Document(
            '<nav aria-label="Main navigation">'
            + "".join(top)
            + f'</nav><nav data-nav-module="{module_id}">'
            + "".join(side)
            + "</nav><main><a href='/cetirun/docs/guides/'>Related reading</a></main>"
        )

    def errors(self, document, page, base="https://docs.example.com/cetirun/"):
        return check_site.check_module_navigation(
            document, Path(page.lstrip("/") + "/index.html"), base, MODULES
        )

    def test_all_pages_stay_in_their_module(self):
        for module in MODULES:
            for page in check_site.navigation_pages(module):
                with self.subTest(page=page):
                    self.assertEqual(
                        self.errors(self.document(module["id"], page), page), []
                    )

    def test_old_docs_entry_selects_overview(self):
        document = self.document("overview", "/docs/overview")
        self.assertEqual(self.errors(document, "/docs"), [])

    def test_alternate_base_path(self):
        document = self.document("user-guide", "/docs/getting-started", "/preview/")
        self.assertEqual(
            self.errors(
                document, "/docs/getting-started", "https://docs.example.com/preview/"
            ),
            [],
        )

    def test_root_base_path(self):
        document = self.document("user-guide", "/docs/getting-started", "/")
        self.assertEqual(
            self.errors(document, "/docs/getting-started", "https://docs.example.com/"),
            [],
        )

    def test_hidden_cross_module_link_fails(self):
        document = self.document("overview", "/docs/overview")
        document.sidebar_links.append("/cetirun/docs/guides/")
        self.assertIn(
            "sidebar contains cross-module or unexpected links",
            self.errors(document, "/docs/overview"),
        )

    def test_missing_module_page_fails(self):
        document = self.document("overview", "/docs/overview")
        document.sidebar_links.pop()
        self.assertIn(
            "sidebar is missing module pages", self.errors(document, "/docs/overview")
        )

    def test_duplicate_sidebar_link_fails(self):
        document = self.document("overview", "/docs/overview")
        document.sidebar_links.append(document.sidebar_links[0])
        self.assertIn(
            "sidebar contains duplicate links", self.errors(document, "/docs/overview")
        )

    def test_wrong_active_module_fails(self):
        document = self.document("overview", "/docs/overview")
        document.top_links[1]["current"] = "true"
        self.assertIn(
            "top navigation must mark exactly the current module",
            self.errors(document, "/docs/overview"),
        )

    def test_wrong_current_page_fails(self):
        document = self.document("overview", "/docs/overview")
        document.sidebar_current = ["/cetirun/docs/security/"]
        self.assertIn(
            "sidebar must mark exactly the current page",
            self.errors(document, "/docs/overview"),
        )

    def test_wrong_top_labels_fail(self):
        document = self.document("overview", "/docs/overview")
        document.top_links[0]["text"] = "Documentation"
        self.assertIn(
            "top navigation must be Overview, User Guide, Operations, Reference, Contributing, GitHub",
            self.errors(document, "/docs/overview"),
        )

    def test_top_destination_must_match_module(self):
        document = self.document("overview", "/docs/overview")
        document.top_links[0]["href"] = "/cetirun/docs/"
        self.assertIn(
            "wrong top navigation destination: overview",
            self.errors(document, "/docs/overview"),
        )

    def test_wrong_sidebar_module_fails(self):
        document = self.document("overview", "/docs/overview")
        document.sidebar_module = "operations"
        self.assertIn(
            "expected overview sidebar", self.errors(document, "/docs/overview")
        )

    def test_duplicate_page_assignment_fails(self):
        modules = copy.deepcopy(MODULES)
        modules[1]["groups"][0]["pages"].append("/docs/overview")
        errors = check_site.check_module_navigation(
            self.document("overview", "/docs/overview"),
            Path("docs/overview/index.html"),
            "https://docs.example.com/cetirun/",
            modules,
        )
        self.assertIn(
            "page must belong to one navigation module: /docs/overview", errors
        )

    def test_home_does_not_select_a_module(self):
        document = self.document("overview", "/docs/overview")
        for link in document.top_links:
            link["current"] = ""
        self.assertEqual(
            check_site.check_module_navigation(
                document,
                Path("index.html"),
                "https://docs.example.com/cetirun/",
                MODULES,
            ),
            [],
        )

    def test_previous_guides_entry_selects_operations(self):
        document = self.document("operations", "/docs/operations")
        self.assertEqual(self.errors(document, "/docs/guidances"), [])

    def test_install_page_uses_operations_at_its_existing_url(self):
        page = "/docs/getting-started/platform"
        self.assertEqual(self.errors(self.document("operations", page), page), [])

    def test_reference_does_not_appear_in_operations_sidebar(self):
        document = self.document("operations", "/docs/operations")
        document.sidebar_links.append("/cetirun/docs/reference/helm/")
        self.assertIn(
            "sidebar contains cross-module or unexpected links",
            self.errors(document, "/docs/operations"),
        )


class SourceContractTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.site = Path(temp.name)
        (self.site / "data").mkdir()
        (self.site / "data/navigation.toml").write_bytes(
            (SITE / "data/navigation.toml").read_bytes()
        )
        for module in MODULES:
            for page in check_site.navigation_pages(module) + module.get(
                "extra_paths", []
            ):
                self.write(page + "/_index.md", "# Fixture\n")

    def write(self, relative, content):
        path = self.site / "content/en" / relative.lstrip("/")
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content)
        return path

    def roadmap(self, body="A short list of improvements."):
        # Replace the synthetic section with the real leaf-page shape.
        (self.site / "content/en/docs/overview/roadmap/_index.md").unlink()
        return self.write(
            "/docs/overview/roadmap.md",
            '---\ntitle: "Roadmap"\nstatus: planned\n---\n\n**Status: planned.**\n'
            + body,
        )

    def test_current_config_covers_every_page(self):
        self.assertEqual(check_site.check_navigation_config(SITE), [])

    def test_approved_module_names(self):
        self.assertEqual(
            [module["title"] for module in MODULES],
            ["Overview", "User Guide", "Operations", "Reference", "Contributing"],
        )

    def test_operations_groups(self):
        operations = next(module for module in MODULES if module["id"] == "operations")
        self.assertEqual(
            [group["title"] for group in operations["groups"] if group["title"]],
            ["Install", "Configuration", "Troubleshooting"],
        )

    def test_reader_tasks_have_the_right_owner(self):
        owners = {
            page: module["id"]
            for module in MODULES
            for page in check_site.navigation_pages(module)
        }
        expected = {
            "user-guide": [
                "/docs/getting-started",
                "/docs/getting-started/first-instance",
                "/docs/guides",
                "/docs/guides/models",
                "/docs/guides/lifecycle",
                "/docs/guides/tailscale",
            ],
            "operations": [
                "/docs/getting-started/platform",
                "/docs/operations/infrastructure",
                "/docs/concepts/configuration",
                "/docs/guides/teams",
                "/docs/operations/storage",
                "/docs/operations/istio",
                "/docs/operations/troubleshooting",
            ],
            "reference": [
                "/docs/reference",
                "/docs/reference/crds",
                "/docs/reference/helm",
                "/docs/reference/scripts",
            ],
            "contributing": [
                "/docs/contributing",
                "/docs/contributing/development",
                "/docs/getting-started/local-checks",
                "/docs/contributing/site",
            ],
        }
        for owner, pages in expected.items():
            for page in pages:
                with self.subTest(page=page):
                    self.assertEqual(owners.get(page), owner)

    def test_fixture_config(self):
        self.assertEqual(check_site.check_navigation_config(self.site), [])

    def test_new_page_needs_assignment(self):
        self.write("/docs/guides/new.md", "# New guide\n")
        self.assertIn(
            "navigation page needs exactly one assignment: /docs/guides/new (0)",
            check_site.check_navigation_config(self.site),
        )

    def test_removed_page_fails(self):
        (self.site / "content/en/docs/guides/models/_index.md").unlink()
        self.assertIn(
            "missing navigation source: /docs/guides/models",
            check_site.check_navigation_config(self.site),
        )

    def test_current_roadmap_scope(self):
        self.assertEqual(check_site.check_roadmap_scope(SITE), [])

    def test_only_one_planned_page(self):
        self.roadmap()
        self.write("/docs/guides/future.md", "---\nstatus: planned\n---\n")
        self.assertIn(
            "planned content belongs only on Roadmap: /docs/guides/future",
            check_site.check_roadmap_scope(self.site),
        )

    def test_roadmap_stays_short(self):
        self.roadmap("word " * 260)
        self.assertIn(
            "Roadmap must remain a short overview (at most 250 words)",
            check_site.check_roadmap_scope(self.site),
        )

    def test_old_plan_is_not_reintroduced(self):
        self.roadmap()
        self.write("/docs/concepts/platform-direction.md", "# Old plan\n")
        self.assertIn(
            "remove the detailed platform-direction content: /docs/concepts/platform-direction",
            check_site.check_roadmap_scope(self.site),
        )


if __name__ == "__main__":
    unittest.main()
