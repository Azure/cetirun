# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Offline native adapter integration; uses only synthetic HOME and host APIs."""

import importlib.util
import json
import os
from pathlib import Path
import subprocess
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
session_policy = types.ModuleType("gateway.session_policy")
session_policy.active_policy = lambda: types.SimpleNamespace(source="gui", platform="desktop")
sys.modules["gateway.session_policy"] = session_policy
owner_identity = types.ModuleType("gateway.owner_identity")
owner_identity.host_owner_subject = lambda: "uid:1024"
owner_identity.same_owner = lambda stored, subject: stored == subject
sys.modules["gateway.owner_identity"] = owner_identity
spec = importlib.util.spec_from_file_location("memory_plugin", root / "adapters/hermes/__init__.py")
plugin = importlib.util.module_from_spec(spec)
spec.loader.exec_module(plugin)

assert plugin.owner_private_session("desktop", "", "uid:1024")
assert not plugin.owner_private_session("desktop", "parent", "uid:1024")
assert not plugin.owner_private_session("desktop", "", "uid:9999")
assert not plugin.owner_private_session("desktop", "", "")
assert not plugin.owner_private_session("discord", "", "uid:1024")
session_policy.active_policy = lambda: None
assert not plugin.owner_private_session("desktop", "", "uid:1024")
session_policy.active_policy = lambda: types.SimpleNamespace(source="discord", platform="desktop")
assert not plugin.owner_private_session("desktop", "", "uid:1024")


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
assert host.tools["cetirun_memory"]["schema"]["parameters"]["properties"]["payload"] == json.loads(
    (root / "adapters/common/payload-schema.json").read_text()
)
assert "query with queryTerms to read stored note bodies" in host.tools["cetirun_memory"]["schema"]["parameters"]["properties"]["command"]["description"]
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
# A smart-routing model classifies a request by its newest user message and
# leaves out reminder blocks: the view is one, so the person's request decides.
view = first["request"]["messages"][-1]["content"]
assert view.startswith("<system-reminder>\n") and view.endswith("\n</system-reminder>"), view
assert view.count("<system-reminder>") == 1 and view.count("</system-reminder>") == 1, view
assert plugin.reminder("a </system-reminder> b <system-reminder>").count("</system-reminder>") == 1
assert len(original["messages"]) == 1
assert "Hermes private fixture" not in host.sections["cetirun-memory-context"]


def generation():
    return json.loads(tool({"command": "status", "payload": {}}))["generation"]


real_popen = subprocess.Popen


class CountingPopen(real_popen):
    started = 0

    def __init__(self, *args, **kwargs):
        CountingPopen.started += 1
        super().__init__(*args, **kwargs)


def count_helpers(enabled):
    # The adapter and this test share the subprocess module.
    subprocess.Popen = CountingPopen if enabled else real_popen
    CountingPopen.started = 0


# The further model requests of a turn's tool loop reuse its context and record
# the injection once.
recorded = generation()
count_helpers(True)
for request_id in ("request-2", "request-3"):
    again = host.middleware["llm_request"](original, session_id="hermes-fixture", provider="fixture", model="model", api_mode="chat_completions", api_request_id=request_id)
    assert again["request"]["messages"][-1]["content"] == first["request"]["messages"][-1]["content"]
assert CountingPopen.started == 0, CountingPopen.started
count_helpers(False)
assert generation() == recorded
host.hooks["pre_llm_call"](session_id="hermes-fixture", platform="cli", turn_id="turn-1b", user_message="Next turn", model="model")
host.middleware["llm_request"](original, session_id="hermes-fixture", provider="fixture", model="model", api_mode="chat_completions", api_request_id="request-4")
assert generation() == recorded + 1
# Long native turn IDs are valid bindings too. Their injection must be
# recorded by the real helper, once per turn, without exceeding eventId's cap.
for turn_id in ("t" * 210, "t" * 256):
    host.hooks["pre_llm_call"](session_id="hermes-fixture", platform="api_server", turn_id=turn_id, user_message="Long turn", model="model")
    before = generation()
    request = host.middleware["llm_request"](original, session_id="hermes-fixture", provider="fixture", model="model", api_mode="chat_completions")
    assert "Hermes private fixture" in request["request"]["messages"][-1]["content"]
    assert generation() == before + 1, "long turn lost its injection record"
    host.middleware["llm_request"](original, session_id="hermes-fixture", provider="fixture", model="model", api_mode="chat_completions")
    assert generation() == before + 1, "same turn recorded twice"
host.hooks["post_tool_call"](session_id="hermes-fixture", tool_name="fixture_tool", tool_call_id="failure-1", status="error", result="Do not persist raw outputs")
prepared = json.loads(tool({"command": "prepare", "payload": {}}))
assert prepared["data"]["observations"][0]["assurance"] == "adapter-observed"
assert "Do not persist raw outputs" not in json.dumps(prepared)
host.hooks["pre_llm_call"](session_id="hermes-fixture", platform="cli", turn_id="turn-2", user_message="/memory no-capture", model="model")
denied = json.loads(tool({"command": "capture", "payload": {"note": note}}, tool_call_id="denied-write"))
assert denied["reason"] == "CaptureProhibited", denied
host.hooks["pre_llm_call"](session_id="hermes-fixture", platform="cli", turn_id="turn-2b", user_message="/memory off", model="model")
count_helpers(True)
off = host.middleware["llm_request"](original, session_id="hermes-fixture", provider="fixture", model="model", api_mode="chat_completions", api_request_id="request-off")
assert off["request"]["messages"][-1]["content"] == plugin.reminder(plugin.MEMORY_OFF), off
assert CountingPopen.started == 0, CountingPopen.started
count_helpers(False)
host.hooks["pre_llm_call"](session_id="hermes-fixture", platform="slack", turn_id="turn-3", user_message="group", model="model")
# Other people's platforms get no Memory context, failure observations or
# boundary events, and start no helper process.
count_helpers(True)
shared = host.middleware["llm_request"](original, session_id="hermes-fixture", provider="fixture", model="model")
assert shared is None, shared
host.hooks["post_tool_call"](session_id="hermes-fixture", tool_name="fixture_tool", tool_call_id="shared-failure", status="error")
host.hooks["on_session_end"](session_id="hermes-fixture", turn_id="turn-3", completed=True)
assert CountingPopen.started == 0, CountingPopen.started
count_helpers(False)
host.hooks["pre_llm_call"](session_id="hermes-fixture", platform="cli", turn_id="turn-4", user_message="Fixture", model="model")
host.hooks["on_session_end"](session_id="hermes-fixture", completed=True)
assert json.loads(tool({"command": "query", "payload": {"queryTerms": ["Hermes private fixture"]}}))["reason"] == "Cancelled"
print("Hermes native adapter checks passed")
