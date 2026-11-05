# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Run the inventory's actual builders against a local Docker command double."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts/cetirun-release.py"
IMAGES = {"cetirun-base", "openclaw-gateway", "hermes-gateway", "cetirun-desktop", "cetirun-operator",
          "cetirun-ai-gateway", "cetirun-agent-broker", "cetirun-connections-mcp", "cetirun-terminal",
          "cetirun-portal", "cetirun-vscode", "cetirun-pi-agent", "cetirun-memory-context"}
CHARTS = {"cetirun", "cetirun-public-ingress", "cetirun-istio", "cetirun-operator", "cetirun-portal", "cetirun-platform",
          "cetirun-ai-gateway", "cetirun-agent-broker", "cetirun-connections-mcp"}
DOCKER = r'''
import json, os, pathlib, sys
args = sys.argv[1:]
log = pathlib.Path(os.environ["ARTIFACT_COMMANDS"])
previous = [json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []
with log.open("a") as output:
    output.write(json.dumps(args) + "\n")
if args[:2] == ["buildx", "inspect"]:
    print("Driver: docker")
if args[:3] == ["buildx", "imagetools", "inspect"]:
    reference = args[3]
    if os.environ.get("ARTIFACT_REGISTRY_ERROR"):
        print(os.environ["ARTIFACT_REGISTRY_ERROR"], file=sys.stderr)
        sys.exit(1)
    if reference == os.environ.get("ARTIFACT_EXISTS"):
        sys.exit(0)
    if not any(item[:2] == ["buildx", "build"] and reference in item for item in previous):
        print("manifest unknown", file=sys.stderr)
        sys.exit(1)
if args[:2] == ["buildx", "build"] and os.environ.get("ARTIFACT_FAIL_BUILD") in args:
    sys.exit(7)
'''


class ReleaseInventoryTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="cetirun-artifacts-test-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.log = self.root / "commands.jsonl"
        docker = self.root / "docker"
        docker.write_text(f"#!{sys.executable}\n" + DOCKER)
        docker.chmod(0o755)
        version = subprocess.run([sys.executable, "-B", str(ROOT / "scripts/cetirun-version.py"), "get"], capture_output=True, text=True, check=True).stdout.strip()
        self.env = {**os.environ, "PATH": f"{self.root}:{os.environ['PATH']}", "REGISTRY": "registry.example.com/test",
                    "VERSION": version, "GIT_SHA": "a" * 40, "ACCEPT_VSCODE_SERVER_LICENSE_TERMS": "true",
                    "ARTIFACT_COMMANDS": str(self.log), "KUBECONFIG": os.devnull, "HELM_REGISTRY_CONFIG": os.devnull}

    def run_release(self, *args, success=True):
        result = subprocess.run([sys.executable, "-B", str(SCRIPT), *args], env=self.env, cwd=ROOT, capture_output=True, text=True, timeout=60)
        self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
        return result

    def commands(self):
        return [json.loads(line) for line in self.log.read_text().splitlines()] if self.log.exists() else []

    def image(self, name):
        return f"{self.env['REGISTRY']}/{name}:{self.env['VERSION']}"

    def test_inventory_and_plan_do_not_contact_registry(self):
        self.assertEqual(set(self.run_release("list", "images").stdout.splitlines()), IMAGES)
        self.assertEqual(set(self.run_release("list", "charts").stdout.splitlines()), CHARTS)
        self.run_release("check")
        plan = json.loads(self.run_release("plan").stdout)
        self.assertEqual(plan["gitSha"], "a" * 40)
        self.assertEqual(self.commands(), [])

    def test_release_pipeline_shell_syntax(self):
        pipeline = yaml.safe_load((ROOT / ".pipelines/release-mcr.yml").read_text())
        for step in pipeline["jobs"][0]["steps"]:
            script = step.get("bash") or step.get("inputs", {}).get("inlineScript")
            if script:
                result = subprocess.run(["bash", "-n"], input=script, capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, f"{step['displayName']}: {result.stderr}")

    def test_actual_builders_build_each_image_once_without_push(self):
        result = subprocess.run(["make", "build-images"], env=self.env, cwd=ROOT, capture_output=True, text=True, timeout=60)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        builds = [command for command in self.commands() if command[:2] == ["buildx", "build"]]
        self.assertEqual(len(builds), 13)
        tags = {command[command.index("-t") + 1] if "-t" in command else command[command.index("--tag") + 1] for command in builds}
        self.assertEqual(tags, {self.image(name) for name in IMAGES})
        for command in builds:
            self.assertIn("--load", command)
            self.assertNotIn("--push", command)
            self.assertTrue(any("a" * 40 in argument for argument in command), "source SHA not passed to image build")
        self.assertIn(self.image("cetirun-base"), builds[0])
        vscode = next(command for command in builds if self.image("cetirun-vscode") in command)
        self.assertIn("ACCEPT_VSCODE_SERVER_LICENSE_TERMS=true", vscode)

    def test_license_and_coordinates_fail_before_docker(self):
        for key, value in (("ACCEPT_VSCODE_SERVER_LICENSE_TERMS", "false"), ("REGISTRY", ""),
                           ("VERSION", "latest"), ("GIT_SHA", "unknown")):
            with self.subTest(key=key):
                previous = self.env[key]
                self.env[key] = value
                self.run_release("publish", success=False)
                self.assertEqual(self.commands(), [])
                self.env[key] = previous

    def test_optional_vscode_exclusion_skips_license_gate_and_image(self):
        self.env.pop("ACCEPT_VSCODE_SERVER_LICENSE_TERMS")
        plan = json.loads(self.run_release("plan", "--exclude-image", "cetirun-vscode").stdout)
        self.assertNotIn("cetirun-vscode", {entry["name"] for entry in plan["images"]})
        self.run_release("build-images", "--exclude-image", "cetirun-vscode")
        builds = [command for command in self.commands() if command[:2] == ["buildx", "build"]]
        self.assertEqual(len(builds), 12)
        self.assertFalse(any(self.image("cetirun-vscode") in command for command in builds))

    def test_invalid_image_exclusions_fail_before_docker(self):
        cases = (("unknown", ["not-in-inventory"], "unknown image"),
                 ("required", ["cetirun-portal"], "only exclude optional"),
                 ("duplicate", ["cetirun-vscode", "cetirun-vscode"], "duplicate"))
        for name, excluded, message in cases:
            with self.subTest(name=name):
                result = self.run_release("publish-images", *(item for image in excluded for item in ("--exclude-image", image)), success=False)
                self.assertIn(message, result.stderr)
                self.assertEqual(self.commands(), [])

    def test_existing_broker_or_vscode_or_chart_stops_before_any_build(self):
        for artifact in ("cetirun-agent-broker", "cetirun-vscode", "helm/cetirun-agent-broker"):
            with self.subTest(artifact=artifact):
                self.env["ARTIFACT_EXISTS"] = self.image(artifact)
                self.run_release("publish", success=False)
                self.assertFalse(any(command[:2] == ["buildx", "build"] for command in self.commands()))

    def test_registry_access_error_is_not_treated_as_an_absent_tag(self):
        for error in ("unauthorized: authentication required", "credential helper not found"):
            with self.subTest(error=error):
                self.env["ARTIFACT_REGISTRY_ERROR"] = error
                result = self.run_release("publish-images", success=False)
                self.assertIn("Cannot determine", result.stderr)
                self.assertFalse(any(command[:2] == ["buildx", "build"] for command in self.commands()))

    def test_publish_checks_each_image_and_failure_stops_the_sequence(self):
        self.env["ARTIFACT_FAIL_BUILD"] = self.image("cetirun-agent-broker")
        self.run_release("publish-images", success=False)
        commands = self.commands()
        builds = [command for command in commands if command[:2] == ["buildx", "build"]]
        self.assertIn(self.image("cetirun-agent-broker"), builds[-1])
        self.assertFalse(any(self.image("cetirun-vscode") in command for command in builds))
        for command in builds:
            self.assertIn("--push", command)
            self.assertNotIn("--load", command)

    def test_successful_publish_verifies_all_thirteen_images(self):
        self.run_release("publish-images")
        commands = self.commands()
        builds = [command for command in commands if command[:2] == ["buildx", "build"]]
        self.assertEqual(len(builds), 13)
        last_build = max(index for index, command in enumerate(commands) if command[:2] == ["buildx", "build"])
        self.assertEqual(commands[last_build + 1], ["buildx", "imagetools", "inspect", self.image("cetirun-pi-agent")])
        for name in IMAGES:
            inspect = ["buildx", "imagetools", "inspect", self.image(name)]
            self.assertEqual(commands.count(inspect), 2, "expected one absence check and one post-push inspection")

    @unittest.skipUnless(shutil.which("helm"), "requires existing Helm")
    def test_all_nine_charts_package_with_real_local_helm(self):
        output = self.root / "packages"
        self.run_release("package-charts", "--package-dir", str(output))
        self.assertEqual({path.name for path in output.glob("*.tgz")}, {f"{name}-{self.env['VERSION']}.tgz" for name in CHARTS})
        self.assertEqual(self.commands(), [])


if __name__ == "__main__":
    unittest.main()
