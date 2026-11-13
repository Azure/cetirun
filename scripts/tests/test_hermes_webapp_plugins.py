# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Managed Webapp plugin policy; native cases use the prepared MEMORY_HERMES_SOURCE."""

import dataclasses
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import types
import unittest
from unittest import mock


ROOT = Path(__file__).resolve().parents[2]
HELPER = ROOT / "app/cetirun-chart/agents/hermes/compat/apply_webapp_plugins.py"
spec = importlib.util.spec_from_file_location("webapp_plugins_patch", HELPER)
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)
NATIVE_SOURCE = os.environ.get("MEMORY_HERMES_SOURCE")
LOADER = "apps/desktop/src/contrib/runtime-loader.ts"
SURFACE = "hermes_cli/web_server_surface.py"


class InstallerTests(unittest.TestCase):
    def test_unknown_upstream_source_is_rejected(self):
        for relative in installer.PATCHES:
            with self.subTest(path=relative), self.assertRaisesRegex(ValueError, "Unsupported Hermes source"):
                installer.prepare_patch(relative, "unexpected source\n")

    def test_all_inputs_are_checked_before_any_write(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for relative in installer.PATCHES:
                path = root / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("original\n")
            with mock.patch.object(sys, "argv", [str(HELPER), "--root", str(root)]), \
                    mock.patch.object(installer, "prepare_patch", side_effect=["patched\n", ValueError("source changed")]), \
                    mock.patch("sys.stderr"), self.assertRaises(SystemExit) as failure:
                installer.main()
            self.assertEqual(failure.exception.code, 1)
            for relative in installer.PATCHES:
                self.assertEqual((root / relative).read_text(), "original\n")


@unittest.skipUnless(NATIVE_SOURCE, "set MEMORY_HERMES_SOURCE to the pinned, prepared Hermes source")
class NativeWebappPluginTests(unittest.TestCase):
    def setUp(self):
        self.source = Path(NATIVE_SOURCE)
        self.original = {
            relative: (self.source / relative).read_text(encoding="utf-8")
            for relative in installer.PATCHES
        }
        self.patched = {
            relative: installer.prepare_patch(relative, source)
            for relative, source in self.original.items()
        }

    def test_cli_check_apply_and_repeat_preserve_the_boundary(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for relative, source in self.original.items():
                path = root / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(source, encoding="utf-8")
            command = [sys.executable, "-B", str(HELPER), "--root", str(root)]
            subprocess.run([*command, "--check"], check=True, capture_output=True)
            for relative, source in self.original.items():
                self.assertEqual((root / relative).read_text(), source)
            for _ in range(2):
                subprocess.run(command, check=True, capture_output=True)
                for relative, source in self.patched.items():
                    self.assertEqual((root / relative).read_text(), source)
            path = root / SURFACE
            path.write_text(self.patched[SURFACE] + "\n# unexpected upstream edit\n")
            result = subprocess.run(command, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("Unsupported Hermes source", result.stderr)

    def test_native_loader_blocks_evaluation_scanning_and_watches_but_keeps_bundled_plugins(self):
        result = subprocess.run(
            ["node", "--experimental-vm-modules", str(ROOT / "scripts/tests/hermes_webapp_plugins.cjs")],
            input=json.dumps({
                "original": self.original[LOADER],
                "patched": self.patched[LOADER],
                "bundled": (self.source / "apps/desktop/src/contrib/plugins.ts").read_text(),
            }), text=True, capture_output=True, timeout=30,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_native_route_policy_refuses_plugins_without_changing_other_capabilities(self):
        class HTTPException(Exception):
            def __init__(self, *, status_code, detail):
                self.status_code = status_code
                super().__init__(detail)

        fastapi = types.ModuleType("fastapi")
        fastapi.HTTPException, fastapi.Request = HTTPException, object

        def load_policy(name, source):
            module = types.ModuleType(name)
            with mock.patch.dict(sys.modules, {"fastapi": fastapi, name: module}):
                exec(compile(source, SURFACE, "exec"), module.__dict__)
            return module

        original = load_policy("original_surface", self.original[SURFACE])
        patched = load_policy("patched_surface", self.patched[SURFACE])
        for surface, previous in original.POLICIES.items():
            expected = dataclasses.asdict(previous)
            expected["desktop_plugins"] = False
            self.assertEqual(dataclasses.asdict(patched.POLICIES[surface]), expected)
            for auth_required in (True, False):
                state = types.SimpleNamespace(ui_surface=surface, auth_required=auth_required)
                request = types.SimpleNamespace(app=types.SimpleNamespace(state=state))
                if surface == "webapp":
                    original.route_on("desktop_plugins")(request)
                with self.assertRaises(HTTPException) as failure:
                    patched.route_on("desktop_plugins")(request)
                self.assertEqual(failure.exception.status_code, 404)
                for field, enabled in expected.items():
                    if enabled:
                        patched.route_on(field)(request)
                self.assertEqual(patched.private_launch(state), original.private_launch(state))


if __name__ == "__main__":
    unittest.main()
