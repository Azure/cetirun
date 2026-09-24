# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Run the actual init scripts against synthetic HOME/PVC directories only."""

import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys

import pytest
import yaml


CHART = Path(__file__).resolve().parents[1]
IMAGE = "registry.example.com/agent@sha256:" + "a" * 64


@pytest.fixture(params=["openclaw", "hermes"])
def agent(request, tmp_path):
    kind = request.param
    source, target, config, secrets, binaries = [tmp_path / name for name in ("source", "pvc", "config", "secrets", "bin")]
    for directory in (source, target, config, secrets, binaries):
        directory.mkdir()
    (source / ".zshrc").write_text("image shell")
    (source / "dependency").write_text("image dependency")
    (source / "dependency").chmod(0o755)
    shutil.copy(CHART / "configs/init-home-common.sh", config)
    (config / f"{kind}-auth.sh").write_text("#!/bin/sh\nexit 0\n")
    command_log = tmp_path / "commands"
    for command in ("git", "curl", "az", "hermes"):
        stub = binaries / command
        stub.write_text(f'#!/bin/sh\nprintf "%s\\n" "{command}" >> "$COMMAND_LOG"\nexit 1\n')
        stub.chmod(0o755)
    if kind == "openclaw":
        (source / ".openclaw/workspace").mkdir(parents=True)
        (source / ".openclaw/workspace/USER.md").write_text("image user")
        (source / ".openclaw/npm").symlink_to("/usr/local/share/openclaw-npm")
        (config / "openclaw.json").write_text(json.dumps({
            "agents": {"defaults": {"model": {"primary": "test-model"}}},
            "channels": {"msteams": {"enabled": True}},
        }))
        (config / "user-md").write_text("Example User")
        (config / "acpx-config.json").write_text('{"agents": {}}')
        script = CHART / "agents/openclaw/bootstrap/init-home.sh"
        config_file = target / ".openclaw/openclaw.json"
    else:
        (config / "hermes-config.yaml").write_text("model:\n  default: test-model\n  provider: openai\n")
        (config / "hermes-soul.md").write_text("Example persona")
        (config / "hermes-user.md").write_text("Example User\n")
        (config / "hermes-team.md").write_text("Example Team")
        (secrets / "hermes.env").write_text("API_SERVER_KEY=sk-dummy\nOPENAI_API_KEY=sk-dummy-model\n")
        shutil.copy(CHART / "agents/hermes/bootstrap/hermes-apply-config.py", config)
        script = CHART / "agents/hermes/bootstrap/hermes-init-home.sh"
        config_file = target / ".hermes/config.yaml"
    return {
        "kind": kind, "source": source, "target": target, "config": config,
        "secrets": secrets, "script": script, "config_file": config_file,
        "command_log": command_log, "binaries": binaries,
        "env": dict(
            os.environ,
            SOURCE_HOME=str(source), TARGET_HOME=str(target),
            CETIRUN_CONFIG_DIR=str(config), CETIRUN_SECRET_DIR=str(secrets),
            HERMES_PYTHON=sys.executable, CETIRUN_INIT_IMAGE=IMAGE,
            AZDEVOPS_GIT_CREDENTIAL_ENABLED="false",
            COMMAND_LOG=str(command_log), PATH=str(binaries) + os.pathsep + os.environ["PATH"],
        ),
    }


def run(agent):
    return subprocess.run(
        ["sh", str(agent["script"])], env=agent["env"],
        capture_output=True, text=True, timeout=15,
    )


def read_config(agent):
    return yaml.safe_load(agent["config_file"].read_text())


def native_marker(agent):
    return agent["target"] / f'.{agent["kind"]}/.initialized'


