# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

import importlib.util
import shutil
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("cetirun_version", ROOT / "scripts/cetirun-version.py")
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)

VERSION_FILES = (
    "app/cetirun-chart/Chart.yaml",
    "app/cetirun-operator/chart/Chart.yaml",
    "app/cetirun-portal/chart/Chart.yaml",
    "app/cetirun-platform/Chart.yaml",
    "app/cetirun-platform/Chart.lock",
    "infra/istio/Chart.yaml",
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
        self.root = Path(temporary.name)
        for relative in VERSION_FILES:
            destination = self.root / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / relative, destination)
        self.enterContext(patch.object(MODULE, "ROOT", self.root))
        self.istio = self.root / "infra/istio/Chart.yaml"
        self.version = MODULE.top_level_yaml(self.root / "app/cetirun-chart/Chart.yaml", "version")

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
