# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

import importlib.util
import base64
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import threading
import unittest
from unittest.mock import patch
from urllib.parse import unquote


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

    def test_reviewed_install_inputs_match_the_guard_and_preserve_managed_policy(self):
        source = SCRIPT.parent / "acpx-runtime"
        manifest = json.loads((source / "package.json").read_text())
        lock_bytes = (source / "package-lock.json").read_bytes()
        lock = json.loads(lock_bytes)
        # setUp replaces the digest for synthetic fixtures; read the actual
        # reviewed constant here to keep the checked-in inputs tied to it.
        actual = importlib.util.module_from_spec(SPEC)
        SPEC.loader.exec_module(actual)
        dockerfile = (SCRIPT.parent / "Dockerfile").read_text()
        plugin_version = manifest["dependencies"]["@openclaw/acpx"]
        self.assertIn(f"ARG OPENCLAW_VERSION={plugin_version}", dockerfile.splitlines(),
                      "the managed installer selects the plugin at the image's pinned host version")
        self.assertEqual(lock["packages"]["node_modules/@openclaw/acpx"]["version"], plugin_version)
        self.assertEqual(actual.dependency_lock_digest(lock_bytes), actual.REVIEWED_LOCK_SHA256)
        self.assertEqual(manifest["dependencies"], lock["packages"][""]["dependencies"])
        self.assertEqual(set(manifest["openclaw"]["managedPeerDependencies"]),
                         set(manifest["dependencies"]) - {"@openclaw/acpx"})
        self.assertEqual(set(manifest["openclaw"]["managedOverrides"]),
                         set(manifest["overrides"]) - {"zod@4", "smol-toml@1.8.0"})
        self.assertEqual(manifest["dependencies"]["zod"], "4.6.6")
        self.assertEqual(manifest["overrides"]["zod@4"], "4.6.6")
        self.assertEqual(lock["packages"]["node_modules/zod"]["version"], "4.6.6")
        self.assertEqual(manifest["overrides"]["smol-toml@1.8.0"], "1.9.1")
        self.assertEqual(lock["packages"]["node_modules/smol-toml"]["version"],
                         manifest["overrides"]["smol-toml@1.8.0"])
        seed = "COPY --chown=vibe:vibe agents/openclaw/acpx-runtime/ /usr/local/share/openclaw-npm/projects/openclaw-acpx-052d680d6d/"
        self.assertLess(dockerfile.index(seed), dockerfile.index('openclaw plugins install "npm:@openclaw/acpx'))

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

    @unittest.skipUnless(shutil.which("npm"), "requires npm for fresh loopback-only installations")
    def test_seeded_lock_preserves_reviewed_transitive_version_after_new_release(self):
        packages = {}
        archives = {}
        for name, version in (("@openclaw/acpx", "2026.9.6"),
                              ("cetirun-fixture-dependency", "1.0.0"),
                              ("cetirun-fixture-dependency", "1.0.1")):
            manifest = {"name": name, "version": version, "main": "index.js", "types": "index.d.ts"}
            if name == "@openclaw/acpx":
                manifest["dependencies"] = {"cetirun-fixture-dependency": "^1.0.0"}
            buffer = io.BytesIO()
            with tarfile.open(fileobj=buffer, mode="w:gz") as archive:
                for filename, content in {"package.json": json.dumps(manifest),
                                          "index.js": "exports.fixture = true;\n",
                                          "index.d.ts": "export declare const fixture: boolean;\n"}.items():
                    data = content.encode()
                    info = tarfile.TarInfo("package/" + filename)
                    info.size = len(data)
                    archive.addfile(info, io.BytesIO(data))
            route = "/tarballs/" + name.replace("/", "-") + "-" + version + ".tgz"
            archives[route] = buffer.getvalue()
            packages[(name, version)] = {**manifest, "route": route}
        published = ["1.0.0"]

        class Registry(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def do_GET(self):
                route = unquote(self.path)
                if route in archives:
                    data = archives[route]
                else:
                    name = route.removeprefix("/")
                    versions = {}
                    for (package, version), item in packages.items():
                        if package != name or (name != "@openclaw/acpx" and version not in published):
                            continue
                        metadata = {k: v for k, v in item.items() if k != "route"}
                        metadata["dist"] = {
                            "tarball": f"http://127.0.0.1:{self.server.server_port}" + item["route"],
                            "integrity": "sha512-" + base64.b64encode(
                                hashlib.sha512(archives[item["route"]]).digest()).decode(),
                        }
                        versions[version] = metadata
                    if not versions:
                        self.send_error(404)
                        return
                    data = json.dumps({"name": name, "dist-tags": {"latest": max(versions)},
                                       "versions": versions}).encode()
                self.send_response(200)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

        server = ThreadingHTTPServer(("127.0.0.1", 0), Registry)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(thread.join, 2)
        self.addCleanup(server.shutdown)
        for name in ("user.npmrc", "global.npmrc"):
            (self.root / name).write_text("")
        env = {"PATH": os.environ["PATH"], "HOME": str(self.root / "npm-home"),
               "NPM_CONFIG_USERCONFIG": str(self.root / "user.npmrc"),
               "NPM_CONFIG_GLOBALCONFIG": str(self.root / "global.npmrc"),
               "NPM_CONFIG_CACHE": str(self.root / "npm-cache")}

        def install(label, seed=None):
            projects = self.root / label
            project = projects / (".openclaw-install-stage-" + label)
            project.mkdir(parents=True)
            if seed:
                for name in ("package.json", "package-lock.json"):
                    shutil.copyfile(seed / name, project / name)
            else:
                (project / "package.json").write_text(json.dumps({
                    "private": True, "dependencies": {"@openclaw/acpx": "2026.9.6"}}))
            result = subprocess.run(
                ["npm", "install", "--ignore-scripts", "--no-audit", "--no-fund",
                 "--omit=dev", "--omit=peer", "--legacy-peer-deps", "--prefer-online",
                 "--registry", f"http://127.0.0.1:{server.server_port}"],
                cwd=project, env=env, capture_output=True, text=True, timeout=60,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            return project

        reviewed = install("reviewed")
        digest = MODULE.dependency_lock_digest((reviewed / "package-lock.json").read_bytes())
        patch.object(MODULE, "REVIEWED_LOCK_SHA256", digest).start()
        paths = sorted(p.relative_to(reviewed).as_posix() for p in reviewed.rglob("*.d.ts"))
        patch.object(MODULE, "REVIEWED_METADATA_PATHS_SHA256",
                     hashlib.sha256(("\n".join(paths) + "\n").encode()).hexdigest()).start()
        published.append("1.0.1")
        unseeded = install("unseeded")
        package = "node_modules/cetirun-fixture-dependency/package.json"
        self.assertEqual(json.loads((unseeded / package).read_text())["version"], "1.0.1")
        with self.assertRaisesRegex(ValueError, "dependency lock"):
            MODULE.prune(unseeded.parent, "2026.9.6")
        self.assertTrue((unseeded / "node_modules/@openclaw/acpx/index.d.ts").exists())
        seeded = install("seeded", reviewed)
        self.assertEqual(json.loads((seeded / package).read_text())["version"], "1.0.0")
        self.assertEqual(MODULE.dependency_lock_digest((seeded / "package-lock.json").read_bytes()), digest)
        self.assertEqual(MODULE.prune(seeded.parent, "2026.9.6"), 2)

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
