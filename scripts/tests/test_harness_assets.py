# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

import os
import re
import runpy
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
CHART = ROOT / "app/cetirun-chart"


class HarnessAssetsTests(unittest.TestCase):
    def test_teams_discovery_does_not_import_runtime_dependencies(self):
        path = CHART / "agents/hermes/plugins/platforms/cetirun_teams/__init__.py"
        code = compile(path.read_text(), str(path), "exec")
        namespace = {"__name__": "cetirun_teams_registration_test"}
        registered = {}

        class Context:
            def register_platform(self, **kwargs):
                registered.update(kwargs)

        with patch("builtins.__import__", side_effect=AssertionError("Discovery must not import runtime SDKs")):
            exec(code, namespace)
            namespace["register"](Context())
            self.assertTrue(registered["check_fn"]())
        self.assertEqual(registered["name"], "cetirun_teams")
        self.assertEqual(registered["adapter_factory"].__name__, "create_adapter")

    def test_image_sources_exist_in_unchanged_build_context(self):
        for kind in ("openclaw", "hermes"):
            dockerfile = CHART / f"agents/{kind}/Dockerfile"
            content = dockerfile.read_text()
            for line in content.splitlines():
                if not line.startswith("COPY ") or "--from=" in line:
                    continue
                source = [part for part in line.split()[1:] if not part.startswith("--")][0]
                self.assertTrue((CHART / source).exists(), f"{kind}: {source}")
            native_version = re.search(
                rf"^ARG {kind.upper()}_VERSION=([^\n]+)", content, re.MULTILINE
            )[1]
            profile = (CHART / f"agents/{kind}/profile.yaml").read_text()
            self.assertIn(f'nativeVersion: "{native_version}"', profile)

    def test_hermes_process_binding_wins_over_retained_home(self):
        source = (CHART / "agents/hermes/bootstrap/hermes-entrypoint.sh").read_text()
        start = source.index("while IFS= read")
        end = source.index('done < "$HERMES_HOME/.env"', start)
        loop = source[start:end] + 'done < "$HERMES_HOME/.env"\n'
        with tempfile.TemporaryDirectory() as directory:
            Path(directory, ".env").write_text("API_SERVER_PORT=8642\nAPI_SERVER_HOST=127.0.0.1\n")
            for port, expected in (("8080", "8080"), (None, "8642")):
                env = dict(os.environ, HERMES_HOME=directory)
                env.pop("API_SERVER_PORT", None)
                env.pop("API_SERVER_HOST", None)
                if port:
                    env["API_SERVER_PORT"] = port
                result = subprocess.run(
                    ["bash", "-eu", "-c", loop + 'printf "%s %s" "$API_SERVER_PORT" "$API_SERVER_HOST"'],
                    env=env, capture_output=True, text=True, check=True,
                )
                self.assertEqual(result.stdout, f"{expected} 127.0.0.1")

    def test_hermes_uses_supported_runtime_interfaces(self):
        entrypoint = (
            CHART / "agents/hermes/bootstrap/hermes-entrypoint.sh"
        ).read_text()
        auth = (CHART / "agents/hermes/bootstrap/hermes-auth.sh").read_text()
        runtime = (
            CHART / "templates/agents/hermes/_runtime.tpl"
        ).read_text()

        self.assertIn(
            "hermes gateway run --replace --accept-hooks --external-supervisor",
            entrypoint,
        )
        self.assertNotIn("| tee -a", entrypoint)
        self.assertNotIn("--insecure", entrypoint)
        self.assertIn("HERMES_DASHBOARD_HOST", entrypoint)
        self.assertIn("/usr/local/lib/hermes-agent/venv/bin/python", entrypoint)

        self.assertIn("hermes auth status copilot", auth)
        self.assertIn("hermes model", auth)
        self.assertNotIn("hermes_cli.", auth)
        legacy = CHART / "agents/hermes/bootstrap/hermes-copilot-auth.py"
        self.assertNotIn("hermes_cli.", legacy.read_text())

        for name, key in (
            ("HERMES_DASHBOARD_BASIC_AUTH_PASSWORD", "API_SERVER_KEY"),
            ("HERMES_DASHBOARD_BASIC_AUTH_SECRET", "CETIRUN_AUTH_KEY"),
        ):
            self.assertRegex(
                runtime,
                rf"- name: {name}\n"
                r"  valueFrom:\n"
                r"    secretKeyRef:\n"
                r"      name: .*?\n"
                rf"      key: {key}",
            )
        self.assertIn('name: HERMES_DASHBOARD_BASIC_AUTH_USERNAME', runtime)
        self.assertIn('value: "cetirun"', runtime)

    def test_legacy_hermes_auth_helper_delegates_to_public_cli(self):
        helper = runpy.run_path(
            str(CHART / "agents/hermes/bootstrap/hermes-copilot-auth.py")
        )
        for arguments, expected in (
            ([], ["hermes", "model"]),
            (["login"], ["hermes", "model"]),
            (["status"], ["hermes", "auth", "status", "copilot"]),
        ):
            with self.subTest(arguments=arguments), patch("os.execvp") as execute:
                helper["main"](arguments)
                execute.assert_called_once_with("hermes", expected)

    def test_hermes_dashboard_defaults_preserve_explicit_bindings(self):
        source = (CHART / "agents/hermes/bootstrap/hermes-entrypoint.sh").read_text()
        bindings = "\n".join(
            line for line in source.splitlines()
            if line.startswith("export HERMES_DASHBOARD_")
        )
        names = (
            "HERMES_DASHBOARD_HOST",
            "HERMES_DASHBOARD_BASIC_AUTH_USERNAME",
            "HERMES_DASHBOARD_BASIC_AUTH_PASSWORD",
            "HERMES_DASHBOARD_BASIC_AUTH_SECRET",
        )
        env = {key: value for key, value in os.environ.items() if key not in names}
        env.update(API_SERVER_KEY="test-api-key", CETIRUN_AUTH_KEY="test-auth-key")
        output = 'printf "%s\\n" ' + " ".join(f'"${name}"' for name in names)
        for overrides, expected in (
            ({}, ["0.0.0.0", "cetirun", "test-api-key", "test-auth-key"]),
            (
                dict(zip(names, ["127.0.0.1", "custom", "password", "secret"])),
                ["127.0.0.1", "custom", "password", "secret"],
            ),
        ):
            with self.subTest(overrides=bool(overrides)):
                result = subprocess.run(
                    ["bash", "-eu", "-c", bindings + "\n" + output],
                    env=env | overrides, capture_output=True, text=True, check=True,
                )
                self.assertEqual(result.stdout.splitlines(), expected)

    def test_hermes_config_uses_current_memory_keys(self):
        values = (CHART / "values.yaml").read_text()
        self.assertIn("memory_char_limit: 2200", values)
        self.assertNotRegex(values, r"(?m)^\s+char_limit:")


if __name__ == "__main__":
    unittest.main()
