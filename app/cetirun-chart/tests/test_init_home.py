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
    shutil.copytree(
        CHART / "skills/connected-services",
        source / f".{kind}/skills/connected-services",
        dirs_exist_ok=True,
    )
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
        script = CHART / "agents/openclaw/bootstrap/init-home.sh"
        config_file = target / ".openclaw/openclaw.json"
    else:
        (config / "hermes-config.yaml").write_text(yaml.safe_dump(GATEWAY_CONFIG))
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
            CETIRUN_AI_GATEWAY="true", CETIRUN_GATEWAY_KEY_ENV="CETIRUN_GATEWAY_API_KEY",
            CETIRUN_BUILTIN_SKILL_SYNC=str(CHART / "configs/builtin-skill-sync.sh"),
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
    assert not helper.exists()
    skill_source = CHART / "skills/connected-services"
    skill_target = agent["target"] / f'.{agent["kind"]}/skills/connected-services'
    for path in skill_source.rglob("*"):
        if path.is_file():
            installed = skill_target / path.relative_to(skill_source)
            assert installed.read_bytes() == path.read_bytes()
    if agent["kind"] == "openclaw":
        user = agent["target"] / ".openclaw/workspace/USER.md"
        assert read_config(agent)["channels"]["msteams"]["allowFrom"] == ["00000000-0000-0000-0000-000000000000"]
        assert (agent["target"] / ".openclaw/npm").is_symlink()
        assert not (agent["target"] / ".acpx/config.json").exists()
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


def test_openclaw_disabling_memory_removes_only_platform_plugin_config(agent):
    if agent["kind"] != "openclaw":
        return
    source = agent["config"] / "openclaw.json"
    config = json.loads(source.read_text())
    config["plugins"] = {
        "allow": ["cetirun-memory-context", "fixture-plugin"],
        "load": {"paths": ["/opt/cetirun-memory/adapters/openclaw", "/fixture/plugin"]},
        "entries": {"cetirun-memory-context": {"enabled": True}, "fixture-plugin": {"enabled": True}},
    }
    config["skills"] = {"load": {"extraDirs": ["/home/vibe/.agents/skills", "/fixture/skills"]}}
    source.write_text(json.dumps(config))
    assert run(agent).returncode == 0
    del config["plugins"]
    del config["skills"]
    source.write_text(json.dumps(config))
    disabled = run(agent)
    assert disabled.returncode == 0, disabled.stderr
    saved = read_config(agent)
    assert saved["plugins"]["allow"] == ["fixture-plugin"]
    assert saved["plugins"]["load"]["paths"] == ["/fixture/plugin"]
    assert saved["plugins"]["entries"] == {"fixture-plugin": {"enabled": True}}
    assert saved["skills"]["load"]["extraDirs"] == ["/fixture/skills"]
    assert run(agent).returncode == 0
    assert read_config(agent) == saved


