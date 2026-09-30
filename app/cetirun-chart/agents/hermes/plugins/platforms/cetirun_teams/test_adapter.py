# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Unit tests for cetirun_teams serviceUrl persistence and standalone send.

These import gateway.* (via adapter.py) so they only run inside the Hermes
image, e.g.:

    python -m pytest agents/hermes/plugins/platforms/cetirun_teams/test_adapter.py -v
"""

import asyncio
import inspect
import json
from collections import OrderedDict
from unittest.mock import AsyncMock

import pytest

from . import adapter as A
from . import create_adapter, is_connected, register, standalone_send

TRUSTED = "https://smba.trafficmanager.net/teams/"
CONV = "19:abc123@thread.tacv2;messageid=1000000000001"
CONV_BASE = "19:abc123@thread.tacv2"
OTHER_THREAD = "19:abc123@thread.tacv2;messageid=1000000000002"


def test_registers_through_plugin_context():
    registered = {}

    class Context:
        def register_platform(self, **kwargs):
            registered.update(kwargs)

    register(Context())
    assert registered["name"] == "cetirun_teams"
    assert registered["adapter_factory"] is create_adapter
    assert registered["standalone_sender_fn"] is standalone_send
    assert registered["validate_config"] is is_connected
    assert registered["is_connected"] is is_connected


def test_factory_creates_the_adapter_when_called(monkeypatch):
    config = object()
    adapter = object()

    def factory(received):
        assert received is config
        return adapter

    monkeypatch.setattr(A, "CetiRunTeamsAdapter", factory)
    assert create_adapter(config) is adapter


def test_standalone_factory_forwards_the_send_contract(monkeypatch):
    config = object()
    sender = AsyncMock(return_value={"success": True})
    monkeypatch.setattr(A, "_standalone_send", sender)
    result = asyncio.run(standalone_send(config, CONV, "hello", thread_id="thread"))
    assert result == {"success": True}
    sender.assert_awaited_once_with(config, CONV, "hello", thread_id="thread")


def test_platform_is_not_connected_without_an_app_id(monkeypatch):
    monkeypatch.delenv("MSTEAMS_APP_ID", raising=False)
    config = type("Config", (), {"extra": {}})()
    assert not is_connected(config)
    config.extra["app_id"] = "configured-app"
    assert is_connected(config)


def test_adapter_signatures_match_current_hermes_contract():
    connect = inspect.signature(A.CetiRunTeamsAdapter.connect)
    send_typing = inspect.signature(A.CetiRunTeamsAdapter.send_typing)
    assert connect.parameters["is_reconnect"].kind is inspect.Parameter.KEYWORD_ONLY
    assert "metadata" in send_typing.parameters


@pytest.fixture
def state_file(tmp_path, monkeypatch):
    path = tmp_path / "service_urls.json"
    monkeypatch.setenv(A.SERVICE_URL_STATE_ENV, str(path))
    return path


def test_channel_base_strips_messageid():
    assert A._channel_base(CONV) == CONV_BASE
    assert A._channel_base(CONV_BASE) == CONV_BASE  # idempotent


def test_is_trusted_service_url():
    assert A._is_trusted_service_url(TRUSTED)
    assert A._is_trusted_service_url("https://x.botframework.com/")
    assert not A._is_trusted_service_url("https://evil.example.com/")
    assert not A._is_trusted_service_url("http://smba.trafficmanager.net/")  # not https
    assert not A._is_trusted_service_url("")


def test_conv_id_regex_accepts_messageid_suffix():
    assert A.CONV_ID_RE.match(CONV)        # the ;messageid= form must pass
    assert A.CONV_ID_RE.match(CONV_BASE)
    assert not A.CONV_ID_RE.match("19:abc/../../etc")  # path traversal blocked
    assert not A.CONV_ID_RE.match("19:abc id")          # space blocked
    assert not A.CONV_ID_RE.match(CONV + "\n")          # trailing newline blocked


def test_persist_writes_both_keys(state_file):
    A._persist_service_url(CONV, TRUSTED)
    data = json.loads(state_file.read_text())["service_urls"]
    assert data[CONV] == TRUSTED
    assert data[CONV_BASE] == TRUSTED


def test_persist_is_atomic_and_reloadable(state_file):
    A._persist_service_url(CONV, TRUSTED)
    loaded = A._load_service_url_state()
    assert loaded[CONV] == TRUSTED
    assert loaded[CONV_BASE] == TRUSTED


def test_load_missing_file_returns_empty(state_file):
    assert A._load_service_url_state() == {}


def test_load_corrupt_file_returns_empty(state_file):
    state_file.write_text("{not valid json")
    assert A._load_service_url_state() == {}


def test_load_non_dict_json_returns_empty(state_file):
    # Valid JSON that isn't an object must not raise (json.load succeeds,
    # then .get() would AttributeError without the isinstance guard).
    state_file.write_text("[1, 2, 3]")
    assert A._load_service_url_state() == {}


def test_resolve_channel_base_fallback_from_disk(state_file):
    # Only the original thread was ever seen, persisted under conv + base.
    A._persist_service_url(CONV, TRUSTED)
    # A *different* thread in the same channel resolves via the channel base.
    assert A._resolve_service_url(OTHER_THREAD) == TRUSTED


def test_resolve_prefers_memory_cache(state_file):
    mem = {OTHER_THREAD: TRUSTED}
    assert A._resolve_service_url(OTHER_THREAD, mem) == TRUSTED


def test_resolve_rejects_untrusted_persisted_url(state_file):
    # A poisoned state file must not be trusted.
    state_file.write_text(json.dumps({"service_urls": {CONV: "https://evil.example.com/"}}))
    assert A._resolve_service_url(CONV) is None


def test_persist_lru_eviction(state_file, monkeypatch):
    monkeypatch.setattr(A, "SERVICE_URL_CACHE_MAX", 4)
    for i in range(10):
        A._persist_service_url(f"19:c{i}@thread.tacv2;messageid={i}", TRUSTED)
    assert len(A._load_service_url_state()) <= 4


def test_persist_concurrent_writers_merge(state_file):
    # Two writers learning different conversations must both survive the
    # locked read-modify-write (no lost update).
    import threading

    def writer(n):
        A._persist_service_url(f"19:c{n}@thread.tacv2;messageid={n}", TRUSTED)

    threads = [threading.Thread(target=writer, args=(n,)) for n in range(20)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()

    state = A._load_service_url_state()
    for n in range(20):
        assert state.get(f"19:c{n}@thread.tacv2") == TRUSTED


# --- _standalone_send -------------------------------------------------------

class _FakeResp:
    def __init__(self, status_code, payload=None, text=""):
        self.status_code = status_code
        self._payload = payload or {}
        self.text = text

    def json(self):
        return self._payload


class _FakeClient:
    def __init__(self, resp):
        self._resp = resp
        self.posted = []

    async def __aenter__(self):
        return self

    async def __aexit__(self, *a):
        return False

    async def post(self, url, json=None, headers=None):
        self.posted.append({"url": url, "json": json, "headers": headers})
        return self._resp


def _patch_token(monkeypatch):
    async def fake_get_token(self, scope=A.BOT_FRAMEWORK_SCOPE):
        return "fake-token"
    monkeypatch.setattr(A.BotTokenProvider, "get_token", fake_get_token)


def test_standalone_send_missing_chat_id(state_file):
    r = asyncio.run(A._standalone_send(None, "", "hi"))
    assert "chat_id is required" in r["error"]


def test_standalone_send_rejects_bad_chat_id(state_file):
    r = asyncio.run(A._standalone_send(None, "19:abc/../x", "hi"))
    assert "outside the Bot Framework" in r["error"]


def test_standalone_send_no_service_url_learned(state_file):
    r = asyncio.run(A._standalone_send(None, CONV, "hi"))
    assert "no serviceUrl learned" in r["error"]


def test_standalone_send_success_via_channel_base(state_file, monkeypatch):
    # Learn serviceUrl from one thread, deliver to a different thread.
    A._persist_service_url(CONV, TRUSTED)
    _patch_token(monkeypatch)
    fake = _FakeClient(_FakeResp(201, {"id": "msg-1"}))
    monkeypatch.setattr(A.httpx, "AsyncClient", lambda *a, **k: fake)

    r = asyncio.run(A._standalone_send(None, OTHER_THREAD, "hello"))
    assert r == {"success": True, "message_id": "msg-1"}
    # URL must target the exact (full) conversation id, host from channel base.
    assert fake.posted[0]["url"] == f"{TRUSTED.rstrip('/')}/v3/conversations/{OTHER_THREAD}/activities"
    assert fake.posted[0]["headers"]["Authorization"] == "Bearer fake-token"


def test_standalone_send_http_error(state_file, monkeypatch):
    A._persist_service_url(CONV, TRUSTED)
    _patch_token(monkeypatch)
    fake = _FakeClient(_FakeResp(403, text="Forbidden"))
    monkeypatch.setattr(A.httpx, "AsyncClient", lambda *a, **k: fake)

    r = asyncio.run(A._standalone_send(None, CONV, "hello"))
    assert "HTTP 403" in r["error"]


def test_standalone_send_truncates_long_message(state_file, monkeypatch):
    A._persist_service_url(CONV, TRUSTED)
    _patch_token(monkeypatch)
    fake = _FakeClient(_FakeResp(200, {"id": "x"}))
    monkeypatch.setattr(A.httpx, "AsyncClient", lambda *a, **k: fake)

    asyncio.run(A._standalone_send(None, CONV, "z" * (A.MAX_MESSAGE_LENGTH + 500)))
    sent = fake.posted[0]["json"]["text"]
    assert len(sent) <= A.MAX_MESSAGE_LENGTH
    assert sent.endswith(A.TRUNCATION_SUFFIX)


# --- live _post_activity chat_id validation ---------------------------------

def test_post_activity_rejects_malformed_chat_id():
    # The live send path must reject path-traversal chat_ids before building
    # the /v3/conversations/<id>/activities URL, matching the standalone path.
    adapter = A.CetiRunTeamsAdapter.__new__(A.CetiRunTeamsAdapter)
    adapter._service_urls = OrderedDict({A._channel_base(CONV): TRUSTED})

    class _Boom:
        async def post(self, *a, **k):
            raise AssertionError("should not POST a malformed chat_id")

    adapter._http = _Boom()
    bad = "19:abc@thread.tacv2;messageid=../../foo"
    with pytest.raises(ValueError):
        asyncio.run(adapter._post_activity(bad, {"type": "message"}))


def test_post_activity_warms_cache_from_disk(state_file, monkeypatch):
    # A disk-only hit must populate the in-memory cache so the next send
    # doesn't re-read disk.
    A._persist_service_url(CONV, TRUSTED)
    _patch_token(monkeypatch)
    adapter = A.CetiRunTeamsAdapter.__new__(A.CetiRunTeamsAdapter)
    adapter._service_urls = OrderedDict()  # cold memory cache
    adapter._token_provider = A.BotTokenProvider()

    posted = []

    class _Client:
        async def post(self, url, json=None, headers=None):
            posted.append(url)
            return _FakeResp(201, {"id": "x"})

    adapter._http = _Client()
    asyncio.run(adapter._post_activity(CONV, {"type": "message"}))
    # serviceUrl now warmed in memory under both keys.
    assert adapter._service_urls.get(CONV) == TRUSTED
    assert adapter._service_urls.get(CONV_BASE) == TRUSTED
