# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().parents[2] / "app/cetirun-chart/agents/openclaw/prune-acpx-runtime.py"
SPEC = importlib.util.spec_from_file_location("prune_acpx_runtime", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class RuntimeMetadataTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.projects = self.root / "projects"
        self.plugin = self.projects / "acpx-example/node_modules/@openclaw/acpx"
        self.plugin.mkdir(parents=True)
        self.package = {"name": "@openclaw/acpx", "version": "2026.9.6",
                        "exports": {".": {"types": "./index.d.ts", "import": "./index.js"}}}
        self.write_package()

    def write_package(self):
        (self.plugin / "package.json").write_text(json.dumps(self.package))

    def test_only_build_metadata_is_removed_from_the_selected_project(self):
        keep = ["index.js", "implementation.ts", "implementation.mts", "implementation.cts",
                "native.node", "agent-binary", "LICENSE", "package.json"]
        remove = ["index.d.ts", "index.d.mts", "index.d.cts", "index.js.map", "index.d.ts.map"]
        for name in keep[:-1] + remove:
            (self.plugin / name).write_text("synthetic fixture")
        external = self.root / "external"
        external.mkdir()
        (external / "keep.d.ts").write_text("preserve")
        (self.plugin / "linked").symlink_to(external, target_is_directory=True)
        (self.plugin / "linked.map").symlink_to(external / "keep.d.ts")
        other = self.projects / "another-plugin/node_modules"
        other.mkdir(parents=True)
        (other / "keep.d.ts").write_text("preserve")
        self.assertEqual(MODULE.prune(self.projects, "2026.9.6"), len(remove))
        self.assertTrue(all((self.plugin / name).is_file() for name in keep))
        self.assertTrue(all(not (self.plugin / name).exists() for name in remove))
        self.assertTrue((self.plugin / "linked.map").is_symlink())
        self.assertEqual((external / "keep.d.ts").read_text(), "preserve")
        self.assertEqual((other / "keep.d.ts").read_text(), "preserve")
        self.assertEqual(MODULE.prune(self.projects, "2026.9.6"), 0)

    def test_a_runtime_entry_point_refuses_pruning_before_any_delete(self):
        for field in ("main", "module", "bin", "browser", "exports", "imports"):
            with self.subTest(field=field):
                self.package = {"name": "@openclaw/acpx", "version": "2026.9.6",
                                field: {"default": "./runtime.d.ts"}}
                self.write_package()
                (self.plugin / "first.map").write_text("preserve")
                with self.assertRaisesRegex(ValueError, "runtime entry point"):
                    MODULE.prune(self.projects, "2026.9.6")
                self.assertEqual((self.plugin / "first.map").read_text(), "preserve")

    def test_unexpected_package_version_and_ambiguous_projects_are_rejected(self):
        with self.assertRaisesRegex(ValueError, "pinned OpenClaw version"):
            MODULE.prune(self.projects, "different-version")
        duplicate = self.projects / "duplicate/node_modules/@openclaw/acpx"
        duplicate.mkdir(parents=True)
        (duplicate / "package.json").write_text(json.dumps(self.package))
        with self.assertRaisesRegex(ValueError, "exactly one"):
            MODULE.prune(self.projects, "2026.9.6")


if __name__ == "__main__":
    unittest.main()
