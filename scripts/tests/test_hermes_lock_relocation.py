# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

import importlib.util
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
VALIDATOR_PATH = (
    ROOT
    / "app"
    / "cetirun-chart"
    / "agents"
    / "hermes"
    / "validate_uv_lock_relocation.py"
)
SPEC = importlib.util.spec_from_file_location("validate_uv_lock_relocation", VALIDATOR_PATH)
VALIDATOR = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(VALIDATOR)


def lock(registry, version="1.0.0", hashes=("sha256:aaa", "sha256:bbb")):
    artifacts = ",\n".join(
        f'    {{ url = "https://example.invalid/{value[7:]}.whl", hash = "{value}" }}'
        for value in hashes
    )
    return f'''version = 1

[[package]]
name = "example"
version = "{version}"
source = {{ registry = "{registry}" }}
wheels = [
{artifacts}
]

[[package]]
name = "local-project"
version = "0.1.0"
source = {{ editable = "." }}
'''


class HermesLockRelocationTests(unittest.TestCase):
    def validate(self, original, relocated):
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            original_path = root / "original.lock"
            relocated_path = root / "relocated.lock"
            original_path.write_text(original, encoding="utf-8")
            relocated_path.write_text(relocated, encoding="utf-8")
            return VALIDATOR.validate(original_path, relocated_path)

    def test_accepts_registry_relocation_and_artifact_subset(self):
        errors = self.validate(
            lock("https://pypi.org/simple"),
            lock("https://packages.example/simple", hashes=("sha256:aaa",)),
        )
        self.assertEqual(errors, [])

    def test_rejects_version_change(self):
        errors = self.validate(
            lock("https://pypi.org/simple"),
            lock("https://packages.example/simple", version="2.0.0"),
        )
        self.assertIn("package identities changed", errors)

    def test_rejects_unpinned_artifact(self):
        errors = self.validate(
            lock("https://pypi.org/simple"),
            lock("https://packages.example/simple", hashes=("sha256:ccc",)),
        )
        self.assertTrue(any("unpinned artifact hashes" in error for error in errors))

    def test_rejects_removing_every_artifact(self):
        errors = self.validate(
            lock("https://pypi.org/simple"),
            lock("https://packages.example/simple", hashes=()),
        )
        self.assertTrue(any("lost all pinned artifacts" in error for error in errors))


if __name__ == "__main__":
    unittest.main()
