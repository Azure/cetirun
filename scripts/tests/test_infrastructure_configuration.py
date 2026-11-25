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
from unittest.mock import Mock, patch

from scripts.tests import test_published_deployment as _published_fixture
from scripts import deploy_environment as ENV_DEPLOY

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


class InfrastructureWiringTests(unittest.TestCase):
    def setUp(self):
        self.driver = DEPLOY.Deployment(
            {"_infrastructureOnly": True, "platform": {"namespace": "platform"},
             "broker": {"namespace": "broker"}},
            {"platform": {"cetirun-portal": {"enabled": True}}}, Path("unused"))
        self.operator = {"name": "manager", "env": [
            {"name": "AI_GATEWAY_ENABLED", "value": "true"},
            {"name": "AI_GATEWAY_MASTER_KEY_SECRET", "value": "gateway-auth"},
            {"name": "AI_GATEWAY_MASTER_KEY_SECRET_KEY", "value": "master-key"},
            {"name": "CONNECTIONS_MCP_RUNTIME_TYPES", "value": "hermes,openclaw,custom"}]}
        self.gateway = {"name": "gateway", "env": [self.secret("LITELLM_MASTER_KEY", "gateway-auth", "master-key"),
                                                     self.secret("DATABASE_URL", "external-db", "url"),
                                                     self.secret("LITELLM_SALT_KEY", "gateway-auth", "salt-key")]}
        self.portal = {"name": "portal", "env": [
            {"name": "FEATURE_AGENT_TOOLS_ENABLED", "value": "true"},
            {"name": "CONNECTIONS_MCP_RUNTIME_TYPES", "value": "custom,hermes,openclaw"}]}
        self.broker = {"name": "broker", "env": [
            {"name": "OPERATOR_NAMESPACE", "value": "platform"},
            {"name": "OPERATOR_SERVICE_ACCOUNT", "value": "portal-account"},
            {"name": "PORTAL_NAMESPACE", "value": "platform"},
            {"name": "PORTAL_SERVICE_ACCOUNT", "value": "portal-account"},
            {"name": "CONNECTIONS_MCP_RUNTIME_TYPES", "value": "hermes,custom,openclaw"}]}
        self.gateway_doc = self.workload("gateway-renamed", self.gateway)
        self.gateway_doc["spec"]["template"]["spec"]["initContainers"] = [
            {"name": "migrate", "env": [self.secret("DATABASE_URL", "external-db", "url")]}]
        self.driver.documents = {"platform": [self.workload("operator", self.operator), self.gateway_doc,
                                              self.workload("portal-renamed", self.portal)],
                                 "broker": [self.workload("broker", self.broker)]}
        self.driver.kubectl = Mock(return_value=json.dumps(self.gateway_doc))

    @staticmethod
    def secret(name, secret, key):
        return {"name": name, "valueFrom": {"secretKeyRef": {"name": secret, "key": key}}}

    @staticmethod
    def workload(name, container):
        return {"kind": "Deployment", "metadata": {"name": name},
                "spec": {"template": {"spec": {"containers": [container], "serviceAccountName": "portal-account"}}}}

    @staticmethod
    def set_env(container, name, value):
        next(item for item in container["env"] if item["name"] == name)["value"] = value

    def test_valid_bundled_and_standalone_portal_wiring(self):
        self.driver.check_infrastructure_wiring()
        portal = self.driver.documents["platform"].pop()
        self.driver.config["portal"] = {"namespace": "portal-ns"}
        self.driver.values["portal"] = {}
        self.driver.documents["portal"] = [portal]
        self.set_env(self.broker, "PORTAL_NAMESPACE", "portal-ns")
        self.driver.check_infrastructure_wiring()
        self.driver.kubectl.assert_not_called()

    def test_broker_caller_must_match_active_portal_in_both_topologies(self):
        for standalone in (False, True):
            for field in ("PORTAL_NAMESPACE", "PORTAL_SERVICE_ACCOUNT"):
                with self.subTest(standalone=standalone, field=field):
                    self.setUp()
                    if standalone:
                        self.driver.config["portal"] = {"namespace": "platform"}
                        self.driver.values["portal"] = {}
                        self.driver.documents["portal"] = [self.driver.documents["platform"].pop()]
                    self.set_env(self.broker, field, "wrong")
                    with self.assertRaisesRegex(ValueError, "active Portal"):
                        self.driver.check_infrastructure_wiring()

    def test_management_key_requires_same_secret_and_key(self):
        for field in ("AI_GATEWAY_MASTER_KEY_SECRET", "AI_GATEWAY_MASTER_KEY_SECRET_KEY"):
            with self.subTest(field=field):
                self.setUp()
                self.set_env(self.operator, field, "different")
                with self.assertRaisesRegex(ValueError, "management-key"):
                    self.driver.check_infrastructure_wiring()
                self.driver.kubectl.assert_not_called()

    def test_broker_operator_caller_is_required_even_with_portal_disabled(self):
        for portal_enabled in (True, False):
            for field in ("OPERATOR_NAMESPACE", "OPERATOR_SERVICE_ACCOUNT"):
                with self.subTest(portal_enabled=portal_enabled, field=field):
                    self.setUp()
                    self.driver.values["platform"]["cetirun-portal"]["enabled"] = portal_enabled
                    self.set_env(self.broker, field, "different")
                    with self.assertRaisesRegex(ValueError, "Operator namespace and ServiceAccount"):
                        self.driver.check_infrastructure_wiring()
                    self.driver.kubectl.assert_not_called()

    def test_custom_operator_identity_matches_broker_without_active_portal(self):
        self.driver.values["platform"]["cetirun-portal"]["enabled"] = False
        self.driver.documents["platform"][0]["spec"]["template"]["spec"]["serviceAccountName"] = "custom-operator"
        self.set_env(self.broker, "OPERATOR_SERVICE_ACCOUNT", "custom-operator")
        self.driver.check_infrastructure_wiring()

    def test_gateway_reference_is_effective_render_including_generated_auth_secret(self):
        self.set_env(self.operator, "AI_GATEWAY_MASTER_KEY_SECRET", "renamed-gateway-auth")
        self.gateway["env"][0]["valueFrom"]["secretKeyRef"]["name"] = "renamed-gateway-auth"
        self.driver.check_infrastructure_wiring()

    def test_runtime_sets_reject_narrower_broker_or_portal_and_invalid_members(self):
        for component in ("operator", "portal", "broker"):
            for runtimes in ("hermes,openclaw", "hermes,hermes", "unknown", ""):
                with self.subTest(component=component, runtimes=runtimes):
                    self.setUp()
                    self.set_env(getattr(self, component), "CONNECTIONS_MCP_RUNTIME_TYPES", runtimes)
                    with self.assertRaisesRegex(ValueError, "runtime sets"):
                        self.driver.check_infrastructure_wiring()

    def test_disabled_agent_tools_does_not_require_equal_runtime_sets(self):
        self.set_env(self.portal, "FEATURE_AGENT_TOOLS_ENABLED", "false")
        self.set_env(self.broker, "CONNECTIONS_MCP_RUNTIME_TYPES", "hermes")
        self.driver.check_infrastructure_wiring()

    def test_disabled_bundled_portal_does_not_validate_an_inactive_caller(self):
        self.driver.values["platform"]["cetirun-portal"]["enabled"] = False
        self.driver.documents["platform"].pop()
        self.driver.check_infrastructure_wiring()

    def test_database_reference_preserved_without_reading_secret_data(self):
        self.driver.check_gateway_database_binding()
        self.driver.kubectl.assert_called_once_with("get", "deployment", "gateway-renamed", "--namespace", "platform", "-o", "json")
        # API optional=false is equivalent to an omitted optional field.
        live = copy.deepcopy(self.gateway_doc)
        live["spec"]["template"]["spec"]["containers"][0]["env"][1]["valueFrom"]["secretKeyRef"]["optional"] = False
        self.driver.kubectl.return_value = json.dumps(live)
        self.driver.check_gateway_database_binding()

    def test_database_secret_or_key_change_rejected_before_upgrade(self):
        for field in ("name", "key"):
            with self.subTest(field=field):
                live = copy.deepcopy(self.gateway_doc)
                pod = live["spec"]["template"]["spec"]
                for container in [*pod["initContainers"], *pod["containers"]]:
                    for entry in container["env"]:
                        if entry["name"] == "DATABASE_URL":
                            entry["valueFrom"]["secretKeyRef"][field] = "different"
                self.driver.kubectl.return_value = json.dumps(live)
                with self.assertRaisesRegex(ValueError, "change the Gateway DATABASE_URL"):
                    self.driver.check_gateway_database_binding()

    def test_database_encryption_secret_name_or_key_change_is_rejected(self):
        for field in ("name", "key"):
            with self.subTest(field=field):
                live = copy.deepcopy(self.gateway_doc)
                gateway = live["spec"]["template"]["spec"]["containers"][0]
                salt = next(entry for entry in gateway["env"] if entry["name"] == "LITELLM_SALT_KEY")
                salt["valueFrom"]["secretKeyRef"][field] = "different"
                self.driver.kubectl.return_value = json.dumps(live)
                with self.assertRaisesRegex(ValueError, "LITELLM_SALT_KEY"):
                    self.driver.check_gateway_database_binding()
                self.assertTrue(all(call.args[:2] == ("get", "deployment") for call in self.driver.kubectl.call_args_list))

    def test_migration_consumer_cannot_use_another_database(self):
        self.gateway_doc["spec"]["template"]["spec"]["initContainers"][0]["env"][0]["valueFrom"]["secretKeyRef"]["key"] = "different"
        with self.assertRaisesRegex(ValueError, "migration DATABASE_URL"):
            self.driver.check_gateway_database_binding()

    def test_adding_same_database_migration_consumer_is_allowed(self):
        live = copy.deepcopy(self.gateway_doc)
        del live["spec"]["template"]["spec"]["initContainers"]
        self.driver.kubectl.return_value = json.dumps(live)
        self.driver.check_gateway_database_binding()

    def test_database_literal_optional_or_duplicate_bindings_are_rejected(self):
        for variant in ("literal", "optional", "duplicate", "missing"):
            with self.subTest(variant=variant):
                self.setUp()
                entries = self.gateway["env"]
                if variant == "literal":
                    entries[1] = {"name": "DATABASE_URL", "value": "synthetic-value"}
                elif variant == "optional":
                    entries[1]["valueFrom"]["secretKeyRef"]["optional"] = True
                elif variant == "duplicate":
                    entries.append(copy.deepcopy(entries[1]))
                else:
                    entries.pop(1)
                with self.assertRaisesRegex(ValueError, "DATABASE_URL"):
                    self.driver.check_gateway_database_binding()

    def test_disabled_gateway_does_not_require_database_or_management_key_read(self):
        self.driver.values["platform"]["cetirun-ai-gateway"] = {"enabled": False}
        self.driver.documents["platform"].remove(self.gateway_doc)
        self.driver.check_gateway_database_binding()
        self.driver.check_infrastructure_wiring()
        self.driver.kubectl.assert_not_called()
        # An enabled Gateway with a missing render is still invalid.
        self.driver.values["platform"]["cetirun-ai-gateway"]["enabled"] = True
        with self.assertRaisesRegex(ValueError, "unavailable"):
            self.driver.check_gateway_database_binding()

    def test_bootstrap_database_check_does_not_require_an_installed_gateway(self):
        self.driver.config["_infrastructureOnly"] = False
        self.driver.documents = {}
        self.driver.check_gateway_database_binding()
        self.driver.check_infrastructure_wiring()
        self.driver.kubectl.assert_not_called()

    def test_fallback_change_is_rejected_with_identical_model_list(self):
        import yaml
        for field in ("fallbacks", "context_window_fallbacks"):
            with self.subTest(field=field):
                self.driver.config["modelRouting"] = {"mode": "preserve"}
                self.driver.values["platform"]["cetirun-ai-gateway"] = {}
                old = {"model_list": [{"model_name": "primary"}],
                       "router_settings": {field: [{"primary": ["backup"]}]}}
                candidate = {"model_list": old["model_list"]}
                def document(config):
                    return {"kind": "ConfigMap", "metadata": {"name": "gateway-config", "labels": {
                            "app.kubernetes.io/name": "cetirun-ai-gateway"}}, "data": {"config.yaml": yaml.safe_dump(config)}}
                self.driver.documents["platform"] = [document(candidate)]
                self.driver.kubectl.return_value = json.dumps(document(old))
                with self.assertRaisesRegex(ValueError, "model routes"):
                    self.driver.check_model_routing()
                self.driver.documents["platform"] = [document(old)]
                self.driver.check_model_routing()
                empty = {**candidate, "router_settings": {field: []}}
                self.assertEqual(DEPLOY.Deployment._model_routing(document(candidate)),
                                 DEPLOY.Deployment._model_routing(document(empty)))

    def test_same_model_failover_and_strategy_remain_effective(self):
        import yaml
        self.driver.config["modelRouting"] = {"mode": "preserve"}
        self.driver.values["platform"]["cetirun-ai-gateway"] = {}
        models = [{"model_name": "primary", "litellm_params": {"model": "azure/first"}},
                  {"model_name": "primary", "litellm_params": {"model": "azure/second"}}]
        def document(router):
            return {"kind": "ConfigMap", "metadata": {"name": "gateway-config", "labels": {
                    "app.kubernetes.io/name": "cetirun-ai-gateway"}},
                    "data": {"config.yaml": yaml.safe_dump({"model_list": models, "router_settings": router})}}
        live = document({"routing_strategy": "simple-shuffle", "enable_weighted_failover": True})
        self.driver.kubectl.return_value = json.dumps(live)
        for router in ({"routing_strategy": "simple-shuffle"},
                       {"routing_strategy": "simple-shuffle", "enable_weighted_failover": False},
                       {"routing_strategy": "least-busy", "enable_weighted_failover": True}):
            with self.subTest(router=router):
                self.driver.documents["platform"] = [document(router)]
                with self.assertRaisesRegex(ValueError, "model routes"):
                    self.driver.check_model_routing()
        self.driver.documents["platform"] = [live]
        self.driver.check_model_routing()
        self.assertEqual(DEPLOY.Deployment._model_routing(document({})),
                         DEPLOY.Deployment._model_routing(document({"enable_weighted_failover": False,
                                                                   "routing_strategy": "simple-shuffle"})))



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

    def test_external_postgresql_ignores_unused_bundled_storage_class(self):
        with tempfile.TemporaryDirectory() as temporary:
            scratch = Path(temporary)
            source = self.source_fixture(scratch)
            gateway = source["platform"]["values"]["cetirun-ai-gateway"]
            gateway["postgresql"]["bundled"]["enabled"] = False
            gateway["postgresql"]["bundled"]["persistence"]["storageClass"] = ""
            gateway["postgresql"]["urlSecret"] = {"existingSecret": "external-db", "key": "url"}
            config, values, roots = DEPLOY.infrastructure_configuration(source, self.lock, scratch / "compile")
            render = scratch / "render"
            render.mkdir()
            driver = DEPLOY.Deployment(config, values, render, published_artifacts=self.lock, chart_roots=roots)
            driver.render()
            refs = driver.references()
            self.assertNotIn(("StorageClass", "", "", ""), refs)
            self.assertIn(("Secret", config["platform"]["namespace"], "external-db", "url"), refs)
            self.assertFalse(any(doc.get("kind") == "StatefulSet" and "postgresql" in doc["metadata"]["name"]
                                 for doc in driver.documents["platform"]))
            # Even a nonempty unused bundled class must not be queried.
            values["platform"]["cetirun-ai-gateway"]["postgresql"]["bundled"]["persistence"]["storageClass"] = "unused-db-class"
            self.assertNotIn(("StorageClass", "", "unused-db-class", ""), driver.references())
            # The bundled case still requires explicit storage selection.
            gateway["postgresql"]["bundled"]["enabled"] = True
            with self.assertRaisesRegex(ValueError, "storageClass"):
                DEPLOY.infrastructure_configuration(source, self.lock, scratch / "invalid")

    def test_independent_gateway_does_not_require_platform_render_or_routing_reads(self):
        with tempfile.TemporaryDirectory() as temporary:
            scratch = Path(temporary)
            source = self.source_fixture(scratch)
            gateway = source["platform"]["values"]["cetirun-ai-gateway"]
            gateway["enabled"] = False
            gateway["postgresql"]["bundled"]["persistence"]["storageClass"] = ""
            config, values, roots = DEPLOY.infrastructure_configuration(source, self.lock, scratch / "compile")
            render = scratch / "render"
            render.mkdir()
            driver = DEPLOY.Deployment(config, values, render, published_artifacts=self.lock, chart_roots=roots)
            driver.render()
            driver.kubectl = Mock(side_effect=AssertionError("External Gateway must not be read"))
            driver.check_model_routing()
            driver.check_gateway_database_binding()
            with patch.object(ENV_DEPLOY, "collect_auxiliary_image_receipts", return_value={}):
                self.assertEqual(ENV_DEPLOY.audit_rendered_images(driver, self.lock), 4)
                incomplete_lock = copy.deepcopy(self.lock)
                del incomplete_lock["images"]["cetirun-ai-gateway"]
                with self.assertRaisesRegex(ENV_DEPLOY.Blocked, "artifacts_missing"):
                    ENV_DEPLOY.audit_rendered_images(driver, incomplete_lock)
                # Enabled owned components may not vanish from the render.
                values["platform"]["cetirun-ai-gateway"]["enabled"] = True
                with self.assertRaisesRegex(ENV_DEPLOY.Blocked, "images_missing"):
                    ENV_DEPLOY.audit_rendered_images(driver, self.lock)
            with self.assertRaisesRegex(ValueError, "model configuration is unavailable"):
                driver.check_model_routing()

    def test_rejects_missing_retained_runtime_chart_digest(self):
        with tempfile.TemporaryDirectory() as temporary:
            scratch = Path(temporary)
            source = self.source_fixture(scratch)
            del source["platform"]["values"]["cetirun-operator"]["cetirun"]["chart"]["digest"]
            with self.assertRaisesRegex(ValueError, "runtime chart.*digest"):
                DEPLOY.infrastructure_configuration(source, self.lock, scratch / "compile")


if __name__ == "__main__":
    unittest.main()
