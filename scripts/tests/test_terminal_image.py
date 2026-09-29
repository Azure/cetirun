# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Offline checks for the independent terminal image and its build entrypoint."""

import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts/terminal-docker-build.sh"


class TerminalImageTests(unittest.TestCase):
    def test_mcr_release_builds_and_publishes_terminal(self):
        pipeline = (ROOT / ".pipelines/release-mcr.yml").read_text()
        self.assertTrue("bash scripts/terminal-docker-build.sh" in pipeline, "MCR release does not build the terminal")
        build_inventory = re.search(r"for image in ([^;]+); do", pipeline)
        publish_inventory = re.search(r"image_artifacts=\(([^)]+)\)", pipeline)
        self.assertIsNotNone(build_inventory)
        self.assertIsNotNone(publish_inventory)
        self.assertIn("cetirun-terminal", build_inventory.group(1).split())
        self.assertIn("cetirun-terminal", publish_inventory.group(1).split())

    def test_help_does_not_require_docker_or_network(self):
        result = subprocess.run(
            ["/bin/bash", str(SCRIPT), "--help"],
            env={"PATH": "/not-installed"},
            capture_output=True, text=True, check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("PUSH defaults to false", result.stdout)

    def test_invalid_inputs_fail_before_docker(self):
        with tempfile.TemporaryDirectory(prefix="cetirun-terminal-invalid-") as temp:
            docker = Path(temp) / "docker"
            docker.write_text("#!/bin/sh\nexit 99\n")
            docker.chmod(0o755)
            for key, value in (
                ("PUSH", "maybe"), ("PLATFORM", "linux/unknown"),
                ("REGISTRY", "https://registry.example.com"), ("IMAGE_TAG", "bad tag"),
            ):
                env = {
                    **os.environ, "PATH": f"{temp}:{os.environ['PATH']}",
                    "VERSION": "0.1.1", "GIT_SHA": "synthetic",
                    "REGISTRY": "registry.example.com", "PUSH": "false",
                    "PLATFORM": "linux/amd64", "IMAGE_TAG": "0.1.1", key: value,
                }
                result = subprocess.run(["bash", str(SCRIPT)], env=env, capture_output=True, text=True)
                self.assertEqual(result.returncode, 2, result.stderr)

    def test_build_uses_dedicated_dockerfile_and_does_not_push(self):
        with tempfile.TemporaryDirectory(prefix="cetirun-terminal-build-") as temp:
            directory = Path(temp)
            log = directory / "args.jsonl"
            docker = directory / "docker"
            docker.write_text(
                "#!/usr/bin/env python3\nimport json, os, sys\n"
                "with open(os.environ['TERMINAL_TEST_LOG'], 'a') as output:\n"
                "    output.write(json.dumps(sys.argv[1:]) + '\\n')\n"
            )
            docker.chmod(0o755)
            env = {**os.environ, "PATH": f"{temp}:{os.environ['PATH']}",
                   "VERSION": "0.1.1", "GIT_SHA": "synthetic", "REGISTRY": "registry.example.com",
                   "PUSH": "false", "PLATFORM": "linux/amd64", "TERMINAL_TEST_LOG": str(log)}
            result = subprocess.run(["bash", str(SCRIPT)], env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            commands = [json.loads(line) for line in log.read_text().splitlines()]
            build = commands[-1]
            self.assertEqual(build[:2], ["buildx", "build"])
            self.assertIn(str(ROOT / "app/cetirun-operator/Dockerfile.terminal"), build)
            self.assertIn("registry.example.com/cetirun-terminal:0.1.1", build)
            self.assertIn("--load", build)
            self.assertNotIn("--push", build)

    def test_image_is_independent_and_preserves_upstream_license(self):
        image = (ROOT / "app/cetirun-operator/Dockerfile.terminal").read_text()
        self.assertIn('ENTRYPOINT ["/terminal"]', image)
        self.assertIn('CMD ["serve"]', image)
        self.assertIn("sha256sum --check", image)
        self.assertNotIn("/manager", image)
        self.assertTrue((ROOT / "app/cetirun-operator/third_party/ttyd.LICENSE").is_file())
        manager = (ROOT / "app/cetirun-operator/Dockerfile").read_text()
        self.assertNotIn("/ttyd", manager)


if __name__ == "__main__":
    unittest.main()
