# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""One JSON line on stdout for every model call the Gateway handles.

LiteLLM writes no per-request record to its own log, and of its export
callbacks the Azure Blob Storage one needs a LiteLLM Enterprise license while
the others need a third-party service. A line on stdout reaches whatever
collects the cluster's container logs, such as Container Insights into a Log
Analytics workspace, with nothing more to deploy or authorize.

The record is metadata only: the caller (virtual key, user, Team, and the Agent
the CetiRun operator recorded on the key), the model, tokens, spend, timing and
the outcome. Request and response content is never written.

A client can ask LiteLLM for per-request debug output with
litellm_request_debug, which makes LiteLLM print that request's prompt and the
provider's raw response to the log. The callback drops that field from every
request, so content stays out of the log.

LiteLLM loads it from litellm_settings.callbacks as
"cetirun_ai_gateway.request_log.handler".
"""

import datetime
import json
import sys
from urllib.parse import urlsplit

from litellm.integrations.custom_logger import CustomLogger

LOG_TYPE = "cetirun.ai_gateway.request"
SCHEMA_VERSION = 1

# The container runtime splits a line longer than 16 KiB, and a collector that
# does not join the pieces again stores each as its own line, none of them
# valid JSON. Container Insights does that unless multiline logging is on.
MAX_LINE_BYTES = 15 * 1024
ERROR_MESSAGE_MAX_CHARS = 1024
# Values a client or an administrator chooses freely are cut to this length.
FREE_TEXT_MAX_CHARS = 256
MAX_REQUEST_TAGS = 20
# Metadata the CetiRun operator puts on each Agent's virtual key.
KEY_METADATA_PREFIX = "cetirun_"
OPERATOR_KEY_METADATA = ("cetirun_agent", "cetirun_agent_uid", "cetirun_user", "cetirun_team")
# LiteLLM tags every request with the client's user agent, which the record
# already carries in its own field.
USER_AGENT_TAG_PREFIX = "User-Agent:"
# A request body field that makes LiteLLM log that request's content.
DEBUG_PARAMETER = "litellm_request_debug"


def iso_time(timestamp):
    if not isinstance(timestamp, (int, float)) or timestamp <= 0:
        return None
    moment = datetime.datetime.fromtimestamp(timestamp, tz=datetime.timezone.utc)
    return moment.isoformat(timespec="milliseconds").replace("+00:00", "Z")


def milliseconds(start, end):
    if not isinstance(start, (int, float)) or not isinstance(end, (int, float)) or end < start:
        return None
    return round((end - start) * 1000)


def endpoint_origin(api_base):
    """Scheme and host of the provider endpoint, without any path, query or credentials."""
    if not api_base:
        return None
    parts = urlsplit(str(api_base))
    if not parts.scheme or not parts.hostname:
        return None
    origin = f"{parts.scheme}://{parts.hostname}"
    return f"{origin}:{parts.port}" if parts.port else origin


def first_number(*values):
    for value in values:
        if isinstance(value, (int, float)) and not isinstance(value, bool):
            return value
    return None


def as_dict(value):
    return value if isinstance(value, dict) else {}


def token_details(usage):
    """Cached, cache-write and reasoning tokens, in whichever shape the provider reported them."""
    prompt_details = as_dict(usage.get("prompt_tokens_details")) or as_dict(usage.get("input_tokens_details"))
    completion_details = as_dict(usage.get("completion_tokens_details")) or as_dict(usage.get("output_tokens_details"))
    return {
        "cached_tokens": first_number(prompt_details.get("cached_tokens"), usage.get("cache_read_input_tokens")),
        "cache_creation_tokens": first_number(
            usage.get("cache_creation_input_tokens"), prompt_details.get("cache_creation_tokens")
        ),
        "reasoning_tokens": first_number(completion_details.get("reasoning_tokens")),
    }


def dump(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), default=str)


def clip(value, limit=FREE_TEXT_MAX_CHARS):
    if value is None:
        return None
    text = str(value)
    return text if len(text) <= limit else text[:limit]


def without_empty(record):
    return {key: value for key, value in record.items() if value is not None and value != {} and value != []}


class RequestLog(CustomLogger):
    """Writes the record of each completed or failed call to stdout."""

    def __init__(self, stream=None):
        super().__init__()
        self.stream = stream

    async def async_pre_call_hook(self, user_api_key_dict, cache, data, call_type):
        if isinstance(data, dict):
            data.pop(DEBUG_PARAMETER, None)
        return None

    async def async_log_success_event(self, kwargs, response_obj, start_time, end_time):
        self.write(kwargs)

    async def async_log_failure_event(self, kwargs, response_obj, start_time, end_time):
        self.write(kwargs)

    def write(self, kwargs):
        payload = kwargs.get("standard_logging_object")
        if not isinstance(payload, dict):
            return
        stream = self.stream or sys.stdout
        stream.write(self.line(payload) + "\n")
        stream.flush()

    def record(self, payload):
        metadata = as_dict(payload.get("metadata"))
        hidden = as_dict(payload.get("hidden_params"))
        usage = as_dict(metadata.get("usage_object")) or as_dict(hidden.get("usage_object"))
        error = as_dict(payload.get("error_information"))
        key_metadata = {
            name: value if isinstance(value, (int, float)) else clip(value)
            for name, value in as_dict(metadata.get("user_api_key_auth_metadata")).items()
            if isinstance(name, str) and name.startswith(KEY_METADATA_PREFIX) and isinstance(value, (str, int, float))
        }
        tags = [
            clip(tag)
            for tag in payload.get("request_tags") or []
            if isinstance(tag, str) and not tag.startswith(USER_AGENT_TAG_PREFIX)
        ][:MAX_REQUEST_TAGS]
        start = payload.get("startTime")
        end = payload.get("endTime")
        stream = payload.get("stream") is True
        status = payload.get("status")

        record = {
            "log_type": LOG_TYPE,
            "schema_version": SCHEMA_VERSION,
            "time": iso_time(end),
            "start_time": iso_time(start),
            "status": status,
            "request_id": clip(payload.get("id"), 1024),
            "call_id": payload.get("litellm_call_id"),
            "trace_id": payload.get("trace_id"),
            "route": metadata.get("user_api_key_request_route"),
            "call_type": payload.get("call_type"),
            "stream": stream,
            "model": clip(payload.get("model_group") or payload.get("model")),
            "provider_model": clip(hidden.get("litellm_model_name") or payload.get("model")),
            "provider": payload.get("custom_llm_provider"),
            "deployment_id": payload.get("model_id"),
            "api_base": endpoint_origin(payload.get("api_base") or hidden.get("api_base")),
            "duration_ms": milliseconds(start, end),
            "time_to_first_token_ms": milliseconds(start, payload.get("completionStartTime")) if stream else None,
            "prompt_tokens": first_number(payload.get("prompt_tokens")),
            "completion_tokens": first_number(payload.get("completion_tokens")),
            "total_tokens": first_number(payload.get("total_tokens")),
            **token_details(usage),
            "spend": first_number(payload.get("response_cost")),
            "response_cache_hit": payload.get("cache_hit") if isinstance(payload.get("cache_hit"), bool) else None,
            "key_alias": clip(metadata.get("user_api_key_alias")),
            "key_hash": metadata.get("user_api_key_hash"),
            "user_id": clip(metadata.get("user_api_key_user_id")),
            "team_id": clip(metadata.get("user_api_key_team_id")),
            "team_alias": clip(metadata.get("user_api_key_team_alias")),
            "end_user": clip(payload.get("end_user") or metadata.get("user_api_key_end_user_id")),
            "key_metadata": key_metadata,
            "client_ip": payload.get("requester_ip_address") or metadata.get("requester_ip_address"),
            "user_agent": clip(payload.get("user_agent") or metadata.get("user_agent")),
            "request_tags": tags,
        }
        if status == "failure":
            message = error.get("error_message") or payload.get("error_str")
            record.update(
                {
                    "error_class": clip(error.get("error_class")),
                    "error_code": None if error.get("error_code") is None else clip(error.get("error_code")),
                    "error_provider": clip(error.get("llm_provider")),
                    "error_message": clip(message, ERROR_MESSAGE_MAX_CHARS) if message else None,
                    "rate_limit_type": error.get("error_rate_limit_type"),
                    "budget_entity_type": error.get("error_budget_entity_type"),
                }
            )
        return without_empty(record)

    def line(self, payload):
        record = self.record(payload)
        text = dump(record)
        # The fields above are capped, so a line stays well under the limit.
        # Should it not, the error text goes first, then the client's tags,
        # then key metadata beyond what the operator sets: the rest is what
        # every record must keep. JSON escaping only ever lengthens a string,
        # so removing as many bytes from a value as the line is over brings the
        # line within the limit.
        excess = len(text.encode("utf-8")) - MAX_LINE_BYTES
        message = record.get("error_message")
        if excess > 0 and message:
            data = message.encode("utf-8")
            record["error_message"] = data[: max(0, len(data) - excess)].decode("utf-8", "ignore")
            text = dump(record)
        if len(text.encode("utf-8")) > MAX_LINE_BYTES:
            record.pop("request_tags", None)
            text = dump(record)
        if len(text.encode("utf-8")) > MAX_LINE_BYTES and "key_metadata" in record:
            record["key_metadata"] = {
                name: value for name, value in record["key_metadata"].items() if name in OPERATOR_KEY_METADATA
            }
            text = dump(without_empty(record))
        return text


handler = RequestLog()
