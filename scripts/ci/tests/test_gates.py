# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Self-contained helper regressions: no cloud, tool installs, or third-party modules."""

from contextlib import redirect_stderr
import hashlib
from http.server import BaseHTTPRequestHandler, HTTPServer
import io
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import socket
import subprocess
import sys
import tarfile
import tempfile
import textwrap
import threading
import time
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import install_tools
import python_safety
import scan_image
import validate


class GateTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory(prefix="cetirun-gate-test-")
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.module = self.root / "app/cetirun-operator"
        self.module.mkdir(parents=True)
        (self.module / "go.mod").write_text("module example.invalid/operator\n")

    def test_bad_root_fails_before_running_tools(self):
        with patch.object(validate, "run") as run, redirect_stderr(io.StringIO()):
            self.assertEqual(
                validate.main(["chart-check", "--root", str(self.root / "missing")]), 1
            )
        run.assert_not_called()

    def test_missing_license_validator_is_a_failure(self):
        with patch.object(validate, "run") as run:
            with self.assertRaisesRegex(
                ValueError, "requires scripts/check-license-headers.py"
            ):
                validate.license_gate(self.root)
        run.assert_not_called()

    def test_license_contract_passes_explicit_root(self):
        checker = self.root / "scripts/check-license-headers.py"
        checker.parent.mkdir()
        checker.write_text("# fixture")
        with patch.object(validate, "run") as run:
            validate.license_gate(self.root)
        self.assertEqual(
            run.call_args.args[0],
            [sys.executable, "-B", str(checker), "--root", str(self.root)],
        )

    def test_license_failure_propagates(self):
        checker = self.root / "scripts/check-license-headers.py"
        checker.parent.mkdir()
        checker.write_text("# fixture")
        with patch.object(
            validate, "run", side_effect=subprocess.CalledProcessError(1, "checker")
        ):
            with redirect_stderr(io.StringIO()):
                self.assertEqual(
                    validate.main(["license-check", "--root", str(self.root)]), 1
                )

    def test_docs_contract_requires_site_makefile(self):
        with self.assertRaisesRegex(ValueError, "requires site/Makefile"):
            validate.docs_gate(self.root)
        (self.root / "site").mkdir()
        (self.root / "site/Makefile").write_text("check:\n")
        with patch.object(validate, "run") as run:
            validate.docs_gate(self.root)
        self.assertEqual(run.call_args.args[0], ["make", "-C", "site", "check"])

    def test_chart_inventory_is_validated_before_execution(self):
        with patch.object(validate, "run") as run:
            with self.assertRaises(ValueError):
                validate.chart_gate(self.root)
        run.assert_not_called()

    def test_all_eight_charts_and_both_runtime_types_are_checked(self):
        for directory, _ in validate.CHARTS:
            chart = self.root / directory
            chart.mkdir(parents=True, exist_ok=True)
            for filename in ("Chart.yaml", "values.yaml"):
                (chart / filename).write_text("fixture")
        (self.root / "scripts").mkdir()
        (self.root / "scripts/cetirun-helm-test.sh").write_text("# fixture")
        (self.root / "scripts/cetirun-platform-helm-test.sh").write_text("# fixture")
        (self.root / "scripts/cetirun-istio-helm-test.sh").write_text("# fixture")
        (self.root / "scripts/cetirun-agent-broker-helm-test.sh").write_text("# fixture")
        (self.root / "scripts/cetirun-ai-gateway-helm-test.sh").write_text("# fixture")
        with patch.object(validate, "run") as run:
            validate.chart_gate(self.root)
        commands = [call.args[0] for call in run.call_args_list]
        self.assertEqual(sum(c[:2] == ["helm", "lint"] for c in commands), 9)
        self.assertEqual(sum(c[:2] == ["helm", "template"] for c in commands), 9)
        self.assertEqual(
            {str(Path(c[3]).relative_to(self.root)) for c in commands if c[:2] == ["helm", "lint"]},
            {
                "app/cetirun-chart", "app/cetirun-operator/chart", "app/cetirun-portal/chart",
                "app/agent-tools-mcp/chart", "app/cetirun-agent-broker/chart", "app/cetirun-ai-gateway/chart",
                "app/cetirun-platform", "infra/istio",
            },
        )
        self.assertTrue(all("--strict" in c for c in commands if c[1] == "lint"))
        self.assertEqual(sum(c[:2] == ["helm", "dependency"] for c in commands), 1)
        self.assertEqual(sum(c[:2] == ["bash", str(self.root / "scripts/cetirun-istio-helm-test.sh")] for c in commands), 1)
        self.assertEqual(
            [Path(c[1]).name for c in commands if c[0] == "bash"],
            [
                "cetirun-helm-test.sh",
                "cetirun-platform-helm-test.sh",
                "cetirun-istio-helm-test.sh",
                "cetirun-agent-broker-helm-test.sh",
                "cetirun-ai-gateway-helm-test.sh",
            ],
        )

    def test_offline_go_environment_cannot_inherit_network_defaults(self):
        with patch.dict(
            "os.environ", {"GOPROXY": "https://example.invalid", "GOTOOLCHAIN": "auto"}
        ):
            env = validate.offline_env()
        self.assertEqual(env["GOPROXY"], "off")
        self.assertEqual(env["GOTOOLCHAIN"], "local")
        self.assertEqual(env["GOFLAGS"], "-mod=readonly")
        self.assertEqual(env["npm_config_offline"], "true")

    def test_missing_frontend_dependencies_fail(self):
        web = self.root / "app/cetirun-portal/web"
        web.mkdir(parents=True)
        (web / "package-lock.json").write_text("{}")
        with patch.object(validate, "run") as run:
            with self.assertRaisesRegex(ValueError, "dependencies are missing"):
                validate.portal_gate(self.root, "test")
        run.assert_not_called()

    def test_portal_embed_fixture_is_disposable(self):
        portal = self.root / "app/cetirun-portal"
        (portal / "web").mkdir(parents=True)
        (portal / "go.mod").write_text("module example.invalid/portal\n")
        shared = self.root / "app/internal"
        shared.mkdir()
        (shared / "go.mod").write_text("module example.invalid/internal\n")
        with validate.go_directory(self.root, "portal") as directory:
            self.assertNotEqual(directory, portal)
            self.assertTrue((directory / "web/dist/index.html").is_file())
            self.assertEqual(
                (directory.parent / "internal/go.mod").read_text(),
                (shared / "go.mod").read_text(),
            )
            self.assertTrue((directory.parent / "cetirun-operator/go.mod").is_file())
        self.assertFalse((portal / "web/dist").exists())
        self.assertFalse(directory.exists())

    def test_shared_internal_module_has_its_own_go_gate(self):
        shared = self.root / "app/internal"
        shared.mkdir()
        (shared / "go.mod").write_text("module example.invalid/internal\n")
        with patch.object(validate, "run") as run:
            validate.go_gate(self.root, "test", "internal")
        self.assertEqual(run.call_args.args[:2], (["go", "test", "./...", "-count=1"], shared))

    def test_operator_go_test_uses_the_component_test_target(self):
        with patch.object(validate, "run") as run:
            validate.go_gate(self.root, "test", "operator")
        self.assertEqual(run.call_args.args[:2], (["make", "test"], self.module))
        self.assertEqual(run.call_args.kwargs["env"]["GOPROXY"], "off")
        self.assertEqual(run.call_args.kwargs["env"]["GOFLAGS"], "-mod=readonly")

    def test_operator_test_failure_propagates(self):
        with patch.object(
            validate, "run", side_effect=subprocess.CalledProcessError(1, "make")
        ), redirect_stderr(io.StringIO()):
            self.assertEqual(
                validate.main(["go-test", "--component", "operator", "--root", str(self.root)]),
                1,
            )

    def test_missing_tools_never_pass(self):
        with patch("shutil.which", return_value=None):
            with self.assertRaisesRegex(ValueError, "Required tool is missing"):
                validate.run(["missing"], self.root)

    def test_workflow_gate_requires_shellcheck_and_all_owned_workflows(self):
        directory = self.root / ".github/workflows"
        directory.mkdir(parents=True)
        for name in validate.WORKFLOWS:
            (directory / name).write_text("name: fixture\n")
        with patch("shutil.which", return_value=None):
            with self.assertRaisesRegex(ValueError, "requires shellcheck"):
                validate.workflow_gate(self.root)
        with patch("shutil.which", return_value="/fixture/shellcheck"):
            with patch.object(validate, "run") as run:
                validate.workflow_gate(self.root)
        command = run.call_args.args[0]
        self.assertEqual(len(command), 2 + len(validate.WORKFLOWS))
        self.assertNotIn(str(directory / "e2e.yml"), command)

    def test_image_exists_before_scanner_download(self):
        with patch.object(scan_image, "run") as run:
            self.assertEqual(scan_image.main(["--image", scan_image.IMAGES[0]]), 0)
        first, second = [call.args[0] for call in run.call_args_list]
        self.assertEqual(first[:3], ["docker", "image", "inspect"])
        self.assertIn("--exit-code", second)
        self.assertIn("CRITICAL,HIGH", second)
        self.assertIn("--ignore-unfixed", second)
        self.assertIn(scan_image.TRIVY, second)

    def test_scan_failure_fails_gate(self):
        with patch.object(
            scan_image, "run", side_effect=subprocess.CalledProcessError(1, "docker")
        ):
            with redirect_stderr(io.StringIO()):
                self.assertEqual(scan_image.main(["--image", scan_image.IMAGES[0]]), 1)


