#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Apply already-generated Hermes files. This does not choose model settings.

On the platform AI Gateway (--gateway-key-env), the generated model choice and
the CetiRun AI Gateway provider replace the saved ones instead of merging into
them, so a model the Gateway no longer offers leaves Hermes' list. The variable
that carries the Agent's key is reserved: Hermes loads its .env over the
process environment, so no assignment of it stays in .env, neither a saved one
nor one in the generated file. Once per HOME,
the GitHub Copilot sign-in made inside the agent before the Gateway is
removed: its token in .env and Hermes' cache of the token it exchanged. A
token in the generated file, from the instance Secret, is not merged into .env
on the Gateway, so the sign-in does not come back.
The Gateway is required; direct provider selection is not supported.
"""

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

# The provider the chart renders for the platform AI Gateway.
GATEWAY_PROVIDER = "cetirun-gateway"
# Written once the in-agent GitHub Copilot sign-in is removed from a HOME.
COPILOT_RETIRED_MARKER = Path(".hermes/.cetirun-copilot-sign-in-retired")
# Where the in-agent sign-in kept its GitHub token, and Hermes the Copilot
# token it exchanged for it.
COPILOT_TOKEN_ENV = "COPILOT_GITHUB_TOKEN"
COPILOT_TOKEN_CACHE = Path(".hermes/.copilot_jwt.json")
RESERVED_ENV = ("CETIRUN_AI_GATEWAY",)
ENV_NAME = re.compile(r"[A-Z][A-Z0-9_]{0,63}")


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


def reconcile_memory_lists(existing: dict[str, Any], generated: dict[str, Any], merged: dict[str, Any]) -> None:
    for section, key, owned in (
        ("plugins", "enabled", "cetirun-memory-context"),
        ("skills", "external_dirs", "/home/vibe/.agents/skills"),
    ):
        saved, desired = existing.get(section) or {}, generated.get(section) or {}
        if not isinstance(saved, dict) or not isinstance(desired, dict):
            raise ValueError("invalid plugin or skills configuration")
        if key not in saved and key not in desired:
            continue
        before, after = saved.get(key) or [], desired.get(key) or []
        if not isinstance(before, list) or not isinstance(after, list) or not all(isinstance(item, str) for item in before + after):
            raise ValueError("plugin and skills paths must be lists of strings")
        values = [item for item in before if item != owned or item in after]
        if not isinstance(merged.get(section), dict):
            merged[section] = {}
        merged[section][key] = list(dict.fromkeys(values + after))


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


def apply_gateway(config: dict[str, Any], desired: dict[str, Any]) -> dict[str, Any]:
    """Replace the model choice and the Gateway provider with the generated ones."""
    provider = (desired.get("providers") or {}).get(GATEWAY_PROVIDER)
    if not isinstance(provider, dict) or not isinstance(desired.get("model"), dict):
        raise ValueError("generated Gateway configuration is incomplete")
    config["providers"] = {GATEWAY_PROVIDER: copy.deepcopy(provider)}
    config["model"] = copy.deepcopy(desired["model"])
    return config


def copilot_sign_in_pending(root: Path) -> bool:
    """Whether this HOME's in-agent GitHub Copilot sign-in is still to be removed."""
    try:
        return not check_destination(root, COPILOT_RETIRED_MARKER).exists()
    except ValueError:
        print("[agent-init] HermesCopilotSignInNotRetired: the marker is not a regular file", file=sys.stderr)
        return False


def retire_copilot_sign_in(root: Path) -> None:
    """Remove the cached Copilot token and mark the HOME. The GitHub token left
    .env with the rewrite. A failure leaves the HOME unmarked, for the next
    start to try again, without keeping the Agent from starting."""
    try:
        cache = check_destination(root, COPILOT_TOKEN_CACHE)
        if cache.exists():
            cache.unlink()
        replace_file(check_destination(root, COPILOT_RETIRED_MARKER), "retired\n")
    except (OSError, ValueError):
        print("[agent-init] HermesCopilotSignInNotRetired: tried again at the next start", file=sys.stderr)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--home", type=Path, required=True)
    parser.add_argument("--config", type=Path)
    parser.add_argument("--env", type=Path)
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--gateway-key-env", default="CETIRUN_GATEWAY_API_KEY", help="the variable carrying the Agent's Gateway key")
    args = parser.parse_args()
    if not args.check and (args.config is None or args.env is None):
        parser.error("--config and --env are required unless --check is used")
    if not ENV_NAME.fullmatch(args.gateway_key_env):
        parser.error("--gateway-key-env must name an environment variable")
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
        generated = read_yaml(args.config)
        generated_env = read_env(args.env)
        desired_config = merge(existing_config, generated)
        reconcile_memory_lists(existing_config, generated, desired_config)
        desired_config = apply_gateway(desired_config, generated)
        # The owner signs in to GitHub Copilot through the Portal. The
        # generated environment, which carries a token from the instance
        # Secret, brings none into .env, so the sign-in removed once
        # cannot come back at this or a later start.
        generated_env.pop(COPILOT_TOKEN_ENV, None)
        retire = copilot_sign_in_pending(root)
        if retire:
            existing_env.pop(COPILOT_TOKEN_ENV, None)
        for key, value in generated_env.items():
            if value or key not in existing_env:
                existing_env[key] = value
        # Hermes loads .env over its environment, where the chart puts the
        # Agent's key: no assignment of the key's variable may remain,
        # neither a saved one nor one the chart generates, as it does for
        # OPENAI_API_KEY.
        for name in (args.gateway_key_env, *RESERVED_ENV):
            existing_env.pop(name, None)
        config_text = yaml.safe_dump(desired_config, sort_keys=False)
        env_text = "".join(f"{key}={value}\n" for key, value in existing_env.items())
        replace_file(config_path, config_text)
        replace_file(env_path, env_text)
        if retire:
            retire_copilot_sign_in(root)
        return 0
    except (OSError, ValueError, yaml.YAMLError):
        # Parser diagnostics can contain credential values. Do not echo them.
        print("[agent-init] HermesConfigApplyFailed: preserving unapplied files", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
