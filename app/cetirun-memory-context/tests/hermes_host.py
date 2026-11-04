# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Exercise the pinned native host APIs with synthetic files and a loopback model.

Requires separately provisioned Hermes v2026.9.24 sources and dependencies;
never installs packages or reads an existing profile.
"""

import importlib.util
import hashlib
import json
import os
from pathlib import Path
import sys
import threading
import tomllib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest.mock import patch

source = Path(os.environ["MEMORY_HERMES_SOURCE"])
assert tomllib.loads((source / "pyproject.toml").read_text())["project"]["version"] == "0.21.5"
sys.path.insert(0, str(source))
root = Path(os.environ["MEMORY_TEST_ASSETS"])
workspace = os.environ["MEMORY_TEST_WORKSPACE"]
Path(os.environ["HERMES_HOME"]).mkdir()

from hermes_cli import plugins
from hermes_cli.middleware import apply_llm_request_middleware
from agent.runtime_cwd import set_session_cwd
from tools.registry import registry

set_session_cwd(workspace)
spec = importlib.util.spec_from_file_location("memory_native_fixture", root / "adapters/hermes/__init__.py")
adapter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(adapter)
manager = plugins.PluginManager()
manager._discovered = True
manifest = plugins.PluginManifest(name="cetirun-memory-context", version="1", source="bundled", path=str(root / "adapters/hermes"))
adapter.register(plugins.PluginContext(manifest, manager), root=root)
assert registry.get_entry("cetirun_memory", scope=manager.scope_key) is not None
assert registry.get_definitions({"cetirun_memory"})[0]["function"]["name"] == "cetirun_memory"

with patch.object(plugins, "get_plugin_manager", return_value=manager):
    manager.invoke_hook("pre_llm_call", session_id="native-hermes", platform="cli", turn_id="owner-pref",
                        model="fixture", user_message="Remember my preference: use synthetic concise examples")
    request = {"messages": [{"role": "user", "content": "Native history stays unchanged"}]}
    transformed = apply_llm_request_middleware(request, session_id="native-hermes", provider="openai", model="fixture",
                                              api_mode="chat_completions", api_request_id="native-request")
    assert transformed.changed
    assert "synthetic concise examples" in json.dumps(transformed.payload)
    assert "synthetic concise examples" not in json.dumps(request)
    status = json.loads(registry.dispatch("cetirun_memory", {"command": "status", "payload": {}}, session_id="native-hermes"))
    assert status["ok"] and status["data"]["modelEvaluationSupported"]

    from model_tools import handle_function_call

    source_file = Path(workspace) / "synthetic-source.txt"
    source_file.write_text("before")
    note = {"kind": "fact", "title": "Hermes replay fixture", "scope": {"project": workspace}, "body": "Synthetic file claim",
            "temporalClass": "live", "status": "active", "revalidation": {"mode": "on-use"},
            "sources": [{"kind": "file", "ref": str(source_file), "origin": "tool-observed", "assurance": "reported",
                         "version": "sha256:" + hashlib.sha256(b"before").hexdigest()}]}

    def native_tool(command, payload, call_id):
        result = json.loads(handle_function_call("cetirun_memory", {"command": command, "payload": payload},
                           session_id="native-hermes", turn_id="owner-pref", tool_call_id=call_id,
                           enabled_tools=["cetirun_memory"]))
        assert result["ok"], result
        return result

    saved = native_tool("capture", {"note": note}, "stable-native-tool-id")
    replayed = native_tool("capture", {"note": note}, "stable-native-tool-id")
    assert replayed == saved, (saved, replayed)
    ref = saved["data"]["refs"][0]
    native_tool("source", {"ref": ref, "sourceRef": str(source_file)}, "source-before")
    matched = native_tool("query", {"queryTerms": [note["title"]]}, "query-before")
    assert matched["data"]["hits"][0]["review"]["needsRecheck"] is False
    source_file.write_text("after")
    native_tool("source", {"ref": ref, "sourceRef": str(source_file)}, "source-after")
    changed = native_tool("query", {"queryTerms": [note["title"]]}, "query-after")
    assert changed["data"]["hits"][0]["review"]["needsRecheck"] is True
    assert changed["data"]["hits"][0]["review"]["reviewReason"] == "SourceChanged"
    manager.invoke_hook("pre_llm_call", session_id="native-hermes", platform="api_server", turn_id="overlap-a", model="fixture", user_message="First turn")
    manager.invoke_hook("pre_llm_call", session_id="native-hermes", platform="api_server", turn_id="overlap-b", model="fixture", user_message="Second turn")
    manager.invoke_hook("on_session_end", session_id="native-hermes", turn_id="overlap-a", completed=True)
    surviving = json.loads(registry.dispatch("cetirun_memory", {"command": "status", "payload": {}}, session_id="native-hermes"))
    assert surviving["ok"], surviving
    manager.invoke_hook("on_session_end", session_id="native-hermes", turn_id="overlap-b", completed=True)
    closed = json.loads(registry.dispatch("cetirun_memory", {"command": "query", "payload": {"queryTerms": ["fixture"]}}, session_id="native-hermes"))
    assert closed["reason"] == "Cancelled"
    manager.invoke_hook("pre_llm_call", session_id="native-hermes", platform="api_server", turn_id="final-turn", model="fixture", user_message="Final turn")
    manager.invoke_hook("on_session_finalize", session_id="native-hermes")
    finalized = json.loads(registry.dispatch("cetirun_memory", {"command": "status", "payload": {}}, session_id="native-hermes"))
    assert finalized["reason"] == "Cancelled"

from agent.auxiliary_client import set_runtime_main, reset_runtime_main

requests = []
response_status = 200


class ModelFixture(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        requests.append(body)
        assert body.get("metadata", {}).get("tags") == ["cetirun-memory-evaluation", "artifact-contract-v1"]
        assert body.get("tools") is None and body.get("stream") is False
        assert "synthetic concise examples" not in json.dumps(body)
        self.send_response(response_status)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps({"id": "fixture", "object": "chat.completion", "model": "fixture",
                                    "choices": [{"index": 0, "message": {"role": "assistant", "content": '{"total":9}'}, "finish_reason": "stop"}],
                                    "usage": {"total_tokens": 8}}).encode())


server = ThreadingHTTPServer(("127.0.0.1", 0), ModelFixture)
thread = threading.Thread(target=server.serve_forever, daemon=True)
thread.start()
token = set_runtime_main("openai", "fixture", base_url=f"http://127.0.0.1:{server.server_port}/v1", api_key="sk-dummy",
                         api_mode="chat_completions", session_id="native-hermes")
session = adapter.MemorySession(root, {"model": "openai/fixture", "sessionId": "native-hermes",
    "modelRevision": adapter.model_revision("openai", "fixture", f"http://127.0.0.1:{server.server_port}/v1", "chat_completions")})
ticket = {"system": "Only synthetic JSON fixtures", "prompt": "Return a fixture total", "maxTokens": 512, "timeoutSeconds": 2}
try:
    result = adapter.bounded_completion(ticket, session)
    assert result["outcome"] == "completed" and result["requests"] == 1
    assert len(requests) == 1
    response_status = 500
    try:
        adapter.bounded_completion(ticket, session)
        raise AssertionError("server error was accepted")
    except Exception as error:
        assert not isinstance(error, AssertionError)
    assert len(requests) == 2, "native SDK retried a failed evaluation"
    session.binding["sessionId"] = "other-session"
    try:
        adapter.bounded_completion(ticket, session)
        raise AssertionError("cross-session model route accepted")
    except ValueError as error:
        assert str(error) == "EvaluationRouteUnavailable"
    assert len(requests) == 2
finally:
    reset_runtime_main(token)
    server.shutdown()
    server.server_close()
    thread.join()

print("Pinned Hermes host checks passed")
