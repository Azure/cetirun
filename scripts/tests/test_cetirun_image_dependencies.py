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
