# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""The Hermes "Share usage stats with Nous" answer in a synthetic HOME."""

from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import yaml

CHART = Path(__file__).resolve().parents[1]
APPLY = CHART / "agents/hermes/bootstrap/hermes-apply-config.py"
NO_THANKS = {"enabled": False, "send": False, "offer_version": 2}


class HermesSharedMetricsTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        root = Path(directory.name)
        self.home = root / "home"
        (self.home / ".hermes").mkdir(parents=True)
        self.config = self.home / ".hermes/config.yaml"
        self.generated = root / "generated.yaml"
        self.generated_env = root / "generated.env"
        self.write_generated({})
        self.generated_env.write_text("API_SERVER_KEY=sk-dummy\n")

    def write_generated(self, extra):
        generated = {
            "model": {"provider": "cetirun-gateway", "default": "synthetic-model"},
            "providers": {"cetirun-gateway": {"api": "http://gateway.example.invalid/v1", "models": {"synthetic-model": {}}}},
        }
        generated.update(extra)
        self.generated.write_text(yaml.safe_dump(generated))

    def apply(self, *arguments):
        arguments = arguments or ("--config", str(self.generated), "--env", str(self.generated_env))
        result = subprocess.run([sys.executable, str(APPLY), "--home", str(self.home), *arguments],
                                text=True, capture_output=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        return yaml.safe_load(self.config.read_text())

    def test_fresh_home_answers_no_thanks(self):
        config = self.apply()
        self.assertEqual(config["telemetry"]["shared_metrics"], NO_THANKS)
        text = self.config.read_text()
        self.assertEqual(self.apply(), config)
        self.assertEqual(self.config.read_text(), text)
        self.apply("--check")

    def test_saved_opt_in_is_kept(self):
        for saved in ({"enabled": True, "send": True, "offer_version": 2}, {"enabled": True, "send": False}):
            with self.subTest(saved=saved):
                self.config.write_text(yaml.safe_dump({"telemetry": {"shared_metrics": saved, "other": 1}}))
                config = self.apply()
                self.assertEqual(config["telemetry"], {"shared_metrics": saved, "other": 1})

    def test_old_off_answer_is_settled(self):
        self.config.write_text(yaml.safe_dump({"telemetry": {"shared_metrics": {"enabled": False, "send": False}}}))
        self.assertEqual(self.apply()["telemetry"]["shared_metrics"], NO_THANKS)

    def test_generated_file_cannot_change_a_saved_answer(self):
        self.config.write_text(yaml.safe_dump({"telemetry": {"shared_metrics": {"enabled": True, "send": True, "offer_version": 2}}}))
        self.write_generated({"telemetry": {"shared_metrics": {"enabled": False, "send": False}}})
        self.assertEqual(self.apply()["telemetry"]["shared_metrics"], {"enabled": True, "send": True, "offer_version": 2})

    def test_invalid_telemetry_value_is_replaced(self):
        self.config.write_text(yaml.safe_dump({"telemetry": "off"}))
        self.assertEqual(self.apply()["telemetry"], {"shared_metrics": NO_THANKS})


if __name__ == "__main__":
    unittest.main()
