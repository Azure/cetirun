# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Tests for infra/cluster/deploy.sh's Container Insights agent settings step.

deploy.sh runs against stand-ins for az and kubectl that log their arguments.
The settings are created on a cluster that has none; settings a cluster
already has are checked and never replaced, since replacing them would drop
the cluster's own collection settings.
"""

import os
import pathlib
import subprocess
import tempfile
import textwrap
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
DEPLOY = ROOT / "infra" / "cluster" / "deploy.sh"
SETTINGS_FILE = ROOT / "infra" / "cluster" / "container-azm-ms-agentconfig.yaml"

FAKE_AZ = """#!/usr/bin/env sh
echo "az $*" >> "$CALLS_LOG"
case "$1 $2" in
  "feature show") echo Registered ;;
  "group show") echo westus3 ;;
  "deployment group")
    if [ "$3" = "show" ]; then echo example-cluster; else echo '{}'; fi ;;
  "aks show") echo https://issuer.example.invalid/ ;;
esac
exit 0
"""

FAKE_KUBECTL = """#!/usr/bin/env sh
echo "kubectl $*" >> "$CALLS_LOG"
if [ "$1 $2" = "get configmap" ]; then
  [ -n "$FAKE_KUBECTL_FAIL" ] && exit 1
  case "$*" in
    *--ignore-not-found*)
      [ -f "$FAKE_SETTINGS" ] && echo configmap/container-azm-ms-agentconfig
      exit 0 ;;
    *jsonpath*)
      cat "$FAKE_SETTINGS"
      exit 0 ;;
  esac
fi
exit 0
"""


def shipped_log_settings():
    """The log-data-collection-settings text of the shipped settings file."""
    lines = SETTINGS_FILE.read_text(encoding="utf-8").splitlines()
    start = next(i for i, line in enumerate(lines) if line.startswith("  log-data-collection-settings:"))
    block = []
    for line in lines[start + 1:]:
        if line.strip() and not line.startswith("    "):
            break
        block.append(line[4:])
    return "\n".join(block) + "\n"


class DeployAgentSettingsTests(unittest.TestCase):
    def run_deploy(self, existing_settings=None, kubectl_fails=False):
        with tempfile.TemporaryDirectory() as directory:
            temp = pathlib.Path(directory)
            for name, body in (("az", FAKE_AZ), ("kubectl", FAKE_KUBECTL)):
                stub = temp / name
                stub.write_text(body, encoding="utf-8")
                stub.chmod(0o755)
            calls = temp / "calls.log"
            calls.write_text("", encoding="utf-8")
            environment = {
                **os.environ,
                "PATH": f"{temp}{os.pathsep}{os.environ['PATH']}",
                "CALLS_LOG": str(calls),
                "FAKE_SETTINGS": str(temp / "settings.toml"),
                "RESOURCE_GROUP": "example-rg",
                "LOCATION": "westus3",
                "PARAM_FILE": str(temp / "example.bicepparam"),
            }
            if existing_settings is not None:
                (temp / "settings.toml").write_text(existing_settings, encoding="utf-8")
            if kubectl_fails:
                environment["FAKE_KUBECTL_FAIL"] = "1"
            result = subprocess.run(["bash", str(DEPLOY)], capture_output=True, text=True, env=environment,
                                    check=False)
            return result, calls.read_text(encoding="utf-8").splitlines()

    def assert_no_settings_written(self, calls):
        self.assertFalse([c for c in calls if c.startswith(("kubectl create", "kubectl apply", "kubectl patch",
                                                           "kubectl replace", "kubectl edit"))], calls)

    def test_a_cluster_without_settings_gets_them_before_anything_else(self):
        result, calls = self.run_deploy()
        self.assertEqual(result.returncode, 0, result.stderr)
        created = [i for i, c in enumerate(calls) if c == f"kubectl create -f {SETTINGS_FILE}"]
        self.assertEqual(len(created), 1, calls)
        gateway_api = [i for i, c in enumerate(calls) if c.startswith("az aks update")]
        self.assertTrue(gateway_api and created[0] < gateway_api[0], calls)

    def test_the_shipped_settings_pass_the_check(self):
        result, calls = self.run_deploy(existing_settings=shipped_log_settings())
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_no_settings_written(calls)
        self.assertIn("left unchanged", result.stdout)
        self.assertNotIn("multiline", result.stderr)

    def test_existing_settings_that_turn_collection_off_are_left_unchanged(self):
        settings = textwrap.dedent("""\
            [log_collection_settings]
               [log_collection_settings.stdout]
                  enabled = true
                  exclude_namespaces = ["kube-system", "team-a"]
               [log_collection_settings.env_var]
                  enabled=false # set by the cluster's owner
            """)
        result, calls = self.run_deploy(existing_settings=settings)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_no_settings_written(calls)
        self.assertIn("multiline", result.stderr)
        self.assertTrue([c for c in calls if c.startswith("az aks update")], calls)

    def test_existing_settings_that_keep_collection_on_stop_the_script(self):
        cases = {
            "collection on": "[log_collection_settings.env_var]\n   enabled = true\n",
            "no env_var section": "[log_collection_settings.stdout]\n   exclude_namespaces = [\"team-a\"]\n",
            "off in another section": "[log_collection_settings.stdout]\n   enabled = false\n",
            "no log settings": "",
        }
        for name, settings in cases.items():
            with self.subTest(name):
                result, calls = self.run_deploy(existing_settings=settings)
                self.assertEqual(result.returncode, 1, result.stdout)
                self.assertIn("left unchanged", result.stderr)
                self.assert_no_settings_written(calls)
                self.assertFalse([c for c in calls if c.startswith("az aks update")], calls)

    def test_settings_that_cannot_be_read_stop_the_script(self):
        result, calls = self.run_deploy(kubectl_fails=True)
        self.assertEqual(result.returncode, 1, result.stdout)
        self.assert_no_settings_written(calls)
        self.assertFalse([c for c in calls if c.startswith("az aks update")], calls)


if __name__ == "__main__":
    unittest.main()
