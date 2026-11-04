# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Webapp startup and readiness contracts, using synthetic HOME and listener fixtures."""

import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import types
import unittest
from unittest.mock import patch

import yaml

CHART = Path(__file__).resolve().parents[1]
BOOTSTRAP = CHART / "agents/hermes/bootstrap"
spec = importlib.util.spec_from_file_location("hermes_runtime", BOOTSTRAP / "hermes-runtime.py")
runtime = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runtime)


class WebappRuntimeTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        root = Path(self.directory.name)
        self.home, self.config, self.image = root / "home/.hermes", root / "config", root / "image"
        self.managed = self.config / "hermes-managed"
        self.home.mkdir(parents=True)
        self.managed.mkdir(parents=True)
        self.image.mkdir()
        self.env = {
            "HERMES_HOME": str(self.home), "HERMES_MANAGED_DIR": str(self.managed),
            "HERMES_PYTHON": "/usr/local/bin/hermes-python", "API_SERVER_ENABLED": "true",
            "API_SERVER_HOST": "0.0.0.0", "API_SERVER_PORT": "8642", "CETIRUN_AI_GATEWAY": "true",
            "HERMES_WEBAPP_ENABLED": "true", "HERMES_WEBAPP_HOST": "0.0.0.0", "HERMES_WEBAPP_PORT": "9120",
            "HERMES_WEBAPP_PUBLIC_URL": "https://agents.example.com/ui/synthetic-agent",
            "HERMES_WEBAPP_DIST": str(self.image / "apps/desktop/dist-webapp"),
            "HERMES_DASHBOARD_BASIC_AUTH_USERNAME": "cetirun", "API_SERVER_KEY": "synthetic-api-key-" * 3,
            "CETIRUN_AUTH_KEY": "synthetic-signing-key-" * 3, "CETIRUN_GATEWAY_KEY_ENV": "CETIRUN_GATEWAY_API_KEY",
            "CETIRUN_GATEWAY_API_KEY": "sk-synthetic-model",
        }
        self.env.update(HERMES_DASHBOARD_BASIC_AUTH_PASSWORD=self.env["API_SERVER_KEY"],
                        HERMES_DASHBOARD_BASIC_AUTH_SECRET=self.env["CETIRUN_AUTH_KEY"])
        self.expected = {"enabled": True, "host": "0.0.0.0", "port": 9120,
                         "public_url": self.env["HERMES_WEBAPP_PUBLIC_URL"]}
        self.write_managed()
        self.home.joinpath(".env").write_text("")
        self.home.joinpath("config.yaml").write_text("preferences: {theme: dark}\n")
        shutil.copy(BOOTSTRAP / "hermes-apply-config.py", self.config)
        for name, value in {"HOME": self.home, "CONFIG": self.config, "MANAGED": self.managed, "ROOT": self.image}.items():
            mock = patch.object(runtime, name, value)
            mock.start()
            self.addCleanup(mock.stop)
        env = patch.dict(os.environ, self.env, clear=True)
        env.start()
        self.addCleanup(env.stop)

    def write_managed(self, value=None):
        path = self.managed / "config.yaml"
        if path.exists():
            path.chmod(0o644)
        path.write_text(yaml.safe_dump({"webapp": self.expected if value is None else value}))
        path.chmod(0o444)

    def test_contract_requires_exact_managed_types_and_readonly_file(self):
        self.assertEqual(runtime.contract(), self.expected)
        self.write_managed(self.expected | {"enabled": 1})
        with self.assertRaisesRegex(ValueError, "ManagedWebappTypeInvalid"):
            runtime.contract()
        self.write_managed()
        (self.managed / "config.yaml").chmod(0o644)
        with self.assertRaisesRegex(ValueError, "ManagedConfigWritable"):
            runtime.contract()

    def test_mtls_cannot_use_a_wildcard_api_or_webapp(self):
        os.environ["CETIRUN_INGRESS_MTLS"] = "true"
        with self.assertRaisesRegex(ValueError, "ManagedWebappInvalid"):
            runtime.contract()
        self.expected["host"] = "127.0.0.1"
        self.write_managed()
        with self.assertRaisesRegex(ValueError, "PlatformEnvironmentInvalid"):
            runtime.contract()
        os.environ.update(API_SERVER_HOST="127.0.0.1", HERMES_WEBAPP_HOST="127.0.0.1")
        self.assertEqual(runtime.contract(), self.expected)

    def test_missing_or_shared_auth_keys_never_fall_back(self):
        for name in ("API_SERVER_KEY", "CETIRUN_AUTH_KEY", "HERMES_DASHBOARD_BASIC_AUTH_PASSWORD",
                     "HERMES_DASHBOARD_BASIC_AUTH_SECRET"):
            with self.subTest(name=name), patch.dict(os.environ, {name: ""}):
                with self.assertRaises(ValueError):
                    runtime.contract()
        with patch.dict(os.environ, {"CETIRUN_AUTH_KEY": self.env["API_SERVER_KEY"]}):
            with self.assertRaisesRegex(ValueError, "SigningKeyInvalid"):
                runtime.contract()

    def test_external_secret_bindings_cannot_override_platform_or_execute_bulk_helpers(self):
        original = {"secrets": {"onepassword": {"enabled": True, "env": {"UNRELATED_SERVICE_TOKEN": "op://example/item/token"}}}}
        runtime.validate_secret_sources(original)
        self.assertEqual(original["secrets"]["onepassword"]["env"], {"UNRELATED_SERVICE_TOKEN": "op://example/item/token"})
        for variable in ("API_SERVER_HOST", "HERMES_CONFIG_PATH", "HERMES_DASHBOARD_BASIC_AUTH_PASSWORD",
                         "CETIRUN_GATEWAY_API_KEY", "CETIRUN_AUTH_KEY"):
            with self.subTest(variable=variable):
                settings = {"enabled": True, "override_existing": True, "env": {variable: "op://example/item/token"}}
                with self.assertRaisesRegex(ValueError, "SecretSourcePlatformOverride"):
                    runtime.validate_secret_sources({"secrets": {"onepassword": settings}})
        for source in ("bitwarden", "command", "unqualified_plugin"):
            with self.subTest(source=source):
                with self.assertRaisesRegex(ValueError, "SecretSourceOutputsUnbounded"):
                    runtime.validate_secret_sources({"secrets": {source: {"enabled": True}}})
        with self.assertRaisesRegex(ValueError, "SecretSourcePlatformCredentialInvalid"):
            runtime.validate_secret_sources({"secrets": {"onepassword": {
                "enabled": True, "service_account_token_env": "API_SERVER_KEY", "env": {},
            }}})

    def test_process_binding_wins_over_retained_home(self):
        api = {"enabled": False, "host": "::", "port": 18000, "key": "synthetic-override",
               "extra": {"host": "::", "port": 18001, "key": "synthetic-override", "model_name": "retained"}}
        source = {"api_server": api, "platforms": {"api_server": api},
                  "gateway": {"api_server": api, "platforms": {"api_server": api}}, "preferences": {"theme": "dark"}}
        env = '# retained comment\nexport HERMES_MANAGED_DIR="/tmp/bypass"\nHERMES_DASHBOARD_BASIC_AUTH_PASSWORD="line1\nline2"\nAPI_SERVER_PORT=8642\nCETIRUN_GATEWAY_API_KEY=sk-override\nUSER_SETTING="keep me"\n'
        for port in ("8080", "8642"):
            with self.subTest(port=port), patch.dict(os.environ, {"API_SERVER_PORT": port}):
                (self.home / "config.yaml").write_text(yaml.safe_dump(source))
                (self.home / "gateway.json").write_text(json.dumps(source))
                (self.home / ".env").write_text(env)
                runtime.remove_home_overrides()
                self.assertEqual(runtime.contract(), self.expected)
                self.assertEqual(runtime.api_port(), int(port))
                self.assertEqual(os.environ["API_SERVER_PORT"], port)
                self.assertEqual((self.home / ".env").read_text(), '# retained comment\nUSER_SETTING="keep me"\n')
                for relative in ("config.yaml", "gateway.json"):
                    actual = yaml.safe_load((self.home / relative).read_text())
                    self.assertEqual(actual["preferences"], {"theme": "dark"})
                    for row in (actual["api_server"], actual["platforms"]["api_server"], actual["gateway"]["api_server"],
                                actual["gateway"]["platforms"]["api_server"]):
                        self.assertEqual(row, {"extra": {"model_name": "retained"}})

    def test_readiness_requires_live_socket_provider_and_private_persistent_token(self):
        os.environ["API_SERVER_PORT"] = "8080"
        dist = self.image / "apps/desktop/dist-webapp"
        dist.mkdir(parents=True)
        for name in ("index.html", "hermes-build.json", "renderer-manifest.json"):
            (dist / name).write_text("{}")
        folder = self.home / "webapp"
        folder.mkdir(mode=0o700)
        token = folder / "session-token"
        token.write_text("a" * 43 + "\n")
        token.chmod(0o600)
        original = token.read_bytes()
        status = {"ok": True, "result": {"state": "serving", "url": "http://127.0.0.1:9120"}}
        fake = types.SimpleNamespace(private_control_query=lambda *_, **_kwargs: json.dumps(status).encode())
        with patch.dict("sys.modules", {"hermes_cli.gateway_client": fake}), patch.object(runtime, "get_json") as get:
            get.side_effect = [{"status": "ok"}, {"providers": [{"name": "basic", "supports_password": True}]}]
            runtime.readiness(1)
            self.assertEqual(get.call_args_list[0].args[0:2], (8080, "/health"))
            self.assertEqual(token.read_bytes(), original)
            status["result"]["state"] = "unavailable"
            with self.assertRaisesRegex(ValueError, "WebappNotServing"):
                runtime.readiness(1)
            status["result"]["state"] = "serving"
            get.side_effect = [{"status": "ok"}, {"providers": []}]
            with self.assertRaisesRegex(ValueError, "LivePasswordProviderMissing"):
                runtime.readiness(1)
            token.chmod(0o644)
            with self.assertRaisesRegex(ValueError, "BrowserCredentialPermissionsInvalid"):
                runtime.readiness(1)

        with patch.dict(os.environ, {"API_SERVER_PORT": "8080"}, clear=True), \
                patch.object(runtime.sys, "argv", ["hermes-runtime.py", "liveness"]), patch.object(runtime, "get_json") as get:
            self.assertEqual(runtime.main(), 0)
            self.assertEqual(get.call_args.args[0:2], (8080, "/health"))

    def test_mtls_and_egress_helpers_gate_health_with_bounded_checks(self):
        runtime.helper_health(1)
        for flag, filename, code in (
            ("CETIRUN_INGRESS_MTLS", "ingress-health.cjs", "IngressTLSNotReady"),
            ("CETIRUN_EGRESS_SNI", "proxy-health.cjs", "EgressProxyNotReady"),
        ):
            with self.subTest(flag=flag), patch.dict(os.environ, {flag: "true"}), patch.object(runtime.subprocess, "run") as run:
                run.return_value = subprocess.CompletedProcess([], 0)
                runtime.helper_health(0.5)
                self.assertEqual(run.call_args.args[0][-1], str(self.config / filename))
                self.assertEqual(run.call_args.kwargs["timeout"], 0.5)
                run.return_value = subprocess.CompletedProcess([], 1)
                with self.assertRaisesRegex(ValueError, code):
                    runtime.helper_health(0.5)
                run.side_effect = subprocess.TimeoutExpired("node", 0.5)
                with self.assertRaisesRegex(ValueError, code):
                    runtime.helper_health(0.5)


