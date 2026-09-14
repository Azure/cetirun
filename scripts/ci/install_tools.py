#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Download checksum-pinned Linux CI tools to an explicit, task-local directory.

This is a networked provisioning command, never invoked by the offline Make targets.
Tool versions and release provenance live in tools.json; no shared installs change.
"""

import argparse
import hashlib
import io
import json
from pathlib import Path
import platform
import re
import sys
import tarfile
from urllib.request import urlopen


MANIFEST = Path(__file__).with_name("tools.json")


def binary_bytes(data, checksum, member=None):
    if hashlib.sha256(data).hexdigest() != checksum:
        raise ValueError("Tool download SHA256 mismatch")
    if member is None:
        return data
    # Read just the named regular file. Never extract paths/symlinks from a tarball.
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
        entry = archive.getmember(member)
        if not entry.isfile():
            raise ValueError(f"Tool archive member is not a regular file: {member}")
        return archive.extractfile(entry).read()


def plan(manifest, names, destination, system, machine):
    if system != "Linux" or machine not in ("x86_64", "aarch64", "arm64"):
        raise ValueError("Tool provisioning supports Linux amd64/arm64 only")
    arch = "amd64" if machine == "x86_64" else "arm64"
    if destination.exists() and not destination.is_dir():
        raise ValueError("--bin-dir must be a directory")
    if len(names) != len(set(names)):
        raise ValueError("Request each tool only once")
    entries = []
    for name in names:
        if name not in manifest:
            raise ValueError(f"Unknown tool: {name}")
        output = destination / name
        if output.exists() or output.is_symlink():
            raise ValueError(f"Refusing to overwrite existing tool: {output}")
        spec = manifest[name]
        asset_arch = spec.get("architectures", {}).get(arch, arch)
        url = spec["url"].format(arch=asset_arch)
        checksum = spec["sha256"][arch]
        if not url.startswith("https://") or not re.fullmatch("[0-9a-f]{64}", checksum):
            raise ValueError(f"Invalid download pin: {name}")
        member = spec.get("member")
        entries.append(
            (output, url, checksum, member.format(arch=asset_arch) if member else None)
        )
    return entries


def main(argv=None):
    manifest = json.loads(MANIFEST.read_text())
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--bin-dir",
        required=True,
        type=Path,
        help="new/empty task-local binary directory (never a global bin directory)",
    )
    parser.add_argument("tools", nargs="+", choices=manifest)
    args = parser.parse_args(argv)
    try:
        destination = args.bin_dir.absolute()
        entries = plan(
            manifest, args.tools, destination, platform.system(), platform.machine()
        )
        destination.mkdir(parents=True, exist_ok=True)
        for output, url, checksum, member in entries:
            print(f"Installing {output.name} from {url}", flush=True)
            with urlopen(url, timeout=45) as response:
                data = binary_bytes(response.read(), checksum, member)
            with output.open("xb") as target:
                target.write(data)
            output.chmod(0o755)
    except (ValueError, OSError, KeyError, tarfile.TarError) as error:
        print(f"ERROR: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
