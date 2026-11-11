# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Tests for the AI Gateway image's content records (cetirun_ai_gateway/content.py).

LiteLLM and the Azure SDKs are not installed for these tests: the module only
subclasses LiteLLM's CustomLogger and AdditionalLoggingUtils, which are
replaced by stand-ins, and the writer and reader take stand-in clients.
"""

import asyncio
import datetime
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


class AdditionalLoggingUtils:
    def __init__(self, *args, **kwargs):
        pass


def load_content():
    stubs = {
        "litellm": types.ModuleType("litellm"),
        "litellm.integrations": types.ModuleType("litellm.integrations"),
        "litellm.integrations.custom_logger": types.ModuleType("litellm.integrations.custom_logger"),
        "litellm.integrations.additional_logging_utils": types.ModuleType("litellm.integrations.additional_logging_utils"),
    }
    stubs["litellm.integrations.custom_logger"].CustomLogger = CustomLogger
    stubs["litellm.integrations.additional_logging_utils"].AdditionalLoggingUtils = AdditionalLoggingUtils
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
            "user_api_key_user_id": "litellm-user-1",
            "user_api_key_team_id": "litellm-team-1",
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
        # The LiteLLM user and Team the console checks, and the cluster the console reads.
        self.assertEqual((first["litellm_user_id"], first["litellm_team_id"]), ("litellm-user-1", "litellm-team-1"))
        self.assertIsNone(first.get("cluster"))
        self.assertEqual(CONTENT.records(payload(messages), cluster="example")[0]["cluster"], "example")
        self.assertRegex(first["time"], r"^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$")
        self.assertEqual(CONTENT.records(payload(messages, endTime=0.5 + 1767225600))[0]["time"], "2026-01-01T00:00:00.500Z")

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

    def test_callback_hands_records_to_the_writer(self):
        received = []
        log = CONTENT.ContentLog(received.append, cluster="example", log=lambda message: None)
        kwargs = {"standard_logging_object": payload([{"role": "user", "content": "Hello"}])}
        asyncio.run(log.async_log_success_event(kwargs, None, None, None))
        asyncio.run(log.async_log_failure_event({"standard_logging_object": None}, None, None, None))
        self.assertEqual(len(received), 1)
        self.assertEqual([r["kind"] for r in received[0]], ["request", "response"])
        self.assertEqual({r["cluster"] for r in received[0]}, {"example"})

    def test_calls_litellm_makes_itself_are_not_stored(self):
        received = []
        log = CONTENT.ContentLog(received.append, log=lambda message: None)
        messages = [{"role": "user", "content": "Hello"}]
        probe = payload(messages)
        probe["metadata"]["user_api_key_team_id"] = "litellm-internal-health-check"
        asyncio.run(log.async_log_success_event({"standard_logging_object": probe}, None, None, None))
        for name in ("metadata", "litellm_metadata"):
            classifier = {"standard_logging_object": payload(messages),
                          "litellm_params": {name: {"internal_call_origin": "autorouter_classifier"}}}
            asyncio.run(log.async_log_success_event(classifier, None, None, None))
        self.assertEqual(received, [])
        # A client's call with LiteLLM metadata but no origin is stored.
        asyncio.run(log.async_log_success_event({"standard_logging_object": payload(messages),
                                                  "litellm_params": {"metadata": {"other": "x"}}}, None, None, None))
        self.assertEqual(len(received), 1)

    def test_a_record_that_cannot_be_built_does_not_fail_the_call(self):
        logged = []
        log = CONTENT.ContentLog(lambda found: self.fail("nothing to write"), log=logged.append)
        # A metadata value too long for any record.
        kwargs = {"standard_logging_object": payload("x", model_group="m" * (CONTENT.MAX_RECORD_BYTES + 1))}
        asyncio.run(log.async_log_success_event(kwargs, None, None, None))
        self.assertIn("could not build the records", logged[0])


class Ingest:
    """A stand-in QueuedIngestClient: records what it is sent; fails the first `failures` sends."""

    def __init__(self, failures=0):
        self.failures = failures
        self.bodies = []

    def ingest_from_stream(self, stream, ingestion_properties=None):
        if self.failures:
            self.failures -= 1
            raise RuntimeError("synthetic ingestion failure")
        self.bodies.append((stream.read().decode("utf-8"), ingestion_properties))


def writer(client, **options):
    logged = []
    options.setdefault("sleep", lambda seconds: None)
    return CONTENT.KustoWriter(client, "CetiRunContent", log=logged.append, ingestion_properties="properties", **options), logged


class WriterTests(unittest.TestCase):
    def test_batches_are_json_lines_within_the_limits(self):
        client = Ingest()
        write, _ = writer(client, batch_records=3, batch_seconds=0.01)
        records = CONTENT.records(payload([{"role": "user", "content": "Hello"}]))
        write(records)
        write(records)
        write(records)
        while not write.queue.empty():
            write.send(write.next_batch(0.01))
        lines = [json.loads(line) for body, _ in client.bodies for line in body.splitlines()]
        self.assertEqual(len(lines), 6)
        self.assertEqual([len(body.splitlines()) for body, _ in client.bodies], [3, 3])
        self.assertTrue(all(properties == "properties" for _, properties in client.bodies))
        self.assertEqual(write.sent, 6)

    def test_a_full_queue_drops_whole_calls(self):
        write, logged = writer(Ingest(), capacity=3)
        records = CONTENT.records(payload([{"role": "user", "content": "Hello"}]))
        write(records)  # 2 records
        write(records)  # would make 4: dropped whole
        self.assertEqual(write.queue.qsize(), 2)
        self.assertEqual(write.dropped, 2)
        self.assertIn("queue is full", logged[0])

    def test_a_refused_batch_is_retried_then_dropped(self):
        sleeps = []
        client = Ingest(failures=2)
        write, logged = writer(client, sleep=sleeps.append)
        self.assertTrue(write.send([{"a": 1}]))
        self.assertEqual(sleeps, [2, 4])
        self.assertEqual(len(client.bodies), 1)

        client = Ingest(failures=3)
        write, logged = writer(client, sleep=lambda seconds: None)
        self.assertFalse(write.send([{"a": 1}, {"b": 2}]))
        self.assertEqual(write.dropped, 2)
        self.assertIn("refused a batch of 2 records 3 times", logged[0])
        self.assertNotIn("synthetic ingestion failure" + "x", logged[0])

    def test_the_ingestion_endpoint_comes_from_the_query_uri(self):
        self.assertEqual(CONTENT.ingest_uri("https://example.australiaeast.kusto.windows.net/"),
                         "https://ingest-example.australiaeast.kusto.windows.net")


class Row(dict):
    def to_dict(self):
        return dict(self)


class Query:
    """A stand-in KustoClient: returns rows and records the query and its parameters."""

    def __init__(self, rows):
        self.rows = rows
        self.calls = []

    def execute_query(self, database, query, properties):
        self.calls.append((database, query, properties))
        return types.SimpleNamespace(primary_results=[[Row(row) for row in self.rows]])


class Properties:
    def __init__(self):
        self.parameters = {}

    def set_parameter(self, name, value):
        self.parameters[name] = value


def stored(kind, text, complete=True):
    return {"Kind": kind, "Text": text, "Complete": complete, "LiteLLMUserId": "litellm-user-1", "LiteLLMTeamId": "",
            "CallId": "call-1", "RequestId": "chatcmpl-123", "Model": "gpt-example", "Status": "success",
            "Timestamp": "2026-11-10T00:00:00Z"}


class ReaderTests(unittest.TestCase):
    def test_the_console_gets_messages_response_and_owner(self):
        client = Query([stored("request", json.dumps([{"role": "user", "content": "Hello"}])),
                        stored("response", "plain answer")])
        log = CONTENT.ContentLog(lambda found: None, CONTENT.KustoReader(client, "CetiRunContent", "example", Properties))
        result = asyncio.run(log.get_request_response_payload("chatcmpl-123", None, None))
        self.assertEqual(result["messages"], [{"role": "user", "content": "Hello"}])
        self.assertEqual(result["response"], "plain answer")
        # What LiteLLM's ownership check reads; an empty Team is no Team.
        self.assertEqual(result["metadata"], {"user_api_key_user_id": "litellm-user-1", "user_api_key_team_id": None})
        self.assertTrue(result["complete"])
        database, query, properties = client.calls[0]
        self.assertEqual(database, "CetiRunContent")
        # The request ID from the console's URL is a query parameter, never part of the query text.
        self.assertEqual(properties.parameters, {"request_id": "chatcmpl-123", "cluster": "example"})
        self.assertNotIn("chatcmpl-123", query)
        self.assertTrue(query.startswith("declare query_parameters(request_id: string, cluster: string);"))
        # The call's rows are filtered before their parts are joined, not after.
        self.assertIn("let rows = Content\n    | where Cluster == cluster and "
                      "(Body.request_id == request_id or Body.call_id == request_id);\nGatewayContentOf(rows)\n", query)
        self.assertNotIn("GatewayContent()", query)
        self.assertNotIn("Timestamp between", query)

    def test_a_time_range_from_the_console_limits_the_rows_read(self):
        client = Query([stored("request", "[]")])
        reader = CONTENT.KustoReader(client, "db", "example", Properties)
        start = datetime.datetime(2026, 11, 10, 8, 0, tzinfo=datetime.timezone.utc)
        end = datetime.datetime(2026, 11, 10, 9, 0)  # naive: LiteLLM's are UTC
        log = CONTENT.ContentLog(lambda found: None, reader)
        asyncio.run(log.get_request_response_payload("chatcmpl-123", start, end))
        _, query, properties = client.calls[0]
        self.assertIn("from_time: datetime, to_time: datetime", query)
        self.assertIn("let rows = Content\n    | where Timestamp between (from_time .. to_time)\n", query)
        self.assertEqual(properties.parameters["from_time"], "2026-11-09T08:00:00+00:00")
        self.assertEqual(properties.parameters["to_time"], "2026-11-11T09:00:00+00:00")
        # Only one end given: a window around it.
        reader("chatcmpl-123", None, end)
        self.assertEqual((client.calls[1][2].parameters["from_time"], client.calls[1][2].parameters["to_time"]),
                         ("2026-11-09T09:00:00+00:00", "2026-11-11T09:00:00+00:00"))

    def test_a_call_without_its_request_falls_back_to_the_spend_log(self):
        log = CONTENT.ContentLog(lambda found: None, CONTENT.KustoReader(Query([stored("response", "x")]), "db", "example", Properties))
        self.assertIsNone(asyncio.run(log.get_request_response_payload("chatcmpl-123", None, None)))
        log = CONTENT.ContentLog(lambda found: None, CONTENT.KustoReader(Query([]), "db", "example", Properties))
        self.assertIsNone(asyncio.run(log.get_request_response_payload("chatcmpl-123", None, None)))

    def test_a_partly_received_call_says_so(self):
        client = Query([stored("request", "[]", complete=False)])
        result = CONTENT.KustoReader(client, "db", "example", Properties)("chatcmpl-123")
        self.assertFalse(result["complete"])

    def test_a_failed_query_falls_back_to_the_spend_log(self):
        class Broken:
            def execute_query(self, *args):
                raise RuntimeError("synthetic query failure")

        logged = []
        log = CONTENT.ContentLog(lambda found: None, CONTENT.KustoReader(Broken(), "db", "example", Properties), log=logged.append)
        self.assertIsNone(asyncio.run(log.get_request_response_payload("chatcmpl-123", None, None)))
        self.assertIn("could not read", logged[0])
        # No reader, or no request ID: nothing to look up.
        self.assertIsNone(asyncio.run(CONTENT.ContentLog(lambda found: None).get_request_response_payload("x", None, None)))
        self.assertIsNone(asyncio.run(log.get_request_response_payload("", None, None)))


class EnvironmentTests(unittest.TestCase):
    def test_off_without_an_endpoint(self):
        self.assertIsNone(CONTENT.from_environment({}))

    def test_an_endpoint_needs_the_database_and_cluster(self):
        with self.assertRaisesRegex(SystemExit, "needs CETIRUN_CONTENT_DATABASE and CETIRUN_CONTENT_CLUSTER"):
            CONTENT.from_environment({"CETIRUN_CONTENT_ENDPOINT": "https://example.kusto.windows.net"})


if __name__ == "__main__":
    unittest.main()
