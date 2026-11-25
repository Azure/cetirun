#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""An independent proxy release is retained without becoming a runtime default."""

import copy
import json
import unittest
from unittest.mock import Mock, patch

from scripts import deploy_environment as deploy


class ProxyOwnershipTests(unittest.TestCase):
    def setUp(self):
        self.driver = Mock()
        self.driver.config = {"_infrastructureOnly": True,
                              "istio": {"release": "istio", "namespace": "istio"},
                              "broker": {"release": "broker", "namespace": "broker"},
                              "platform": {"release": "platform", "namespace": "platform"}}
        image = "registry.example.com/proxy:old@sha256:" + "b" * 64
        spec = {"containers": [{"name": "proxy", "image": image}]}
        def meta(name, uid):
            return {"name": name, "uid": uid, "namespace": "agent-ns"}
        def owner(kind, name, uid):
            return {"apiVersion": "apps/v1", "kind": kind, "controller": True, "name": name, "uid": uid}
        self.pod = {"metadata": {**meta("proxy-pod", "pod-uid"), "ownerReferences": [owner("ReplicaSet", "proxy-rs", "rs-uid")]},
                    "spec": copy.deepcopy(spec)}
        self.rs = {"metadata": {**meta("proxy-rs", "rs-uid"), "ownerReferences": [owner("Deployment", "proxy-deploy", "dep-uid")]},
                   "spec": {"template": {"spec": copy.deepcopy(spec)}}}
        self.controller = {"apiVersion": "apps/v1", "kind": "Deployment",
                           "metadata": {**meta("proxy-deploy", "dep-uid"), "annotations": {
                               "meta.helm.sh/release-name": "foreign-release", "meta.helm.sh/release-namespace": "agent-ns"}},
                           "spec": {"template": {"spec": copy.deepcopy(spec)}}}
        self.manifest = copy.deepcopy(self.controller)
        self.responses = {"pod": self.pod, "replicaset": self.rs, "deployment": self.controller}
        self.driver.kubectl.side_effect = lambda operation, resource, *args: json.dumps(self.responses[resource])
        self.driver.helm.side_effect = lambda *args: json.dumps(self.manifest)
        self.record = {"uid": "pod-uid", "podNamespace": "agent-ns", "podName": "proxy-pod",
                       "specHash": deploy.digest(self.pod["spec"]), "copilotProxyImage": image,
                       "generation": None, "deleting": False, "ready": True, "bound": None,
                       "runtimeAgentUID": "agent-uid"}
        self.snapshot = {"objects": {"agents/fixture": {"name": "fixture", "agentType": "openclaw",
                                                       "helmReleaseName": "cetirun-fixture"}}}

    def verify(self):
        return deploy.verify_foreign_copilot_proxy(self.driver, self.record, self.snapshot)

    def test_foreign_manifest_owner_is_accepted_despite_current_runtime_labels(self):
        # A shared label must neither confer runtime ownership nor prevent
        # preserving a separately owned release.
        self.pod["metadata"]["labels"] = {"app.kubernetes.io/instance": "cetirun-fixture"}
        self.assertTrue(self.verify())
        self.driver.helm.assert_called_once_with("get", "manifest", "foreign-release", "--namespace", "agent-ns")

    def test_each_owner_uid_and_deletion_is_checked(self):
        for resource in self.responses.values():
            for field, value in (("uid", "replacement"), ("deletionTimestamp", "2026-01-01T00:00:00Z")):
                with self.subTest(resource=resource["metadata"]["name"], field=field):
                    meta = copy.deepcopy(resource["metadata"])
                    resource["metadata"][field] = value
                    with self.assertRaisesRegex(deploy.Blocked, "ownership_changed"):
                        self.verify()
                    resource["metadata"] = meta

    def test_labels_or_non_controller_owner_cannot_establish_ownership(self):
        for resource in (self.pod, self.rs):
            owners = resource["metadata"]["ownerReferences"]
            for replacement in ([], [{**owners[0], "controller": False}], [owners[0], owners[0]]):
                resource["metadata"]["ownerReferences"] = replacement
                with self.assertRaisesRegex(deploy.Blocked, "ownership_unavailable"):
                    self.verify()
            resource["metadata"]["ownerReferences"] = owners

    def test_managed_and_both_agent_release_identities_cannot_be_exempted(self):
        self.snapshot["objects"]["agents/fixture"]["helmReleaseName"] = "recorded-release"
        for release in ("platform", "broker", "istio", "cetirun-fixture", "recorded-release"):
            with self.subTest(release=release):
                self.controller["metadata"]["annotations"]["meta.helm.sh/release-name"] = release
                with self.assertRaisesRegex(deploy.Blocked, "ownership_unavailable"):
                    self.verify()
        self.driver.helm.assert_not_called()

    def test_expected_proxy_name_cannot_be_hidden_by_foreign_annotations(self):
        for agent_name in ("fixture", "a" * 60):
            self.snapshot["objects"]["agents/fixture"]["name"] = agent_name
            raw = "cetirun-" + agent_name + "-copilot-proxy"
            for name in (raw, raw[:63].rstrip("-")):
                self.rs["metadata"]["ownerReferences"][0]["name"] = name
                self.controller["metadata"]["name"] = name
                with self.assertRaisesRegex(deploy.Blocked, "ownership_unavailable"):
                    self.verify()
        self.driver.helm.assert_not_called()

    def test_manifest_must_contain_exactly_one_matching_deployment(self):
        for field, replacement in (("name", "unrelated"), ("namespace", "other")):
            old = self.manifest["metadata"][field]
            self.manifest["metadata"][field] = replacement
            with self.assertRaisesRegex(deploy.Blocked, "ownership_unavailable"):
                self.verify()
            self.manifest["metadata"][field] = old
        for raw in ("", json.dumps(self.manifest) + "\n---\n" + json.dumps(self.manifest)):
            self.driver.helm.side_effect = lambda *args: raw
            with self.assertRaisesRegex(deploy.Blocked, "ownership_unavailable"):
                self.verify()

    def test_manifest_and_controller_images_cannot_drift_even_to_an_equivalent_tag(self):
        for document in (self.rs, self.controller, self.manifest):
            container = document["spec"]["template"]["spec"]["containers"][0]
            original = container["image"]
            container["image"] = original.replace(":old@", ":new@")
            with self.assertRaisesRegex(deploy.Blocked, "ownership_changed"):
                self.verify()
            container["image"] = original

    def test_disabled_operator_proxy_still_checks_foreign_workload_ownership(self):
        self.snapshot["objects"]["pods/agent-ns/proxy-pod"] = self.record
        with patch.object(deploy, "verify_existing_agent_images"), \
             patch.object(deploy, "verify_operator_runtime_transition"), \
             patch.object(deploy, "published_runtime_agents", return_value=([], False)), \
             patch.object(deploy, "verify_custom_home_preservation"), \
             patch.object(deploy, "verify_cetiadmin_transition"), \
             patch.object(deploy, "rendered_operator", return_value={"env": []}):
            deploy.verify_runtime_preservation(self.driver, self.snapshot, {}, {})
            self.driver.helm.assert_called_once_with("get", "manifest", "foreign-release", "--namespace", "agent-ns")
            self.controller["metadata"]["annotations"]["meta.helm.sh/release-name"] = "cetirun-fixture"
            with self.assertRaisesRegex(deploy.Blocked, "ownership_unavailable"):
                deploy.verify_runtime_preservation(self.driver, self.snapshot, {}, {})
            self.controller["metadata"]["annotations"]["meta.helm.sh/release-name"] = "foreign-release"
            self.pod["metadata"]["ownerReferences"] = []
            with self.assertRaisesRegex(deploy.Blocked, "ownership_unavailable"):
                deploy.verify_runtime_preservation(self.driver, self.snapshot, {}, {})

    def test_foreign_pod_is_still_protected_by_preservation(self):
        self.assertTrue(self.verify())
        before = {"objects": {"pods/agent-ns/proxy-pod": self.record}}
        for field in ("uid", "specHash"):
            after = copy.deepcopy(before)
            after["objects"]["pods/agent-ns/proxy-pod"][field] = "changed"
            with self.assertRaisesRegex(deploy.Blocked, "preserved_resource_changed"):
                deploy.preservation(before, after)


if __name__ == "__main__":
    unittest.main()
