# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Keep deployment checks active when client schema downloads are unavailable."""

import json
import unittest
from unittest.mock import patch

from scripts.tests.test_full_deployment import DEPLOY, DeploymentFixture


class DeploymentTransportTests(DeploymentFixture, unittest.TestCase):
    def test_client_validation_defaults_on_and_requires_boolean(self):
        config, _ = self.configure(self.config)
        self.assertNotIn("clientOpenAPIValidation", config["release"])
        for value in (None, "false", 0, [], {}):
            with self.subTest(value=value), patch.object(DEPLOY.subprocess, "run") as run:
                self.config["release"]["clientOpenAPIValidation"] = value
                with self.assertRaisesRegex(ValueError, "clientOpenAPIValidation must be a boolean"):
                    self.configure(self.config)
                run.assert_not_called()

    def test_default_upgrades_keep_client_schema_validation(self):
        deployment = self.deployment()
        self.assertTrue(deployment.plan()["clientOpenAPIValidation"])
        deployment.execute()
        self.assertTrue(deployment.applied)
        for _, _, command in deployment.applied:
            self.assertNotIn("--disable-openapi-validation", command)

    def transport_deployment(self):
        self.config["release"]["clientOpenAPIValidation"] = False
        return self.deployment()

    def test_explicit_transport_keeps_staging_and_strict_admission_dry_run(self):
        deployment = self.transport_deployment()
        self.assertFalse(deployment.plan()["clientOpenAPIValidation"])
        original = deployment.kubectl
        probes = []

        def kubectl(*args, input_text=None):
            if args[:2] == ("create", "--raw"):
                self.assertEqual(args, ("create", "--raw",
                    "/apis/cetirun.microsoft.com/v1alpha1/teams?dryRun=All&fieldValidation=Strict", "-f", "-"))
                document = json.loads(input_text)
                self.assertEqual(document["apiVersion"], "cetirun.microsoft.com/v1alpha1")
                self.assertEqual(document["kind"], "Team")
                self.assertEqual(document["metadata"]["name"], "cetirun-admission-deployment-check")
                probes.append(document)
                return input_text
            return original(*args, input_text=input_text)

        with patch.object(deployment, "kubectl", side_effect=kubectl):
            deployment.execute()
        self.assertEqual([name for name, _, _ in deployment.applied], ["istio", "broker", "platform", "platform"])
        for _, _, command in deployment.applied:
            self.assertIn("--disable-openapi-validation", command)
        self.assertEqual(len(probes), 1)

    def test_transport_cannot_bypass_failed_preflight_or_admission(self):
        deployment = self.transport_deployment()
        with patch.object(deployment, "preflight", side_effect=ValueError("target or ownership mismatch")):
            with self.assertRaisesRegex(ValueError, "target or ownership mismatch"):
                deployment.execute()
        self.assertFalse(deployment.writes)
        original = deployment.kubectl

        def deny(*args, input_text=None):
            if args[:2] == ("create", "--raw"):
                raise ValueError("admission rejected the dry-run")
            return original(*args, input_text=input_text)

        with patch.object(deployment, "kubectl", side_effect=deny):
            with self.assertRaisesRegex(ValueError, "admission rejected the dry-run"):
                deployment.execute()


if __name__ == "__main__":
    unittest.main()
