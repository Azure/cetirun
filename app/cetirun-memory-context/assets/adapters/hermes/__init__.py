# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Pinned Hermes plugin: request-only context, scoped tools, and minimal events."""

import asyncio
from contextvars import ContextVar
import hashlib
import json
import os
from pathlib import Path
import subprocess
import threading
import time
import uuid

ADAPTER = "memory-adapter/v1"
NATIVE_VERSION = "0.21.5"
COMMANDS = ("status", "query", "capture", "prepare", "commit", "propose", "check", "advance",
            "select", "retire", "revise", "record-source-check", "record-use", "trace", "lint", "source", "evaluate")


def model_revision(provider, model, base_url, api_mode):
    return "sha256:" + hashlib.sha256(json.dumps([provider, model, base_url or "", api_mode], separators=(",", ":")).encode()).hexdigest()


def load_assets(root):
    manifest_path = root / "manifest.json"
    if manifest_path.is_symlink() or not manifest_path.is_file() or manifest_path.stat().st_size > 256 * 1024 or manifest_path.stat().st_nlink != 1:
        raise ValueError("MemoryManifestInvalid")
    manifest = json.loads(manifest_path.read_text())
    if manifest.get("contractVersion") != "memory-context/v1" or manifest.get("helperABI") != 1:
        raise ValueError("MemoryContractUnsupported")
    files = manifest.get("files", [])
    if not isinstance(files, list) or len(files) > 512:
        raise ValueError("MemoryAssetLimit")
    seen, total = set(), 0
    for item in files:
        relative = item["path"]
        if not isinstance(relative, str) or not relative or relative.startswith("/") or "\\" in relative or any(part in ("", ".", "..") for part in relative.split("/")) or relative in seen:
            raise ValueError("MemoryAssetUnsafe")
        seen.add(relative)
        current = root
        for part in relative.split("/"):
            current = current / part
            if current.is_symlink():
                raise ValueError("MemoryAssetUnsafe")
        stat = current.stat()
        total += stat.st_size
        if not current.is_file() or stat.st_nlink != 1 or total > 32 * 1024 * 1024 or item.get("mode") not in (0o444, 0o555):
            raise ValueError("MemoryAssetUnsafe")
        if "sha256:" + hashlib.sha256(current.read_bytes()).hexdigest() != item["digest"]:
            raise ValueError("MemoryAssetMismatch")
    if not {"AGENTS.md", "skills/cetirun-memory/SKILL.md", "bin/cetirun-memory"}.issubset(seen):
        raise ValueError("MemoryAssetMissing")
    canonical = (root / "AGENTS.md").read_text()
    if len(canonical.encode()) > 2048:
        raise ValueError("MemoryContractTooLarge")
    return canonical


