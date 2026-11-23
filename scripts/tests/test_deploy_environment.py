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


class EnvironmentTests(unittest.TestCase):
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
        for apply, conflict, failure in ((False, False, False), (True, True, False), (True, False, False), (True, False, True)):
            with self.subTest(apply=apply, conflict=conflict, failure=failure), tempfile.TemporaryDirectory() as directory:
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
                driver.scratch = root / "private" / "deployment"
                driver.resource.return_value = {"uid": "synthetic-cluster"}
                live_releases = copy.deepcopy(baseline()["releases"])
                def upgraded(name, values=None):
                    live_releases[name]["revision"] = str(int(live_releases[name]["revision"]) + 1)
                driver.upgrade.side_effect = upgraded
                if failure:
                    def partial_execute():
                        driver.upgrade("istio")
                        raise ValueError("private-upgrade-log")
                    driver.execute.side_effect = partial_execute
                snap = baseline("registry.example.com/hermes-gateway:old") if conflict else baseline()
                with patch.object(deploy.artifacts, "resolve", return_value=lock), \
                     patch.object(deploy.platform, "published_configuration", return_value=(config, {}, {})), \
                     patch.object(deploy.platform, "Deployment", return_value=driver), \
                     patch.object(deploy, "capture", return_value=snap), \
                     patch.object(deploy, "release_snapshot", side_effect=lambda *args, **kwargs: copy.deepcopy(live_releases)), \
                     patch.object(deploy, "stable_acceptance", return_value=snap), \
                     patch.object(deploy, "compare_manifests", return_value={}), \
                     patch.object(deploy, "audit_rendered_images", return_value=5), \
                     patch.object(deploy, "verify_runtime_chart", return_value="PASS"), \
                     patch.object(deploy, "verify_images", return_value=3), \
                     patch.object(deploy, "verify_entry", return_value={"tls": "PASS"}), redirect_stdout(io.StringIO()):
                    code = deploy.run(args)
                self.assertEqual(code, 1 if conflict or failure else 0)
                self.assertEqual(driver.execute.call_count, int(apply and not conflict))
                report = (root / "report.json").read_text()
                for secret in ("synthetic-secret", "synthetic-cluster", "sensitive-spec-hash", "agents/fixture", "portal.example.invalid", "private-upgrade-log"):
                    self.assertNotIn(secret, report)
                if failure:
                    result = json.loads(report)
                    self.assertEqual(result["status"], "FAIL")
                    self.assertEqual(result["afterReleases"]["istio"]["revision"], "5")
                    self.assertEqual(result["preservationAfterFailure"], "PASS")
                    self.assertIn({"stage": "upgrade_istio", "status": "PASS"}, result["stages"])

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

    def test_mcr_packages_digest_locked_charts_only_after_image_promotion(self):
        path = Path(__file__).resolve().parents[2] / ".pipelines/release-mcr.yml"
        pipeline = yaml.safe_load(path.read_text())
        scripts = [step.get("bash", step.get("inputs", {}).get("inlineScript", ""))
                   for job in pipeline["jobs"] for step in job["steps"]]
        packaging = [script for script in scripts if "package-charts --package-dir" in script]
        self.assertEqual(len(packaging), 1)
        script = packaging[0]
        promotion = script.index('published_images+=("${artifact}@${digest}")')
        handoff = script.index('"${image_digest_file}" "${published_images[@]}"')
        packaging_index = script.index("package-charts --package-dir")
        publishing = script.index('helm push "${archive}"')
        public_verification = script.index("scripts/verify-mcr-image.py")
        latest = script.index('latest_image="${registry}/${repository}:latest"')
        self.assertLess(promotion, handoff)
        self.assertLess(handoff, packaging_index)
        self.assertLess(packaging_index, publishing)
        self.assertLess(publishing, public_verification)
        self.assertLess(public_verification, latest)
        self.assertIn('RELEASE_IMAGE_DIGESTS_FILE="${image_digest_file}"', script)
        self.assertIn("REGISTRY=mcr.microsoft.com/aks/ai-runtime/cetirun", script)

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
