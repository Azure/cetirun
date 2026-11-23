#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Offline checks for immutable chart extraction and image pinning."""

import importlib.util
from pathlib import Path
import tarfile
import hashlib
import tempfile
import unittest
import shutil
import subprocess
import copy
import json
from unittest.mock import patch

import yaml


ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("cetirun_platform_deploy", ROOT / "scripts" / "cetirun_platform_deploy.py")
DEPLOY = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(DEPLOY)


class PublishedDeploymentTests(unittest.TestCase):
    def test_extracts_verified_chart_without_links(self):
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary)
            source = work / "cetirun-platform"
            source.mkdir()
            (source / "Chart.yaml").write_text("apiVersion: v2\nname: cetirun-platform\nversion: 0.2.1\n")
            archive = work / "chart.tgz"
            with tarfile.open(archive, "w:gz") as stream:
                stream.add(source, arcname="cetirun-platform")
            digest = "sha256:" + hashlib.sha256(archive.read_bytes()).hexdigest()
            roots = DEPLOY.prepare_published_charts(
                {"charts": {"platform": {"path": str(archive), "sha256": digest}}}, work / "extract")
            self.assertEqual(roots["platform"].name, "cetirun-platform")
            self.assertTrue((roots["platform"] / "Chart.yaml").is_file())

    def test_rejects_archive_path_escape(self):
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary)
            archive = work / "unsafe.tgz"
            with tarfile.open(archive, "w:gz") as stream:
                info = tarfile.TarInfo("../escape")
                info.size = 0
                stream.addfile(info)
            with self.assertRaises(ValueError):
                DEPLOY.prepare_published_charts(
                    {"charts": {"platform": {"path": str(archive), "sha256": "sha256:" + "a" * 64}}}, work / "extract")

    def test_pins_only_known_first_party_images(self):
        digest = "sha256:" + "a" * 64
        values = {"image": "mcr.microsoft.com/aks/ai-runtime/cetirun/cetirun-portal:0.2.1",
                  "args": ["thirdparty/thing:1.0", "cetirun-portal-sidecar:0.2.1"]}
        result = DEPLOY._pin_published_images(
            values, {"cetirun-portal": {"reference": "mcr.microsoft.com/aks/ai-runtime/cetirun/cetirun-portal:0.2.1@" + digest}})
        self.assertIn("@" + digest, result["image"])
        self.assertEqual(result["args"][0], "thirdparty/thing:1.0")
        self.assertEqual(result["args"][1], "cetirun-portal-sidecar:0.2.1")

    def test_real_helm_renders_original_archive(self):
        helm = shutil.which("helm")
        if not helm:
            self.skipTest("Helm is not installed")
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary)
            chart = work / "cetirun-platform"
            (chart / "templates").mkdir(parents=True)
            (chart / "Chart.yaml").write_text("apiVersion: v2\nname: cetirun-platform\nversion: 0.2.1\n")
            (chart / "values.yaml").write_text("marker: published\n")
            (chart / "templates" / "marker.yaml").write_text("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Release.Name }}\ndata:\n  marker: {{ .Values.marker | quote }}\n")
            archive = work / "cetirun-platform-0.2.1.tgz"
            with tarfile.open(archive, "w:gz") as stream:
                stream.add(chart, arcname="cetirun-platform")
            digest = "sha256:" + hashlib.sha256(archive.read_bytes()).hexdigest()
            roots = DEPLOY.prepare_published_charts({"charts": {"cetirun-platform": {"path": str(archive), "sha256": digest}}}, work / "extract")
            result = subprocess.run([helm, "template", "published", str(archive)], text=True,
                                    capture_output=True, check=True)
            self.assertIn('marker: "published"', result.stdout)
            self.assertTrue((roots["cetirun-platform"] / "Chart.yaml").is_file())


