#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Retained non-default runtime versions still resolve the published tag."""

import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

from scripts import deploy_environment as deploy


class RetainedRuntimeTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.registry = "mcr.microsoft.com/aks/ai-runtime/cetirun"
        self.pin = "sha256:" + "a" * 64
        self.old_image = self.registry + "/hermes-gateway@sha256:" + "b" * 64
        self.new_image = self.registry + "/hermes-gateway@sha256:" + "c" * 64
        self.memory = self.registry + "/cetirun-memory-context@sha256:" + "d" * 64
        self.lock = {"registry": self.registry, "version": "0.2.2", "charts": {"cetirun": {"digest": self.pin}},
                     "images": {"hermes-gateway": {"reference": self.new_image}}}
        self.manager = {"name": "manager", "args": ["--chart-repo-url=oci://" + self.registry + "/helm/cetirun",
                         "--chart-version=0.2.1", "--chart-digest=sha256:" + "e" * 64,
                         "--memory-context-enabled=true", "--memory-context-image="], "env": []}
        self.driver = Mock()
        self.driver.config = {"_infrastructureOnly": True, "platform": {"namespace": "platform"}}
        self.driver.values = {"platform": {"cetirun-operator": {"customRuntime": {"approvedImages": []}}}}
        self.driver.documents = {"platform": [{"kind": "Deployment", "metadata": {"name": "cetirun-operator"},
                                   "spec": {"template": {"spec": {"containers": [self.manager]}}}}]}
        self.agent = {"uid": "retained", "agentType": "hermes", "image": self.old_image,
                      "chartVersion": "0.2.2", "suspended": True}
        self.snapshot = {"objects": {"agent": self.agent}}
        profile = self.root / "agents/hermes/profile.yaml"
        profile.parent.mkdir(parents=True)
        profile.write_text("qualifiedImages: []\n")
        (self.root / "values.yaml").write_text(json.dumps({"memoryContext": {"image": self.memory}}))

    def test_republished_explicit_version_rejects_old_image_even_when_suspended(self):
        with patch.object(deploy.artifacts, "_manifest", return_value=(self.pin, {}, b"")), \
             self.assertRaisesRegex(deploy.Blocked, "existing_agent_image_not_qualified"):
            deploy.verify_existing_agent_images(self.driver, self.snapshot, {"cetirun": self.root}, self.lock)

    def test_repository_and_retained_default_digest_select_the_actual_archive(self):
        self.manager["args"][0] = "--chart-repo-url=oci://retained.example.com/helm/cetirun"
        self.assertEqual(deploy.published_runtime_agents(self.driver, self.snapshot, self.lock), ([], False))
        self.manager["args"][0] = "--chart-repo-url=oci://" + self.registry + "/helm/cetirun"
        self.manager["args"][1] = "--chart-version=0.2.2"
        self.assertEqual(deploy.published_runtime_agents(self.driver, self.snapshot, self.lock), ([], False))
        self.manager["args"][2] = "--chart-digest=" + self.pin
        self.assertEqual(deploy.published_runtime_agents(self.driver, self.snapshot, self.lock), ([self.agent], False))

    def test_mutable_tag_is_rechecked_before_each_write(self):
        self.agent["image"] = self.new_image
        with patch.object(deploy.artifacts, "_manifest", side_effect=[(self.pin, {}, b""), ("sha256:" + "f" * 64, {}, b"")]) as read:
            deploy.verify_existing_agent_images(self.driver, self.snapshot, {"cetirun": self.root}, self.lock)
            with self.assertRaisesRegex(deploy.Blocked, "retained_chart_tag_changed"):
                deploy.verify_existing_agent_images(self.driver, self.snapshot, {"cetirun": self.root}, self.lock)
            self.assertEqual(read.call_count, 2)

    def test_changed_memory_and_missing_suspended_baseline_fail_before_apply(self):
        with self.assertRaisesRegex(deploy.Blocked, "retained_runtime_memory_unverified"):
            deploy.verify_memory_runtime_images(self.snapshot, self.manager, {"cetirun": self.root}, self.lock,
                                                affected_agents=[self.agent])
        self.snapshot["objects"]["pod"] = {"runtimeAgentUID": "retained", "isRuntimePod": True,
                                             "memoryInitImages": ["retained.example.com/memory@sha256:" + "f" * 64]}
        with self.assertRaisesRegex(deploy.Blocked, "existing_memory_init_rollout_out_of_scope"):
            deploy.verify_memory_runtime_images(self.snapshot, self.manager, {"cetirun": self.root}, self.lock,
                                                affected_agents=[self.agent])

    def test_explicit_memory_override_still_requires_old_chart_capability_evidence(self):
        self.manager["args"][-1] = "--memory-context-image=" + self.memory
        with self.assertRaisesRegex(deploy.Blocked, "retained_runtime_memory_unverified"):
            deploy.verify_memory_runtime_images(self.snapshot, self.manager, {"cetirun": self.root}, self.lock,
                                                affected_agents=[self.agent])
        self.snapshot["objects"]["pod"] = {"runtimeAgentUID": "retained", "isRuntimePod": True,
                                             "memoryInitImages": [self.memory]}
        deploy.verify_memory_runtime_images(self.snapshot, self.manager, {"cetirun": self.root}, self.lock,
                                            affected_agents=[self.agent])

    def test_atomic_snapshot_cannot_combine_arguments_and_environment_from_different_revisions(self):
        candidate = copy.deepcopy(self.driver.documents["platform"][0])
        candidate["metadata"].update(uid="operator", resourceVersion="1")
        current = copy.deepcopy(candidate)
        current["spec"]["template"]["spec"]["containers"][0]["env"] = [{"name": "AI_GATEWAY_DEFAULT_MODEL", "value": "unexpected"}]
        later = copy.deepcopy(candidate)
        later["metadata"]["resourceVersion"] = "2"
        later["spec"]["template"]["spec"]["containers"][0]["args"][1] = "--chart-version=0.1.0"
        self.driver.kubectl.side_effect = [json.dumps(current), json.dumps(later)]
        with self.assertRaisesRegex(deploy.Blocked, "existing_runtime_environment_transition_out_of_scope"):
            deploy.verify_operator_runtime_transition(self.driver, {"objects": {}}, self.manager)
        self.assertEqual(self.driver.kubectl.call_count, 1)


if __name__ == "__main__":
    unittest.main()
