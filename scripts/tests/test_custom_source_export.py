# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]


class CustomSourceExportTests(unittest.TestCase):
    def test_git_visible_source_contains_canonical_context_and_runs(self):
        if not (ROOT / ".git").exists():
            self.skipTest("Git visibility is checked in the source checkout, not inside an exported source archive")
        inventory = subprocess.run(
            ["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z"],
            cwd=ROOT, capture_output=True, check=True,
        ).stdout.decode().split("\0")
        self.assertIn("examples/custom-agent/context/AGENTS.md", inventory)
        with tempfile.TemporaryDirectory(prefix="cetirun-custom-export-") as directory:
            export = Path(directory)
            for name in inventory:
                if name.startswith(("examples/custom-agent/", "scripts/tests/test_custom")) or name in (
                    "app/cetirun-chart/configs/init-home-common.sh",
                    "app/cetirun-chart/agents/custom/bootstrap/init-home.sh",
                    "scripts/tests/test_home_initialization.py",
                ):
                    destination = export / name
                    destination.parent.mkdir(parents=True, exist_ok=True)
                    shutil.copy2(ROOT / name, destination)
            result = subprocess.run(
                [sys.executable, "-B", "examples/custom-agent/qualify.py"],
                cwd=export, text=True, capture_output=True, timeout=30,
            )
            self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == "__main__":
    unittest.main()
