# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Offline checks of the Portal chart's usage report: the workload identity, the configuration and its checks."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[2]
CHART = ROOT / "app/cetirun-portal/chart"
OFFLINE = {**os.environ, "KUBECONFIG": os.devnull, "HELM_REGISTRY_CONFIG": os.devnull, "HELM_REPOSITORY_CONFIG": os.devnull}
# Synthetic inputs: a public example domain and a synthetic client ID.
USAGE = {
    "enabled": True,
    "clusterName": "example",
    "adx": {"endpoint": "https://example.australiaeast.kusto.windows.net/", "database": "CetiRunLogs",
            "clientId": "99999999-9999-9999-9999-999999999999"},
}


@unittest.skipUnless(shutil.which("helm"), "requires Helm")
class PortalUsageChartTests(unittest.TestCase):
    def render(self, values, *, success=True):
        values = {"istio": {"host": "portal.example.com"}, "agentIdentity": {
            "tenantId": "11111111-1111-1111-1111-111111111111", "portalClientId": "22222222-2222-2222-2222-222222222222"}, **values}
        result = subprocess.run(
            ["helm", "template", "usage-test", str(CHART), "--namespace", "test-platform", "--values", "-"],
            input=json.dumps(values), text=True, capture_output=True, env=OFFLINE, timeout=30, check=False,
        )
        if not success:
            self.assertNotEqual(result.returncode, 0, "the chart rendered")
            return result.stderr
        self.assertEqual(result.returncode, 0, result.stderr)
        return [value for value in yaml.safe_load_all(result.stdout) if value]

    @staticmethod
    def find(objects, kind):
        return next(value for value in objects if value["kind"] == kind and value["metadata"]["name"] == "cetirun-portal")

    def usage_env(self, objects):
        container = self.find(objects, "Deployment")["spec"]["template"]["spec"]["containers"][0]
        return {item["name"]: item["value"] for item in container["env"] if item["name"].startswith("USAGE_")}

    def test_off_by_default_without_an_identity(self):
        objects = self.render({})
        self.assertEqual(self.usage_env(objects), {})
        pod = self.find(objects, "Deployment")["spec"]["template"]["metadata"]
        self.assertNotIn("azure.workload.identity/use", pod["labels"])
        self.assertFalse(pod.get("annotations"))
        self.assertNotIn("annotations", self.find(objects, "ServiceAccount")["metadata"])

    def test_enabled_names_the_identity_and_the_logs_database(self):
        objects = self.render({"usage": USAGE, "serviceAccount": {"annotations": {"example.com/owner": "platform"}}})
        self.assertEqual(self.usage_env(objects), {
            "USAGE_ENABLED": "true",
            "USAGE_ADX_ENDPOINT": "https://example.australiaeast.kusto.windows.net",
            "USAGE_ADX_DATABASE": "CetiRunLogs",
            "USAGE_CLUSTER_NAME": "example",
        })
        pod = self.find(objects, "Deployment")["spec"]["template"]["metadata"]
        self.assertEqual(pod["labels"]["azure.workload.identity/use"], "true")
        # The webhook sets the client ID when it creates a Pod: another client ID rolls the Pods.
        self.assertEqual(pod["annotations"], {"cetirun.microsoft.com/usage-client-id": "99999999-9999-9999-9999-999999999999"})
        changed = self.render({"usage": {**USAGE, "adx": {**USAGE["adx"], "clientId": "88888888-8888-8888-8888-888888888888"}}})
        self.assertEqual(self.find(changed, "Deployment")["spec"]["template"]["metadata"]["annotations"],
                         {"cetirun.microsoft.com/usage-client-id": "88888888-8888-8888-8888-888888888888"})
        self.assertEqual(self.find(objects, "ServiceAccount")["metadata"]["annotations"], {
            "example.com/owner": "platform", "azure.workload.identity/client-id": "99999999-9999-9999-9999-999999999999"})
        # The selector stays as it is: an upgrade can turn the report on in place.
        self.assertNotIn("azure.workload.identity/use", self.find(objects, "Deployment")["spec"]["selector"]["matchLabels"])

    def test_incomplete_or_invalid_settings_are_refused(self):
        cases = [
            ({"enabled": "true"}, "usage.enabled must be a boolean"),
            ({"adx": {**USAGE["adx"], "endpoint": "http://example.kusto.windows.net"}}, "usage.adx.endpoint must be the query URI"),
            ({"adx": {**USAGE["adx"], "endpoint": "https://example.kusto.windows.net/CetiRunLogs"}}, "usage.adx.endpoint must be the query URI"),
            ({"adx": {**USAGE["adx"], "database": "Ceti-Run-Logs"}}, "usage.adx.database must be the name of the logs database"),
            ({"adx": {**USAGE["adx"], "clientId": ""}}, "usage.adx.clientId must be the client ID"),
            ({"clusterName": ""}, "usage.clusterName must be a DNS label"),
            ({"clusterName": "Example_Cluster"}, "usage.clusterName must be a DNS label"),
        ]
        for change, message in cases:
            with self.subTest(change=change):
                self.assertIn(message, self.render({"usage": {**USAGE, **change}}, success=False))
        self.assertIn("usage.enabled needs serviceAccount.create",
                      self.render({"usage": USAGE, "serviceAccount": {"create": False, "name": "cetirun-portal"}}, success=False))
        self.assertIn("usage must be an object", self.render({"usage": "on"}, success=False))

    def test_disabled_settings_are_not_checked(self):
        objects = self.render({"usage": {"enabled": False, "clusterName": "", "adx": {"endpoint": "", "clientId": ""}}})
        self.assertEqual(self.usage_env(objects), {})


if __name__ == "__main__":
    unittest.main()
