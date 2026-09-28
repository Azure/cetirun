# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

from contextlib import redirect_stdout
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch
import urllib.error
import urllib.request


EXAMPLE = Path(__file__).resolve().parents[2] / "examples/custom-agent"


def load(name):
    spec = importlib.util.spec_from_file_location("qualification_" + name, EXAMPLE / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


GATEWAY, PROBE = load("mock_gateway"), load("probe")
with patch.dict(sys.modules, {"mock_gateway": GATEWAY, "probe": PROBE}):
    QUALIFY = load("qualify")


class ImageQualificationTests(unittest.TestCase):
    def run_candidate(self, fault=None):
        image_id = "sha256:" + "a" * 64
        initial_digest = hashlib.sha256((EXAMPLE / "initial-home/seed.txt").read_bytes()).hexdigest()
        preserved_digest = hashlib.sha256(PROBE.RESTART_CONTENT).hexdigest()
        initializations = 0
        memory = None

        def fake_run(args, capture=False):
            nonlocal initializations, memory
            if args[:3] == ["docker", "image", "inspect"]:
                return json.dumps([{"Id": image_id, "Config": {"User": "2000:2000"}}])
            if "--mode" in args:
                mode = args[args.index("--mode") + 1]
                value = {"uid": 2000, "gid": 2001 if fault == "gid" else 2000,
                    "username": "worker", "home": "/home/worker", "context_readonly": fault != "writable-context",
                    "flag": "seeded-v1\n" + image_id + "\n",
                    "seed_digest": preserved_digest if initializations > 1 or mode == "prepare-restart" else initial_digest,
                    "memory": memory}
                if fault == "missing-flag":
                    value.pop("flag")
                if fault == "no-copy" and mode == "seed":
                    value["seed_digest"] = "not-copied"
                if fault == "recopy" and initializations > 1:
                    value["seed_digest"] = initial_digest
                if fault == "memory":
                    value["memory"] = None
                return json.dumps(value)
            if args[-1] == "/etc/cetirun/custom-init-home.sh":
                initializations += 1
                return ""
            if args[-1] == "--headless-once":
                mounted = next(value for value in args if value.startswith("type=bind,src=") and ",dst=/home/worker/.agents" in value)
                fields = dict(part.split("=", 1) for part in mounted.split(",") if "=" in part)
                context_dir = EXAMPLE / "context" if fault == "hardcoded-context" else Path(fields["src"])
                context = "\n".join(path.read_text() for path in context_dir.rglob("*.md"))
                base = next(value.split("=", 1)[1] for value in args if value.startswith("OPENAI_BASE_URL="))
                request = urllib.request.Request(base + "/chat/completions",
                    data=json.dumps({"model": "mock-upstream-model", "messages": [
                        {"role": "system", "content": "unapproved" if fault == "gateway-error" else context},
                        {"role": "user", "content": "Run example"},
                    ]}).encode(), headers={"Authorization": "Bearer sk-dummy", "Content-Type": "application/json"})
                opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
                try:
                    with opener.open(request, timeout=3) as response:
                        memory = json.load(response)
                except urllib.error.HTTPError as error:
                    error.close()  # Simulate an image that exits 0 despite a rejected request.
                return ""
            raise AssertionError("Unexpected Docker command")

        output = io.StringIO()
        with tempfile.TemporaryDirectory() as directory, patch.object(QUALIFY.shutil, "which", return_value="/synthetic/docker"), \
                patch.object(QUALIFY, "run", side_effect=fake_run), redirect_stdout(output):
            QUALIFY.qualify_image("example:local", Path(directory))
        return json.loads(output.getvalue())

    def test_all_claims_require_matching_evidence(self):
        for fault in ("gid", "missing-flag", "no-copy", "recopy", "writable-context", "gateway-error", "hardcoded-context", "memory"):
            with self.subTest(fault=fault), self.assertRaises(ValueError):
                self.run_candidate(fault)

    def test_valid_evidence_produces_bounded_report(self):
        result = self.run_candidate()
        self.assertEqual(result["observed_identity"]["gid"], 2000)
        self.assertEqual(result["checks"], ["same-local-image", "seed-and-reinitialize", "mock-model-context-memory"])
        self.assertIn("Kubernetes", result["not_checked"])


if __name__ == "__main__":
    unittest.main()
