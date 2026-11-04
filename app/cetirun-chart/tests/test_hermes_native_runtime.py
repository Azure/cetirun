# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Run with HERMES_QUALIFICATION_SOURCE and HERMES_QUALIFICATION_PYTHON set explicitly."""

import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

CHART = Path(__file__).resolve().parents[1]
SOURCE = os.environ.get("HERMES_QUALIFICATION_SOURCE", "")
PYTHON = os.environ.get("HERMES_QUALIFICATION_PYTHON", "")


@unittest.skipUnless(SOURCE and PYTHON, "requires the pinned Hermes source and its prepared PM interpreter")
class HermesNativeRuntimeTests(unittest.TestCase):
    def test_actual_loaders_password_provider_and_restarts(self):
        for case in ("ordinary", "mtls", "profiles", "missing-managed", "invalid-managed",
                     "missing-signing-key", "invalid-signing-key", "source-policy", "missing-provider", "missing-bundle"):
            with self.subTest(case=case):
                env = {name: value for name, value in os.environ.items()
                       if not name.startswith(("HERMES_", "API_SERVER_", "CETIRUN_"))}
                env.update(PYTHONPATH=SOURCE, HERMES_RUNTIME_DIR=str(Path(SOURCE) / "tools"),
                           PYTHONDONTWRITEBYTECODE="1", HERMES_DISABLE_LAZY_INSTALLS="1")
                result = subprocess.run([
                    PYTHON, str(CHART / "tests/fixtures/hermes_webapp_native.py"), SOURCE,
                    str(CHART / "agents/hermes/bootstrap"), case,
                ], env=env, capture_output=True, text=True, timeout=60)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("rejected" if case.startswith(("missing-", "invalid-")) else "passed", result.stdout)



@unittest.skipUnless(SOURCE and PYTHON and sys.platform == "linux", "requires pinned Hermes source and Linux PM Python")
class HermesNativeGatewayTests(unittest.TestCase):
    def run_fixture(self, name):
        with tempfile.TemporaryDirectory(prefix=f"hermes-{name}-") as directory:
            caller = Path(directory) / "caller"
            (caller / ".hermes").mkdir(parents=True)
            env = {key: os.environ[key] for key in ("PATH", "LANG", "LC_ALL", "TMPDIR") if key in os.environ}
            env.update(
                HOME=str(caller), HERMES_HOME=str(caller / ".hermes"),
                PYTHONPATH=SOURCE, HERMES_RUNTIME_DIR=str(Path(SOURCE) / "tools"),
                HERMES_QUALIFICATION_SOURCE=SOURCE, HERMES_QUALIFICATION_WORKDIR=directory,
                PYTHONDONTWRITEBYTECODE="1", HERMES_DISABLE_LAZY_INSTALLS="1", AWS_EC2_METADATA_DISABLED="true",
            )
            result = subprocess.run([
                PYTHON, str(CHART / f"tests/fixtures/hermes_{name}_native.py"),
            ], env=env, capture_output=True, text=True, timeout=240)
            self.assertEqual(result.returncode, 0, result.stderr)
            return json.loads(result.stdout.strip().splitlines()[-1])

    def test_storage_patch_rejects_source_drift_and_is_idempotent(self):
        spec = importlib.util.spec_from_file_location("webapp_storage_patch", CHART / "agents/hermes/compat/apply_webapp_storage.py")
        patch = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(patch)
        source = (Path(SOURCE) / "apps/desktop/src/entry.ts").read_text()
        prepared = patch.prepare_patch(source)
        self.assertIn(patch.AFTER, prepared)
        self.assertEqual(patch.prepare_patch(prepared), prepared)
        with self.assertRaisesRegex(ValueError, "Unsupported Hermes renderer entry"):
            patch.prepare_patch(source + "\n// unexpected source change\n")
        handoff = (Path(SOURCE) / "hermes_cli/web_routers/webapp.py").read_text()
        patched_handoff = patch.prepare_handoff_patch(handoff)
        self.assertIn(patch.HANDOFF_AFTER, patched_handoff)
        self.assertEqual(patch.prepare_handoff_patch(patched_handoff), patched_handoff)
        with self.assertRaisesRegex(ValueError, "Unsupported Hermes Webapp handoff source"):
            patch.prepare_handoff_patch(handoff + "\n# unexpected source change\n")

    def test_startup_patch_rejects_source_drift_and_is_idempotent(self):
        spec = importlib.util.spec_from_file_location("webapp_startup_patch", CHART / "agents/hermes/compat/apply_webapp_startup.py")
        patch = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(patch)
        source = (Path(SOURCE) / "gateway/run_runtime.py").read_text()
        prepared = patch.prepare_patch(source)
        self.assertIn(patch.AFTER, prepared)
        self.assertEqual(patch.prepare_patch(prepared), prepared)
        with self.assertRaisesRegex(ValueError, "Unsupported Hermes gateway source"):
            patch.prepare_patch(source + "\n# unexpected source change\n")

    def test_rotation_signout_and_listener_failure(self):
        self.assertEqual(self.run_fixture("auth"), {
            "restart_preserves_login": True,
            "expired_cookie_same_key_login": True,
            "key_rotation_revokes_auth": True,
            "signout_revokes_browsers_without_restart": True,
            "occupied_port_fails_without_fallback": True,
        })

    def test_real_gateway_channel_workspace_and_restart(self):
        self.assertEqual(self.run_fixture("workspace"), {
            "channel_session_fifo_delivery": True,
            "unbound_history_read_only": True,
            "files_upload_download": True,
            "git_status_diff": True,
            "preview_asset": True,
            "cron_run_cancel_profile": True,
            "terminal_reconnect_close_restart": True,
            "history_and_browser_credential_restart": True,
        })

    def test_mixed_plugin_lifecycle(self):
        self.assertEqual(self.run_fixture("plugins"), {
            "pinned_mixed_plugin_install": True,
            "desktop_half_served": True,
            "enabled_and_disabled_survive_restart": True,
            "remove_prunes_desktop_half": True,
        })


if __name__ == "__main__":
    unittest.main()
