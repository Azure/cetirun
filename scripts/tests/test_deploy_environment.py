# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Offline deployment safety checks; never connect to a cluster or registry."""

import argparse
import base64
import copy
from contextlib import redirect_stdout
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, patch

import yaml

from scripts import deploy_environment as deploy


def inputs():
    config = {"target": {"context": "fixture", "cluster": "fixture", "server": "https://cluster.example.invalid",
                         "clusterUID": "synthetic-cluster"}}
    kube = {"apiVersion": "v1", "kind": "Config", "current-context": "fixture",
            "contexts": [{"name": "fixture", "context": {"cluster": "fixture", "user": "deployer"}}],
            "clusters": [{"name": "fixture", "cluster": {"server": config["target"]["server"], "certificate-authority-data": "synthetic-ca"}}],
            "users": [{"name": "deployer", "user": {"token": "synthetic-secret"}}]}
    return config, kube


def baseline(image="mcr.microsoft.com/aks/ai-runtime/cetirun/hermes-gateway@sha256:" + "a" * 64):
    return {"objects": {"agents/fixture": {"uid": "original", "specHash": "sensitive-spec-hash", "generation": 3,
                                           "deleting": False, "ready": True, "bound": None, "agentType": "hermes",
                                           "image": image, "suspended": False}},
            "releases": {name: {"revision": "4", "chart": chart + "-0.2.1", "status": "deployed"}
                         for name, chart in (("platform", "cetirun-platform"), ("istio", "cetirun-istio"), ("broker", "cetirun-agent-broker"))}}


def operator_args(version="0.2.1", image="", chart_digest="sha256:" + "a" * 64):
    return ["--chart-repo-url=oci://mcr.microsoft.com/aks/ai-runtime/cetirun/helm/cetirun",
            "--chart-version=" + version, "--chart-digest=" + chart_digest,
            "--memory-context-enabled=true", "--memory-context-image=" + image]


