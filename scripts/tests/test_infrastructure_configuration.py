#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Offline contracts for infrastructure-only published configuration."""

import copy
import importlib.util
from pathlib import Path
import tempfile
import unittest
import json
from unittest.mock import Mock

from scripts.tests import test_published_deployment as _published_fixture

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location(
    "cetirun_platform_deploy_infrastructure", ROOT / "scripts" / "cetirun_platform_deploy.py"
)
DEPLOY = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(DEPLOY)


class InfrastructureConfigurationTests(unittest.TestCase):
    def test_chart_selection_is_explicit_and_portal_is_separate(self):
        self.assertEqual(set(DEPLOY.deployment_charts({})), {"istio", "broker", "platform"})
        charts = DEPLOY.deployment_charts({"portal": {"release": "portal", "namespace": "portal-ns"}})
        self.assertEqual(charts["portal"], "app/cetirun-portal/chart")

    def test_model_routes_are_compared_before_infrastructure_upgrade(self):
        deployment = object.__new__(DEPLOY.Deployment)
        deployment.kubectl = Mock()
        deployment.config = {"_infrastructureOnly": True, "modelRouting": {"mode": "preserve"},
                             "platform": {"namespace": "platform"}}
        deployment.values = {"platform": {"cetirun-ai-gateway": {}}}
        entry = {"model_name": "gpt-4.1-mini", "litellm_params": {"model": "azure/gpt"}}
        config = {"model_list": [entry]}
        rendered = {"kind": "ConfigMap", "metadata": {"name": "gateway-config", "labels": {"app.kubernetes.io/name": "cetirun-ai-gateway"}},
                    "data": {"config.yaml": __import__("yaml").safe_dump(config)}}
        deployment.documents = {"platform": [rendered]}
        deployment.kubectl.return_value = json.dumps({"data": {"config.yaml": __import__("yaml").safe_dump(config)}})
        DEPLOY.Deployment.check_model_routing(deployment)
        changed = {"model_list": [{**entry, "litellm_params": {"model": "azure/other"}}]}
        deployment.kubectl.return_value = json.dumps({"data": {"config.yaml": __import__("yaml").safe_dump(changed)}})
        with unittest.TestCase().assertRaisesRegex(ValueError, "model routes"):
            DEPLOY.Deployment.check_model_routing(deployment)

    def test_model_route_check_is_skipped_for_non_preserve_mode(self):
        deployment = object.__new__(DEPLOY.Deployment)
        deployment.kubectl = Mock()
        deployment.config = {"_infrastructureOnly": True, "modelRouting": {"mode": "auto"}}
        DEPLOY.Deployment.check_model_routing(deployment)
        deployment.kubectl.assert_not_called()


