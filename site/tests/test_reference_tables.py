# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Keep the Reference tables aligned with the CRDs and the chart values."""

import difflib
import importlib.util
import re
import sys
import unittest
from pathlib import Path
from unittest import mock

SITE = Path(__file__).resolve().parents[1]
REPO = SITE.parent
SPEC = importlib.util.spec_from_file_location(
    "reference_tables", SITE / "scripts/reference_tables.py"
)
tables = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = tables
SPEC.loader.exec_module(tables)

REFERENCE = tables.CONTENT / "reference"
HELM_SECTIONS = tables.HELM_SECTIONS


def table(rows):
    return tables.Table("example", "reference/example.md", tables.VALUES, "", lambda: rows)


def table_rows(text, section):
    """Return the cells of the table rows below a heading, up to the next heading."""
    part = text.split(section, 1)[1].split("\n#", 1)[0]
    return [tuple(cell.strip() for cell in line.strip("|").split(" | ")) for line in part.splitlines() if line.startswith("| `")]


class GeneratedTablesTests(unittest.TestCase):
    def test_generated_tables_are_current(self):
        for path, (current, generated) in tables.generate().items():
            with self.subTest(page=path.name):
                if current != generated:
                    diff = "".join(
                        list(
                            difflib.unified_diff(
                                current.splitlines(True),
                                generated.splitlines(True),
                                "page",
                                "sources",
                            )
                        )[:40]
                    )
                    self.fail(f"run {tables.COMMAND}\n{diff}")

    def test_page_descriptions_are_kept(self):
        page = (
            "<!-- generated: example -->\n\n"
            "| Value | Default | Description |\n| --- | --- | --- |\n"
            "| `kept` | `1` | Edited text. |\n"
            "| `removed` | `2` | Old text. |\n\n"
            "<!-- /generated: example -->\n"
        )
        rows = [
            tables.Row("kept", ["`3`"], "Source text."),
            tables.Row("added", ["`4`"], "Source text of a new row."),
        ]
        updated = tables.update(page, [table(rows)])
        self.assertIn("| `kept` | `3` | Edited text. |", updated)
        self.assertIn("| `added` | `4` | Source text of a new row. |", updated)
        self.assertNotIn("removed", updated)

    def test_a_block_must_exist_once(self):
        with self.assertRaisesRegex(ValueError, "expected one block 'example'"):
            tables.update("No block.\n", [table([])])

    def test_unknown_blocks_are_rejected(self):
        page = REFERENCE / "crds.md"
        text = page.read_text() + "\n<!-- generated: missing-table -->\n<!-- /generated: missing-table -->\n"
        original = Path.read_text

        def read(path, *args, **kwargs):
            return text if path == page else original(path, *args, **kwargs)

        with mock.patch.object(Path, "read_text", read):
            self.assertEqual(
                tables.unknown_blocks(),
                [f"{page.relative_to(REPO)}: unknown generated block 'missing-table'"] * 2,
            )
            with self.assertRaisesRegex(ValueError, "unknown generated block 'missing-table'"):
                tables.generate()

    def test_lists_of_objects_show_only_their_size(self):
        node = tables.load_nodes("cidrs:\n  - cidr: 192.0.2.10/32\n    ports: [80]\n")
        self.assertEqual(tables.value_default(node.value["cidrs"]), "1 entry")
        node = tables.load_nodes("names: [a, b]\nempty: {}\ntext: \"x\"\n")
        self.assertEqual(tables.value_default(node.value["names"]), '`["a", "b"]`')
        self.assertEqual(tables.value_default(node.value["empty"]), "`{}`")
        self.assertEqual(tables.value_default(node.value["text"]), '`"x"`')

    def test_helm_docs_comments_describe_values(self):
        source = (
            "# Not a description.\n\n"
            "# -- Number of replicas.\n# Keep one.\n"
            "replicas: 1\n"
            "# A note without the marker.\nplain: true\n"
            "# -- Values below.\n# Example:\n#   other: 1\nother: 2\n"
        )
        rows = {row.key: row for row in tables._values_rows_from_node(tables.load_nodes(source), ())}
        self.assertEqual(rows["replicas"].description, "Number of replicas. Keep one.")
        self.assertEqual(rows["plain"].description, "")
        self.assertEqual(rows["other"].description, "Values below. Example: other: 1")

    def test_crd_validation_shows_schema_rules(self):
        schema = {
            "type": "object",
            "required": ["mode"],
            "properties": {
                "mode": {"type": "string", "enum": ["a", "b"], "default": "a"},
                "items": {
                    "type": "array",
                    "maxItems": 1,
                    "items": {"type": "string", "pattern": "^x$"},
                },
                "ref": {
                    "type": "object",
                    "required": ["name"],
                    "properties": {"name": {"type": "string", "minLength": 1}},
                },
            },
            "x-kubernetes-validations": [{"rule": "true", "message": "must be valid"}],
        }
        rows = []
        tables._property_rows(schema, "", set(), rows)
        cells = {row.key: row.cells for row in rows}
        self.assertEqual(cells["mode"], ["string", '`"a"`', "Required. One of `a`, `b`."])
        self.assertEqual(cells["items"][2], "At most 1 item. Each item: Pattern `^x$`.")
        self.assertEqual(cells["ref"][0], "reference (name)")
        self.assertEqual(cells["ref"][2], "`name`: Required. At least 1 character.")
        self.assertNotIn("ref.name", cells)


