# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""The session logger sidecar is off by default and runs only when enabled."""

import subprocess
import unittest
from pathlib import Path

import yaml


CHART = Path(__file__).resolve().parents[1]


def containers(agent_type, *extra):
    """The containers of the Agent's StatefulSet, rendered with the chart defaults."""
    command = [
        "helm", "template", "cetirun", str(CHART),
        "--set", f"agentType={agent_type}",
        "--set-string", "secrets.model=lint-model",
        "--set-string", "secrets.apiBase=http://gateway.example.svc.cluster.local:4000/v1",
        "--set-string", "aiGateway.keySecretName=lint-model-key",
        "--set-string", "hermes.env.HERMES_WEBAPP_PUBLIC_URL=https://agents.example.com/ui/cetirun",
        "--set-json", 'aiGateway.models=[{"id":"lint-model","name":"Lint model"}]',
        *extra,
    ]
    if agent_type == "openclaw":
        command.extend(["--set-string", "secrets.openclawGatewayToken=lint-token"])
    rendered = subprocess.run(command, check=True, capture_output=True, text=True).stdout
    for document in yaml.safe_load_all(rendered):
        if document and document["kind"] == "StatefulSet":
            return [container["name"] for container in document["spec"]["template"]["spec"]["containers"]]
    raise AssertionError("the chart rendered no StatefulSet")


class SessionLoggerTests(unittest.TestCase):
    def test_off_by_default(self):
        # OpenClaw keeps its sessions in SQLite, so the logger would export nothing for it,
        # and the Hermes gateway log is not a transcript.
        for agent_type in ("openclaw", "hermes"):
            with self.subTest(agent_type=agent_type):
                self.assertNotIn("logger", containers(agent_type))

    def test_runs_when_enabled(self):
        for agent_type in ("openclaw", "hermes"):
            with self.subTest(agent_type=agent_type):
                self.assertIn("logger", containers(agent_type, "--set", "logger.enabled=true"))


if __name__ == "__main__":
    unittest.main()
