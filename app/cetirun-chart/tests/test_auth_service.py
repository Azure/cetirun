# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

import importlib.util
import os
import sys
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

from aiohttp.test_utils import TestClient, TestServer

SERVICE_PATH = Path(__file__).resolve().parents[1] / "configs/cetirun-auth-service.py"


class AuthServiceCompatibilityTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        state = tempfile.TemporaryDirectory()
        self.addCleanup(state.cleanup)
        self.state_dir = Path(state.name)
        environment = mock.patch.dict(
            os.environ,
            {
                "CETIRUN_AGENT_TYPE": "openclaw",
                "CETIRUN_AUTH_KEY": "test-auth-key",
                "OPENCLAW_STATE_DIR": state.name,
            },
            clear=True,
        )
        environment.start()
        self.addCleanup(environment.stop)
        spec = importlib.util.spec_from_file_location("auth_service", SERVICE_PATH)
        service = importlib.util.module_from_spec(spec)
        sys.modules[spec.name] = service
        self.addCleanup(sys.modules.pop, spec.name, None)
        spec.loader.exec_module(service)
        self.service = service
        service.PROFILE = SimpleNamespace(
            name="openclaw",
            read_provider_and_model=lambda: {"provider": "copilot", "model": "test"},
            is_authenticated=lambda: False,
        )
        self.client = TestClient(TestServer(service.build_app()))
        await self.client.start_server()
        self.addAsyncCleanup(self.client.close)

    async def test_health_endpoint_stays_public(self):
        response = await self.client.get("/health")
        self.assertEqual(response.status, 200)
        self.assertEqual(await response.json(), {"ok": True, "agent": "openclaw"})

    async def test_api_rejects_missing_and_wrong_keys(self):
        for headers in ({}, {"Authorization": "Bearer wrong-key"}):
            with self.subTest(headers=headers):
                response = await self.client.get("/v1/status", headers=headers)
                self.assertEqual(response.status, 401)

    async def test_api_rejects_unconfigured_key(self):
        os.environ.pop("CETIRUN_AUTH_KEY")
        response = await self.client.get(
            "/v1/status", headers={"Authorization": "Bearer test-auth-key"}
        )
        self.assertEqual(response.status, 503)

    async def test_api_accepts_valid_key(self):
        response = await self.client.get(
            "/v1/status", headers={"Authorization": "Bearer test-auth-key"}
        )
        self.assertEqual(response.status, 200)
        payload = await response.json()
        self.assertFalse(payload["authenticated"])
        self.assertEqual(payload["provider"], "copilot")
        self.assertEqual(payload["model"], "test")

    def test_openclaw_token_save_uses_cli_stdin_and_updates_dotenv(self):
        token = "synthetic-copilot-token"
        env_file = self.state_dir / ".env"
        env_file.write_text(
            "OPENCLAW_GITHUB_TOKEN=legacy-token\nCUSTOM_FIELD=preserved\n"
        )
        completed = SimpleNamespace(returncode=0, stdout="saved", stderr="")
        with mock.patch.object(
            self.service.subprocess, "run", return_value=completed
        ) as runner:
            self.service._openclaw_save_token(token)

        command = runner.call_args.args[0]
        self.assertNotIn(token, command)
        self.assertEqual(
            command,
            [
                "openclaw",
                "models",
                "auth",
                "paste-token",
                "--provider",
                "github-copilot",
                "--profile-id",
                "github-copilot:github",
                "--agent",
                "main",
            ],
        )
        self.assertEqual(runner.call_args.kwargs["input"], token + "\n")
        self.assertTrue(runner.call_args.kwargs["capture_output"])
        self.assertEqual(runner.call_args.kwargs["timeout"], 30)
        entries = self.service._read_dotenv(env_file)
        self.assertEqual(entries["COPILOT_GITHUB_TOKEN"], token)
        self.assertEqual(entries["CUSTOM_FIELD"], "preserved")
        self.assertNotIn("OPENCLAW_GITHUB_TOKEN", entries)

    def test_openclaw_token_save_failure_does_not_replace_dotenv(self):
        env_file = self.state_dir / ".env"
        env_file.write_text("COPILOT_GITHUB_TOKEN=existing-token\n")
        completed = SimpleNamespace(returncode=1, stdout="", stderr="failed")
        with mock.patch.object(
            self.service.subprocess, "run", return_value=completed
        ):
            with self.assertRaisesRegex(
                RuntimeError, "OpenClaw credential command failed"
            ):
                self.service._openclaw_save_token("replacement-token")
        self.assertEqual(
            self.service._read_dotenv(env_file)["COPILOT_GITHUB_TOKEN"],
            "existing-token",
        )

    def test_openclaw_auth_status_uses_redacted_cli_inventory(self):
        (self.state_dir / ".env").write_text(
            "COPILOT_GITHUB_TOKEN=synthetic-copilot-token\n"
        )
        completed = SimpleNamespace(
            returncode=0,
            stdout=(
                '{"profiles":[{"id":"github-copilot:github",'
                '"provider":"github-copilot","type":"token"}]}'
            ),
            stderr="",
        )
        with mock.patch.object(
            self.service.subprocess, "run", return_value=completed
        ) as runner:
            self.assertTrue(self.service._openclaw_is_authenticated())
        self.assertNotIn("synthetic-copilot-token", runner.call_args.args[0])
        self.assertEqual(runner.call_args.kwargs.get("input"), None)


if __name__ == "__main__":
    unittest.main()
