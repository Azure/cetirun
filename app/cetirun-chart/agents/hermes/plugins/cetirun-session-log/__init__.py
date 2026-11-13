# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""CetiRun session log for Hermes.

The post_llm_call and post_tool_call hooks give the user input, the assistant
output, and the tool calls and results of each turn. The plugin appends one
JSON record for each to /home/vibe/.hermes/cetirun-session-log/hermes.jsonl,
which the agent chart's logger copies to the container log. The records have
the format of the OpenClaw plugin, and the same line and file limits. The
chart enables the plugin with logger.enabled.
"""

import json
import logging
import os
import threading
import uuid
from datetime import datetime, timezone
from pathlib import Path

try:
    import fcntl
except ImportError:  # Not Linux: one process writes.
    fcntl = None

logger = logging.getLogger(__name__)

RECORD_VERSION = 1
# The container runtime splits longer log lines.
MAX_LINE_BYTES = 16000
MAX_FILE_BYTES = 8 * 1024 * 1024
# The logger container mounts /home/vibe/.hermes. A Hermes profile has its own
# HERMES_HOME below it, so the directory does not follow HERMES_HOME.
LOG_DIR = Path("/home/vibe/.hermes/cetirun-session-log")


def _now():
    return datetime.now(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def _text(value):
    if value is None:
        return ""
    if isinstance(value, str):
        return value
    try:
        return json.dumps(value, ensure_ascii=False, default=str)
    except (TypeError, ValueError):
        return str(value)


def _message_text(value):
    """The text of a message: a string, or text parts. Other parts are named, not copied."""
    if isinstance(value, list):
        parts = []
        for part in value:
            if isinstance(part, str):
                parts.append(part)
            elif isinstance(part, dict) and isinstance(part.get("text"), str):
                parts.append(part["text"])
            elif isinstance(part, dict):
                parts.append("[%s]" % (part.get("type") if isinstance(part.get("type"), str) else "part"))
        return "\n".join(part for part in parts if part)
    if isinstance(value, dict) and "content" in value:
        return _message_text(value["content"])
    return _text(value)


def _cut_utf8(text, max_bytes):
    data = text.encode("utf-8")
    if len(data) <= max_bytes:
        return text
    return data[:max(0, max_bytes)].decode("utf-8", errors="ignore")


def encode_record(record, max_line_bytes=MAX_LINE_BYTES):
    """One JSON line of at most max_line_bytes with its newline. A longer content is cut and marked."""
    content = record.get("content") or ""
    truncated = False
    for attempt in range(8):
        line = json.dumps({**record, "content": content, "truncated": truncated}, ensure_ascii=False) + "\n"
        size = len(line.encode("utf-8"))
        if size <= max_line_bytes:
            return line
        over = size - max_line_bytes
        content = _cut_utf8(content, len(content.encode("utf-8")) - over - 64 * (attempt + 1))
        truncated = True
    return json.dumps({**record, "content": "", "truncated": True}, ensure_ascii=False) + "\n"


class LogFile:
    """An append-only JSONL file with one rotated predecessor: <name> and <name>.1.

    Hermes can run several processes with plugins, such as the gateway and a
    terminal session; a lock file keeps their rotations and appends apart.
    """

    def __init__(self, directory, name, max_file_bytes=MAX_FILE_BYTES):
        self.directory = Path(directory)
        self.file = self.directory / name
        self.rotated = self.directory / (name + ".1")
        self.lock_file = self.directory / ("." + name + ".lock")
        self.max_file_bytes = max_file_bytes
        self._lock = threading.Lock()

    def append(self, lines):
        if not lines:
            return
        with self._lock:
            self.directory.mkdir(mode=0o700, parents=True, exist_ok=True)
            lock = os.open(self.lock_file, os.O_WRONLY | os.O_CREAT, 0o600)
            try:
                if fcntl:
                    fcntl.flock(lock, fcntl.LOCK_EX)
                for line in lines:
                    self._append_line(line.encode("utf-8"))
            finally:
                os.close(lock)

    def _append_line(self, data):
        try:
            size = self.file.stat().st_size
        except FileNotFoundError:
            size = 0
        if size > 0 and size + len(data) > self.max_file_bytes:
            try:
                os.replace(self.file, self.rotated)
            except FileNotFoundError:
                pass  # Another writer rotated it.
        fd = os.open(self.file, os.O_WRONLY | os.O_APPEND | os.O_CREAT, 0o600)
        try:
            view = memoryview(data)
            while view:
                view = view[os.write(fd, view):]
        finally:
            os.close(fd)


class Exporter:
    """Records of the turns and tool calls that Hermes reports to its hooks.

    A record id comes from the session and the turn or tool call, so a record
    that the logger copies twice is easy to remove. The conversation history
    that post_llm_call also gives is not copied.
    """

    def __init__(self, directory, redact, now=_now, max_file_bytes=MAX_FILE_BYTES, max_line_bytes=MAX_LINE_BYTES):
        self.output = LogFile(directory, "hermes.jsonl", max_file_bytes)
        self.redact = redact
        self.now = now
        self.max_line_bytes = max_line_bytes

    def _record(self, session_id, kind, key, run_id=None, platform=None, **fields):
        record = {
            "v": RECORD_VERSION,
            "id": f"hermes:{session_id}:{key}",
            "ts": self.now(),
            "runtime": "hermes",
            "sessionId": session_id,
        }
        if run_id:
            record["runId"] = run_id
        if platform:
            record["channel"] = platform
        record["kind"] = kind
        record.update(fields)
        return record

    def _write(self, records):
        self.output.append([encode_record(record, self.max_line_bytes) for record in records])

    def on_llm(self, session_id=None, turn_id=None, user_message=None, assistant_response=None, model=None,
               platform=None, **_):
        try:
            if not session_id:
                return
            turn = turn_id or uuid.uuid4().hex
            records = []
            if user_message:
                records.append(self._record(session_id, "user", f"{turn}:user", turn_id, platform,
                                            content=self.redact(_message_text(user_message))))
            if assistant_response:
                extra = {"model": model} if isinstance(model, str) and model else {}
                records.append(self._record(session_id, "assistant", f"{turn}:assistant", turn_id, platform, **extra,
                                            content=self.redact(_message_text(assistant_response))))
            self._write(records)
        except Exception:
            logger.warning("cetirun-session-log: a turn was not exported", exc_info=True)

    def on_tool(self, tool_name=None, args=None, result=None, session_id=None, tool_call_id=None, turn_id=None,
                status=None, **_):
        try:
            if not session_id:
                return
            call = tool_call_id or uuid.uuid4().hex
            tool = tool_name if isinstance(tool_name, str) else ""
            extra = {"toolCallId": tool_call_id} if tool_call_id else {}
            records = [
                self._record(session_id, "tool_call", f"{call}:call", turn_id, tool=tool, **extra,
                             content=self.redact(_text(args))),
                self._record(session_id, "tool_result", f"{call}:result", turn_id, tool=tool, **extra,
                             status=status or "ok", isError=status == "error", content=self.redact(_text(result))),
            ]
            self._write(records)
        except Exception:
            logger.warning("cetirun-session-log: a tool call was not exported", exc_info=True)


def register(ctx):
    try:
        from agent.redact import redact_sensitive_text
    except ImportError:
        logger.warning("cetirun-session-log: Hermes redaction is not available; sessions are not exported")
        return
    exporter = Exporter(
        LOG_DIR,
        # Mask also when Hermes's own redaction setting is off, and the credentials in URLs.
        lambda text: redact_sensitive_text(text, force=True, redact_url_credentials=True),
    )
    ctx.register_hook("post_llm_call", exporter.on_llm)
    ctx.register_hook("post_tool_call", exporter.on_tool)
