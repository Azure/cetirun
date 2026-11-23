# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Exercise MCP release steps with isolated command doubles, never a registry."""

import json
import hashlib
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[2]
COMPONENT = "cetirun-connections-mcp"
VERSION = "9.8.7"
DIGEST = "sha256:" + "a" * 64
WRAPPER_DIGEST = "sha256:" + "b" * 64
CHART = f"app/{COMPONENT}/chart"
BUILD_SCRIPT = f"scripts/{COMPONENT}-docker-build.sh"
PREFLIGHT = "Authenticate and preflight MCR release artifacts"
LINT = "Lint and template all charts"
BUILD = "Build, push, and verify Linux AMD64 release images"
PUBLISH = "Publish charts and promote latest MCR images"
CLEANUP = "Clean staging MCR image repositories"

# Only these doubles and a small set of local filesystem utilities enter PATH.
# The MCP build helper runs unchanged, but its Docker command is intercepted.
MOCK_TOOL = r'''
import json, os, pathlib, subprocess, sys
tool, args = pathlib.Path(sys.argv[0]).name, sys.argv[1:]
with open(os.environ["MCP_RELEASE_LOG"], "a") as log:
    log.write(json.dumps([tool, *args]) + "\n")
component, version = "cetirun-connections-mcp", os.environ["RELEASE_VERSION"]
if tool == "python3" and args[:1] == ["-B"]:
    args = args[1:]
if tool == "python3" and args[:1] == ["-c"]:
    # Execute the pipeline's inline digest-file writer with real Python. Azure,
    # Docker, Helm and provider verification remain intercepted below.
    sys.exit(subprocess.run([sys.executable, *args], env=os.environ).returncode)
if tool == "python3" and args[:1] == ["scripts/cetirun-release.py"]:
    if args[1] == "publish-images":
        with open(os.environ["MCP_RELEASE_LOG"] + ".driver-env", "a") as output:
            output.write(json.dumps({"license": os.environ.get("ACCEPT_VSCODE_SERVER_LICENSE_TERMS")}) + "\n")
    if args[1] == "package-charts":
        digests = json.loads(pathlib.Path(os.environ["RELEASE_IMAGE_DIGESTS_FILE"]).read_text())
        pathlib.Path(os.environ["MCP_RELEASE_LOG"] + ".digests").write_text(json.dumps(digests))
    sys.exit(subprocess.run([sys.executable, *args], env=os.environ).returncode)
if tool == "bash" and args[0].endswith("-docker-build.sh") and os.environ.get("IMAGE_NAME"):
    marker = pathlib.Path(os.environ["MCP_RELEASE_LOG"] + ".published")
    with marker.open("a") as output:
        output.write(os.environ["REGISTRY"] + "/" + os.environ["IMAGE_NAME"] + ":" + version + "\n")
if tool == "bash" and args == ["scripts/cetirun-connections-mcp-docker-build.sh"]:
    sys.exit(subprocess.run(["/bin/bash", *args], env=os.environ).returncode)
if tool == "az" and args[:3] == ["acr", "repository", "show-tags"]:
    repository = args[args.index("--repository") + 1]
    if repository == os.environ.get("MCP_EXISTING_REPOSITORY"):
        print(version)
elif tool == "az" and args[:3] == ["acr", "repository", "show"]:
    image = args[args.index("--image") + 1]
    if "/staging/" not in image and os.environ.get("MCP_PROMOTION_MISMATCH"):
        print("sha256:" + "c" * 64)
    elif image.endswith("-provenance") or ("/staging/" not in image and "/cetirun-operator:" in image):
        print("sha256:" + "b" * 64)
    else:
        print(os.environ.get("MCP_IMAGE_DIGEST", "sha256:" + "a" * 64))
elif tool == "helm" and args[0] == "package":
    name = next(line.split(":", 1)[1].strip() for line in
                (pathlib.Path(args[1]) / "Chart.yaml").read_text().splitlines()
                if line.startswith("name:"))
    destination = pathlib.Path(args[args.index("--destination") + 1])
    destination.mkdir(parents=True, exist_ok=True)
    with open(os.environ["MCP_RELEASE_LOG"] + ".packages", "a") as output:
        output.write(json.dumps({"name": name, "values": (pathlib.Path(args[1]) / "values.yaml").read_text()}) + "\n")
    if not (name == component and os.environ.get("MCP_SKIP_PACKAGE")):
        (destination / f"{name}-{version}.tgz").touch()
elif tool == "helm" and args[0] == "pull":
    destination = pathlib.Path(args[args.index("--destination") + 1])
    destination.mkdir(parents=True, exist_ok=True)
    (destination / f"{args[1].rsplit('/', 1)[-1]}-{version}.tgz").touch()
elif tool == "python3" and args[0] == "scripts/verify-mcr-image.py":
    if os.environ.get("MCP_FAIL_VERIFY") and f"/{component}:" in args[args.index("--image") + 1]:
        sys.exit(1)
elif tool == "docker" and args[:2] == ["buildx", "build"] and "--file" in args:
    tag = args[args.index("--tag") + 1]
    marker = pathlib.Path(os.environ["MCP_RELEASE_LOG"] + ".published")
    with marker.open("a") as output:
        output.write(tag + "\n")
    pathlib.Path(os.environ["MCP_RELEASE_LOG"] + ".wrapper").write_text(
        pathlib.Path(args[args.index("--file") + 1]).read_text())
elif tool == "docker" and args[:2] == ["image", "inspect"]:
    sys.exit(1)
elif tool == "docker" and args[:3] == ["buildx", "imagetools", "inspect"] and "/staging/" in args[3]:
    marker = pathlib.Path(os.environ["MCP_RELEASE_LOG"] + ".published")
    if not marker.exists() or args[3] not in marker.read_text().splitlines():
        print("manifest unknown", file=sys.stderr)
        sys.exit(1)
'''


class ConnectionsMCPReleaseTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="cetirun-mcp-release-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.log = self.root / "commands.jsonl"
        self.env = {
            "PATH": str(self.bin), "MCP_RELEASE_LOG": str(self.log),
            "ACR_NAME": "synthetic", "REPOSITORY_PREFIX": "public/example/cetirun",
            "RELEASE_VERSION": VERSION, "BUILD_ID": "42", "FORCE_PUBLISH": "false",
            "AGENT_TEMPDIRECTORY": str(self.root / "temporary"), "HELM_BIN": str(self.bin),
            "GIT_SHA": "a" * 40, "ACCEPT_VSCODE_SERVER_LICENSE_TERMS": "true",
        }
        for name in ("awk", "dirname", "grep", "mkdir", "rm", "tr", "cmp", "git"):
            executable = shutil.which(name)
            self.assertIsNotNone(executable, f"requires {name}")
            (self.bin / name).symlink_to(executable)
        for name in ("az", "docker", "helm", "bash", "python3"):
            executable = self.bin / name
            executable.write_text(f"#!{sys.executable}\n" + MOCK_TOOL)
            executable.chmod(0o755)
        manifests = [*ROOT.glob("app/*/chart/Chart.yaml"),
                     ROOT / "app/cetirun-chart/Chart.yaml",
                     ROOT / "app/cetirun-platform/Chart.yaml", ROOT / "infra/istio/Chart.yaml"]
        for source in manifests:
            target = self.root / source.relative_to(ROOT)
            target.parent.mkdir(parents=True, exist_ok=True)
            name = yaml.safe_load(source.read_text())["name"]
            target.write_text(f'name: {name}\nversion: {VERSION}\nappVersion: "{VERSION}"\n')
            shutil.copyfile(source.parent / "values.yaml", target.parent / "values.yaml")
        (self.root / "scripts").mkdir()
        inventory = json.loads((ROOT / "scripts/release-artifacts.json").read_text())
        for path in [entry["script"] for entry in inventory["images"]] + [
            "scripts/release-artifacts.json", "scripts/cetirun-release.py",
            "scripts/cetirun-release-metadata.sh", "scripts/cetirun-runtime-chart-lock.py",
        ]:
            shutil.copy2(ROOT / path, self.root / path)
        for runtime in ("openclaw", "hermes"):
            relative = Path("app/cetirun-chart/agents") / runtime / "profile.yaml"
            (self.root / relative).parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / relative, self.root / relative)
        # Use the real transformation helpers. Full source consistency is
        # exercised separately; this fixture uses minimal metadata, values and
        # runtime profiles rather than the complete chart source.
        (self.root / "scripts/cetirun-version.py").write_text(
            (ROOT / "scripts/cetirun-version.py").read_text().partition('if __name__ == "__main__":')[0])
        subprocess.run([shutil.which("git"), "-c", "init.defaultBranch=main", "init", "--quiet"], cwd=self.root, check=True)
        subprocess.run([shutil.which("git"), "add", "app", "infra", "scripts"], cwd=self.root, check=True)
        pipeline = yaml.safe_load((ROOT / ".pipelines/release-mcr.yml").read_text())
        self.pipeline = pipeline
        self.steps = {step["displayName"]: step for step in pipeline["jobs"][0]["steps"]
                      if "displayName" in step}
        self.repository = f'{self.env["REPOSITORY_PREFIX"]}/{COMPONENT}'
        self.chart_repository = f'{self.env["REPOSITORY_PREFIX"]}/helm/{COMPONENT}'
        self.image = f'synthetic.azurecr.io/{self.repository}:{VERSION}'
        self.staged = f'synthetic.azurecr.io/{self.env["REPOSITORY_PREFIX"]}/staging/42/{COMPONENT}:{VERSION}'
        self.archive = self.root / f"temporary/cetirun-chart-packages/{COMPONENT}-{VERSION}.tgz"

    def run_step(self, name, *, success=True):
        step = self.steps[name]
        script = step.get("bash") or step["inputs"]["inlineScript"]
        result = subprocess.run(["/bin/bash", "-c", script], cwd=self.root, env=self.env,
                                capture_output=True, text=True, timeout=30)
        if success:
            self.assertEqual(result.returncode, 0, f"{name}: {result.stdout}\n{result.stderr}")
        else:
            self.assertNotEqual(result.returncode, 0, f"{name} unexpectedly succeeded")
        return result

    def commands(self):
        return [json.loads(line) for line in self.log.read_text().splitlines()]

    def test_default_tag_run_preserves_configured_ci_license_and_failure_boundaries(self):
        import re
        defaults = {parameter["name"]: parameter["default"] for parameter in self.pipeline["parameters"]}
        self.assertNotIn("acceptVSCodeServerLicenseTerms", defaults)
        for name in ("Resolve and validate unified release version", BUILD):
            configured = self.steps[name].get("env", {}).get("ACCEPT_VSCODE_SERVER_LICENSE_TERMS")
            self.env.pop("ACCEPT_VSCODE_SERVER_LICENSE_TERMS", None)
            if configured is not None:
                match = re.fullmatch(r"\$\{\{ parameters\.(\w+) }}", configured)
                self.env["ACCEPT_VSCODE_SERVER_LICENSE_TERMS"] = str(defaults[match[1]] if match else configured).lower()
            self.env.update(BUILD_SOURCEBRANCH=f"refs/tags/v{VERSION}", BUILD_REASON="IndividualCI",
                            RELEASE_VERSION_INPUT="", FORCE_PUBLISH_INPUT="false")
            self.run_step(name)
        observed = json.loads(Path(str(self.log) + ".driver-env").read_text().splitlines()[-1])
        self.assertEqual(observed["license"], "true")
        self.env["FORCE_PUBLISH_INPUT"] = "true"
        result = self.run_step("Resolve and validate unified release version", success=False)
        self.assertIn("manual", result.stderr.lower())
        self.env.update(FORCE_PUBLISH_INPUT="false", RELEASE_VERSION_INPUT="0.0.0")
        result = self.run_step("Resolve and validate unified release version", success=False)
        self.assertIn("match tag version", result.stderr)

    def test_mcp_follows_the_complete_release_lifecycle(self):
        for name in ("Validate all chart versions", LINT, PREFLIGHT, BUILD):
            self.run_step(name)
        self.assertFalse(self.archive.exists(), "charts must wait for verified staging image digests")
        self.assertFalse(any(c[:2] == ["helm", "package"] for c in self.commands()))
        for name in (PUBLISH, CLEANUP):
            self.run_step(name)
        commands = self.commands()
        self.assertTrue(self.archive.is_file(), "MCP chart was not packaged")
        packages = [json.loads(line) for line in Path(str(self.log) + ".packages").read_text().splitlines()]
        packaged = next(item for item in packages if item["name"] == COMPONENT)
        self.assertIn("mcr.microsoft.com/aks/ai-runtime/cetirun/" + COMPONENT, packaged["values"])
        self.assertNotIn("staging/", packaged["values"])
        self.assertNotIn("synthetic.azurecr.io", packaged["values"])
        # MCR serves the synchronized public artifacts. Every write remains
        # on the backing ACR, including all inventory images and chart pushes.
        built = Path(str(self.log) + ".published").read_text().splitlines()
        inventory = json.loads((ROOT / "scripts/release-artifacts.json").read_text())
        digests = json.loads(Path(str(self.log) + ".digests").read_text())
        self.assertEqual(digests, {entry["name"]: DIGEST for entry in inventory["images"]})
        wrapper_lines = Path(str(self.log) + ".wrapper").read_text().splitlines()
        self.assertEqual(len(wrapper_lines), 2)
        self.assertEqual(wrapper_lines[0], f"FROM synthetic.azurecr.io/{self.env['REPOSITORY_PREFIX']}/staging/42/cetirun-operator:{VERSION}@{DIGEST}")
        label = wrapper_lines[1].split("=", 1)[1]
        chart_digests = json.loads(json.loads(label))
        self.assertEqual(set(chart_digests), {entry["name"] for entry in inventory["charts"]})
        self.assertEqual(chart_digests, {entry["name"]: "sha256:" + hashlib.sha256(
            (self.archive.parent / (entry["name"] + "-" + VERSION + ".tgz")).read_bytes()).hexdigest()
            for entry in inventory["charts"]})
        runtime_values = yaml.safe_load(next(item["values"] for item in packages if item["name"] == "cetirun"))
        for runtime in ("openclaw", "hermes", "desktop", "ttyd"):
            self.assertEqual(runtime_values[runtime]["image"]["digest"], DIGEST)
        self.assertEqual(runtime_values["memoryContext"]["image"],
                         f"mcr.microsoft.com/aks/ai-runtime/cetirun/cetirun-memory-context:{VERSION}@{DIGEST}")
        first_package = next(i for i, c in enumerate(commands) if c[:2] == ["helm", "package"])
        digest_reads = [i for i, c in enumerate(commands) if c[:4] == ["az", "acr", "repository", "show"]]
        self.assertEqual(len(digest_reads), len(inventory["images"]) * 2 + 2)
        self.assertLess(digest_reads[len(inventory["images"]) - 1], first_package)
        self.assertGreater(digest_reads[len(inventory["images"])], first_package)
        expected_built = {
            f"synthetic.azurecr.io/{self.env['REPOSITORY_PREFIX']}/staging/42/{entry['name']}:{VERSION}"
            for entry in inventory["images"]
        }
        expected_built.add(f"synthetic.azurecr.io/{self.env['REPOSITORY_PREFIX']}/staging/42/cetirun-operator:{VERSION}-provenance")
        self.assertEqual(set(built), expected_built)
        pushed_charts = [c for c in commands if c[:2] == ["helm", "push"]]
        self.assertEqual(len(pushed_charts), len(inventory["charts"]))
        for command in pushed_charts:
            self.assertEqual(command[-1], f"oci://synthetic.azurecr.io/{self.env['REPOSITORY_PREFIX']}/helm")
        promoted_targets = [command[command.index("--tag") + 1] for command in commands
                            if command[:4] == ["docker", "buildx", "imagetools", "create"]]
        self.assertCountEqual(promoted_targets, [
            f"synthetic.azurecr.io/{self.env['REPOSITORY_PREFIX']}/{entry['name']}:{tag}"
            for entry in inventory["images"] for tag in (VERSION, "latest")
        ])
        for operation in ("lint", "template", "package"):
            command = next(c for c in commands if c[:2] == ["helm", operation] and any(arg.endswith(CHART) for arg in c))
            if operation == "package":
                self.assertEqual(command[command.index("--version") + 1], VERSION)
                self.assertEqual(command[command.index("--app-version") + 1], VERSION)
            else:
                self.assertTrue(any(arg.endswith(f"{CHART}/ci/render-values.yaml") for arg in command))
        for repository in (self.repository, self.chart_repository):
            self.assertIn(["az", "acr", "repository", "show-tags", "--name", "synthetic",
                           "--repository", repository, "--output", "tsv"], commands)
        build = next(c for c in commands if c[:3] == ["docker", "buildx", "build"] and self.staged in c)
        self.assertIn("--push", build)
        self.assertNotIn("--load", build)
        self.assertEqual(build[build.index("--platform") + 1], "linux/amd64")
        self.assertIn(f"VERSION={VERSION}", build)
        build_root = Path(build[-1])
        self.assertNotEqual(build_root, self.root, "image builds must use the filtered source snapshot")
        self.assertIn(str(build_root / "app/cetirun-connections-mcp/Dockerfile"), build)
        self.assertIn(["docker", "buildx", "imagetools", "inspect", self.staged], commands)
        promoted = ["docker", "buildx", "imagetools", "create", "--prefer-index=false", "--tag", self.image, self.staged]
        self.assertIn(promoted, commands)
        operator_image = f"synthetic.azurecr.io/{self.env['REPOSITORY_PREFIX']}/cetirun-operator:{VERSION}"
        self.assertIn(["docker", "buildx", "imagetools", "create", "--prefer-index=false", "--tag", operator_image,
                       f"synthetic.azurecr.io/{self.env['REPOSITORY_PREFIX']}/staging/42/cetirun-operator:{VERSION}-provenance"], commands)
        self.assertIn(["python3", "scripts/verify-mcr-image.py", "--image",
                       f"mcr.microsoft.com/aks/ai-runtime/cetirun/cetirun-operator:{VERSION}", "--digest", WRAPPER_DIGEST], commands)
        self.assertIn(["docker", "buildx", "imagetools", "inspect", self.image], commands)
        destination = f'oci://synthetic.azurecr.io/{self.env["REPOSITORY_PREFIX"]}/helm'
        push = ["helm", "push", str(self.archive), destination]
        self.assertIn(push, commands)
        self.assertLess(commands.index(promoted), commands.index(push))
        self.assertIn(["helm", "pull", f"{destination}/{COMPONENT}", "--version", VERSION,
                       "--destination", str(self.root / "temporary/cetirun-chart-verify")], commands)
        verify = ["python3", "scripts/verify-mcr-image.py", "--image",
                  f"mcr.microsoft.com/aks/ai-runtime/cetirun/{COMPONENT}:{VERSION}", "--digest", DIGEST]
        latest = ["docker", "buildx", "imagetools", "create", "--prefer-index=false", "--tag",
                  self.image.rsplit(":", 1)[0] + ":latest", self.image]
        self.assertIn(verify, commands)
        self.assertIn(latest, commands)
        self.assertLess(commands.index(verify), commands.index(latest))
        self.assertIn(["az", "acr", "repository", "delete", "--name", "synthetic",
                       "--repository", f'{self.env["REPOSITORY_PREFIX"]}/staging/42/{COMPONENT}',
                       "--yes", "--output", "none"], commands)
        self.assertEqual(self.steps[CLEANUP]["condition"], "and(always(), eq(variables['stagingStarted'], 'true'))")

    def test_mcp_chart_version_mismatch_fails_validation(self):
        manifest = self.root / CHART / "Chart.yaml"
        manifest.write_text(manifest.read_text().replace(VERSION, "0.0.0"))
        result = self.run_step("Validate all chart versions", success=False)
        self.assertIn(f"{CHART}/Chart.yaml", result.stderr)

    def test_existing_mcp_image_or_chart_blocks_release_preflight(self):
        for repository in (self.repository, self.chart_repository):
            with self.subTest(repository=repository):
                self.env["MCP_EXISTING_REPOSITORY"] = repository
                result = self.run_step(PREFLIGHT, success=False)
                self.assertIn(f"{repository}:{VERSION} already exists", result.stderr)

    def test_missing_mcp_package_fails_packaging(self):
        for name in (LINT, PREFLIGHT, BUILD):
            self.run_step(name)
        self.env["MCP_SKIP_PACKAGE"] = "1"
        result = self.run_step(PUBLISH, success=False)
        self.assertIn(f"Missing packaged chart: {COMPONENT}", result.stderr)
        self.assertFalse(any(c[:2] == ["helm", "push"] for c in self.commands()))
        self.assertFalse(Path(str(self.log) + ".wrapper").exists())
        self.assertFalse(any(c[:4] == ["docker", "buildx", "imagetools", "create"] and ":latest" not in c
                            for c in self.commands()))
        self.assertFalse(any(argument.endswith(":latest")
                             for command in self.commands() for argument in command))

    def test_failed_mcr_verification_blocks_latest(self):
        for name in (LINT, PREFLIGHT, BUILD):
            self.run_step(name)
        self.env["MCP_FAIL_VERIFY"] = "1"
        self.run_step(PUBLISH, success=False)
        self.assertTrue(self.archive.is_file())
        self.assertIn(["python3", "scripts/verify-mcr-image.py", "--image",
                       f"mcr.microsoft.com/aks/ai-runtime/cetirun/{COMPONENT}:{VERSION}", "--digest", DIGEST], self.commands())
        self.assertFalse(any(argument.endswith(":latest")
                             for command in self.commands() for argument in command))

    def test_changed_promoted_digest_prevents_chart_push_and_latest(self):
        for name in (LINT, PREFLIGHT, BUILD):
            self.run_step(name)
        self.env["MCP_PROMOTION_MISMATCH"] = "1"
        result = self.run_step(PUBLISH, success=False)
        self.assertIn("differs from the staged digest", result.stderr)
        self.assertFalse(any(c[:2] == ["helm", "push"] for c in self.commands()))
        self.assertFalse(any(argument.endswith(":latest") for command in self.commands() for argument in command))

    def test_invalid_staged_digest_prevents_packaging_and_latest(self):
        for name in (LINT, PREFLIGHT, BUILD):
            self.run_step(name)
        self.env["MCP_IMAGE_DIGEST"] = "invalid"
        result = self.run_step(PUBLISH, success=False)
        self.assertIn("invalid digest", result.stderr)
        commands = self.commands()
        self.assertFalse(any(c[:2] in (["helm", "package"], ["helm", "push"]) for c in commands))
        self.assertFalse(any(argument.endswith(":latest")
                             for command in commands for argument in command))


if __name__ == "__main__":
    unittest.main()
