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
    def render(self, enabled):
        return subprocess.run(
            ["helm", "template", "owner-test", str(ROOT / "app/cetirun-portal/chart"),
             "--namespace", "test-platform", "--values", "-"],
            input=json.dumps({"istio": {"host": "portal.example.com", "internalAuthMTLS": enabled,
                                        "gatewayNamespace": "test-ingress", "gatewayServiceAccount": "test-gateway"}}),
            text=True, capture_output=True, timeout=30,
            env={**os.environ, "KUBECONFIG": os.devnull, "HELM_REGISTRY_CONFIG": os.devnull},
        )

    def test_mtls_requires_gateway_principal_on_internal_port(self):
        result = self.render(True)
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

    def test_legacy_mode_preserves_alias_only_port(self):
        result = self.render(False)
        self.assertEqual(result.returncode, 0, result.stderr)
        objects = [x for x in yaml.safe_load_all(result.stdout) if x]
        peer = next(x for x in objects if x["kind"] == "PeerAuthentication")["spec"]
        self.assertEqual(peer["portLevelMtls"], {8081: {"mode": "DISABLE"}})
        policy = next(x for x in objects if x["kind"] == "AuthorizationPolicy")["spec"]
        self.assertIn({"to": [{"operation": {"ports": ["8081"]}}]}, policy["rules"])

    def test_mtls_setting_rejects_non_boolean(self):
        result = self.render("false")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("istio.internalAuthMTLS must be a boolean", result.stderr)