@pytest.mark.parametrize("agent", ["openclaw", "hermes"], indirect=True)
@pytest.mark.skipif(shutil.which("helm") is None, reason="requires Helm")
def test_reconciled_rendered_memory_survives_home_initialization(agent):
    helpers = CHART.parents[1] / "scripts/tests/test_native_gateway_chart.py"
    spec = importlib.util.spec_from_file_location("native_gateway_chart_fixture", helpers)
    fixture = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(fixture)
    values = fixture.native_values()
    values["agentType"] = agent["kind"]
    values["sandboxWorkload"].update(reconciled=True, executionHeld=False)
    values["memoryContext"] = {"enabled": True, "image": "registry.example.com/memory:1.2.3"}
    agent["env"].update(CETIRUN_AI_GATEWAY="true", CETIRUN_NATIVE_SANDBOX="true")
    config_name = "openclaw.json" if agent["kind"] == "openclaw" else "hermes-config.yaml"
    source = agent["config"] / config_name
    rendered = fixture.object_of_kind(fixture.render(CHART, values), "ConfigMap")["data"][config_name]
    source.write_text(rendered)
    for _ in range(2):
        result = run(agent)
        assert result.returncode == 0, result.stderr
        current = read_config(agent)
        if agent["kind"] == "openclaw":
            plugin = current.get("plugins", {}).get("entries", {}).get("cetirun-memory-context", {})
            assert plugin.get("enabled") is True
            assert plugin.get("hooks", {}).get("allowConversationAccess") is True
            assert current["plugins"]["allow"].count("cetirun-memory-context") == 1
            assert current["plugins"]["load"]["paths"].count("/opt/cetirun-memory/adapters/openclaw") == 1
            assert current["skills"]["load"]["extraDirs"].count("/home/vibe/.agents/skills") == 1
        else:
            assert current.get("plugins", {}).get("enabled", []).count("cetirun-memory-context") == 1
            assert current.get("skills", {}).get("external_dirs", []).count("/home/vibe/.agents/skills") == 1
        current["user_preference"] = "synthetic-retained-preference"
        agent["config_file"].write_text(json.dumps(current))

    values["memoryContext"]["enabled"] = False
    source.write_text(fixture.object_of_kind(fixture.render(CHART, values), "ConfigMap")["data"][config_name])
    result = run(agent)
    assert result.returncode == 0, result.stderr
    disabled = read_config(agent)
    if agent["kind"] == "openclaw":
        assert "cetirun-memory-context" not in disabled["plugins"]["entries"]
        assert "/opt/cetirun-memory/adapters/openclaw" not in disabled["plugins"]["load"]["paths"]
        assert "/home/vibe/.agents/skills" not in disabled["skills"]["load"]["extraDirs"]
    else:
        assert "cetirun-memory-context" not in disabled["plugins"]["enabled"]
        assert "/home/vibe/.agents/skills" not in disabled["skills"]["external_dirs"]
    assert disabled["user_preference"] == "synthetic-retained-preference"


def test_memory_upgrade_preserves_home_plugin_and_skill_lists(agent):
    assert run(agent).returncode == 0
    saved = read_config(agent)
    source = agent["config"] / ("openclaw.json" if agent["kind"] == "openclaw" else "hermes-config.yaml")
    desired = yaml.safe_load(source.read_text())
    if agent["kind"] == "openclaw":
        saved["plugins"] = {"allow": ["home-plugin"], "load": {"paths": ["/fixture/home-plugin"]},
                            "entries": {"home-plugin": {"enabled": True}}}
        saved["skills"] = {"load": {"extraDirs": ["/fixture/home-skills"]}}
        desired["plugins"] = {"allow": ["home-plugin", "cetirun-memory-context", "chart-plugin"],
                              "load": {"paths": ["/opt/cetirun-memory/adapters/openclaw", "/fixture/chart-plugin"]},
                              "entries": {"cetirun-memory-context": {"enabled": True}, "chart-plugin": {"enabled": True}}}
        desired["skills"] = {"load": {"extraDirs": ["/home/vibe/.agents/skills", "/fixture/chart-skills"]}}
    else:
        saved["plugins"] = {"enabled": ["home-plugin"]}
        saved["skills"] = {"external_dirs": ["/fixture/home-skills"]}
        desired["plugins"] = {"enabled": ["cetirun-memory-context", "chart-plugin"]}
        desired["skills"] = {"external_dirs": ["/home/vibe/.agents/skills", "/fixture/chart-skills"]}
    agent["config_file"].write_text(json.dumps(saved))
    source.write_text(json.dumps(desired))
    for _ in range(2):
        result = run(agent)
        assert result.returncode == 0, result.stderr
        enabled = read_config(agent)
        if agent["kind"] == "openclaw":
            assert enabled["plugins"]["allow"] == ["home-plugin", "cetirun-memory-context", "chart-plugin"]
            assert enabled["plugins"]["load"]["paths"] == ["/fixture/home-plugin", "/opt/cetirun-memory/adapters/openclaw", "/fixture/chart-plugin"]
            assert enabled["skills"]["load"]["extraDirs"] == ["/fixture/home-skills", "/home/vibe/.agents/skills", "/fixture/chart-skills"]
        else:
            assert enabled["plugins"]["enabled"] == ["home-plugin", "cetirun-memory-context", "chart-plugin"]
            assert enabled["skills"]["external_dirs"] == ["/fixture/home-skills", "/home/vibe/.agents/skills", "/fixture/chart-skills"]
    if agent["kind"] == "openclaw":
        desired["plugins"] = {"allow": ["home-plugin", "chart-plugin"], "load": {"paths": ["/fixture/chart-plugin"]}}
        desired["skills"] = {"load": {"extraDirs": ["/fixture/chart-skills"]}}
    else:
        desired["plugins"] = {"enabled": ["chart-plugin"]}
        desired["skills"] = {"external_dirs": ["/fixture/chart-skills"]}
    source.write_text(json.dumps(desired))
    for _ in range(2):
        result = run(agent)
        assert result.returncode == 0, result.stderr
        disabled = read_config(agent)
        if agent["kind"] == "openclaw":
            assert disabled["plugins"]["allow"] == ["home-plugin", "chart-plugin"]
            assert disabled["plugins"]["load"]["paths"] == ["/fixture/home-plugin", "/fixture/chart-plugin"]
            assert disabled["skills"]["load"]["extraDirs"] == ["/fixture/home-skills", "/fixture/chart-skills"]
            assert "cetirun-memory-context" not in disabled["plugins"]["entries"]
        else:
            assert disabled["plugins"]["enabled"] == ["home-plugin", "chart-plugin"]
            assert disabled["skills"]["external_dirs"] == ["/fixture/home-skills", "/fixture/chart-skills"]


