#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Pin the reserved Memory skill to its installed assets in native skill loaders."""

import argparse
import hashlib
from pathlib import Path


HERMES_FILTER = '''def cetirun_memory_skill_allowed(skill_file: Path) -> bool:
    assets = os.environ.get("CETIRUN_MEMORY_ASSETS")
    if not assets:
        return True
    reserved = Path(assets) / "skills/cetirun-memory/SKILL.md"
    if skill_file.resolve() == reserved.resolve():
        return True
    if skill_file.parent.name == "cetirun-memory" or skill_file.stem == "cetirun-memory":
        return False
    try:
        frontmatter, _ = parse_frontmatter(skill_file.read_text(encoding="utf-8"))
    except (OSError, UnicodeError):
        return True  # Leave unrelated unreadable skills to the native loader.
    return frontmatter.get("name") != "cetirun-memory"


'''

PATCHES = {
    "hermes": (
        ("agent/skill_utils.py", "be192879ed2df6ef165c26e51293dddab131f24dc52bf85ce98714fea704c6ce", (
            ("def iter_skill_index_files(skills_dir: Path, filename: str):\n",
             HERMES_FILTER + "def iter_skill_index_files(skills_dir: Path, filename: str):\n"),
            ("    yield from map(Path, sorted(matches))\n",
             "    yield from (p for p in map(Path, sorted(matches))\n"
             '                if filename != "SKILL.md" or cetirun_memory_skill_allowed(p))\n'),
        )),
        ("tools/skills_tool.py", "0d47bda41ed9d1ec84856df3d336a42bb94161e89bd9a8b857e76ec76be9f7b7", (
            ("    def _record(sd: Optional[Path], smd: Path) -> None:\n",
             "    def _record(sd: Optional[Path], smd: Path) -> None:\n"
             "        from agent.skill_utils import cetirun_memory_skill_allowed\n"
             "        if not cetirun_memory_skill_allowed(smd):\n"
             "            return\n"),
        )),
    ),
    "openclaw": (
        ("dist/workspace-skill-loader-CoQ1pd8t.mjs", "7db6941683a467cd406ada13c8a211c0dcbd85f646c72887801e58e9e66cffb9", (
            ("\tfor (const record of records) {\n",
             "\tfor (const record of records) {\n"
             "\t\tconst assets = process.env.CETIRUN_MEMORY_ASSETS;\n"
             '\t\tif (assets && record.skill.name === "cetirun-memory" &&\n'
             '\t\t\tcanonicalizePath(record.skill.filePath) !== canonicalizePath(path.join(assets, "skills/cetirun-memory/SKILL.md"))) continue;\n'),
        )),
    ),
}


def prepare_patches(root, runtime):
    updates = []
    for relative, expected, replacements in PATCHES[runtime]:
        path = (root / relative).resolve()
        if not path.is_relative_to(root):
            raise ValueError(f"Source path escapes native checkout: {relative}")
        source = path.read_text(encoding="utf-8")
        original = source
        for before, after in reversed(replacements):
            original = original.replace(after, before, 1)
        if hashlib.sha256(original.encode()).hexdigest() != expected:
            raise ValueError(f"Unsupported {runtime} skill source: {relative}; review the pinned integration")
        updated = original
        for before, after in replacements:
            if updated.count(before) != 1:
                raise ValueError(f"Expected one skill integration point: {relative}")
            updated = updated.replace(before, after, 1)
        if updated != source:
            updates.append((path, updated))
    return updates


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, required=True, help="Pinned native source directory")
    parser.add_argument("--runtime", choices=PATCHES, required=True)
    parser.add_argument("--check", action="store_true", help="Validate without modifying the source")
    args = parser.parse_args()
    try:
        updates = prepare_patches(args.root.resolve(), args.runtime)
        if not args.check:
            for path, content in updates:
                path.write_text(content, encoding="utf-8")
    except (OSError, ValueError) as error:
        parser.exit(1, f"{error}\n")
    print(f"Memory skill source: {len(updates)} file(s) {'validated' if args.check else 'updated'}")


if __name__ == "__main__":
    main()
