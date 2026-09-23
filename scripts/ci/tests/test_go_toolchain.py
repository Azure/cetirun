# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Keep native checks, modules, and shipped Go builders on one toolchain."""

from pathlib import Path
import re
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[3]
GO_IMAGE = re.compile(r"([a-zA-Z0-9._/-]*golang):([^\s@]+)(?:@sha256:([0-9a-f]{64}))?")


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
        self.assertGreaterEqual(tuple(map(int, self.version.split("."))), (1, 26, 8))

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
            for image, version, digest in GO_IMAGE.findall(path.read_text()):
                with self.subTest(path=path.relative_to(ROOT), image=image):
                    self.assertEqual(image, "mcr.microsoft.com/oss/go/microsoft/golang")
                    # A Microsoft packaging revision does not change the Go release.
                    self.assertRegex(version, rf"^{re.escape(self.version)}(?:-\d+)?$")
                    self.assertRegex(digest, r"^[0-9a-f]{64}$")
                    builders.add(path.relative_to(ROOT).as_posix())
                    digests.add(digest)
        self.assertTrue(
            {
                "app/cetirun-chart/Dockerfile.base",
                "app/cetirun-operator/Dockerfile",
                "app/cetirun-portal/Dockerfile",
            }.issubset(builders)
        )
        self.assertEqual(len(digests), 1)


if __name__ == "__main__":
    unittest.main()