def test_native_first_start_and_repeat_preserve_user_files(agent):
    first = run(agent)
    assert first.returncode == 0, first.stderr
    assert native_marker(agent).is_file()
    assert (agent["target"] / ".cetirun-home-seeded").is_file()
    helper = agent["target"] / ".local/bin/copilot-auth"
    assert helper.stat().st_mode & 0o777 == 0o755
    if agent["kind"] == "openclaw":
        user = agent["target"] / ".openclaw/workspace/USER.md"
        assert read_config(agent)["channels"]["msteams"]["allowFrom"] == ["00000000-0000-0000-0000-000000000000"]
        assert (agent["target"] / ".openclaw/npm").is_symlink()
    else:
        user = agent["target"] / ".hermes/memories/USER.md"
        assert "TEAM.md" in user.read_text()
    user.write_text("user edits")
    (agent["target"] / ".zshrc").unlink()
    (agent["source"] / "dependency").write_text("new dependency")
    current = read_config(agent)
    current["user_preference"] = "preserved"
    agent["config_file"].write_text(json.dumps(current))
    second = run(agent)
    assert second.returncode == 0, second.stderr
    assert user.read_text() == "user edits"
    assert not (agent["target"] / ".zshrc").exists()
    assert (agent["target"] / "dependency").read_text() == "image dependency"
    assert read_config(agent)["user_preference"] == "preserved"
    stages = ["seed_home_once", "agent_prepare_home", "agent_apply_config", "agent_prepare_integrations", "agent_verify", "complete"]
    offsets = [second.stderr.index("[agent-init] " + stage) for stage in stages]
    assert offsets == sorted(offsets)


def test_corrupt_config_is_preserved_and_not_logged(agent):
    assert run(agent).returncode == 0
    corrupt = 'not-json-or-yaml: [secret-test-sentinel'
    agent["config_file"].write_text(corrupt)
    result = run(agent)
    assert result.returncode != 0
    assert agent["config_file"].read_text() == corrupt
    assert "secret-test-sentinel" not in result.stderr + result.stdout
    assert "[agent-init] complete" not in result.stderr


def test_native_failure_after_seed_resumes_without_recopy(agent):
    source = agent["config"] / ("openclaw.json" if agent["kind"] == "openclaw" else "hermes-config.yaml")
    saved = source.read_text()
    source.write_text("invalid: [")
    result = run(agent)
    assert result.returncode != 0
    assert (agent["target"] / ".cetirun-home-seeded").is_file()
    assert not native_marker(agent).exists()
    (agent["source"] / "new-dependency").write_text("must not recopy")
    source.write_text(saved)
    result = run(agent)
    assert result.returncode == 0, result.stderr
    assert native_marker(agent).is_file()
    assert not (agent["target"] / "new-dependency").exists()


def test_native_config_link_is_rejected(agent, tmp_path):
    assert run(agent).returncode == 0
    outside = tmp_path / "outside"
    outside.write_text("keep")
    agent["config_file"].unlink()
    agent["config_file"].symlink_to(outside)
    result = run(agent)
    assert result.returncode != 0
    assert outside.read_text() == "keep"


def test_existing_home_is_not_scanned_for_legacy_credentials(agent):
    directory = agent["target"] / f'.{agent["kind"]}'
    directory.mkdir()
    native_marker(agent).touch()
    token = agent["target"] / ".litellm/github_copilot/access-token"
    token.parent.mkdir(parents=True)
    token.write_text("legacy-test-sentinel")
    other = agent["target"] / ".openclaw/agents/main/agent/auth-profiles.json"
    other.parent.mkdir(parents=True, exist_ok=True)
    other.write_text('{"profiles":{"github-copilot:github":{"token":"legacy-test-sentinel"}}}')
    result = run(agent)
    assert result.returncode == 0, result.stderr
    assert token.read_text() == "legacy-test-sentinel"
    assert not agent["command_log"].exists()
    if agent["kind"] == "hermes":
        assert "legacy-test-sentinel" not in (directory / ".env").read_text()