def test_hermes_null_generated_sections_preserve_home_lists(agent):
    if agent["kind"] != "hermes":
        return
    assert run(agent).returncode == 0
    saved = read_config(agent)
    saved["plugins"] = {"enabled": ["home-plugin", "cetirun-memory-context"]}
    saved["skills"] = {"external_dirs": ["/fixture/home-skills", "/home/vibe/.agents/skills"]}
    agent["config_file"].write_text(json.dumps(saved))
    source = agent["config"] / "hermes-config.yaml"
    desired = yaml.safe_load(source.read_text())
    desired.update(plugins=None, skills=None)
    source.write_text(json.dumps(desired))
    result = run(agent)
    assert result.returncode == 0, result.stderr
    actual = read_config(agent)
    assert actual["plugins"]["enabled"] == ["home-plugin"]
    assert actual["skills"]["external_dirs"] == ["/fixture/home-skills"]


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


@pytest.mark.parametrize("agent", ["openclaw"], indirect=True)
@pytest.mark.parametrize(("content", "kept"), [
    # The header of the helper earlier releases installed.
    ("#!/bin/sh\nset -e\n\n# copilot-auth \u2014 Authenticate GitHub Copilot for OpenClaw.\n", False),
    ("#!/bin/sh\necho my own script\n", True),
])
def test_openclaw_removes_only_the_retired_copilot_helper(agent, content, kept):
    # A HOME an earlier release initialized, restarted with this one.
    assert run(agent).returncode == 0
    helper = agent["target"] / ".local/bin/copilot-auth"
    helper.parent.mkdir(parents=True, exist_ok=True)
    helper.write_text(content)
    result = run(agent)
    assert result.returncode == 0, result.stderr
    assert helper.exists() == kept


def openai_config(models, provider=None):
    # The configuration both an Agent on the AI Gateway and a deployment with
    # its own openai endpoint render: an openai/ default and that provider.
    return {
        "agents": {"defaults": {"model": {"primary": "openai/model-a"}, "models": models}},
        "models": {"providers": {"openai": provider or {"baseUrl": "https://models.example.com/v1", "models": []}}},
    }


# The Gateway provider as the chart renders it: bound to the Agent's virtual
# key in OPENAI_API_KEY.
GATEWAY_PROVIDER = {
    "baseUrl": "http://cetirun-ai-gateway.cetirun-system.svc.cluster.local:4000/v1",
    "models": [{"id": "model-a", "name": "model-a"}],
    "auth": "api-key",
    "apiKey": {"source": "env", "provider": "default", "id": "OPENAI_API_KEY"},
}


