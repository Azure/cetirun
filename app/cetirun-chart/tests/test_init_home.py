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
        CHART / "skills/azure",
        source / f".{kind}/skills/azure",
        dirs_exist_ok=True,
    )
    if kind == "hermes":
        (config / "hermes-auth.sh").write_text("#!/bin/sh\nexit 0\n")
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
    if agent["kind"] == "hermes":
        assert helper.stat().st_mode & 0o777 == 0o755
    else:
        # OpenClaw's models come through the AI Gateway; no in-agent sign-in.
        assert not helper.exists()
    assert (
        agent["target"] / f'.{agent["kind"]}/skills/azure/SKILL.md'
    ).read_text() == (CHART / "skills/azure/SKILL.md").read_text()
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
    # Other providers keep the owner's settings.
    assert config["models"]["providers"]["anthropic"] == {"apiKey": "owner-anthropic-test-key"}


@pytest.mark.parametrize("agent", ["openclaw"], indirect=True)
@pytest.mark.parametrize("marker", [None, "false"])
def test_openclaw_with_its_own_openai_endpoint_keeps_its_model_settings(agent, marker):
    # Without the Gateway, an openai/ default is not CetiRun's model list.
    agent["env"].pop("CETIRUN_AI_GATEWAY", None)
    if marker is not None:
        agent["env"]["CETIRUN_AI_GATEWAY"] = marker
    saved_openclaw_config(agent)
    result = run(agent)
    assert result.returncode == 0, result.stderr
    config = read_config(agent)
    assert config["agents"]["defaults"]["models"] == {
        "openai/model-a": {"alias": "my-alias", "params": {"temperature": 0.2}},
        "openai/model-old": {},
        "openai/model-b": {},
    }
    assert "github-copilot:github" in config["auth"]["profiles"]
    # The owner's own openai settings stay, with the rendered endpoint.
    assert config["models"]["providers"]["openai"]["apiKey"] == "owner-test-key"
    assert config["models"]["providers"]["openai"]["baseUrl"] == "https://models.example.com/v1"


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
    on_gateway(agent)

    result = run(agent)
    assert result.returncode == 0, result.stderr
    config = read_config(agent)
    # The Gateway owns the model choice and its provider: replaced, not merged.
    assert config["model"] == GATEWAY_CONFIG["model"]
    assert config["providers"]["cetirun-gateway"] == GATEWAY_CONFIG["providers"]["cetirun-gateway"]
    assert config["providers"]["mine"] == {"api": "https://models.example.com/v1", "key_env": "MY_KEY"}
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
def test_hermes_off_the_gateway_drops_its_provider(agent):
    on_gateway(agent)
    assert run(agent).returncode == 0
    config = read_config(agent)
    config["providers"]["mine"] = {"api": "https://models.example.com/v1"}
    agent["config_file"].write_text(yaml.safe_dump(config))
    (agent["config"] / "hermes-config.yaml").write_text("model:\n  default: test-model\n  provider: openai\n")
    (agent["config"] / "hermes-auth.sh").write_text("#!/bin/sh\nexit 0\n")
    for name in ("CETIRUN_AI_GATEWAY", "CETIRUN_GATEWAY_KEY_ENV"):
        agent["env"].pop(name)
    result = run(agent)
    assert result.returncode == 0, result.stderr
    config = read_config(agent)
    assert config["providers"] == {"mine": {"api": "https://models.example.com/v1"}}
    assert config["model"]["provider"] == "openai" and config["model"]["default"] == "test-model"
    assert (agent["target"] / ".local/bin/copilot-auth").stat().st_mode & 0o777 == 0o755


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
    assert "COPILOT_GITHUB_TOKEN" in env_names(agent)

    on_gateway(agent)
    result = run(agent)
    assert result.returncode == 0, result.stderr
    assert "COPILOT_GITHUB_TOKEN" not in env_names(agent)
    assert (agent["target"] / ".hermes/.cetirun-copilot-sign-in-retired").is_file()

    result = run(agent)
    assert result.returncode == 0, result.stderr
    names = env_names(agent)
    assert "COPILOT_GITHUB_TOKEN" not in names and "API_SERVER_KEY" in names