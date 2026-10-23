# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Rendered contracts for the native Agent Tools MCP integration."""

import subprocess
import tempfile
import unittest
from pathlib import Path

import yaml


CHART = Path(__file__).resolve().parents[1]
TARGET = "https://agent-tools-mcp.cetirun-tools.svc.cluster.local/mcp"


class ToolsMCPChartTests(unittest.TestCase):
    def render(self, agent_type, *extra):
        command = [
            "helm",
            "template",
            "cetirun",
            str(CHART),
            "--set",
            f"agentType={agent_type}",
            "--set-string",
            "secrets.model=lint-model",
            "--set",
            "toolsMCP.enabled=true",
            "--set-string",
            f"toolsMCP.url={TARGET}",
            "--set-string",
            "toolsMCP.audience=agent-tools-mcp",
            "--set-string",
            "toolsMCP.caSecretName=agent-tools-mcp-ca",
            "--set",
            "serviceAccount.create=true",
            "--set-string",
            "serviceAccount.name=agent-workload",
            "--set",
            "serviceAccount.automountToken=false",
            "--set",
            "networkPolicy.enabled=true",
            "--set",
            "sidecarScope.enabled=true",
            *extra,
        ]
        if agent_type == "openclaw":
            command.extend(["--set-string", "secrets.openclawGatewayToken=lint-token"])
        return subprocess.run(command, check=False, capture_output=True, text=True)

    def test_native_tools_mcp_has_projected_identity_ca_and_no_broker_path(self):
        for agent_type in ("openclaw", "hermes"):
            with self.subTest(agent_type=agent_type):
                rendered = self.render(agent_type)
                self.assertEqual(rendered.returncode, 0, rendered.stderr)
                output = rendered.stdout
                for expected in (
                    "serviceAccountName: agent-workload",
                    "automountServiceAccountToken: false",
                    "mountPath: /var/run/cetirun/tools",
                    "mountPath: /var/run/cetirun/tools-ca",
                    "defaultMode: 0440",
                    'audience: "agent-tools-mcp"',
                    "expirationSeconds: 600",
                    'secretName: "agent-tools-mcp-ca"',
                    "key: ca.crt",
                    f'value: "{TARGET}"',
                    "kubernetes.io/metadata.name: cetirun-tools",
                    "app.kubernetes.io/name: agent-tools-mcp",
                ):
                    self.assertIn(expected, output)
                self.assertNotIn("CETIRUN_AGENT_BROKER_URL", output)
                self.assertNotIn("cetirun-agent-broker", output)
                self.assertNotIn("cetirun-credentials", output)

    def test_native_configs_use_streamable_http_without_static_auth_headers(self):
        expectations = {
            "openclaw": ('"transport": "streamable-http"', '"sslVerify": true'),
            "hermes": ("transport: streamable-http", "strict_redirect_headers: true"),
        }
        for agent_type, expected_values in expectations.items():
            with self.subTest(agent_type=agent_type):
                rendered = self.render(agent_type)
                self.assertEqual(rendered.returncode, 0, rendered.stderr)
                self.assertIn(TARGET, rendered.stdout)
                for expected in expected_values:
                    self.assertIn(expected, rendered.stdout)
                self.assertNotIn("Authorization: Bearer", rendered.stdout)
                self.assertNotIn('"headers"', rendered.stdout)

    def test_tools_mcp_preserves_native_memory_integration(self):
        for agent_type in ("openclaw", "hermes"):
            with self.subTest(agent_type=agent_type):
                rendered = self.render(
                    agent_type, "--set", "memoryContext.enabled=true",
                )
                self.assertEqual(rendered.returncode, 0, rendered.stderr)
                for expected in (
                    "CETIRUN_MEMORY_", "cetirun-memory-context",
                    "mountPath: /var/run/cetirun/tools",
                    "mountPath: /var/run/cetirun/tools-ca", TARGET,
                ):
                    self.assertIn(expected, rendered.stdout)

    def test_tools_mcp_preserves_other_native_servers(self):
        custom_server = {"url": "https://mcp.example.com/mcp", "timeout": 45}
        for agent_type in ("hermes", "openclaw"):
            for enabled in (True, False):
                with self.subTest(agent_type=agent_type, enabled=enabled):
                    servers = {
                        "example": custom_server,
                        "agent-tools": {
                            "url": "https://old.example.com/mcp",
                            "headers": {"X-Example": "old"},
                        },
                    }
                    native = ({"mcp_servers": servers} if agent_type == "hermes"
                              else {"mcp": {"servers": servers, "enabled": True}})
                    with tempfile.TemporaryDirectory() as directory:
                        values = Path(directory) / "values.yaml"
                        values.write_text(yaml.safe_dump({
                            agent_type: {"config": native},
                            "toolsMCP": {"enabled": enabled},
                        }), encoding="utf-8")
                        rendered = self.render(
                            agent_type, "-f", str(values),
                            "--set", f"toolsMCP.enabled={str(enabled).lower()}",
                        )
                    self.assertEqual(rendered.returncode, 0, rendered.stderr)
                    filename = ("hermes-config.yaml" if agent_type == "hermes"
                                else "openclaw.json")
                    configmap = next(
                        doc for doc in yaml.safe_load_all(rendered.stdout)
                        if doc and doc.get("kind") == "ConfigMap"
                        and filename in doc.get("data", {})
                    )
                    config = yaml.safe_load(configmap["data"][filename])
                    actual = (config["mcp_servers"] if agent_type == "hermes"
                              else config["mcp"]["servers"])
                    self.assertEqual(actual["example"], custom_server)
                    if agent_type == "openclaw":
                        self.assertTrue(config["mcp"]["enabled"])
                    if enabled:
                        expected = {"url": TARGET, "transport": "streamable-http"}
                        if agent_type == "hermes":
                            expected.update(strict_redirect_headers=True, ssl_verify=True)
                        else:
                            expected["sslVerify"] = True
                        self.assertEqual(actual["agent-tools"], expected)
                    else:
                        self.assertNotIn("agent-tools", actual)

    def test_custom_rejects_identity_and_tools_mcp(self):
        for extra, expected in (
            ([], "Custom runtimes do not support Agent Tools MCP"),
            (["--set", "toolsMCP.enabled=false", "--set", "agentIdentity.enabled=true"],
             "Custom runtimes do not support Agent Identity"),
        ):
            with self.subTest(expected=expected):
                rendered = self.render("custom", *extra)
                self.assertNotEqual(rendered.returncode, 0)
                self.assertIn(expected, rendered.stderr)

    def test_tools_mcp_rejects_a_held_sandbox_workload(self):
        with tempfile.TemporaryDirectory() as directory:
            values = Path(directory) / "sandbox-values.yaml"
            values.write_text(
                """\
replicaCount: 0
runtimeClassName: gvisor
podAnnotations:
  sidecar.istio.io/inject: "false"
sidecarScope:
  enabled: false
persistence:
  existingClaim: sandbox-home
  useStatefulSetVolumeClaim: false
skills:
  existingClaim: sandbox-skills
sandboxWorkload:
  contractVersion: v1
  executionHeld: true
  podResources: {}
  helperResources: {}
  securityContext:
    runAsNonRoot: true
  scratchSizeLimit: 1Gi
  ownerReferences:
    - apiVersion: v1
      kind: ConfigMap
      name: sandbox-owner
      uid: 00000000-0000-0000-0000-000000000000
  podLabels: {}
  dnsConfig:
    nameservers:
      - 10.0.0.10
  identityVolumes: []
  identityMounts: []
  identityEnvironment: []
  objects:
    - apiVersion: v1
      kind: ConfigMap
      metadata:
        name: sandbox-object
""",
                encoding="utf-8",
            )
            rendered = self.render(
                "hermes", "-f", str(values), "--set", "sidecarScope.enabled=false"
            )
        self.assertNotEqual(rendered.returncode, 0)
        self.assertIn(
            "Sandbox identities must use compiler-owned explicit projections",
            rendered.stderr,
        )


if __name__ == "__main__":
    unittest.main()