class WebappChartTests(unittest.TestCase):
    def render(self, *extra):
        return subprocess.run([
            "helm", "template", "synthetic-agent", str(CHART), "--set", "aiGateway.enabled=true",
            "--set", "aiGateway.keySecretName=synthetic-model-key", "--set", "secrets.model=synthetic-model",
            "--set", "secrets.apiBase=http://gateway.example.com/v1",
            "--set", "hermes.env.HERMES_WEBAPP_PUBLIC_URL=https://agents.example.com/ui/synthetic-agent", *extra,
        ], capture_output=True, text=True)

    def test_chart_renders_managed_mount_gateway_launcher_and_full_readiness(self):
        result = self.render()
        self.assertEqual(result.returncode, 0, result.stderr)
        docs = list(yaml.safe_load_all(result.stdout))
        pod = next(d for d in docs if d and d["kind"] == "StatefulSet")["spec"]["template"]["spec"]
        main = pod["containers"][0]
        self.assertIn({"name": "hermes-managed", "mountPath": "/etc/cetirun/hermes-managed", "readOnly": True}, main["volumeMounts"])
        self.assertEqual(main["readinessProbe"]["exec"]["command"][0:3],
                         ["/usr/local/bin/hermes-python", "/etc/cetirun/hermes-runtime.py", "readiness"])
        self.assertIn({"name": "webapp", "containerPort": 9120, "protocol": "TCP"}, main["ports"])
        self.assertNotIn(9119, [port["containerPort"] for port in main["ports"]])

    def test_old_and_unmanaged_binding_inputs_are_rejected(self):
        for override in ("hermes.env.HERMES_DASHBOARD_PUBLIC_URL=https://example.com", "hermes.env.API_SERVER_PORT=9120",
                         "extraEnv[0].name=HERMES_MANAGED_DIR,extraEnv[0].value=/tmp/bypass", "hermes.config.webapp.port=9121"):
            with self.subTest(override=override):
                self.assertNotEqual(self.render("--set", override).returncode, 0)

    def test_external_sources_cannot_hydrate_reserved_environment(self):
        for override in (
            "hermes.config.secrets.onepassword.env.API_SERVER_KEY=op://example/item/key",
            "hermes.config.secrets.onepassword.env.HERMES_PROFILE_NAME=op://example/item/profile",
            "hermes.config.secrets.onepassword.service_account_token_env=CETIRUN_AUTH_KEY",
            "hermes.config.secrets.command.enabled=true",
            "hermes.config.secrets.bitwarden.enabled=true",
            "aiGateway.hermes.keyEnv=EXAMPLE_MODEL_API_KEY,hermes.config.secrets.onepassword.env.EXAMPLE_MODEL_API_KEY=op://example/item/key",
        ):
            with self.subTest(override=override):
                self.assertNotEqual(self.render("--set", override).returncode, 0)
        permitted = self.render("--set", "hermes.config.secrets.onepassword.enabled=true,hermes.config.secrets.onepassword.env.EXAMPLE_SERVICE_TOKEN=op://example/item/token")
        self.assertEqual(permitted.returncode, 0, permitted.stderr)


if __name__ == "__main__":
    unittest.main()
