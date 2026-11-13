# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Disable runtime Desktop plugins in the managed Hermes Webapp."""

import argparse
import hashlib
from pathlib import Path


PATCHES = {
    "apps/desktop/src/contrib/runtime-loader.ts": (
        "e50e63eb99e4fcab4961691679764223e45ba911fb5b1c6e06b3ac1bda3542a3",
        (
            (
                "const loaded = new Map<string, (() => void)[]>()\n",
                "const loaded = new Map<string, (() => void)[]>()\n"
                "\n// Disk plugins must not inherit the managed Webapp's Portal origin.\n"
                "const runtimePluginsEnabled = false\n",
            ),
            (
                "  installPluginSdk()\n\n  try {\n    const unsupported = unsupportedImports(source)",
                "  try {\n"
                "    if (!runtimePluginsEnabled) {\n"
                "      throw new Error('Runtime Desktop plugins are disabled in CetiRun')\n"
                "    }\n\n"
                "    installPluginSdk()\n"
                "    const unsupported = unsupportedImports(source)",
            ),
            ("if (!desktop || scanning) {", "if (!runtimePluginsEnabled || !desktop || scanning) {"),
            ("if (watching || !desktop) {", "if (!runtimePluginsEnabled || watching || !desktop) {"),
        ),
    ),
    "hermes_cli/web_server_surface.py": (
        "6a418664d4363375b5f473c071c37f0842abc8eaecf69ed0bc2b839ab4cf12bb",
        (("desktop_plugins=True, ui_preferences=True", "desktop_plugins=False, ui_preferences=True"),),
    ),
}


def prepare_patch(relative: str, source: str) -> str:
    digest, replacements = PATCHES[relative]
    original = source
    for before, after in reversed(replacements):
        if after in original:
            original = original.replace(after, before, 1)
    if hashlib.sha256(original.encode()).hexdigest() != digest:
        raise ValueError(f"Unsupported Hermes source: {relative}; review the Webapp plugin boundary")
    patched = original
    for before, after in replacements:
        if patched.count(before) != 1:
            raise ValueError(f"Hermes Webapp plugin integration point changed: {relative}")
        patched = patched.replace(before, after, 1)
    return patched


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--check", action="store_true", help="validate source compatibility without writing")
    args = parser.parse_args()
    try:
        prepared = {
            args.root / relative: prepare_patch(relative, (args.root / relative).read_text(encoding="utf-8"))
            for relative in PATCHES
        }
        if not args.check:
            for path, source in prepared.items():
                path.write_text(source, encoding="utf-8")
    except (OSError, ValueError) as error:
        parser.exit(1, str(error) + "\n")


if __name__ == "__main__":
    main()
