# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

import io
import shutil
import subprocess
import unittest
from pathlib import Path
import yaml


CHART = Path(__file__).parents[2] / "app" / "cetirun-ai-gateway" / "chart"


@unittest.skipUnless(shutil.which("helm"), "requires Helm")
class ProviderIdentityChartTests(unittest.TestCase):
    def render(self, *sets):
        command = ["helm", "template", "provider-identity", str(CHART)]
        for value in sets:
            command.extend(["--set", value])
        return subprocess.run(command, capture_output=True, text=True, check=False)

    def test_identity_adds_workload_binding_and_refresh_setting(self):
        result = self.render(
            "providerIdentity.enabled=true",
            "providerIdentity.clientId=11111111-1111-1111-1111-111111111111",
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        documents = [document for document in yaml.safe_load_all(io.StringIO(result.stdout)) if document]
        service_account = next(document for document in documents if document.get("kind") == "ServiceAccount")
        deployment = next(document for document in documents if document.get("kind") == "Deployment")
        config_map = next(document for document in documents if document.get("kind") == "ConfigMap" and "config.yaml" in document.get("data", {}))
        self.assertEqual(service_account["metadata"]["annotations"]["azure.workload.identity/client-id"], "11111111-1111-1111-1111-111111111111")
        self.assertEqual(deployment["spec"]["template"]["metadata"]["labels"]["azure.workload.identity/use"], "true")
        self.assertIn("enable_azure_ad_token_refresh: true", config_map["data"]["config.yaml"])

    def test_zero_identity_and_conflicting_content_identity_fail(self):
        zero = self.render(
            "providerIdentity.enabled=true",
            "providerIdentity.clientId=00000000-0000-0000-0000-000000000000",
        )
        self.assertNotEqual(zero.returncode, 0)
        self.assertIn("must not be the zero UUID", zero.stderr)

        conflict = self.render(
            "providerIdentity.enabled=true",
            "providerIdentity.clientId=11111111-1111-1111-1111-111111111111",
            "content.enabled=true",
            "content.clientId=22222222-2222-2222-2222-222222222222",
        )
        self.assertNotEqual(conflict.returncode, 0)
        self.assertIn("must match content.clientId", conflict.stderr)

    def test_identity_options_are_strict_and_same_content_identity_is_allowed(self):
        self.assertEqual(self.render("providerIdentity.enabled=false").returncode, 0)
        for value in ("0", "''"):
            result = self.render(f"providerIdentity.enabled={value}")
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("/providerIdentity/enabled", result.stderr)
            self.assertIn("want boolean", result.stderr)
        missing = self.render("providerIdentity.enabled=true")
        self.assertNotEqual(missing.returncode, 0)
        self.assertIn("providerIdentity.clientId must be a UUID", missing.stderr)
        no_service_account = self.render(
            "providerIdentity.enabled=true",
            "providerIdentity.clientId=11111111-1111-1111-1111-111111111111",
            "serviceAccount.create=false",
        )
        self.assertNotEqual(no_service_account.returncode, 0)
        self.assertIn("serviceAccount.create=true", no_service_account.stderr)
        annotation = self.render(
            "providerIdentity.enabled=true",
            "providerIdentity.clientId=11111111-1111-1111-1111-111111111111",
            "serviceAccount.annotations.azure\\.workload\\.identity/client-id=22222222-2222-2222-2222-222222222222",
        )
        self.assertNotEqual(annotation.returncode, 0)
        self.assertIn("conflicts with providerIdentity", annotation.stderr)
        same = self.render(
            "providerIdentity.enabled=true",
            "providerIdentity.clientId=11111111-1111-1111-1111-111111111111",
            "content.enabled=true",
            "content.clientId=11111111-1111-1111-1111-111111111111",
            "content.clusterName=prod",
            "content.adx.endpoint=https://adx.example.com",
        )
        self.assertEqual(same.returncode, 0, same.stderr)

    def test_default_api_key_mode_does_not_enable_token_refresh(self):
        result = self.render()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("enable_azure_ad_token_refresh", result.stdout)


if __name__ == "__main__":
    unittest.main()
