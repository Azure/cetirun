# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Check real Hermes discovery and lookup against changing synthetic shared skills."""

import json
import os
from pathlib import Path
import sys
import tempfile


def skill(directory, name, description):
    directory.mkdir(parents=True, exist_ok=True)
    (directory / "SKILL.md").write_text(
        f"---\nname: {name}\ndescription: {description}\n---\nSynthetic skill.\n"
    )


with tempfile.TemporaryDirectory(prefix="memory-skills-hermes-") as temporary:
    root = Path(temporary)
    assets = Path(os.environ.get("MEMORY_SKILLS_ASSETS", root / "platform"))
    platform = assets / "skills"
    shared, personal = root / "shared", root / "personal"
    profile = root / ".hermes"
    profile.mkdir()
    os.environ.update(HOME=str(root), HERMES_HOME=str(profile), CETIRUN_MEMORY_ASSETS=str(assets))
    os.chdir(root)
    (profile / "config.yaml").write_text(json.dumps({"skills": {"external_dirs": list(map(str, [shared, personal, platform]))}}))
    if "MEMORY_SKILLS_ASSETS" not in os.environ:
        skill(platform / "cetirun-memory", "cetirun-memory", "Platform fixture")
    skill(shared / "team-skill", "team-skill", "Version one")
    skill(personal / "personal-skill", "personal-skill", "Personal fixture")
    sys.path.insert(0, os.environ["MEMORY_HERMES_SOURCE"])
    from tools.skills_tool import _find_all_skills, _locate_skill, _SKILLS_CACHE
    from agent.prompt_builder import build_skills_system_prompt, clear_skills_system_prompt_cache

    def check(description):
        for directories in ([shared, personal, platform], [platform, personal, shared]):
            error, _, found = _locate_skill("cetirun-memory", None, [], directories)
            assert error is None, error
            assert found == platform / "cetirun-memory/SKILL.md", found
        _SKILLS_CACHE.clear()
        entries = _find_all_skills()
        reserved = [entry for entry in entries if entry["name"] == "cetirun-memory"]
        assert len(reserved) == 1 and "collision" not in reserved[0]["description"], reserved
        assert next(entry for entry in entries if entry["name"] == "team-skill")["description"] == description
        assert any(entry["name"] == "personal-skill" for entry in entries)
        clear_skills_system_prompt_cache(clear_snapshot=True)
        prompt = build_skills_system_prompt()
        assert "cetirun-memory" in prompt and "collision" not in prompt, prompt

    check("Version one")
    skill(shared / "cetirun-memory", "cetirun-memory", "Shared collision")
    check("Version one")
    skill(shared / "category/alias", "cetirun-memory", "Frontmatter collision")
    skill(personal / "renamed", "cetirun-memory", "Personal collision")
    (personal / "cetirun-memory.md").write_text("Legacy collision")
    skill(shared / "team-skill", "team-skill", "Version two")
    skill(shared / "added-later", "added-later", "Live publication")
    check("Version two")
    assert any(entry["name"] == "added-later" for entry in _find_all_skills())
    skill(personal / "team-skill", "team-skill", "Unrelated collision")
    error, _, found = _locate_skill("team-skill", None, [], [shared, personal, platform])
    assert found is None and "Ambiguous skill name" in error
    del os.environ["CETIRUN_MEMORY_ASSETS"]
    error, _, found = _locate_skill("cetirun-memory", None, [], [shared, personal, platform])
    assert found is None and "Ambiguous skill name" in error
    print("Pinned Hermes live skill-source checks passed")