def saved_openclaw_config(agent, provider=None):
    # A HOME the Agent has used: its own model settings, a sign-in profile,
    # and the owner's own keys, for openai and for another provider.
    assert run(agent).returncode == 0
    saved = read_config(agent)
    saved["agents"]["defaults"]["models"] = {
        "openai/model-a": {"alias": "my-alias", "params": {"temperature": 0.2}},
        "openai/model-old": {},
    }
    saved["auth"] = {"profiles": {"github-copilot:github": {"provider": "github-copilot"}}}
    saved["models"] = {"providers": {
        "openai": {
            "baseUrl": "https://api.openai.com/v1",
            "apiKey": "owner-test-key",
            "request": {"auth": {"mode": "authorization-bearer", "token": "owner-test-key"}},
        },
        "anthropic": {"apiKey": "owner-anthropic-test-key"},
    }}
    agent["config_file"].write_text(json.dumps(saved))
    (agent["config"] / "openclaw.json").write_text(
        json.dumps(openai_config({"openai/model-a": {}, "openai/model-b": {}}, provider)))


@pytest.mark.parametrize("agent", ["openclaw"], indirect=True)
def test_openclaw_on_the_gateway_takes_the_gateway_model_list(agent):
    agent["env"]["CETIRUN_AI_GATEWAY"] = "true"
    saved_openclaw_config(agent, GATEWAY_PROVIDER)
    result = run(agent)
    assert result.returncode == 0, result.stderr
    config = read_config(agent)
    assert config["agents"]["defaults"]["models"] == {"openai/model-a": {}, "openai/model-b": {}}
    assert "github-copilot:github" not in config["auth"]["profiles"]
    # The Gateway provider is CetiRun's, bound to the Agent's key: a key or a
    # request override saved on it would be sent to the Gateway instead.
    assert config["models"]["providers"]["openai"] == GATEWAY_PROVIDER
    # Saved direct providers cannot survive the platform Gateway configuration.
    assert set(config["models"]["providers"]) == {"openai"}


def native_openclaw_source(agent):
    saved_openclaw_config(agent, GATEWAY_PROVIDER)
    source_file = agent["config"] / "openclaw.json"
    source = json.loads(source_file.read_text())
    source["auth"] = {"profiles": {}}
    source["gateway"] = {"auth": {"mode": "token"}, "controlUi": {"basePath": "/ui/example-agent"}}
    source["agents"]["defaults"].update({
        "model": {"primary": "openai/model-a"},
        "modelPolicy": {"allow": ["openai/model-a", "openai/model-b"]},
        "utilityModel": "openai/model-a",
    })
    source_file.write_text(json.dumps(source))
    agent["env"].update(CETIRUN_AI_GATEWAY="true", CETIRUN_NATIVE_SANDBOX="true")
    return source


@pytest.mark.parametrize("agent", ["openclaw"], indirect=True)
def test_native_openclaw_replaces_saved_model_and_gateway_overrides(agent):
    source = native_openclaw_source(agent)
    saved = read_config(agent)
    saved["user_preference"] = "preserved"
    saved["gateway"] = {"auth": {"mode": "none"}, "controlUi": {"allowInsecureAuth": True}}
    saved["agents"]["list"] = [{"id": "example-extra", "model": "anthropic/ungranted"}]
    saved["agents"]["defaults"]["model"]["fallbacks"] = ["anthropic/ungranted"]
    agent["config_file"].write_text(json.dumps(saved))
    env_file = agent["target"] / ".openclaw/.env"
    env_file.write_text(
        "OPENAI_API_KEY=sk-dummy\n  export ANTHROPIC_API_KEY = sk-dummy\n"
        "OPENAI_BASE_URL=https://ungranted.example.com\nMODEL_API_KEY=sk-dummy\n"
        "COPILOT_GITHUB_TOKEN=sk-dummy\nBRAVE_API_KEY=keep-brave\nUSER_SETTING=keep\n"
    )
    for _ in range(2):
        result = run(agent)
        assert result.returncode == 0, result.stderr
        config = read_config(agent)
        assert config["models"] == source["models"]
        assert config["auth"] == source["auth"]
        assert config["gateway"] == source["gateway"]
        assert config["agents"]["defaults"]["model"] == source["agents"]["defaults"]["model"]
        assert config["agents"]["defaults"]["modelPolicy"] == source["agents"]["defaults"]["modelPolicy"]
        assert config["agents"]["defaults"]["utilityModel"] == "openai/model-a"
        assert "list" not in config["agents"]
        assert config["user_preference"] == "preserved"
        assert env_file.read_text() == "BRAVE_API_KEY=keep-brave\nUSER_SETTING=keep\n"
        assert env_file.stat().st_mode & 0o777 == 0o600
        assert "sk-dummy" not in result.stdout + result.stderr


