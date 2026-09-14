#!/usr/bin/env python3
"""GitHub Copilot auth helper for CetiRun Hermes instances."""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

import yaml


def _current_model() -> str:
    from hermes_cli.config import load_config

    config = load_config()
    model = config.get("model")
    if isinstance(model, dict):
        return str(model.get("default") or "")
    return str(model or "")


def _run_login_and_model_selection() -> int:
    from hermes_cli.config import load_config, save_env_value
    from hermes_cli.copilot_auth import copilot_device_code_login
    from hermes_cli.main import _model_flow_copilot

    current_model = _current_model()

    print("Starting GitHub Copilot login...")
    token = copilot_device_code_login()
    if not token:
        print("GitHub Copilot login cancelled or failed.", file=sys.stderr)
        return 1

    save_env_value("COPILOT_GITHUB_TOKEN", token)
    print("GitHub Copilot token saved.")
    print()

    _model_flow_copilot(load_config(), current_model)
    return 0


def _print_status() -> int:
    from hermes_cli.config import get_config_path

    config_path = Path(get_config_path())
    provider = "copilot"
    model_name = ""

    try:
        with config_path.open("r", encoding="utf-8") as f:
            config = yaml.safe_load(f) or {}
        model = config.get("model")
        if isinstance(model, dict):
            provider = str(model.get("provider") or provider)
            model_name = str(model.get("default") or "")
        elif model:
            model_name = str(model)
    except FileNotFoundError:
        pass

    print(f"Configured provider: {provider}")
    if model_name:
        print(f"Model: {model_name}")
    print()

    from hermes_cli.auth_commands import auth_status_command

    args = argparse.Namespace(provider="copilot")
    auth_status_command(args)
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Authenticate Hermes with GitHub Copilot and select a model."
    )
    subparsers = parser.add_subparsers(dest="command")
    subparsers.add_parser("login", help="Run GitHub Copilot login and model setup")
    subparsers.add_parser("status", help="Show GitHub Copilot auth status")

    args = parser.parse_args(argv)
    if args.command == "status":
        return _print_status()
    return _run_login_and_model_selection()


if __name__ == "__main__":
    raise SystemExit(main())
