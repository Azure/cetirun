# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""The Hermes plugin for the CetiRun AI Gateway's errors, and the chart and image that install it."""

import importlib.util
import json
import shutil
import subprocess
import unittest
from pathlib import Path

import yaml


CHART = Path(__file__).resolve().parents[1]
PLUGIN = CHART / "agents/hermes/plugins/cetirun-gateway-errors"
NAME = "cetirun-gateway-errors"
# The Gateway's codes when a provider refused its credentials (cetirun_ai_gateway/guard.py).
CODES = ("cetirun_provider_credentials", "cetirun_copilot_credentials")
HOOK = "transform_api_error_classification"


def load_plugin():
    spec = importlib.util.spec_from_file_location("cetirun_gateway_errors_under_test", PLUGIN / "__init__.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class FakeContext:
    def __init__(self):
        self.hooks = {}

    def register_hook(self, name, callback):
        self.hooks.setdefault(name, []).append(callback)


def hook_arguments(**changed):
    """The arguments that Hermes 0.22.7 gives the hook for a 502 of the Gateway."""
    return {"provider": "cetirun-gateway", "model": "gpt-6", "status_code": 502, "error_type": "InternalServerError",
            "error_code": CODES[0], "error_message": "error code: 502 cetirun ai gateway: ...",
            "error_body": {"message": "CetiRun AI Gateway: ...", "code": CODES[0]}, "error": None,
            "approx_tokens": 1200, "context_length": 200000, "num_messages": 3, **changed}


class GatewayErrorsPluginTests(unittest.TestCase):
    def setUp(self):
        self.plugin = load_plugin()

    def test_refused_credentials_end_the_turn(self):
        for code in CODES:
            with self.subTest(code=code):
                self.assertEqual(self.plugin.classify(**hook_arguments(error_code=code)),
                                 {"reason": "unknown", "retryable": False})

    def test_other_errors_keep_the_classification_of_hermes(self):
        for code in ("", "502", "server_error", "internal_server_error", "budget_exceeded", "api_error",
                     "cetirun_provider_credentials_other", None, 502):
            with self.subTest(code=code):
                self.assertIsNone(self.plugin.classify(**hook_arguments(error_code=code)))

    def test_the_plugin_registers_only_its_hook(self):
        context = FakeContext()
        self.plugin.register(context)
        self.assertEqual(context.hooks, {HOOK: [self.plugin.classify]})

    def test_the_manifest_names_the_plugin(self):
        manifest = yaml.safe_load((PLUGIN / "plugin.yaml").read_text(encoding="utf-8"))
        self.assertEqual((manifest["name"], manifest["requires_hermes"]), (NAME, ">=0.22.7"))

    def test_the_image_installs_the_plugin_and_checks_it_with_its_hermes(self):
        dockerfile = (CHART / "agents/hermes/Dockerfile").read_text(encoding="utf-8")
        install = f"COPY agents/hermes/plugins/{NAME}/ /usr/local/lib/hermes-agent/plugins/{NAME}/"
        check = "/usr/local/lib/hermes-agent/cetirun_compat/check_gateway_errors.py"
        self.assertIn(install, dockerfile)
        self.assertIn(f"COPY agents/hermes/compat/check_gateway_errors.py {check}", dockerfile)
        # The check runs with the image's Hermes, as the user of the image, after the files are installed.
        run = dockerfile.index(f'HERMES_HOME="$check_home" /usr/local/bin/hermes-python \\\n      {check}')
        self.assertGreater(run, dockerfile.rindex("USER vibe"))
        self.assertGreater(run, dockerfile.index(install))


@unittest.skipUnless(shutil.which("helm"), "requires Helm")
class GatewayErrorsChartTests(unittest.TestCase):
    def render(self, agent_type, *extra):
        command = [
            "helm", "template", "cetirun", str(CHART),
            "--set", f"agentType={agent_type}",
            "--set-string", "secrets.model=lint-model",
            "--set-string", "secrets.apiBase=http://gateway.example.svc.cluster.local:4000/v1",
            "--set-string", "aiGateway.keySecretName=lint-model-key",
            "--set-string", "hermes.env.HERMES_WEBAPP_PUBLIC_URL=https://agents.example.com/ui/cetirun",
            "--set-json", 'aiGateway.models=[{"id":"lint-model","name":"Lint model"}]',
            *extra,
        ]
        if agent_type == "openclaw":
            command.extend(["--set-string", "secrets.openclawGatewayToken=lint-token"])
        result = subprocess.run(command, capture_output=True, text=True, check=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        data = {}
        for document in yaml.safe_load_all(result.stdout):
            if document and document.get("kind") == "ConfigMap":
                data.update(document.get("data") or {})
        return data

    def test_every_hermes_agent_enables_the_plugin(self):
        for extra in ((), ("--set", "logger.enabled=true"), ("--set", "logger.enabled=false"),
                      ("--set-json", 'hermes.config={"plugins":{"enabled":["home-plugin"]}}')):
            with self.subTest(values=extra):
                config = yaml.safe_load(self.render("hermes", *extra)["hermes-config.yaml"])
                enabled = config["plugins"]["enabled"]
                self.assertEqual(enabled.count(NAME), 1)
                if extra and extra[0] == "--set-json":
                    self.assertEqual(enabled[0], "home-plugin")

    def test_openclaw_does_not_name_the_plugin(self):
        self.assertNotIn(NAME, json.dumps(self.render("openclaw")))


if __name__ == "__main__":
    unittest.main()
