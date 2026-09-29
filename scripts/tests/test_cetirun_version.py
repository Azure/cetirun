# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

import importlib.util
import json
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("cetirun_version", ROOT / "scripts/cetirun-version.py")
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)

ACCEPTED_VERSIONS = (
    "0.0.0",
    "1.2.30",
    "0.1.0-alpha",
    "0.1.0-alpha.1",
    "0.1.0-beta.2",
    "0.1.0-dev.20260927.gabcdef0",
    "0.1.0-rc.1",
    "0.1.0-preview.custom",
    "0.1.0-0",
    "0.1.0-alpha-beta.1",
    "0.1.0-01a",
    "1.2.3-" + "a" * 122,
)

REJECTED_VERSIONS = (
    "", "1", "1.2", "1.2.x", "v1.2.3", "dev-1.2.3",
    " 1.2.3", "01.2.3", "1.02.3", "1.2.03",
    "1.2.3custom", "1.2.3.4", "1.2.3-", "1.2.3-alpha..1",
    "1.2.3-.alpha", "1.2.3-alpha.", "1.2.3-01", "1.2.3-alpha.01",
    "1.2.3 ", "1.2.3\n", "1.2.3-\tdev", '1.2.3-"quoted"',
    "1.2.3-'quoted'", "1.2.3-α", "1.2.٣",
    "1.2.3+build.123", "1.2.3-dev.1+build.123",
    "1.2.3-" + "a" * 123,
)

VERSION_FILES = (
    "app/cetirun-chart/Chart.yaml",
    "app/cetirun-operator/chart/Chart.yaml",
    "app/cetirun-portal/chart/Chart.yaml",
    "app/cetirun-platform/Chart.yaml",
    "app/cetirun-platform/Chart.lock",
    "infra/istio/Chart.yaml",
    "app/cetirun-ai-gateway/chart/Chart.yaml",
    "app/cetirun-operator/chart/values.yaml",
    "app/cetirun-operator/cmd/main.go",
    "app/cetirun-operator/deploy/deploy.sh",
    "app/cetirun-portal/deploy/deploy.sh",
    "app/cetirun-portal/go.mod",
    "app/cetirun-portal/web/package.json",
    "app/cetirun-portal/web/package-lock.json",
    "app/cetirun-chart/agents/hermes/plugins/platforms/cetirun_teams/plugin.yaml",
    "app/cetirun-portal/internal/handler/teamsapp/manifest.json",
)


class CetiRunVersionTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.addCleanup(MODULE.PENDING_UPDATES.clear)
        self.root = Path(temporary.name)
        for relative in VERSION_FILES:
            destination = self.root / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / relative, destination)
        self.enterContext(patch.object(MODULE, "ROOT", self.root))
        self.istio = self.root / "infra/istio/Chart.yaml"
        self.version = MODULE.top_level_yaml(self.root / "app/cetirun-chart/Chart.yaml", "version")

    def test_accepts_release_and_prerelease_versions(self):
        for command in ("set", "check"):
            for version in ACCEPTED_VERSIONS:
                with self.subTest(command=command, version=version):
                    with patch.object(MODULE, f"{command}_version") as action, \
                            patch.object(sys, "argv", ["version", command, version]):
                        self.assertEqual(MODULE.main(), 0)
                    action.assert_called_once_with(version)

    def test_rejects_invalid_versions_before_updates(self):
        for command in ("set", "check"):
            for version in REJECTED_VERSIONS:
                with self.subTest(command=command, version=version):
                    with patch.object(MODULE, f"{command}_version") as action, \
                            patch.object(sys, "argv", ["version", command, version]):
                        with self.assertRaisesRegex(ValueError, "must be X.Y.Z"):
                            MODULE.main()
                    action.assert_not_called()

    def test_invalid_version_leaves_files_unchanged(self):
        before = {name: (self.root / name).read_bytes() for name in VERSION_FILES}
        for version in ("1.2.3custom", '1.2.3-"quoted"', "1.2.3+build.123"):
            with self.subTest(version=version):
                with patch.object(sys, "argv", ["version", "set", version]):
                    with self.assertRaises(ValueError):
                        MODULE.main()
                self.assertEqual(
                    before, {name: (self.root / name).read_bytes() for name in VERSION_FILES}
                )

    def test_preserves_suffix_in_version_files(self):
        for version in ACCEPTED_VERSIONS:
            with self.subTest(version=version):
                MODULE.set_version(version)
                for relative in VERSION_FILES:
                    if relative.endswith("Chart.lock"):
                        continue  # Updated by Helm, not the Python setter.
                    self.assertIn(version, (self.root / relative).read_text(), relative)
                self.assertEqual(
                    MODULE.top_level_yaml(self.root / VERSION_FILES[0], "version"), version
                )
                for relative in (
                    "app/cetirun-portal/web/package.json",
                    "app/cetirun-portal/web/package-lock.json",
                    "app/cetirun-portal/internal/handler/teamsapp/manifest.json",
                ):
                    self.assertEqual(json.loads((self.root / relative).read_text())["version"], version)

    @unittest.skipUnless(shutil.which("helm") and shutil.which("make"), "requires Helm and Make")
    def test_make_set_version(self):
        for relative in ("Makefile", "scripts/cetirun-version.py", "scripts/cetirun-release-metadata.sh"):
            destination = self.root / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(ROOT / relative, destination)
        for version in (
            "0.1.0-alpha.1", "0.1.0-beta.2", "0.1.0-dev.20260927.gabcdef0", "0.1.0",
        ):
            with self.subTest(version=version):
                for target in ("set-version", "version-check"):
                    command = ["make", target, f"PYTHON={sys.executable}"]
                    if target == "set-version":
                        command.append(f"VERSION={version}")
                    result = subprocess.run(
                        command, cwd=self.root, text=True, capture_output=True, timeout=30
                    )
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                MODULE.check_version(version)
                lint = subprocess.run(
                    ["helm", "lint", "infra/istio"], cwd=self.root,
                    text=True, capture_output=True, timeout=30
                )
                self.assertEqual(lint.returncode, 0, lint.stdout + lint.stderr)
                for component in ("operator", "portal"):
                    archive = self.root / f"app/cetirun-platform/charts/cetirun-{component}-{version}.tgz"
                    self.assertTrue(archive.is_file(), archive)

    def test_set_synchronizes_istio_app_version(self):
        MODULE.set_version("1.2.3")
        self.assertEqual(MODULE.top_level_yaml(self.istio, "version"), "1.2.3")
        self.assertEqual(MODULE.top_level_yaml(self.istio, "appVersion"), "1.2.3")

    def test_check_rejects_stale_istio_app_version(self):
        lines = self.istio.read_text().splitlines()
        self.istio.write_text("\n".join(
            'appVersion: "0.0.0"' if line.startswith("appVersion:") else line
            for line in lines
        ) + "\n")
        with self.assertRaisesRegex(ValueError, "infra/istio/Chart.yaml: appVersion"):
            MODULE.check_version(self.version)

    def test_missing_app_version_does_not_partially_update_files(self):
        self.istio.write_text("".join(
            line for line in self.istio.read_text().splitlines(keepends=True)
            if not line.startswith("appVersion:")
        ))
        before = {name: (self.root / name).read_bytes() for name in VERSION_FILES}
        with self.assertRaisesRegex(ValueError, "infra/istio/Chart.yaml"):
            MODULE.set_version("1.2.3")
        self.assertEqual(before, {name: (self.root / name).read_bytes() for name in VERSION_FILES})


if __name__ == "__main__":
    unittest.main()