class PublishedInfrastructureRenderTests(unittest.TestCase):
    """Compile and render an infrastructure-only upgrade from real archives."""

    @classmethod
    def setUpClass(cls):
        # Reuse the archive construction used by published-deployment tests;
        # the compiler under test must not use a checkout chart or a mock.
        _published_fixture.PublishedIntegrationTests.setUpClass()
        cls.lock = _published_fixture.PublishedIntegrationTests.lock
        cls.base = _published_fixture.PublishedIntegrationTests().configuration()

    @classmethod
    def tearDownClass(cls):
        _published_fixture.PublishedIntegrationTests.tearDownClass()

    def source_fixture(self, scratch):
        # Normalize the synthetic environment once to obtain a complete,
        # explicit retained runtime contract. The compiler under test consumes
        # this source directly and does not call bootstrap configuration.
        normalized, values, _ = DEPLOY.published_configuration(self.base, self.lock, scratch / "bootstrap-fixture")
        source = copy.deepcopy(normalized)
        source["platform"]["values"] = copy.deepcopy(values["platform"])
        source["broker"]["values"] = copy.deepcopy(values["broker"])
        source["istio"]["values"] = copy.deepcopy(values["istio"])
        operator = source["platform"]["values"]["cetirun-operator"]
        operator["cetirun"]["chart"] = {"repoURL": "oci://retained.example.com/helm/cetirun",
                                        "version": "0.1.7", "digest": "sha256:" + "c" * 64}
        operator["memoryContext"]["image"] = "retained.example.com/memory@sha256:" + "d" * 64
        operator["customRuntime"]["approvedImages"] = ["retained.example.com/custom@sha256:" + "e" * 64]
        operator["aiGateway"]["copilotProxy"]["image"] = {
            "repository": "retained.example.com/cetirun-ai-gateway", "tag": "0.1.7", "digest": "sha256:" + "f" * 64}
        source["platform"]["values"]["global"]["portalChat"]["enabled"] = False
        admission = source["platform"]["values"]["cetirun-operator"]["sandboxAdmission"]
        admission["portalServiceAccount"] = ""
        admission["adminUsers"].append("system:serviceaccount:cetirun-portal-chat:cetirun-portal")
        source["platform"]["values"]["cetirun-portal"]["enabled"] = False
        source["platform"]["values"]["cetirun-observability"]["enabled"] = False
        source["observability"] = {"enabled": False}
        source["modelRouting"] = {"mode": "preserve"}
        # This is an existing installation value, not a release default. The
        # Istio chart requires it even when rendering offline.
        source["istio"]["values"]["clientId"] = source["identity"]["blueprintClientId"]
        source["istio"]["values"]["tenantId"] = source["identity"]["tenantId"]
        source["istio"]["values"]["portalAuth"] = {
            "clusterHost": "cetirun-portal-chat.cetirun-portal-chat.svc.cluster.local",
            "clusterPort": 8081,
            "path": "/internal/auth",
            "mtls": {"enabled": True,
                      "serverIdentity": "spiffe://cluster.local/ns/cetirun-portal-chat/sa/cetirun-portal"},
        }
        portal_values = copy.deepcopy(values["platform"]["cetirun-portal"])
        portal_values["istio"]["host"] = source["domain"]
        portal_values["istio"]["gateway"] = "aks-istio-ingress/cetirun-gateway"
        portal_values["fullnameOverride"] = "cetirun-portal-chat"
        source["portal"] = {"release": "cetirun-portal-chat", "namespace": "cetirun-portal-chat",
                             "values": portal_values}
        source["broker"]["values"]["portal"] = {
            "namespace": source["portal"]["namespace"],
            "serviceAccount": portal_values["serviceAccount"]["name"],
        }
        for key in ("_publishedInventory", "_releaseOrigins", "_source"):
            source.pop(key, None)
        return source

    def test_compiler_preserves_runtime_contract_and_pins_control_plane(self):
        with tempfile.TemporaryDirectory() as temporary:
            scratch = Path(temporary)
            source = self.source_fixture(scratch)
            old_chart = copy.deepcopy(source["platform"]["values"]["cetirun-operator"]["cetirun"]["chart"])
            config, values, roots = DEPLOY.infrastructure_configuration(source, self.lock, scratch / "compile")
            self.assertTrue(config["_infrastructureOnly"])
            self.assertEqual(values["platform"]["cetirun-operator"]["cetirun"]["chart"], old_chart)
            for field in ("customRuntime", "memoryContext", "cetiadmin", "cetirun"):
                self.assertEqual(values["platform"]["cetirun-operator"][field],
                                 source["platform"]["values"]["cetirun-operator"][field])
            self.assertEqual(values["platform"]["cetirun-ai-gateway"]["models"],
                             source["platform"]["values"]["cetirun-ai-gateway"]["models"])
            self.assertEqual(values["platform"]["global"].get("registry"), "")
            self.assertEqual(values["platform"]["global"].get("version"), "")
            self.assertFalse(values["platform"]["cetirun-observability"]["enabled"])
            self.assertEqual(roots["portal"].name, "cetirun-portal")
            self.assertIn("portal", DEPLOY.deployment_charts(config))

    def test_real_helm_render_pins_control_plane_and_standalone_portal(self):
        with tempfile.TemporaryDirectory() as temporary:
            scratch = Path(temporary)
            source = self.source_fixture(scratch)
            config, values, roots = DEPLOY.infrastructure_configuration(source, self.lock, scratch / "compile")
            render_root = scratch / "render"
            render_root.mkdir()
            deployment = DEPLOY.Deployment(config, values, render_root,
                                           published_artifacts=self.lock, chart_roots=roots)
            deployment.render()
            self.assertIn("portal", deployment.documents)
            self.assertTrue(deployment.documents["portal"])
            operator = next(doc for doc in deployment.documents["platform"]
                            if doc.get("kind") == "Deployment" and doc["metadata"]["name"] == "cetirun-operator")
            container = operator["spec"]["template"]["spec"]["containers"][0]
            self.assertEqual(container["image"], self.lock["images"]["cetirun-operator"]["reference"])
            self.assertIn("--chart-version=0.1.7", container["args"])
            self.assertIn("--chart-repo-url=oci://retained.example.com/helm/cetirun", container["args"])
            portal = next(doc for doc in deployment.documents["portal"] if doc.get("kind") == "Deployment")
            self.assertEqual(portal["metadata"]["name"], "cetirun-portal-chat")
            self.assertEqual(portal["spec"]["template"]["spec"]["containers"][0]["image"],
                             self.lock["images"]["cetirun-portal"]["reference"])
            self.assertEqual(deployment.charts["portal"], Path(self.lock["charts"]["cetirun-portal"]["path"]).resolve())
            self.assertEqual(deployment.bootstrap_documents, [])
            self.assertEqual(deployment.rendered_control("cetirun-portal", "portal")["env"]["NOTICE_BANNER"],
                             "published-release-marker")
            self.assertTrue(any("--chart-digest=sha256:" in arg for arg in container.get("args", [])))
            self.assertFalse(any(doc.get("kind") == "Deployment" and
                                 doc.get("metadata", {}).get("name") == "cetirun-observability-ingestor"
                                 for doc in deployment.documents["platform"]))
            # Execution addresses the original standalone release and routes
            # every write through the same revision/observation callbacks.
            deployment.preflight = Mock()
            deployment.enforced = True
            deployment.namespaces = {config[name]["namespace"]: {"labels": {"istio.io/rev": config["istio"]["revision"]}}
                                     for name in deployment.chart_specs}
            deployment.kubectl = Mock()
            deployment.upgrade = Mock()
            deployment.ready = Mock()
            before, after = Mock(return_value=7), Mock()
            deployment.execute_platform(before_upgrade=before, after_upgrade=after)
            self.assertEqual([call.args[0] for call in deployment.upgrade.call_args_list],
                             ["istio", "broker", "platform", "portal"])
            self.assertTrue(all(call.kwargs["expected_revision"] == 7 for call in deployment.upgrade.call_args_list))
            after.assert_any_call("portal", None)
            deployment.ready.assert_any_call("portal")

    def test_rejects_missing_retained_runtime_chart_digest(self):
        with tempfile.TemporaryDirectory() as temporary:
            scratch = Path(temporary)
            source = self.source_fixture(scratch)
            del source["platform"]["values"]["cetirun-operator"]["cetirun"]["chart"]["digest"]
            with self.assertRaisesRegex(ValueError, "runtime chart.*digest"):
                DEPLOY.infrastructure_configuration(source, self.lock, scratch / "compile")


if __name__ == "__main__":
    unittest.main()