class LoaderTests(unittest.TestCase):
    def test_loader_reads_the_yaml_of_the_sources(self):
        source = """---
# comment
map:
  plain: value with spaces
  quoted: "a \\"b\\" \\u00e9"
  single: 'it''s'
  folded: first
    second

    third
  multi: 'it''s
    two'
  literal: |-
    line 1
      line 2
  keep: |+
    end

  numbers: [1, 0x10, 010, 1.5, -2]
  flow: {a: [x, "y, z"], b: {}}
  nulls: [null, ~, ""]
  bools: [true, false, yes, off]
list:
- name: a
  value: 1
- - nested
-
  deep: true
empty:
"""
        self.assertEqual(
            tables.load(source),
            {
                "map": {
                    "plain": "value with spaces",
                    "quoted": 'a "b" é',
                    "single": "it's",
                    "folded": "first second\nthird",
                    "multi": "it's two",
                    "literal": "line 1\n  line 2",
                    "keep": "end\n\n",
                    "numbers": [1, 16, 8, 1.5, -2],
                    "flow": {"a": ["x", "y, z"], "b": {}},
                    "nulls": [None, None, ""],
                    "bools": [True, False, True, False],
                },
                "list": [{"name": "a", "value": 1}, ["nested"], {"deep": True}],
                "empty": None,
            },
        )

    def test_loader_keeps_the_comment_above_each_key(self):
        source = (
            "# file header\n\n"
            "# -- parent\nparent:\n"
            "  # -- first child\n  first: 1\n"
            "  # -- second child\n  second: 2\n"
            "# -- next key\nnext: 3\n"
            "# -- comment that a blank line ends\n\nlast: 4\n"
        )
        node = tables.load_nodes(source).value
        self.assertEqual(node["parent"].comment, "# -- parent")
        self.assertEqual(node["parent"].value["first"].comment, "# -- first child")
        self.assertEqual(node["parent"].value["second"].comment, "# -- second child")
        self.assertEqual(node["next"].comment, "# -- next key")
        self.assertEqual(node["last"].comment, "")

    def test_loader_rejects_yaml_that_it_does_not_support(self):
        for source, message in (
            ("a: &x 1\nb: *x\n", "anchors"),
            ("a: !!str 1\n", "tags"),
            ("a:\n\tb: 1\n", "tabs"),
            ("a: 1\n---\nb: 2\n", "one YAML document"),
            ("a: 2026-11-11\n", "timestamp"),
            ("a: 1\na: 2\n", "duplicate key"),
            ("a: [1, 2\n", "unterminated flow"),
        ):
            with self.subTest(source=source):
                with self.assertRaisesRegex(tables.YamlError, message):
                    tables.load(source)

    def test_loader_matches_pyyaml(self):
        try:
            import yaml
        except ImportError:
            self.skipTest("PyYAML is not installed; the other tests cover the loader")
        loader = getattr(yaml, "CSafeLoader", yaml.SafeLoader)
        paths = {REPO / table.source for table in tables.TABLES}
        for chart in tables.charts().values():
            paths |= {REPO / chart / "values.yaml", REPO / chart / "Chart.yaml"}
        for path in sorted(paths):
            with self.subTest(source=path.relative_to(REPO).as_posix()):
                text = path.read_text()
                self.assertEqual(tables.load(text), yaml.load(text, Loader=loader))


class CuratedReferenceTests(unittest.TestCase):
    def test_helm_reference_names_existing_values(self):
        text = (REFERENCE / "helm.md").read_text()
        self.assertEqual(tables.unknown_values(text, HELM_SECTIONS), [])

    def test_unknown_values_are_reported(self):
        text = "## Operator chart\n\n| Value | Default |\n| --- | --- |\n| `replicaCount`, `noSuchValue` | `2` |\n"
        self.assertEqual(
            tables.unknown_values(text, HELM_SECTIONS),
            ["line 5: noSuchValue is not a value of the cetirun-operator chart"],
        )

    def test_agent_condition_types_are_documented(self):
        api = REPO / "app/cetirun-operator"
        sources = (
            (api / "api/v1alpha1/agent_types.go", r'\bCondition\w+\s*=\s*"(\w+)"'),
            (api / "api/v1alpha1/migration.go", r'\bCondition\w+\s*=\s*"(\w+)"'),
            (api / "internal/controller/workload_health.go", r'\bbuiltInPrerequisitesReady\s*=\s*"(\w+)"'),
        )
        types = {name for path, pattern in sources for name in re.findall(pattern, path.read_text())}
        documented = {cells[0].strip("`") for cells in table_rows((REFERENCE / "crds.md").read_text(), "### Agent conditions")}
        self.assertTrue(types)
        self.assertEqual(documented, types)

    def test_team_compatibility_codes_are_documented(self):
        source = (REPO / "app/cetirun-operator/api/v1alpha1/team_types.go").read_text()
        block = source.split("var compatibilityIssueMessages", 1)[1].split("\n}", 1)[0]
        codes = [(f"`{code}`", message) for code, message in re.findall(r'"([a-z_]+)":\s*"([^"]+)"', block)]
        self.assertTrue(codes)
        self.assertEqual(table_rows((REFERENCE / "crds.md").read_text(), "### Team compatibility"), codes)


if __name__ == "__main__":
    unittest.main()
