# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Exercise the pinned native skill catalog and lookup against synthetic roots."""

import importlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import sys

sys.path.insert(0, str(Path(__file__).parent))
from native_network import install

install()

SOURCE = Path("/usr/local/lib/hermes-agent")
HOME = Path("/home/vibe")
ASSETS = Path("/opt/cetirun-memory")
CANONICAL = ASSETS / "skills/cetirun-memory/SKILL.md"
PLATFORM = Path("/opt/cetirun/cetiadmin/skills")


def load_module(name, path):
    package, _, child = name.rpartition(".")
    parent = importlib.import_module(package) if package else None
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    if parent is not None:
        setattr(parent, child, module)
    spec.loader.exec_module(module)
    return module


def skill(root, directory, name, body):
    path = root / directory / "SKILL.md"
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(f"---\nname: {name}\ndescription: Synthetic qualification skill.\n---\n{body}\n")
    return path


def main():
    assert sys.version_info[:2] == (3, 14)
    assert os.environ["CETIRUN_MEMORY_ASSETS"] == str(ASSETS)
    from hermes_cli.version_info import get_version_info

    assert get_version_info().commit == "b200f5bc60e31588ba2cb60e288029c4a54ba5fa"
    patch = load_module("memory_skill_patch", Path("/candidate-patch.py"))
    if os.environ.get("CETIRUN_HERMES_TEST_SKILL_PATCH") == "true":
        copied = HOME / "qualification-source"
        for relative, _expected, _replacements in patch.PATCHES["hermes"]:
            destination = copied / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(SOURCE / relative, destination)
        for path, content in patch.prepare_patches(copied, "hermes"):
            path.write_text(content)
        assert not patch.prepare_patches(copied, "hermes")
        skill_utils = load_module("agent.skill_utils", copied / "agent/skill_utils.py")
        skills_tool = load_module("tools.skills_tool", copied / "tools/skills_tool.py")
        prompt_builder = load_module("agent.prompt_builder", copied / "agent/prompt_builder.py")
    else:
        assert not patch.prepare_patches(SOURCE, "hermes"), "selected image does not contain the reserved Memory skill patch"
        skill_utils = importlib.import_module("agent.skill_utils")
        skills_tool = importlib.import_module("tools.skills_tool")
        prompt_builder = importlib.import_module("agent.prompt_builder")

    local = HOME / ".hermes/skills"
    personal = HOME / ".agents/skills"
    shared = HOME / ".aks/skills"
    configured = HOME / "configured-skills"
    for root in (local, personal, shared, configured):
        root.mkdir(parents=True, exist_ok=True)
    roots = [personal, shared, configured, PLATFORM, ASSETS / "skills"]
    config = HOME / ".hermes/config.yaml"

    def configure(directories, disabled=()):
        config.write_text(json.dumps({"skills": {"external_dirs": list(map(str, directories)), "disabled": list(disabled)}}))
        (HOME / ".hermes/.env").touch()
        skill_utils._external_dirs_cache_clear()
        skills_tool.clear_skills_cache()

    ordinary = {"local-user": local, "personal-user": personal, "shared-user": shared, "configured-user": configured}
    for name, root in ordinary.items():
        skill(root, name, name, "SYNTHETIC_" + name)
    ordinary["platform-user"] = PLATFORM
    shadow = skill(local, "cetirun-memory", "disguised-user", "SHADOW_DIRECTORY")
    alias = skill(personal, "memory-alias", "cetirun-memory", "SHADOW_FRONTMATTER")
    skill(shared, "cetirun-memory", "cetirun-memory", "SHADOW_SHARED")
    flat = local / "cetirun-memory.md"
    flat.write_text("---\nname: flat-user\n---\nSHADOW_FLAT\n")
    foreign_link = configured / "foreign-alias"
    foreign_link.symlink_to(alias.parent, target_is_directory=True)
    configure(roots)

    def read(name):
        skills_tool.reset_skill_view_dedup()
        return json.loads(skills_tool.skill_view(name, preprocess=False))

    indexed = skills_tool.find_all_skills()
    names = {item["name"] for item in indexed}
    assert set(ordinary) <= names, names
    assert "cetirun-memory" in names and "disguised-user" not in names
    assert "CANONICAL_MEMORY_FIXTURE" in json.dumps(read("cetirun-memory"))
    for name in ordinary:
        assert "SYNTHETIC_" + name in json.dumps(read(name)), name
    for name in ("memory-alias", "foreign-alias"):
        result = read(name)
        assert result.get("success") is False and "SHADOW_" not in json.dumps(result), result
    for root in (local, personal, shared, configured):
        found = list(skill_utils.iter_skill_index_files(root, "SKILL.md"))
        assert all(path.parent.name != "cetirun-memory" and path != alias for path in found)
    candidates = skills_tool._collect_skill_candidates("cetirun-memory", None, [local, personal, shared, configured, ASSETS / "skills"])
    assert candidates and all(path.resolve() == CANONICAL for _, path in candidates), candidates

    trusted_link = local / "canonical-alias"
    trusted_link.symlink_to(CANONICAL.parent, target_is_directory=True)
    skills_tool.clear_skills_cache()
    assert "CANONICAL_MEMORY_FIXTURE" in json.dumps(read("canonical-alias"))
    assert all(path.resolve() == CANONICAL for _, path in skills_tool._collect_skill_candidates(
        "cetirun-memory", None, [local, personal, shared, configured, ASSETS / "skills"]))
    os.environ.pop("CETIRUN_MEMORY_ASSETS")
    prompt_builder.clear_skills_system_prompt_cache(clear_snapshot=True)
    prompt_builder.build_skills_system_prompt()
    snapshot_path = prompt_builder._skills_prompt_snapshot_path()
    snapshot = json.loads(snapshot_path.read_text())
    forged = next(entry for entry in snapshot["skills"] if entry["rel"] == "canonical-alias/SKILL.md")
    forged.update(frontmatter_name="cetirun-memory", description="CACHED_CANONICAL_SHADOW")
    snapshot_path.write_text(json.dumps(snapshot))
    retained = snapshot_path.read_bytes()
    os.environ["CETIRUN_MEMORY_ASSETS"] = str(ASSETS)
    prompt_builder.clear_skills_system_prompt_cache(clear_snapshot=False)
    prompt = prompt_builder.build_skills_system_prompt()
    assert "CACHED_CANONICAL_SHADOW" not in prompt and "Synthetic canonical Memory skill" in prompt
    assert snapshot_path.read_bytes() == retained
    trusted_link.unlink()

    skill(personal, "created-user", "created-user", "SYNTHETIC_NEW_PERSONAL")
    skills_tool.clear_skills_cache()
    assert "SYNTHETIC_NEW_PERSONAL" in json.dumps(read("created-user"))

    shadow.write_text("---\nname: cetirun-memory\ndescription: RETAINED_SHADOW_PROMPT\n---\nSynthetic shadow.\n")
    os.environ.pop("CETIRUN_MEMORY_ASSETS")
    prompt_builder.clear_skills_system_prompt_cache(clear_snapshot=True)
    assert "RETAINED_SHADOW_PROMPT" in prompt_builder.build_skills_system_prompt()
    assert snapshot_path.is_file()
    retained = snapshot_path.read_bytes()
    os.environ["CETIRUN_MEMORY_ASSETS"] = str(ASSETS)
    prompt_builder.clear_skills_system_prompt_cache(clear_snapshot=False)
    prompt = prompt_builder.build_skills_system_prompt()
    assert "RETAINED_SHADOW_PROMPT" not in prompt and "Synthetic canonical Memory skill" in prompt
    assert all(name in prompt for name in ordinary)
    assert snapshot_path.read_bytes() == retained
    os.environ.pop("CETIRUN_MEMORY_ASSETS")
    prompt_builder.clear_skills_system_prompt_cache(clear_snapshot=False)
    assert "RETAINED_SHADOW_PROMPT" in prompt_builder.build_skills_system_prompt()
    assert snapshot_path.read_bytes() == retained
    os.environ["CETIRUN_MEMORY_ASSETS"] = str(ASSETS)
    snapshot = json.loads(snapshot_path.read_text())
    forged = next(entry for entry in snapshot["skills"] if entry["skill_name"] == "local-user")
    forged.update(frontmatter_name="cetirun-memory", description="CACHED_NAME_SHADOW")
    snapshot_path.write_text(json.dumps(snapshot))
    retained = snapshot_path.read_bytes()
    prompt_builder.clear_skills_system_prompt_cache(clear_snapshot=False)
    prompt = prompt_builder.build_skills_system_prompt()
    assert "CACHED_NAME_SHADOW" not in prompt and "Synthetic canonical Memory skill" in prompt
    assert snapshot_path.read_bytes() == retained
    prompt_builder.clear_skills_system_prompt_cache(clear_snapshot=True)
    (shared / "shared-user/SKILL.md").write_text(
        "---\nname: shared-user\ndescription: SHARED_VERSION_TWO\n---\nSynthetic shared update.\n")
    skill(shared, "shared-added-later", "shared-added-later", "SYNTHETIC_SHARED_ADDITION")
    prompt = prompt_builder.build_skills_system_prompt()
    assert "SHARED_VERSION_TWO" in prompt and "shared-added-later" in prompt
    assert "RETAINED_SHADOW_PROMPT" not in prompt
    assert not snapshot_path.exists()

    app_skill = skill(local, "app-gated", "app-gated", "SYNTHETIC_APP_GATE")
    app_skill.write_text("---\nname: app-gated\ndescription: APP_GATE_DESCRIPTION\nrequires_apps: [SyntheticApp]\n---\nSynthetic fixture.\n")
    original_apps = prompt_builder.skill_matches_apps
    app_state = {"available": False}
    prompt_builder.skill_matches_apps = lambda fm: (
        app_state["available"] if "SyntheticApp" in fm.get("requires_apps", []) else original_apps(fm)
    )
    try:
        for enabled in (False, True):
            if enabled:
                os.environ["CETIRUN_MEMORY_ASSETS"] = str(ASSETS)
            else:
                os.environ.pop("CETIRUN_MEMORY_ASSETS")
            prompt_builder.clear_skills_system_prompt_cache(clear_snapshot=False)
            for available in (False, True, False):
                app_state["available"] = available
                prompt = prompt_builder.build_skills_system_prompt()
                assert ("APP_GATE_DESCRIPTION" in prompt) is available
    finally:
        prompt_builder.skill_matches_apps = original_apps
        os.environ["CETIRUN_MEMORY_ASSETS"] = str(ASSETS)
        app_skill.unlink()

    configure(roots, disabled=["cetirun-memory"])
    prompt = prompt_builder.build_skills_system_prompt()
    assert "Synthetic canonical Memory skill" not in prompt and "RETAINED_SHADOW_PROMPT" not in prompt
    assert all(name in prompt for name in ordinary)
    configure(roots)
    assert "Synthetic canonical Memory skill" in prompt_builder.build_skills_system_prompt()

    configure(roots[:-1])
    assert "cetirun-memory" not in {item["name"] for item in skills_tool.find_all_skills()}
    assert read("cetirun-memory").get("success") is False
    configure(roots)
    os.environ.pop("CETIRUN_MEMORY_ASSETS")
    skills_tool.clear_skills_cache()
    assert "SHADOW_FRONTMATTER" in json.dumps(read("memory-alias"))
    assert shadow in list(skill_utils.iter_skill_index_files(local, "SKILL.md"))
    os.environ["CETIRUN_MEMORY_ASSETS"] = str(ASSETS)
    assert CANONICAL.read_text().endswith("CANONICAL_MEMORY_FIXTURE\n")
    try:
        CANONICAL.write_text("unexpected write")
    except OSError:
        pass
    else:
        raise AssertionError("canonical Memory asset was writable")
    print(json.dumps({"canonical_memory_read": True, "shadow_index_and_lookup_rejected": True,
                      "canonical_and_foreign_aliases": True, "ordinary_skills_retained": True,
                      "personal_skill_creation": True, "missing_canonical_no_fallback": True,
                      "memory_disabled_native_behavior": True, "canonical_assets_read_only": True,
                      "retained_snapshot_filtered": True, "shared_prompt_refresh": True,
                      "canonical_snapshot_metadata_rejected": True, "snapshot_mode_preserved": True,
                      "canonical_skill_disable_preserved": True, "dynamic_app_visibility_preserved": True}))


if __name__ == "__main__":
    main()
