#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Download checksum-pinned Linux CI tools to an explicit, task-local directory.

This is a networked provisioning command, never invoked by the offline Make targets.
Tool versions and release provenance live in tools.json; no shared installs change.
Bundles use a "members" map of output basenames to exact archive member names.
All pins and outputs are checked before download, all payloads before writing.
"""

import argparse
from contextlib import ExitStack
import hashlib
import io
import json
import os
from pathlib import Path
import platform
import re
import sys
import tarfile
from urllib.request import urlopen


MANIFEST = Path(__file__).with_name("tools.json")


def archive_member(member):
    if (
        not isinstance(member, str)
        or "\\" in member
        or any(ord(char) < 32 for char in member)
        or any(part in ("", ".", "..") for part in member.split("/"))
    ):
        raise ValueError(f"Invalid tool archive member: {member!r}")
    return member


def binaries_bytes(data, checksum, members):
    if hashlib.sha256(data).hexdigest() != checksum:
        raise ValueError("Tool download SHA256 mismatch")
    binaries = {}
    requested = {archive_member(member) for member in members if member is not None}
    if None in members:
        binaries[None] = data
    if not requested:
        return binaries
    # Read only explicitly named regular files, never extract archive paths.
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
        entries = {}
        for entry in archive.getmembers():
            if entry.name not in requested:
                continue
            if not entry.isfile():
                raise ValueError(
                    f"Tool archive member is not a regular file: {entry.name}"
                )
            if entry.name in entries:
                raise ValueError(f"Duplicate tool archive member: {entry.name}")
            entries[entry.name] = entry
        for member in requested:
            if member not in entries:
                raise ValueError(f"Missing tool archive member: {member}")
            binaries[member] = archive.extractfile(entries[member]).read()
    return binaries


def binary_bytes(data, checksum, member=None):
    return binaries_bytes(data, checksum, [member])[member]


def check_destination(destination):
    for directory in (destination, *destination.parents):
        if directory.is_symlink():
            raise ValueError("--bin-dir must not contain symlinks")
        if directory.exists() and not directory.is_dir():
            raise ValueError("--bin-dir must be a directory")


def output_name(name):
    if not isinstance(name, str) or not re.fullmatch("[A-Za-z0-9][A-Za-z0-9._-]*", name):
        raise ValueError(f"Invalid tool output name: {name!r}")
    return name


def check_output(output):
    if output.exists() or output.is_symlink():
        raise ValueError(f"Refusing to overwrite existing tool: {output}")


def plan(manifest, names, destination, system, machine):
    if system != "Linux" or machine not in ("x86_64", "aarch64", "arm64"):
        raise ValueError("Tool provisioning supports Linux amd64/arm64 only")
    arch = "amd64" if machine == "x86_64" else "arm64"
    check_destination(destination)
    if len(names) != len(set(names)):
        raise ValueError("Request each tool only once")
    entries = []
    outputs = set()
    for name in names:
        output_name(name)
        if name not in manifest:
            raise ValueError(f"Unknown tool: {name}")
        spec = manifest[name]
        if "members" in spec:
            members = spec["members"]
            if "member" in spec or not isinstance(members, dict) or not members:
                raise ValueError(f"Invalid tool bundle members: {name}")
        else:
            members = {name: spec.get("member")}
        planned = []
        for filename, member in members.items():
            output = destination / output_name(filename)
            check_output(output)
            if output in outputs:
                raise ValueError(f"Duplicate tool output: {output}")
            outputs.add(output)
            if "members" in spec and member is None:
                raise ValueError(f"Invalid tool archive member: {member!r}")
            planned.append((output, member))
        asset_arch = spec.get("architectures", {}).get(arch, arch)
        url = spec["url"].format(arch=asset_arch)
        checksum = spec["sha256"][arch]
        if (
            not url.startswith("https://")
            or not isinstance(checksum, str)
            or not re.fullmatch("[0-9a-f]{64}", checksum)
        ):
            raise ValueError(f"Invalid download pin: {name}")
        for output, member in planned:
            if member is not None:
                member = archive_member(member).format(arch=asset_arch)
                archive_member(member)
            entries.append((output, url, checksum, member))
    return entries


def write_binaries(destination, binaries):
    check_destination(destination)
    for output in binaries:
        check_output(output)
    destination.mkdir(parents=True, exist_ok=True)
    created = []
    try:
        with ExitStack() as stack:
            targets = []
            # Reserve every output exclusively before writing the first binary.
            for output, data in binaries.items():
                target = stack.enter_context(output.open("xb"))
                created.append((output, os.fstat(target.fileno())))
                targets.append((target, data))
            for target, data in targets:
                target.write(data)
                target.flush()
                os.fchmod(target.fileno(), 0o755)
    except BaseException:
        for output, owned in created:
            try:
                current = output.lstat()
                if (current.st_dev, current.st_ino) == (owned.st_dev, owned.st_ino):
                    output.unlink()
            except FileNotFoundError:
                pass
        raise


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
        downloads = {}
        for output, url, checksum, member in entries:
            downloads.setdefault((url, checksum), []).append((output, member))
        binaries = {}
        for (url, checksum), outputs in downloads.items():
            names = ", ".join(output.name for output, _ in outputs)
            print(f"Installing {names} from {url}", flush=True)
            with urlopen(url, timeout=45) as response:
                data = binaries_bytes(
                    response.read(), checksum, [member for _, member in outputs]
                )
            for output, member in outputs:
                binaries[output] = data[member]
        write_binaries(destination, binaries)
    except (ValueError, OSError, KeyError, tarfile.TarError) as error:
        print(f"ERROR: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