@unittest.skipUnless(shutil.which("helm"), "Helm is required for complete published-chart rendering")
class PublishedIntegrationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory()
        cls.root = Path(cls.temporary.name)
        inventory = DEPLOY.RELEASE.inventory()
        cls.version = DEPLOY.RELEASE.VERSIONS.top_level_yaml(ROOT / "app/cetirun-chart/Chart.yaml", "version")
        cls.registry = "mcr.microsoft.com/aks/ai-runtime/cetirun"
        staged = {}
        for entry in inventory["charts"]:
            destination = cls.root / "source" / entry["name"]
            shutil.copytree(ROOT / entry["path"], destination, ignore=shutil.ignore_patterns("charts", "__pycache__"))
            staged[entry["name"]] = destination
        # Package a distinct published default to detect current-checkout fallback.
        portal_values = staged["cetirun-portal"] / "values.yaml"
        value = yaml.safe_load(portal_values.read_text())
        value["noticeBanner"] = "published-release-marker"
        portal_values.write_text(yaml.safe_dump(value))
        for entry in inventory["charts"]:
            parent = staged[entry["name"]]
            metadata = yaml.safe_load((parent / "Chart.yaml").read_text())
            for dependency in metadata.get("dependencies", []):
                shutil.copytree(staged[dependency["name"]], parent / "charts" / dependency["name"])
        cls.lock = {"registry": cls.registry, "version": cls.version, "sourceSHA": "a" * 40, "images": {}, "charts": {}}
        for entry in inventory["images"]:
            sha = "sha256:" + hashlib.sha256(entry["name"].encode()).hexdigest()
            cls.lock["images"][entry["name"]] = {"reference": f"{cls.registry}/{entry['name']}:{cls.version}@{sha}",
                                               "digest": sha, "platformDigest": sha}
        for name, source in staged.items():
            archive = cls.root / (name + ".tgz")
            with tarfile.open(archive, "w:gz") as stream:
                stream.add(source, arcname=name)
            cls.lock["charts"][name] = {"path": str(archive), "digest": "sha256:" + "b" * 64,
                                        "sha256": "sha256:" + hashlib.sha256(archive.read_bytes()).hexdigest()}

    @classmethod
    def tearDownClass(cls):
        cls.temporary.cleanup()

    def configuration(self):
        config = yaml.safe_load((ROOT / "app/cetirun-platform/ci/deployment-example.yaml").read_text())
        config.pop("release")
        config["platform"]["values"]["cetirun-operator"]["cetiadmin"]["enabled"] = False
        return config

    def test_full_render_pins_every_control_image_and_uses_downloaded_defaults(self):
        from scripts.deploy_environment import audit_rendered_images
        with tempfile.TemporaryDirectory() as temporary:
            scratch = Path(temporary)
            config, values, roots = DEPLOY.published_configuration(self.configuration(), self.lock, scratch)
            self.assertEqual(values["platform"]["cetirun-portal"]["noticeBanner"], "published-release-marker")
            old_custom = self.configuration()["platform"]["values"]["cetirun-operator"]["customRuntime"]["approvedImages"]
            self.assertEqual(values["platform"]["cetirun-operator"]["customRuntime"]["approvedImages"], old_custom)
            deployment = DEPLOY.Deployment(config, values, scratch, published_artifacts=self.lock, chart_roots=roots)
            with patch.object(deployment, "run", wraps=deployment.run) as commands:
                deployment.render()
            self.assertFalse(any(call.args[0][0:2] == ["helm", "dependency"] for call in commands.call_args_list))
            self.assertGreaterEqual(audit_rendered_images(deployment, self.lock), 5)
            self.assertEqual(deployment.charts["platform"], Path(self.lock["charts"]["cetirun-platform"]["path"]))
            with patch.object(deployment, "helm") as helm:
                deployment.upgrade("broker")
            self.assertEqual(helm.call_args.args[3], self.lock["charts"]["cetirun-agent-broker"]["path"])

    def test_refuses_changed_chart_before_render_and_apply(self):
        with tempfile.TemporaryDirectory() as temporary:
            scratch = Path(temporary)
            lock = copy.deepcopy(self.lock)
            original = Path(lock["charts"]["cetirun-agent-broker"]["path"])
            changed = scratch / "broker.tgz"
            changed.write_bytes(original.read_bytes())
            lock["charts"]["cetirun-agent-broker"]["path"] = str(changed)
            config, values, roots = DEPLOY.published_configuration(self.configuration(), lock, scratch)
            deployment = DEPLOY.Deployment(config, values, scratch, published_artifacts=lock, chart_roots=roots)
            changed.write_bytes(changed.read_bytes() + b"changed")
            with self.assertRaisesRegex(ValueError, "changed"):
                deployment.render()

    def test_published_inventory_does_not_revert_to_current_checkout(self):
        result = DEPLOY.release_inventory({"_publishedInventory": {"images": ["future-image"], "charts": ["future-chart"]}})
        self.assertEqual([item["name"] for item in result["images"]], ["future-image"])
        self.assertEqual([item["name"] for item in result["charts"]], ["future-chart"])


if __name__ == "__main__":
    unittest.main()
