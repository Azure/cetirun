# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Tests for the AI Gateway image's content records (cetirun_ai_gateway/content.py).

LiteLLM is not installed for these tests: the module only subclasses LiteLLM's
CustomLogger, which is replaced by a stand-in.
"""

import asyncio
import importlib.util
import json
import pathlib
import sys
import types
import unittest

PACKAGE = pathlib.Path(__file__).resolve().parents[2] / "app/cetirun-ai-gateway/cetirun_ai_gateway"


class CustomLogger:
    def __init__(self, *args, **kwargs):
        pass


def load_content():
    stubs = {
        "litellm": types.ModuleType("litellm"),
        "litellm.integrations": types.ModuleType("litellm.integrations"),
        "litellm.integrations.custom_logger": types.ModuleType("litellm.integrations.custom_logger"),
    }
    stubs["litellm.integrations.custom_logger"].CustomLogger = CustomLogger
    saved = {name: sys.modules.get(name) for name in stubs}
    sys.modules.update(stubs)
    try:
        spec = importlib.util.spec_from_file_location("gateway_content", PACKAGE / "content.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module
    finally:
        for name, previous in saved.items():
            if previous is None:
                sys.modules.pop(name, None)
            else:
                sys.modules[name] = previous


CONTENT = load_content()


def payload(messages, response=None, **overrides):
    value = {
        "id": "chatcmpl-123",
        "litellm_call_id": "call-1",
        "model_group": "gpt-example",
        "status": "success",
        "messages": messages,
        "response": response if response is not None else {"choices": [{"message": {"role": "assistant", "content": "Hi."}}]},
        "metadata": {
            "user_api_key_auth_metadata": {
                "cetirun_agent": "example-agent",
                "cetirun_agent_uid": "11111111-1111-1111-1111-111111111111",
                "cetirun_user": "example-user",
                "cetirun_team": "example-team",
                "other": "not copied",
            }
        },
    }
    value.update(overrides)
    return value


def joined(records, kind):
    parts = sorted((r for r in records if r["kind"] == kind), key=lambda r: r["part"])
    return "".join(r["text"] for r in parts)


class ContentRecordTests(unittest.TestCase):
    def test_short_call_gives_one_record_each_with_the_agent(self):
        messages = [{"role": "user", "content": "Hello"}]
        records = CONTENT.records(payload(messages))
        self.assertEqual([(r["kind"], r["part"], r["parts"]) for r in records], [("request", 0, 1), ("response", 0, 1)])
        first = records[0]
        self.assertEqual(json.loads(first["text"]), messages)
        self.assertEqual((first["agent"], first["owner"], first["team"]), ("example-agent", "example-user", "example-team"))
        self.assertEqual(first["agent_uid"], "11111111-1111-1111-1111-111111111111")
        self.assertNotIn("other", json.dumps(first))
        self.assertEqual((first["call_id"], first["request_id"], first["model"]), ("call-1", "chatcmpl-123", "gpt-example"))
        self.assertEqual(first["log_type"], "cetirun.ai_gateway.content")

    def test_long_content_is_cut_within_the_limit_and_joins_again(self):
        # Multibyte characters, characters JSON escapes, and plain text.
        text = ("Grüße 你好 🙂 \"quoted\"\n\t\x01 " * 4000) + "end"
        messages = [{"role": "user", "content": text}]
        limit = 4096
        records = CONTENT.records(payload(messages), limit=limit)
        request = [r for r in records if r["kind"] == "request"]
        self.assertGreater(len(request), 10)
        for record in records:
            self.assertLessEqual(len(CONTENT.dump(record).encode("utf-8")), limit)
            self.assertEqual(record["parts"], len([r for r in records if r["kind"] == record["kind"]]))
        self.assertEqual(json.loads(joined(records, "request")), messages)
        self.assertEqual([r["part"] for r in request], list(range(len(request))))

    def test_inline_data_and_encrypted_reasoning_are_replaced_by_notes(self):
        image = "data:image/png;base64," + "A" * 5000
        messages = [{"role": "user", "content": [{"type": "text", "text": "What is this?"},
                                                 {"type": "image_url", "image_url": {"url": image}}]}]
        response = {"output": [{"type": "reasoning", "encrypted_content": "gAAAA" + "x" * 900}]}
        records = CONTENT.records(payload(messages, response))
        request = json.loads(joined(records, "request"))
        self.assertEqual(request[0]["content"][0]["text"], "What is this?")
        self.assertEqual(request[0]["content"][1]["image_url"]["url"], f"[image/png omitted, {len(image)} characters]")
        self.assertNotIn("AAAAAAAA", joined(records, "request"))
        self.assertEqual(json.loads(joined(records, "response"))["output"][0]["encrypted_content"], "[encrypted, 905 characters]")

    def test_call_without_content_gives_no_records(self):
        self.assertEqual(CONTENT.records(payload(None, response={}, )), [])

    def test_string_content_is_kept_as_text(self):
        records = CONTENT.records(payload("plain prompt", response="plain answer"))
        self.assertEqual(joined(records, "request"), "plain prompt")
        self.assertEqual(joined(records, "response"), "plain answer")

    def test_cutting_never_splits_a_character_or_stalls(self):
        self.assertEqual(CONTENT.parts_of("你好🙂", 1), ["你", "好", "🙂"])
        self.assertEqual(CONTENT.prefix("🙂abc", 2), "🙂")
        # Mostly characters that JSON escapes to six bytes each.
        text = "\x01" * 20000
        records = CONTENT.records(payload(text, response="ok"), limit=2048)
        self.assertEqual(joined(records, "request"), text)
        self.assertTrue(all(len(CONTENT.dump(r).encode("utf-8")) <= 2048 for r in records))

    def test_limit_smaller_than_a_record_without_text_is_refused(self):
        with self.assertRaisesRegex(ValueError, "longer than the record limit"):
            CONTENT.records(payload("x"), limit=100)

    def test_callback_hands_records_to_the_sink(self):
        received = []
        log = CONTENT.ContentLog(received.append)
        kwargs = {"standard_logging_object": payload([{"role": "user", "content": "Hello"}])}
        asyncio.run(log.async_log_success_event(kwargs, None, None, None))
        asyncio.run(log.async_log_failure_event({"standard_logging_object": None}, None, None, None))
        self.assertEqual(len(received), 1)
        self.assertEqual([r["kind"] for r in received[0]], ["request", "response"])


if __name__ == "__main__":
    unittest.main()
