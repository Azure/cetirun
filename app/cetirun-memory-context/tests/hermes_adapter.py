# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Offline native adapter integration; uses only synthetic HOME and host APIs."""

import importlib.util
import json
import os
from pathlib import Path
import sys
import types

root = Path(os.environ["MEMORY_TEST_ASSETS"])
workspace = os.environ["MEMORY_TEST_WORKSPACE"]
runtime_cwd = types.ModuleType("agent.runtime_cwd")
runtime_cwd.resolve_agent_cwd = lambda: Path(workspace)
sys.modules["agent"] = types.ModuleType("agent")
sys.modules["agent.runtime_cwd"] = runtime_cwd
session_context = types.ModuleType("gateway.session_context")
session_context.get_session_env = lambda name: "hermes-fixture" if name == "HERMES_SESSION_ID" else ""
sys.modules["gateway"] = types.ModuleType("gateway")
sys.modules["gateway.session_context"] = session_context
spec = importlib.util.spec_from_file_location("memory_plugin", root / "adapters/hermes/__init__.py")
plugin = importlib.util.module_from_spec(spec)
spec.loader.exec_module(plugin)


class Host:
    def __init__(self):
        self.hooks, self.middleware, self.tools, self.sections = {}, {}, {}, {}

    def register_hook(self, name, callback):
        self.hooks[name] = callback

    def register_middleware(self, name, callback):
        self.middleware[name] = callback

    def register_tool(self, **kwargs):
        self.tools[kwargs["name"]] = kwargs

    def register_system_prompt_section(self, name, content, **kwargs):
        self.sections[name] = content


host = Host()
plugin.register(host, root=root)
assert host.sections["cetirun-memory-context"] == (Path.home() / ".agents/AGENTS.md").read_text()
host.hooks["pre_llm_call"](session_id="hermes-fixture", platform="cli", turn_id="turn-1", user_message="Fixture", model="model")
tool = host.tools["cetirun_memory"]["handler"]
note = {"kind": "fact", "title": "Hermes private fixture", "scope": {"project": workspace}, "body": "Private synthetic body",
        "temporalClass": "historical", "status": "active", "revalidation": {"mode": "on-change"},
        "sources": [{"kind": "fixture", "ref": "source-fixture", "origin": "external-claim", "assurance": "reported", "observedAt": "2026-10-08T00:00:00Z"}]}
saved = json.loads(tool({"command": "capture", "payload": {"note": note}}, tool_call_id="write-1"))
assert saved["ok"], saved
original = {"messages": [{"role": "user", "content": "Original history"}], "model": "model"}
first = host.middleware["llm_request"](original, session_id="hermes-fixture", provider="fixture", model="model", api_mode="chat_completions", api_request_id="request-1")
assert "Hermes private fixture" in first["request"]["messages"][-1]["content"]
assert len(original["messages"]) == 1
assert "Hermes private fixture" not in host.sections["cetirun-memory-context"]
host.hooks["post_tool_call"](session_id="hermes-fixture", tool_name="fixture_tool", tool_call_id="failure-1", status="error", result="Do not persist raw outputs")
prepared = json.loads(tool({"command": "prepare", "payload": {}}))
assert prepared["data"]["observations"][0]["assurance"] == "adapter-observed"
assert "Do not persist raw outputs" not in json.dumps(prepared)
host.hooks["pre_llm_call"](session_id="hermes-fixture", platform="cli", turn_id="turn-2", user_message="/memory no-capture", model="model")
denied = json.loads(tool({"command": "capture", "payload": {"note": note}}, tool_call_id="denied-write"))
assert denied["reason"] == "CaptureProhibited", denied
host.hooks["pre_llm_call"](session_id="hermes-fixture", platform="slack", turn_id="turn-3", user_message="group", model="model")
shared = host.middleware["llm_request"](original, session_id="hermes-fixture", provider="fixture", model="model")
assert "Hermes private fixture" not in json.dumps(shared)
host.hooks["on_session_end"](session_id="hermes-fixture", completed=True)
assert json.loads(tool({"command": "query", "payload": {"queryTerms": ["Hermes private fixture"]}}))["reason"] == "Cancelled"
print("Hermes native adapter checks passed")
