# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

import json
import os
import re
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CHART = ROOT / "app" / "cetirun-chart"


def version_tuple(value):
    return tuple(int(part) for part in value.split("."))


class ImageDependencyTests(unittest.TestCase):
    def _docker_build_arguments(self, **overrides):
        with tempfile.TemporaryDirectory() as temp_dir:
            temp = Path(temp_dir)
            log_path = temp / "docker-arguments.log"
            docker = temp / "docker"
            docker.write_text(
                "#!/usr/bin/env sh\n"
                'printf "%s\\n" "$*" >> "$CETIRUN_DOCKER_ARGUMENTS"\n'
                'if [ "$1" = "buildx" ] && [ "$2" = "inspect" ]; then exit 1; fi\n'
            )
            docker.chmod(0o755)
            environment = os.environ.copy()
            environment.update(
                {
                    "BUILD_BASE": "false",
                    "BUILD_DESKTOP": "false",
                    "GIT_SHA": "test-sha",
                    "PUSH": "false",
                    "VERSION": "test-version",
                    "CETIRUN_DOCKER_ARGUMENTS": str(log_path),
                    "PATH": f"{temp}{os.pathsep}{environment['PATH']}",
                }
            )
            environment.update(overrides)
            result = subprocess.run(
                [ROOT / "scripts" / "cetirun-docker-build.sh"],
                capture_output=True,
                text=True,
                check=False,
                env=environment,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            return log_path.read_text()

    def test_base_image_provides_common_initializer_tools(self):
        content = (CHART / "Dockerfile.base").read_text()
        common = (CHART / "configs/init-home-common.sh").read_text()
        required = re.search(r"for tool in ([^;]+); do", common)
        checked = re.search(r"RUN for tool in ([^;]+); do", content)
        self.assertIsNotNone(required)
        self.assertIsNotNone(checked)
        self.assertEqual(set(checked[1].split()), {"sh", *required[1].split()})
        self.assertRegex(content, r"(?m)^  diffutils \\\s*$")
        self.assertIn('command -v "$tool" >/dev/null 2>&1 || exit 1;', content)

    def test_fixed_image_inputs(self):
        minimums = {
            "Dockerfile.base": {
                "CLAUDE_CODE_VERSION": "2.1.163",
                "OPENCODE_VERSION": "1.18.33",
                "GH_VERSION": "2.95.0",
                "KUBECTL_VERSION": "1.36.4",
                "TAILSCALE_VERSION": "1.100.0",
                "MSGPACK_VERSION": "1.2.1",
                "SETUPTOOLS_VERSION": "80.10.2",
            },
            "agents/openclaw/Dockerfile": {
                "OPENCLAW_VERSION": "2026.9.6",
                "CLAWHUB_VERSION": "0.23.3",
                "CLAWHUB_UNDICI_VERSION": "7.29.1",
            },
            "agents/hermes/Dockerfile": {
                "HERMES_VERSION": "0.21.5",
            },
        }
        for filename, arguments in minimums.items():
            content = (CHART / filename).read_text()
            for name, minimum in arguments.items():
                with self.subTest(filename=filename, argument=name):
                    match = re.search(rf"^ARG {name}=([\d.]+)$", content, re.MULTILINE)
                    self.assertIsNotNone(match)
                    self.assertGreaterEqual(
                        version_tuple(match[1]), version_tuple(minimum)
                    )

    def test_openclaw_config_uses_current_schema_contracts(self):
        template = (CHART / "agents/openclaw/config/native.tpl").read_text()
        entrypoint = (
            CHART / "agents/openclaw/bootstrap/entrypoint.sh"
        ).read_text()
        values = (CHART / "values.yaml").read_text()
        schema = json.loads((CHART / "values.schema.json").read_text())

        self.assertIn('set $defaultsCurrent "modelPolicy"', template)
        self.assertIn('set $pluginEntries "github"', template)
        self.assertIn("rememberAcrossConversations: false", values)
        self.assertIn('trustedProxies:\n        - "127.0.0.6"', values)
        self.assertIn(
            'tailscale serve --bg "http://127.0.0.1:${TAILSCALE_SERVE_PORT}"',
            entrypoint,
        )
        self.assertNotIn('tailscale serve --bg "http://localhost:', entrypoint)
        self.assertNotIn(
            "memorySearch", schema["properties"]["openclaw"]["properties"]
        )

        retired_fields = (
            "reserveTokensFloor",
            "maxConcurrentSessions",
            "coalesceIdleMs",
            "maxChunkChars",
            "ttlMinutes",
            "remoteCdpTimeoutMs",
            "remoteCdpHandshakeTimeoutMs",
            "watchDebounceMs",
        )
        for field in retired_fields:
            with self.subTest(field=field):
                self.assertNotIn(field, template)
                self.assertNotIn(field, values)

    def test_hermes_uses_official_locked_source_install(self):
        content = (CHART / "agents/hermes/Dockerfile").read_text()
        expected = {
            "HERMES_RELEASE": "2026.9.24",
            "HERMES_GIT_SHA": "f97608f178d1ffeca59860195ab7da295f7c8e5f",
            "HERMES_SOURCE_SHA256": (
                "15b15ce4e6ec8ea424a081823709d1e17"
                "f0943e7b42b59597d24ebb94cbd1742"
            ),
            "HERMES_UV_IMAGE": (
                "ghcr.io/astral-sh/uv:0.11.6@sha256:"
                "43cb71695fcad1516c2fbe0f56e500184c42d8bce838d9f64593b8aff2c16298"
            ),
            "HERMES_NODE_IMAGE": (
                "node:26-bookworm-slim@sha256:"
                "9e6f9357d371591e32ab6f2d8a26d63bdd0d17c29eee3f4f3e7e454d9634bf73"
            ),
            "HERMES_SQLITE_VERSION": "3530400",
            "HERMES_SQLITE_SHA256": (
                "0e9483900e92cd5de8fd48d16bf9200145a61f7fd"
                "5be542a5ac81d8a9516eb9c"
            ),
        }
        for argument, value in expected.items():
            with self.subTest(argument=argument):
                self.assertIn(f"ARG {argument}={value}\n", content)

        self.assertIn(
            "codeload.github.com/NousResearch/hermes-agent/tar.gz/refs/tags/"
            "v${HERMES_RELEASE}",
            content,
        )
        self.assertIn(
            'echo "${HERMES_SOURCE_SHA256}  /tmp/hermes-agent.tar.gz" '
            "| sha256sum --check",
            content,
        )
        self.assertIn(
            "uv sync --python /usr/bin/python3 --frozen --extra all", content
        )
        self.assertIn("--extra anthropic", content)
        self.assertIn("--extra azure-identity", content)
        self.assertIn(
            "COPY --from=hermes-uv /uv /usr/local/bin/uv",
            content,
        )
        self.assertIn("FROM --platform=$BUILDPLATFORM ${HERMES_NODE_IMAGE}", content)
        self.assertIn(
            "COPY --from=hermes-ui-build --chown=vibe:vibe", content
        )
        self.assertIn(
            "COPY --from=hermes-sqlite-build "
            "/opt/sqlite-fixed/lib/libsqlite3.so.3.53.4",
            content,
        )
        self.assertIn("npm --prefix web run build", content)
        self.assertIn("npm --prefix ui-tui run build", content)
        self.assertIn("printf '%s\\n' docker > .install_method", content)
        self.assertIn("/etc/hermes/image-provenance.json", content)
        self.assertNotIn("HERMES_WHEEL_SHA256", content)
        self.assertNotIn('"hermes-agent[all]==${HERMES_VERSION}"', content)
        self.assertNotIn("PLUGINS_DIR=", content)

    def test_hermes_sqlite_build_has_kernel_headers(self):
        content = (CHART / "agents/hermes/Dockerfile").read_text()
        sqlite_build = content.split("AS hermes-sqlite-build", 1)[1].split(
            "FROM --platform=", 1
        )[0]
        self.assertRegex(sqlite_build, r"tdnf install -y [^\n]*\bkernel-headers\b")
        self.assertIn("-DSQLITE_ENABLE_FTS5", sqlite_build)
        self.assertIn("-DSQLITE_THREADSAFE=1", sqlite_build)
        self.assertNotIn("cc-test", sqlite_build)

    def test_node_release_and_architecture_checksums(self):
        content = (CHART / "Dockerfile.base").read_text()
        expected = {
            "NODE_VERSION": "24.19.0",
            "NODE_SHA256_X64": "14b342e71204f811bde6153be8e04b62aef63c236fef92b55f9c83154b409647",
            "NODE_SHA256_ARM64": "01443c1e1a29e531ccad5a46fefa6df490d2189c49f7955904aecdbb0fe86fdc",
        }
        for argument, value in expected.items():
            with self.subTest(argument=argument):
                self.assertIn(f"ARG {argument}={value}\n", content)
        self.assertIn('NODE_ARCH="x64"; NODE_SHA256="${NODE_SHA256_X64}"', content)
        self.assertIn('NODE_ARCH="arm64"; NODE_SHA256="${NODE_SHA256_ARM64}"', content)
        self.assertIn('echo "${NODE_SHA256}  /tmp/node.tar.xz" | sha256sum --check', content)

    def test_npm_overrides_are_empty_after_dependency_upgrades(self):
        overrides = json.loads((CHART / "configs/cve-overrides.json").read_text())[
            "overrides"
        ]
        self.assertEqual(overrides, {})

        base = (CHART / "Dockerfile.base").read_text()
        self.assertNotIn("tinyglobby/node_modules/picomatch", base)
        self.assertIn(
            "UNDICI_DIR=/usr/local/lib/node_modules/npm/node_modules/undici", base
        )
        self.assertIn("npm pkg set 'allowScripts.opencode-ai=true' --json", base)
        self.assertIn("npm rebuild opencode-ai", base)
        self.assertIn('test "$(opencode --version)" = "${OPENCODE_VERSION}"', base)
        self.assertIn(
            "npm pkg set 'allowScripts[@anthropic-ai/claude-code]=true' --json",
            base,
        )
        self.assertIn("npm rebuild @anthropic-ai/claude-code", base)
        self.assertIn('claude --version | grep -F "${CLAUDE_CODE_VERSION}"', base)

        openclaw = (CHART / "agents/openclaw/Dockerfile").read_text()
        self.assertIn('npm pkg set "overrides[undici@7]=${CLAWHUB_UNDICI_VERSION}"', openclaw)
        self.assertIn('npm ls "undici@${CLAWHUB_UNDICI_VERSION}" --all', openclaw)
        self.assertIn("! npm ls undici@7.29.0 --all", openclaw)
        self.assertIn(
            'npm pack undici@6.27.0 --quiet --registry "${NPM_REGISTRY}"',
            base,
        )
        self.assertIn(
            "test \"$(node -p 'require(\"./package.json\").version')\" "
            '= "6.27.0"',
            base,
        )

    def test_azure_cli_top_level_packages_are_not_replaced(self):
        content = (CHART / "Dockerfile.base").read_text()
        install = content.split("# Install Azure CLI", 1)[1].split(
            "# Install ripgrep", 1
        )[0]
        self.assertNotIn('rm -rf "${AZ_SITE}/urllib3"', install)
        self.assertNotIn('rm -rf "${AZ_SITE}/msgpack"', install)
        self.assertNotIn('rm -rf "${AZ_SITE}/setuptools"', install)
        self.assertNotIn('--target "${AZ_SITE}"', install)

    def test_pip_vendor_payloads_and_manifest_are_updated_together(self):
        content = (CHART / "Dockerfile.base").read_text()
        install = content.split("# Install Azure CLI", 1)[1].split(
            "# Install ripgrep", 1
        )[0]
        self.assertIn("import pip._vendor", install)
        self.assertIn(
            'for PIP_VENDOR in "${SYSTEM_PIP_VENDOR}" "${AZ_SITE}/pip/_vendor"',
            install,
        )
        self.assertIn(
            '--upgrade --no-deps --only-binary=:all: --target "${PIP_VENDOR}"',
            install,
        )
        for package in ("msgpack", "setuptools"):
            with self.subTest(package=package):
                self.assertIn(f'"{package}==${{{package.upper()}_VERSION}}"', install)
        self.assertLess(
            install.index("tdnf install -y azure-cli"), install.index("for PIP_VENDOR")
        )
        self.assertLess(
            install.index('--target "${PIP_VENDOR}"'),
            install.index("python3 /usr/local/share/cetirun/sync-pip-vendor.py"),
        )
        self.assertIn('test -f "${PIP_VENDOR}/vendor.txt"', install)
        self.assertIn('PYTHONPATH="${PIP_VENDOR%/pip/_vendor}"', install)
        self.assertIn("--check", install)
        self.assertIn("|| exit 1;", install)

    def test_plugin_install_and_identity_cleanup_failure_handling(self):
        content = (CHART / "agents/openclaw/Dockerfile").read_text()
        self.assertNotIn("OPENCLAW_PLUGIN_STAGE_DIR", content)
        self.assertIn("npm pkg set 'allowScripts.openclaw=true' --json", content)
        self.assertIn("npm pkg delete allowScripts.openclaw", content)
        self.assertIn("npm approve-scripts openclaw", content)
        self.assertIn("npm rebuild openclaw", content)
        self.assertIn(
            "test ! -e node_modules/openclaw/.openclaw-lifecycle-pending", content
        )
        self.assertIn("ARG OPENCLAW_TARBALL_INTEGRITY=sha512-", content)
        self.assertIn('view "openclaw@${OPENCLAW_VERSION}" dist.tarball dist.integrity', content)
        self.assertIn('crypto.createHash("sha512")', content)
        self.assertNotIn("npm --min-release-age=0 pack", content)
        self.assertIn(
            "source=/home/vibe/openclaw-pack/openclaw.tgz,target=/tmp/openclaw.tgz,ro",
            content,
        )
        self.assertNotIn("openclaw-2026.9.6.tgz", content)
        self.assertIn("--prefix /tmp/openclaw-plugin-prefetch", content)
        expected_plugins = (
            "acpx",
            "brave-plugin",
            "discord",
            "feishu",
            "msteams",
            "perplexity-plugin",
            "slack",
        )
        for plugin in expected_plugins:
            with self.subTest(plugin=plugin):
                package = f"@openclaw/{plugin}@${{OPENCLAW_VERSION}}"
                self.assertIn(f'"{package}"', content)
                self.assertIn(f'openclaw plugins install "npm:{package}"', content)
        self.assertLess(
            content.index("--prefix /tmp/openclaw-plugin-prefetch"),
            content.index('openclaw plugins install "npm:@openclaw/acpx'),
        )
        self.assertNotIn("openclaw doctor --fix", content)
        match = re.search(
            r"^RUN (.*openclaw plugins install.*)$",
            content.replace("\\\n", ""),
            re.MULTILINE,
        )
        self.assertIsNotNone(match)
        install = match[1]
        self.assertIn("rm -rf /home/vibe/.openclaw/identity", install)
        self.assertIn("rm -f /home/vibe/.openclaw/openclaw.json", install)
        self.assertNotIn("\nRUN openclaw doctor", content)
        cases = [(fail_at, 0) for fail_at in range(8)] + [(0, 1), (7, 1)]
        for fail_at, cleanup_exit in cases:
            with self.subTest(fail_at=fail_at, cleanup_exit=cleanup_exit):
                mocks = (
                    "calls=0\n"
                    "openclaw() { calls=$((calls + 1)); "
                    f'[ "$calls" -ne {fail_at} ]; }}\n'
                    "npm() { return 0; }\n"
                    "find() { return 0; }\n"
                    f'rm() {{ printf "%s\\n" "$*"; return {cleanup_exit}; }}\n'
                )
                result = subprocess.run(
                    ["sh", "-c", mocks + install],
                    capture_output=True,
                    text=True,
                    check=False,
                )
                if fail_at or cleanup_exit:
                    self.assertNotEqual(result.returncode, 0, result.stderr)
                else:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertIn("-rf /home/vibe/.openclaw/identity", result.stdout)
                    self.assertIn("-f /home/vibe/.openclaw/openclaw.json", result.stdout)

    def test_runtime_build_receives_the_configured_python_index(self):
        index = "https://packages.example.invalid/simple/"
        arguments = self._docker_build_arguments(
            BUILD_IMAGE="true",
            BUILD_HERMES="false",
            PYPI_INDEX_URL=index,
        )
        self.assertIn(f"--build-arg PYPI_INDEX_URL={index}", arguments)

if __name__ == "__main__":
    unittest.main()
