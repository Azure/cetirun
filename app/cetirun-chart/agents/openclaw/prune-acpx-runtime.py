#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Omit build/debug metadata from the pinned managed ACPX runtime project."""

import json
import hashlib
from pathlib import Path
import re
import sys


# Qualified with OpenClaw/ACPX 2026.9.6 on linux/amd64, after the image's
# existing optional-platform pruning. Dependency drift requires a new review
# of runtime file references and a full doctor/Gateway/ACP qualification.
REVIEWED_LOCK_SHA256 = "630412acc3827bab1bf79f46944b247ce8e758221ae800e20db9948f9d5733c9"
REVIEWED_METADATA_PATHS_SHA256 = "e830e9c275eaadc55a6c3ae149557a73173eddbfcc53f0154903cbf7cbceffb0"


def direct_manifest(project):
    current = project
    for part in ("node_modules", "@openclaw", "acpx", "package.json"):
        current = current / part
        if current.is_symlink():
            raise ValueError("Managed ACPX identity path must not contain symlinks")
    return current


def is_metadata(name):
    return name.endswith(".map") or re.search(r"\.d\.[cm]?ts$", name) is not None


def runtime_targets(value):
    if isinstance(value, str):
        yield value
    elif isinstance(value, list):
        for item in value:
            yield from runtime_targets(item)
    elif isinstance(value, dict):
        for key, item in value.items():
            if key not in ("types", "typings"):
                yield from runtime_targets(item)


def prune(projects, version):
    projects = Path(projects)
    candidates = [p for p in projects.iterdir()
                  if p.is_dir() and not p.is_symlink()
                  and direct_manifest(p).is_file()]
    if len(candidates) != 1:
        raise ValueError("Expected exactly one managed ACPX project")
    project = candidates[0]
    manifest = json.loads(direct_manifest(project).read_text())
    if manifest.get("name") != "@openclaw/acpx" or manifest.get("version") != version:
        raise ValueError("Managed ACPX does not match the pinned OpenClaw version")

    # Do not follow npm executable links or any other symlink. Validate all
    # package runtime entry points before removing anything from the project.
    files = []
    pending = [project]
    while pending:
        directory = pending.pop()
        for entry in directory.iterdir():
            if entry.is_symlink():
                continue
            if entry.is_dir():
                pending.append(entry)
            elif entry.is_file():
                files.append(entry)
                if entry.name == "package.json":
                    package = json.loads(entry.read_text())
                    for field in ("main", "module", "bin", "browser", "exports", "imports"):
                        if any(is_metadata(target) for target in runtime_targets(package.get(field))):
                            raise ValueError("A package runtime entry point needs build/debug metadata")
    removed = [entry for entry in files if is_metadata(entry.name)]
    lock = project / "package-lock.json"
    if lock.is_symlink() or hashlib.sha256(lock.read_bytes()).hexdigest() != REVIEWED_LOCK_SHA256:
        raise ValueError("Unreviewed ACPX dependency lock; qualify the new runtime project")
    if removed:
        paths = "\n".join(sorted(entry.relative_to(project).as_posix() for entry in removed)) + "\n"
        if hashlib.sha256(paths.encode()).hexdigest() != REVIEWED_METADATA_PATHS_SHA256:
            raise ValueError("Unreviewed ACPX metadata inventory; qualify the new runtime project")
    for entry in removed:
        entry.unlink()
    return len(removed)


if __name__ == "__main__":
    if len(sys.argv) != 3:
        raise SystemExit("Usage: prune-acpx-runtime.py PROJECTS_DIR PINNED_OPENCLAW_VERSION")
    print(f"Omitted {prune(sys.argv[1], sys.argv[2])} ACPX declaration/source-map files")