@pytest.mark.parametrize("agent", ["openclaw"], indirect=True)
def test_native_openclaw_requires_a_gateway_binding(agent):
    agent["env"]["CETIRUN_NATIVE_SANDBOX"] = "true"
    agent["env"].pop("CETIRUN_AI_GATEWAY")
    result = run(agent)
    assert result.returncode != 0
    assert "AIGatewayRequired" in result.stderr
    assert not (agent["target"] / ".cetirun-home-seeded").exists()


@pytest.mark.parametrize("agent", ["openclaw"], indirect=True)
@pytest.mark.parametrize("invalid", ["key", "named-agent"])
def test_native_openclaw_refuses_unbound_configuration(agent, invalid):
    native_openclaw_source(agent)
    source_file = agent["config"] / "openclaw.json"
    source = json.loads(source_file.read_text())
    if invalid == "key":
        source["models"]["providers"]["openai"]["apiKey"] = "sk-dummy-unbound"
    else:
        source["agents"]["list"] = [{"id": "example-extra", "model": "anthropic/ungranted"}]
    source_file.write_text(json.dumps(source))
    saved = agent["config_file"].read_text()
    result = run(agent)
    assert result.returncode != 0
    assert "OpenClawNativeModelConfigInvalid" in result.stderr
    assert "sk-dummy-unbound" not in result.stdout + result.stderr
    assert agent["config_file"].read_text() == saved


@pytest.mark.parametrize("agent", ["openclaw"], indirect=True)
def test_native_openclaw_does_not_follow_a_dotenv_link(agent, tmp_path):
    native_openclaw_source(agent)
    outside = tmp_path / "outside.env"
    outside.write_text("OPENAI_API_KEY=sk-dummy\n")
    (agent["target"] / ".openclaw/.env").symlink_to(outside)
    result = run(agent)
    assert result.returncode != 0
    assert "HomePathIsSymlink" in result.stderr
    assert outside.read_text() == "OPENAI_API_KEY=sk-dummy\n"


@pytest.mark.parametrize("agent", ["openclaw"], indirect=True)
@pytest.mark.parametrize("marker", [None, "false"])
def test_openclaw_refuses_direct_model_mode(agent, marker):
    assert run(agent).returncode == 0
    before = agent["config_file"].read_bytes()
    if marker is None:
        agent["env"].pop("CETIRUN_AI_GATEWAY", None)
    else:
        agent["env"]["CETIRUN_AI_GATEWAY"] = marker
    result = run(agent)
    assert result.returncode != 0
    assert "AIGatewayRequired" in result.stderr
    assert agent["config_file"].read_bytes() == before


GATEWAY_CONFIG = {
    "model": {"default": "gpt-6-sol", "provider": "cetirun-gateway"},
    "providers": {"cetirun-gateway": {
        "name": "CetiRun AI Gateway", "api": "http://gateway.example.svc.cluster.local:4000/v1",
        "key_env": "CETIRUN_GATEWAY_API_KEY", "transport": "chat_completions", "default_model": "gpt-6-sol",
        "discover_models": False, "models": {"gpt-6-sol": {"context_length": 272000}},
    }},
}


def on_gateway(agent):
    """Render the agent's next start as the chart does on the AI Gateway."""
    (agent["config"] / "hermes-config.yaml").write_text(yaml.safe_dump(GATEWAY_CONFIG))
    (agent["config"] / "hermes-auth.sh").unlink(missing_ok=True)
    agent["env"].update(CETIRUN_AI_GATEWAY="true", CETIRUN_GATEWAY_KEY_ENV="CETIRUN_GATEWAY_API_KEY")


def env_names(agent):
    return [line.split("=", 1)[0] for line in (agent["target"] / ".hermes/.env").read_text().splitlines() if line]


