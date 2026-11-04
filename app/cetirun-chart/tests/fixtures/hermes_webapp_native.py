# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Run the platform guard with a pinned Hermes interpreter and synthetic files."""

import importlib.util
import json
import os
from pathlib import Path
import shutil
import sys
import tempfile
from unittest.mock import patch

from native_network import install

install()


def secret_source_policy(runtime, home, generated, expected):
    from agent.secret_sources import registry
    from agent.secret_sources.base import FetchResult, SecretSource
    from agent.secret_sources.onepassword import OnePasswordSource
    from hermes_cli import env_loader

    for override in (True, False):
        for variable in ("API_SERVER_HOST", "HERMES_CONFIG_BACKEND", "HERMES_ENV_PATH", "HERMES_PROFILE_NAME",
                         "HERMES_DASHBOARD_BASIC_AUTH_SECRET", "CETIRUN_GATEWAY_API_KEY"):
            settings = {"enabled": True, "override_existing": override,
                        "env": {variable: "op://example/item/token"}}
            (home / "config.yaml").write_text(json.dumps(generated | {"secrets": {"onepassword": settings}}))
            with patch.object(env_loader, "load_hermes_dotenv", side_effect=AssertionError("hydration reached")) as hydrate:
                try:
                    runtime.effective(expected)
                except ValueError as exc:
                    assert str(exc) == "SecretSourcePlatformOverride"
                else:
                    raise AssertionError("reserved external binding accepted")
                hydrate.assert_not_called()
        for name in ("bitwarden", "command"):
            (home / "config.yaml").write_text(json.dumps(generated | {
                "secrets": {name: {"enabled": True, "override_existing": override, "command": "must-not-run"}},
            }))
            with patch.object(env_loader, "load_hermes_dotenv", side_effect=AssertionError("hydration reached")) as hydrate:
                try:
                    runtime.effective(expected)
                except ValueError as exc:
                    assert str(exc) == "SecretSourceOutputsUnbounded"
                else:
                    raise AssertionError("bulk external source accepted")
                hydrate.assert_not_called()

    (home / "config.yaml").write_text(json.dumps(generated))
    profile = home / "profiles/synthetic-b"
    profile.mkdir(parents=True)
    original_key = os.environ["CETIRUN_GATEWAY_KEY_ENV"]
    os.environ["CETIRUN_GATEWAY_KEY_ENV"] = "OPENAI_API_KEY"
    mapped = {"enabled": True, "override_existing": True, "env": {"OPENAI_API_KEY_SYNTHETIC_B": "op://example/item/token"}}
    (profile / "config.yaml").write_text(json.dumps({"secrets": {"onepassword": mapped}}))
    with patch.object(env_loader, "load_hermes_dotenv", side_effect=AssertionError("hydration reached")) as hydrate:
        try:
            runtime.effective(expected)
        except ValueError as exc:
            assert str(exc) == "SecretSourceProfileOverride"
        else:
            raise AssertionError("profile alias replaced platform model key")
        hydrate.assert_not_called()
    (profile / "config.yaml").write_text(json.dumps({"secrets": {"profile_alias": False, "onepassword": mapped}}))
    runtime.validate_home_secret_sources()
    os.environ["CETIRUN_GATEWAY_KEY_ENV"] = original_key
    (profile / "config.yaml").write_text("{}")

    permitted = {"onepassword": {"enabled": True, "override_existing": True,
                                "env": {"UNRELATED_SERVICE_TOKEN": "op://example/item/token"}}}
    (home / "config.yaml").write_text(json.dumps(generated | {"secrets": permitted}))
    with patch.object(OnePasswordSource, "fetch", side_effect=AssertionError("provider called")) as fetch:
        runtime.effective(expected)
        fetch.assert_not_called()
    before = dict(os.environ)
    after = dict(before)
    with patch.object(OnePasswordSource, "fetch", return_value=FetchResult(secrets={"UNRELATED_SERVICE_TOKEN": "synthetic"})):
        registry.apply_all(permitted, home, environ=after)
    assert after["UNRELATED_SERVICE_TOKEN"] == "synthetic"
    assert all(after.get(name) == value for name, value in before.items() if runtime.reserved(name))

    class UnqualifiedSource(SecretSource):
        name = "synthetic_unqualified"
        shape = "bulk"

        def is_enabled(self, _config):
            return True

        def fetch(self, _config, _home):
            raise AssertionError("unqualified source was fetched")

    registry.register_source(UnqualifiedSource())
    try:
        runtime.validate_registered_secret_sources(generated, home)
    except ValueError as exc:
        assert str(exc) == "SecretSourceOutputsUnbounded"
    else:
        raise AssertionError("automatically enabled plugin source accepted")


