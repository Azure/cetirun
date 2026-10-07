#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Apply already-generated Hermes files. This does not choose model settings."""

from __future__ import annotations

import argparse
import copy
import os
import re
import sys
import tempfile
from pathlib import Path
from typing import Any

import yaml


def read_yaml(path: Path) -> dict[str, Any]:
    if not path.exists():
        return {}
    value = yaml.safe_load(path.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise ValueError("configuration must be an object")
    return value


def read_env(path: Path) -> dict[str, str]:
    values = {}
    if not path.exists():
        return values
    for line in path.read_text(encoding="utf-8").splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        match = re.fullmatch(r"([A-Za-z_][A-Za-z0-9_]*)=(.*)", line)
        if not match:
            raise ValueError("invalid environment assignment")
        name, value = match.groups()
        values[name] = value
    return values


def merge(base: Any, override: Any) -> Any:
    if not isinstance(base, dict) or not isinstance(override, dict):
        return copy.deepcopy(override)
    result = copy.deepcopy(base)
    for key, value in override.items():
        result[key] = merge(result.get(key), value)
    return result


def check_destination(root: Path, relative: Path) -> Path:
    target = root
    for part in relative.parts:
        target /= part
        if target.is_symlink():
            raise ValueError("destination contains a symlink")
    if target.exists() and not target.is_file():
        raise ValueError("destination is not a regular file")
    return target


def replace_file(path: Path, content: str) -> None:
    fd, name = tempfile.mkstemp(prefix=path.name + ".tmp.", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as stream:
            stream.write(content)
            stream.flush()
            os.fsync(stream.fileno())
        os.chmod(name, 0o600)
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--home", type=Path, required=True)
    parser.add_argument("--config", type=Path)
    parser.add_argument("--env", type=Path)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    if not args.check and (args.config is None or args.env is None):
        parser.error("--config and --env are required unless --check is used")
    try:
        root = args.home.absolute()
        if root == Path("/") or root.is_symlink() or not root.is_dir():
            raise ValueError("invalid HOME")
        config_path = check_destination(root, Path(".hermes/config.yaml"))
        env_path = check_destination(root, Path(".hermes/.env"))
        existing_config = read_yaml(config_path)
        existing_env = read_env(env_path)
        if args.check:
            if not config_path.is_file() or not env_path.is_file():
                raise ValueError("required configuration is missing")
            return 0
        if not args.config.is_file() or not args.env.is_file():
            raise ValueError("required input is missing")
        desired_input = read_yaml(args.config)
        desired_env = read_env(args.env)
        if os.environ.get("CETIRUN_AI_GATEWAY") == "true":
            # A Hermes home is durable. Merging a legacy home would retain an
            # old provider key, auth profile, or custom endpoint that can
            # bypass the Gateway. The mounted chart configuration is the
            # complete, platform-owned runtime state in Gateway mode. The
            # workload injects its virtual key and endpoint separately.
            desired_config = desired_input
            existing_env = desired_env
        else:
            desired_config = merge(existing_config, desired_input)
            for key, value in desired_env.items():
                if value or key not in existing_env:
                    existing_env[key] = value
        config_text = yaml.safe_dump(desired_config, sort_keys=False)
        env_text = "".join(f"{key}={value}\n" for key, value in existing_env.items())
        replace_file(config_path, config_text)
        replace_file(env_path, env_text)
        return 0
    except (OSError, ValueError, yaml.YAMLError):
        # Parser diagnostics can contain credential values. Do not echo them.
        print("[agent-init] HermesConfigApplyFailed: preserving unapplied files", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
