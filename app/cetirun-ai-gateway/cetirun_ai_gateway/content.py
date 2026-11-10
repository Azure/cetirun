# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Content records of model calls: the prompt and the response of each call, for a store of their own.

The request log (request_log.py) keeps metadata only. These records carry the
messages a client sent and the response it got, so they are kept apart from
every log: not on stdout, where every reader of the container's logs would see
them, and not through a log collector. ``ContentLog`` hands each call's records
to a sink, which sends them to a store with readers of its own.

A record is at most MAX_RECORD_BYTES of JSON. Content longer than that is cut
into parts at UTF-8 character boundaries; each part says its number and the
number of parts, and the store joins them again in order (see the
GatewayContent() function of the CetiRunContent database).

Before it is cut, the content is reduced:

- Inline images and files (``data:`` URLs with base64) are replaced by a note
  with their media type and size: they are large, and the text describes
  what the model saw.
- Encrypted reasoning items are replaced by a note with their size: no reader
  can decrypt them.
"""

import json
import re

from litellm.integrations.custom_logger import CustomLogger

LOG_TYPE = "cetirun.ai_gateway.content"
SCHEMA_VERSION = 1
# A row of Azure Data Explorer holds far more, but ingestion and queries handle
# rows of this size well, and a call's text rarely needs more than a few parts.
MAX_RECORD_BYTES = 256 * 1024
OPERATOR_KEY_METADATA = {
    "cetirun_agent": "agent",
    "cetirun_agent_uid": "agent_uid",
    "cetirun_user": "owner",
    "cetirun_team": "team",
}
DATA_URL = re.compile(r"data:([\w.+-]+/[\w.+-]+)?(;[\w=.-]+)*;base64,[A-Za-z0-9+/=\s]+")
ENCRYPTED_FIELDS = ("encrypted_content",)


def as_dict(value):
    return value if isinstance(value, dict) else {}


def dump(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), default=str)


def reduced(value):
    """value with inline base64 data and encrypted reasoning replaced by short notes."""
    if isinstance(value, str):
        return DATA_URL.sub(lambda match: f"[{match.group(1) or 'data'} omitted, {len(match.group(0))} characters]", value)
    if isinstance(value, dict):
        result = {}
        for name, item in value.items():
            if name in ENCRYPTED_FIELDS and isinstance(item, str) and item:
                result[name] = f"[encrypted, {len(item)} characters]"
            else:
                result[name] = reduced(item)
        return result
    if isinstance(value, (list, tuple)):
        return [reduced(item) for item in value]
    return value


def parts_of(text, budget):
    """text cut into pieces of at most budget UTF-8 bytes, at character boundaries."""
    data = text.encode("utf-8")
    pieces = []
    while data:
        end = min(budget, len(data))
        # Step back to the start of a character: continuation bytes are 10xxxxxx.
        while end < len(data) and end > 0 and data[end] & 0xC0 == 0x80:
            end -= 1
        if end == 0:
            # The budget is smaller than the first character: take that character whole.
            end = 1
            while end < len(data) and data[end] & 0xC0 == 0x80:
                end += 1
        pieces.append(data[:end].decode("utf-8"))
        data = data[end:]
    return pieces or [""]


def prefix(text, budget):
    """The longest prefix of text of at most budget UTF-8 bytes, at a character boundary; at least one character."""
    data = text.encode("utf-8")
    if len(data) <= budget:
        return text
    return parts_of(data[: budget + 4].decode("utf-8", "ignore"), budget)[0]


def fit(record, text, limit):
    """The longest prefix of text that keeps record, with it as its text, within limit bytes of JSON."""
    # Start with as many bytes as the record leaves room for, then cut what
    # escaping adds.
    room = limit - len(dump(dict(record, text="")).encode("utf-8"))
    if room <= 0:
        return ""
    text = prefix(text, room)
    while True:
        over = len(dump(dict(record, text=text)).encode("utf-8")) - limit
        if over <= 0:
            return text
        # Escaping makes a character at most six bytes longer than it is, so
        # cutting at least the excess converges. When not even one character
        # fits, nothing does.
        shorter = prefix(text, max(1, len(text.encode("utf-8")) - over))
        if shorter == text:
            return ""
        text = shorter


def records(payload, limit=MAX_RECORD_BYTES):
    """The content records of one call: its request, then its response, each cut into parts.

    payload is LiteLLM's StandardLoggingPayload. Calls without content, such as
    one LiteLLM logged with message logging turned off, give no records.
    """
    metadata = as_dict(payload.get("metadata"))
    key_metadata = as_dict(metadata.get("user_api_key_auth_metadata"))
    base = {
        "log_type": LOG_TYPE,
        "schema_version": SCHEMA_VERSION,
        "call_id": payload.get("litellm_call_id"),
        "request_id": payload.get("id"),
        "model": payload.get("model_group") or payload.get("model"),
        "status": payload.get("status"),
        **{field: key_metadata.get(name) for name, field in OPERATOR_KEY_METADATA.items() if isinstance(key_metadata.get(name), str)},
    }
    base = {name: value for name, value in base.items() if value is not None}
    result = []
    for kind, value in (("request", payload.get("messages")), ("response", payload.get("response"))):
        if value in (None, "", [], {}):
            continue
        text = value if isinstance(value, str) else dump(reduced(value))
        if isinstance(value, str):
            text = reduced(text)
        record = dict(base, kind=kind, part=0, parts=0)
        pieces = []
        rest = text
        while rest or not pieces:
            piece = fit(record, rest, limit)
            if not piece and rest:
                raise ValueError("a content record without its text is longer than the record limit")
            pieces.append(piece)
            rest = rest[len(piece):]
        for number, piece in enumerate(pieces):
            result.append(dict(base, kind=kind, part=number, parts=len(pieces), text=piece))
    return result


class ContentLog(CustomLogger):
    """Hands the content records of each completed or failed call to a sink.

    The sink takes a list of records. It must not block the request: it should
    queue them and send them elsewhere, and drop them when it cannot keep up.
    """

    def __init__(self, sink):
        super().__init__()
        self.sink = sink

    async def async_log_success_event(self, kwargs, response_obj, start_time, end_time):
        self.write(kwargs)

    async def async_log_failure_event(self, kwargs, response_obj, start_time, end_time):
        self.write(kwargs)

    def write(self, kwargs):
        payload = kwargs.get("standard_logging_object")
        if not isinstance(payload, dict):
            return
        found = records(payload)
        if found:
            self.sink(found)
