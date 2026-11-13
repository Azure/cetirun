# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""The CetiRun session log: runtime plugins, logger, and rendered configuration."""

import importlib.util
import json
import logging
import os
import shutil
import stat
import subprocess
import sys
import tempfile
import types
import unittest
from pathlib import Path
from unittest import mock

import yaml


CHART = Path(__file__).resolve().parents[1]
HERMES_PLUGIN = CHART / "agents/hermes/plugins/cetirun-session-log/__init__.py"
OPENCLAW_PLUGIN = "/usr/local/lib/cetirun/openclaw-plugins/cetirun-session-log"
SECRET = "sk-synthetic0000000000000000000000"


def load_hermes_plugin():
    spec = importlib.util.spec_from_file_location("cetirun_session_log_under_test", HERMES_PLUGIN)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def hermes_redaction(calls):
    """A stand-in for Hermes's agent.redact that records its options."""
    def redact_sensitive_text(text, **options):
        calls.append(options)
        return text.replace(SECRET, "sk-syn...0000")

    package = types.ModuleType("agent")
    module = types.ModuleType("agent.redact")
    module.redact_sensitive_text = redact_sensitive_text
    package.redact = module
    return {"agent": package, "agent.redact": module}


class FakeContext:
    def __init__(self):
        self.hooks = {}

    def register_hook(self, name, callback):
        self.hooks.setdefault(name, []).append(callback)


def read_records(path):
    if not path.exists():
        return []
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line]


@unittest.skipUnless(shutil.which("node"), "requires Node for the OpenClaw plugin and the logger")
class SessionLogNodeTests(unittest.TestCase):
    def test_openclaw_exporter_and_logger(self):
        result = subprocess.run(
            [shutil.which("node"), "--test", "tests/session_log/exporter.test.mjs", "tests/session_log/logger.test.mjs"],
            cwd=CHART, capture_output=True, text=True, timeout=300, check=False,
        )
        self.assertEqual(result.returncode, 0, result.stdout[-4000:] + result.stderr[-2000:])


