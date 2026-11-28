# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Focused offline checks for the internal-only bootstrap guard."""

import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock


ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("cetirun_platform_deploy", ROOT / "scripts/cetirun_platform_deploy.py")
DEPLOY = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(DEPLOY)
FIXTURE = ROOT / "app/cetirun-platform/ci/deployment-example.yaml"


class InternalBootstrapTests(unittest.TestCase):
    def test_internal_only_is_explicit_and_omits_public_tls_reference(self):
        config, values = DEPLOY.configuration(FIXTURE, internal_only=True)
        self.assertTrue(config["_internalOnly"])
        self.assertFalse(config.get("publicTLS", {}).get("enabled", False))

        deployment = DEPLOY.Deployment(config, values, ROOT / "scripts/tests")
        ingress_reference = ("Secret", config["istio"]["ingressNamespace"], "cetirun-tls", "tls.crt")
        self.assertNotIn(ingress_reference, deployment.references())

    def test_internal_only_rejects_managed_public_tls(self):
        with self.assertRaisesRegex(ValueError, "cannot be combined"):
            DEPLOY.configuration({"publicTLS": {"enabled": True}}, internal_only=True)

    def test_internal_only_rejects_existing_gateway(self):
        config, values = DEPLOY.configuration(FIXTURE, internal_only=True)
        deployment = DEPLOY.Deployment(config, values, ROOT / "scripts/tests")
        deployment.kubectl = Mock(return_value='{"items":[{"metadata":{"name":"cetirun-gateway"}}]}')
        with self.assertRaisesRegex(ValueError, "existing Istio Gateway resources"):
            deployment.check_internal_surface()

    def test_internal_only_rejects_non_cluster_ip_service(self):
        config, values = DEPLOY.configuration(FIXTURE, internal_only=True)
        deployment = DEPLOY.Deployment(config, values, ROOT / "scripts/tests")
        deployment.resource = Mock(side_effect=lambda kind, name, namespace="":
                                   {"name": name} if kind == "namespace" else None)
        deployment.kubectl = Mock(side_effect=lambda *args, **kwargs:
                                  '{"items":[]}' if args[:2] == ("get", "gateways.networking.istio.io")
                                  else '{"items":[{"metadata":{"name":"public"},"spec":{"type":"LoadBalancer"}}]}')
        with self.assertRaisesRegex(ValueError, "non-ClusterIP Service"):
            deployment.check_internal_surface()

    def test_internal_only_rejects_rendered_public_service(self):
        config, values = DEPLOY.configuration(FIXTURE, internal_only=True)
        deployment = DEPLOY.Deployment(config, values, ROOT / "scripts/tests")
        deployment.documents = {
            "platform": [{"kind": "Service", "metadata": {"name": "gateway"},
                          "spec": {"type": "LoadBalancer", "ports": [{"port": 80, "nodePort": 30080}]}}],
            "broker": [], "istio": [],
        }
        with self.assertRaisesRegex(ValueError, "rendered Service"):
            deployment.check_internal_surface()

    def test_internal_only_plan_does_not_claim_public_istio_stage(self):
        config, values = DEPLOY.configuration(FIXTURE, internal_only=True)
        deployment = DEPLOY.Deployment(config, values, ROOT / "scripts/tests")
        deployment.documents = {"istio": [], "broker": [], "platform": []}
        stages = deployment.plan()["stages"]
        self.assertNotIn("CetiRun Istio/OAuth2 release and mesh provider", stages)

    def test_internal_stage_receipt_allows_only_matching_private_receipt(self):
        config, values = DEPLOY.configuration(FIXTURE, internal_only=True)
        with tempfile.TemporaryDirectory() as directory:
            receipt = Path(directory) / "internal-stage.json"
            receipt.write_text(json.dumps({
                "schemaVersion": 1,
                "mode": "internal-only",
                "target": {"cluster": config["target"]["cluster"], "server": config["target"]["server"],
                            "clusterUID": config["target"]["clusterUID"]},
                "release": {key: config["release"][key] for key in ("registry", "version", "gitSHA")},
                "charts": {name: {"release": config[name]["release"], "namespace": config[name]["namespace"]}
                           for name in deployment_chart_names(config)},
            }))
            receipt.chmod(0o600)
            config["_internalStageReceipt"] = str(receipt)
            deployment = DEPLOY.Deployment(config, values, ROOT / "scripts/tests")
            self.assertTrue(deployment.internal_stage_receipt_valid())


def deployment_chart_names(config):
    return {**DEPLOY.CHARTS, **({"portal": "app/cetirun-portal/chart"} if "portal" in config else {})}


if __name__ == "__main__":
    unittest.main()
