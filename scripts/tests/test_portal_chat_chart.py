# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Offline checks for the optional Portal Chat installation contract."""

import base64
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[2]
OFFLINE = {**os.environ, "KUBECONFIG": os.devnull, "HELM_REGISTRY_CONFIG": os.devnull, "HELM_REPOSITORY_CONFIG": os.devnull}


@unittest.skipUnless(shutil.which("helm"), "requires Helm")
class PortalChatChartTests(unittest.TestCase):
    def render(self, chart, values, *, success=True, namespace="test-platform", release="chat-test"):
        portal = values if chart.name == "chart" else values.setdefault("cetirun-portal", {})
        portal.setdefault("agentIdentity", {
            "tenantId": "11111111-1111-1111-1111-111111111111",
            "portalClientId": "22222222-2222-2222-2222-222222222222",
        })
        result = subprocess.run(
            ["helm", "template", release, str(chart), "--namespace", namespace, "--kube-version", "1.37.0", "--values", "-"],
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

    def render_platform(self, values):
        with tempfile.TemporaryDirectory(prefix="cetirun-chat-charts-") as scratch:
            base = Path(scratch)
            for relative in ("cetirun-platform", "cetirun-operator/chart", "cetirun-portal/chart", "cetirun-ai-gateway/chart", "cetirun-connections-mcp/chart", "cetirun-agent-broker/chart", "cetirun-observability/chart"):
                shutil.copytree(ROOT / "app" / relative, base / relative, ignore=shutil.ignore_patterns("charts", ".bin", "node_modules"))
            subprocess.run(["helm", "dependency", "build", str(base / "cetirun-platform"), "--skip-refresh"], env=OFFLINE, capture_output=True, text=True, check=True, timeout=30)
            return self.render(base / "cetirun-platform", values)

    def test_portal_defaults_do_not_enable_chat_or_add_storage(self):
        objects = self.render(ROOT / "app/cetirun-portal/chart", {"istio": {"host": "portal.example.com"}})
        env = self.deployment_env(objects, "cetirun-portal")
        self.assertEqual(env["FEATURE_CUSTOM_CHAT_ENABLED"], "false")
        self.assertFalse(any(value["kind"] in {"PersistentVolumeClaim", "StatefulSet"} for value in objects))
        roles = [value for value in objects if value["kind"] == "ClusterRole"]
        self.assertEqual(len(roles), 1)
        self.assertFalse(any("networkpolicies" in rule["resources"] for role in roles for rule in role["rules"]))
        self.assertFalse(any(value["kind"] == "Role" and value["metadata"]["name"].endswith("-chat-mtls") for value in objects))

    def test_suspension_reader_is_scoped_without_chat(self):
        objects = self.render(ROOT / "app/cetirun-portal/chart", {
            "istio": {"host": "portal.example.com"},
            "chat": {"sandboxStateNamespace": "operator-state"},
        })
        role = next(value for value in objects if value["kind"] == "Role" and value["metadata"]["name"] == "cetirun-portal-suspension-state")
        self.assertEqual(role["metadata"]["namespace"], "operator-state")
        self.assertEqual(role["rules"], [{"apiGroups": [""], "resources": ["configmaps"], "verbs": ["get"]}])
        binding = next(value for value in objects if value["kind"] == "RoleBinding" and value["metadata"]["name"] == role["metadata"]["name"])
        self.assertEqual(binding["metadata"]["namespace"], "operator-state")
        self.assertEqual(binding["roleRef"], {"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": role["metadata"]["name"]})
        self.assertEqual(binding["subjects"], [{"kind": "ServiceAccount", "name": "cetirun-portal", "namespace": "test-platform"}])

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
            {"apiGroups": ["cilium.io"], "resources": ["ciliumnetworkpolicies"], "verbs": ["get"]},
            {"apiGroups": ["networking.istio.io"], "resources": ["virtualservices"], "verbs": ["get"]},
        ])
        binding = next(value for value in objects if value["kind"] == "ClusterRoleBinding" and value["metadata"]["name"] == "cetirun-portal-chat")
        self.assertEqual(binding["roleRef"]["name"], role["metadata"]["name"])
        self.assertEqual(binding["subjects"], [{"kind": "ServiceAccount", "name": "cetirun-portal", "namespace": "test-platform"}])
        state_role = next(value for value in objects if value["kind"] == "Role" and value["metadata"]["name"] == "cetirun-portal-chat-sandbox-state")
        self.assertEqual(state_role["metadata"]["namespace"], "cetirun-system")
        self.assertEqual(state_role["rules"], [{"apiGroups": [""], "resources": ["configmaps"], "verbs": ["get"]}])
        state_binding = next(value for value in objects if value["kind"] == "RoleBinding" and value["metadata"]["name"] == state_role["metadata"]["name"])
        self.assertEqual(state_binding["metadata"]["namespace"], "cetirun-system")
        self.assertEqual(state_binding["subjects"], binding["subjects"])
        self.assertEqual(env["SANDBOX_STATE_NAMESPACE"], "cetirun-system")
        tls_role = next(value for value in objects if value["kind"] == "Role" and value["metadata"]["name"] == "cetirun-portal-chat-mtls")
        self.assertEqual(tls_role["metadata"]["namespace"], "test-platform")
        self.assertEqual(tls_role["rules"], [{"apiGroups": ["networking.istio.io"], "resources": ["destinationrules"], "verbs": ["get"]}])
        tls_binding = next(value for value in objects if value["kind"] == "RoleBinding" and value["metadata"]["name"] == tls_role["metadata"]["name"])
        self.assertEqual(tls_binding["metadata"]["namespace"], "test-platform")
        self.assertEqual(tls_binding["roleRef"], {"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": tls_role["metadata"]["name"]})
        self.assertEqual(tls_binding["subjects"], binding["subjects"])

    def test_explicit_state_namespace_is_bound_in_rbac_and_environment(self):
        objects = self.render(ROOT / "app/cetirun-portal/chart", {
            "istio": {"host": "portal.example.com"},
            "chat": {"enabled": True, "identityRealm": "synthetic", "sandboxStateNamespace": "operator-state"},
        })
        env = self.deployment_env(objects, "cetirun-portal")
        self.assertEqual(env["SANDBOX_STATE_NAMESPACE"], "operator-state")
        readers = [value for value in objects if value["kind"] in {"Role", "RoleBinding"} and value["metadata"]["name"] == "cetirun-portal-chat-sandbox-state"]
        self.assertEqual(len(readers), 2)
        self.assertTrue(all(value["metadata"]["namespace"] == "operator-state" for value in readers))
        tls_readers = [value for value in objects if value["kind"] in {"Role", "RoleBinding"} and value["metadata"]["name"] == "cetirun-portal-chat-mtls"]
        self.assertEqual(len(tls_readers), 2)
        self.assertTrue(all(value["metadata"]["namespace"] == "test-platform" for value in tls_readers))

    def test_enabled_portal_requires_identity_realm(self):
        error = self.render(ROOT / "app/cetirun-portal/chart", {"istio": {"host": "portal.example.com"}, "chat": {"enabled": True}}, success=False)
        self.assertIn("identityRealm", error)

    def test_isolated_sds_profile_matches_the_standalone_portal(self):
        directory = ROOT / "examples/native-sandbox"
        values = yaml.safe_load((directory / "portal-chat-values.yaml").read_text())
        profile = list(yaml.safe_load_all((directory / "portal-chat-sds.yaml").read_text()))
        namespace = next(value["metadata"]["name"] for value in profile if value["kind"] == "Namespace")
        objects = self.render(ROOT / "app/cetirun-portal/chart", values, namespace=namespace, release="cetirun-portal")
        deployment = next(value for value in objects if value["kind"] == "Deployment")
        env = self.deployment_env(objects, deployment["metadata"]["name"])
        self.assertEqual(env["FEATURE_CUSTOM_CHAT_ENABLED"], "true")
        self.assertNotEqual(namespace, env["SANDBOX_STATE_NAMESPACE"])
        role = next(value for value in profile if value["kind"] == "Role")
        binding = next(value for value in profile if value["kind"] == "RoleBinding")
        self.assertEqual(role["metadata"]["namespace"], namespace)
        self.assertEqual(role["rules"], [{"apiGroups": [""], "resources": ["secrets"], "verbs": ["list"]}])
        self.assertEqual(binding["metadata"]["namespace"], namespace)
        self.assertEqual(binding["roleRef"], {"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": role["metadata"]["name"]})
        self.assertEqual(binding["subjects"], [{"kind": "ServiceAccount", "namespace": namespace,
                                               "name": deployment["spec"]["template"]["spec"]["serviceAccountName"]}])
        for value in objects + profile:
            if value["kind"] == "ClusterRole":
                self.assertFalse(any(
                    {"secrets", "*"}.intersection(rule["resources"]) and {"list", "*"}.intersection(rule["verbs"])
                    for rule in value["rules"]))
        tls_role = next(value for value in objects if value["kind"] == "Role" and value["metadata"]["name"].endswith("-chat-mtls"))
        self.assertEqual(tls_role["metadata"]["namespace"], namespace)
        self.assertEqual(json.loads(env["CHAT_CALLER_LABELS"]), deployment["spec"]["template"]["metadata"]["labels"])

    def test_documented_isolated_sds_operator_targets_the_standalone_portal(self):
        directory = ROOT / "examples/native-sandbox"
        section = (directory / "README.md").read_text().split("## Isolated Portal Chat mTLS\n", 1)[1].split("\n## ", 1)[0]
        blocks = re.findall(r"^   ```yaml\n(.*?)^   ```", section, re.MULTILINE | re.DOTALL)
        self.assertEqual(len(blocks), 1, "the isolated profile must provide its Operator values")
        values = yaml.safe_load(blocks[0])
        values["global"] = {"portalChat": {
            "enabled": True, "identityRealm": "synthetic",
            **values.get("global", {}).get("portalChat", {}),
        }}
        values["istio"] = {"enabled": True}
        admission = {
            "enabled": True, "enforce": True, "podsEnabled": True, "validationMode": "strict",
            "tlsSecretName": "synthetic-admission-tls",
            # Render-only PEM framing, not a certificate.
            "caBundle": base64.b64encode(b"-----BEGIN CERTIFICATE-----\nQQ==\n-----END CERTIFICATE-----").decode(),
            "adminGroups": ["platform-admins"],
            **values["cetirun-operator"].get("sandboxAdmission", {}),
        }
        admission["adminUsers"] = ["admin@example.com", *admission.get("adminUsers", [])]
        values["cetirun-operator"] = {
            "domainSuffix": "agents.example.com", "namespace": "test-platform", "cetiadmin": {"enabled": False},
            **values["cetirun-operator"],
            "sandboxAdmission": admission,
            "sandboxSources": {
                "enabled": True, "nativeExecution": True, "bootstrapOwnedPrerequisites": True,
                "platformConfigMapName": "sandbox-platform",
            },
        }
        objects = self.render_platform(values)
        self.assertFalse(any(value["kind"] == "Deployment" and value["metadata"]["name"] == "cetirun-portal" for value in objects))
        operator = self.deployment_env(objects, "cetirun-operator")
        access = json.loads(operator["CETIRUN_CHAT_ACCESS"])
        self.assertTrue(access["enabled"])
        profile = list(yaml.safe_load_all((directory / "portal-chat-sds.yaml").read_text()))
        namespace = next(value["metadata"]["name"] for value in profile if value["kind"] == "Namespace")
        portal_objects = self.render(ROOT / "app/cetirun-portal/chart",
                                     yaml.safe_load((directory / "portal-chat-values.yaml").read_text()),
                                     namespace=namespace, release="cetirun-portal")
        portal = self.deployment_env(portal_objects, "cetirun-portal")
        self.assertEqual(access["callers"], [{"namespace": namespace, "podLabels": json.loads(portal["CHAT_CALLER_LABELS"])}])
        portal_deployment = next(value for value in portal_objects if value["kind"] == "Deployment")
        portal_account = portal_deployment["spec"]["template"]["spec"]["serviceAccountName"]
        admission_env = self.deployment_env(objects, "cetirun-sandbox-admission")
        config = json.loads(admission_env["CETIRUN_SANDBOX_ADMISSION"])
        self.assertTrue(config["nativeExecution"])
        self.assertEqual(config["adminUsers"], ["admin@example.com", f"system:serviceaccount:{namespace}:{portal_account}"])
        self.assertEqual(config["adminGroups"], ["platform-admins"])

    def test_umbrella_generates_the_same_caller_as_the_portal(self):
        objects = self.render_platform({
            "istio": {"enabled": True},
            "global": {"portalChat": {"enabled": True, "identityRealm": "synthetic"}},
            "cetirun-operator": {"domainSuffix": "agents.example.com", "namespace": "test-platform", "cetiadmin": {"enabled": False}},
            "cetirun-portal": {"istio": {"host": "portal.example.com"}, "aiGateway": {"catalogNamespace": "test-platform"}},
        })
        portal = self.deployment_env(objects, "cetirun-portal")
        self.assertEqual(portal["SANDBOX_STATE_NAMESPACE"], "test-platform")
        operator = self.deployment_env(objects, "cetirun-operator")
        access = json.loads(operator["CETIRUN_CHAT_ACCESS"])
        self.assertTrue(access["enabled"])
        self.assertEqual(access["callers"], [{"namespace": "test-platform", "podLabels": json.loads(portal["CHAT_CALLER_LABELS"])}])


if __name__ == "__main__":
    unittest.main()