@pytest.mark.parametrize("agent", ["hermes"], indirect=True)
def test_hermes_moves_onto_the_gateway(agent):
    assert run(agent).returncode == 0
    helper = agent["target"] / ".local/bin/copilot-auth"
    helper.write_text("#!/bin/sh\n# copilot-auth: Authenticate GitHub Copilot for Hermes.\nhermes model\n")
    (agent["target"] / ".hermes/.env").write_text(
        "API_SERVER_KEY=old\nCOPILOT_GITHUB_TOKEN=gho_example_signed_in_inside\nGITHUB_TOKEN=ghp_example_own\n"
        "OPENAI_API_KEY=sk-example-own\nCETIRUN_GATEWAY_API_KEY=stale\n")
    config = read_config(agent)
    config["providers"] = {
        "cetirun-gateway": {"api": "http://old.example/v1", "models": {"gone-model": {}}},
        "mine": {"api": "https://models.example.com/v1", "key_env": "MY_KEY"},
    }
    config["model"]["base_url"] = "https://models.example.com/v1"
    config["user_preference"] = "preserved"
    agent["config_file"].write_text(yaml.safe_dump(config))
    jwt = agent["target"] / ".hermes/.copilot_jwt.json"
    jwt.write_text('{"token": "example-exchanged"}')
    (agent["target"] / ".hermes/.cetirun-copilot-sign-in-retired").unlink(missing_ok=True)
    on_gateway(agent)

    result = run(agent)
    assert result.returncode == 0, result.stderr
    config = read_config(agent)
    # The Gateway owns the model choice and its provider: replaced, not merged.
    assert config["model"] == GATEWAY_CONFIG["model"]
    assert config["providers"]["cetirun-gateway"] == GATEWAY_CONFIG["providers"]["cetirun-gateway"]
    assert set(config["providers"]) == {"cetirun-gateway"}
    assert config["user_preference"] == "preserved"
    # The sign-in made inside the agent and the helper are gone; other
    # credentials stay, and the key's variable cannot be overridden from .env.
    names = env_names(agent)
    assert "COPILOT_GITHUB_TOKEN" not in names and "CETIRUN_GATEWAY_API_KEY" not in names
    assert {"GITHUB_TOKEN", "OPENAI_API_KEY", "API_SERVER_KEY"} <= set(names)
    assert not jwt.exists()
    assert not helper.exists()
    assert (agent["target"] / ".hermes/.cetirun-copilot-sign-in-retired").is_file()
    assert native_marker(agent).is_file()

    # Once per HOME: a token the owner adds later stays; the key's variable
    # is removed at every start.
    env = agent["target"] / ".hermes/.env"
    env.write_text(env.read_text() + "COPILOT_GITHUB_TOKEN=gho_example_added_later\nCETIRUN_GATEWAY_API_KEY=again\n")
    result = run(agent)
    assert result.returncode == 0, result.stderr
    names = env_names(agent)
    assert "COPILOT_GITHUB_TOKEN" in names and "CETIRUN_GATEWAY_API_KEY" not in names


@pytest.mark.parametrize("agent", ["hermes"], indirect=True)
def test_hermes_on_the_gateway_keeps_a_helper_it_did_not_install(agent):
    on_gateway(agent)
    assert run(agent).returncode == 0
    helper = agent["target"] / ".local/bin/copilot-auth"
    assert not helper.exists()
    helper.write_text("#!/bin/sh\necho the owner's own script\n")
    result = run(agent)
    assert result.returncode == 0, result.stderr
    assert helper.read_text() == "#!/bin/sh\necho the owner's own script\n"


@pytest.mark.parametrize("agent", ["hermes"], indirect=True)
def test_hermes_refuses_gateway_disable_without_modifying_home(agent):
    on_gateway(agent)
    assert run(agent).returncode == 0
    before = agent["config_file"].read_bytes()
    agent["env"].pop("CETIRUN_AI_GATEWAY")
    result = run(agent)
    assert result.returncode != 0
    assert "AI Gateway is required" in result.stderr
    assert agent["config_file"].read_bytes() == before
    assert not (agent["target"] / ".local/bin/copilot-auth").exists()


