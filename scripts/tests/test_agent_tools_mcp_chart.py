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

    def test_broker_identity_modes_control_sdk_and_configuration(self):
        chart = ROOT / "app/cetirun-agent-broker/chart"
        common = [
            "agentIdentity.tenantId=11111111-1111-1111-1111-111111111111",
            "agentIdentity.encryptionClientId=22222222-2222-2222-2222-222222222222",
            "agentIdentity.keyVaultKeyUrl=https://example.vault.azure.net/keys/cache",
            "agentIdentity.redirectUri=https://portal.example.com/api/v1/connections/callback",
            "agentIdentity.portalOrigin=https://portal.example.com",
            "agentIdentity.portalBaseUrl=https://portal.example.com",
            "istio.revision=asm-example", "istio.sidecarInjection=true",
        ]
        for autonomous, delegated in ((False, False), (True, False), (False, True), (True, True)):
            with self.subTest(autonomous=autonomous, delegated=delegated):
                flags = common + [
                    f"identityModes.azureAgent.enabled={str(autonomous).lower()}",
                    f"identityModes.microsoftDelegated.enabled={str(delegated).lower()}",
                ]
                if autonomous or delegated:
                    flags.append("agentIdentity.blueprintClientId=33333333-3333-3333-3333-333333333333")
                if delegated:
                    flags.append("agentIdentity.authorizationClientId=44444444-4444-4444-4444-444444444444")
                extra = tuple(arg for flag in flags for arg in ("--set", flag))
                rendered = self.render(chart, chart / "values.yaml", "credentials", extra)
                self.assertEqual(rendered.returncode, 0, rendered.stderr)
                objects = [value for value in yaml.safe_load_all(rendered.stdout) if value]
                pod = next(v for v in objects if v["kind"] == "Deployment")["spec"]["template"]["spec"]
                self.assertEqual(any(c["name"] == "auth-sdk" for c in pod["containers"]), delegated)
                self.assertEqual(any(v["name"] == "auth-sdk-keys" for v in pod["volumes"]), delegated)
                env = {value["name"]: value.get("value") for value in pod["containers"][0]["env"]}
                self.assertEqual(env["AZURE_AGENT_ENABLED"], str(autonomous).lower())
                self.assertEqual(env["MICROSOFT_DELEGATED_ENABLED"], str(delegated).lower())
                if autonomous or delegated:
                    invalid = self.render(chart, chart / "values.yaml", "credentials", (*extra, "--set", "agentIdentity.blueprintClientId="))
                    self.assertNotEqual(invalid.returncode, 0)
