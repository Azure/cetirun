# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

import os
import re
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CHART = ROOT / "app/cetirun-chart"


class HarnessAssetsTests(unittest.TestCase):
    def test_image_sources_exist_in_unchanged_build_context(self):
        for kind in ("openclaw", "hermes"):
            dockerfile = CHART / f"agents/{kind}/Dockerfile"
            content = dockerfile.read_text()
            for line in content.splitlines():
                if not line.startswith("COPY ") or "--from=" in line:
                    continue
                source = [part for part in line.split()[1:] if not part.startswith("--")][0]
                self.assertTrue((CHART / source).exists(), f"{kind}: {source}")
            native_version = re.search(
                rf"^ARG {kind.upper()}_VERSION=([^\n]+)", content, re.MULTILINE
            )[1]
            profile = (CHART / f"agents/{kind}/profile.yaml").read_text()
            self.assertIn(f'nativeVersion: "{native_version}"', profile)

    def test_hermes_process_binding_wins_over_retained_home(self):
        source = (CHART / "agents/hermes/bootstrap/hermes-entrypoint.sh").read_text()
        start = source.index("while IFS= read")
        end = source.index('done < "$HERMES_HOME/.env"', start)
        loop = source[start:end] + 'done < "$HERMES_HOME/.env"\n'
        with tempfile.TemporaryDirectory() as directory:
            Path(directory, ".env").write_text("API_SERVER_PORT=8642\nAPI_SERVER_HOST=127.0.0.1\n")
            for port, expected in (("8080", "8080"), (None, "8642")):
                env = dict(os.environ, HERMES_HOME=directory)
                env.pop("API_SERVER_PORT", None)
                env.pop("API_SERVER_HOST", None)
                if port:
                    env["API_SERVER_PORT"] = port
                result = subprocess.run(
                    ["bash", "-eu", "-c", loop + 'printf "%s %s" "$API_SERVER_PORT" "$API_SERVER_HOST"'],
                    env=env, capture_output=True, text=True, check=True,
                )
                self.assertEqual(result.stdout, f"{expected} 127.0.0.1")


if __name__ == "__main__":
    unittest.main()