@pytest.mark.parametrize("agent", ["hermes"], indirect=True)
def test_hermes_on_the_gateway_with_another_key_variable(agent):
    # Standalone Helm can bind the key to another variable, such as
    # OPENAI_API_KEY, which the chart also writes into the generated
    # hermes.env: empty without secrets.apiKey. Hermes loads .env over its
    # environment, where the key is, so no assignment of the variable may
    # stay in .env, generated or saved.
    on_gateway(agent)
    agent["env"]["CETIRUN_GATEWAY_KEY_ENV"] = "OPENAI_API_KEY"
    config = json.loads(json.dumps(GATEWAY_CONFIG))
    config["providers"]["cetirun-gateway"]["key_env"] = "OPENAI_API_KEY"
    (agent["config"] / "hermes-config.yaml").write_text(yaml.safe_dump(config))
    (agent["secrets"] / "hermes.env").write_text("API_SERVER_KEY=sk-dummy\nOPENAI_API_KEY=\nBRAVE_API_KEY=\n")
    result = run(agent)
    assert result.returncode == 0, result.stderr
    names = env_names(agent)
    assert "OPENAI_API_KEY" not in names
    assert {"API_SERVER_KEY", "BRAVE_API_KEY"} <= set(names)

    env = agent["target"] / ".hermes/.env"
    env.write_text(env.read_text() + "OPENAI_API_KEY=sk-example-saved\n")
    (agent["secrets"] / "hermes.env").write_text("API_SERVER_KEY=sk-dummy\nOPENAI_API_KEY=sk-example-generated\n")
    result = run(agent)
    assert result.returncode == 0, result.stderr
    assert "OPENAI_API_KEY" not in env_names(agent)
    assert read_config(agent)["providers"]["cetirun-gateway"]["key_env"] == "OPENAI_API_KEY"


@pytest.mark.parametrize("agent", ["hermes"], indirect=True)
def test_hermes_on_the_gateway_takes_no_copilot_token_from_the_secret(agent):
    # The operator copies a COPILOT_GITHUB_TOKEN of the instance Secret into
    # hermes.env. Without the Gateway it is Hermes' in-agent sign-in. On the
    # Gateway it must not reach .env: not at the start that retires the
    # sign-in, where the merge would put it back after the saved one is
    # removed and the marker would keep later starts from removing it, nor at
    # a later start.
    (agent["secrets"] / "hermes.env").write_text(
        "API_SERVER_KEY=sk-dummy\nCOPILOT_GITHUB_TOKEN=gho_example_from_the_secret\n")
    assert run(agent).returncode == 0
    assert "COPILOT_GITHUB_TOKEN" not in env_names(agent)

    on_gateway(agent)
    result = run(agent)
    assert result.returncode == 0, result.stderr
    assert "COPILOT_GITHUB_TOKEN" not in env_names(agent)
    assert (agent["target"] / ".hermes/.cetirun-copilot-sign-in-retired").is_file()

    result = run(agent)
    assert result.returncode == 0, result.stderr
    names = env_names(agent)
    assert "COPILOT_GITHUB_TOKEN" not in names and "API_SERVER_KEY" in names

PORTAL_SIGN_IN = {
    "mode": "trusted-proxy",
    "trustedProxy": {"userHeader": "x-auth-request-email", "allowLoopback": True, "deviceAutoApprove": {"enabled": True}},
}


@pytest.mark.parametrize("agent", ["openclaw"], indirect=True)
def test_openclaw_portal_sign_in_replaces_and_then_leaves_the_saved_gateway_auth(agent):
    # A HOME that saved a token: OpenClaw refuses a token in trusted-proxy mode.
    assert run(agent).returncode == 0
    saved = read_config(agent)
    saved["gateway"] = {"port": 18789, "auth": {"mode": "token", "token": "saved-test-token"}}
    agent["config_file"].write_text(json.dumps(saved))
    source_file = agent["config"] / "openclaw.json"
    source = json.loads(source_file.read_text())
    source["gateway"] = {"trustedProxies": ["127.0.0.6"], "auth": PORTAL_SIGN_IN}
    source_file.write_text(json.dumps(source))
    result = run(agent)
    assert result.returncode == 0, result.stderr
    config = read_config(agent)
    assert config["gateway"]["auth"] == PORTAL_SIGN_IN
    assert config["gateway"]["port"] == 18789
    # Turned off again: the chart's mode goes, and OpenClaw falls back to the
    # token from its environment.
    del source["gateway"]["auth"]
    source_file.write_text(json.dumps(source))
    result = run(agent)
    assert result.returncode == 0, result.stderr
    config = read_config(agent)
    assert "auth" not in config["gateway"]
    assert config["gateway"]["trustedProxies"] == ["127.0.0.6"]
    # An owner's own non-proxy auth settings are not the chart's to remove.
    config["gateway"]["auth"] = {"mode": "password"}
    agent["config_file"].write_text(json.dumps(config))
    result = run(agent)
    assert result.returncode == 0, result.stderr
    assert read_config(agent)["gateway"]["auth"] == {"mode": "password"}