class MemorySession:
    def __init__(self, root, binding):
        self.root, self.binding = root, binding
        self.cancelled = threading.Event()
        self.checks, self.injected = [], []
        self.loop = self.task = None
        self.model_lock = threading.Lock()

    def stop(self):
        self.cancelled.set()
        if self.loop and self.task and not self.loop.is_closed():
            self.loop.call_soon_threadsafe(self.task.cancel)

    def call(self, event, payload=None, command="", event_id=None):
        if self.cancelled.is_set():
            return {"ok": False, "reason": "Cancelled"}
        request = {"binding": {**self.binding, "currentSourceChecks": self.checks}, "event": event,
                   "payload": payload or {}, "command": command, "eventId": event_id or str(uuid.uuid4())}
        raw = json.dumps(request).encode()
        if len(raw) > 128 * 1024:
            return {"ok": False, "reason": "LimitReached"}
        process = None
        try:
            process = subprocess.Popen([str(self.root / "bin/cetirun-memory"), "native-event"],
                                       stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
            deadline = time.monotonic() + 5
            first = raw
            while True:
                try:
                    output, _ = process.communicate(first, timeout=0.1)
                    break
                except subprocess.TimeoutExpired:
                    first = None
                    if self.cancelled.is_set() or time.monotonic() >= deadline:
                        process.kill()
                        process.communicate()
                        return {"ok": False, "reason": "CancelledOrTimedOut"}
            if len(output) > 256 * 1024:
                return {"ok": False, "reason": "LimitReached"}
            result = json.loads(output)
            if bool(result.get("ok")) != (process.returncode == 0):
                raise ValueError("MemoryProtocolError")
            return result
        except (OSError, ValueError):
            return {"ok": False, "reason": "MemoryUnavailable"}
        finally:
            if process and process.poll() is None:
                process.kill()
                process.wait()

    def context(self):
        result = self.call("context")
        self.injected = result.get("data", {}).get("refs", []) if result.get("ok") else []
        return result["data"]["context"] if result.get("ok") else "CetiRun Memory: " + result.get("reason", "unavailable") + ". Do not read or save private memory."

    def record_injection(self, request_id):
        if self.injected and not self.binding["noCapture"]:
            self.call("injected", {"refs": self.injected}, event_id=request_id)
        self.injected = []

    def tool(self, args, call_id, executor):
        if set(args) - {"command", "payload"} or args.get("command") not in COMMANDS or not isinstance(args.get("payload"), dict):
            return {"ok": False, "reason": "InvalidInput"}
        command, payload = args["command"], args["payload"]
        if command == "evaluate":
            return self.evaluate(payload, call_id, executor)
        if command == "propose" and payload.get("procedure", {}).get("kind") == "playbook":
            payload = json.loads(json.dumps(payload))
            payload["procedure"]["compatibility"] = {"helperABI": "1", "adapter": ADAPTER,
                                                    "nativeVersion": NATIVE_VERSION, "model": self.binding["model"]}
            if self.binding.get("modelRevision"):
                payload["procedure"]["compatibility"]["modelRevision"] = self.binding["modelRevision"]
        result = self.call("tool", payload, command, call_id)
        if command == "source" and result.get("ok"):
            latest = result["data"]
            self.checks = [check for check in self.checks
                           if (check["check"]["sourceRef"], check["check"]["targetRef"]["id"]) !=
                           (latest["check"]["sourceRef"], latest["check"]["targetRef"]["id"])]
            self.checks = [*self.checks, latest][-8:]
        if command == "query" and result.get("ok") and not self.binding["noCapture"]:
            refs = [hit["ref"] for hit in result["data"].get("hits", [])]
            if refs:
                self.call("injected", {"refs": refs}, event_id=call_id + "-result")
        return result

    def evaluate(self, payload, call_id, executor):
        if not self.binding["modelEvaluation"] or not self.model_lock.acquire(blocking=False):
            return {"ok": False, "reason": "EvaluationUnavailable"}
        try:
            return self._evaluate(payload, call_id, executor)
        finally:
            self.model_lock.release()

    def _evaluate(self, payload, call_id, executor):
        reserved = self.call("evaluation-next", payload, event_id=call_id)
        if not reserved.get("ok"):
            return reserved
        ticket = reserved["data"]
        if "call" not in ticket:
            return reserved
        try:
            result = executor(ticket, self)
        except (Exception, asyncio.CancelledError):
            result = {"output": "", "outcome": "unknown", "requests": 1, "model": self.binding["model"]}
        return self.call("evaluation-result", {"id": ticket["call"]["id"], **result}, event_id=call_id + "-result")


def bounded_completion(ticket, session):
    """Use Hermes' current route preparation, with SDK retries disabled.

    This deliberately avoids auxiliary_client.call_llm's recovery/fallback
    ladder. Other native transports are rejected before a model dispatch.
    """
    async def run():
        from agent.auxiliary_client import _plan_aux_call, _RUNTIME_MAIN_CONTEXT
        from openai import AsyncOpenAI
        session.loop, session.task = asyncio.get_running_loop(), asyncio.current_task()
        if session.cancelled.is_set():
            raise asyncio.CancelledError()
        runtime = _RUNTIME_MAIN_CONTEXT.get()
        if not isinstance(runtime, dict) or runtime.get("session_id") != session.binding["sessionId"] or runtime.get("provider", "") + "/" + runtime.get("model", "") != session.binding["model"]:
            raise ValueError("EvaluationRouteUnavailable")
        if model_revision(runtime["provider"], runtime["model"], runtime.get("base_url"), runtime.get("api_mode")) != session.binding.get("modelRevision"):
            raise ValueError("EvaluationRouteChanged")
        prepared, _, _ = _plan_aux_call(
            None, async_mode=True, provider=runtime["provider"], model=runtime["model"],
            base_url=runtime.get("base_url"), api_key=runtime.get("api_key"),
            main_runtime=runtime, messages=[{"role": "system", "content": ticket["system"]},
                                        {"role": "user", "content": ticket["prompt"]}],
            temperature=0, max_tokens=ticket["maxTokens"], tools=None, timeout=ticket["timeoutSeconds"],
            extra_body=None, reasoning_config=None, extra_headers=None, api_mode=runtime.get("api_mode"), route_info=None,
        )
        model = runtime["provider"] + "/" + prepared.final_model
        if not isinstance(prepared.client, AsyncOpenAI) or model != session.binding["model"] or not runtime.get("base_url") or str(prepared.client.base_url).rstrip("/") != runtime["base_url"].rstrip("/"):
            raise ValueError("EvaluationRouteUnsupported")
        kwargs = {**prepared.kwargs, "stream": False}
        kwargs.pop("tools", None)
        kwargs.pop("tool_choice", None)
        response = await asyncio.wait_for(
            prepared.client.with_options(max_retries=0, timeout=ticket["timeoutSeconds"]).chat.completions.create(**kwargs),
            ticket["timeoutSeconds"],
        )
        choice = response.choices[0]
        return {"output": choice.message.content or "", "outcome": "completed" if choice.finish_reason == "stop" else "unknown",
                "requests": 1, "model": model, "tokens": getattr(response.usage, "total_tokens", None)}
    try:
        return asyncio.run(run())
    finally:
        session.loop = session.task = None


def register(ctx, *, root=None, session_factory=MemorySession, executor=bounded_completion):
    selected = os.environ.get("CETIRUN_MEMORY_ASSETS")
    if root is None and selected != "/opt/cetirun-memory":
        return
    root = root or Path(selected)
    memory_contract = load_assets(root)
    context_dir = Path.home() / ".agents"
    context_file = context_dir / "AGENTS.md"
    if context_dir.is_symlink() or context_file.is_symlink() or not context_file.is_file() or context_file.stat().st_size > 32768 or context_file.stat().st_nlink != 1:
        raise ValueError("AgentContextUnavailable")
    canonical = context_file.read_text()
    if len(canonical) > 4000:
        raise ValueError("AgentContextTooLarge")
    sessions, guard = {}, threading.RLock()
    tool_identity = ContextVar("cetirun_memory_tool_identity", default=None)

    def state(session_id=None):
        if not session_id:
            from gateway.session_context import get_session_env
            session_id = get_session_env("HERMES_SESSION_ID")
        with guard:
            return sessions.get(session_id)

    def start(session_id="", platform="", parent_session_id="", user_message="", turn_id="", model="", conversation_history=None, **kwargs):
        from agent.runtime_cwd import resolve_agent_cwd
        private = platform in ("cli", "tui", "api_server") and not parent_session_id
        if not session_id:
            return
        with guard:
            previous = sessions.get(session_id)
            controls = {"noCapture": False, "noMemory": False}
            if previous:
                controls = {key: previous.binding[key] for key in controls}
                previous.stop()
            else:
                for message in conversation_history or []:
                    if not isinstance(message, dict) or message.get("role") != "user":
                        continue
                    text = message.get("content")
                    if isinstance(text, str) and text.strip() in ("/memory off", "/memory no-capture"):
                        controls["noCapture"] = True
                        controls["noMemory"] |= text.strip() == "/memory off"
            if private and isinstance(user_message, str) and user_message.strip() in ("/memory off", "/memory no-capture", "/memory on"):
                mode = user_message.strip().split()[1]
                controls = {"noCapture": mode != "on", "noMemory": mode == "off"}
            controls["noCapture"] |= kwargs.get("noCapture") is True
            controls["noMemory"] |= kwargs.get("noMemory") is True
            provider = kwargs.get("provider", "")
            # The effective route is resolved by native request middleware.
            binding = {"adapter": ADAPTER, "entrypoint": "hermes", "nativeVersion": NATIVE_VERSION,
                       "sessionId": session_id, "opportunityId": turn_id or str(uuid.uuid4()),
                       "cwd": str(resolve_agent_cwd()), "audience": "owner-private" if private else "unknown",
                       "destination": {"kind": "owner-private" if private else "unknown"},
                       "parentSessionId": parent_session_id or "", "evaluation": False, **controls,
                       "model": provider + "/" + model, "modelEvaluation": False}
            sessions[session_id] = session_factory(root, binding)
            if len(sessions) > 128:
                sessions.pop(next(iter(sessions))).stop()
        if private and isinstance(user_message, str) and user_message.lower().startswith(("remember my preference:", "记住我的偏好：")):
            sessions[session_id].call("owner-preference", {"message": user_message}, event_id=binding["opportunityId"] + "-preference")

    def request(request, session_id="", provider="", model="", api_request_id="", base_url="", **kwargs):
        current = state(session_id)
        if not current:
            return None
        current.binding["model"] = provider + "/" + model
        current.binding["modelRevision"] = model_revision(provider, model, base_url, kwargs.get("api_mode"))
        current.binding["modelEvaluation"] = kwargs.get("api_mode") in ("chat_completions", "chat", "openai")
        messages = request.get("messages")
        if not isinstance(messages, list):
            # Do not guess another transport's message schema.
            return None
        text = current.context()
        if text.startswith(memory_contract):
            text = text[len(memory_contract):].lstrip()
        next_request = dict(request)
        next_request["messages"] = [*messages, {"role": "user", "content": text}]
        current.record_injection(api_request_id or str(uuid.uuid4()))
        return {"request": next_request, "source": "cetirun-memory-context", "reason": "current private Memory view"}

    def tool(args, **kwargs):
        current = state(kwargs.get("session_id"))
        if not current:
            return json.dumps({"ok": False, "reason": "NativeContextUnavailable"})
        if current.cancelled.is_set():
            return json.dumps({"ok": False, "reason": "Cancelled"})
        identity = tool_identity.get()
        call_id = kwargs.get("tool_call_id") or (identity[1] if identity and identity[0] == current.binding["sessionId"] else None)
        if not call_id:
            if args.get("command") not in ("status", "prepare", "lint", "trace", "source"):
                return json.dumps({"ok": False, "reason": "NativeCallIdentityUnavailable"})
            call_id = str(uuid.uuid4())
        return json.dumps(current.tool(args, call_id, executor))

    def execute(tool_name, args, next_call, session_id="", tool_call_id="", **_kwargs):
        if tool_name != "cetirun_memory":
            return next_call(args)
        token = tool_identity.set((session_id, tool_call_id))
        try:
            return next_call(args)
        finally:
            tool_identity.reset(token)

    def feedback(tool_name="", status="", session_id="", tool_call_id="", **_kwargs):
        current = state(session_id)
        if current and tool_name == "cetirun_memory" and status == "cancelled":
            current.stop()
        elif current and tool_name != "cetirun_memory" and status in ("error", "blocked"):
            current.call("feedback", {"tool": tool_name, "category": "failure" if status == "error" else "permission-denied"}, event_id=tool_call_id or str(uuid.uuid4()))

    def outbound(tool_name="", session_id="", **_kwargs):
        current = state(session_id)
        if current and current.binding["audience"] == "owner-private" and not current.binding["noMemory"] and tool_name in ("send_message", "delegate_task"):
            return {"action": "approve", "message": "This task may contain Agent-private memory. Confirm the exact recipient and minimum outgoing content; a child receives no automatic Memory access."}

    def finish(session_id="", turn_id="", completed=False, interrupted=False, **_kwargs):
        current = state(session_id)
        if current and (not turn_id or current.binding["opportunityId"] == turn_id):
            if completed and not interrupted:
                current.call("boundary")
            current.stop()

    def finalize(session_id="", **_kwargs):
        current = state(session_id)
        if current:
            current.stop()

    ctx.register_system_prompt_section("cetirun-memory-context", canonical, max_chars=4000)
    ctx.register_hook("pre_llm_call", start)
    ctx.register_middleware("llm_request", request)
    ctx.register_middleware("tool_execution", execute)
    ctx.register_hook("post_tool_call", feedback)
    ctx.register_hook("pre_tool_call", outbound)
    ctx.register_hook("on_session_end", finish)
    ctx.register_hook("on_session_finalize", finalize)
    ctx.register_tool(name="cetirun_memory", toolset="cetirun_memory", handler=tool, schema={
        "name": "cetirun_memory", "description": "Use this Agent's private Memory: look up earlier conversations before answering, save facts, preferences, decisions and your own recommendations worth keeping, correct notes, and propose scoped method candidates. Read the cetirun-memory skill for payload formats. This tool cannot grant authority, change request restrictions or shared skills.",
        "parameters": {"type": "object", "additionalProperties": False, "properties": {
            "command": {"type": "string", "enum": list(COMMANDS)}, "payload": {"type": "object"}}, "required": ["command", "payload"]},
    })
