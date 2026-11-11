# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Keep native checks, modules, and shipped Go builders on one toolchain."""

from pathlib import Path
import re
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[3]
GO_IMAGE = re.compile(r"([a-zA-Z0-9._/-]*golang):([^\s@]+)(?:@sha256:([0-9a-f]{64}))?")
GO_BUILDER_FROM = re.compile(
    r"(?m)^FROM(?: --platform=\$BUILDPLATFORM)? "
    r"(?P<image>mcr\.microsoft\.com/oss/go/microsoft/golang):"
    r"(?P<version>[^\s@]+)@sha256:(?P<digest>[0-9a-f]{64}) AS \S+$"
)
MICROSOFT_GO_BOOTSTRAP_VERSION = "1.27.3-1"
MICROSOFT_GO_BOOTSTRAP_DIGEST = "2b3c026371fe2221a588f80cfb5dbc592af1efd3be0d53297676c7f3a612a5d9"
MICROSOFT_GO_RELEASE = "1.27.4-1"
MICROSOFT_GO_ARCHIVES = {
    "amd64": (
        "https://download.visualstudio.microsoft.com/download/pr/"
        "b32709ee-0545-4d3f-913f-bf51b626be52/"
        "33fbba165da669d1cb19d3799e623805/go1.27.4-20261110.2.linux-amd64.tar.gz",
        "423eee29e71d3da6b8d59f7fa080ca21f28015857af58f3da5349c264a5f3f2f",
    ),
    "arm64": (
        "https://download.visualstudio.microsoft.com/download/pr/"
        "b32709ee-0545-4d3f-913f-bf51b626be52/"
        "a5caf67eaca4942d973c08bb1e54fa40/go1.27.4-20261110.2.linux-arm64.tar.gz",
        "8bd381cdc3a3dc932117a3c32ac49ac8787d8a32ca4fba13ecbac890265afd3a",
    ),
}
GO_BUILDERS = {
    "app/cetirun-agent-broker/Dockerfile",
    "app/cetirun-chart/Dockerfile.base",
    "app/cetirun-connections-mcp/Dockerfile",
    "app/cetirun-memory-context/Dockerfile",
    "app/cetirun-observability/Dockerfile",
    "app/cetirun-operator/Dockerfile",
    "app/cetirun-operator/Dockerfile.terminal",
    "app/cetirun-portal/Dockerfile",
}


class GoToolchainTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.version = (ROOT / ".go-version").read_text().strip()
        output = subprocess.check_output(
            ["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z"],
            cwd=ROOT,
        )
        cls.paths = {
            ROOT / path.decode()
            for path in output.split(b"\0")
            if path and (ROOT / path.decode()).is_file()
        }

    def test_security_floor(self):
        self.assertRegex(self.version, r"^\d+\.\d+\.\d+$")
        # GO-2026-6859 is fixed in Go 1.27 starting with 1.27.4.
        self.assertGreaterEqual(tuple(map(int, self.version.split("."))), (1, 27, 4))

    def test_module_minimums_match(self):
        modules = sorted(path for path in self.paths if path.name == "go.mod")
        self.assertGreaterEqual(len(modules), 2)
        for path in modules:
            with self.subTest(path=path.relative_to(ROOT)):
                text = path.read_text()
                self.assertEqual(
                    re.findall(r"^go (\S+)$", text, re.MULTILINE), [self.version]
                )
                for toolchain in re.findall(r"^toolchain go(\S+)$", text, re.MULTILINE):
                    self.assertEqual(toolchain, self.version)

    def test_every_go_workflow_uses_the_version_file(self):
        count = 0
        for path in sorted((ROOT / ".github/workflows").glob("*.y*ml")):
            text = path.read_text()
            steps = len(re.findall(r"uses:\s*actions/setup-go@", text))
            if not steps:
                continue
            count += steps
            with self.subTest(path=path.relative_to(ROOT)):
                versions = re.findall(
                    r"^\s+go-version-file:\s*[\"']?([^\"'\s#]+)", text, re.MULTILINE
                )
                self.assertEqual(versions, [".go-version"] * steps)
                self.assertNotRegex(text, r"(?m)^\s+go-version:")
        self.assertGreaterEqual(count, 5)

    def test_all_go_builders_match_and_are_digest_pinned(self):
        builders = set()
        digests = set()
        for path in sorted(self.paths):
            if path.name != "Dockerfile" and not path.name.startswith("Dockerfile."):
                continue
            text = path.read_text()
            builders_in_file = list(GO_BUILDER_FROM.finditer(text))
            with self.subTest(path=path.relative_to(ROOT), check="Go builder coverage"):
                self.assertEqual(
                    [
                        (builder["image"], builder["version"], builder["digest"])
                        for builder in builders_in_file
                    ],
                    GO_IMAGE.findall(text),
                    "Every Go image must be a digest-pinned Microsoft builder with "
                    "the verified Go 1.27.4 overlay",
                )
            for builder in builders_in_file:
                with self.subTest(path=path.relative_to(ROOT), image=builder["image"]):
                    self.assertEqual(
                        builder["image"], "mcr.microsoft.com/oss/go/microsoft/golang"
                    )
                    # MCR has not published the Microsoft 1.27.4 image yet. Every
                    # builder starts from this digest-pinned Microsoft base and must
                    # immediately replace it with the verified 1.27.4 archive below.
                    self.assertEqual(builder["version"], MICROSOFT_GO_BOOTSTRAP_VERSION)
                    self.assertEqual(builder["digest"], MICROSOFT_GO_BOOTSTRAP_DIGEST)
                    overlay = self.microsoft_go_overlay()
                    self.assertTrue(
                        text[builder.end() :].startswith(overlay),
                        "Go 1.27.4 overlay must immediately follow the builder FROM",
                    )
                    builders.add(path.relative_to(ROOT).as_posix())
                    digests.add(builder["digest"])
        self.assertEqual(builders, GO_BUILDERS)
        self.assertEqual(len(digests), 1)

    def microsoft_go_overlay(self):
        amd64_url, amd64_sha256 = MICROSOFT_GO_ARCHIVES["amd64"]
        arm64_url, arm64_sha256 = MICROSOFT_GO_ARCHIVES["arm64"]
        return f'''\
\n# Use the published Microsoft patch until its MCR image is available.
# Pins: https://github.com/microsoft/go/releases/tag/v{MICROSOFT_GO_RELEASE}
ENV GOLANG_VERSION=1.27.4 GOTOOLCHAIN=local
RUN set -eu; \\
    case "$(uname -m)" in \\
      x86_64) go_url="{amd64_url}"; go_sha256={amd64_sha256} ;; \\
      aarch64) go_url="{arm64_url}"; go_sha256={arm64_sha256} ;; \\
      *) echo "Unsupported Go build architecture" >&2; exit 1 ;; \\
    esac; \\
    curl -fsSL --connect-timeout 15 --max-time 300 --retry 3 --retry-delay 5 "$go_url" -o /tmp/go.tar.gz; \\
    echo "$go_sha256  /tmp/go.tar.gz" | sha256sum --check; \\
    rm -rf /usr/local/go; \\
    tar xzf /tmp/go.tar.gz -C /usr/local; \\
    rm /tmp/go.tar.gz; \\
    test "$(go env GOVERSION)" = go{self.version}
'''

    def dependabot_directories(self, ecosystem):
        text = (ROOT / ".github/dependabot.yml").read_text()
        block = re.search(
            rf'(?ms)^  - package-ecosystem: "{ecosystem}"\n(.*?)(?=^  - package-ecosystem:|\Z)',
            text,
        )
        self.assertIsNotNone(block, f"Missing Dependabot ecosystem: {ecosystem}")
        directories = re.search(r"(?ms)^    directories:\n(.*?)(?=^    \S)", block[1])
        self.assertIsNotNone(directories)
        return set(re.findall(r'^      - "([^"\n]+)"$', directories[1], re.MULTILINE)), block[1]

    def test_dependabot_covers_and_can_parse_every_go_builder(self):
        directories, block = self.dependabot_directories("docker")
        self.assertIn('group-by: "dependency-name"', block)
        for path in sorted(self.paths):
            if path.name != "Dockerfile" and not path.name.startswith("Dockerfile."):
                continue
            text = path.read_text()
            images = GO_IMAGE.findall(text)
            if not images:
                continue
            with self.subTest(path=path.relative_to(ROOT)):
                self.assertIn("/" + path.parent.relative_to(ROOT).as_posix(), directories)
                visible_images = GO_IMAGE.findall("\n".join(
                    line for line in text.splitlines() if line.startswith("FROM ")
                ))
                self.assertEqual(images, visible_images, "Dependabot cannot update images hidden in ARGs")

    def test_dependabot_covers_all_application_go_modules(self):
        directories, block = self.dependabot_directories("gomod")
        self.assertIn('dependency-type: "all"', block)
        for path in sorted(self.paths):
            if path.name == "go.mod" and path.relative_to(ROOT).parts[0] == "app":
                with self.subTest(path=path.relative_to(ROOT)):
                    self.assertIn("/" + path.parent.relative_to(ROOT).as_posix(), directories)

    def test_runtime_go_override_defaults_to_the_discoverable_stage(self):
        text = (ROOT / "app/cetirun-chart/Dockerfile.base").read_text()
        self.assertEqual(re.findall(r"(?m)^ARG GO_BUILD_IMAGE=(\S+)$", text), ["go-toolchain"])
        self.assertRegex(text, r"(?m)^FROM \S+golang:\S+ AS go-toolchain$")
        stages = re.findall(r"(?m)^FROM \$\{GO_BUILD_IMAGE\} AS (\S+)$", text)
        self.assertEqual(stages, ["tailscale-build", "gh-build", "kubectl-build"])


if __name__ == "__main__":
    unittest.main()
