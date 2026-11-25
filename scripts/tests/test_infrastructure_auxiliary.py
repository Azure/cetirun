#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Regression tests for retained auxiliary workload images and containers."""

import importlib.util
from pathlib import Path
import sys
from unittest.mock import Mock
import unittest
import yaml
import json
import hashlib
import base64
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
SPEC = importlib.util.spec_from_file_location("deploy_environment", ROOT / "scripts" / "deploy_environment.py")
DEPLOY = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(DEPLOY)


def workload(image, *, sidecar=True):
    containers = [{"name": "main", "image": image}]
    if sidecar:
        containers.append({"name": "auth-sdk", "image": "mcr.example/auth-sdk@sha256:" + "a" * 64})
    return {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": "broker"},
            "spec": {"selector": {"matchLabels": {"app": "broker"}},
                     "template": {"spec": {"containers": containers}}}}


class AuxiliaryImageTests(unittest.TestCase):
    def driver(self, old, new):
        driver = Mock()
        driver.config = {"_infrastructureOnly": True,
                         "istio": {"release": "istio", "namespace": "istio"},
                         "broker": {"release": "broker", "namespace": "broker"},
                         "platform": {"release": "platform", "namespace": "platform"}}
        driver.documents = {"istio": [new], "broker": [new], "platform": [new]}
        driver.helm.return_value = yaml.safe_dump_all([old])
        return driver

    def test_removed_auxiliary_sidecar_is_rejected(self):
        old = workload("mcr.example/broker@sha256:" + "b" * 64)
        new = workload("mcr.example/broker@sha256:" + "b" * 64, sidecar=False)
        with self.assertRaisesRegex(DEPLOY.Blocked, "auxiliary_container_transition_out_of_scope"):
            DEPLOY.compare_manifests(self.driver(old, new))

    def test_auxiliary_index_accepts_verified_platform_manifest(self):
        index = "sha256:" + "b" * 64
        platform = "sha256:" + "c" * 64
        DEPLOY.auxiliary_image_identity("mcr.example/broker@" + index,
                                        "docker.io/containerd@" + platform,
                                        {"digest": index, "platformDigest": platform})

    def test_auxiliary_index_rejects_unrelated_platform_manifest(self):
        with self.assertRaisesRegex(DEPLOY.Blocked, "auxiliary_image_identity_mismatch"):
            DEPLOY.auxiliary_image_identity(
                "mcr.example/broker@sha256:" + "b" * 64,
                "docker.io/containerd@sha256:" + "d" * 64,
                {"digest": "sha256:" + "b" * 64, "platformDigest": "sha256:" + "c" * 64})

    def test_tagged_auxiliary_image_uses_manifest_preservation(self):
        DEPLOY.auxiliary_image_identity("registry.example/broker:retained", "docker.io/containerd@sha256:" + "d" * 64)

    def test_receipt_resolves_index_and_platform_child(self):
        child = json.dumps({"schemaVersion": 2, "config": {"digest": "sha256:" + "e" * 64}}).encode()
        child_digest = "sha256:" + hashlib.sha256(child).hexdigest()
        index_document = {"schemaVersion": 2, "manifests": [{
            "digest": child_digest, "size": len(child),
            "platform": {"os": "linux", "architecture": "amd64"}}]}
        index = json.dumps(index_document).encode()
        index_digest = "sha256:" + hashlib.sha256(index).hexdigest()

        class Response:
            def __init__(self, body, digest):
                self.body, self.headers = body, {"Docker-Content-Digest": digest}
            def __enter__(self): return self
            def __exit__(self, *args): return None
            def read(self, limit): return self.body

        with patch.object(DEPLOY, "urlopen", side_effect=[Response(index, index_digest), Response(child, child_digest)]):
            receipt = DEPLOY.auxiliary_image_receipt("registry.example/library/cache:8@" + index_digest)
        self.assertEqual(receipt, {"digest": index_digest, "platformDigest": child_digest})

    def test_receipt_rejects_multiple_linux_platform_children(self):
        children = [{"digest": "sha256:" + value * 64, "size": 1,
                     "platform": {"os": "linux", "architecture": "amd64"}}
                    for value in ("a", "b")]
        body = json.dumps({"schemaVersion": 2, "manifests": children}).encode()
        digest = "sha256:" + hashlib.sha256(body).hexdigest()
        class Response:
            headers = {"Docker-Content-Digest": digest}
            def __enter__(self): return self
            def __exit__(self, *args): return None
            def read(self, limit): return body
        with patch.object(DEPLOY, "urlopen", return_value=Response()), self.assertRaisesRegex(
                DEPLOY.Blocked, "auxiliary_platform_manifest_ambiguous"):
            DEPLOY.auxiliary_image_receipt("registry.example/library/cache@" + digest)

    @staticmethod
    def private_receipts():
        child = json.dumps({"schemaVersion": 2, "config": {"digest": "sha256:" + "e" * 64}}).encode()
        child_digest = "sha256:" + hashlib.sha256(child).hexdigest()
        index = json.dumps({"schemaVersion": 2, "manifests": [{"digest": child_digest,
            "size": len(child), "platform": {"os": "linux", "architecture": "amd64"}}]}).encode()
        digest = "sha256:" + hashlib.sha256(index).hexdigest()
        return digest, child_digest, {digest: base64.b64encode(index).decode(),
                                     child_digest: base64.b64encode(child).decode()}

    def test_private_auxiliary_uses_verified_raw_receipts_without_registry_credentials(self):
        digest, child, receipts = self.private_receipts()
        image = "private.example.com/postgresql@" + digest
        document = workload(image, sidecar=False)
        driver = self.driver(document, document)
        driver.config["auxiliaryManifestReceipts"] = receipts
        with patch.object(DEPLOY, "urlopen", side_effect=AssertionError("no anonymous request needed")) as registry:
            actual = DEPLOY.collect_auxiliary_image_receipts(driver)
        registry.assert_not_called()
        self.assertEqual(actual[image], {"digest": digest, "platformDigest": child})
        DEPLOY.auxiliary_image_identity(image, "containerd://" + child, actual[image])
        with self.assertRaisesRegex(DEPLOY.Blocked, "identity_mismatch"):
            DEPLOY.auxiliary_image_identity(image, "containerd://sha256:" + "f" * 64, actual[image])

    def test_raw_receipts_reject_tampering_malformed_input_and_excessive_size(self):
        digest, _, receipts = self.private_receipts()
        oversized = b" " * (DEPLOY._AUX_RECEIPTS_MAX_BYTES + 1)
        oversized_digest = "sha256:" + hashlib.sha256(oversized).hexdigest()
        cases = [[], {digest: "invalid base64!"}, {digest: base64.b64encode(b"{}").decode()},
                 {digest: {"platformDigest": "sha256:" + "f" * 64}},
                 {oversized_digest: base64.b64encode(oversized).decode()}]
        for receipt in cases:
            with self.subTest(receipt_type=type(receipt).__name__), self.assertRaisesRegex(
                    DEPLOY.Blocked, "auxiliary_manifest_receipts_invalid"):
                DEPLOY.auxiliary_manifest_receipts({"auxiliaryManifestReceipts": receipt})
        self.assertEqual(len(DEPLOY.auxiliary_manifest_receipts({"auxiliaryManifestReceipts": receipts})), 2)

    def test_private_index_without_child_evidence_still_requires_registry_verification(self):
        digest, child, receipts = self.private_receipts()
        del receipts[child]
        verified = DEPLOY.auxiliary_manifest_receipts({"auxiliaryManifestReceipts": receipts})
        with patch.object(DEPLOY, "urlopen", side_effect=DEPLOY.HTTPError(
                "https://private.example.com", 401, "Unauthorized", {}, None)) as registry:
            with self.assertRaisesRegex(DEPLOY.Blocked, "auxiliary_registry_manifest_unavailable"):
                DEPLOY.auxiliary_image_receipt("private.example.com/postgresql@" + digest, verified)
        self.assertEqual(registry.call_count, 1)

    def test_manifest_receipt_does_not_authorize_changing_a_retained_image(self):
        digest, _, receipts = self.private_receipts()
        old = workload("private.example.com/postgresql@sha256:" + "f" * 64, sidecar=False)
        new = workload("private.example.com/postgresql@" + digest, sidecar=False)
        driver = self.driver(old, new)
        driver.config["auxiliaryManifestReceipts"] = receipts
        with self.assertRaisesRegex(DEPLOY.Blocked, "auxiliary_image_transition_out_of_scope"):
            DEPLOY.compare_manifests(driver)


if __name__ == "__main__":
    unittest.main()
