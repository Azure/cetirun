# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

import importlib.util
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch


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
        self.lock = self.projects / "acpx-example/package-lock.json"
        self.lock_data = {
            "name": ".openclaw-install-stage-reviewed",
            "lockfileVersion": 3,
            "requires": True,
            "packages": {
                "": {"dependencies": {"@openclaw/acpx": "2026.9.6"}},
                "node_modules/@openclaw/acpx": {
                    "version": "2026.9.6",
                    "resolved": "https://registry.npmjs.org/@openclaw/acpx/-/acpx-2026.9.6.tgz",
                    "integrity": "sha512-synthetic-reviewed",
                },
            },
        }
        self.lock.write_text(json.dumps(self.lock_data))
        self.addCleanup(patch.stopall)
        reviewed = {key: value for key, value in self.lock_data.items() if key != "name"}
        patch.object(MODULE, "REVIEWED_LOCK_SHA256", hashlib.sha256(
            json.dumps(reviewed, sort_keys=True, separators=(",", ":")).encode()).hexdigest()).start()

    def qualify_metadata(self):
        project = self.projects / "acpx-example"
        paths = sorted(p.relative_to(project).as_posix() for p in project.rglob("*")
                       if p.is_file() and not p.is_symlink() and MODULE.is_metadata(p.name))
        patch.object(MODULE, "REVIEWED_METADATA_PATHS_SHA256",
                     hashlib.sha256(("\n".join(paths) + "\n").encode()).hexdigest()).start()

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
        self.qualify_metadata()
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

    def test_dependency_or_metadata_inventory_drift_fails_before_deletion(self):
        (self.plugin / "keep.map").write_text("preserve")
        self.qualify_metadata()
        (self.plugin / "new.d.ts").write_text("unreviewed")
        with self.assertRaisesRegex(ValueError, "metadata inventory"):
            MODULE.prune(self.projects, "2026.9.6")
        self.assertTrue((self.plugin / "keep.map").exists())
        (self.plugin / "new.d.ts").unlink()
        self.lock.write_text('{"synthetic": "changed dependencies"}')
        with self.assertRaisesRegex(ValueError, "dependency lock"):
            MODULE.prune(self.projects, "2026.9.6")
        self.assertTrue((self.plugin / "keep.map").exists())

    def test_only_the_lock_root_name_and_json_formatting_are_ignored(self):
        (self.plugin / "index.d.ts").write_text("synthetic declaration")
        self.qualify_metadata()
        self.lock_data["name"] = ".openclaw-install-stage-new"
        self.lock.write_text(json.dumps(self.lock_data, sort_keys=True, indent=4) + "\n")
        self.assertEqual(MODULE.prune(self.projects, "2026.9.6"), 1)

    def test_dependency_fields_still_fail_before_deletion(self):
        (self.plugin / "index.d.ts").write_text("preserve")
        self.qualify_metadata()
        package = ("packages", "node_modules/@openclaw/acpx")
        changes = [(*package, field) for field in
                   ("name", "version", "resolved", "integrity", "hasInstallScript")]
        changes += [("packages", "", "dependencies", "@openclaw/acpx"), ("lockfileVersion",)]
        for path in changes:
            with self.subTest(path=path):
                lock = json.loads(json.dumps(self.lock_data))
                target = lock
                for part in path[:-1]:
                    target = target[part]
                target[path[-1]] = "unreviewed"
                self.lock.write_text(json.dumps(lock))
                with self.assertRaisesRegex(ValueError, "dependency lock"):
                    MODULE.prune(self.projects, "2026.9.6")
                self.assertEqual((self.plugin / "index.d.ts").read_text(), "preserve")

    def test_unexpected_or_missing_lock_root_name_fails_before_deletion(self):
        (self.plugin / "index.d.ts").write_text("preserve")
        self.qualify_metadata()
        for name in (None, "", "unrelated-project", ".openclaw-install-stage-", 123):
            with self.subTest(name=name):
                lock = dict(self.lock_data)
                if name is None:
                    del lock["name"]
                else:
                    lock["name"] = name
                self.lock.write_text(json.dumps(lock))
                with self.assertRaisesRegex(ValueError, "dependency lock root name"):
                    MODULE.prune(self.projects, "2026.9.6")
                self.assertEqual((self.plugin / "index.d.ts").read_text(), "preserve")

    @unittest.skipUnless(shutil.which("npm"), "requires npm for fresh offline installations")
    def test_fresh_npm_installs_in_random_stages_share_one_reviewed_fingerprint(self):
        # A local tarball exercises npm's real lock generation without registry
        # access, lifecycle scripts, existing node_modules, or a reused lock.
        tarball = self.root / "acpx-fixture.tgz"
        with tarfile.open(tarball, "w:gz") as archive:
            for name, content in {
                "package.json": json.dumps({"name": "@openclaw/acpx", "version": "2026.9.6",
                                            "main": "index.js", "types": "index.d.ts"}),
                "index.js": "exports.fixture = true;\n",
                "index.d.ts": "export declare const fixture: boolean;\n",
            }.items():
                data = content.encode()
                info = tarfile.TarInfo("package/" + name)
                info.size = len(data)
                archive.addfile(info, io.BytesIO(data))
        for config in ("user.npmrc", "global.npmrc"):
            (self.root / config).write_text("")
        env = {"PATH": os.environ["PATH"], "HOME": str(self.root / "npm-home"),
               "NPM_CONFIG_USERCONFIG": str(self.root / "user.npmrc"),
               "NPM_CONFIG_GLOBALCONFIG": str(self.root / "global.npmrc"),
               "NPM_CONFIG_CACHE": str(self.root / "npm-cache")}
        locks = []
        for run in range(2):
            projects = self.root / f"fresh-{run}"
            projects.mkdir()
            project = Path(tempfile.mkdtemp(prefix=".openclaw-install-stage-", dir=projects))
            (project / "package.json").write_text(json.dumps({
                "private": True, "dependencies": {"@openclaw/acpx": "file:" + str(tarball)}}))
            result = subprocess.run(
                ["npm", "install", "--offline", "--ignore-scripts", "--no-audit", "--no-fund",
                 "--omit=dev", "--omit=peer", "--legacy-peer-deps"],
                cwd=project, env=env, capture_output=True, text=True, timeout=60,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            lock = (project / "package-lock.json").read_bytes()
            self.assertEqual(json.loads(lock)["name"], project.name)
            locks.append(lock)
            if run == 0:
                patch.object(MODULE, "REVIEWED_LOCK_SHA256", MODULE.dependency_lock_digest(lock)).start()
                inventory = "node_modules/@openclaw/acpx/index.d.ts\n"
                patch.object(MODULE, "REVIEWED_METADATA_PATHS_SHA256",
                             hashlib.sha256(inventory.encode()).hexdigest()).start()
            self.assertEqual(MODULE.prune(projects, "2026.9.6"), 1)
            self.assertTrue((project / "node_modules/@openclaw/acpx/index.js").is_file())
        self.assertNotEqual(hashlib.sha256(locks[0]).digest(), hashlib.sha256(locks[1]).digest())
        self.assertEqual(MODULE.dependency_lock_digest(locks[0]), MODULE.dependency_lock_digest(locks[1]))

    def test_symlinked_identity_manifest_and_package_directory_are_rejected(self):
        anchor = self.plugin / "package.json"
        external = self.root / "external-package.json"
        anchor.rename(external)
        anchor.symlink_to(external)
        with self.assertRaisesRegex(ValueError, "identity path"):
            MODULE.prune(self.projects, "2026.9.6")
        anchor.unlink()
        external.rename(anchor)
        real_plugin = self.root / "real-plugin"
        self.plugin.rename(real_plugin)
        self.plugin.symlink_to(real_plugin, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, "identity path"):
            MODULE.prune(self.projects, "2026.9.6")


if __name__ == "__main__":
    unittest.main()