def main():
    source, bootstrap, case = Path(sys.argv[1]), Path(sys.argv[2]), sys.argv[3]
    with tempfile.TemporaryDirectory(prefix="hermes-webapp-native-") as directory:
        root = Path(directory)
        home, managed = root / "home/.hermes", root / "config/hermes-managed"
        home.mkdir(parents=True)
        managed.mkdir(parents=True)
        host = "127.0.0.1" if case == "mtls" else "0.0.0.0"
        os.environ.update(
            HERMES_HOME=str(home), HERMES_MANAGED_DIR=str(managed), HERMES_PYTHON="/usr/local/bin/hermes-python",
            API_SERVER_ENABLED="true", API_SERVER_HOST=host, API_SERVER_PORT="8642", CETIRUN_AI_GATEWAY="true",
            HERMES_WEBAPP_ENABLED="true", HERMES_WEBAPP_HOST=host, HERMES_WEBAPP_PORT="9120",
            HERMES_WEBAPP_PUBLIC_URL="https://agents.example.com/ui/synthetic-agent",
            HERMES_WEBAPP_DIST=str(source / "apps/desktop/dist-webapp"),
            HERMES_DASHBOARD_BASIC_AUTH_USERNAME="cetirun", API_SERVER_KEY="synthetic-api-key-" * 3,
            CETIRUN_AUTH_KEY="synthetic-signing-key-" * 3, CETIRUN_GATEWAY_KEY_ENV="CETIRUN_GATEWAY_API_KEY",
            CETIRUN_GATEWAY_API_KEY="sk-synthetic-model", CETIRUN_INGRESS_MTLS="true" if case == "mtls" else "false",
        )
        os.environ.update(HERMES_DASHBOARD_BASIC_AUTH_PASSWORD=os.environ["API_SERVER_KEY"],
                          HERMES_DASHBOARD_BASIC_AUTH_SECRET=os.environ["CETIRUN_AUTH_KEY"])
        spec = importlib.util.spec_from_file_location("hermes_runtime", bootstrap / "hermes-runtime.py")
        runtime = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(runtime)
        runtime.HOME, runtime.MANAGED, runtime.ROOT, runtime.CONFIG = home, managed, source, root / "config"
        shutil.copy(bootstrap / "hermes-apply-config.py", runtime.CONFIG)
        expected = {"enabled": True, "host": host, "port": 9120,
                    "public_url": os.environ["HERMES_WEBAPP_PUBLIC_URL"]}
        (managed / "config.yaml").write_text(json.dumps({"webapp": expected}))
        (managed / "config.yaml").chmod(0o444)
        generated = {"model": {"provider": "cetirun-gateway", "default": "synthetic-model"},
                     "providers": {"cetirun-gateway": {
                         "name": "CetiRun AI Gateway", "api": "https://gateway.example.com/v1",
                         "key_env": "CETIRUN_GATEWAY_API_KEY", "transport": "chat_completions",
                         "default_model": "synthetic-model", "discover_models": False, "models": {"synthetic-model": {}},
                     }}}
        (runtime.CONFIG / "hermes-config.yaml").write_text(json.dumps(generated))
        (home / "config.yaml").write_text(json.dumps(generated))
        (home / ".env").write_text("")
        if case == "missing-managed":
            (managed / "config.yaml").unlink()
        elif case == "invalid-managed":
            (managed / "config.yaml").chmod(0o644)
            (managed / "config.yaml").write_text('{"webapp":{"enabled":true}}')
            (managed / "config.yaml").chmod(0o444)
        elif case == "missing-signing-key":
            os.environ.pop("CETIRUN_AUTH_KEY")
        elif case == "invalid-signing-key":
            os.environ["CETIRUN_AUTH_KEY"] = "$(unexpanded-command)"
        negative = case in {"missing-managed", "invalid-managed", "missing-signing-key", "invalid-signing-key"}
        try:
            runtime.check() if negative else runtime.contract()
        except ValueError:
            if not negative:
                raise
            print("rejected")
            return
        if negative:
            raise AssertionError("invalid contract was accepted")
        if case == "source-policy":
            secret_source_policy(runtime, home, generated, expected)
            print("passed")
            return
        if case == "missing-provider":
            (home / "config.yaml").write_text(json.dumps(generated | {"plugins": {"disabled": ["basic"]}}))
            try:
                runtime.check()
            except ValueError as exc:
                assert str(exc) == "PasswordProviderMissing"
            else:
                raise AssertionError("missing password provider accepted")
            print("rejected")
            return
        if case == "missing-bundle":
            runtime.ROOT = root / "image-without-bundle"
            runtime.ROOT.mkdir()
            shutil.copy(source / "install-stamp.json", runtime.ROOT)
            os.environ["HERMES_WEBAPP_DIST"] = str(runtime.ROOT / "apps/desktop/dist-webapp")
            try:
                runtime.check()
            except ValueError as exc:
                assert str(exc) == "WebappBundleMissing"
            else:
                raise AssertionError("missing packaged bundle accepted")
            print("rejected")
            return

        api = {"enabled": False, "host": "::", "port": 18000, "key": "sk-override",
               "extra": {"host": "::", "port": 18001, "key": "sk-override"}}
        retained = generated | {"api_server": api, "platforms": {"api_server": api},
                                "gateway": {"api_server": api, "platforms": {"api_server": api}}}
        if case == "profiles":
            (home / "active_profile").write_text("synthetic-b\n")
            for profile in ("synthetic-a", "synthetic-b"):
                path = home / "profiles" / profile
                path.mkdir(parents=True)
                (path / "config.yaml").write_text(json.dumps(retained | {"webapp": {"enabled": False, "port": 18002}}))
                (path / ".env").write_text("API_SERVER_PORT=18003\n")
        for _ in range(2):
            (home / "config.yaml").write_text(json.dumps(retained))
            (home / "gateway.json").write_text(json.dumps({"platforms": {"api_server": api}}))
            (home / ".env").write_text(
                "HERMES_MANAGED_DIR=/tmp/bypass\nHERMES_DASHBOARD_BASIC_AUTH_PASSWORD=wrong\n"
                "HERMES_CONFIG_BACKEND=remote\nHERMES_PROFILE=synthetic-b\nHERMES_INSTALL_ROOT=/tmp/other\n"
                "HERMES_CONFIG_PATH=/tmp/other.yaml\nHERMES_ENV_PATH=/tmp/other.env\nAPI_SERVER_HOST=::\n"
                "API_SERVER_PORT=18004\nCETIRUN_GATEWAY_API_KEY=sk-override\n"
            )
            runtime.check()
        print("passed")


if __name__ == "__main__":
    main()
