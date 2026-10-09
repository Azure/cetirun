# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Regression tests for transient MCR manifest propagation."""

import importlib.util
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location(
    "verify_mcr_image", ROOT / "scripts" / "verify-mcr-image.py"
)
assert SPEC is not None and SPEC.loader is not None
VERIFY_MCR_IMAGE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(VERIFY_MCR_IMAGE)

IMAGE = "mcr.microsoft.com/aks/ai-runtime/cetirun/openclaw-gateway:0.1.1"
DIGEST = "sha256:" + "a" * 64


class VerifyMCRImageTests(unittest.TestCase):
    def test_retries_when_platform_manifest_lags_tag_digest(self) -> None:
        """A synchronized index can reference a child manifest not yet in MCR."""
        pull_failure = subprocess.CalledProcessError(
            1,
            ["docker", "pull"],
            stderr="manifest unknown",
        )
        successful_pull = subprocess.CompletedProcess(["docker", "pull"], 0)
        inspect = subprocess.CompletedProcess(
            ["docker", "image", "inspect"],
            0,
            stdout="linux/amd64\n",
        )

        with (
            patch.object(VERIFY_MCR_IMAGE.shutil, "which", return_value="/usr/bin/docker"),
            patch.object(VERIFY_MCR_IMAGE, "manifest_matches", side_effect=[True, True]),
            patch.object(
                VERIFY_MCR_IMAGE.subprocess,
                "run",
                side_effect=[pull_failure, successful_pull, inspect],
            ) as run,
            patch.object(VERIFY_MCR_IMAGE.time, "sleep") as sleep,
        ):
            result = VERIFY_MCR_IMAGE.main(
                ["--image", IMAGE, "--digest", DIGEST, "--attempts", "2", "--interval", "0"]
            )

        self.assertEqual(result, 0)
        self.assertEqual(run.call_count, 3)
        sleep.assert_called_once_with(0)


if __name__ == "__main__":
    unittest.main()