class HermesSessionLogTests(unittest.TestCase):
    def setUp(self):
        # main() of hermes-runtime.py, which another test calls, turns logging
        # off for the whole process.
        previous = logging.root.manager.disable
        logging.disable(logging.NOTSET)
        self.addCleanup(logging.disable, previous)
        self.plugin = load_hermes_plugin()
        self.directory = Path(tempfile.mkdtemp(prefix="cetirun-session-log-"))
        self.addCleanup(shutil.rmtree, self.directory, True)
        self.log = self.directory / "cetirun-session-log"
        self.calls = []

    def register(self):
        context = FakeContext()
        with mock.patch.dict(sys.modules, hermes_redaction(self.calls)), \
                mock.patch.object(self.plugin, "LOG_DIR", self.log):
            self.plugin.register(context)
        return context

    # The keyword arguments of Hermes 0.22.7: agent/turn_finalizer.py and model_tools.py.
    def turn(self, context, **overrides):
        arguments = dict(
            session_id="session-1", task_id="task-1", turn_id="turn-1",
            user_message=f"deploy with key {SECRET}", assistant_response="Deployed.",
            conversation_history=[{"role": "system", "content": "system prompt marker"}],
            model="gpt-test", platform="api_server", side_question_token=None,
        )
        arguments.update(overrides)
        for hook in context.hooks["post_llm_call"]:
            hook(**arguments)

    def tool(self, context, **overrides):
        arguments = dict(
            tool_name="terminal", args={"command": f"login --token {SECRET}"}, result='{"output": "pod-a Running"}',
            task_id="task-1", session_id="session-1", tool_call_id="call-1", turn_id="turn-1", api_request_id="",
            duration_ms=12, status="ok", error_type=None, error_message=None, middleware_trace=[],
        )
        arguments.update(overrides)
        for hook in context.hooks["post_tool_call"]:
            hook(**arguments)

    def test_records_of_a_turn_and_a_tool_call(self):
        context = self.register()
        self.assertEqual(sorted(context.hooks), ["post_llm_call", "post_tool_call"])
        self.tool(context)
        self.turn(context)
        records = read_records(self.log / "hermes.jsonl")
        self.assertEqual([record["id"] for record in records], [
            "hermes:session-1:call-1:call", "hermes:session-1:call-1:result",
            "hermes:session-1:turn-1:user", "hermes:session-1:turn-1:assistant",
        ])
        call, result, user, assistant = records
        self.assertEqual([call["kind"], call["tool"], call["toolCallId"], call["runId"]], ["tool_call", "terminal", "call-1", "turn-1"])
        self.assertEqual(json.loads(call["content"]), {"command": "login --token sk-syn...0000"})
        self.assertEqual([result["kind"], result["status"], result["isError"], result["content"]],
                         ["tool_result", "ok", False, '{"output": "pod-a Running"}'])
        self.assertEqual([user["kind"], user["channel"], user["content"]], ["user", "api_server", "deploy with key sk-syn...0000"])
        self.assertEqual([assistant["kind"], assistant["model"], assistant["content"]], ["assistant", "gpt-test", "Deployed."])
        for record in records:
            self.assertEqual([record["v"], record["runtime"], record["sessionId"], record["truncated"]], [1, "hermes", "session-1", False])
            self.assertTrue(record["ts"].endswith("Z"))
        text = (self.log / "hermes.jsonl").read_text(encoding="utf-8")
        self.assertNotIn(SECRET, text)
        self.assertNotIn("system prompt marker", text)
        self.assertTrue(self.calls)
        self.assertTrue(all(options == {"force": True, "redact_url_credentials": True} for options in self.calls))
        if os.name == "posix":
            self.assertEqual(stat.S_IMODE(self.log.stat().st_mode), 0o700)
            self.assertEqual(stat.S_IMODE((self.log / "hermes.jsonl").stat().st_mode), 0o600)

    def test_failed_tool_calls_and_missing_ids(self):
        context = self.register()
        self.tool(context, tool_call_id="", turn_id="", status="error", result='{"error": "denied"}')
        call, result = read_records(self.log / "hermes.jsonl")
        self.assertEqual(call["id"].rsplit(":", 1)[0], result["id"].rsplit(":", 1)[0])
        self.assertEqual([result["status"], result["isError"]], ["error", True])
        for record in (call, result):
            self.assertNotIn("toolCallId", record)
            self.assertNotIn("runId", record)

    def test_a_multimodal_message_keeps_its_text(self):
        context = self.register()
        self.turn(context, user_message=[{"type": "text", "text": "look at this"},
                                         {"type": "image_url", "image_url": {"url": "data:image/png;base64,aGVsbG8="}}],
                  assistant_response=None)
        [user] = read_records(self.log / "hermes.jsonl")
        self.assertEqual(user["content"], "look at this\n[image_url]")

    def test_nothing_without_a_session(self):
        context = self.register()
        self.turn(context, session_id="")
        self.tool(context, session_id=None)
        self.assertFalse((self.log / "hermes.jsonl").exists())

    def test_a_long_content_is_cut_to_the_line_limit(self):
        context = self.register()
        self.turn(context, user_message="\u00e4" * 40000, assistant_response=None)
        line = (self.log / "hermes.jsonl").read_bytes()
        self.assertLessEqual(len(line), self.plugin.MAX_LINE_BYTES)
        record = json.loads(line)
        self.assertTrue(record["truncated"])
        self.assertGreater(len(record["content"]), 1000)
        self.assertEqual(set(record["content"]), {"\u00e4"})

    def test_the_file_rotates_and_keeps_one_predecessor(self):
        exporter = self.plugin.Exporter(self.log, lambda text: text, max_file_bytes=2000)
        for index in range(40):
            exporter.on_llm(session_id="session-1", turn_id=f"turn-{index:02}", user_message=f"question {index}")
        rotated, current = self.log / "hermes.jsonl.1", self.log / "hermes.jsonl"
        self.assertLessEqual(rotated.stat().st_size, 2000)
        self.assertLessEqual(current.stat().st_size, 2000)
        ids = [record["id"] for record in read_records(rotated) + read_records(current)]
        self.assertEqual(ids[-1], "hermes:session-1:turn-39:user")
        self.assertEqual(ids, sorted(ids))
        numbers = [int(record_id.split("turn-")[1].split(":")[0]) for record_id in ids]
        self.assertEqual(numbers, list(range(numbers[0], 40)))

    def test_a_failure_is_logged_and_never_raised(self):
        def broken(text):
            raise RuntimeError("synthetic failure")

        exporter = self.plugin.Exporter(self.log, broken)
        with self.assertLogs(self.plugin.logger, level="WARNING") as logs:
            exporter.on_llm(session_id="session-1", turn_id="turn-1", user_message="hello")
            exporter.on_tool(session_id="session-1", tool_name="terminal", args={}, result="", tool_call_id="call-1")
        self.assertEqual(len(logs.records), 2)
        self.assertFalse((self.log / "hermes.jsonl").exists())

    def test_no_hooks_without_hermes_redaction(self):
        context = FakeContext()
        with mock.patch.dict(sys.modules, {"agent": None, "agent.redact": None}), \
                mock.patch.object(self.plugin, "LOG_DIR", self.log), \
                self.assertLogs(self.plugin.logger, level="WARNING"):
            self.plugin.register(context)
        self.assertEqual(context.hooks, {})


