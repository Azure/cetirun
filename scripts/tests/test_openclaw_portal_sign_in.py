# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Render the agent chart with OpenClaw's Portal sign-in (trusted-proxy mode)."""

import json
import shutil
import unittest

from test_native_gateway_chart import RUNTIME_CHART, native_values, object_of_kind, render


def legacy_values(**extra):
    values = {
        "agentType": "openclaw",
        "aiGateway": {
            "enabled": True,
            "keySecretName": "example-model-key",
            "models": [{"id": "example-model", "name": "Example model"}],
        },
        "secrets": {
            "openclawGatewayToken": "example-token",
            "provider": "openai",
            "model": "example-model",
            "apiBase": "http://gateway.example-platform.svc.cluster.local:4000/v1",
        },
    }
    for key, value in extra.items():
        values[key] = {**values.get(key, {}), **value}
    return values


def rendered(values):
    objects = render(RUNTIME_CHART, values)
    config = json.loads(object_of_kind(objects, "ConfigMap")["data"]["openclaw.json"])
    container = next(c for c in object_of_kind(objects, "StatefulSet")["spec"]["template"]["spec"]["containers"]
                     if c["name"] == "cetirun")
    names = {env["name"]: env for env in container.get("env", [])}
    return config, names


@unittest.skipUnless(shutil.which("helm"), "requires Helm 3 or 4")
class OpenClawPortalSignInTests(unittest.TestCase):
    def test_off_by_default_keeps_the_gateway_token(self):
        config, env = rendered(legacy_values())
        self.assertNotIn("auth", config["gateway"])
        self.assertIn("OPENCLAW_GATEWAY_TOKEN", env)
        self.assertNotIn("OPENCLAW_GATEWAY_PASSWORD", env)

    def test_on_trusts_the_sidecar_for_the_owner_and_turns_the_token_into_a_password(self):
        config, env = rendered(legacy_values(openclaw={"portalSignIn": {"enabled": True}}))
        self.assertEqual(config["gateway"]["auth"], {
            "mode": "trusted-proxy",
            "trustedProxy": {"userHeader": "x-auth-request-email", "allowLoopback": True,
                             "deviceAutoApprove": {"enabled": True}},
        })
        self.assertIn("127.0.0.6", config["gateway"]["trustedProxies"])
        # OpenClaw refuses to start in trusted-proxy mode with a token set.
        self.assertNotIn("OPENCLAW_GATEWAY_TOKEN", env)
        self.assertEqual(env["OPENCLAW_GATEWAY_PASSWORD"]["valueFrom"]["secretKeyRef"]["key"], "OPENCLAW_GATEWAY_TOKEN")

    def test_tailscale_keeps_the_token(self):
        values = legacy_values(openclaw={"portalSignIn": {"enabled": True}})
        values["secrets"]["tailscaleAuthKey"] = "tskey-example"
        config, env = rendered(values)
        self.assertNotIn("auth", config["gateway"])
        self.assertIn("OPENCLAW_GATEWAY_TOKEN", env)

    def test_a_native_sandbox_workload_keeps_the_token(self):
        values = native_values()
        values["openclaw"]["portalSignIn"] = {"enabled": True}
        config, env = rendered(values)
        self.assertNotEqual(config["gateway"].get("auth", {}).get("mode"), "trusted-proxy")
        self.assertIn("OPENCLAW_GATEWAY_TOKEN", env)

    def test_without_the_sidecar_address_the_chart_refuses(self):
        values = legacy_values(openclaw={"portalSignIn": {"enabled": True},
                                         "config": {"gateway": {"trustedProxies": ["192.0.2.1"]}}})
        render(RUNTIME_CHART, values, error="127.0.0.6")


if __name__ == "__main__":
    unittest.main()
