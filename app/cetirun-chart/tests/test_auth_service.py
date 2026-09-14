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


if __name__ == "__main__":
    unittest.main()
