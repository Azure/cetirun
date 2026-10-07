#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Verify that relocating a uv lock file changes sources, not resolution."""

from __future__ import annotations

import argparse
from collections import Counter, defaultdict
import json
from pathlib import Path
import sys
import tomllib
from collections.abc import Iterable
from typing import Any


PackageKey = tuple[str, str, str, str]


def package_key(package: dict[str, Any]) -> PackageKey:
    source = package.get("source", {})
    if "registry" in source:
        source_kind = "registry"
        source_value = ""
    elif len(source) == 1:
        source_kind, source_value = next(iter(source.items()))
        source_value = str(source_value)
    else:
        source_kind = "unknown"
        source_value = json.dumps(source, sort_keys=True, separators=(",", ":"))
    return (
        str(package.get("name", "")),
        str(package.get("version", "")),
        source_kind,
        source_value,
    )


def artifact_hashes(package: dict[str, Any]) -> set[str]:
    hashes: set[str] = set()
    sdist = package.get("sdist")
    if isinstance(sdist, dict) and isinstance(sdist.get("hash"), str):
        hashes.add(sdist["hash"])
    for wheel in package.get("wheels", []):
        if isinstance(wheel, dict) and isinstance(wheel.get("hash"), str):
            hashes.add(wheel["hash"])
    return hashes


def load_lock(path: Path) -> list[dict[str, Any]]:
    with path.open("rb") as stream:
        document = tomllib.load(stream)
    packages = document.get("package")
    if not isinstance(packages, list):
        raise ValueError(f"{path}: missing package list")
    return packages


def validate(
    original_path: Path,
    relocated_path: Path,
    required_artifact_hashes: Iterable[str] = (),
) -> list[str]:
    original = load_lock(original_path)
    relocated = load_lock(relocated_path)
    errors: list[str] = []

    original_resolution = Counter(package_key(package) for package in original)
    relocated_resolution = Counter(package_key(package) for package in relocated)
    if original_resolution != relocated_resolution:
        errors.append("package identities changed")

    original_hashes: dict[PackageKey, set[str]] = defaultdict(set)
    relocated_hashes: dict[PackageKey, set[str]] = defaultdict(set)
    for package in original:
        original_hashes[package_key(package)].update(artifact_hashes(package))
    for package in relocated:
        relocated_hashes[package_key(package)].update(artifact_hashes(package))

    for key, current_hashes in relocated_hashes.items():
        upstream_hashes = original_hashes.get(key, set())
        unexpected = current_hashes - upstream_hashes
        if unexpected:
            errors.append(f"{key[0]} {key[1]} introduced unpinned artifact hashes")
        if upstream_hashes and not current_hashes:
            errors.append(f"{key[0]} {key[1]} lost all pinned artifacts")

    all_original_hashes = set().union(*original_hashes.values())
    for required_hash in required_artifact_hashes:
        if required_hash not in all_original_hashes:
            errors.append(f"required artifact hash {required_hash} is not upstream-pinned")

    return errors


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("original", type=Path)
    parser.add_argument("relocated", type=Path)
    parser.add_argument("--required-artifact-hash", action="append", default=[])
    args = parser.parse_args()

    try:
        errors = validate(
            args.original,
            args.relocated,
            required_artifact_hashes=args.required_artifact_hash,
        )
    except (OSError, ValueError, tomllib.TOMLDecodeError) as exc:
        print(f"uv lock relocation validation failed: {exc}", file=sys.stderr)
        return 1
    if errors:
        for error in errors:
            print(f"uv lock relocation validation failed: {error}", file=sys.stderr)
        return 1
    print("uv lock relocation preserved package identities and pinned artifacts")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
