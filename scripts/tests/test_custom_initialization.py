# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

import importlib.util
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "app/cetirun-chart/agents/custom/bootstrap/init-home.sh"
COMMON = ROOT / "app/cetirun-chart/configs/init-home-common.sh"
IMAGE = "registry.example.com/agent@sha256:" + "a" * 64
TOOLS = ("sh", "cp", "mkdir", "mv", "rm", "chmod", "mktemp", "find", "cmp")


class CustomInitializationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source = self.root / "image-home"
        self.target = self.root / "pvc"
        self.context = self.root / "context"
        self.tools = self.root / "bin"
        for directory in (self.source, self.target, self.context, self.tools):
            directory.mkdir()
        (self.context / "skills/cetirun-memory").mkdir(parents=True)
        (self.context / "AGENTS.md").write_text("synthetic canonical contract")
        (self.context / "skills/cetirun-memory/SKILL.md").write_text("synthetic memory contract")
        for name in TOOLS:
            tool = shutil.which(name)
            self.assertIsNotNone(tool, f"offline prerequisite missing: {name}")
            (self.tools / name).symlink_to(tool)
        self.env = {
            "PATH": str(self.tools),
            "SOURCE_HOME": str(self.source),
            "TARGET_HOME": str(self.target),
            "CETIRUN_INIT_IMAGE": IMAGE,
            "CETIRUN_INIT_COMMON": str(COMMON),
            "CETIRUN_CONTEXT_DIR": str(self.context),
            "SCRIPT": str(SCRIPT),
        }

    def run_init(self, setup=""):
        return subprocess.run(
            ["/bin/sh", "-c", setup + '\n. "$SCRIPT"'],
            env=self.env, capture_output=True, text=True, timeout=10,
        )

    def test_custom_runs_common_stages_with_only_basic_tools(self):
        (self.source / ".config").mkdir()
        (self.source / ".config/initial").write_text("keep")
        (self.source / "dependency").write_text("v1")
        (self.source / "dependency").chmod(0o750)
        (self.source / "dependency-link").symlink_to("dependency")
        result = self.run_init()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.target / ".config/initial").read_text(), "keep")
        self.assertEqual((self.target / "dependency").stat().st_mode & 0o777, 0o750)
        self.assertEqual(os.readlink(self.target / "dependency-link"), "dependency")
        self.assertTrue((self.target / ".cetirun-home-seeded").is_file())
        for stage in ("seed_home_once", "agent_prepare_home", "agent_apply_config", "agent_prepare_integrations", "agent_verify"):
            self.assertIn(stage, result.stderr)
        self.assertFalse((self.target / ".hermes").exists())
        self.assertFalse((self.target / ".openclaw").exists())

    def test_reinitialize_and_upgrade_preserve_home(self):
        (self.source / "dependency").write_text("v1")
        self.assertEqual(self.run_init().returncode, 0)
        (self.target / "session").write_text("private session")
        (self.source / "dependency").write_text("v2")
        (self.source / "new-dependency").write_text("v2")
        self.env["CETIRUN_INIT_IMAGE"] = "registry.example.com/agent@sha256:" + "b" * 64
        self.assertEqual(self.run_init().returncode, 0)
        self.assertEqual((self.target / "dependency").read_text(), "v1")
        self.assertEqual((self.target / "session").read_text(), "private session")
        self.assertFalse((self.target / "new-dependency").exists())

    def test_copy_failure_same_digest_recovers(self):
        (self.source / "dependency").write_text("v1")
        result = self.run_init("cp() { return 23; }")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.target / ".cetirun-home-seeded").exists())
        self.assertEqual(self.run_init().returncode, 0)
        self.assertEqual((self.target / "dependency").read_text(), "v1")

    def test_unknown_nonempty_home_is_preserved(self):
        (self.target / "private").write_text("do not touch")
        result = self.run_init()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("HomeSeedStateUnknown", result.stderr)
        self.assertEqual((self.target / "private").read_text(), "do not touch")

    def test_missing_command_fails_without_installing(self):
        (self.tools / "cp").unlink()
        result = self.run_init()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("InitToolsMissing: cp", result.stderr)
        self.assertEqual(list(self.target.iterdir()), [])

    def test_missing_context_fails_before_copy(self):
        (self.context / "AGENTS.md").unlink()
        result = self.run_init()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("ContextBindingPending", result.stderr)
        self.assertEqual(list(self.target.iterdir()), [])

    @unittest.skipIf(os.geteuid() == 0, "requires a non-root test process")
    def test_unwritable_home_fails_without_permission_repair(self):
        self.target.chmod(0o500)
        try:
            result = self.run_init()
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("HomePermissionsInvalid", result.stderr)
            self.assertEqual(self.target.stat().st_mode & 0o777, 0o500)
        finally:
            self.target.chmod(0o700)

    def test_image_agents_symlink_is_not_repaired_or_followed(self):
        outside = self.root / "outside"
        outside.mkdir()
        (outside / "private").write_text("keep")
        (self.source / ".agents").symlink_to(outside)
        result = self.run_init()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("ImageAgentsPathInvalid", result.stderr)
        self.assertEqual((outside / "private").read_text(), "keep")
        self.assertEqual(list(self.target.iterdir()), [])
        (self.source / ".agents").unlink()
        (self.source / ".agents").mkdir()
        self.env["CETIRUN_INIT_IMAGE"] = "registry.example.com/agent@sha256:" + "b" * 64
        self.assertEqual(self.run_init().returncode, 0)

    def test_source_agents_file_fails_before_flag_and_can_be_corrected(self):
        (self.source / ".agents").write_text("not a directory")
        failed = self.run_init()
        self.assertNotEqual(failed.returncode, 0)
        self.assertIn("ImageAgentsPathInvalid", failed.stderr)
        self.assertEqual(list(self.target.iterdir()), [])
        (self.source / ".agents").unlink()
        (self.source / ".agents").mkdir()
        self.env["CETIRUN_INIT_IMAGE"] = "registry.example.com/agent@sha256:" + "c" * 64
        self.assertEqual(self.run_init().returncode, 0)

    def test_completed_home_does_not_revalidate_an_unused_image_agents_path(self):
        self.assertEqual(self.run_init().returncode, 0)
        (self.source / ".agents").symlink_to(self.root / "absent-image-context")
        self.env["CETIRUN_INIT_IMAGE"] = "registry.example.com/agent@sha256:" + "d" * 64
        result = self.run_init()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((self.target / ".agents").is_dir())

    def test_image_context_is_preserved_without_rewriting_approved_material(self):
        (self.source / ".agents/skills/cetirun-memory").mkdir(parents=True)
        (self.source / ".agents/skills/cetirun-memory/SKILL.md").write_text("image copy")
        self.assertEqual(self.run_init().returncode, 0)
        self.assertEqual((self.target / ".agents/skills/cetirun-memory/SKILL.md").read_text(), "image copy")
        self.assertEqual((self.context / "skills/cetirun-memory/SKILL.md").read_text(), "synthetic memory contract")
        # Read-only shadowing is asserted separately against rendered mounts.

    def test_lost_found_directory_is_preserved_but_file_and_symlink_are_not_empty_home(self):
        for kind in ("directory", "file", "symlink"):
            with self.subTest(kind=kind):
                target = self.root / ("target-" + kind)
                target.mkdir()
                entry = target / "lost+found"
                if kind == "directory":
                    entry.mkdir()
                    (entry / "recovered").write_text("preserve")
                elif kind == "file":
                    entry.write_text("preserve")
                else:
                    entry.symlink_to(self.source)
                self.env["TARGET_HOME"] = str(target)
                result = self.run_init()
                self.assertEqual(result.returncode == 0, kind == "directory", result.stderr)
                self.assertEqual((target / ".cetirun-home-seeded").exists(), kind == "directory")
                if kind == "directory":
                    self.assertEqual((entry / "recovered").read_text(), "preserve")

    def test_two_homes_do_not_share_flags_or_private_data(self):
        self.assertEqual(self.run_init().returncode, 0)
        (self.target / "private").write_text("one")
        other = self.root / "other"
        other.mkdir()
        self.env["TARGET_HOME"] = str(other)
        self.assertEqual(self.run_init().returncode, 0)
        self.assertFalse((other / "private").exists())
        self.assertTrue((other / ".cetirun-home-seeded").is_file())

    def prepare_shared_skills(self):
        shared = self.root / "shared-skills"
        view = self.root / "context-view"
        shared.mkdir()
        view.mkdir()
        (shared / "team-skill").mkdir()
        (shared / "team-skill/SKILL.md").write_text("unique approved shared instruction")
        (shared / "team-skill/helper").write_text("synthetic helper")
        (shared / "team-skill/helper").chmod(0o755)
        self.env["CETIRUN_SHARED_SKILLS_DIR"] = str(shared)
        self.env["CETIRUN_CONTEXT_VIEW"] = str(view)
        return shared, view

    def test_shared_skills_are_materialized_without_modifying_home_or_sources(self):
        shared, view = self.prepare_shared_skills()
        (self.source / ".agents/skills/team-skill").mkdir(parents=True)
        (self.source / ".agents/skills/team-skill/SKILL.md").write_text("private image copy")
        result = self.run_init()
        self.assertEqual(result.returncode, 0, result.stderr)
        published = view / "current"
        self.assertEqual((published / "AGENTS.md").read_bytes(), (self.context / "AGENTS.md").read_bytes())
        self.assertEqual(
            (published / "skills/cetirun-memory/SKILL.md").read_bytes(),
            (self.context / "skills/cetirun-memory/SKILL.md").read_bytes(),
        )
        self.assertEqual((published / "skills/team-skill/SKILL.md").read_text(), "unique approved shared instruction")
        self.assertEqual((published / "skills/team-skill/helper").stat().st_mode & 0o777, 0o755)
        self.assertEqual((shared / "team-skill/SKILL.md").read_text(), "unique approved shared instruction")
        self.assertEqual((self.target / ".agents/skills/team-skill/SKILL.md").read_text(), "private image copy")
        self.assertEqual([p.name for p in view.iterdir()], ["current"])
        spec = importlib.util.spec_from_file_location("skills_reader", ROOT / "examples/custom-agent/agent.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        container_home = self.root / "container-home"
        container_home.mkdir()
        (container_home / ".agents").symlink_to(published)
        # Exercise the actual discovery code without constructing any model
        # binding, mock service or network connection.
        agent = module.Agent.__new__(module.Agent)
        agent.home = container_home
        context = agent.load_context()
        self.assertIn("unique approved shared instruction", context)
        self.assertIn("synthetic canonical contract", context)
        self.assertIn("synthetic memory contract", context)
        self.assertNotIn("private image copy", context)

    def test_shared_skill_cannot_shadow_canonical_context(self):
        shared, view = self.prepare_shared_skills()
        (shared / "cetirun-memory").mkdir()
        (shared / "cetirun-memory/SKILL.md").write_text("unapproved replacement")
        result = self.run_init()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("SharedSkillConflictsWithCanonicalContext", result.stderr)
        self.assertEqual(list(view.iterdir()), [])
        self.assertEqual((self.context / "skills/cetirun-memory/SKILL.md").read_text(), "synthetic memory contract")
        self.assertEqual((shared / "cetirun-memory/SKILL.md").read_text(), "unapproved replacement")

    def test_missing_shared_source_fails_before_home_seed(self):
        _, view = self.prepare_shared_skills()
        self.env["CETIRUN_SHARED_SKILLS_DIR"] = str(self.root / "missing")
        result = self.run_init()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("SharedSkillsUnavailable", result.stderr)
        self.assertEqual(list(self.target.iterdir()), [])
        self.assertEqual(list(view.iterdir()), [])

    def test_shared_copy_failure_is_retryable_without_partial_publication(self):
        shared, view = self.prepare_shared_skills()
        result = self.run_init('cp() { if [ "$1" = "-RP" ]; then return 23; fi; command cp "$@"; }')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(list(view.iterdir()), [])
        self.assertTrue((self.target / ".cetirun-home-seeded").exists())
        (self.target / "private-memory").write_text("preserve")
        (shared / "team-skill/SKILL.md").write_text("retry instruction")
        result = self.run_init()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((view / "current/skills/team-skill/SKILL.md").read_text(), "retry instruction")
        self.assertEqual((self.target / "private-memory").read_text(), "preserve")

    def test_shared_view_is_stable_on_retry_and_refreshed_for_a_new_pod(self):
        shared, view = self.prepare_shared_skills()
        self.assertEqual(self.run_init().returncode, 0)
        (shared / "team-skill/SKILL.md").write_text("new published instruction")
        self.assertEqual(self.run_init().returncode, 0)
        self.assertEqual((view / "current/skills/team-skill/SKILL.md").read_text(), "unique approved shared instruction")
        replacement = self.root / "replacement-pod-view"
        replacement.mkdir()
        self.env["CETIRUN_CONTEXT_VIEW"] = str(replacement)
        self.assertEqual(self.run_init().returncode, 0)
        self.assertEqual((replacement / "current/skills/team-skill/SKILL.md").read_text(), "new published instruction")

    def test_configmap_symlinks_are_materialized_but_shared_links_are_not_followed(self):
        shared, view = self.prepare_shared_skills()
        data = self.context / "..data"
        data.mkdir()
        (self.context / "skills").rename(data / "skills")
        (self.context / "AGENTS.md").rename(data / "AGENTS.md")
        (self.context / "skills").symlink_to("..data/skills")
        (self.context / "AGENTS.md").symlink_to("..data/AGENTS.md")
        (data / "skills").chmod(0o555)
        (shared / "outside-link").symlink_to(self.source)
        try:
            result = self.run_init()
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertFalse((view / "current/AGENTS.md").is_symlink())
            self.assertFalse((view / "current/skills/cetirun-memory").is_symlink())
            self.assertEqual(os.readlink(view / "current/skills/outside-link"), str(self.source))
        finally:
            (data / "skills").chmod(0o755)

    def test_shared_special_files_are_not_copied(self):
        shared, view = self.prepare_shared_skills()
        os.mkfifo(shared / "pipe")
        result = self.run_init()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("UnsupportedSkillFile", result.stderr)
        self.assertEqual(list(view.iterdir()), [])


if __name__ == "__main__":
    unittest.main()
