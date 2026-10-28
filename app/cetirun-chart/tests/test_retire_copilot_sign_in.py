# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Run the OpenClaw sign-in retirement script against synthetic homes, with a
stand-in for the OpenClaw CLI that records what it is asked to do."""

import json
import os
from pathlib import Path
import subprocess
import sys

import pytest


CHART = Path(__file__).resolve().parents[1]
SCRIPT = CHART / "agents/openclaw/bootstrap/retire-copilot-sign-in.sh"
NATIVE_SCRIPT = CHART / "agents/openclaw/bootstrap/retire-direct-model-auth.sh"
PROFILE = "github-copilot:github"
ENTRY = "GITHUB_COPILOT_TOKEN_DEVICE"
AGENT_CONFIG = {"models": {"providers": {"openai": {
    "auth": "api-key", "apiKey": {"source": "env", "provider": "default", "id": "OPENAI_API_KEY"}}}}}

# The OpenClaw CLI as far as the script uses it. Auth profiles and the secret
# store live in a JSON file and every call is recorded. As in OpenClaw, a
# removed store entry is only marked deleted and keeps its value, and logout
# removes the entry its profile refers to. The Agent's configuration refers
# to secrets of the agent container's environment, so the CLI fails with it
# in an init container. "refuse" makes the auth store wait for doctor's
# import of earlier profile files; "fail" makes the commands that start with
# it fail.
FAKE_OPENCLAW = r'''
import json, os, sys

path = os.environ["FAKE_OPENCLAW_STATE"]
with open(path) as handle:
    state = json.load(handle)
argv = sys.argv[1:]
state["calls"].append(argv)


def save():
    with open(path, "w") as handle:
        json.dump(state, handle)


def fail(reason, also_logged=None):
    # With --json the CLI reports the error on standard output.
    if "--json" in argv:
        print(json.dumps({"ok": False, "error": {"type": "cli_error", "message": reason}}))
    else:
        print("[openclaw] The CLI command failed.", file=sys.stderr)
        print("[openclaw] Reason: " + reason, file=sys.stderr)
    if also_logged:
        print(also_logged, file=sys.stderr)
    save()
    sys.exit(1)


agent_config = os.path.join(os.environ["OPENCLAW_STATE_DIR"], "openclaw.json")
if os.path.realpath(os.environ.get("OPENCLAW_CONFIG_PATH") or agent_config) == os.path.realpath(agent_config):
    fail("failed to resolve secrets from the active gateway snapshot; local resolution also failed")
with open(os.environ["OPENCLAW_CONFIG_PATH"]) as handle:
    state.setdefault("configs", []).append(json.load(handle))
if state.get("fail") and argv[:len(state["fail"])] == state["fail"]:
    fail("disk I/O error")
if argv[:3] == ["models", "auth", "list"]:
    if state.get("fail_readback") and sum(call[:3] == ["models", "auth", "list"] for call in state["calls"]) > 1:
        fail("synthetic readback failure", "sk-dummy")
    if state.get("refuse"):
        fail("Auth profile store requires legacy credential migration; run openclaw doctor --fix.",
             "[auth-profiles/persistence] code=AUTH_PROFILE_MIGRATION_REQUIRED")
    print(json.dumps({"agentId": "main", "profiles": [{"id": p} for p in state["profiles"]]}))
elif argv[:3] == ["models", "auth", "logout"]:
    if not state.get("keep_profiles"):
        ref = state["profiles"].pop(argv[3]).get("tokenRef")
        if ref in state["store"]:
            state["removed"][ref] = state["store"].pop(ref)
elif argv[:3] == ["secrets", "store", "list"]:
    print(json.dumps([{"name": name, "kind": "secret"} for name in sorted(state["store"])]))
elif argv[:3] == ["secrets", "store", "set"]:
    assert argv[4:] == ["--kind", "secret", "--value-file", "-"], argv
    state["store"][argv[3]] = sys.stdin.read().strip()
elif argv[:3] == ["secrets", "store", "rm"]:
    for name in argv[3:]:
        if not name.startswith("-"):
            state["removed"][name] = state["store"].pop(name)
save()
'''


@pytest.fixture
def home(tmp_path):
    state = tmp_path / "home/.openclaw"
    (state / "state").mkdir(parents=True)
    # The Agent's configuration, as the chart renders it on the Gateway.
    (state / "openclaw.json").write_text(json.dumps(AGENT_CONFIG))
    binaries = tmp_path / "bin"
    binaries.mkdir()
    fake = binaries / "openclaw"
    fake.write_text(f"#!{sys.executable}\n{FAKE_OPENCLAW}")
    fake.chmod(0o755)
    cli = tmp_path / "cli.json"
    cli.write_text(json.dumps({"profiles": {}, "store": {}, "removed": {}, "calls": []}))
    env = dict(
        os.environ, HOME=str(tmp_path / "home"), OPENCLAW_STATE_DIR=str(state),
        FAKE_OPENCLAW_STATE=str(cli), PATH=str(binaries) + os.pathsep + os.environ["PATH"],
    )
    env.pop("OPENCLAW_CONFIG_PATH", None)
    return {"state": state, "cli": cli, "env": env}


def run(home, script=SCRIPT):
    return subprocess.run(["sh", str(script)], env=home["env"], capture_output=True, text=True, timeout=30)


def cli(home):
    return json.loads(home["cli"].read_text())


def set_cli(home, **values):
    state = cli(home)
    state.update(values)
    home["cli"].write_text(json.dumps(state))


def marked(home):
    return (home["state"] / ".cetirun-copilot-sign-in-retired").is_file()


def signed_in_home(home, refuse=False):
    """What the in-agent sign-in left: the device login's profile, referring
    to its secret-store entry, in OpenClaw's databases; the token in .env; and
    auth profile files of earlier OpenClaw versions, live and archived."""
    state = home["state"]
    (state / "state/openclaw.sqlite").write_bytes(b"SQLite\x00" + PROFILE.encode() + b"\x00" + ENTRY.encode())
    (state / "agents/main/agent").mkdir(parents=True)
    (state / "agents/main/agent/openclaw-agent.sqlite").write_bytes(b"SQLite\x00")
    set_cli(home, refuse=refuse, profiles={PROFILE: {"tokenRef": ENTRY}, "openai:personal": {}},
            store={ENTRY: "ghu_test_device", "UNRELATED_SECRET": "keep-me"})
    (state / ".env").write_text(
        "COPILOT_GITHUB_TOKEN=ghu_test_helper\nexport OPENCLAW_GITHUB_TOKEN=ghu_test_older\nBRAVE_API_KEY=keep-brave\n")
    profiles = {PROFILE: {"type": "token", "token": "ghu_test_legacy"}, "openai:legacy": {"type": "api_key", "key": "keep-legacy"}}
    (state / "agents/main/agent/auth-profiles.json").write_text(json.dumps({"profiles": profiles}))
    (state / "agents/main/agent/auth-profiles.json.migrated-2026-09-30T00-00-00-000Z-abc").write_text(
        json.dumps({"profiles": {PROFILE: {"type": "token", "token": "ghu_test_archived"}}}))


def legacy(home, name):
    return json.loads((home["state"] / "agents/main/agent" / name).read_text())["profiles"]


def compactions(home):
    return [call for call in cli(home)["calls"] if call[:1] == ["doctor"]]


def test_retires_the_in_agent_sign_in_and_keeps_other_credentials(home):
    signed_in_home(home)
    result = run(home)
    assert result.returncode == 0, result.stderr
    state = cli(home)
    assert list(state["profiles"]) == ["openai:personal"]
    assert state["store"] == {"UNRELATED_SECRET": "keep-me"}
    # A removed entry keeps its value until OpenClaw purges it, and logout
    # removes the entry of its profile: the value is overwritten before.
    assert state["removed"] == {ENTRY: "retired"}
    calls = state["calls"]
    assert calls.index(["secrets", "store", "set", ENTRY, "--kind", "secret", "--value-file", "-"]) < \
        calls.index(["models", "auth", "logout", PROFILE, "--agent", "main", "--yes"])
    assert compactions(home) == [
        ["doctor", "--state-sqlite", "compact"],
        ["doctor", "--session-sqlite", "compact", "--session-sqlite-agent", "main"],
    ]
    env_file = home["state"] / ".env"
    assert env_file.read_text() == "BRAVE_API_KEY=keep-brave\n"
    assert env_file.stat().st_mode & 0o777 == 0o600
    assert legacy(home, "auth-profiles.json") == {"openai:legacy": {"type": "api_key", "key": "keep-legacy"}}
    assert legacy(home, "auth-profiles.json.migrated-2026-09-30T00-00-00-000Z-abc") == {}
    # The CLI ran with a configuration of its own; the Agent's is untouched.
    assert json.loads((home["state"] / "openclaw.json").read_text()) == AGENT_CONFIG
    assert marked(home)

    # Once per home: a later start calls nothing.
    assert run(home).returncode == 0
    assert len(cli(home)["calls"]) == len(calls)


def test_a_home_never_signed_in_is_marked_without_calling_openclaw(home):
    (home["state"] / "state/openclaw.sqlite").write_bytes(b"SQLite\x00other data")
    (home["state"] / ".env").write_text("BRAVE_API_KEY=keep-brave\n")
    result = run(home)
    assert result.returncode == 0, result.stderr
    assert cli(home)["calls"] == []
    assert (home["state"] / ".env").read_text() == "BRAVE_API_KEY=keep-brave\n"
    assert marked(home)


def test_a_failed_step_is_reported_and_a_later_start_finishes(home):
    # A failure does not keep the agent from starting; the home stays
    # unmarked, and the next start tries again.
    signed_in_home(home)
    set_cli(home, fail=["doctor", "--state-sqlite", "compact"])
    result = run(home)
    assert result.returncode == 0, result.stderr
    assert "could not compact the shared state database: disk I/O error; the next start tries again" in result.stderr
    assert (home["state"] / ".env").read_text() == "BRAVE_API_KEY=keep-brave\n"
    assert not marked(home)

    # The rows are gone already; the next start still rewrites the databases.
    set_cli(home, fail=None, calls=[])
    result = run(home)
    assert result.returncode == 0, result.stderr
    assert ["models", "auth", "logout", PROFILE, "--agent", "main", "--yes"] not in cli(home)["calls"]
    assert len(compactions(home)) == 2
    assert marked(home)


def test_an_auth_store_that_cannot_be_listed_is_tried_again(home):
    signed_in_home(home)
    set_cli(home, fail=["models", "auth", "list"])
    result = run(home)
    assert result.returncode == 0, result.stderr
    assert "could not list the auth profiles: disk I/O error" in result.stderr
    assert PROFILE in cli(home)["profiles"]
    assert not marked(home)


def test_an_entry_that_cannot_be_overwritten_keeps_its_profile(home):
    # Logging the profile out could remove the entry with its value kept:
    # neither goes until the entry has been overwritten.
    signed_in_home(home)
    set_cli(home, fail=["secrets", "store", "set"])
    result = run(home)
    assert result.returncode == 0, result.stderr
    assert f"could not overwrite the secret-store entry {ENTRY}: disk I/O error" in result.stderr
    state = cli(home)
    assert PROFILE in state["profiles"]
    assert state["store"][ENTRY] == "ghu_test_device"
    assert state["removed"] == {}
    assert not marked(home)

    set_cli(home, fail=None)
    assert run(home).returncode == 0
    assert cli(home)["removed"] == {ENTRY: "retired"}
    assert marked(home)


def test_waits_for_doctors_import_before_retiring_the_profile(home):
    # With doctor turned off, OpenClaw refuses its auth store while an
    # earlier version's profile file waits for import. The rest is retired;
    # the profile, and the marker, wait for a start after the import.
    signed_in_home(home, refuse=True)
    result = run(home)
    assert result.returncode == 0, result.stderr
    assert "requires legacy credential migration" in result.stderr
    assert PROFILE in cli(home)["profiles"]
    assert cli(home)["store"] == {"UNRELATED_SECRET": "keep-me"}
    assert (home["state"] / ".env").read_text() == "BRAVE_API_KEY=keep-brave\n"
    assert PROFILE not in legacy(home, "auth-profiles.json")
    assert not marked(home)

    set_cli(home, refuse=False)
    assert run(home).returncode == 0
    assert list(cli(home)["profiles"]) == ["openai:personal"]
    assert marked(home)


def test_profile_files_of_the_oldest_shape_are_retired(home):
    agent = home["state"] / "agents/main/agent"
    agent.mkdir(parents=True)
    (agent / "auth-profiles.json").write_text(json.dumps({PROFILE: {"type": "token", "token": "ghu_test_flat"}, "openai:flat": {"key": "keep-flat"}}))
    assert run(home).returncode == 0
    assert json.loads((agent / "auth-profiles.json").read_text()) == {"openai:flat": {"key": "keep-flat"}}


def test_an_unreadable_profile_file_is_left_and_reported(home):
    signed_in_home(home)
    broken = '{"profiles": {"' + PROFILE + '": '
    (home["state"] / "agents/main/agent/auth-profiles.json").write_text(broken)
    result = run(home)
    assert result.returncode == 0, result.stderr
    assert "could not rewrite" in result.stderr
    assert (home["state"] / "agents/main/agent/auth-profiles.json").read_text() == broken
    assert (home["state"] / ".env").read_text() == "BRAVE_API_KEY=keep-brave\n"
    assert not marked(home)


def test_native_retires_direct_profiles_on_every_start(home):
    signed_in_home(home)
    result = run(home, NATIVE_SCRIPT)
    assert result.returncode == 0, result.stderr
    assert cli(home)["profiles"] == {}
    assert legacy(home, "auth-profiles.json") == {}
    assert len(compactions(home)) == 2
    assert "ghu_test_device" not in result.stdout + result.stderr
    assert json.loads((home["state"] / "openclaw.json").read_text()) == AGENT_CONFIG

    set_cli(home, profiles={"openai:later": {}}, calls=[])
    result = run(home, NATIVE_SCRIPT)
    assert result.returncode == 0, result.stderr
    assert cli(home)["profiles"] == {}
    assert ["models", "auth", "logout", "openai:later", "--agent", "main", "--yes"] in cli(home)["calls"]


def test_native_empty_auth_store_is_checked_once_per_start(home):
    database = home["state"] / "state/openclaw.sqlite"
    database.write_bytes(b"SQLite\x00previously removed synthetic profiles")
    for _ in range(2):
        set_cli(home, calls=[])
        result = run(home, NATIVE_SCRIPT)
        assert result.returncode == 0, result.stderr
        calls = cli(home)["calls"]
        assert [call for call in calls if call[:3] == ["models", "auth", "list"]] == [
            ["models", "auth", "list", "--agent", "main", "--json"],
        ]
        assert ["doctor", "--state-sqlite", "compact"] in calls
        assert cli(home)["profiles"] == {}


def test_native_auth_commands_disable_unused_plugins_without_changing_agent_config(home):
    signed_in_home(home)
    original = {
        **AGENT_CONFIG,
        "plugins": {"enabled": True, "entries": {"example-plugin": {"enabled": True}}},
    }
    active = home["state"] / "openclaw.json"
    active.write_text(json.dumps(original))
    result = run(home, NATIVE_SCRIPT)
    assert result.returncode == 0, result.stderr
    assert cli(home)["configs"]
    assert all(config["plugins"].get("enabled") is False for config in cli(home)["configs"])
    assert json.loads(active.read_text()) == original
    assert cli(home)["profiles"] == {}
    assert len(compactions(home)) == 2


@pytest.mark.parametrize("command", [
    ["models", "auth", "list"],
    ["models", "auth", "logout"],
    ["doctor", "--state-sqlite", "compact"],
    ["doctor", "--session-sqlite", "compact"],
])
def test_native_auth_failures_block_start_and_retry(home, command):
    signed_in_home(home)
    set_cli(home, fail=command)
    result = run(home, NATIVE_SCRIPT)
    assert result.returncode != 0
    assert "refusing to start the Agent" in result.stderr
    assert "ghu_test_device" not in result.stdout + result.stderr
    set_cli(home, fail=None)
    result = run(home, NATIVE_SCRIPT)
    assert result.returncode == 0, result.stderr


def test_native_auth_checks_successful_logout_actually_removed_the_profiles(home):
    signed_in_home(home)
    set_cli(home, keep_profiles=True)
    result = run(home, NATIVE_SCRIPT)
    assert result.returncode != 0
    assert "model auth profiles remain" in result.stderr


def test_native_logout_readback_failure_still_blocks_start(home):
    signed_in_home(home)
    set_cli(home, fail_readback=True)
    result = run(home, NATIVE_SCRIPT)
    assert result.returncode != 0
    assert cli(home)["profiles"] == {}
    assert "could not list model auth profiles" in result.stderr
    assert "sk-dummy" not in result.stdout + result.stderr


@pytest.mark.parametrize("contents", ["not-json", "[]", '{"profiles": []}'])
def test_native_auth_rejects_malformed_legacy_profiles(home, contents):
    directory = home["state"] / "agents/main/agent"
    directory.mkdir(parents=True)
    profiles = directory / "auth-profiles.json"
    profiles.write_text(contents)
    result = run(home, NATIVE_SCRIPT)
    assert result.returncode != 0
    assert "could not clear legacy auth profiles" in result.stderr
    assert profiles.read_text() == contents
    assert list(directory.glob("*.tmp.*")) == []


def test_native_auth_rejects_links_before_calling_openclaw(home, tmp_path):
    outside = tmp_path / "outside.json"
    outside.write_text('{"profiles": {"openai:personal": {"key": "sk-dummy"}}}')
    directory = home["state"] / "agents/main/agent"
    directory.mkdir(parents=True)
    (directory / "auth-profiles.json").symlink_to(outside)
    result = run(home, NATIVE_SCRIPT)
    assert result.returncode != 0
    assert "unsupported auth store link" in result.stderr
    assert cli(home)["calls"] == []
    assert "sk-dummy" in outside.read_text()


def test_native_auth_refuses_unsupported_named_agent_stores(home):
    (home["state"] / "agents/example-extra/agent").mkdir(parents=True)
    result = run(home, NATIVE_SCRIPT)
    assert result.returncode != 0
    assert "additional Agent model stores are unsupported" in result.stderr
    assert cli(home)["calls"] == []