@unittest.skipUnless(shutil.which("helm"), "requires Helm")
class SessionLogChartTests(unittest.TestCase):
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
        return [document for document in yaml.safe_load_all(result.stdout) if document]

    @staticmethod
    def configmap_data(documents):
        data = {}
        for document in documents:
            if document.get("kind") == "ConfigMap":
                data.update(document.get("data") or {})
        return data

    @staticmethod
    def containers(documents):
        """The containers of the StatefulSet by name; the runtime's container comes first."""
        workload = next(document for document in documents if document.get("kind") == "StatefulSet")
        return {container["name"]: container for container in workload["spec"]["template"]["spec"]["containers"]}

    def registered(self, agent_type, data):
        if agent_type == "hermes":
            return "cetirun-session-log" in (yaml.safe_load(data["hermes-config.yaml"]).get("plugins") or {}).get("enabled", [])
        plugins = json.loads(data["openclaw.json"]).get("plugins") or {}
        loaded = OPENCLAW_PLUGIN in (plugins.get("load") or {}).get("paths", [])
        allowed = "cetirun-session-log" in plugins.get("allow", [])
        enabled = ((plugins.get("entries") or {}).get("cetirun-session-log") or {}).get("enabled") is True
        self.assertEqual({loaded, allowed, enabled}, {loaded}, "the OpenClaw plugin is registered only in part")
        return loaded

    def test_the_logger_and_its_plugin_go_together(self):
        for agent_type in ("openclaw", "hermes"):
            for enabled in (True, False):
                with self.subTest(agent_type=agent_type, logger=enabled):
                    documents = self.render(agent_type, "--set", f"logger.enabled={str(enabled).lower()}")
                    data = self.configmap_data(documents)
                    self.assertEqual(self.registered(agent_type, data), enabled)
                    containers = self.containers(documents)
                    self.assertEqual("logger" in containers, enabled)
                    if enabled:
                        self.assertEqual(data["session-logger.sh"], (CHART / "configs/session-logger.sh").read_text(encoding="utf-8"))
                        self.assertEqual(data["session-logger.mjs"], (CHART / "configs/session-logger.mjs").read_text(encoding="utf-8"))

    def test_the_plugins_write_where_the_logger_reads(self):
        hermes_directory = load_hermes_plugin().LOG_DIR
        for agent_type in ("openclaw", "hermes"):
            with self.subTest(agent_type=agent_type):
                containers = self.containers(self.render(agent_type))
                logger = containers["logger"]
                self.assertEqual(logger["command"], ["/bin/sh", "/etc/cetirun/session-logger.sh"])
                home = next(mount for mount in logger["volumeMounts"] if mount["name"] == "data")
                self.assertEqual([home["mountPath"], home.get("readOnly")], [f"/home/vibe/.{agent_type}", True])
                if agent_type == "openclaw":
                    runtime = next(iter(containers.values()))
                    environment = {item["name"]: item.get("value") for item in runtime["env"]}
                    self.assertEqual(environment["OPENCLAW_STATE_DIR"], home["mountPath"])
                else:
                    self.assertEqual(str(hermes_directory.parent), home["mountPath"])


if __name__ == "__main__":
    unittest.main()