@pytest.mark.parametrize("agent", ["hermes"], indirect=True)
def test_hermes_preserves_env_and_updates_team(agent):
    assert run(agent).returncode == 0
    env = agent["target"] / ".hermes/.env"
    env.write_text("API_SERVER_KEY=old-key\nOPENAI_API_KEY=retained-key\nCUSTOM_FIELD=keep\n")
    (agent["secrets"] / "hermes.env").write_text("API_SERVER_KEY=rotated-key\nOPENAI_API_KEY=\n")
    (agent["config"] / "hermes-team.md").write_text("")
    result = run(agent)
    assert result.returncode == 0, result.stderr
    assert "API_SERVER_KEY=rotated-key" in env.read_text()
    assert "OPENAI_API_KEY=retained-key" in env.read_text()
    assert "CUSTOM_FIELD=keep" in env.read_text()
    assert not (agent["target"] / ".hermes/memories/TEAM.md").exists()


@pytest.mark.parametrize("agent", ["openclaw"], indirect=True)
def test_openclaw_optional_git_failure_and_managed_skill(agent):
    assert run(agent).returncode == 0
    skill = agent["source"] / ".openclaw/skills/example"
    skill.mkdir(parents=True)
    (skill / "SKILL.md").write_text("Example task")
    agent["env"]["AZDEVOPS_GIT_CREDENTIAL_ENABLED"] = "true"
    result = run(agent)
    assert result.returncode == 0, result.stderr
    assert "WARNING" in result.stderr
    assert agent["command_log"].read_text().splitlines() == ["git", "git", "git"]
    assert (agent["target"] / ".openclaw/skills/example/SKILL.md").read_text() == "Example task"
    assert native_marker(agent).is_file()


@pytest.mark.parametrize("agent", ["openclaw"], indirect=True)
def test_duplicate_git_setting_does_not_skip_other_settings(agent):
    assert run(agent).returncode == 0
    (agent["binaries"] / "git").unlink()
    real_git = shutil.which("git")
    config = agent["target"] / ".gitconfig"
    prefix = "credential.https://dev.azure.com"
    for value in ("true", "false"):
        subprocess.run(
            [real_git, "config", "--file", str(config), "--add", prefix + ".useHttpPath", value],
            check=True, capture_output=True,
        )
    agent["env"]["AZDEVOPS_GIT_CREDENTIAL_ENABLED"] = "true"
    result = run(agent)
    assert result.returncode == 0, result.stderr
    assert "WARNING" in result.stderr
    for name, expected in (("provider", "generic"), ("helper", "/home/vibe/.git-credential-azdevops.sh")):
        value = subprocess.run(
            [real_git, "config", "--file", str(config), "--get", prefix + "." + name],
            check=True, capture_output=True, text=True,
        ).stdout.strip()
        assert value == expected


@pytest.mark.parametrize("agent", ["openclaw"], indirect=True)
def test_openclaw_replace_failure_preserves_original(agent):
    assert run(agent).returncode == 0
    original = agent["config_file"].read_bytes()
    stub = agent["binaries"] / "mv"
    real_mv = shutil.which("mv")
    stub.write_text(
        '#!/bin/sh\nfor arg in "$@"; do\n'
        'case "$arg" in */.openclaw/openclaw.json) exit 1;; esac\ndone\n'
        f'exec "{real_mv}" "$@"\n'
    )
    stub.chmod(0o755)
    result = run(agent)
    assert result.returncode != 0
    assert agent["config_file"].read_bytes() == original


def test_hermes_atomic_replace_failure(tmp_path, monkeypatch):
    spec = importlib.util.spec_from_file_location("hermes_apply", CHART / "agents/hermes/bootstrap/hermes-apply-config.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    dest = tmp_path / "config.yaml"
    dest.write_text("original")

    def reject(*_):
        raise OSError("synthetic failure")

    monkeypatch.setattr(module.os, "replace", reject)
    with pytest.raises(OSError):
        module.replace_file(dest, "new")
    assert dest.read_text() == "original"
    assert sorted(path.name for path in tmp_path.iterdir()) == ["config.yaml"]
