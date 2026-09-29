# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

import json
import re
import subprocess
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CHART = ROOT / "app" / "cetirun-chart"


def version_tuple(value):
    return tuple(int(part) for part in value.split("."))


class ImageDependencyTests(unittest.TestCase):
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
                "GH_VERSION": "2.95.0",
                "KUBECTL_VERSION": "1.36.4",
                "TAILSCALE_VERSION": "1.100.0",
                "MSGPACK_VERSION": "1.2.1",
                "SETUPTOOLS_VERSION": "80.10.2",
            },
            "agents/openclaw/Dockerfile": {
                "OPENCLAW_VERSION": "2026.6.35",
                "CETIRUN_AUTH_AIOHTTP_VERSION": "3.14.3",
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

    def test_npm_fixes_preserve_dependency_major_versions(self):
        overrides = json.loads((CHART / "configs/cve-overrides.json").read_text())[
            "overrides"
        ]
        minimums = {
            "form-data@2": "2.5.6",
            "form-data@3": "3.0.5",
            "form-data@4": "4.0.6",
            "protobufjs@7": "7.6.1",
            "protobufjs@8": "8.4.1",
            "undici@6": "6.27.0",
            "undici@7": "7.28.0",
            "undici@8": "8.5.0",
            "ws@8": "8.21.0",
        }
        for package, minimum in minimums.items():
            with self.subTest(package=package):
                self.assertIn(package, overrides)
                spec = overrides[package]
                self.assertRegex(spec, r"^\^\d+\.\d+\.\d+$")
                version = version_tuple(spec[1:])
                self.assertEqual(version[0], version_tuple(minimum)[0])
                self.assertGreaterEqual(version, version_tuple(minimum))
        self.assertNotIn("protobufjs", overrides)

        base = (CHART / "Dockerfile.base").read_text()
        self.assertIn(
            "UNDICI_DIR=/usr/local/lib/node_modules/npm/node_modules/undici", base
        )
        self.assertIn(
            'npm pack undici@6.27.0 --quiet --registry "${NPM_REGISTRY}"',
            base,
        )
        self.assertIn(
            "test \"$(node -p 'require(\"./package.json\").version')\" "
            '= "6.27.0"',
            base,
        )

    def test_azure_cli_private_python_packages_are_updated(self):
        content = (CHART / "Dockerfile.base").read_text()
        install = content.split("# Install Azure CLI", 1)[1].split(
            "# Install ripgrep", 1
        )[0]
        for package in ("msgpack", "setuptools"):
            with self.subTest(package=package):
                self.assertIn(f"{package}==${{{package.upper()}_VERSION}}", install)
                self.assertIn(f'"${{AZ_SITE}}/{package}"', install)
                self.assertIn(f'"${{AZ_SITE}}"/{package}-*.dist-info', install)
        self.assertIn('--target "${AZ_SITE}"', install)
        self.assertIn("--upgrade", install)

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
        match = re.search(
            r"^RUN (.*openclaw plugins install.*)$",
            content.replace("\\\n", ""),
            re.MULTILINE,
        )
        self.assertIsNotNone(match)
        install = match[1]
        self.assertIn("(openclaw doctor --fix --non-interactive --yes || true)", install)
        self.assertIn("rm -rf /home/vibe/.openclaw/identity", install)
        self.assertIn("rm -f /home/vibe/.openclaw/openclaw.json", install)
        self.assertNotIn("\nRUN openclaw doctor", content)
        for fail_at, cleanup_exit in ((0, 0), (1, 0), (2, 0), (3, 0), (0, 1), (3, 1)):
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
                if fail_at in (1, 2) or cleanup_exit:
                    self.assertNotEqual(result.returncode, 0, result.stderr)
                else:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertIn("-rf /home/vibe/.openclaw/identity", result.stdout)
                    self.assertIn("-f /home/vibe/.openclaw/openclaw.json", result.stdout)


if __name__ == "__main__":
    unittest.main()
