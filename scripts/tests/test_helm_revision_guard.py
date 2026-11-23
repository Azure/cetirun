# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Run the post-renderer against a synthetic Helm history command."""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


GUARD = Path(__file__).resolve().parents[1] / "helm_revision_guard.py"


class HelmRevisionGuardTests(unittest.TestCase):
    def run_guard(self, history, *, exit_code=0):
        with tempfile.TemporaryDirectory() as temporary:
            command = Path(temporary) / "helm"
            command.write_text(f"#!{sys.executable}\nimport os, sys\nprint(os.environ['CETIRUN_TEST_HISTORY'])\nsys.exit({exit_code})\n")
            command.chmod(0o700)
            return subprocess.run([sys.executable, "-B", str(GUARD), "--expected-revision", "7", "--release", "example",
                                   "--namespace", "example", "--context", "synthetic"],
                                  input="---\nkind: ConfigMap\nmetadata: {name: example}\n", text=True, capture_output=True,
                                  env={**os.environ, "PATH": temporary, "CETIRUN_TEST_HISTORY": json.dumps(history)}, timeout=10)

    def test_current_revision_passes_manifest_through_unchanged(self):
        result = self.run_guard([{"revision": 7, "status": "deployed"}])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "---\nkind: ConfigMap\nmetadata: {name: example}\n")

    def test_competing_or_missing_history_emits_no_manifest(self):
        for history in ([], {}, [{"revision": 8, "status": "deployed"}],
                        [{"revision": 7, "status": "pending-upgrade"}],
                        [{"revision": 7, "status": "deployed"}, {"revision": 8, "status": "failed"}],
                        [{"status": "deployed"}]):
            with self.subTest(history=history):
                result = self.run_guard(history)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, "")

    def test_unavailable_history_does_not_disclose_command_output(self):
        result = self.run_guard({"synthetic-private-diagnostic": "not-for-the-manifest"}, exit_code=1)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")
        self.assertNotIn("synthetic-private-diagnostic", result.stderr)


if __name__ == "__main__":
    unittest.main()
