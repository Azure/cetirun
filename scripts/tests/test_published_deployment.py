#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Offline checks for immutable chart extraction and image pinning."""

import importlib.util
import base64
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

    def test_image_identity_ignores_optional_tag_but_keeps_repository(self):
        digest = "sha256:" + "a" * 64
        self.assertEqual(
            DEPLOY._repository_digest("mcr.example/cetirun-pi-agent@" + digest),
            DEPLOY._repository_digest("mcr.example/cetirun-pi-agent:0.2.1@" + digest),
        )
        self.assertNotEqual(
            DEPLOY._repository_digest("other.example/cetirun-pi-agent:0.2.1@" + digest),
            DEPLOY._repository_digest("mcr.example/cetirun-pi-agent:0.2.1@" + digest),
        )

    def test_excluded_published_inventory_is_availability_metadata(self):
        config = {"release": {"excludeImages": ["cetirun-vscode"]},
                  "_publishedInventory": {"images": [{"name": "cetirun-vscode", "optional": True}], "charts": []}}
        DEPLOY.validate_excluded_images(config)
        config["platform"] = {"values": {"image": "mcr.example/cetirun-vscode:1"}}
        with self.assertRaisesRegex(ValueError, "still referenced"):
            DEPLOY.validate_excluded_images(config)
        for inventory in (
                [{"name": "cetirun-portal", "optional": False}],
                [{"name": "known", "optional": True}],
        ):
            excluded = "cetirun-missing" if inventory[0]["name"] == "known" else inventory[0]["name"]
            with self.subTest(inventory=inventory), self.assertRaisesRegex(ValueError, "optional|unknown"):
                DEPLOY.validate_excluded_images({"release": {"excludeImages": [excluded]},
                                                 "_publishedInventory": {"images": inventory, "charts": []}})

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
                                               "digest": sha, "platformDigest": sha,
                                               "optional": bool(entry.get("optional", False))}
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
            operator = next(doc for doc in deployment.documents["platform"]
                            if doc.get("kind") == "Deployment" and doc["metadata"]["name"] == "cetirun-operator")
            self.assertIn("--chart-digest=" + self.lock["charts"]["cetirun"]["digest"],
                          operator["spec"]["template"]["spec"]["containers"][0]["args"])
            self.assertIn("--memory-context-image=", operator["spec"]["template"]["spec"]["containers"][0]["args"])
            self.assertEqual(deployment.charts["platform"], Path(self.lock["charts"]["cetirun-platform"]["path"]))
            with patch.object(deployment, "helm") as helm:
                deployment.upgrade("broker", expected_revision=7)
            self.assertIn(self.lock["charts"]["cetirun-agent-broker"]["path"], helm.call_args.args)
            self.assertNotIn("--install", helm.call_args.args)
            guard_args = [arg.split("=", 1)[1] for arg in helm.call_args.args if arg.startswith("--post-renderer-args=")]
            self.assertEqual(guard_args, ["--expected-revision", "7", "--release", config["broker"]["release"],
                                         "--namespace", config["broker"]["namespace"], "--context", config["target"]["context"]])

    def test_published_upgrade_requires_explicit_expected_revision(self):
        with tempfile.TemporaryDirectory() as temporary:
            scratch = Path(temporary)
            config, values, roots = DEPLOY.published_configuration(self.configuration(), self.lock, scratch)
            deployment = DEPLOY.Deployment(config, values, scratch, published_artifacts=self.lock, chart_roots=roots)
            deployment.render()
            with patch.object(deployment, "helm") as helm, self.assertRaisesRegex(
                    ValueError, "explicit expected release revision"):
                deployment.upgrade("broker")
            helm.assert_not_called()

    def test_retained_istio_upgrade_uses_previewed_values_and_preserves_only_tls(self):
        with tempfile.TemporaryDirectory() as temporary:
            config, values, roots = DEPLOY.published_configuration(self.configuration(), self.lock, Path(temporary))
            config["istio"]["tlsMode"] = "retain"
            values["istio"]["oauth2Proxy"]["nodeSelector"] = {"agentpool": None, "new-pool": "true"}
            values["istio"]["oauth2Proxy"]["sessionStore"]["redis"]["existingSecret"] = "sessions-fixture"
            deployment = DEPLOY.Deployment(config, values, Path(temporary), published_artifacts=self.lock, chart_roots=roots)
            deployment.render()
            preview = next(doc for doc in deployment.documents["istio"] if doc["kind"] == "Deployment" and doc["metadata"]["name"] == "oauth2-proxy-sessions")
            metadata = {"uid": "tls-uid", "resourceVersion": "7", "annotations": {
                "meta.helm.sh/release-name": config["istio"]["release"],
                "meta.helm.sh/release-namespace": config["istio"]["namespace"]}}
            tls = {"cert": "synthetic preserved certificate", "key": "synthetic preserved private key"}
            files = []
            def read(*args):
                value = tls["cert" if "tls\\.crt" in args[-1] else "key"]
                return base64.b64encode(value.encode()).decode()
            def upgrade(*args):
                self.assertEqual(args[0], "upgrade")
                self.assertIn("--reset-values", args)
                self.assertNotIn("--reuse-values", args)
                for option in ("--tls-secret-uid", "tls-uid", "--tls-secret-resource-version", "7"):
                    self.assertIn("--post-renderer-args=" + option, args)
                paths = [Path(args[i + 1]) for i, arg in enumerate(args) if arg == "--values"]
                files.extend(paths[1:])
                self.assertEqual(paths[-1].stat().st_mode & 0o777, 0o600)
                self.assertEqual(json.loads(paths[-1].read_text()), {"tls": {"enabled": True, **tls}})
                # Replay the exact applied values through real Helm's renderer.
                # Only the unrelated cookie lookup is stubbed for offline Helm.
                command = ["helm", "template", config["istio"]["release"], str(deployment.charts["istio"]),
                           "--namespace", config["istio"]["namespace"], "--set", "cookieSecret=offline-cookie"]
                for path in paths:
                    command += ["--values", str(path)]
                result = subprocess.run(command, check=True, capture_output=True, text=True,
                                        env={**DEPLOY.os.environ, "KUBECONFIG": "/dev/null",
                                             "HELM_REGISTRY_CONFIG": "/dev/null", "HELM_REPOSITORY_CONFIG": "/dev/null"})
                documents = list(yaml.safe_load_all(result.stdout))
                applied = next(doc for doc in documents if doc and doc["kind"] == "Deployment" and doc["metadata"]["name"] == "oauth2-proxy-sessions")
                self.assertEqual(applied, preview)
                self.assertEqual(applied["spec"]["template"]["spec"]["nodeSelector"],
                                 preview["spec"]["template"]["spec"]["nodeSelector"])
                secret = next(doc for doc in documents if doc and doc["kind"] == "Secret" and doc["metadata"]["name"] == "cetirun-tls")
                self.assertEqual(secret["data"], {"tls.crt": base64.b64encode(tls["cert"].encode()).decode(),
                                                  "tls.key": base64.b64encode(tls["key"].encode()).decode()})
            with patch.object(deployment, "resource", return_value=metadata), \
                 patch.object(deployment, "kubectl", side_effect=read), patch.object(deployment, "helm", side_effect=upgrade):
                deployment.upgrade("istio", expected_revision=7)
            self.assertTrue(files)
            self.assertTrue(all(not path.exists() for path in files))
            with patch.object(deployment, "resource", return_value=metadata), \
                 patch.object(deployment, "kubectl", side_effect=read), \
                 patch.object(deployment, "helm", side_effect=ValueError("synthetic failure")), \
                 self.assertRaisesRegex(ValueError, "synthetic failure"):
                deployment.upgrade("istio", expected_revision=7)
            self.assertFalse(list(Path(temporary).glob("*-retained-tls.json")))
            with patch.object(deployment, "resource", side_effect=[metadata, {**metadata, "uid": "replacement"}]), \
                 patch.object(deployment, "kubectl", side_effect=read), patch.object(deployment, "helm") as helm, \
                 self.assertRaisesRegex(ValueError, "TLS Secret changed"):
                deployment.upgrade("istio", expected_revision=7)
            helm.assert_not_called()

    def test_published_image_optionality_requires_boolean(self):
        for invalid in ("false", "true", 0, 1, None, []):
            lock = copy.deepcopy(self.lock)
            lock["images"]["cetirun-vscode"]["optional"] = invalid
            with self.subTest(optional=invalid), tempfile.TemporaryDirectory() as temporary, \
                 self.assertRaisesRegex(ValueError, "optional must be boolean"):
                DEPLOY.published_configuration(self.configuration(), lock, Path(temporary))

    def test_custom_approved_image_limit_matches_operator(self):
        config = self.configuration()
        digest = "sha256:" + "a" * 64
        config["platform"]["values"]["cetirun-operator"]["customRuntime"]["approvedImages"] = [
            f"mcr.example/custom-{index}@{digest}" for index in range(65)
        ]
        with tempfile.TemporaryDirectory() as temporary, self.assertRaisesRegex(ValueError, "more than 64"):
            DEPLOY.published_configuration(config, self.lock, Path(temporary))

    def test_published_configuration_applies_optional_exclusions(self):
        config = self.configuration()
        config["release"] = {"excludeImages": ["cetirun-vscode"]}
        approved = config["platform"]["values"]["cetirun-operator"]["customRuntime"]["approvedImages"]
        config["platform"]["values"]["cetirun-operator"]["customRuntime"]["approvedImages"] = [
            image for image in approved if "cetirun-vscode" not in image
        ]
        with tempfile.TemporaryDirectory() as temporary:
            resolved, _, _ = DEPLOY.published_configuration(config, self.lock, Path(temporary))
        self.assertNotIn("cetirun-vscode", {entry["name"] for entry in DEPLOY.release_inventory(resolved)["images"]})
        for excluded in ("cetirun-portal", "missing-image"):
            config = self.configuration()
            config["release"] = {"excludeImages": [excluded]}
            with tempfile.TemporaryDirectory() as temporary, self.subTest(excluded=excluded), \
                    self.assertRaisesRegex(ValueError, "optional|unknown"):
                DEPLOY.published_configuration(config, self.lock, Path(temporary))

    def test_custom_approved_image_limit_accepts_64(self):
        config = self.configuration()
        digest = "sha256:" + "a" * 64
        config["platform"]["values"]["cetirun-operator"]["customRuntime"]["approvedImages"] = [
            f"mcr.example/custom-{index}@{digest}" for index in range(64)
        ]
        with tempfile.TemporaryDirectory() as temporary:
            DEPLOY.published_configuration(config, self.lock, Path(temporary))

    def test_published_execute_cannot_delegate_an_unguarded_upgrade(self):
        with tempfile.TemporaryDirectory() as temporary:
            config, values, roots = DEPLOY.published_configuration(self.configuration(), self.lock, Path(temporary))
            driver = DEPLOY.Deployment(config, values, Path(temporary), published_artifacts=self.lock, chart_roots=roots)
            driver.namespaces = {config[name]["namespace"]: {"labels": {"istio.io/rev": config["istio"]["revision"]}}
                                 for name in DEPLOY.CHARTS}
            for revision in (None, 0, -1, True, "7"):
                with self.subTest(revision=revision), patch.object(driver, "preflight"), \
                     patch.object(driver, "helm") as helm, patch.object(driver, "ready"), \
                     self.assertRaisesRegex(ValueError, "explicit expected release revision"):
                    driver.execute_platform(before_upgrade=lambda *args: revision)
                helm.assert_not_called()

    def test_published_runtime_helm_values_overrides_are_preserved(self):
        config = self.configuration()
        helm_values = config["platform"]["values"]["cetirun-operator"].setdefault("cetirun", {}).setdefault("helmValues", {})
        override = {
            "initContainers": [{"name": "runtime-helper", "image": self.registry + "/cetirun-pi-agent:legacy"}],
            "helpers": {"image": self.registry + "/cetirun-pi-agent:legacy"},
        }
        helm_values.update(copy.deepcopy(override))
        with tempfile.TemporaryDirectory() as temporary:
            values = DEPLOY.published_configuration(config, self.lock, Path(temporary))[1]
        actual = values["platform"]["cetirun-operator"]["cetirun"]["helmValues"]
        self.assertEqual(actual["initContainers"], override["initContainers"])
        self.assertEqual(actual["helpers"], override["helpers"])

    def test_tagless_approved_image_matches_locked_digest_and_is_preserved(self):
        config = self.configuration()
        digest = self.lock["images"]["cetirun-pi-agent"]["digest"]
        config["platform"]["values"]["cetirun-operator"]["customRuntime"]["approvedImages"][0] = (
            f"{self.registry}/cetirun-pi-agent@{digest}")
        with tempfile.TemporaryDirectory() as temporary:
            values = DEPLOY.published_configuration(config, self.lock, Path(temporary))[1]
        self.assertEqual(values["platform"]["cetirun-operator"]["customRuntime"]["approvedImages"][0],
                         f"{self.registry}/cetirun-pi-agent@{digest}")

    def test_published_custom_allowlist_keeps_multiple_approved_digests(self):
        config = self.configuration()
        custom = config["platform"]["values"]["cetirun-operator"]["customRuntime"]
        old = self.registry + "/cetirun-pi-agent@sha256:" + "f" * 64
        current = self.lock["images"]["cetirun-pi-agent"]["reference"]
        custom["approvedImages"] = [old, current]
        with tempfile.TemporaryDirectory() as temporary:
            values = DEPLOY.published_configuration(config, self.lock, Path(temporary))[1]
        self.assertEqual(values["platform"]["cetirun-operator"]["customRuntime"]["approvedImages"], [old, current])

    def test_published_cetiadmin_runtime_pin_is_preserved_or_rejected(self):
        config = self.configuration()
        admin = config["platform"]["values"]["cetirun-operator"]["cetiadmin"]
        admin["enabled"] = True
        admin["runtime"] = {"version": self.version, "image": "", "port": 8642}
        locked = self.lock["images"]["hermes-gateway"]["reference"]
        admin["runtime"]["image"] = locked
        with tempfile.TemporaryDirectory() as temporary:
            values = DEPLOY.published_configuration(config, self.lock, Path(temporary))[1]
        self.assertEqual(values["platform"]["cetirun-operator"]["cetiadmin"]["runtime"]["image"], locked)
        for image in (
            "other.example/hermes-gateway@sha256:" + "a" * 64,
            self.registry + "/hermes-gateway@sha256:" + "f" * 64,
        ):
            config = self.configuration()
            candidate = config["platform"]["values"]["cetirun-operator"]["cetiadmin"]
            candidate["enabled"] = True
            candidate["runtime"] = {"version": self.version, "image": image, "port": 8642}
            with tempfile.TemporaryDirectory() as temporary, self.assertRaisesRegex(ValueError, "cetiadmin.runtime.image conflicts"):
                DEPLOY.published_configuration(config, self.lock, Path(temporary))

    def test_published_mesh_requires_an_already_matching_provider(self):
        document = {"metadata": {"uid": "mesh-uid", "resourceVersion": "1"},
                    "data": {"mesh": yaml.safe_dump({"extensionProviders": [DEPLOY.MESH._oauth2_proxy_provider("oauth2-proxy.example") ]})}}
        with self.assertRaisesRegex(ValueError, "mesh ConfigMap"):
            DEPLOY.validate_mesh_configmap(document, "oauth2-proxy.expected", published=True)
        patch, changed = DEPLOY.validate_mesh_configmap(document, "oauth2-proxy.example", published=True)
        self.assertFalse(changed)
        self.assertEqual(patch["metadata"]["resourceVersion"], "1")

    def test_published_mesh_rejects_missing_provider_headers_or_configmap(self):
        from scripts.tests.test_full_deployment import SyntheticCluster
        for missing in ("configmap", "provider", "authorization", "set-cookie"):
            with self.subTest(missing=missing), tempfile.TemporaryDirectory() as temporary:
                config, values, _ = DEPLOY.published_configuration(self.configuration(), self.lock, Path(temporary))
                driver = SyntheticCluster(config, values, Path(temporary) / "deployment")
                driver.published_artifacts = self.lock
                mesh = yaml.safe_load(driver.mesh["data"]["mesh"])
                if missing == "configmap":
                    driver.mesh = None
                elif missing == "provider":
                    mesh["extensionProviders"] = []
                else:
                    header = "headersToUpstreamOnAllow" if missing == "authorization" else "headersToDownstreamOnAllow"
                    mesh["extensionProviders"][0]["envoyExtAuthzHttp"][header].remove(missing)
                if driver.mesh:
                    driver.mesh["data"]["mesh"] = yaml.safe_dump(mesh)
                with self.assertRaisesRegex(ValueError, "mesh ConfigMap"):
                    driver.check_mesh_configuration()
                self.assertFalse(driver.writes)

    def test_published_broker_conflict_does_not_patch_mesh(self):
        from scripts.tests.test_full_deployment import SyntheticCluster
        with tempfile.TemporaryDirectory() as temporary:
            config, values, _ = DEPLOY.published_configuration(self.configuration(), self.lock, Path(temporary))
            driver = SyntheticCluster(config, values, Path(temporary) / "deployment")
            driver.published_artifacts = self.lock
            driver.namespaces = {config[name]["namespace"]: {"labels": {"istio.io/rev": config["istio"]["revision"]}}
                                 for name in DEPLOY.CHARTS}
            before = copy.deepcopy(driver.mesh)
            def upgrade(name, *, expected_revision):
                if name == "broker":
                    raise ValueError("broker revision changed")
            # Namespace, certificate and artifact probes have separate coverage.
            # Keep the real mesh prerequisite and execute ordering here.
            with patch.object(driver, "preflight", side_effect=driver.check_mesh_configuration), \
                 patch.object(driver, "upgrade", side_effect=upgrade) as upgrades, \
                 patch.object(driver, "ready"), self.assertRaisesRegex(ValueError, "broker revision changed"):
                driver.execute_platform()
            self.assertEqual([call.args[0] for call in upgrades.call_args_list], ["istio", "broker"])
            self.assertEqual(driver.mesh, before)
            self.assertFalse(driver.writes)

    def test_conflicting_runtime_chart_digest_is_rejected(self):
        config = self.configuration()
        config["platform"]["values"]["cetirun-operator"].setdefault("cetirun", {})["chart"] = {
            "digest": "sha256:" + "f" * 64}
        with tempfile.TemporaryDirectory() as temporary, self.assertRaisesRegex(ValueError, "runtime chart digest override"):
            DEPLOY.published_configuration(config, self.lock, Path(temporary))

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
        result = DEPLOY.release_inventory({"_publishedInventory": {
            "images": [{"name": "future-image", "optional": False}],
            "charts": [{"name": "future-chart"}],
        }})
        self.assertEqual([item["name"] for item in result["images"]], ["future-image"])
        self.assertEqual([item["name"] for item in result["charts"]], ["future-chart"])

    def test_published_memory_overrides_are_rejected_before_rewriting(self):
        image = self.lock["images"]["cetirun-memory-context"]
        for path in ("global.memoryContext.image", "cetirun-operator.memoryContext.image"):
            for reference in (image["reference"], f"{self.registry}/cetirun-memory-context@{image['digest']}",
                              "other.example/cetirun-memory-context@" + image["digest"],
                              f"{self.registry}/cetirun-memory-context@sha256:" + "f" * 64):
                config = self.configuration()
                DEPLOY.set_value(config["platform"]["values"], path, reference)
                with self.subTest(path=path, reference=reference), tempfile.TemporaryDirectory() as temporary, \
                     patch.object(DEPLOY, "_pin_published_images") as pin, \
                     self.assertRaisesRegex(ValueError, "unsupported in published deployments"):
                    DEPLOY.published_configuration(config, self.lock, Path(temporary))
                pin.assert_not_called()

    def test_republished_memory_digest_never_becomes_a_global_operator_override(self):
        changed = copy.deepcopy(self.lock)
        image = changed["images"]["cetirun-memory-context"]
        image["digest"] = "sha256:" + "f" * 64
        image["reference"] = f"{self.registry}/cetirun-memory-context:{self.version}@{image['digest']}"
        for lock in (self.lock, changed):
            with tempfile.TemporaryDirectory() as temporary:
                _, values, _ = DEPLOY.published_configuration(self.configuration(), lock, Path(temporary))
                self.assertEqual(values["platform"]["global"]["memoryContext"]["image"], "")
                self.assertEqual(values["platform"]["cetirun-operator"]["memoryContext"]["image"], "")

    def test_published_preflight_requires_existing_namespace_and_revision(self):
        # Exercise the real preflight/execute entry points against synthetic
        # Kubernetes responses; no namespace write may precede the Helm guard.
        from scripts.tests.test_full_deployment import SyntheticCluster
        for name in ("broker", "platform"):
            for state in ("missing", "unlabeled", "different"):
                with self.subTest(component=name, state=state), tempfile.TemporaryDirectory() as temporary:
                    config, values, roots = DEPLOY.published_configuration(self.configuration(), self.lock, Path(temporary))
                    driver = SyntheticCluster(config, values, Path(temporary) / "deployment")
                    driver.published_artifacts = self.lock
                    driver.documents = {component: [] for component in DEPLOY.CHARTS}
                    for component in DEPLOY.CHARTS:
                        ns = config[component]["namespace"]
                        driver.objects[("namespace", "", ns)] = {"name": ns, "uid": "ns-uid", "labels": {
                            "istio.io/rev": config["istio"]["revision"]}}
                    key = ("namespace", "", config[name]["namespace"])
                    if state == "missing":
                        del driver.objects[key]
                    else:
                        driver.objects[key]["labels"] = {} if state == "unlabeled" else {"istio.io/rev": "other"}
                    # The unrelated probes are tested by the full deployment
                    # suite; stop here at the namespace prerequisite.
                    with patch.object(driver, "check_kubectl_version"), \
                         self.assertRaisesRegex(ValueError, "must already|another Istio revision"):
                        driver.execute_platform()
                    self.assertFalse(driver.writes)


if __name__ == "__main__":
    unittest.main()
