# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Offline checks for the optional Portal Chat installation contract."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[2]
OFFLINE = {**os.environ, "KUBECONFIG": os.devnull, "HELM_REGISTRY_CONFIG": os.devnull, "HELM_REPOSITORY_CONFIG": os.devnull}


@unittest.skipUnless(shutil.which("helm"), "requires Helm")
class PortalChatChartTests(unittest.TestCase):
    def render(self, chart, values, *, success=True):
        result = subprocess.run(
            ["helm", "template", "chat-test", str(chart), "--namespace", "test-platform", "--kube-version", "1.37.0", "--values", "-"],
            input=json.dumps(values), text=True, capture_output=True, env=OFFLINE, timeout=30, check=False,
        )
        if not success:
            self.assertNotEqual(result.returncode, 0)
            return result.stderr
        self.assertEqual(result.returncode, 0, result.stderr)
        return [value for value in yaml.safe_load_all(result.stdout) if value]

    @staticmethod
    def deployment_env(objects, name):
        deployment = next(value for value in objects if value["kind"] == "Deployment" and value["metadata"]["name"] == name)
        return {item["name"]: item.get("value", item.get("valueFrom")) for item in deployment["spec"]["template"]["spec"]["containers"][0]["env"]}

    def test_portal_defaults_do_not_enable_chat_or_add_storage(self):
        objects = self.render(ROOT / "app/cetirun-portal/chart", {"istio": {"host": "portal.example.com"}})
        env = self.deployment_env(objects, "cetirun-portal")
        self.assertEqual(env["FEATURE_CUSTOM_CHAT_ENABLED"], "false")
        self.assertFalse(any(value["kind"] in {"PersistentVolumeClaim", "StatefulSet"} for value in objects))
        roles = [value for value in objects if value["kind"] == "ClusterRole"]
        self.assertEqual(len(roles), 1)
        self.assertFalse(any("networkpolicies" in rule["resources"] for role in roles for rule in role["rules"]))

    def test_enabled_portal_has_scoped_reader_and_no_retry_route(self):
        objects = self.render(ROOT / "app/cetirun-portal/chart", {"istio": {"host": "portal.example.com"}, "chat": {"enabled": True, "identityRealm": "synthetic"}})
        env = self.deployment_env(objects, "cetirun-portal")
        self.assertEqual(env["FEATURE_CUSTOM_CHAT_ENABLED"], "true")
        self.assertEqual(json.loads(env["CHAT_CALLER_LABELS"]), {"app.kubernetes.io/name": "cetirun-portal", "app.kubernetes.io/instance": "chat-test"})
        route = next(value for value in objects if value["kind"] == "VirtualService")["spec"]["http"][0]
        self.assertEqual(route["retries"]["attempts"], 0)
        self.assertEqual(route["timeout"], "610s")
        role = next(value for value in objects if value["kind"] == "ClusterRole" and value["metadata"]["name"] == "cetirun-portal-chat")
        self.assertEqual(role["rules"], [
            {"apiGroups": [""], "resources": ["services"], "verbs": ["get"]},
            {"apiGroups": ["networking.k8s.io"], "resources": ["networkpolicies"], "verbs": ["get"]},
            {"apiGroups": ["networking.istio.io"], "resources": ["virtualservices"], "verbs": ["get"]},
        ])
        binding = next(value for value in objects if value["kind"] == "ClusterRoleBinding" and value["metadata"]["name"] == "cetirun-portal-chat")
        self.assertEqual(binding["roleRef"]["name"], role["metadata"]["name"])
        self.assertEqual(binding["subjects"], [{"kind": "ServiceAccount", "name": "cetirun-portal", "namespace": "test-platform"}])

    def test_enabled_portal_requires_identity_realm(self):
        error = self.render(ROOT / "app/cetirun-portal/chart", {"istio": {"host": "portal.example.com"}, "chat": {"enabled": True}}, success=False)
        self.assertIn("identityRealm", error)

    def test_umbrella_generates_the_same_caller_as_the_portal(self):
        with tempfile.TemporaryDirectory(prefix="cetirun-chat-charts-") as scratch:
            base = Path(scratch)
            for relative in ("cetirun-platform", "cetirun-operator/chart", "cetirun-portal/chart", "cetirun-ai-gateway/chart"):
                shutil.copytree(ROOT / "app" / relative, base / relative, ignore=shutil.ignore_patterns("charts", ".bin", "node_modules"))
            subprocess.run(["helm", "dependency", "build", str(base / "cetirun-platform"), "--skip-refresh"], env=OFFLINE, capture_output=True, text=True, check=True, timeout=30)
            objects = self.render(base / "cetirun-platform", {
                "istio": {"enabled": True},
                "global": {"portalChat": {"enabled": True, "identityRealm": "synthetic"}},
                "cetirun-operator": {
                    "domainSuffix": "agents.example.com",
                    "namespace": "test-platform",
                    "sandboxAdmission": {
                        "tlsSecretName": "synthetic-admission-tls",
                        "caBundle": "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tClEwRT0KLS0tLS1FTkQgQ0VSVElGSUNBVEUtLS0tLQo=",
                        "adminUsers": ["lint-admin@example.invalid"],
                    },
                },
                "cetirun-portal": {
                    "istio": {"host": "portal.example.com"},
                    "aiGateway": {"catalogNamespace": "test-platform"},
                    "agentIdentity": {
                        "tenantId": "11111111-1111-1111-1111-111111111111",
                        "portalClientId": "33333333-3333-3333-3333-333333333333",
                    },
                    "administration": {"platformAdminSubjects": [{
                        "tenantID": "11111111-1111-1111-1111-111111111111",
                        "objectID": "22222222-2222-2222-2222-222222222222",
                    }]},
                },
            })
            portal = self.deployment_env(objects, "cetirun-portal")
            operator = self.deployment_env(objects, "cetirun-operator")
            access = json.loads(operator["CETIRUN_CHAT_ACCESS"])
            self.assertTrue(access["enabled"])
            self.assertEqual(access["callers"], [{"namespace": "test-platform", "podLabels": json.loads(portal["CHAT_CALLER_LABELS"])}])


if __name__ == "__main__":
    unittest.main()
