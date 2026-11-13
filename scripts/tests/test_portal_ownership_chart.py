# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Offline checks for the internal Portal ownership transport."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[2]


@unittest.skipUnless(shutil.which("helm"), "requires Helm")
class PortalOwnershipChartTests(unittest.TestCase):
    def render(self, enabled=None, identity=None):
        istio = {"host": "portal.example.com", "gatewayNamespace": "test-ingress", "gatewayServiceAccount": "test-gateway"}
        if enabled is not None:
            istio["internalAuthMTLS"] = enabled
        return subprocess.run(
            ["helm", "template", "owner-test", str(ROOT / "app/cetirun-portal/chart"),
             "--namespace", "test-platform", "--values", "-"],
            input=json.dumps({
                "istio": istio,
                "features": {"agentIdentityEnabled": False, "agentToolsEnabled": False},
                "agentIdentity": identity if identity is not None else {
                    "tenantId": "11111111-1111-1111-1111-111111111111",
                    "portalClientId": "22222222-2222-2222-2222-222222222222",
                },
            }),
            text=True, capture_output=True, timeout=30,
            env={**os.environ, "KUBECONFIG": os.devnull, "HELM_REGISTRY_CONFIG": os.devnull},
        )

    def test_mtls_requires_gateway_principal_on_internal_port(self):
        result = self.render()
        self.assertEqual(result.returncode, 0, result.stderr)
        objects = [x for x in yaml.safe_load_all(result.stdout) if x]
        peer = next(x for x in objects if x["kind"] == "PeerAuthentication")["spec"]
        self.assertEqual(peer["mtls"], {"mode": "STRICT"})
        self.assertNotIn("portLevelMtls", peer)
        policy = next(x for x in objects if x["kind"] == "AuthorizationPolicy")["spec"]
        self.assertEqual(policy["rules"], [{"from": [{"source": {
            "principals": ["cluster.local/ns/test-ingress/sa/test-gateway"],
        }}]}])
        self.assertTrue(any(x["kind"] == "NetworkPolicy" for x in objects))

    def test_explicit_owner_verification_does_not_depend_on_agent_features_or_mtls(self):
        for mtls in (None, True, False):
            with self.subTest(mtls=mtls):
                result = self.render(mtls)
                self.assertEqual(result.returncode, 0, result.stderr)
                objects = [x for x in yaml.safe_load_all(result.stdout) if x]
                deployment = next(x for x in objects if x["kind"] == "Deployment")
                env = {item["name"]: item.get("value") for item in
                       deployment["spec"]["template"]["spec"]["containers"][0]["env"]}
                self.assertEqual(env["FEATURE_AGENT_IDENTITY_ENABLED"], "false")
                self.assertEqual(env["FEATURE_AGENT_TOOLS_ENABLED"], "false")
                self.assertEqual(env["ENTRA_TENANT_ID"], "11111111-1111-1111-1111-111111111111")
                self.assertEqual(env["PORTAL_OIDC_CLIENT_ID"], "22222222-2222-2222-2222-222222222222")

    def test_rejects_partial_owner_verification_identity(self):
        for mtls in (None, True, False):
            for field in ("tenantId", "portalClientId"):
                with self.subTest(mtls=mtls, field=field):
                    identity = {"tenantId": "test-tenant", "portalClientId": "test-client"}
                    identity[field] = ""
                    result = self.render(mtls, identity)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn(f"agentIdentity.{field} is required", result.stderr)

    def test_legacy_mode_preserves_alias_only_port(self):
        result = self.render(False, {})
        self.assertEqual(result.returncode, 0, result.stderr)
        objects = [x for x in yaml.safe_load_all(result.stdout) if x]
        peer = next(x for x in objects if x["kind"] == "PeerAuthentication")["spec"]
        self.assertEqual(peer["portLevelMtls"], {8081: {"mode": "DISABLE"}})
        policy = next(x for x in objects if x["kind"] == "AuthorizationPolicy")["spec"]
        self.assertIn({"to": [{"operation": {"ports": ["8081"]}}]}, policy["rules"])
        deployment = next(x for x in objects if x["kind"] == "Deployment")
        env = {item["name"] for item in deployment["spec"]["template"]["spec"]["containers"][0]["env"]}
        self.assertNotIn("ENTRA_TENANT_ID", env)
        self.assertNotIn("PORTAL_OIDC_CLIENT_ID", env)

    def test_mtls_setting_rejects_non_boolean(self):
        result = self.render("false")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("istio.internalAuthMTLS must be a boolean", result.stderr)
