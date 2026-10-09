# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Regression tests for the fail-closed direct model credential cleanup."""

import json
import os
from pathlib import Path
import subprocess
import sys

import pytest


CHART = Path(__file__).resolve().parents[1]
SCRIPT = CHART / "agents/openclaw/bootstrap/retire-direct-model-auth.sh"

FAKE_OPENCLAW = r'''
import json, os, sys

state_path = os.environ["FAKE_OPENCLAW_STATE"]
state = json.loads(open(state_path).read())
argv = sys.argv[1:]
state["calls"].append(argv)
if state.get("fail") and argv[:len(state["fail"])] == state["fail"]:
    open(state_path, "w").write(json.dumps(state))
    print(json.dumps({"error": {"message": "synthetic failure"}}))
    raise SystemExit(1)
if argv[:3] == ["models", "auth", "list"]:
    print(json.dumps({"profiles": [{"id": item} for item in state["profiles"]]}))
elif argv[:3] == ["models", "auth", "logout"]:
    state["profiles"].remove(argv[3])
elif argv[:3] == ["secrets", "store", "set"]:
    state.setdefault("store", {})[argv[3]] = {"value": sys.stdin.read().rstrip("\n"), "deleted": False}
elif argv[:3] == ["secrets", "store", "rm"]:
    entry = state.setdefault("store", {}).get(argv[3])
    if entry is not None:
        entry["deleted"] = True
open(state_path, "w").write(json.dumps(state))
'''


@pytest.fixture
def home(tmp_path):
    state = tmp_path / "home/.openclaw"
    (state / "agents/main/agent").mkdir(parents=True)
    binaries = tmp_path / "bin"
    binaries.mkdir()
    fake = binaries / "openclaw"
    fake.write_text(f"#!{sys.executable}\n{FAKE_OPENCLAW}")
    fake.chmod(0o755)
    cli = tmp_path / "cli.json"
    cli.write_text(json.dumps({"profiles": ["openai:personal", "anthropic:personal"], "calls": [], "store": {}}))
    env = dict(os.environ, HOME=str(tmp_path / "home"), OPENCLAW_STATE_DIR=str(state),
               FAKE_OPENCLAW_STATE=str(cli), PATH=str(binaries) + os.pathsep + os.environ["PATH"])
    return state, cli, env


def run(home):
    return subprocess.run(["sh", str(SCRIPT)], env=home[2], capture_output=True, text=True, timeout=30)


def test_removes_every_supported_direct_model_credential_on_every_start(home):
    state, cli, _ = home
    (state / ".cetirun-direct-model-auth-retired").write_text("earlier check\n")
    (state / ".env").write_text(
        "OPENAI_API_KEY=direct-openai\n"
        "export ANTHROPIC_API_KEY=direct-anthropic\n"
        "COPILOT_GITHUB_TOKEN=direct-copilot\n"
        "BRAVE_API_KEY=keep-tool-key\n"
    )
    profiles = state / "agents/main/agent/auth-profiles.json"
    profiles.write_text(json.dumps({"profiles": {"openai:legacy": {"key": "direct"}}}))

    result = run(home)
    assert result.returncode == 0, result.stderr
    assert (state / ".env").read_text() == "BRAVE_API_KEY=keep-tool-key\n"
    assert (state / ".env").stat().st_mode & 0o777 == 0o600
    assert json.loads(profiles.read_text()) == {"profiles": {}}
    saved = json.loads(cli.read_text())
    assert saved["profiles"] == []
    assert len(saved["store"]) == 4
    assert all(entry == {"value": "retired", "deleted": True} for entry in saved["store"].values())
    assert [call[:3] for call in saved["calls"]].count(["models", "auth", "logout"]) == 2
    assert [call[:3] for call in saved["calls"]].count(["secrets", "store", "set"]) == 4
    assert [call[:3] for call in saved["calls"]].count(["secrets", "store", "rm"]) == 4


def test_overwrites_soft_deleted_provider_secrets_before_removing_them(home):
    _, cli, _ = home
    saved = json.loads(cli.read_text())
    saved["profiles"] = []
    saved["store"] = {
        "OPENAI_API_KEY": {"value": "retained-soft-deleted-token", "deleted": True},
        "UNRELATED_TOKEN": {"value": "keep", "deleted": False},
    }
    cli.write_text(json.dumps(saved))

    result = run(home)
    assert result.returncode == 0, result.stderr
    saved = json.loads(cli.read_text())
    assert saved["store"]["OPENAI_API_KEY"] == {"value": "retired", "deleted": True}
    assert saved["store"]["UNRELATED_TOKEN"] == {"value": "keep", "deleted": False}
    assert ["secrets", "store", "set", "OPENAI_API_KEY", "--kind", "secret"] in saved["calls"]
    assert ["secrets", "store", "rm", "OPENAI_API_KEY", "--yes"] in saved["calls"]


def test_auth_store_failure_keeps_the_agent_down(home):
    state, cli, _ = home
    saved = json.loads(cli.read_text())
    saved["fail"] = ["models", "auth", "list"]
    cli.write_text(json.dumps(saved))

    result = run(home)
    assert result.returncode == 1
    assert "refusing to start the Agent" in result.stderr
    assert not (state / ".cetirun-direct-model-auth-retired").exists()


def test_compaction_is_retried_after_logout_already_succeeded(home):
    state, cli, _ = home
    (state / "state").mkdir()
    (state / "state/openclaw.sqlite").write_bytes(b"SQLite with deleted credential bytes")
    saved = json.loads(cli.read_text())
    saved["fail"] = ["doctor", "--state-sqlite", "compact"]
    cli.write_text(json.dumps(saved))

    first = run(home)
    assert first.returncode == 1
    saved = json.loads(cli.read_text())
    assert saved["profiles"] == []
    saved.pop("fail")
    saved["calls"] = []
    cli.write_text(json.dumps(saved))

    second = run(home)
    assert second.returncode == 0, second.stderr
    calls = json.loads(cli.read_text())["calls"]
    assert ["doctor", "--state-sqlite", "compact"] in calls
