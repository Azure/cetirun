# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Pinned native skill qualification using synthetic HOME and read-only assets."""

import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

CHART = Path(__file__).resolve().parents[1]
SOURCE = os.environ.get("HERMES_QUALIFICATION_SOURCE", "")
IMAGE = os.environ.get("CETIRUN_HERMES_IMAGE", "")
PATCH = CHART / "configs/apply-memory-skills.py"


class HermesNativeSkillsTests(unittest.TestCase):
    @unittest.skipUnless(SOURCE, "requires pinned Hermes source")
    def test_patch_rejects_drift_and_is_idempotent(self):
        spec = importlib.util.spec_from_file_location("memory_skill_patch", PATCH)
        patch = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(patch)
        with tempfile.TemporaryDirectory(prefix="hermes-skill-source-") as directory:
            root = Path(directory)
            for relative, _expected, _replacements in patch.PATCHES["hermes"]:
                target = root / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(Path(SOURCE) / relative, target)
            for path, content in patch.prepare_patches(root, "hermes"):
                path.write_text(content)
            self.assertEqual(patch.prepare_patches(root, "hermes"), [])
            for relative, _expected, _replacements in patch.PATCHES["hermes"]:
                target = root / relative
                original = target.read_text()
                target.write_text(original + "\n# source drift\n")
                with self.assertRaisesRegex(ValueError, "Unsupported hermes skill source"):
                    patch.prepare_patches(root, "hermes")
                target.write_text(original)

    @unittest.skipUnless(IMAGE, "requires an explicitly selected cached Hermes image")
    def test_native_catalog_and_lookup_keep_reserved_memory_and_user_skills(self):
        with tempfile.TemporaryDirectory(prefix="hermes-skill-assets-") as directory:
            root = Path(directory)
            assets = root / "assets"
            skill = assets / "skills/cetirun-memory/SKILL.md"
            skill.parent.mkdir(parents=True)
            skill.write_text("---\nname: cetirun-memory\ndescription: Synthetic canonical Memory skill.\n---\nCANONICAL_MEMORY_FIXTURE\n")
            skill.chmod(0o444)
            platform = root / "platform"
            platform_skill = platform / "platform-user/SKILL.md"
            platform_skill.parent.mkdir(parents=True)
            platform_skill.write_text("---\nname: platform-user\ndescription: Synthetic platform skill.\n---\nSYNTHETIC_platform-user\n")
            platform_skill.chmod(0o444)
            result = subprocess.run([
                "docker", "run", "--rm", "--pull=never", "--network=none", "--read-only", "--cap-drop=ALL",
                "--security-opt=no-new-privileges", "--user=1024:1024", "--workdir=/home/vibe",
                "--tmpfs=/home/vibe:rw,mode=0700,uid=1024,gid=1024", "--tmpfs=/tmp:rw,mode=1777",
                f"--mount=type=bind,src={assets},dst=/opt/cetirun-memory,readonly",
                f"--mount=type=bind,src={platform},dst=/opt/cetirun/cetiadmin/skills,readonly",
                f"--mount=type=bind,src={CHART / 'tests/fixtures'},dst=/candidate,readonly",
                f"--mount=type=bind,src={PATCH},dst=/candidate-patch.py,readonly",
                "-e", "HOME=/home/vibe", "-e", "HERMES_HOME=/home/vibe/.hermes",
                "-e", "CETIRUN_MEMORY_ASSETS=/opt/cetirun-memory", "-e", "PYTHONDONTWRITEBYTECODE=1",
                "-e", "CETIRUN_HERMES_TEST_SKILL_PATCH=" + os.environ.get("CETIRUN_HERMES_TEST_SKILL_PATCH", "false"),
                "--entrypoint=/usr/local/bin/hermes-python", IMAGE, "/candidate/hermes_skills_native.py",
            ], capture_output=True, text=True, timeout=90)
            self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
            self.assertEqual(json.loads(result.stdout.strip().splitlines()[-1]), {
                "canonical_memory_read": True, "shadow_index_and_lookup_rejected": True,
                "canonical_and_foreign_aliases": True, "ordinary_skills_retained": True,
                "personal_skill_creation": True, "missing_canonical_no_fallback": True,
                "memory_disabled_native_behavior": True, "canonical_assets_read_only": True,
                "retained_snapshot_filtered": True, "shared_prompt_refresh": True,
                "canonical_snapshot_metadata_rejected": True, "snapshot_mode_preserved": True,
                "canonical_skill_disable_preserved": True, "dynamic_app_visibility_preserved": True,
            })


if __name__ == "__main__":
    unittest.main()
