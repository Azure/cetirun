# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

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
class AgentToolsMCPChartTests(unittest.TestCase):
    def render(self, chart, values, namespace, extra=()):
        return subprocess.run(["helm", "template", "tools-test", str(chart), "-n", namespace, "-f", str(values), *extra], env=OFFLINE, text=True, capture_output=True, timeout=30)

    def test_standalone_ports_trust_and_credentials_are_separate(self):
        chart = ROOT / "app/agent-tools-mcp/chart"
        rendered = self.render(chart, chart / "ci/render-values.yaml", "cetirun-tools")
        self.assertEqual(rendered.returncode, 0, rendered.stderr)
        objects = [value for value in yaml.safe_load_all(rendered.stdout) if value]
        service = next(value for value in objects if value["kind"] == "Service")
        self.assertEqual(service["spec"]["ports"], [{"name": "https", "port": 443, "targetPort": "https"}])
        deployment = next(value for value in objects if value["kind"] == "Deployment")
        pod = deployment["spec"]["template"]["spec"]
        self.assertFalse(pod["automountServiceAccountToken"])
        env = {value["name"]: value.get("value") for value in pod["containers"][0]["env"]}
        self.assertEqual(env["AGENT_BROKER_BASE_URL"], "https://cetirun-agent-broker.cetirun-credentials.svc.cluster.local")
        roles = [value for value in objects if value["kind"] == "Role"]
        self.assertEqual(roles, [])
        self.assertNotIn("OPERATION_RECORDS_NAMESPACE", env)
        for role in (value for value in objects if value["kind"] in ("Role", "ClusterRole")):
            for rule in role["rules"]:
                self.assertNotIn("secrets", rule.get("resources", []))
        self.assertNotIn("client-secret", rendered.stdout)
        self.assertIn("15012", rendered.stdout)
        denied = self.render(chart, chart / "ci/render-values.yaml", "cetirun-credentials")
        self.assertNotEqual(denied.returncode, 0)

    def test_full_stack_uses_separate_broker_and_native_mcp(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            for relative in ("cetirun-platform", "cetirun-operator/chart", "cetirun-portal/chart", "cetirun-ai-gateway/chart", "agent-tools-mcp/chart", "cetirun-agent-broker/chart"):
                shutil.copytree(ROOT / "app" / relative, base / relative, ignore=shutil.ignore_patterns("charts"))
            subprocess.run(["helm", "dependency", "build", "--skip-refresh", str(base / "cetirun-platform")], env=OFFLINE, check=True, capture_output=True, timeout=30)
            chart = base / "cetirun-platform"
            result = self.render(chart, chart / "ci/tools-render-values.yaml", "cetirun-system", ("--set", "global.registry=registry.example.invalid"))
            self.assertEqual(result.returncode, 0, result.stderr)
            objects = [value for value in yaml.safe_load_all(result.stdout) if value]
            deployments = {value["metadata"]["name"]: value for value in objects if value["kind"] == "Deployment"}
            self.assertNotIn("cetirun-agent-broker", deployments)
            self.assertEqual(deployments["agent-tools-mcp"]["spec"]["template"]["spec"]["containers"][0]["image"], "registry.example.invalid/agent-tools-mcp:0.1.1")
            operator = next(value for value in objects if value["kind"] == "Deployment" and value["metadata"]["labels"]["app.kubernetes.io/name"] == "cetirun-operator")["spec"]["template"]["spec"]["containers"][0]
            env = {value["name"]: value.get("value") for value in operator["env"]}
            self.assertEqual(env["TOOLS_MCP_CA_SECRET_NAME"], "tools-ca")
            self.assertIn("AGENT_ID_BROKER_URL", env)
            self.assertEqual(env["AGENT_ID_ENABLED"], "false")
