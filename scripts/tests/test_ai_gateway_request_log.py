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

    def test_no_content_setting_remains(self):
        source = (PACKAGE / "request_log.py").read_text(encoding="utf-8")
        self.assertNotIn("CETIRUN_REQUEST_LOG_CONTENT", source)

    def test_module_exposes_the_configured_instance(self):
        self.assertIsInstance(self.module.handler, self.module.RequestLog)


if __name__ == "__main__":
    unittest.main()
