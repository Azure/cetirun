#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Apply already-generated Hermes files. This does not choose model settings.

With policy-controlled model access (--enforce-model-access), the generated
model choice and provider list replace saved ones instead of merging into
them, so a removed provider or model leaves Hermes' configuration. Durable
direct-provider variables and the Copilot token cache are removed on every Pod
start. With a Gateway key, its variable is reserved too: Hermes loads .env over
the process environment, so no saved assignment may replace the Pod-only key.
Without the Gateway, the provider CetiRun added for it is removed.
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
DIRECT_MODEL_ENV = (COPILOT_TOKEN_ENV, "OPENCLAW_GITHUB_TOKEN", "OPENAI_API_KEY", "ANTHROPIC_API_KEY")
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


def apply_managed_model(config: dict[str, Any], desired: dict[str, Any]) -> dict[str, Any]:
    """Replace every saved model provider with the policy-generated set."""
    providers = desired.get("providers")
    if not isinstance(providers, dict) or not isinstance(desired.get("model"), dict):
        raise ValueError("generated policy-controlled model configuration is incomplete")
    config["providers"] = copy.deepcopy(providers)
    config["model"] = copy.deepcopy(desired["model"])
    return config


def remove_gateway(config: dict[str, Any]) -> dict[str, Any]:
    """Remove the Gateway provider CetiRun added, once the Agent is off it."""
    providers = config.get("providers")
    if isinstance(providers, dict) and GATEWAY_PROVIDER in providers:
        del providers[GATEWAY_PROVIDER]
        if not providers:
            del config["providers"]
    return config


def retire_copilot_sign_in(root: Path) -> None:
    """Remove the cached Copilot token and leave audit evidence.

    The marker never skips a later check. Any failure propagates to the caller
    and keeps the Agent container from starting.
    """
    cache = check_destination(root, COPILOT_TOKEN_CACHE)
    if cache.exists():
        cache.unlink()
    replace_file(check_destination(root, COPILOT_RETIRED_MARKER), "retired\n")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--home", type=Path, required=True)
    parser.add_argument("--config", type=Path)
    parser.add_argument("--env", type=Path)
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--enforce-model-access", action="store_true")
    parser.add_argument("--gateway-key-env", help="on the AI Gateway: the variable carrying the Agent's key")
    args = parser.parse_args()
    if not args.check and (args.config is None or args.env is None):
        parser.error("--config and --env are required unless --check is used")
    if args.gateway_key_env is not None and not ENV_NAME.fullmatch(args.gateway_key_env):
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
        on_gateway = args.gateway_key_env is not None
        if on_gateway and not args.enforce_model_access:
            raise ValueError("a Gateway key requires enforced model access")
        if args.enforce_model_access:
            desired_config = apply_managed_model(desired_config, generated)
            for name in DIRECT_MODEL_ENV:
                generated_env.pop(name, None)
                existing_env.pop(name, None)
        else:
            desired_config = remove_gateway(desired_config)
        for key, value in generated_env.items():
            if value or key not in existing_env:
                existing_env[key] = value
        if args.enforce_model_access:
            # Hermes loads .env over its environment, where the chart puts the
            # Agent's key when routed. No durable policy-control variable may
            # remain, and no saved key may override the Pod-only key.
            reserved = RESERVED_ENV
            if args.gateway_key_env is not None:
                reserved = (*reserved, args.gateway_key_env)
            for name in reserved:
                existing_env.pop(name, None)
        config_text = yaml.safe_dump(desired_config, sort_keys=False)
        env_text = "".join(f"{key}={value}\n" for key, value in existing_env.items())
        replace_file(config_path, config_text)
        replace_file(env_path, env_text)
        if args.enforce_model_access:
            retire_copilot_sign_in(root)
        return 0
    except (OSError, ValueError, yaml.YAMLError):
        # Parser diagnostics can contain credential values. Do not echo them.
        print("[agent-init] HermesConfigApplyFailed: preserving unapplied files", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