@pytest.mark.parametrize("agent", ["openclaw"], indirect=True)
def test_team_change_refreshes_only_managed_context(agent):
    team = agent["config"] / "team-md"
    team.write_text("## Team context\nTeam: first\n")
    assert run(agent).returncode == 0
    user = agent["target"] / ".openclaw/workspace/USER.md"
    user.write_text("Owner preference: preserve me\n" + user.read_text())
    private = agent["target"] / "private-note"
    private.write_text("retained personal content")
    team.write_text("## Team context\nTeam: second\n")
    result = run(agent)
    assert result.returncode == 0, result.stderr
    assert "Team: first" not in user.read_text()
    assert user.read_text().count("Team: second") == 1
    assert user.read_text().startswith("Owner preference: preserve me\n")
    assert private.read_text() == "retained personal content"
    assert run(agent).returncode == 0
    assert user.read_text().count("<!-- cetirun:team:start -->") == 1


@pytest.mark.parametrize("agent", ["openclaw"], indirect=True)
def test_openclaw_team_context_does_not_require_awk(agent):
    (agent["config"] / "team-md").write_text("Synthetic governed Team\n")
    # The production Azure Linux base deliberately has no awk. If this stub is
    # called, the init fails; a successful run proves the portable code path.
    awk = agent["binaries"] / "awk"
    awk.write_text('#!/bin/sh\nexit 97\n')
    awk.chmod(0o755)

    result = run(agent)

    assert result.returncode == 0, result.stderr
    user = agent["target"] / ".openclaw/workspace/USER.md"
    assert user.read_text().count("<!-- cetirun:team:start -->") == 1
    assert "Synthetic governed Team" in user.read_text()


@pytest.mark.parametrize("agent", ["openclaw"], indirect=True)
def test_openclaw_team_context_terminates_owner_last_line_without_newline(agent):
    (agent["config"] / "team-md").write_text("Synthetic governed Team\n")
    # The fixture's USER.md input has no final newline. The replacement keeps
    # the old awk print behavior so managed markers remain standalone lines.
    result = run(agent)

    assert result.returncode == 0, result.stderr
    user = agent["target"] / ".openclaw/workspace/USER.md"
    assert user.read_text().startswith("Example User\n<!-- cetirun:team:start -->\n")
    result = run(agent)
    assert result.returncode == 0, result.stderr
    assert user.read_text().count("<!-- cetirun:team:start -->") == 1


@pytest.mark.parametrize("agent", ["openclaw"], indirect=True)
@pytest.mark.parametrize("invalid", [
    "<!-- cetirun:team:end -->\n",
    "<!-- cetirun:team:start -->\n",
    "<!-- cetirun:team:start -->\n<!-- cetirun:team:end -->\n<!-- cetirun:team:start -->\n<!-- cetirun:team:end -->\n",
    "<!-- cetirun:team:start -->\n<!-- cetirun:team:start -->\n<!-- cetirun:team:end -->\n",
])
def test_openclaw_team_context_rejects_invalid_markers(agent, invalid):
    (agent["config"] / "team-md").write_text("Synthetic governed Team\n")
    assert run(agent).returncode == 0
    user = agent["target"] / ".openclaw/workspace/USER.md"
    user.write_text(invalid)

    result = run(agent)

    assert result.returncode != 0
    assert "TeamContextMarkersInvalid" in result.stderr
    assert user.read_text() == invalid