class EnvironmentTests(unittest.TestCase):
    def runtime_driver(self, arguments=None):
        driver = Mock()
        driver.config = {"platform": {"namespace": "platform"}}
        driver.values = {"platform": {"global": {"memoryContext": {"image": ""}}}}
        driver.resource.return_value = {"uid": "operator-uid", "resourceVersion": "7"}
        driver.selected_fields.return_value = [json.dumps(operator_args() if arguments is None else arguments)]
        driver.kubectl.return_value = "[]"
        return driver

    def capture_driver(self, *, ready=False, pod_uid="pod-uid", pvc_items=None, manifest="", namespace_metadata=None):
        agent = {"metadata": {"name": "agent", "namespace": "", "uid": "agent-uid", "generation": 1},
                 "spec": {"agent": {"type": "hermes", "image": "mcr.example/hermes"}, "suspended": False},
                 "status": {}}
        if ready:
            agent["status"]["namespace"] = "cetirun-agent"
        pod = {"metadata": {"name": "gateway", "uid": pod_uid}, "spec": {"containers": [{"name": "gateway"}]},
               "status": {"containerStatuses": [{"name": "gateway", "ready": True}]}}
        namespace = {"metadata": {"name": "cetirun-agent", "uid": "namespace-uid",
                                   "annotations": {"cetirun.microsoft.com/agent-uid": "agent-uid"},
                                   "ownerReferences": [{"apiVersion": "cetirun.microsoft.com/v1alpha1",
                                                         "kind": "Agent", "uid": "agent-uid"}]}}
        if namespace_metadata is not None:
            namespace["metadata"] = namespace_metadata
        pvc_items = pvc_items if pvc_items is not None else []

        class Driver:
            config = {name: {"namespace": name, "release": name} for name in deploy.platform.CHARTS}
            documents = {name: [] for name in deploy.platform.CHARTS}
            helm_manifest = manifest

            def kubectl(self, *args):
                if args[:2] == ("get", "agents.cetirun.microsoft.com"):
                    return json.dumps({"items": [agent]})
                if args[:2] == ("get", "namespace"):
                    return json.dumps({"items": [namespace]})
                if args[:2] == ("get", "pods"):
                    return json.dumps({"items": [pod]})
                if args[:2] == ("get", "pvc"):
                    return json.dumps({"items": pvc_items})
                return json.dumps({"items": []})

            def helm(self, *args):
                if args[0] == "list":
                    return json.dumps([{"name": name, "revision": "1", "chart": "cetirun-" + name + "-0.1.0",
                                        "status": "deployed"} for name in deploy.platform.CHARTS])
                if args[:2] == ("get", "manifest"):
                    return self.helm_manifest
                raise AssertionError(args)

        return Driver()

    def test_capture_accounts_for_owned_namespace_before_agent_status_namespace(self):
        driver = self.capture_driver(ready=False)
        before = deploy.capture(driver)
        driver = self.capture_driver(ready=True)
        after = deploy.capture(driver)
        self.assertIn("pods/cetirun-agent/gateway", before["objects"])
        self.assertIn("pods/cetirun-agent/gateway", after["objects"])
        deploy.preservation(before, after)

    def test_capture_rejects_unattributed_candidate_namespace(self):
        # Portal may create the Agent but fail to patch namespace ownership.
        # A running Pod and Bound HOME must not disappear from the baseline.
        pvc = {"metadata": {"name": "home", "namespace": "cetirun-agent", "uid": "home-uid"},
               "spec": {}, "status": {"phase": "Bound"}}
        for ready in (False, True):
            for binding in ({}, {"ownerReferences": [{"apiVersion": "cetirun.microsoft.com/v1alpha1",
                                                      "kind": "Agent", "uid": "different-agent"}]}):
                with self.subTest(status_namespace=ready, binding=binding):
                    driver = self.capture_driver(ready=ready, pvc_items=[pvc], namespace_metadata={
                        "name": "cetirun-agent", "uid": "namespace-uid", **binding})
                    with self.assertRaisesRegex(deploy.Blocked, "agent_namespace_unattributed"):
                        deploy.capture(driver)

    def test_capture_rejects_conflicting_owner_and_annotation(self):
        driver = self.capture_driver(namespace_metadata={
            "name": "cetirun-agent", "uid": "namespace-uid",
            "annotations": {"cetirun.microsoft.com/agent-uid": "different-agent"},
            "ownerReferences": [{"apiVersion": "cetirun.microsoft.com/v1alpha1", "kind": "Agent", "uid": "agent-uid"}]})
        with self.assertRaisesRegex(deploy.Blocked, "agent_namespace_ownership_conflict"):
            deploy.capture(driver)

    def test_capture_does_not_adopt_unrelated_namespace(self):
        driver = self.capture_driver(namespace_metadata={"name": "unrelated", "uid": "other"})
        snapshot = deploy.capture(driver)
        self.assertFalse(any(key.startswith("pods/") for key in snapshot["objects"]))

    def test_capture_rejects_ambiguous_candidate_namespace_in_either_order(self):
        driver = self.capture_driver()
        kubectl = driver.kubectl
        first = json.loads(kubectl("get", "agents.cetirun.microsoft.com"))["items"][0]
        other = copy.deepcopy(first)
        other["metadata"].update(name="other-agent", uid="other-uid")
        other["status"]["namespace"] = "cetirun-agent"
        for agents in ([first, other], [other, first]):
            def read(*args):
                if args[:2] == ("get", "agents.cetirun.microsoft.com"):
                    return json.dumps({"items": agents})
                return kubectl(*args)
            driver.kubectl = read
            with self.subTest(order=[agent["metadata"]["name"] for agent in agents]):
                with self.assertRaisesRegex(deploy.Blocked, "agent_namespace_unattributed"):
                    deploy.capture(driver)

    def test_capture_detects_replaced_pod_and_scoped_pvc_drift(self):
        pvc = {"metadata": {"name": "home", "namespace": "cetirun-agent", "uid": "pvc-uid"},
               "spec": {}, "status": {"phase": "Bound"}}
        before = deploy.capture(self.capture_driver(pvc_items=[pvc]))
        replaced = deploy.capture(self.capture_driver(pod_uid="replacement", pvc_items=[pvc]))
        with self.assertRaisesRegex(deploy.Blocked, "resource_changed"):
            deploy.preservation(before, replaced)
        missing = deploy.capture(self.capture_driver(pvc_items=[]))
        with self.assertRaisesRegex(deploy.Blocked, "resource_set_changed"):
            deploy.preservation(before, missing)

    def test_capture_ignores_unrelated_pvc_creation(self):
        pvc = {"metadata": {"name": "unrelated", "namespace": "other", "uid": "other-pvc"},
               "spec": {}, "status": {"phase": "Bound"}}
        before = deploy.capture(self.capture_driver())
        after = deploy.capture(self.capture_driver(pvc_items=[pvc]))
        deploy.preservation(before, after)

    def test_capture_ignores_unrelated_pvc_in_installation_namespace(self):
        pvc = {"metadata": {"name": "unrelated", "namespace": "platform", "uid": "other-pvc"},
               "spec": {}, "status": {"phase": "Bound"}}
        before = deploy.capture(self.capture_driver())
        after = deploy.capture(self.capture_driver(pvc_items=[pvc]))
        deploy.preservation(before, after)

    def test_capture_keeps_manifest_and_statefulset_claims(self):
        manifest = yaml.safe_dump_all([
            {"apiVersion": "v1", "kind": "PersistentVolumeClaim",
             "metadata": {"name": "database", "namespace": "platform"}},
            {"apiVersion": "apps/v1", "kind": "StatefulSet",
             "metadata": {"name": "postgres", "namespace": "platform"},
             "spec": {"volumeClaimTemplates": [{"metadata": {"name": "data"}}]}},
            {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": "worker", "namespace": "platform"},
             "spec": {"template": {"spec": {"volumes": [{"name": "home", "persistentVolumeClaim": {"claimName": "worker-home"}}]}}}},
        ])
        claims = [
            {"metadata": {"name": "database", "namespace": "platform", "uid": "database-uid"}, "spec": {}, "status": {"phase": "Bound"}},
            {"metadata": {"name": "data-postgres-0", "namespace": "platform", "uid": "data-uid"}, "spec": {}, "status": {"phase": "Bound"}},
            {"metadata": {"name": "unrelated", "namespace": "platform", "uid": "other-uid"}, "spec": {}, "status": {"phase": "Bound"}},
            {"metadata": {"name": "data-postgres-unrelated", "namespace": "platform", "uid": "other-2"}, "spec": {}, "status": {"phase": "Bound"}},
            {"metadata": {"name": "worker-home", "namespace": "platform", "uid": "worker-uid"}, "spec": {}, "status": {"phase": "Bound"}},
        ]
        snapshot = deploy.capture(self.capture_driver(pvc_items=claims, manifest=manifest))
        self.assertIn("pvc/platform/database", snapshot["objects"])
        self.assertIn("pvc/platform/data-postgres-0", snapshot["objects"])
        self.assertNotIn("pvc/platform/unrelated", snapshot["objects"])
        self.assertNotIn("pvc/platform/data-postgres-unrelated", snapshot["objects"])
        self.assertIn("pvc/platform/worker-home", snapshot["objects"])
        for index in (0, 1, 4):
            with self.subTest(claim=claims[index]["metadata"]["name"]):
                changed = copy.deepcopy(claims)
                changed[index]["metadata"]["uid"] = "replacement"
                with self.assertRaisesRegex(deploy.Blocked, "resource_changed"):
                    deploy.preservation(snapshot, deploy.capture(self.capture_driver(pvc_items=changed, manifest=manifest)))
                changed.pop(index)
                with self.assertRaisesRegex(deploy.Blocked, "resource_set_changed"):
                    deploy.preservation(snapshot, deploy.capture(self.capture_driver(pvc_items=changed, manifest=manifest)))

    def test_planned_pvc_addition_passes_but_existing_claims_remain_protected(self):
        claim = {"apiVersion": "v1", "kind": "PersistentVolumeClaim",
                 "metadata": {"name": "oauth2-proxy-sessions", "namespace": "istio", "uid": "sessions-uid"},
                 "spec": {"storageClassName": "test"}, "status": {"phase": "Bound"}}
        driver = self.capture_driver()
        driver.documents["istio"] = [claim]
        planned = deploy.planned_claim_keys(driver)
        self.assertEqual(planned, {"pvc/istio/oauth2-proxy-sessions"})
        before = deploy.capture(driver)
        after_driver = self.capture_driver(pvc_items=[claim], manifest=yaml.safe_dump(claim))
        after_driver.documents["istio"] = [claim]
        after = deploy.capture(after_driver)
        with self.assertRaisesRegex(deploy.Blocked, "resource_set_changed"):
            deploy.preservation(before, after)
        deploy.preservation(before, after, allowed_additions=planned)
        # A candidate claim that already exists is preserved before Helm runs.
        existing_driver = self.capture_driver(pvc_items=[claim])
        existing_driver.documents["istio"] = [claim]
        existing = deploy.capture(existing_driver)
        self.assertIn("pvc/istio/oauth2-proxy-sessions", existing["objects"])
        changed = copy.deepcopy(after)
        changed["objects"]["pvc/istio/oauth2-proxy-sessions"]["uid"] = "replacement"
        with self.assertRaisesRegex(deploy.Blocked, "resource_changed"):
            deploy.preservation(existing, changed, allowed_additions=planned)
        with self.assertRaisesRegex(deploy.Blocked, "resource_set_changed"):
            deploy.preservation(existing, before, allowed_additions=planned)
        changed = copy.deepcopy(after)
        changed["objects"]["pvc/istio/unplanned"] = changed["objects"]["pvc/istio/oauth2-proxy-sessions"]
        with self.assertRaisesRegex(deploy.Blocked, "resource_set_changed"):
            deploy.preservation(before, changed, allowed_additions=planned)

    def test_planned_claims_exclude_external_references_hooks_and_unplanned_ordinals(self):
        driver = self.capture_driver()
        driver.documents["platform"] = [
            {"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": {"name": "db"},
             "spec": {"replicas": 2, "ordinals": {"start": 3},
                      "volumeClaimTemplates": [{"metadata": {"name": "data"}}]}},
            {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": "external"},
             "spec": {"template": {"spec": {"volumes": [{"persistentVolumeClaim": {"claimName": "external"}}]}}}},
            {"apiVersion": "v1", "kind": "PersistentVolumeClaim",
             "metadata": {"name": "hook", "annotations": {"helm.sh/hook": "post-upgrade"}}},
        ]
        self.assertEqual(deploy.planned_claim_keys(driver), {"pvc/platform/data-db-3", "pvc/platform/data-db-4"})
        driver.config["istio"]["mode"] = "external"
        driver.documents["istio"] = [{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": {"name": "external"}}]
        self.assertEqual(deploy.planned_claim_keys(driver), {"pvc/platform/data-db-3", "pvc/platform/data-db-4"})

    def test_statefulset_scale_up_does_not_adopt_a_same_name_claim(self):
        installed = {"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": {"name": "ingestor", "namespace": "platform"},
                     "spec": {"replicas": 1, "ordinals": {"start": 3},
                              "volumeClaimTemplates": [{"metadata": {"name": "data"}}]}}
        candidate = copy.deepcopy(installed)
        candidate["spec"]["replicas"] = 2
        retained = {"metadata": {"name": "data-ingestor-3", "namespace": "platform", "uid": "retained"},
                    "spec": {}, "status": {"phase": "Bound"}}
        collision = copy.deepcopy(retained)
        collision["metadata"].update(name="data-ingestor-4", uid="unrelated")
        # Matching labels or a numeric template-name suffix are not evidence
        # that an unreferenced retained volume belongs to this installation.
        collision["metadata"]["labels"] = {"app.kubernetes.io/managed-by": "Helm"}
        for claims in ([retained], [retained, collision]):
            driver = self.capture_driver(pvc_items=claims, manifest=yaml.safe_dump(installed))
            driver.documents["platform"] = [candidate]
            snap = deploy.capture(driver)
            if len(claims) == 1:
                deploy.verify_planned_claim_ownership(driver, snap)
            else:
                with self.assertRaisesRegex(deploy.Blocked, "existing_planned_claim_unattributed"):
                    deploy.verify_planned_claim_ownership(driver, snap)
        # An ordinal already used by the installed StatefulSet is known storage.
        installed["spec"]["replicas"] = 2
        driver.helm_manifest = yaml.safe_dump(installed)
        deploy.verify_planned_claim_ownership(driver, deploy.capture(driver))
        missing = self.capture_driver(pvc_items=[retained], manifest=yaml.safe_dump(installed))
        missing.documents["platform"] = [candidate]
        with self.assertRaisesRegex(deploy.Blocked, "installed_claim_missing"):
            deploy.verify_planned_claim_ownership(missing, deploy.capture(missing))

    def test_planned_claim_identity_survives_the_upgrade_to_acceptance_boundary(self):
        old = baseline()
        key = "pvc/istio/oauth2-proxy-sessions"
        created = copy.deepcopy(old)
        created["objects"][key] = {"uid": "first-uid", "specHash": "pending", "generation": None,
                                    "deleting": False, "ready": False, "bound": False,
                                    "claimContractHash": "storage-contract", "volumeName": ""}
        observed = copy.deepcopy(old)
        deploy.observe_preservation(old, observed, created, allowed_additions={key})
        bound = copy.deepcopy(created)
        bound["objects"][key].update(specHash="bound", volumeName="first-pv", bound=True)
        deploy.observe_preservation(old, observed, bound, allowed_additions={key})
        for field, value in (("uid", "replacement"), ("specHash", "changed"),
                             ("volumeName", "different-pv")):
            changed = copy.deepcopy(bound)
            changed["objects"][key][field] = value
            if field == "volumeName":
                changed["objects"][key]["specHash"] = "different-volume-spec"
            with self.subTest(field=field), patch.object(deploy, "capture", return_value=changed), \
                 self.assertRaisesRegex(deploy.Blocked, "preserved_resource_changed"):
                deploy.stable_acceptance(Mock(), old, allowed_additions={key}, observed=copy.deepcopy(observed))
        with self.assertRaisesRegex(deploy.Blocked, "resource_set_changed"):
            deploy.observe_preservation(old, observed, old, allowed_additions={key})

    def test_stability_pins_first_identity_of_planned_claim(self):
        old = baseline()
        added = copy.deepcopy(old)
        key = "pvc/istio/oauth2-proxy-sessions"
        added["objects"][key] = {"uid": "claim-uid", "specHash": "claim-spec", "generation": None,
                                 "deleting": False, "ready": False, "bound": True}
        replaced = copy.deepcopy(added)
        replaced["objects"][key]["uid"] = "replacement"
        ticks = iter(range(100))
        with patch.object(deploy, "capture", side_effect=[old, added, replaced]), \
             patch.object(deploy, "verify_control_readiness"), \
             patch.object(deploy.time, "monotonic", side_effect=lambda: next(ticks)), \
             patch.object(deploy.time, "sleep"), self.assertRaisesRegex(deploy.Blocked, "resource_changed"):
            deploy.stable_acceptance(Mock(), old, allowed_additions={key})
        ticks = iter(range(100))
        with patch.object(deploy, "capture", return_value=added), \
             patch.object(deploy, "verify_control_readiness"), \
             patch.object(deploy.time, "monotonic", side_effect=lambda: next(ticks)), \
             patch.object(deploy.time, "sleep"):
            self.assertEqual(deploy.stable_acceptance(Mock(), old, allowed_additions={key}, window=3), added)

    def test_planned_claim_can_bind_but_not_change_its_storage_contract(self):
        key = "pvc/istio/oauth2-proxy-sessions"
        claim = {"apiVersion": "v1", "kind": "PersistentVolumeClaim",
                 "metadata": {"name": "oauth2-proxy-sessions", "namespace": "istio", "uid": "claim-uid"},
                 "spec": {"storageClassName": "test", "accessModes": ["ReadWriteOnce"]}, "status": {"phase": "Pending"}}
        driver = self.capture_driver()
        driver.documents["istio"] = [claim]
        old = deploy.capture(driver)
        pending = deploy.capture(self.capture_driver(pvc_items=[claim], manifest=yaml.safe_dump(claim)))
        bound_claim = copy.deepcopy(claim)
        bound_claim["spec"]["volumeName"] = "pv-uid"
        bound_claim["status"]["phase"] = "Bound"
        bound = deploy.capture(self.capture_driver(pvc_items=[bound_claim], manifest=yaml.safe_dump(claim)))
        ticks = iter(range(100))
        with patch.object(deploy, "capture", side_effect=[pending] + [bound] * 10), \
             patch.object(deploy, "verify_control_readiness"), \
             patch.object(deploy.time, "monotonic", side_effect=lambda: next(ticks)), \
             patch.object(deploy.time, "sleep"):
            self.assertEqual(deploy.stable_acceptance(Mock(), old, allowed_additions={key}, window=3), bound)
        for mutate in (
            lambda value: value["metadata"].update(uid="replacement"),
            lambda value: value["spec"].update(storageClassName="changed"),
            lambda value: value["spec"].update(accessModes=["ReadWriteMany"]),
        ):
            changed_claim = copy.deepcopy(bound_claim)
            mutate(changed_claim)
            changed = deploy.capture(self.capture_driver(pvc_items=[changed_claim], manifest=yaml.safe_dump(claim)))
            ticks = iter(range(100))
            with patch.object(deploy, "capture", side_effect=[pending, changed]), \
                 patch.object(deploy, "verify_control_readiness"), \
                 patch.object(deploy.time, "monotonic", side_effect=lambda: next(ticks)), \
                 patch.object(deploy.time, "sleep"), self.assertRaisesRegex(deploy.Blocked, "resource_changed"):
                deploy.stable_acceptance(Mock(), old, allowed_additions={key})

    def test_embedded_credentials_are_private_and_unrelated_context_removed(self):
        config, kube = inputs()
        kube["users"].append({"name": "other", "user": {"exec": {"command": "untrusted"}}})
        with tempfile.TemporaryDirectory() as directory:
            config = deploy.credentials(Path(directory), yaml.safe_dump(config), base64.b64encode(yaml.safe_dump(kube).encode()).decode())
            path = Path(config["target"]["kubeconfig"])
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            result = yaml.safe_load(path.read_text())
            self.assertEqual(len(result["users"]), 1)
            self.assertNotIn("untrusted", path.read_text())

    def test_rejects_external_credentials_insecure_tls_and_wrong_cluster(self):
        for mutate in (
            lambda kube: kube["users"][0]["user"].update(exec={"command": "untrusted"}),
            lambda kube: kube["users"][0]["user"].update(tokenFile="/tmp/token"),
            lambda kube: kube["clusters"][0]["cluster"].update({"insecure-skip-tls-verify": True}),
            lambda kube: kube["clusters"][0]["cluster"].update(server="https://wrong.example.invalid"),
        ):
            config, kube = inputs()
            mutate(kube)
            with tempfile.TemporaryDirectory() as directory:
                with self.assertRaises(deploy.Blocked):
                    deploy.credentials(Path(directory), yaml.safe_dump(config), yaml.safe_dump(kube))
                self.assertFalse((Path(directory) / "kubeconfig").exists())

    def test_public_tls_bootstrap_is_out_of_scope(self):
        config, kube = inputs()
        config["publicTLS"] = {"enabled": True}
        with tempfile.TemporaryDirectory() as directory, self.assertRaisesRegex(deploy.Blocked, "bootstrap_out_of_scope"):
            deploy.credentials(Path(directory), yaml.safe_dump(config), yaml.safe_dump(kube))

    def test_nested_inline_secrets_are_rejected_but_secret_references_allowed(self):
        for value in ({"models": [{"api_key": "test-provider-key"}]}, {"oauth": {"clientSecret": "test-oauth"}},
                      {"cookieSecret": "test-cookie"}, {"tls": {"key": "test-key"}}, {"env": {"KEY": "-----BEGIN PRIVATE KEY-----"}}):
            with self.subTest(value=value), self.assertRaisesRegex(deploy.Blocked, "inline_application"):
                deploy.reject_inline_credentials({"platform": {"values": value}})
        deploy.reject_inline_credentials({"providerCredentials": {"existingSecret": "provider"},
                                          "models": [{"api_key": "os.environ/AZURE_KEY"}],
                                          "tls": {"secretName": "existing-tls"}})

    def test_existing_agent_registry_conflict_including_missing_pin(self):
        registry = "mcr.microsoft.com/aks/ai-runtime/cetirun"
        self.assertEqual(deploy.registry_conflicts(baseline(), registry), 0)
        self.assertEqual(deploy.registry_conflicts(baseline("registry.example.com/hermes-gateway:old"), registry), 1)
        self.assertEqual(deploy.registry_conflicts(baseline(""), registry), 1)

    def test_current_operator_projection_reads_runtime_args_and_rejects_drift(self):
        driver = self.runtime_driver()
        result = deploy.current_operator_arguments(driver)
        self.assertEqual(result["chart-version"], "0.2.1")
        self.assertIn('.containers[?(@.name=="manager")].args', driver.selected_fields.call_args.args[2][0])
        driver.resource.side_effect = [{"uid": "old", "resourceVersion": "7"}, {"uid": "new", "resourceVersion": "8"}]
        with self.assertRaisesRegex(deploy.Blocked, "operator_runtime_arguments_changed"):
            deploy.current_operator_arguments(driver)
        for invalid in ({}, ["--chart-version"], operator_args() + ["--chart-version=9.9.9"]):
            with self.subTest(invalid=invalid), self.assertRaises(deploy.Blocked):
                deploy.runtime_arguments(invalid)

    def test_current_memory_override_is_rejected_even_when_environment_omits_it(self):
        driver = self.runtime_driver(operator_args(image="registry.example.com/memory@sha256:" + "b" * 64))
        for runtime in ("openclaw", "hermes", "custom"):
            for suspended in (False, True):
                snap = baseline()
                snap["objects"]["agents/fixture"].update(agentType=runtime, suspended=suspended, chartVersion="0.1.0")
                with self.subTest(runtime=runtime, suspended=suspended), \
                     self.assertRaisesRegex(deploy.Blocked, "existing_memory_override_transition"):
                    deploy.verify_operator_runtime_transition(driver, snap, {"args": operator_args()})
        # Neither a default-only environment file nor a suspended Agent makes
        # the current nonempty Operator argument disappear from this check.
        deploy.verify_operator_runtime_transition(driver, snap, {"args": json.loads(driver.selected_fields.return_value[0])})
        deploy.verify_operator_runtime_transition(driver, {"objects": {}}, {})

    def test_memory_effective_enablement_change_is_rejected(self):
        driver = self.runtime_driver()
        candidate = [arg.replace("enabled=true", "enabled=false") for arg in operator_args()]
        with self.assertRaisesRegex(deploy.Blocked, "existing_memory_override_transition"):
            deploy.verify_operator_runtime_transition(driver, baseline(), {"args": candidate})

    def test_current_runtime_helper_overrides_cannot_change_or_disappear(self):
        driver = self.runtime_driver()
        override = {"initContainers": [{"name": "helper", "image": "registry.example.com/helper@sha256:" + "e" * 64}]}
        entries = [{"name": "CETIRUN_HELM_VALUES", "value": json.dumps(override)}]
        driver.kubectl.return_value = json.dumps(entries)
        snap = baseline()
        snap["objects"]["agents/fixture"].update(chartVersion="0.1.0", suspended=True)
        with self.assertRaisesRegex(deploy.Blocked, "existing_runtime_values_transition"):
            deploy.verify_operator_runtime_transition(driver, snap, {"args": operator_args()})
        deploy.verify_operator_runtime_transition(driver, snap, {"args": operator_args(), "env": entries})
        driver.kubectl.return_value = "[]"
        with self.assertRaisesRegex(deploy.Blocked, "existing_runtime_values_transition"):
            deploy.verify_operator_runtime_transition(driver, snap, {"args": operator_args(), "env": entries})

    def test_chart_repository_switch_is_blocked_before_version_shortcuts(self):
        for default in ("0.2.0", "0.2.1"):
            with self.subTest(default=default):
                current = operator_args(version=default)
                current[0] = "--chart-repo-url=oci://registry.example.com/original/helm/cetirun"
                driver = self.runtime_driver(current)
                snap = baseline()
                snap["objects"]["agents/fixture"].update(agentType="custom", chartVersion="0.1.0", suspended=True)
                with patch.object(deploy.artifacts, "_manifest") as manifest, self.assertRaisesRegex(
                        deploy.Blocked, "existing_chart_repository_transition_out_of_scope"):
                    deploy.verify_operator_runtime_transition(driver, snap, {"args": operator_args()})
                manifest.assert_not_called()

    def test_former_default_chart_digest_is_checked_for_retained_explicit_versions(self):
        driver = self.runtime_driver(operator_args(version="0.2.0"))
        candidate = {"args": operator_args()}
        snap = baseline()
        snap["objects"]["agents/fixture"].update(chartVersion="0.2.0", suspended=True)
        expected = "sha256:" + "a" * 64
        for runtime in ("hermes", "custom"):
            snap["objects"]["agents/fixture"]["agentType"] = runtime
            with patch.object(deploy.artifacts, "_manifest", return_value=(expected, {}, b"")) as manifest:
                deploy.verify_operator_runtime_transition(driver, snap, candidate)
                manifest.assert_called_once_with("aks/ai-runtime/cetirun/helm/cetirun", "0.2.0", "chart")
            with patch.object(deploy.artifacts, "_manifest", return_value=("sha256:" + "b" * 64, {}, b"")), \
                 self.assertRaisesRegex(deploy.Blocked, "retained_chart_tag_changed"):
                deploy.verify_operator_runtime_transition(driver, snap, candidate)
        with patch.object(deploy.artifacts, "_manifest", side_effect=deploy.artifacts.ArtifactError("registry unavailable")), \
             self.assertRaisesRegex(deploy.Blocked, "retained_chart_digest_unverified"):
            deploy.verify_operator_runtime_transition(driver, snap, candidate)
        driver.selected_fields.return_value = [json.dumps(operator_args(version="0.2.0", chart_digest=""))]
        with patch.object(deploy.artifacts, "_manifest") as manifest, self.assertRaisesRegex(deploy.Blocked, "retained_chart_digest_unverified"):
            deploy.verify_operator_runtime_transition(driver, snap, candidate)
        manifest.assert_not_called()

    def test_former_chart_tag_is_rechecked_and_unaffected_versions_are_not_resolved(self):
        driver = self.runtime_driver(operator_args(version="0.2.0"))
        candidate = {"args": operator_args()}
        snap = baseline()
        agent = snap["objects"]["agents/fixture"]
        agent["chartVersion"] = "0.2.0"
        with patch.object(deploy.artifacts, "_manifest", side_effect=[("sha256:" + "a" * 64, {}, b""),
                                                                   ("sha256:" + "b" * 64, {}, b"")]) as manifest:
            deploy.verify_operator_runtime_transition(driver, snap, candidate)
            with self.assertRaisesRegex(deploy.Blocked, "retained_chart_tag_changed"):
                deploy.verify_operator_runtime_transition(driver, snap, candidate)
            self.assertEqual(manifest.call_count, 2)
        agent["chartVersion"] = "0.1.0"
        with patch.object(deploy.artifacts, "_manifest") as manifest:
            deploy.verify_operator_runtime_transition(driver, snap, candidate)
        manifest.assert_not_called()
        agent["chartVersion"] = ""
        with self.assertRaisesRegex(deploy.Blocked, "implicit_agent_chart_upgrade"):
            deploy.verify_operator_runtime_transition(driver, snap, candidate)

    def test_republished_builtin_profile_checks_suspended_and_default_version_agents(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            driver = Mock()
            driver.values = {"platform": {"cetirun-operator": {"customRuntime": {"approvedImages": []}}}}
            old = baseline()
            agent = old["objects"]["agents/fixture"]
            new_image = agent["image"].replace("a" * 64, "b" * 64)
            lock = {"version": "0.2.1", "images": {"hermes-gateway": {"reference": new_image}}}
            profile = root / "agents/hermes/profile.yaml"
            profile.parent.mkdir(parents=True)
            profile.write_text("qualifiedImages: []\n")
            for version in ("", "0.2.1"):
                for suspended in (False, True):
                    agent.update(chartVersion=version, suspended=suspended)
                    with self.subTest(version=version, suspended=suspended), \
                         self.assertRaisesRegex(deploy.Blocked, "existing_agent_image_not_qualified"):
                        deploy.verify_existing_agent_images(driver, old, {"cetirun": root}, lock)
            # Another explicit version continues to use its own profile.
            agent["chartVersion"] = "0.2.0"
            deploy.verify_existing_agent_images(driver, old, {}, lock)
            agent["chartVersion"] = "0.2.1"
            profile.write_text(yaml.safe_dump({"qualifiedImages": [agent["image"]]}))
            deploy.verify_existing_agent_images(driver, old, {"cetirun": root}, lock)
            # Match the Operator's exact approval semantics, not digest aliases.
            agent["image"] = agent["image"].replace("@", ":0.2.1@")
            with self.assertRaisesRegex(deploy.Blocked, "existing_agent_image_not_qualified"):
                deploy.verify_existing_agent_images(driver, old, {"cetirun": root}, lock)
            agent["image"] = new_image
            deploy.verify_existing_agent_images(driver, old, {"cetirun": root}, lock)

    def test_existing_custom_approval_is_independent_from_candidate_digest(self):
        old = baseline()
        agent = old["objects"]["agents/fixture"]
        agent.update(agentType="custom", image="registry.example.com/pi@sha256:" + "a" * 64)
        next_image = agent["image"].replace("a" * 64, "b" * 64)
        driver = Mock()
        approved = [agent["image"], next_image]
        driver.values = {"platform": {"cetirun-operator": {"customRuntime": {"approvedImages": approved}}}}
        deploy.verify_existing_agent_images(driver, old, {}, {})
        approved.remove(agent["image"])
        with self.assertRaisesRegex(deploy.Blocked, "existing_custom_image_not_approved"):
            deploy.verify_existing_agent_images(driver, old, {}, {})

    def test_capture_associates_memory_init_with_the_owned_agent(self):
        driver = self.capture_driver()
        original = driver.kubectl
        def read(*args):
            result = json.loads(original(*args))
            if args[:2] == ("get", "pods"):
                result["items"][0]["spec"]["containers"][0]["name"] = "cetirun"
                result["items"][0]["spec"]["initContainers"] = [
                    {"name": "memory-assets", "image": "registry.example.com/memory@sha256:" + "a" * 64}]
            return json.dumps(result)
        driver.kubectl = read
        pod = deploy.capture(driver)["objects"]["pods/cetirun-agent/gateway"]
        self.assertEqual(pod["runtimeAgentUID"], "agent-uid")
        self.assertTrue(pod["isRuntimePod"])
        self.assertEqual(pod["memoryInitImages"], ["registry.example.com/memory@sha256:" + "a" * 64])

    def test_republished_chart_memory_init_is_checked_before_runtime_rollout(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            image = "registry.example.com/memory@sha256:" + "a" * 64
            (root / "values.yaml").write_text(yaml.safe_dump({"memoryContext": {"image": image}}))
            snap = baseline()
            agent = snap["objects"]["agents/fixture"]
            pod = {"runtimeAgentUID": agent["uid"], "isRuntimePod": True, "memoryInitImages": [image]}
            snap["objects"]["pods/runtime/main"] = pod
            # Auxiliary Pods have no Memory init and are not runtime candidates.
            snap["objects"]["pods/runtime/proxy"] = {"runtimeAgentUID": agent["uid"], "memoryInitImages": []}
            candidate = {"args": operator_args()}
            for runtime in ("openclaw", "hermes", "custom"):
                for version in ("", "0.2.1", "0.1.0"):
                    agent.update(agentType=runtime, chartVersion=version)
                    for previous in ([image], [image.replace("a" * 64, "b" * 64)], [""], [image, image], []):
                        pod["memoryInitImages"] = previous
                        with self.subTest(runtime=runtime, version=version, previous=previous):
                            if version == "0.1.0" or previous == [image]:
                                deploy.verify_memory_runtime_images(snap, candidate, {"cetirun": root}, {"version": "0.2.1"})
                            else:
                                with self.assertRaisesRegex(deploy.Blocked, "existing_memory_init_rollout"):
                                    deploy.verify_memory_runtime_images(snap, candidate, {"cetirun": root}, {"version": "0.2.1"})
            # An explicit unchanged override takes priority over chart defaults.
            agent["chartVersion"] = "0.2.1"
            pod["memoryInitImages"] = ["registry.example.com/override@sha256:" + "b" * 64]
            deploy.verify_memory_runtime_images(snap, {"args": operator_args(image=pod["memoryInitImages"][0])},
                                                {"cetirun": root}, {"version": "0.2.1"})

    def custom_home_fixture(self):
        snap = baseline()
        agent = snap["objects"]["agents/fixture"]
        agent.update(agentType="custom", name="fixture", chartVersion="0.1.0", suspended=True,
                     customOwnershipRequired=True)
        storage = {"storageClass": "retained", "storageSize": "1Gi", "accessMode": "ReadWriteOnce"}
        driver = self.runtime_driver()
        driver.values["platform"]["cetirun-operator"] = {"customRuntime": {"enabled": True, **storage}}
        record = {"uid": "record-uid", "resourceVersion": "7", "ownerReferences": [
            {"apiVersion": "cetirun.microsoft.com/v1alpha1", "kind": "Agent", "name": agent["name"], "uid": agent["uid"]}]}
        receipt = {"agentName": "fixture", "agentUID": agent["uid"], "namespace": "cetirun-fixture",
                   "namespaceUID": "namespace-uid", "release": "cetirun-fixture",
                   "home": "data-cetirun-fixture-0", "homeClaim": storage}
        driver.resource.side_effect = lambda kind, *args: {"uid": "namespace-uid"} if kind == "namespace" else record
        driver.selected_fields.return_value = [json.dumps(receipt), "true"]
        snap["objects"]["pvc/cetirun-fixture/data-cetirun-fixture-0"] = {
            "homeStorage": {"storageClass": "retained", "storageSize": "1024Mi", "accessModes": ["ReadWriteOnce"]}}
        return driver, snap, receipt, record

    def test_retained_custom_home_accepts_same_contract_and_rejects_changes(self):
        for suspended in (False, True):
            for field, changed in (("storageClass", "replacement"), ("storageSize", "2Gi"),
                                   ("accessMode", "ReadWriteMany"), ("enabled", False)):
                with self.subTest(suspended=suspended, field=field):
                    driver, snap, _, _ = self.custom_home_fixture()
                    snap["objects"]["agents/fixture"]["suspended"] = suspended
                    deploy.verify_custom_home_preservation(driver, snap)
                    config = driver.values["platform"]["cetirun-operator"]["customRuntime"]
                    config["storageSize"] = "1024Mi"
                    deploy.verify_custom_home_preservation(driver, snap)
                    config[field] = changed
                    with self.assertRaisesRegex(deploy.Blocked, "existing_custom_home_transition"):
                        deploy.verify_custom_home_preservation(driver, snap)
                    # A temporarily absent PVC does not erase the retained contract.
                    del snap["objects"]["pvc/cetirun-fixture/data-cetirun-fixture-0"]
                    with self.assertRaisesRegex(deploy.Blocked, "existing_custom_home_transition"):
                        deploy.verify_custom_home_preservation(driver, snap)

    def test_custom_home_receipts_require_identity_and_stable_narrow_reads(self):
        for mutation in ("missing", "mutable", "identity", "namespace", "home", "owner", "replaced", "pvc"):
            with self.subTest(mutation=mutation):
                driver, snap, receipt, record = self.custom_home_fixture()
                if mutation == "missing":
                    driver.resource.side_effect = None
                    driver.resource.return_value = None
                elif mutation == "mutable":
                    driver.selected_fields.return_value[1] = "false"
                elif mutation in ("identity", "namespace", "home"):
                    receipt[{"identity": "agentUID", "namespace": "namespaceUID", "home": "home"}[mutation]] = "foreign"
                    driver.selected_fields.return_value[0] = json.dumps(receipt)
                elif mutation == "owner":
                    record["ownerReferences"][0]["uid"] = "foreign"
                elif mutation == "replaced":
                    driver.resource.side_effect = [record, {**record, "uid": "replacement"}]
                else:
                    snap["objects"]["pvc/cetirun-fixture/data-cetirun-fixture-0"]["homeStorage"]["storageClass"] = "foreign"
                with self.assertRaises(deploy.Blocked):
                    deploy.verify_custom_home_preservation(driver, snap)
                if mutation != "missing":
                    self.assertEqual(driver.selected_fields.call_args.args[2], ("data.ownership", "immutable"))
        # Sandbox Custom and not-yet-provisioned Custom have no ordinary receipt.
        snap["objects"]["agents/fixture"]["customOwnershipRequired"] = False
        driver.resource.side_effect = None
        driver.resource.return_value = None
        deploy.verify_custom_home_preservation(driver, snap)

    def test_copilot_proxy_rollout_is_blocked_for_any_runtime_chart(self):
        old = baseline()
        old["objects"]["agents/fixture"]["chartVersion"] = "0.1.0"
        image = "registry.example.com/cetirun-ai-gateway@sha256:" + "a" * 64
        old["objects"]["pods/cetirun-agent/proxy"] = {"agentType": "", "copilotProxyImage": image}
        driver = Mock()
        driver.values = {"platform": {"cetirun-operator": {"customRuntime": {"approvedImages": []}}}}
        operator = {"name": "manager", "image": "operator:2", "env": []}
        driver.documents = {"platform": [{"kind": "Deployment", "metadata": {"name": "cetirun-operator"},
                                          "spec": {"template": {"spec": {"containers": [operator]}}}}]}
        with patch.object(deploy, "verify_cetiadmin_transition"), patch.object(deploy, "verify_operator_runtime_transition"):
            for candidate in ("", image.replace("a" * 64, "b" * 64), image):
                operator["env"] = [{"name": "AI_GATEWAY_COPILOT_PROXY_IMAGE", "value": candidate}]
                if candidate == image:
                    deploy.verify_runtime_preservation(driver, old, {}, {"version": "0.2.1"})
                else:
                    with self.assertRaisesRegex(deploy.Blocked, "copilot_proxy_rollout_out_of_scope"):
                        deploy.verify_runtime_preservation(driver, old, {}, {"version": "0.2.1"})

    def test_capture_reads_native_and_custom_copilot_pods_without_agent_owner_reference(self):
        driver = self.capture_driver()
        read = driver.kubectl
        image = "registry.example.com/proxy@sha256:" + "a" * 64
        def kubectl(*args):
            if args[:2] == ("get", "pods"):
                return json.dumps({"items": [{"metadata": {"name": "proxy", "uid": "proxy-uid", "labels": {
                    "app.kubernetes.io/component": "copilot-proxy", "app.kubernetes.io/name": "cetirun-copilot-proxy"}},
                    "spec": {"containers": [{"name": "proxy", "image": image}]}}]})
            return read(*args)
        driver.kubectl = kubectl
        snapshot = deploy.capture(driver)
        self.assertEqual(snapshot["objects"]["pods/cetirun-agent/proxy"]["copilotProxyImage"], image)

    def test_cetiadmin_coordinates_block_transitions_even_when_agent_is_suspended(self):
        driver = Mock()
        driver.config = {"platform": {"namespace": "platform"}}
        current = {"enabled": True, "instanceName": "synthetic-admin"}
        document = {"kind": "ConfigMap", "metadata": {"name": deploy.platform.CETIADMIN_SOURCE},
                    "data": {"config.json": json.dumps(current)}}
        driver.documents = {"platform": [document]}
        driver.resource.return_value = {"uid": "source-uid"}
        state = {"operatorImage": "operator:old", "runtimeVersion": "0.2.1",
                 "configHash": deploy.platform.cetiadmin_config_hash(current)}
        driver.cetiadmin_binding.return_value = ({}, {}, current, state)
        for phase in ("Active", "Ready", "Installing", "Disabled"):
            state["phase"] = phase
            with self.subTest(phase=phase), self.assertRaisesRegex(deploy.Blocked, "cetiadmin_installation_transition"):
                deploy.verify_cetiadmin_transition(driver, {"image": "operator:new"}, {"version": "0.2.1"})
        deploy.verify_cetiadmin_transition(driver, {"image": "operator:old"}, {"version": "0.2.1"})
        with self.assertRaisesRegex(deploy.Blocked, "cetiadmin_installation_transition"):
            deploy.verify_cetiadmin_transition(driver, {"image": "operator:old"}, {"version": "0.2.2"})
        document["data"]["config.json"] = json.dumps({**current, "platformReadNamespaces": ["other"]})
        with self.assertRaisesRegex(deploy.Blocked, "cetiadmin_installation_transition"):
            deploy.verify_cetiadmin_transition(driver, {"image": "operator:old"}, {"version": "0.2.1"})
        # Disable must already be verified; this check never performs it.
        document["data"]["config.json"] = json.dumps({**current, "enabled": False})
        deploy.verify_cetiadmin_transition(driver, {"image": "operator:new"}, {"version": "0.2.2"})
        driver.check_cetiadmin_stopped.assert_called_once_with(configuration=current)
        driver.check_cetiadmin_stopped.side_effect = ValueError("writers are running")
        with self.assertRaisesRegex(deploy.Blocked, "cetiadmin_stop_unverified"):
            deploy.verify_cetiadmin_transition(driver, {"image": "operator:new"}, {"version": "0.2.2"})

    def test_new_cetiadmin_installation_is_not_an_unplanned_agent_addition(self):
        driver = Mock()
        driver.config = {"platform": {"namespace": "platform"}}
        driver.resource.return_value = None
        document = {"kind": "ConfigMap", "metadata": {"name": deploy.platform.CETIADMIN_SOURCE},
                    "data": {"config.json": '{"enabled":true}'}}
        driver.documents = {"platform": [document]}
        with self.assertRaisesRegex(deploy.Blocked, "cetiadmin_installation_transition"):
            deploy.verify_cetiadmin_transition(driver, {}, {})
        document["data"]["config.json"] = '{"enabled":false}'
        deploy.verify_cetiadmin_transition(driver, {}, {})
        driver.resource.side_effect = [None, {"uid": "checkpoint-without-source"}]
        with self.assertRaisesRegex(deploy.Blocked, "cetiadmin_stop_unverified"):
            deploy.verify_cetiadmin_transition(driver, {}, {})

    def test_preservation_rejects_replacement_drift_loss_and_new_resources(self):
        old = baseline()
        for field, value in (("uid", "replacement"), ("generation", 4), ("specHash", "changed"), ("ready", False)):
            new = copy.deepcopy(old)
            new["objects"]["agents/fixture"][field] = value
            with self.subTest(field=field), self.assertRaises(deploy.Blocked):
                deploy.preservation(old, new)
        new = copy.deepcopy(old)
        new["objects"]["other"] = copy.deepcopy(new["objects"]["agents/fixture"])
        with self.assertRaises(deploy.Blocked):
            deploy.preservation(old, new)

    def test_old_failure_is_not_new_success_or_regression(self):
        old = baseline()
        old["objects"]["agents/fixture"]["ready"] = False
        deploy.preservation(old, copy.deepcopy(old))

    def test_delayed_controller_drift_is_not_accepted(self):
        old, changed = baseline(), baseline()
        changed["objects"]["agents/fixture"]["generation"] = 4
        ticks = iter(range(100))
        with patch.object(deploy, "capture", side_effect=[old, old, changed]), \
             patch.object(deploy, "verify_control_readiness"), \
             patch.object(deploy.time, "monotonic", side_effect=lambda: next(ticks)), \
             patch.object(deploy.time, "sleep"), self.assertRaisesRegex(deploy.Blocked, "resource_changed"):
            deploy.stable_acceptance(Mock(), old, window=60)

    def test_downgrade_and_unrecognized_existing_version_are_blocked(self):
        deploy.reject_downgrade(baseline()["releases"], "0.2.1")
        deploy.reject_downgrade(baseline()["releases"], "0.3.0")
        with self.assertRaisesRegex(deploy.Blocked, "downgrade"):
            deploy.reject_downgrade(baseline()["releases"], "0.2.0")
        with self.assertRaisesRegex(deploy.Blocked, "unrecognized"):
            deploy.reject_downgrade({"platform": {"chart": "unknown"}}, "0.2.1")

    def test_external_istio_is_observed_but_not_written_or_downgraded(self):
        driver = Mock()
        driver.config = {name: {"release": name, "namespace": "ns"} for name in deploy.platform.CHARTS}
        driver.config["istio"]["mode"] = "external"
        self.assertEqual(deploy.managed_release_names(driver), ("broker", "platform"))
        releases = {name: {"chart": "cetirun-" + name + "-0.3.0"} for name in deploy.platform.CHARTS}
        releases["istio"]["chart"] = "cetirun-istio-9.0.0"
        deploy.reject_downgrade(releases, "0.3.0", deploy.managed_release_names(driver))
        driver.documents = {name: [] for name in deploy.platform.CHARTS}
        driver.helm.return_value = ""
        deploy.compare_manifests(driver)
        self.assertEqual([call.args[2] for call in driver.helm.call_args_list], ["broker", "platform"])

    def test_control_plane_crash_after_rollout_cannot_pass_stable_window(self):
        ticks = iter(range(100))
        with patch.object(deploy, "capture", return_value=baseline()), \
             patch.object(deploy, "verify_control_readiness", side_effect=[None] + [deploy.Blocked("control_plane_not_ready")] * 20), \
             patch.object(deploy.time, "monotonic", side_effect=lambda: next(ticks)), \
             patch.object(deploy.time, "sleep"), self.assertRaisesRegex(deploy.Blocked, "stability_timeout"):
            deploy.stable_acceptance(Mock(), baseline(), window=5, timeout=20)

    def test_concurrent_helm_upgrade_cannot_pass_stable_window(self):
        old, changed = baseline(), baseline()
        changed["releases"]["platform"]["revision"] = "99"
        with patch.object(deploy, "capture", return_value=changed), self.assertRaisesRegex(deploy.Blocked, "helm_revision_changed"):
            deploy.stable_acceptance(Mock(), old, expected_releases=old["releases"])

    def test_control_plane_requires_observed_rollout_and_ready_pods(self):
        driver = Mock()
        driver.config = {"platform": {"namespace": "ns"}}
        driver.documents = {"platform": [{"kind": "Deployment", "metadata": {"name": "portal"},
                                         "spec": {"selector": {"matchLabels": {"app": "portal"}}}}]}
        live = {"metadata": {"generation": 2}, "spec": {"replicas": 1},
                "status": {"observedGeneration": 2, "readyReplicas": 1, "updatedReplicas": 1, "availableReplicas": 1}}
        pod = {"metadata": {}, "status": {"phase": "Running", "conditions": [{"type": "Ready", "status": "True"}]}}
        driver.kubectl.side_effect = [json.dumps(live), json.dumps({"items": [pod]})]
        deploy.verify_control_readiness(driver)
        for updated, ready in ((1, True), (2, False)):
            changed, changed_pod = copy.deepcopy(live), copy.deepcopy(pod)
            changed["status"]["observedGeneration"] = updated
            changed_pod["status"]["conditions"][0]["status"] = "True" if ready else "False"
            driver.kubectl.side_effect = [json.dumps(changed), json.dumps({"items": [changed_pod]})]
            with self.assertRaisesRegex(deploy.Blocked, "control_plane_not_ready"):
                deploy.verify_control_readiness(driver)

    def test_auth_redirect_rejects_errors_and_untrusted_destinations(self):
        for location, accepted in (("/oauth2/start?rd=%2F", True), ("https://portal.example.invalid/oauth2/sign_in", True),
                                   ("https://login.microsoftonline.com/common/oauth2/v2.0/authorize?redirect_uri=https%3A%2F%2Fportal.example.invalid%2Foauth2%2Fcallback", True),
                                   ("/error", False), ("//untrusted.example/oauth2/start", False),
                                   ("/oauth2/start?rd=https%3A%2F%2Funtrusted.example", False),
                                   ("https://login.microsoftonline.com/common/oauth2/v2.0/authorize?redirect_uri=https%3A%2F%2Funtrusted.example%2Fcallback", False),
                                   ("https://untrusted.example/oauth2/start", False), ("http://portal.example.invalid/oauth2/start", False)):
            response = Mock(status=302, headers={"Location": location})
            opened = Mock()
            opened.__enter__ = Mock(return_value=response)
            opened.__exit__ = Mock(return_value=False)
            opener = Mock()
            opener.open.return_value = opened
            with self.subTest(location=location), patch.object(deploy, "build_opener", return_value=opener):
                if accepted:
                    self.assertEqual(deploy.verify_entry("portal.example.invalid")["authenticationRedirect"], "PASS")
                else:
                    with self.assertRaisesRegex(deploy.Blocked, "redirect_invalid"):
                        deploy.verify_entry("portal.example.invalid")

    def test_unknown_first_party_rendered_image_is_rejected(self):
        registry = "mcr.microsoft.com/aks/ai-runtime/cetirun"
        driver = Mock()
        driver.documents = {"platform": [{"spec": {"containers": [{"image": registry + "/new-component:latest"}]}}]}
        with self.assertRaisesRegex(deploy.Blocked, "missing_from_inventory"):
            deploy.audit_rendered_images(driver, {"registry": registry, "images": {}})

    def test_image_verification_rejects_one_stale_among_correct_containers(self):
        reference = "mcr.microsoft.com/aks/ai-runtime/cetirun/cetirun-portal:0.2.1@sha256:" + "a" * 64
        lock = {"images": {"cetirun-portal": {"reference": reference, "digest": "sha256:" + "a" * 64, "platformDigest": "sha256:" + "b" * 64}}}
        driver = Mock()
        driver.config = {name: {"namespace": name} for name in deploy.platform.CHARTS}
        document = {"kind": "Deployment", "spec": {"selector": {"matchLabels": {"app": "portal"}},
                    "template": {"spec": {"containers": [{"name": "portal", "image": reference}]}}}}
        driver.documents = {name: [copy.deepcopy(document)] for name in deploy.platform.CHARTS}
        pod = {"metadata": {"labels": {"app": "portal"}}, "spec": {"containers": [{"name": "portal", "image": reference}]},
               "status": {"containerStatuses": [{"name": "portal", "imageID": "mcr.microsoft.com/x@sha256:" + "b" * 64}]}}
        driver.kubectl.return_value = json.dumps({"items": [pod]})
        self.assertEqual(deploy.verify_images(driver, lock), 3)
        stale = copy.deepcopy(pod)
        stale["spec"]["containers"][0]["image"] = "registry.example.com/cetirun-portal:old"
        driver.kubectl.return_value = json.dumps({"items": [pod, stale]})
        with self.assertRaisesRegex(deploy.Blocked, "pin_mismatch"):
            deploy.verify_images(driver, lock)

    def test_resource_removal_and_volume_changes_block_preview(self):
        driver = Mock()
        driver.config = {name: {"release": name, "namespace": "ns"} for name in deploy.platform.CHARTS}
        original = {"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": {"name": "db"},
                    "spec": {"volumeClaimTemplates": [{"spec": {"storageClassName": "original"}}]}}
        driver.helm.return_value = yaml.safe_dump(original)
        driver.documents = {name: [] for name in deploy.platform.CHARTS}
        with self.assertRaisesRegex(deploy.Blocked, "removes"):
            deploy.compare_manifests(driver)
        driver.documents = {name: [copy.deepcopy(original)] for name in deploy.platform.CHARTS}
        driver.documents["istio"][0]["spec"]["volumeClaimTemplates"][0]["spec"]["storageClassName"] = "changed"
        with self.assertRaisesRegex(deploy.Blocked, "storage_contract"):
            deploy.compare_manifests(driver)

    def test_plan_never_executes_and_apply_fails_before_write_on_conflict(self):
        cases = [(False, False, False, None), (True, True, False, None),
                 (True, False, False, None), (True, False, True, None)]
        cases += [(True, False, False, code) for code in (
            "existing_agent_image_not_qualified", "existing_copilot_proxy_rollout_out_of_scope",
            "cetiadmin_installation_transition_out_of_scope", "existing_memory_override_transition_out_of_scope",
            "existing_memory_init_rollout_out_of_scope", "existing_custom_home_transition_out_of_scope",
            "retained_chart_tag_changed", "tag_changed_after_plan")]
        for apply, conflict, failure, runtime_block in cases:
            with self.subTest(apply=apply, conflict=conflict, failure=failure, runtime_block=runtime_block), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                config, kube = inputs()
                config["domain"] = "portal.example.invalid"
                (root / "config").write_text(yaml.safe_dump(config))
                (root / "kube").write_text(yaml.safe_dump(kube))
                args = argparse.Namespace(work=root / "private", report=root / "report.json", source=root,
                                          release_tag="v0.2.1", apply=apply, config=root / "config", kubeconfig=root / "kube")
                lock = {"sourceSHA": "a" * 40, "version": "0.2.1", "registry": "mcr.microsoft.com/aks/ai-runtime/cetirun",
                        "images": {}, "charts": {}}
                driver = Mock()
                driver.config = {name: {"namespace": name, "release": name} for name in deploy.platform.CHARTS}
                driver.documents = {name: [] for name in deploy.platform.CHARTS}
                driver.scratch = root / "private" / "deployment"
                driver.resource.return_value = {"uid": "synthetic-cluster"}
                live_releases = copy.deepcopy(baseline()["releases"])
                def upgraded(name, values=None, *, expected_revision):
                    self.assertEqual(expected_revision, int(live_releases[name]["revision"]))
                    live_releases[name]["revision"] = str(int(live_releases[name]["revision"]) + 1)
                driver.upgrade.side_effect = upgraded
                if failure:
                    def partial_execute(*, before_upgrade, after_upgrade):
                        driver.upgrade("istio", expected_revision=before_upgrade("istio"))
                        after_upgrade("istio")
                        raise ValueError("private-upgrade-log")
                    driver.execute.side_effect = partial_execute
                else:
                    driver.execute.side_effect = lambda *, before_upgrade, after_upgrade: [
                        (driver.upgrade(name, expected_revision=before_upgrade(name)), after_upgrade(name))
                        for name in deploy.platform.CHARTS]
                snap = baseline("registry.example.com/hermes-gateway:old") if conflict else baseline()
                runtime_effect = deploy.Blocked(runtime_block) if runtime_block else None
                if runtime_block == "tag_changed_after_plan":
                    runtime_effect = [None, deploy.Blocked("retained_chart_tag_changed")]
                with patch.object(deploy.artifacts, "resolve", return_value=lock), \
                     patch.object(deploy.platform, "published_configuration", return_value=(config, {}, {})), \
                     patch.object(deploy.platform, "Deployment", return_value=driver), \
                     patch.object(deploy, "capture", return_value=snap), \
                     patch.object(deploy, "release_snapshot", side_effect=lambda *args, **kwargs: copy.deepcopy(live_releases)), \
                     patch.object(deploy, "stable_acceptance", return_value=snap), \
                     patch.object(deploy, "compare_manifests", return_value={}), \
                     patch.object(deploy, "audit_rendered_images", return_value=5), \
                     patch.object(deploy, "verify_runtime_chart", return_value="PASS"), \
                     patch.object(deploy, "verify_runtime_preservation", side_effect=runtime_effect) as runtime_check, \
                     patch.object(deploy, "verify_images", return_value=3), \
                     patch.object(deploy, "verify_entry", return_value={"tls": "PASS"}), redirect_stdout(io.StringIO()):
                    code = deploy.run(args)
                self.assertEqual(code, 1 if conflict or failure or runtime_block else 0)
                self.assertEqual(driver.execute.call_count, int(apply and not conflict and not runtime_block))
                if apply and not (conflict or failure or runtime_block):
                    self.assertEqual(runtime_check.call_count, 2 + len(deploy.platform.CHARTS))
                report = (root / "report.json").read_text()
                for secret in ("synthetic-secret", "synthetic-cluster", "sensitive-spec-hash", "agents/fixture", "portal.example.invalid", "private-upgrade-log"):
                    self.assertNotIn(secret, report)
                if failure:
                    result = json.loads(report)
                    self.assertEqual(result["status"], "FAIL")
                    self.assertEqual(result["afterReleases"]["istio"]["revision"], "5")
                    self.assertEqual(result["preservationAfterFailure"], "PASS")
                    self.assertIn({"stage": "upgrade_istio", "status": "PASS"}, result["stages"])

    def test_new_claim_is_pinned_between_upgrades_and_through_failure_reporting(self):
        for replacement_at in ("collision", "before_broker", "before_platform", "acceptance", "failed_broker",
                               "early_platform_claim", "platform_claim", None):
            with self.subTest(replacement_at=replacement_at), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                config, kube = inputs()
                config["domain"] = "portal.example.invalid"
                (root / "config").write_text(yaml.safe_dump(config))
                (root / "kube").write_text(yaml.safe_dump(kube))
                args = argparse.Namespace(work=root / "work", report=root / "report", source=root, release_tag="v0.2.1",
                                          apply=True, config=root / "config", kubeconfig=root / "kube")
                lock = {"sourceSHA": "a" * 40, "version": "0.2.1", "registry": "mcr.microsoft.com/aks/ai-runtime/cetirun",
                        "images": {}, "charts": {}}
                driver = Mock()
                driver.config = {name: {"namespace": name, "release": name} for name in deploy.platform.CHARTS}
                driver.documents = {name: [] for name in deploy.platform.CHARTS}
                driver.documents["istio"] = [{"apiVersion": "v1", "kind": "PersistentVolumeClaim",
                                              "metadata": {"name": "sessions", "namespace": "istio"}}]
                if replacement_at in ("early_platform_claim", "platform_claim"):
                    driver.documents["platform"] = [{"kind": "StatefulSet", "metadata": {"name": "ingestor"},
                        "spec": {"replicas": 1, "ordinals": {"start": 1}, "volumeClaimTemplates": [
                            {"metadata": {"name": "data"}}]}}]
                driver.scratch = root / "work/deployment"
                driver.resource.return_value = {"uid": "synthetic-cluster"}
                live = baseline()
                key = "pvc/istio/sessions"
                if replacement_at == "collision":
                    live["objects"][key] = {"uid": "unrelated", "specHash": "unrelated", "generation": None,
                                            "deleting": False, "ready": False, "bound": True}
                upgrades = []
                reads = 0
                def upgrade(name, values=None, *, expected_revision):
                    nonlocal reads
                    self.assertEqual(expected_revision, int(live["releases"][name]["revision"]))
                    upgrades.append(name)
                    reads = 0
                    if name == "istio":
                        live["objects"][key] = {"uid": "first-claim", "specHash": "pending", "generation": None,
                                                "deleting": False, "ready": False, "bound": False,
                                                "claimContractHash": "storage", "volumeName": ""}
                    if ((name == "broker" and replacement_at == "early_platform_claim") or
                            (name == "platform" and replacement_at == "platform_claim")):
                        live["objects"]["pvc/platform/data-ingestor-1"] = {
                            "uid": "new-ordinal", "specHash": "storage", "generation": None,
                            "deleting": False, "ready": False, "bound": True}
                    if name == "broker" and replacement_at == "failed_broker":
                        live["objects"][key]["uid"] = "replacement"
                        raise ValueError("synthetic broker failure")
                    live["releases"][name]["revision"] = str(expected_revision + 1)
                def capture(*unused, **kwargs):
                    nonlocal reads
                    reads += 1
                    if upgrades:
                        stage = upgrades[-1]
                        if reads >= 2:
                            live["objects"][key].update(specHash="bound", volumeName="first-pv", bound=True)
                            if ((stage == "istio" and replacement_at == "before_broker") or
                                    (stage == "broker" and replacement_at == "before_platform") or
                                    (stage == "platform" and replacement_at == "acceptance")):
                                live["objects"][key]["uid"] = "replacement"
                    return copy.deepcopy(live)
                driver.upgrade.side_effect = upgrade
                driver.execute.side_effect = lambda *, before_upgrade, after_upgrade: [
                        (driver.upgrade(name, expected_revision=before_upgrade(name)), after_upgrade(name))
                        for name in deploy.platform.CHARTS]
                # Exercise the real run/acceptance tracker without waiting for
                # Kubernetes or the stability interval. All external APIs are doubles.
                acceptance = deploy.stable_acceptance
                with patch.object(deploy.artifacts, "resolve", return_value=lock), \
                     patch.object(deploy.platform, "published_configuration", return_value=(config, {}, {})), \
                     patch.object(deploy.platform, "Deployment", return_value=driver), \
                     patch.object(deploy, "capture", side_effect=capture), \
                     patch.object(deploy, "release_snapshot", side_effect=lambda *a, **k: copy.deepcopy(live["releases"])), \
                     patch.object(deploy, "compare_manifests", return_value={}), \
                     patch.object(deploy, "audit_rendered_images", return_value=5), \
                     patch.object(deploy, "verify_runtime_chart", return_value="PASS"), \
                     patch.object(deploy, "verify_runtime_preservation"), \
                     patch.object(deploy, "verify_control_readiness"), \
                     patch.object(deploy, "verify_images", return_value=3), \
                     patch.object(deploy, "verify_entry", return_value={}), \
                     patch.object(deploy, "stable_acceptance", side_effect=lambda *a, **k: acceptance(*a, **k, window=0)), \
                     redirect_stdout(io.StringIO()):
                    code = deploy.run(args)
                self.assertEqual(code, int(replacement_at not in (None, "platform_claim")))
                if replacement_at == "collision":
                    self.assertEqual(upgrades, [])
                elif replacement_at == "before_broker":
                    self.assertEqual(upgrades, ["istio"])
                elif replacement_at in ("before_platform", "failed_broker", "early_platform_claim"):
                    self.assertEqual(upgrades, ["istio", "broker"])
                else:
                    self.assertEqual(upgrades, list(deploy.platform.CHARTS))
                report = json.loads(args.report.read_text())
                if replacement_at == "collision":
                    self.assertEqual(report["preservationAfterFailure"], "PASS")
                elif replacement_at and replacement_at != "platform_claim":
                    self.assertEqual(report["preservationAfterFailure"], "UNVERIFIED")

    def test_workflow_uses_only_manual_default_branch_and_report_allowlist(self):
        path = Path(__file__).resolve().parents[2] / ".github/workflows/deploy-environment.yml"
        workflow = yaml.safe_load(path.read_text())
        triggers = workflow.get("on", workflow.get(True))
        self.assertEqual(set(triggers), {"workflow_dispatch"})
        selected_environment = triggers["workflow_dispatch"]["inputs"]["environment"]
        self.assertEqual(selected_environment["type"], "environment")
        self.assertTrue(selected_environment["required"])
        self.assertNotIn("default", selected_environment)
        self.assertFalse(workflow["concurrency"]["cancel-in-progress"])
        self.assertEqual(workflow["permissions"], {"contents": "read"})
        job = workflow["jobs"]["deploy"]
        self.assertIn("default_branch", job["if"])
        self.assertEqual(job["environment"], "${{ inputs.environment }}")
        uploads = [step for step in job["steps"] if step.get("uses", "").startswith("actions/upload-artifact")]
        self.assertEqual(len(uploads), 1)
        self.assertEqual(uploads[0]["with"]["path"], "${{ runner.temp }}/cetirun-report/report.json")
        self.assertNotIn("azure/login", path.read_text())

    def test_runtime_chart_missing_digest_cannot_pass(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "values.yaml").write_text("openclaw:\n  image:\n    digest: ''\n")
            with self.assertRaisesRegex(deploy.Blocked, "runtime_chart_digest_pins_missing"):
                deploy.verify_runtime_chart({"cetirun": root}, {"images": {"openclaw-gateway": {"digest": "sha256:" + "a" * 64}}})

    @unittest.skipUnless(shutil.which("helm"), "Helm is required for published runtime packaging")
    def test_full_release_digest_package_passes_runtime_gate_without_source_edits(self):
        root = Path(__file__).resolve().parents[2]
        inventory = json.loads((root / "scripts/release-artifacts.json").read_text())
        version = yaml.safe_load((root / "app/cetirun-chart/Chart.yaml").read_text())["version"]
        registry = "mcr.microsoft.com/aks/ai-runtime/cetirun"
        digests = {entry["name"]: "sha256:" + f"{index:064x}" for index, entry in enumerate(inventory["images"], 1)}
        original = (root / "app/cetirun-chart/values.yaml").read_bytes()
        with tempfile.TemporaryDirectory() as directory:
            scratch = Path(directory)
            input_file = scratch / "digests.json"
            input_file.write_text(json.dumps(digests))
            env = {key: value for key, value in os.environ.items()
                   if key not in ("OPENCLAW_IMAGE_DIGEST", "HERMES_IMAGE_DIGEST")}
            env.update(REGISTRY=registry, VERSION=version, RELEASE_IMAGE_DIGESTS_FILE=str(input_file))
            subprocess.run(["python3", "-B", str(root / "scripts/cetirun-release.py"), "package-charts",
                            "--chart", "cetirun", "--package-dir", str(scratch / "packages")],
                           env=env, cwd=root, check=True, capture_output=True, text=True)
            archive = scratch / "packages" / f"cetirun-{version}.tgz"
            lock = {"registry": registry, "version": version,
                    "images": {name: {"digest": sha, "reference": f"{registry}/{name}:{version}@{sha}"} for name, sha in digests.items()},
                    "charts": {"cetirun": {"path": str(archive), "sha256": "sha256:" + deploy.hashlib.sha256(archive.read_bytes()).hexdigest()}}}
            roots = deploy.platform.prepare_published_charts(lock, scratch / "unpack")
            self.assertEqual(deploy.verify_runtime_chart(roots, lock), "PASS")
            self.assertEqual((root / "app/cetirun-chart/values.yaml").read_bytes(), original)
            (roots["cetirun"] / "agents/openclaw/profile.yaml").write_text("qualifiedImages: []\n")
            with self.assertRaisesRegex(deploy.Blocked, "profile_not_qualified"):
                deploy.verify_runtime_chart(roots, lock)

    def test_source_label_evidence_is_reported_without_local_paths(self):
        lock = {"sourceSHA": "a" * 40, "version": "0.2.1", "registry": "mcr.microsoft.com/aks/ai-runtime/cetirun",
                "images": {"one": {"reference": "example@sha256:" + "b" * 64, "digest": "sha256:" + "b" * 64,
                                    "platformDigest": "sha256:" + "b" * 64, "revisionVerified": False, "versionVerified": True}},
                "charts": {"chart": {"path": "/private/inputs/chart.tgz", "digest": "sha256:" + "c" * 64, "sha256": "sha256:" + "d" * 64}}}
        result = deploy.public_lock(lock)
        self.assertFalse(result["images"]["one"]["revisionVerified"])
        self.assertTrue(result["images"]["one"]["versionVerified"])
        self.assertNotIn("/private", json.dumps(result))


if __name__ == "__main__":
    unittest.main()
