# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Tests for the AI Gateway image entrypoint's Copilot proxy mode."""

import importlib.machinery
import importlib.util
import os
import pathlib
import tempfile
import threading
import time
import unittest
from unittest import mock

ENTRYPOINT = pathlib.Path(__file__).resolve().parents[2] / "app/cetirun-ai-gateway/entrypoint.py"


def load_entrypoint():
    loader = importlib.machinery.SourceFileLoader("gateway_entrypoint", str(ENTRYPOINT))
    spec = importlib.util.spec_from_loader("gateway_entrypoint", loader)
    module = importlib.util.module_from_spec(spec)
    loader.exec_module(module)
    return module


class CopilotProxyModeTest(unittest.TestCase):
    def setUp(self):
        self.entrypoint = load_entrypoint()

    def test_environment_disables_device_sign_in(self):
        env = self.entrypoint.copilot_proxy_environment("/run/token", {"PATH": "/bin"})
        self.assertEqual(env["GITHUB_COPILOT_ACCESS_TOKEN_FILE"], "/run/token")
        self.assertEqual(env["GITHUB_COPILOT_DEVICE_CODE_URL"], self.entrypoint.DISABLED_URL)
        self.assertEqual(env["GITHUB_COPILOT_ACCESS_TOKEN_URL"], self.entrypoint.DISABLED_URL)
        self.assertEqual(env["GITHUB_COPILOT_TOKEN_DIR"], "/var/cache/copilot")
        self.assertEqual(env["LITELLM_LOCAL_MODEL_COST_MAP"], "True")
        self.assertEqual(env["PATH"], "/bin")

    def test_environment_keeps_an_explicit_token_directory(self):
        env = self.entrypoint.copilot_proxy_environment("/run/token", {"GITHUB_COPILOT_TOKEN_DIR": "/cache"})
        self.assertEqual(env["GITHUB_COPILOT_TOKEN_DIR"], "/cache")

    def test_token_present_needs_a_non_empty_file(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "access-token"
            self.assertFalse(self.entrypoint.token_present(path))
            path.write_text("  \n", encoding="utf-8")
            self.assertFalse(self.entrypoint.token_present(path))
            path.write_text("gho_example\n", encoding="utf-8")
            self.assertTrue(self.entrypoint.token_present(path))

    def test_proxy_waits_for_the_token_then_starts_litellm(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "access-token"
            started = []

            def fake_exec(program, argv, env):
                started.append((program, argv, env["GITHUB_COPILOT_ACCESS_TOKEN_FILE"]))
                raise SystemExit(0)

            def sign_in():
                time.sleep(0.3)
                path.write_text("gho_example", encoding="utf-8")

            environ = {"COPILOT_TOKEN_FILE": str(path), "COPILOT_TOKEN_POLL_SECONDS": "0.05"}
            with mock.patch.dict(os.environ, environ), mock.patch.object(self.entrypoint.os, "execvpe", fake_exec), \
                    mock.patch.object(self.entrypoint.signal, "signal"):
                thread = threading.Thread(target=sign_in)
                thread.start()
                with self.assertRaises(SystemExit):
                    self.entrypoint.main_with(["copilot-proxy", "--config", "/etc/litellm/config.yaml", "--port", "4000"])
                thread.join()
            self.assertEqual(started, [("litellm", ["litellm", "--config", "/etc/litellm/config.yaml", "--port", "4000"], str(path))])


if __name__ == "__main__":
    unittest.main()
