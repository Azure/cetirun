# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Static build-only regressions; these do not sign or verify release artifacts."""

from pathlib import Path
import unittest


PIPELINE = Path(__file__).resolve().parents[2] / ".pipelines/release-mcr.yml"


class ReleaseSigningBoundaryTests(unittest.TestCase):
    def test_publishing_stays_disabled(self):
        active = "\n".join(
            line for line in PIPELINE.read_text().splitlines()
            if not line.lstrip().startswith("#")
        )
        self.assertEqual(active.count("PUSH=false"), 3)
        for command in (
            "PUSH=true", "docker push", "docker login", "helm push",
            "helm registry login", "az login", "az acr",
            "docker buildx imagetools create",
        ):
            with self.subTest(command=command):
                self.assertNotIn(command, active)

    def test_disabled_registry_example_requires_external_configuration(self):
        self.assertIn('acr_name="${RELEASE_ACR_NAME:?', PIPELINE.read_text())


if __name__ == "__main__":
    unittest.main()
