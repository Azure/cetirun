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
    def run_guard(self, history, *, exit_code=0, tls_metadata=None):
        with tempfile.TemporaryDirectory() as temporary:
            command = Path(temporary) / "helm"
            command.write_text(f"#!{sys.executable}\nimport os, sys\nprint(os.environ['CETIRUN_TEST_HISTORY'])\nsys.exit({exit_code})\n")
            command.chmod(0o700)
            tls_args = []
            if tls_metadata is not None:
                kubectl = Path(temporary) / "kubectl"
                kubectl.write_text(f"#!{sys.executable}\nimport os\nprint(os.environ['CETIRUN_TEST_TLS_METADATA'])\n")
                kubectl.chmod(0o700)
                tls_args = ["--tls-secret-namespace", "ingress", "--tls-secret-name", "cetirun-tls",
                            "--tls-secret-uid", "tls-uid", "--tls-secret-resource-version", "11"]
            return subprocess.run([sys.executable, "-B", str(GUARD), "--expected-revision", "7", "--release", "example",
                                   "--namespace", "example", "--context", "synthetic", *tls_args],
                                  input="---\nkind: ConfigMap\nmetadata: {name: example}\n", text=True, capture_output=True,
                                  env={**os.environ, "PATH": temporary, "CETIRUN_TEST_HISTORY": json.dumps(history),
                                       "CETIRUN_TEST_TLS_METADATA": json.dumps(tls_metadata)}, timeout=10)

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

    def test_retained_tls_rotation_after_data_read_blocks_manifest_release(self):
        original = {"uid": "tls-uid", "resourceVersion": "11"}
        for current in (original, {**original, "resourceVersion": "12"}, {**original, "uid": "replacement"},
                        {**original, "deletionTimestamp": "synthetic"}, {}, "private-error-marker"):
            with self.subTest(current=current):
                result = self.run_guard([{"revision": 7, "status": "deployed"}], tls_metadata=current)
                self.assertEqual(result.returncode == 0, current == original, result.stderr)
                if current == original:
                    self.assertIn("kind: ConfigMap", result.stdout)
                else:
                    self.assertEqual(result.stdout, "")
                    self.assertNotIn("private-error-marker", result.stderr)

    def test_unavailable_history_does_not_disclose_command_output(self):
        result = self.run_guard({"synthetic-private-diagnostic": "not-for-the-manifest"}, exit_code=1)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")
        self.assertNotIn("synthetic-private-diagnostic", result.stderr)


if __name__ == "__main__":
    unittest.main()
