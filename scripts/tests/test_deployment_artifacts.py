# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

import importlib.util
import json
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch
import io
import hashlib


ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("deployment_artifacts", ROOT / "scripts/deployment_artifacts.py")
ARTIFACTS = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(ARTIFACTS)


class DeploymentArtifactTests(unittest.TestCase):
    def test_requires_semver_tag(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(ARTIFACTS.ArtifactError, "release tag"):
                ARTIFACTS.resolve(ROOT, Path(directory), "latest")

    def test_rejects_unsafe_chart_member(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "bad.tgz"
            with tarfile.open(path, "w:gz") as archive:
                info = tarfile.TarInfo("../../escape")
                info.size = 0
                archive.addfile(info)
            with self.assertRaisesRegex(ARTIFACTS.ArtifactError, "unsafe"):
                ARTIFACTS._safe_chart_members(path)

    def test_resolves_inventory_and_keeps_exact_chart_paths(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "source"
            source.mkdir()
            (source / "scripts").mkdir()
            (source / "app/cetirun-chart").mkdir(parents=True)
            (source / "app/cetirun-chart/Chart.yaml").write_text("version: 0.2.1\n")
            inventory = {"schemaVersion": 1, "images": [{"name": "cetirun-operator"}],
                         "charts": [{"name": "chart", "path": "app/cetirun-chart"}]}
            (source / "scripts/release-artifacts.json").write_text(json.dumps(inventory))
            chart_file = Path(directory) / "chart-0.2.1.tgz"
            chart_file.write_bytes(b"immutable chart")
            image = {"reference": "mcr.example/one:0.2.1@sha256:" + "a" * 64,
                     "digest": "sha256:" + "a" * 64, "platformDigest": "sha256:" + "b" * 64,
                     "configDigest": "sha256:" + "c" * 64, "revision": "d" * 40,
                     "chartDigests": {"chart": "sha256:" + "e" * 64}, "imageDigests": {}}
            with patch.object(subprocess, "check_output", side_effect=["", ("d" * 40) + "\n"]), \
                 patch.object(subprocess, "run", return_value=subprocess.CompletedProcess([], 0, stdout=("d" * 40) + "\n")), \
                 patch.object(ARTIFACTS, "_image", return_value=image), \
                 patch.object(ARTIFACTS, "_chart", return_value={"path": str(chart_file), "digest": "sha256:" + "d" * 64, "sha256": "sha256:" + "e" * 64}) as chart_download:
                result = ARTIFACTS.resolve(source, Path(directory) / "work", "v0.2.1")
                chart_download.assert_called_once_with(ARTIFACTS.PREFIX + "/helm/chart", "0.2.1",
                                                       Path(directory) / "work", "sha256:" + "e" * 64)
            self.assertEqual(result["sourceSHA"], "d" * 40)
            self.assertEqual(result["charts"]["chart"]["path"], str(chart_file))
            self.assertEqual(result["version"], "0.2.1")

    def test_receipt_is_read_from_source_verified_image_config(self):
        receipt = {"cetirun-platform": "sha256:" + "e" * 64}
        for value in (json.dumps(receipt), "not-json", "[]", '{"chart":"not-a-digest"}'):
            labels = {"org.opencontainers.image.version": "0.2.1", "org.opencontainers.image.revision": "d" * 40,
                      ARTIFACTS.CHART_DIGEST_LABEL: value}
            config = json.dumps({"os": "linux", "architecture": "amd64", "config": {"Labels": labels}}).encode()
            config_digest = "sha256:" + hashlib.sha256(config).hexdigest()
            manifest = {"config": {"digest": config_digest, "size": len(config), "mediaType": "application/vnd.oci.image.config.v1+json"}}
            with self.subTest(value=value), patch.object(ARTIFACTS, "_manifest", return_value=("sha256:" + "a" * 64, manifest, b"{}")), \
                 patch.object(ARTIFACTS, "_request", return_value=({}, config)):
                if value == json.dumps(receipt):
                    result = ARTIFACTS._image("repo/cetirun-operator", "0.2.1", "d" * 40)
                    self.assertEqual(result["chartDigests"], receipt)
                    self.assertEqual(result["configDigest"], config_digest)
                else:
                    with self.assertRaisesRegex(ARTIFACTS.ArtifactError, "chart digest receipt"):
                        ARTIFACTS._image("repo/cetirun-operator", "0.2.1", "d" * 40)

    def test_same_version_chart_replacement_fails_before_download(self):
        manifest = {"config": {"digest": "sha256:" + "a" * 64, "size": 2,
                               "mediaType": "application/vnd.cncf.helm.config.v1+json"},
                    "layers": [{"digest": "sha256:" + "b" * 64, "size": 100,
                                "mediaType": "application/vnd.cncf.helm.chart.content.v1.tar+gzip"}]}
        with tempfile.TemporaryDirectory() as temporary, \
             patch.object(ARTIFACTS, "_manifest", return_value=("sha256:" + "c" * 64, manifest, b"{}")), \
             patch.object(ARTIFACTS, "_request") as fetch:
            with self.assertRaisesRegex(ARTIFACTS.ArtifactError, "differs from the source-verified"):
                ARTIFACTS._chart("repo/cetirun-platform", "0.2.1", Path(temporary), "sha256:" + "d" * 64)
            fetch.assert_not_called()

    def test_image_receipt_is_read_from_source_verified_image_config(self):
        receipt = {"portal": "sha256:" + "f" * 64}
        labels = {"org.opencontainers.image.version": "0.2.1", "org.opencontainers.image.revision": "d" * 40,
                  ARTIFACTS.IMAGE_DIGEST_LABEL: json.dumps(receipt)}
        config = json.dumps({"os": "linux", "architecture": "amd64", "config": {"Labels": labels}}).encode()
        config_digest = "sha256:" + hashlib.sha256(config).hexdigest()
        manifest = {"config": {"digest": config_digest, "size": len(config), "mediaType": "application/vnd.oci.image.config.v1+json"}}
        with patch.object(ARTIFACTS, "_manifest", return_value=("sha256:" + "a" * 64, manifest, b"{}")), \
             patch.object(ARTIFACTS, "_request", return_value=({}, config)):
            result = ARTIFACTS._image("repo/cetirun-operator", "0.2.1", "d" * 40)
        self.assertEqual(result["imageDigests"], receipt)

    def test_resolver_rejects_mixed_same_version_image_digest(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory)
            (source / "scripts").mkdir()
            (source / "app/cetirun-chart").mkdir(parents=True)
            (source / "app/cetirun-chart/Chart.yaml").write_text("version: 0.2.1\n")
            (source / "scripts/release-artifacts.json").write_text(json.dumps({
                "schemaVersion": 1,
                "images": [{"name": "cetirun-operator"}, {"name": "portal"}],
                "charts": [{"name": "cetirun", "path": "app/cetirun-chart"}]}))
            operator = {"digest": "sha256:" + "a" * 64,
                        "chartDigests": {"cetirun": "sha256:" + "c" * 64},
                        "imageDigests": {"portal": "sha256:" + "b" * 64}}
            portal = {"digest": "sha256:" + "d" * 64}
            for receipt in ({}, {"portal": operator["imageDigests"]["portal"], "extra": "sha256:" + "e" * 64},
                            operator["imageDigests"]):
                with self.subTest(receipt=receipt), \
                     patch.object(subprocess, "check_output", side_effect=["", "d" * 40]), \
                     patch.object(subprocess, "run", return_value=subprocess.CompletedProcess([], 0, stdout="d" * 40)), \
                     patch.object(ARTIFACTS, "_image", side_effect=[{**operator, "imageDigests": receipt}, portal]), \
                     patch.object(ARTIFACTS, "_chart") as chart_download:
                    with self.assertRaisesRegex(ARTIFACTS.ArtifactError, "complete image digest receipt"):
                        ARTIFACTS.resolve(source, source / "work", "v0.2.1")
                    chart_download.assert_not_called()

    def test_resolver_accepts_exact_image_digest_receipt(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory)
            (source / "scripts").mkdir()
            (source / "app/cetirun-chart").mkdir(parents=True)
            (source / "app/cetirun-chart/Chart.yaml").write_text("version: 0.2.1\n")
            (source / "scripts/release-artifacts.json").write_text(json.dumps({
                "schemaVersion": 1,
                "images": [{"name": "cetirun-operator"}, {"name": "portal"}],
                "charts": [{"name": "cetirun", "path": "app/cetirun-chart"}]}))
            operator = {"digest": "sha256:" + "a" * 64,
                        "chartDigests": {"cetirun": "sha256:" + "c" * 64},
                        "imageDigests": {"portal": "sha256:" + "d" * 64}}
            portal = {"digest": "sha256:" + "d" * 64}
            chart = {"path": str(source / "work/cetirun-0.2.1.tgz"), "dependencies": []}
            with patch.object(subprocess, "check_output", side_effect=["", "d" * 40]), \
                 patch.object(subprocess, "run", return_value=subprocess.CompletedProcess([], 0, stdout="d" * 40)), \
                 patch.object(ARTIFACTS, "_image", side_effect=[operator, portal]), \
                 patch.object(ARTIFACTS, "_chart", return_value=chart):
                result = ARTIFACTS.resolve(source, source / "work", "v0.2.1")
            self.assertEqual(result["images"]["portal"]["digest"], portal["digest"])

    def test_resolver_rejects_missing_or_incomplete_chart_receipt(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory)
            (source / "scripts").mkdir()
            (source / "app/cetirun-chart").mkdir(parents=True)
            (source / "app/cetirun-chart/Chart.yaml").write_text("version: 0.2.1\n")
            (source / "scripts/release-artifacts.json").write_text(json.dumps({
                "schemaVersion": 1, "images": [{"name": "cetirun-operator"}],
                "charts": [{"name": "cetirun", "path": "app/cetirun-chart"}]}))
            for receipt in (None, {}, {"other": "sha256:" + "a" * 64}):
                with self.subTest(receipt=receipt), \
                     patch.object(subprocess, "check_output", side_effect=["", "d" * 40]), \
                     patch.object(subprocess, "run", return_value=subprocess.CompletedProcess([], 0, stdout="d" * 40)), \
                     patch.object(ARTIFACTS, "_image", return_value={"chartDigests": receipt, "imageDigests": {}}), \
                     patch.object(ARTIFACTS, "_chart") as chart_download:
                    with self.assertRaisesRegex(ARTIFACTS.ArtifactError, "complete chart digest receipt"):
                        ARTIFACTS.resolve(source, source / "work", "v0.2.1")
                    chart_download.assert_not_called()

    def test_strict_v_prefix(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(ARTIFACTS.ArtifactError, "start with v"):
                ARTIFACTS.resolve(ROOT, Path(directory), "0.2.1")

    def test_requires_local_release_tag(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "source"
            (source / "scripts").mkdir(parents=True)
            (source / "app/cetirun-chart").mkdir(parents=True)
            (source / "app/cetirun-chart/Chart.yaml").write_text("version: 0.2.1\n")
            (source / "scripts/release-artifacts.json").write_text('{"schemaVersion":1,"images":[],"charts":[]}')
            with patch.object(subprocess, "check_output", side_effect=["", ("d" * 40) + "\n"]), \
                 patch.object(subprocess, "run", return_value=subprocess.CompletedProcess([], 1, stdout="")):
                with self.assertRaisesRegex(ARTIFACTS.ArtifactError, "requested release tag"):
                    ARTIFACTS.resolve(source, Path(directory) / "work", "v0.2.1")

    def test_manifest_bytes_must_match_registry_digest(self):
        class Response:
            headers = {"Docker-Content-Digest": "sha256:" + "a" * 64}
            def __enter__(self): return self
            def __exit__(self, *_): return False
            def read(self, _): return b'{"schemaVersion":2}'
        with patch.object(ARTIFACTS, "urlopen", return_value=Response()):
            with self.assertRaisesRegex(ARTIFACTS.ArtifactError, "valid digest"):
                ARTIFACTS._manifest("repo/image", "0.2.1")

    def test_config_fetch_errors_are_not_ignored(self):
        digest = "sha256:" + "a" * 64
        manifest = {"config": {"digest": digest, "size": 1, "mediaType": "application/vnd.oci.image.config.v1+json"}}
        with patch.object(ARTIFACTS, "_manifest", return_value=(digest, manifest, b"{}")), \
             patch.object(ARTIFACTS, "_request", side_effect=ARTIFACTS.ArtifactError("config unavailable")):
            with self.assertRaisesRegex(ARTIFACTS.ArtifactError, "config unavailable"):
                ARTIFACTS._image("repo/image", "0.2.1", "d" * 40)

    def test_image_requires_matching_release_and_revision_labels(self):
        expected = {"org.opencontainers.image.version": "0.2.1", "org.opencontainers.image.revision": "d" * 40}
        variants = [expected, {}, {**expected, "org.opencontainers.image.version": "0.2.0"},
                    {**expected, "org.opencontainers.image.revision": "e" * 40}]
        for labels in variants:
            config = json.dumps({"os": "linux", "architecture": "amd64", "config": {"Labels": labels}}).encode()
            digest = "sha256:" + hashlib.sha256(config).hexdigest()
            manifest = {"config": {"digest": digest, "size": len(config), "mediaType": "application/vnd.oci.image.config.v1+json"}}
            with self.subTest(labels=labels), patch.object(ARTIFACTS, "_manifest", return_value=("sha256:" + "a" * 64, manifest, b"{}")), \
                 patch.object(ARTIFACTS, "_request", return_value=({}, config)):
                if labels == expected:
                    self.assertTrue(ARTIFACTS._image("repo/image", "0.2.1", "d" * 40)["revisionVerified"])
                else:
                    with self.assertRaisesRegex(ARTIFACTS.ArtifactError, "label does not match"):
                        ARTIFACTS._image("repo/image", "0.2.1", "d" * 40)

    def test_rejects_arm_config_even_when_index_is_linux_amd64(self):
        index_digest = "sha256:" + "a" * 64
        child_digest = "sha256:" + "b" * 64
        config = json.dumps({"os": "linux", "architecture": "arm64", "config": {"Labels": {}}}).encode()
        config_digest = "sha256:" + hashlib.sha256(config).hexdigest()
        manifest = {"manifests": [{"digest": child_digest, "size": 5, "platform": {"os": "linux", "architecture": "amd64"}}]}
        child = {"config": {"digest": config_digest, "size": len(config), "mediaType": "application/vnd.oci.image.config.v1+json"}}
        with patch.object(ARTIFACTS, "_manifest", side_effect=[(index_digest, manifest, b"index"), (child_digest, child, b"child")]), \
             patch.object(ARTIFACTS, "_request", return_value=({}, config)):
            with self.assertRaisesRegex(ARTIFACTS.ArtifactError, "not linux/amd64"):
                ARTIFACTS._image("repo/image", "0.2.1", "d" * 40)

    def test_rejects_duplicate_chart_members(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "duplicate.tgz"
            with tarfile.open(path, "w:gz") as archive:
                for _ in range(2):
                    info = tarfile.TarInfo("chart/Chart.yaml")
                    info.size = 0
                    archive.addfile(info, io.BytesIO())
            with self.assertRaisesRegex(ARTIFACTS.ArtifactError, "duplicate"):
                ARTIFACTS._safe_chart_members(path)

    def test_chart_metadata_covers_embedded_charts(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "chart.tgz"
            with tarfile.open(path, "w:gz") as archive:
                for name, chart in (("root/Chart.yaml", "root"), ("root/charts/sub/Chart.yaml", "sub")):
                    payload = (f"name: {chart}\nversion: 0.2.1\nappVersion: 0.2.1\n").encode()
                    if chart == "root":
                        payload += b"dependencies:\n- name: sub\n  version: 0.2.1\n"
                    info = tarfile.TarInfo(name)
                    info.size = len(payload)
                    archive.addfile(info, io.BytesIO(payload))
            metadata = ARTIFACTS._chart_metadata(path, "root", "0.2.1")
            self.assertEqual(metadata["embedded"], ["sub"])


if __name__ == "__main__":
    unittest.main()