class ToolTests(unittest.TestCase):
    def test_download_mismatch_is_rejected(self):
        with self.assertRaisesRegex(ValueError, "SHA256 mismatch"):
            install_tools.binary_bytes(b"tampered", "0" * 64)

    def test_tar_reads_only_named_regular_member(self):
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode="w:gz") as archive:
            for name, payload in (("tool", b"binary"), ("../../escape", b"malicious")):
                info = tarfile.TarInfo(name)
                info.size = len(payload)
                archive.addfile(info, io.BytesIO(payload))
        data = buffer.getvalue()
        self.assertEqual(
            install_tools.binary_bytes(data, hashlib.sha256(data).hexdigest(), "tool"),
            b"binary",
        )

    def test_symlink_member_is_rejected(self):
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode="w:gz") as archive:
            info = tarfile.TarInfo("tool")
            info.type = tarfile.SYMTYPE
            info.linkname = "/etc/passwd"
            archive.addfile(info)
        data = buffer.getvalue()
        with self.assertRaisesRegex(ValueError, "regular file"):
            install_tools.binary_bytes(data, hashlib.sha256(data).hexdigest(), "tool")

    def test_all_tool_pins_support_both_ci_and_local_architecture(self):
        manifest = json.loads(install_tools.MANIFEST.read_text())
        with tempfile.TemporaryDirectory() as temp:
            for machine in ("x86_64", "aarch64"):
                entries = install_tools.plan(
                    manifest, list(manifest), Path(temp), "Linux", machine
                )
                self.assertEqual(len(entries), 7)
                self.assertTrue(all(len(entry[2]) == 64 for entry in entries))

    def test_helm_provenance_covers_both_linux_architectures_and_stays_on_v3(self):
        manifest = json.loads(install_tools.MANIFEST.read_text())
        helm = manifest["helm"]
        self.assertTrue(helm["version"].startswith("3."))
        self.assertEqual(
            helm["source"], f"https://github.com/helm/helm/releases/tag/v{helm['version']}"
        )
        with tempfile.TemporaryDirectory() as directory:
            for machine, arch in (("x86_64", "amd64"), ("aarch64", "arm64")):
                with self.subTest(arch=arch):
                    self.assertEqual(
                        install_tools.plan(manifest, ["helm"], Path(directory), "Linux", machine),
                        [
                            (
                                Path(directory) / "helm",
                                f"https://get.helm.sh/helm-v{helm['version']}-linux-{arch}.tar.gz",
                                helm["sha256"][arch],
                                f"linux-{arch}/helm",
                            )
                        ],
                    )

    def test_bad_input_is_rejected_before_network(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            with patch.object(install_tools, "urlopen") as fetch:
                with self.assertRaises(ValueError):
                    install_tools.plan({}, [], directory, "Darwin", "arm64")
                with self.assertRaises(ValueError):
                    install_tools.plan({}, ["unknown"], directory, "Linux", "arm64")
                with self.assertRaisesRegex(ValueError, "only once"):
                    install_tools.plan(
                        {}, ["helm", "helm"], directory, "Linux", "arm64"
                    )
            fetch.assert_not_called()

    def test_existing_binary_is_not_overwritten(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            (directory / "helm").write_text("user binary")
            with self.assertRaisesRegex(ValueError, "Refusing to overwrite"):
                install_tools.plan({"helm": {}}, ["helm"], directory, "Linux", "arm64")


class PythonSafetyTests(unittest.TestCase):
    def test_external_dns_is_blocked_before_resolution(self):
        with self.assertRaisesRegex(AssertionError, "External DNS"):
            python_safety.guard_resolve(lambda *a: None)("example.com", 443)

    def test_non_loopback_connections_are_blocked(self):
        for host in ("169.254.169.254", "8.8.8.8", "login.microsoftonline.com"):
            with self.subTest(host=host):
                with self.assertRaisesRegex(AssertionError, "Outbound network"):
                    python_safety.guard_connect(lambda *a: None)(
                        type("Socket", (), {"family": socket.AF_INET})(), (host, 443)
                    )

    def test_loopback_auth_server_is_allowed(self):
        self.assertEqual(
            python_safety.guard_connect(lambda *a: "connected")(
                type("Socket", (), {"family": socket.AF_INET})(), ("127.0.0.1", 8000)
            ),
            "connected",
        )

    def test_cloud_credentials_and_unimplemented_hermes_fail_closed(self):
        with self.assertRaises(AssertionError):
            python_safety.NoCloudCredential().get_token("scope")
        with self.assertRaises(AssertionError):
            python_safety.UnusedHermesInterface()


class WorkflowContractTests(unittest.TestCase):
    """Check small cross-file invariants; actionlint handles YAML/Actions semantics."""

    def test_operator_suite_budget_is_bounded_and_shared_by_all_entrypoints(self):
        makefile = (validate.ROOT / "app/cetirun-operator/Makefile").read_text()
        self.assertIn("GO_TEST_TIMEOUT := 20m", makefile)
        for target in ("test", "test-envtest"):
            with self.subTest(target=target):
                commands = makefile.split(f"\n{target}:\n", 1)[1].split("\n\n", 1)[0]
                self.assertIn("-timeout=$(GO_TEST_TIMEOUT)", commands)
                self.assertIn("-count=1", commands)
                self.assertNotIn("-run", commands)
                self.assertNotIn("|| true", commands)
        self.assertIn("go test ./... -v", makefile)
        for name in ("operator-ci.yml", "operator-release.yml"):
            with self.subTest(workflow=name):
                text = (validate.ROOT / ".github/workflows" / name).read_text()
                steps = {
                    step.splitlines()[0]: step
                    for step in text.split("\n      - name: ")[1:]
                }
                self.assertIn("make test", steps["Run go test"])
                self.assertNotIn("go test ./...", steps["Run go test"])
                self.assertNotIn("continue-on-error", steps["Run go test"])
                self.assertIn("timeout-minutes: 60", text)
        self.assertIn(
            "go -C ../internal test ./... -v -count=1",
            steps["Run go test"],
        )

    def test_operator_envtest_and_helm_use_manifest_installer_without_duplicate_pins(self):
        text = (validate.ROOT / ".github/workflows/operator-ci.yml").read_text()
        manifest = json.loads(install_tools.MANIFEST.read_text())
        command = (
            'python3 -B ../../scripts/ci/install_tools.py --bin-dir "$tools_dir" envtest helm'
        )
        self.assertIn("working-directory: app/cetirun-operator", text)
        self.assertIn(
            'tools_dir="$RUNNER_TEMP/cetirun-admission-tools-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT"',
            text,
        )
        self.assertIn(command, text)
        self.assertLess(text.index(command), text.index("run: make test-envtest"))
        self.assertIn('echo "$tools_dir" >> "$GITHUB_PATH"', text)
        self.assertIn('echo "KUBEBUILDER_ASSETS=$tools_dir" >> "$GITHUB_ENV"', text)
        self.assertNotRegex(text, r"\b(?:curl|tar|sha256sum)\b")
        for tool in ("envtest", "helm"):
            with self.subTest(tool=tool):
                spec = manifest[tool]
                self.assertNotIn(spec["version"], text)
                for checksum in spec["sha256"].values():
                    self.assertNotIn(checksum, text)

    def test_license_update_helper_only_proposes_a_patch(self):
        text = (validate.ROOT / ".github/workflows/portal-ci.yml").read_text()
        self.assertIn('npm run licenses:check-updates -- --output "$RUNNER_TEMP/portal-license-update.patch"', text)
        self.assertIn("steps.license-supplements.outcome == 'failure'", text)
        self.assertIn("name: portal-license-update", text)
        self.assertLess(text.index("Check version-pinned license supplements"), text.index("Install dependencies"))
        self.assertNotIn(": write", text)
        self.assertNotIn("pull_request_target:", text)
        self.assertNotIn("continue-on-error:", text)
        self.assertNotRegex(text, r"git (?:apply|commit|push)")

    def test_portal_restart_api_uses_the_existing_isolated_admission_job(self):
        text = (validate.ROOT / ".github/workflows/operator-ci.yml").read_text()
        steps = {
            step.splitlines()[0]: step
            for step in text.split("\n      - name: ")[1:]
        }
        download = steps["Download Portal API test modules"]
        check = steps["Test Portal restart API admission"]
        self.assertIn("app/cetirun-portal/go.sum", text)
        self.assertIn("working-directory: app/cetirun-portal", download)
        self.assertIn("run: go mod download", download)
        self.assertIn("working-directory: app/cetirun-portal", check)
        self.assertIn('GOPROXY: "off"', check)
        self.assertIn('GOFLAGS: "-mod=readonly"', check)
        self.assertIn("KUBECONFIG: /dev/null", check)
        self.assertIn('USE_EXISTING_CLUSTER: "false"', check)
        self.assertIn("run: go test -tags=envtest ./internal/kube -run '^TestRetainedRestartThroughServedAPI$' -count=1", check)
        self.assertLess(text.index('echo "KUBEBUILDER_ASSETS=$tools_dir"'), text.index(download))
        self.assertLess(text.index(download), text.index(check))
        self.assertNotIn("continue-on-error:", check)

    def test_runtime_image_smoke_selects_only_the_auth_unittest_suite(self):
        text = (validate.ROOT / ".github/workflows/docker-build-validate.yml").read_text()
        commands = re.findall(r"[a-z-]+:validation -m unittest discover[^\n]+", text)
        self.assertEqual(
            commands,
            ["hermes-gateway:validation -m unittest discover -s /tests -p test_auth_service.py -v"],
        )

    def test_runtime_scans_extend_timeout_without_weakening_policy(self):
        text = (validate.ROOT / ".github/workflows/docker-build-validate.yml").read_text()
        scans = {
            step.splitlines()[0]: step
            for step in text.split("\n      - name: ")[1:]
            if "            image \\\n" in step
        }
        self.assertEqual(len(scans), 6)
        for name, step in scans.items():
            with self.subTest(step=name):
                self.assertEqual(step.count("--timeout 10m"), 1)
                self.assertIn("--ignore-unfixed", step)
                self.assertNotRegex(step, r"--(?:scanners|skip-dirs|skip-files)\b")
        gates = {
            "Scan image for vulnerabilities (Trivy)": "CRITICAL,HIGH",
            "Scan Hermes image for vulnerabilities (Trivy)": "CRITICAL",
            "Scan desktop image for vulnerabilities (Trivy)": "CRITICAL",
            "Scan base and Pi agent images for vulnerabilities (Trivy)": "CRITICAL,HIGH",
        }
        for name, severity in gates.items():
            with self.subTest(gate=name):
                self.assertIn("--exit-code 1", scans[name])
                self.assertIn(f"--severity {severity} \\", scans[name])
                self.assertNotIn("continue-on-error:", scans[name])
                self.assertNotIn("\n        if:", scans[name])

    def test_owned_actions_are_sha_pinned_and_default_to_read_only(self):
        for name in validate.WORKFLOWS:
            with self.subTest(workflow=name):
                text = (validate.ROOT / ".github/workflows" / name).read_text()
                self.assertIn("\npermissions:\n  contents: read\n", text)
                self.assertNotIn("pull_request_target:", text)
                for action in re.findall(r"^\s+uses:\s+(\S+)", text, re.MULTILINE):
                    self.assertRegex(action, r"^[^@]+@[0-9a-f]{40}$")

    def test_status_job_names_are_distinct(self):
        names = []
        for name in validate.WORKFLOWS:
            text = (validate.ROOT / ".github/workflows" / name).read_text()
            names.extend(re.findall(r"^    name: (.+)$", text, re.MULTILINE))
        self.assertEqual(len(names), len(set(names)))
        for expected in (
            "Operator Go checks",
            "Portal Go checks",
            "Agent Identity Broker Go checks",
            "Operator image CVEs",
            "Portal image CVEs",
            "Agent Identity Broker image CVEs",
        ):
            self.assertIn(expected, names)

    def test_scans_reuse_each_component_build(self):
        for component in validate.COMPONENTS:
            text = (validate.ROOT / f".github/workflows/{component}-ci.yml").read_text()
            images = [component, "terminal"] if component == "operator" else [component]
            self.assertEqual(text.count("uses: docker/build-push-action@"), len(images))
            self.assertEqual(text.count("load: true"), len(images))
            for image in images:
                self.assertEqual(text.count(f"tags: cetirun-{image}:validation"), 1)
                self.assertEqual(text.count(f"scan_image.py --image cetirun-{image}:validation"), 1)
            self.assertIn("golang.org/x/vuln/cmd/govulncheck@v1.8.0", text)
            self.assertIn(
                "run: '\"$RUNNER_TEMP/cetirun-tools/govulncheck\" ./...'", text
            )

    def test_write_reporting_jobs_never_checkout_or_run_on_prs(self):
        text = (
            validate.ROOT / ".github/workflows/docker-build-validate.yml"
        ).read_text()
        build, sarif, issue = re.split(
            r"\n  (?:sarif-report|cve-issue-report):\n", text
        )
        self.assertNotIn(": write", build)
        self.assertIn("github.event_name != 'pull_request'", sarif)
        self.assertIn(
            "github.event_name == 'schedule' || github.event_name == 'workflow_dispatch'",
            issue,
        )
        for report in (sarif, issue):
            self.assertNotIn("actions/checkout@", report)
            self.assertIn("needs: docker-build-validate", report)

    def test_dependency_review_blocks_and_does_not_comment(self):
        text = (validate.ROOT / ".github/workflows/cetirun-validation.yml").read_text()
        self.assertIn("fail-on-severity: high", text)
        self.assertIn("warn-only: false", text)
        self.assertIn("comment-summary-in-pr: never", text)
        self.assertIn("run: make license-check", text)

    def test_license_headers_have_an_unconditional_ci_job(self):
        text = (validate.ROOT / ".github/workflows/cetirun-validation.yml").read_text()
        self.assertIn("  pull_request:\n", text)
        self.assertIn("  push:\n    branches: [main]\n", text)
        self.assertNotRegex(text, r"(?m)^\s+paths(?:-ignore)?:")
        job = re.search(
            r"(?ms)^  license-headers:\n(.*?)(?=^  [a-z][\w-]*:|\Z)", text
        )
        self.assertIsNotNone(job)
        self.assertIn("name: CetiRun license headers", job[1])
        self.assertIn("run: make license-check", job[1])
        self.assertNotIn("if:", job[1])
        self.assertNotIn("continue-on-error:", job[1])

    def test_ci_dependencies_meet_security_floors(self):
        requirements = (validate.ROOT / "scripts/ci/requirements.txt").read_text()
        for package, minimum in {
            "aiohttp": (3, 14, 3),
            "PyJWT[crypto]": (2, 15, 0),
            "pytest": (9, 0, 3),
        }.items():
            with self.subTest(package=package):
                version = re.search(
                    rf"^{re.escape(package)}==(\d+)\.(\d+)\.(\d+)$",
                    requirements,
                    re.MULTILINE,
                )
                self.assertIsNotNone(version)
                self.assertGreaterEqual(tuple(map(int, version.groups())), minimum)

    def test_component_runtime_bases_reject_known_vulnerable_digest(self):
        images = set()
        vulnerable_digest = (
            "f8f5a9bb739ad1ec347853144c9ed4ca2260e587082277bc6066fcd5cc9973e8"
        )
        for component in validate.COMPONENTS:
            text = (validate.ROOT / f"app/cetirun-{component}/Dockerfile").read_text()
            match = re.search(
                r"^FROM (mcr\.microsoft\.com/azurelinux/distroless/base:3\.0"
                r"@sha256:([0-9a-f]{64}))$",
                text,
                re.MULTILINE,
            )
            self.assertIsNotNone(match)
            self.assertNotEqual(match[2], vulnerable_digest)
            images.add(match[1])
        self.assertEqual(len(images), 1)


class NativeHostDownloadTests(unittest.TestCase):
    def setUp(self):
        workflow = (validate.ROOT / ".github/workflows/memory-context-ci.yml").read_text()
        job = workflow.split("  go-build-test:\n", 1)[1].split("\n  docker-build:", 1)[0]
        step = job.split("      - name: Install pinned native host test dependencies\n", 1)[1].split("\n      - name:", 1)[0]
        self.script = textwrap.dedent(step.split("        run: |\n", 1)[1]).replace("\\\n", "")
        self.downloads = [shlex.split(line) for line in self.script.splitlines() if line.startswith("curl ")]
        self.assertEqual(len(self.downloads), 2)
        self.assertRegex(job, r"(?m)^    timeout-minutes: 30$")
        self.assertNotIn("continue-on-error:", job)
        self.assertIn("run: node --test app/cetirun-memory-context/tests/native.test.mjs", job)
        directory = tempfile.TemporaryDirectory(prefix="cetirun-native-download-")
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.native = self.root / "cetirun-memory-native"
        archive = io.BytesIO()
        with tarfile.open(fileobj=archive, mode="w:gz") as output:
            data = b"synthetic pinned-host fixture\n"
            member = tarfile.TarInfo("hermes-host/fixture.txt")
            member.size = len(data)
            output.addfile(member, io.BytesIO(data))
        self.archive = archive.getvalue()

    def test_both_archives_use_bounded_transient_retries(self):
        for command in self.downloads:
            with self.subTest(url=next(arg for arg in command if arg.startswith("https://"))):
                self.assertIn("-fsSL", command)
                self.assertNotIn("--retry-all-errors", command)
                for flag, expected in {
                    "--retry": "3",
                    "--retry-delay": "15",
                    "--retry-max-time": "120",
                    "--connect-timeout": "15",
                    "--max-time": "300",
                }.items():
                    self.assertEqual(command.count(flag), 1)
                    self.assertEqual(command[command.index(flag) + 1], expected)
        self.assertIn("https://codeload.github.com/NousResearch/hermes-agent/tar.gz/refs/tags/v2026.9.24", self.script)
        self.assertIn("15b15ce4e6ec8ea424a081823709d1e17f0943e7b42b59597d24ebb94cbd1742  $native_dir/hermes.tgz", self.script)
        self.assertIn("https://registry.npmjs.org/openclaw/-/openclaw-2026.9.6.tgz", self.script)
        self.assertIn("Ie0kyQSCVfFqixsgVg39vevUDq01Ch5u3+7Yu5Y3qARczmdAe+lzp8bVnO9925rHiW/+CFp70zfORCyPmCH31g==", self.script)
        self.assertLess(self.script.index("sha256sum --check"), self.script.index("tar xzf"))

    def run_download(self, statuses, *, check_integrity=False):
        self.assertIsNotNone(shutil.which("curl"), "curl is required for the loopback download regression")
        attempts = []
        payload = self.archive

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                status = statuses[min(len(attempts), len(statuses) - 1)]
                attempts.append(time.monotonic())
                body = payload if status == 200 else b"synthetic download failure\n"
                self.send_response(status)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *_):
                pass

        with HTTPServer(("127.0.0.1", 0), Handler) as server:
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            try:
                url = f"http://127.0.0.1:{server.server_port}/archive.tgz"
                source = next(arg for arg in self.downloads[0] if arg.startswith("https://"))
                if check_integrity:
                    script = self.script.split(next(line for line in self.script.splitlines() if line.startswith("curl ") and "openclaw.tgz" in line), 1)[0]
                    script = script.replace(source, url)
                else:
                    self.native.mkdir()
                    script = shlex.join(self.downloads[0]).replace(source, url).replace("$native_dir", str(self.native))
                result = subprocess.run(
                    ["bash", "--noprofile", "--norc", "-e", "-o", "pipefail", "-c", script],
                    env={
                        **os.environ,
                        "HOME": str(self.root), "CURL_HOME": str(self.root),
                        "RUNNER_TEMP": str(self.root), "native_dir": str(self.native),
                        "NO_PROXY": "127.0.0.1,localhost", "no_proxy": "127.0.0.1,localhost",
                    },
                    capture_output=True, text=True, check=False, timeout=75,
                )
            finally:
                server.shutdown()
                thread.join(timeout=5)
                self.assertFalse(thread.is_alive(), "loopback server did not stop")
        return result, attempts

    def test_rate_limit_recovers_before_native_qualification(self):
        result, attempts = self.run_download([429, 200])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(attempts), 2)
        self.assertGreaterEqual(attempts[1] - attempts[0], 14.5)
        self.assertEqual((self.native / "hermes.tgz").read_bytes(), self.archive)

    def test_repeated_rate_limits_fail_after_the_retry_limit(self):
        result, attempts = self.run_download([429])
        self.assertEqual(result.returncode, 22, result.stderr)
        self.assertEqual(len(attempts), 4)
        self.assertFalse((self.native / "hermes/fixture.txt").exists())

    def test_permanent_http_error_is_not_retried(self):
        result, attempts = self.run_download([404], check_integrity=True)
        self.assertEqual(result.returncode, 22, result.stderr)
        self.assertEqual(len(attempts), 1)
        self.assertFalse((self.native / "hermes/fixture.txt").exists())

    def test_integrity_failure_still_blocks_extraction(self):
        result, attempts = self.run_download([200], check_integrity=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(len(attempts), 1)
        self.assertIn("FAILED", result.stdout)
        self.assertFalse((self.native / "hermes/fixture.txt").exists())


if __name__ == "__main__":
    unittest.main()
