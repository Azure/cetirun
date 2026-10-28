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
Without the Gateway, the provider CetiRun added for it is removed.
"""

from __future__ import annotations

import argparse
import copy
import json
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
INGRESS_ENV = ("API_SERVER_ENABLED", "API_SERVER_HOST", "API_SERVER_PORT")
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
    providers = config.get("providers")
    if not isinstance(providers, dict):
        providers = {}
    providers[GATEWAY_PROVIDER] = copy.deepcopy(provider)
    config["providers"] = providers
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


def reserve_ingress_listeners(config: dict[str, Any], env: dict[str, str]) -> None:
    """Keep only the Pod's listener settings authoritative, not HOME overrides."""
    # The native loader merges all three YAML spellings over gateway.json.
    for path in (("platforms", "api_server"), ("gateway", "platforms", "api_server"), ("gateway", "api_server")):
        api = config
        for name in path:
            if not isinstance(api, dict):
                raise ValueError("invalid API platform configuration")
            child = api.get(name)
            if child is None:
                api = None
                break
            if not isinstance(child, dict):
                raise ValueError("invalid API platform configuration")
            # deepcopy preserves YAML aliases. Detach each managed path before
            # replacing children or removing keys shared with user preferences.
            api[name] = child.copy()
            api = api[name]
        if api is not None:
            if not isinstance(api, dict):
                raise ValueError("invalid API platform configuration")
            # Hermes promotes flat adapter settings into extra, with extra
            # taking precedence; both spellings must yield to the Pod binding.
            for name in ("enabled", "host", "port"):
                api.pop(name, None)
            extra = api.get("extra")
            if extra is not None:
                if not isinstance(extra, dict):
                    raise ValueError("invalid API adapter configuration")
                extra = extra.copy()
                api["extra"] = extra
                for name in ("host", "port", "_enabled_explicit"):
                    extra.pop(name, None)
    for name in INGRESS_ENV:
        env.pop(name, None)


def validate_ingress_environment() -> None:
    port = os.environ.get("API_SERVER_PORT", "")
    if (
        os.environ.get("CETIRUN_INGRESS_MTLS") != "true"
        or os.environ.get("API_SERVER_ENABLED") != "true"
        or os.environ.get("API_SERVER_HOST") != "127.0.0.1"
        or not re.fullmatch(r"[0-9]+", port)
        or not 1024 <= int(port) <= 65535
        or int(port) in (7681, 9119, 15440, 15443, 15444, 15445)
    ):
        raise ValueError("authoritative ingress binding is unavailable")


def without_ingress_environment(text: str) -> str:
    return "".join(
        line for line in text.splitlines(keepends=True)
        if line.strip().split("=", 1)[0] not in INGRESS_ENV
    )


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
    parser.add_argument("--gateway-key-env", help="on the AI Gateway: the variable carrying the Agent's key")
    parser.add_argument("--ingress-mtls", action="store_true", help="reserve the managed API listener bindings")
    parser.add_argument("--listeners-only", action="store_true", help="enforce mTLS listener authority before a supervised gateway start")
    args = parser.parse_args()
    if args.listeners_only and (not args.ingress_mtls or args.check or args.config is not None or args.env is not None or args.gateway_key_env is not None):
        parser.error("--listeners-only requires only --home and --ingress-mtls")
    if not args.check and not args.listeners_only and (args.config is None or args.env is None):
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
        legacy_path, legacy, existing_legacy = None, None, None
        if args.ingress_mtls:
            legacy_path = check_destination(root, Path(".hermes/gateway.json"))
            if legacy_path.exists():
                existing_legacy = json.loads(legacy_path.read_text(encoding="utf-8"))
                if not isinstance(existing_legacy, dict):
                    raise ValueError("invalid legacy gateway configuration")
                legacy = copy.deepcopy(existing_legacy)
                reserve_ingress_listeners(legacy, {})
        if args.check or args.listeners_only:
            if not config_path.is_file() or not env_path.is_file():
                raise ValueError("required configuration is missing")
            if args.ingress_mtls:
                config, env = copy.deepcopy(existing_config), dict(existing_env)
                reserve_ingress_listeners(config, env)
                if args.check and (config != existing_config or env != existing_env or legacy != existing_legacy):
                    raise ValueError("persisted ingress overrides remain")
                if args.listeners_only:
                    validate_ingress_environment()
                    if config != existing_config:
                        replace_file(config_path, yaml.safe_dump(config, sort_keys=False))
                    if env != existing_env:
                        replace_file(env_path, without_ingress_environment(env_path.read_text(encoding="utf-8")))
                    if legacy != existing_legacy:
                        replace_file(legacy_path, json.dumps(legacy, indent=2) + "\n")
            return 0
        if not args.config.is_file() or not args.env.is_file():
            raise ValueError("required input is missing")
        generated = read_yaml(args.config)
        generated_env = read_env(args.env)
        desired_config = merge(existing_config, generated)
        reconcile_memory_lists(existing_config, generated, desired_config)
        on_gateway = args.gateway_key_env is not None
        retire = False
        if on_gateway:
            desired_config = apply_gateway(desired_config, generated)
            # The owner signs in to GitHub Copilot through the Portal. The
            # generated environment, which carries a token from the instance
            # Secret, brings none into .env, so the sign-in removed once
            # cannot come back at this or a later start.
            generated_env.pop(COPILOT_TOKEN_ENV, None)
            retire = copilot_sign_in_pending(root)
            if retire:
                existing_env.pop(COPILOT_TOKEN_ENV, None)
        else:
            desired_config = remove_gateway(desired_config)
        for key, value in generated_env.items():
            if value or key not in existing_env:
                existing_env[key] = value
        if on_gateway:
            # Hermes loads .env over its environment, where the chart puts the
            # Agent's key: no assignment of the key's variable may remain,
            # neither a saved one nor one the chart generates, as it does for
            # OPENAI_API_KEY.
            for name in (args.gateway_key_env, *RESERVED_ENV):
                existing_env.pop(name, None)
        if args.ingress_mtls:
            reserve_ingress_listeners(desired_config, existing_env)
        config_text = yaml.safe_dump(desired_config, sort_keys=False)
        env_text = "".join(f"{key}={value}\n" for key, value in existing_env.items())
        replace_file(config_path, config_text)
        replace_file(env_path, env_text)
        if legacy != existing_legacy:
            replace_file(legacy_path, json.dumps(legacy, indent=2) + "\n")
        if retire:
            retire_copilot_sign_in(root)
        return 0
    except (OSError, ValueError, yaml.YAMLError):
        # Parser diagnostics can contain credential values. Do not echo them.
        print("[agent-init] HermesConfigApplyFailed: preserving unapplied files", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
