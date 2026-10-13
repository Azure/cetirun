# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Tests for the AI Gateway image's per-request log callback.

LiteLLM is not installed for these tests: the callback only subclasses
LiteLLM's CustomLogger, which is replaced by a stand-in, and the payloads below
follow LiteLLM's StandardLoggingPayload.
"""

import asyncio
import importlib.util
import io
import json
import pathlib
import random
import sys
import types
import unittest

PACKAGE = pathlib.Path(__file__).resolve().parents[2] / "app/cetirun-ai-gateway/cetirun_ai_gateway"


class CustomLogger:
    def __init__(self, *args, **kwargs):
        pass


def load_request_log():
    stubs = {
        "litellm": types.ModuleType("litellm"),
        "litellm.integrations": types.ModuleType("litellm.integrations"),
        "litellm.integrations.custom_logger": types.ModuleType("litellm.integrations.custom_logger"),
    }
    stubs["litellm.integrations.custom_logger"].CustomLogger = CustomLogger
    saved = {name: sys.modules.get(name) for name in stubs}
    sys.modules.update(stubs)
    try:
        spec = importlib.util.spec_from_file_location("gateway_request_log", PACKAGE / "request_log.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module
    finally:
        for name, previous in saved.items():
            if previous is None:
                sys.modules.pop(name, None)
            else:
                sys.modules[name] = previous


def success_payload(**overrides):
    payload = {
        "id": "chatcmpl-123",
        "litellm_call_id": "call-1",
        "trace_id": "trace-1",
        "call_type": "acompletion",
        "stream": True,
        "status": "success",
        "response_cost": 0.0125,
        "custom_llm_provider": "azure",
        "prompt_tokens": 1200,
        "completion_tokens": 300,
        "total_tokens": 1500,
        "startTime": 1791460800.0,
        "completionStartTime": 1791460800.4,
        "endTime": 1791460802.5,
        "model": "gpt-4o",
        "model_id": "deployment-hash",
        "model_group": "gpt-4o",
        "api_base": "https://user:secret@example.openai.azure.com/openai/deployments/prod?api-version=1",
        "cache_hit": None,
        "end_user": None,
        "requester_ip_address": "10.0.0.7",
        "user_agent": "OpenClaw/1.0",
        "request_tags": ["User-Agent: OpenClaw"],
        "messages": [{"role": "user", "content": "private question"}],
        "response": {"choices": [{"message": {"content": "private answer"}}]},
        "error_str": None,
        "error_information": None,
        "hidden_params": {"litellm_model_name": "azure/prod", "api_base": None},
        "metadata": {
            "user_api_key_hash": "88dc28d0f030c55ed4ab77ed8faf098196cb1c05df778539800c9f1243fe6b4b",
            "user_api_key_alias": "cetirun-alice-agent-1234",
            "user_api_key_user_id": "alice",
            "user_api_key_team_id": "cetirun-team-research",
            "user_api_key_team_alias": "research",
            "user_api_key_request_route": "/v1/chat/completions",
            "user_api_key_auth_metadata": {
                "cetirun_agent": "alice-agent",
                "cetirun_agent_uid": "1234",
                "cetirun_user": "alice",
                "cetirun_team": "research",
                "other": "not logged",
            },
            "usage_object": {
                "prompt_tokens": 1200,
                "completion_tokens": 300,
                "prompt_tokens_details": {"cached_tokens": 1024},
                "completion_tokens_details": {"reasoning_tokens": 64},
            },
        },
    }
    payload.update(overrides)
    return payload


def failure_payload(**overrides):
    payload = success_payload(
        status="failure",
        end_user="alice-end-user",
        error_information={
            "error_class": "BadRequestError",
            "error_code": "400",
            "llm_provider": "azure",
            "error_message": "bad request",
            "error_rate_limit_type": "tokens",
            "error_budget_entity_type": "key",
        },
    )
    payload.update(overrides)
    return payload


def flooded(value, text, keep=("status",)):
    """value with every string in it replaced by text(), apart from the keys in keep."""
    if isinstance(value, str):
        return text()
    if isinstance(value, dict):
        return {key: item if key in keep else flooded(item, text, keep) for key, item in value.items()}
    if isinstance(value, list):
        return [flooded(item, text, keep) for item in value]
    return value


def strings_in(value, path=()):
    """(path, string) for every string in value."""
    if isinstance(value, str):
        yield path, value
    elif isinstance(value, dict):
        for key, item in value.items():
            yield from strings_in(item, path + (key,))
    elif isinstance(value, list):
        for index, item in enumerate(value):
            yield from strings_in(item, path + (index,))


class RequestLogTest(unittest.TestCase):
    def setUp(self):
        self.module = load_request_log()

    def handler(self):
        return self.module.RequestLog(stream=io.StringIO())

    def test_success_record_carries_metadata_only(self):
        record = self.handler().record(success_payload())
        self.assertEqual(record["log_type"], "cetirun.ai_gateway.request")
        self.assertEqual(record["status"], "success")
        self.assertEqual(record["time"], "2026-10-08T12:00:02.500Z")
        self.assertEqual(record["duration_ms"], 2500)
        self.assertEqual(record["time_to_first_token_ms"], 400)
        self.assertEqual(record["model"], "gpt-4o")
        self.assertEqual(record["provider_model"], "azure/prod")
        self.assertEqual(record["api_base"], "https://example.openai.azure.com")
        self.assertEqual(record["cached_tokens"], 1024)
        self.assertEqual(record["reasoning_tokens"], 64)
        self.assertEqual(record["spend"], 0.0125)
        self.assertEqual(record["key_metadata"], {
            "cetirun_agent": "alice-agent",
            "cetirun_agent_uid": "1234",
            "cetirun_user": "alice",
            "cetirun_team": "research",
        })
        self.assertEqual(record["route"], "/v1/chat/completions")
        for field in ("request", "response", "messages", "content_truncated", "error_message", "end_user",
                      "response_cache_hit"):
            self.assertNotIn(field, record)
        self.assertNotIn("private", json.dumps(record))
        self.assertNotIn("secret", json.dumps(record))

    def test_failure_record_carries_the_error(self):
        payload = success_payload(
            status="failure",
            response_cost=0,
            error_information={
                "error_class": "RateLimitError",
                "error_code": 429,
                "llm_provider": "azure",
                "error_message": "x" * 5000,
                "error_rate_limit_type": "budget",
            },
        )
        record = self.handler().record(payload)
        self.assertEqual(record["status"], "failure")
        self.assertEqual(record["error_class"], "RateLimitError")
        self.assertEqual(record["error_code"], "429")
        self.assertEqual(record["rate_limit_type"], "budget")
        self.assertEqual(len(record["error_message"]), self.module.ERROR_MESSAGE_MAX_CHARS)
        self.assertNotIn("private", json.dumps(record))

    def test_responses_api_usage_shape(self):
        payload = success_payload(stream=False)
        payload["metadata"]["usage_object"] = {
            "input_tokens_details": {"cached_tokens": 512},
            "output_tokens_details": {"reasoning_tokens": 8},
        }
        record = self.handler().record(payload)
        self.assertEqual(record["cached_tokens"], 512)
        self.assertEqual(record["reasoning_tokens"], 8)
        self.assertNotIn("time_to_first_token_ms", record)

    def test_anthropic_cache_fields(self):
        payload = success_payload()
        payload["metadata"]["usage_object"] = {"cache_read_input_tokens": 900, "cache_creation_input_tokens": 100}
        record = self.handler().record(payload)
        self.assertEqual(record["cached_tokens"], 900)
        self.assertEqual(record["cache_creation_tokens"], 100)

    def test_free_text_from_the_client_is_capped(self):
        payload = success_payload(
            end_user="e" * 5000,
            user_agent="a" * 5000,
            request_tags=["t" * 5000] * 100,
        )
        payload["metadata"]["user_api_key_auth_metadata"]["cetirun_agent"] = "n" * 5000
        record = self.handler().record(payload)
        limit = self.module.FREE_TEXT_MAX_CHARS
        self.assertEqual(len(record["end_user"]), limit)
        self.assertEqual(len(record["user_agent"]), limit)
        self.assertEqual(len(record["request_tags"]), self.module.MAX_REQUEST_TAGS)
        self.assertTrue(all(len(tag) == limit for tag in record["request_tags"]))
        self.assertEqual(len(record["key_metadata"]["cetirun_agent"]), limit)

    def test_line_stays_under_the_collector_limit(self):
        payload = success_payload(
            status="failure",
            messages=[{"role": "user", "content": "\u00e9\"\n" * 20000}],
            response={"text": "\u4e2d" * 20000},
            error_information={"error_class": "BadRequestError", "error_message": "\u4e2d\"" * 5000},
            request_tags=["\u4e2d" * 300] * 100,
        )
        payload["metadata"]["user_api_key_auth_metadata"].update(
            {f"cetirun_extra_{i}": "\u4e2d" * 300 for i in range(40)})
        line = self.handler().line(payload)
        self.assertLessEqual(len(line.encode("utf-8")), self.module.MAX_LINE_BYTES)
        record = json.loads(line)
        self.assertEqual(record["key_alias"], "cetirun-alice-agent-1234")
        self.assertEqual(record["error_class"], "BadRequestError")
        self.assertNotIn("request", record)

    def test_identifiers_the_client_chooses_are_capped(self):
        # LiteLLM records a request's metadata.session_id as trace_id; clients
        # also choose the call ID header, the model name and their address.
        payload = success_payload(
            trace_id="s" * 100_000,
            litellm_call_id="c" * 100_000,
            requester_ip_address="i" * 100_000,
            model_group="m" * 100_000,
            custom_llm_provider="p" * 100_000,
            call_type="t" * 100_000,
            model_id="d" * 100_000,
        )
        payload["metadata"]["user_api_key_request_route"] = "/v1/" + "r" * 100_000
        record = self.handler().record(payload)
        for field in ("trace_id", "call_id", "client_ip", "model", "provider", "call_type", "deployment_id", "route"):
            self.assertEqual(len(record[field]), self.module.FREE_TEXT_MAX_CHARS, field)
        line = self.handler().line(payload)
        self.assertLessEqual(len(line.encode("utf-8")), self.module.MAX_LINE_BYTES)
        self.assertNotIn("truncated", json.loads(line))

    def test_every_string_in_a_record_is_capped(self):
        record = self.handler().record(flooded(failure_payload(), lambda: "x" * 100_000))
        limits = {"request_id": self.module.REQUEST_ID_MAX_CHARS, "error_message": self.module.ERROR_MESSAGE_MAX_CHARS}
        for path, value in strings_in(record):
            self.assertLessEqual(len(value), limits.get(path[0], self.module.FREE_TEXT_MAX_CHARS), path)
        self.assertEqual(len(record["request_tags"]), 1)
        self.assertEqual(len(record["key_metadata"]), 4)

    def test_a_line_never_exceeds_the_limit(self):
        # A control character takes six bytes in JSON, so capped strings can
        # still add up to more than the limit.
        payload = flooded(failure_payload(), lambda: "\x01" * 100_000)
        payload["metadata"]["user_api_key_hash"] = "88dc28d0f030c55ed4ab77ed8faf098196cb1c05df778539800c9f1243fe6b4b"
        line = self.handler().line(payload)
        self.assertLessEqual(len(line.encode("utf-8")), self.module.MAX_LINE_BYTES)
        record = json.loads(line)
        self.assertIs(record["truncated"], True)
        self.assertEqual(record["log_type"], "cetirun.ai_gateway.request")
        self.assertEqual(record["status"], "failure")
        self.assertEqual(record["key_hash"], payload["metadata"]["user_api_key_hash"])

    def test_a_line_fits_whatever_the_strings(self):
        rng = random.Random(166)
        alphabet = ["a", "\u00e9", "\u4e2d", "\U0001f600", '"', "\\", "\n", "\x01", "\u2028"]
        for _ in range(100):
            def text():
                return "".join(rng.choices(alphabet, k=rng.choice([0, 10, 300, 1100])))
            payload = flooded(failure_payload() if rng.random() < 0.5 else success_payload(), text)
            line = self.handler().line(payload)
            self.assertLessEqual(len(line.encode("utf-8")), self.module.MAX_LINE_BYTES)
            self.assertNotIn("\n", line)
            json.loads(line, parse_constant=self.fail)

    def test_numbers_json_cannot_carry_are_left_out(self):
        payload = success_payload(response_cost=float("nan"), prompt_tokens=float("inf"))
        payload["metadata"]["user_api_key_auth_metadata"]["cetirun_ratio"] = float("-inf")
        record = json.loads(self.handler().line(payload), parse_constant=self.fail)
        self.assertNotIn("spend", record)
        self.assertNotIn("prompt_tokens", record)
        self.assertNotIn("cetirun_ratio", record["key_metadata"])

    def test_only_the_call_is_identified_when_nothing_else_fits(self):
        huge = 10 ** 4000
        payload = success_payload(prompt_tokens=huge, completion_tokens=huge, total_tokens=huge, response_cost=huge)
        payload["metadata"]["usage_object"] = {"cache_read_input_tokens": huge, "cache_creation_input_tokens": huge}
        line = self.handler().line(payload)
        self.assertLessEqual(len(line.encode("utf-8")), self.module.MAX_LINE_BYTES)
        self.assertEqual(json.loads(line), {
            "log_type": "cetirun.ai_gateway.request",
            "schema_version": 1,
            "time": "2026-10-08T12:00:02.500Z",
            "status": "success",
            "call_id": "call-1",
            "truncated": True,
        })

    def test_events_write_one_line_each(self):
        handler = self.handler()
        asyncio.run(handler.async_log_success_event({"standard_logging_object": success_payload()}, None, None, None))
        asyncio.run(handler.async_log_failure_event(
            {"standard_logging_object": success_payload(status="failure")}, None, None, None))
        asyncio.run(handler.async_log_success_event({}, None, None, None))
        lines = handler.stream.getvalue().splitlines()
        self.assertEqual([json.loads(line)["status"] for line in lines], ["success", "failure"])

    def test_user_agent_tags_are_left_to_their_own_field(self):
        payload = success_payload(request_tags=["User-Agent: OpenClaw", "User-Agent: OpenClaw/1.0", "nightly"])
        record = self.handler().record(payload)
        self.assertEqual(record["request_tags"], ["nightly"])
        self.assertEqual(record["user_agent"], "OpenClaw/1.0")

    def test_client_debug_output_is_always_dropped(self):
        data = {"model": "gpt-4o", "litellm_request_debug": True}
        result = asyncio.run(self.handler().async_pre_call_hook(None, None, data, "acompletion"))
        self.assertIsNone(result)
        self.assertEqual(data, {"model": "gpt-4o"})

    def test_callers_cannot_change_the_retries(self):
        # num_retries also carries the x-litellm-num-retries header: LiteLLM
        # copies it into the request before the callback runs. max_retries
        # stays: measured, it did not add an upstream call.
        data = {
            "model": "gpt-4o",
            "num_retries": 3,
            "model_group_retry_policy": {"gpt-4o": {"InternalServerErrorRetries": 3}},
            "max_retries": 3,
            "messages": [{"role": "user", "content": "hi"}],
        }
        asyncio.run(self.handler().async_pre_call_hook(None, None, data, "acompletion"))
        self.assertEqual(data, {"model": "gpt-4o", "max_retries": 3, "messages": [{"role": "user", "content": "hi"}]})

    def test_no_content_setting_remains(self):
        source = (PACKAGE / "request_log.py").read_text(encoding="utf-8")
        self.assertNotIn("CETIRUN_REQUEST_LOG_CONTENT", source)

    def test_module_exposes_the_configured_instance(self):
        self.assertIsInstance(self.module.handler, self.module.RequestLog)


if __name__ == "__main__":
    unittest.main()
