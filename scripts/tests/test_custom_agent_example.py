# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

import importlib.util
import http.client
import json
import io
import os
import re
import secrets
import signal
from pathlib import Path
import shutil
import socket
import ssl
import struct
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from unittest.mock import Mock, patch
import urllib.error
import urllib.request
from http.server import ThreadingHTTPServer


ROOT = Path(__file__).resolve().parents[2]
EXAMPLE = ROOT / "examples/custom-agent"


def load(name):
    spec = importlib.util.spec_from_file_location(name, EXAMPLE / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


AGENT = load("agent")
GATEWAY = load("mock_gateway")


class CustomExampleTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.home = Path(self.temp.name)
        shutil.copytree(EXAMPLE / "context", self.home / ".agents")
        self.nonces = []
        for path in (self.home / ".agents/AGENTS.md", *sorted((self.home / ".agents/skills").glob("*/SKILL.md"))):
            nonce = secrets.token_hex(16)
            path.write_text(path.read_text() + "\n" + nonce)
            self.nonces.append(nonce)
        self.gateway = GATEWAY.make_server(expected_context=self.nonces)
        self.start_server(self.gateway)
        self.gateway_base = f"http://127.0.0.1:{self.gateway.server_port}/v1"
        self.agent = AGENT.Agent(self.home, self.gateway_base, "sk-dummy")
        self.server = AGENT.AgentHTTPServer(("127.0.0.1", 0), AGENT.make_handler(self.agent))
        self.start_server(self.server)
        self.base = f"http://127.0.0.1:{self.server.server_port}"
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def start_server(self, server):
        thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.02}, daemon=True)
        thread.start()
        self.addCleanup(thread.join, 2)
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)

    def request(self, path, body=None):
        request = urllib.request.Request(self.base + path,
            data=None if body is None else json.dumps(body).encode(),
            headers={"Content-Type": "application/json"})
        return self.opener.open(request, timeout=5)

    def completion(self, prompt="example", stream=False):
        return self.request("/v1/chat/completions", {
            "model": "custom-example", "messages": [{"role": "user", "content": prompt}], "stream": stream})

    def test_health_and_inbound_models_do_not_call_gateway(self):
        with self.request("/healthz") as response:
            self.assertEqual(response.status, 200)
            self.assertEqual(json.load(response), {"status": "ok"})
        with self.request("/v1/models") as response:
            self.assertEqual(json.load(response), {"object": "list", "data": [{"id": "custom-example", "object": "model"}]})
        self.assertEqual(self.gateway.requests, [])

    def test_nonstream_reads_all_context_and_writes_private_memory(self):
        with self.completion() as response:
            body = json.load(response)
        self.assertEqual(body["choices"][0]["message"]["content"], GATEWAY.MARKER)
        posts = [request for request in self.gateway.requests if request[0] == "POST"]
        self.assertEqual(len(posts), 1)
        self.assertEqual(posts[0][2]["model"], "mock-upstream-model")
        self.assertIn(GATEWAY.MARKER, posts[0][2]["messages"][0]["content"])
        for nonce in self.nonces:
            self.assertIn(nonce, posts[0][2]["messages"][0]["content"])
        self.assertEqual(json.loads((self.home / "memory/last-result.json").read_text()), body)
        for path in (self.home / "memory").iterdir():
            self.assertNotIn("sk-dummy", path.read_text())

    def test_stream_completes_without_replay(self):
        with self.completion(stream=True) as response:
            stream = response.read().decode()
        self.assertIn(GATEWAY.MARKER, stream)
        self.assertEqual(stream.count("[DONE]"), 1)
        events = [line[6:] for line in stream.splitlines() if line.startswith("data: ")]
        self.assertEqual(events[-1], "[DONE]")
        chunks = [json.loads(event) for event in events[:-1]]
        self.assertEqual(len({chunk["id"] for chunk in chunks}), 1)
        for chunk in chunks:
            self.assertEqual(chunk["object"], "chat.completion.chunk")
            self.assertIsInstance(chunk["created"], int)
            self.assertEqual(chunk["model"], "custom-example")
            self.assertEqual(chunk["choices"][0]["index"], 0)
        self.assertEqual(chunks[-1]["choices"][0]["finish_reason"], "stop")
        self.assertEqual(chunks[-1]["choices"][0]["delta"], {})
        self.assertEqual(sum(method == "POST" for method, _, _ in self.gateway.requests), 1)

    def test_interrupted_stream_is_not_replayed_or_falsely_completed(self):
        with self.completion("interrupt", stream=True) as response:
            stream = response.read().decode()
        self.assertIn(GATEWAY.MARKER, stream)
        self.assertNotIn("[DONE]", stream)
        self.assertEqual(sum(method == "POST" for method, _, _ in self.gateway.requests), 1)

    def test_error_is_not_retried_and_does_not_expose_key(self):
        with self.assertRaises(urllib.error.HTTPError) as result:
            self.completion("error")
        self.assertEqual(result.exception.code, 502)
        self.assertNotIn(b"sk-dummy", result.exception.read())
        result.exception.close()
        self.assertEqual(sum(method == "POST" for method, _, _ in self.gateway.requests), 1)

    def test_unsupported_extensions_fail_before_gateway_call(self):
        for extension in ({"tools": []}, {"tool_choice": "required"}, {"temperature": 0.5}):
            with self.subTest(extension=extension):
                body = {"model": "custom-example", "messages": [{"role": "user", "content": "example"}], **extension}
                with self.assertRaises(urllib.error.HTTPError) as result:
                    self.request("/v1/chat/completions", body)
                self.assertEqual(result.exception.code, 400)
                result.exception.close()
        self.assertEqual(self.gateway.requests, [])

    def test_cancel_closes_upstream_without_replay(self):
        body = json.dumps({"model": "custom-example", "messages": [{"role": "user", "content": "cancel"}], "stream": True}).encode()
        client = socket.create_connection(("127.0.0.1", self.server.server_port), timeout=5)
        client.sendall(b"POST /v1/chat/completions HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: " +
            str(len(body)).encode() + b"\r\n\r\n" + body)
        self.assertTrue(self.gateway.stream_started.wait(2))
        client.recv(4096)
        client.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER, struct.pack("ii", 1, 0))
        client.close()
        self.gateway.release_stream.set()
        self.assertTrue(self.gateway.disconnected.wait(3))
        self.assertEqual(sum(method == "POST" for method, _, _ in self.gateway.requests), 1)

    def test_cancel_closes_quiet_upstream_in_all_response_phases(self):
        for prompt in ("quiet-before-headers", "quiet-nonstream", "quiet-stream"):
            with self.subTest(prompt=prompt):
                self.gateway.stream_started.clear()
                self.gateway.disconnected.clear()
                before = len(self.gateway.requests)
                body = json.dumps({"model": "custom-example", "messages": [{"role": "user", "content": prompt}],
                    "stream": prompt != "quiet-nonstream"}).encode()
                client = socket.create_connection(("127.0.0.1", self.server.server_port), timeout=3)
                try:
                    client.sendall(b"POST /v1/chat/completions HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: " +
                        str(len(body)).encode() + b"\r\n\r\n" + body)
                    self.assertTrue(self.gateway.stream_started.wait(1))
                    client.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER, struct.pack("ii", 1, 0))
                finally:
                    client.close()
                self.assertTrue(self.gateway.disconnected.wait(1), "cancellation waited for upstream output or timeout")
                self.assertEqual(sum(method == "POST" for method, _, _ in self.gateway.requests[before:]), 1)
                self.assertFalse((self.home / "memory/last-result.json").exists())

    def test_cancel_interrupts_silent_tls_handshake(self):
        listener = socket.socket()
        listener.bind(("127.0.0.1", 0))
        listener.listen(1)
        listener.settimeout(2)
        self.addCleanup(listener.close)
        downstream, client = socket.socketpair()
        self.addCleanup(downstream.close)
        self.addCleanup(client.close)
        self.agent.base_url = f"https://127.0.0.1:{listener.getsockname()[1]}/v1"
        outcome = []
        with AGENT.CallContext(downstream) as calls:
            def request():
                try:
                    with self.agent.gateway("/models", calls=calls):
                        outcome.append("unexpected response")
                except Exception as error:
                    outcome.append(type(error).__name__)
            thread = threading.Thread(target=request, daemon=True)
            thread.start()
            upstream, _ = listener.accept()
            self.addCleanup(upstream.close)
            upstream.settimeout(2)
            hello = upstream.recv(8192)
            self.assertTrue(hello and hello[0] == 0x16, "client did not begin TLS")
            client.close()
            self.assertTrue(calls.cancelled.wait(1))
            thread.join(1)
            self.assertFalse(thread.is_alive(), "TLS handshake outlived client cancellation")
            self.assertEqual(upstream.recv(1), b"")
        self.assertTrue(outcome)
        self.assertNotIn("unexpected response", outcome)

    def test_https_keeps_certificate_and_hostname_verification(self):
        connection = AGENT.GatewayConnection(AGENT.urlsplit("https://gateway.example.com/v1"), None)
        self.assertEqual(connection.port, 443)
        self.assertGreaterEqual(connection.tls.minimum_version, ssl.TLSVersion.TLSv1_2)
        self.assertEqual(connection.tls.verify_mode, ssl.CERT_REQUIRED)
        self.assertTrue(connection.tls.check_hostname)
        connection.close()

    def test_https_sets_tls12_minimum(self):
        for calls in (None, Mock()):
            for minimum in (ssl.TLSVersion.MINIMUM_SUPPORTED, ssl.TLSVersion.TLSv1,
                            ssl.TLSVersion.TLSv1_1, ssl.TLSVersion.TLSv1_2):
                with self.subTest(cancellable=calls is not None, minimum=minimum):
                    context = Mock(spec=ssl.SSLContext, minimum_version=minimum,
                        maximum_version=ssl.TLSVersion.MAXIMUM_SUPPORTED,
                        verify_mode=ssl.CERT_REQUIRED, check_hostname=True)
                    with patch.object(AGENT.ssl, "create_default_context", return_value=context):
                        connection = AGENT.GatewayConnection(AGENT.urlsplit("https://gateway.example.com/v1"), calls)
                    self.assertIs(connection.tls, context)
                    self.assertEqual(context.minimum_version, ssl.TLSVersion.TLSv1_2)
                    self.assertEqual(context.maximum_version, ssl.TLSVersion.MAXIMUM_SUPPORTED)
                    self.assertEqual(context.verify_mode, ssl.CERT_REQUIRED)
                    self.assertTrue(context.check_hostname)
                    connection.close()

    def test_http_does_not_create_tls_context(self):
        with patch.object(AGENT.ssl, "create_default_context") as create_context:
            connection = AGENT.GatewayConnection(AGENT.urlsplit("http://gateway.example.com/v1"), None)
        create_context.assert_not_called()
        self.assertIsNone(connection.tls)
        self.assertEqual(connection.port, 80)
        connection.close()

    def test_cancel_closes_pending_tcp_socket(self):
        calls = AGENT.CallContext(None)
        pending = Mock()
        pending.connect_ex.side_effect = lambda _address: calls.cancelled.set() or AGENT.errno.EINPROGRESS
        addresses = [(socket.AF_INET, socket.SOCK_STREAM, 0, "", ("127.0.0.1", 8080))]
        with patch.object(AGENT.socket, "getaddrinfo", return_value=addresses), \
                patch.object(AGENT.socket, "socket", return_value=pending):
            connection = AGENT.GatewayConnection(AGENT.urlsplit("http://gateway.example.com/v1"), calls)
            with self.assertRaises(AGENT.RequestCancelled):
                connection.connect()
        pending.close.assert_called_once()

    def test_headless_once_uses_same_environment_and_context(self):
        env = dict(os.environ, HOME=str(self.home), OPENAI_BASE_URL=self.gateway_base, OPENAI_API_KEY="sk-dummy",
            PYTHONDONTWRITEBYTECODE="1")
        env.pop("PORT", None)
        result = subprocess.run([sys.executable, str(EXAMPLE / "agent.py"), "--headless-once"],
            env=env, capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((self.home / "memory/last-result.json").is_file())
        self.assertNotIn("sk-dummy", result.stdout + result.stderr)

    def test_example_skill_is_optional_and_other_skills_are_discovered(self):
        shutil.rmtree(self.home / ".agents/skills/example")
        extra = self.home / ".agents/skills/another"
        extra.mkdir()
        nonce = secrets.token_hex(16)
        (extra / "SKILL.md").write_text(nonce)
        agent = AGENT.Agent(self.home, self.gateway_base, "sk-dummy")
        self.assertIn(nonce, agent.context)
        self.assertIn("last-result.json", agent.context)

    def test_busy_server_returns_429_without_upstream_work(self):
        self.server.slots = threading.BoundedSemaphore(1)
        self.server.slots.acquire()
        try:
            # Admission rejects before reading a request; an upload can race
            # the close and raise BrokenPipeError before urllib reads the 429.
            with patch.object(self.server, "finish_request", wraps=self.server.finish_request) as handler:
                with socket.create_connection(("127.0.0.1", self.server.server_port), timeout=5) as connection:
                    with http.client.HTTPResponse(connection) as response:
                        response.begin()
                        self.assertEqual(response.status, 429)
                        self.assertEqual(response.getheader("Content-Type"), "application/json")
                        self.assertEqual(response.getheader("Connection"), "close")
                        self.assertEqual(json.load(response), {
                            "error": {"type": "rate_limit_error", "message": "Concurrent request limit"}})
                handler.assert_not_called()
            self.assertEqual(self.gateway.requests, [])
        finally:
            self.server.slots.release()

    def test_dockerfile_checks_each_tool_without_building_an_image(self):
        source = (EXAMPLE / "Dockerfile").read_text()
        loop = re.search(r"for tool in [^\n]+; done", source).group(0)
        result = subprocess.run(["sh", "-c", 'command() { [ "$2" != cp ]; }\n' + loop],
            capture_output=True, text=True, timeout=3)
        self.assertNotEqual(result.returncode, 0, "missing middle tool was ignored")

    def test_response_size_and_protocol_errors(self):
        for response in (io.BytesIO(b"x" * (AGENT.MAX_RESPONSE_BYTES + 1)), io.BytesIO(b"[]"), io.BytesIO(b"not json")):
            with self.assertRaises(AGENT.GatewayProtocolError):
                AGENT.read_gateway_json(response)

    def test_oversized_sse_event_is_not_forwarded_as_success(self):
        with self.completion("oversized-event", stream=True) as response:
            content = response.read()
        self.assertNotIn(b"[DONE]", content)
        self.assertLessEqual(len(content), AGENT.MAX_SSE_EVENT_BYTES)
        self.assertEqual(sum(method == "POST" for method, _, _ in self.gateway.requests), 1)

    def test_large_request_is_rejected_before_reading_or_calling_gateway(self):
        client = socket.create_connection(("127.0.0.1", self.server.server_port), timeout=3)
        with client:
            client.sendall(b"POST /v1/chat/completions HTTP/1.1\r\nHost: localhost\r\nContent-Length: 1048577\r\n\r\n")
            self.assertIn(b"400", client.recv(4096).split(b"\r\n")[0])
        self.assertEqual(self.gateway.requests, [])

    @unittest.skipUnless(sys.platform.startswith("linux"), "requires Linux high-fd sockets")
    def test_high_descriptor_requests_are_valid(self):
        import fcntl
        original = socket.socket
        def high_socket(*args, **kwargs):
            sock = original(*args, **kwargs)
            fd = fcntl.fcntl(sock.fileno(), fcntl.F_DUPFD, 1100)
            sock.close()
            return original(fileno=fd)
        with patch.object(AGENT.socket, "socket", side_effect=high_socket):
            with self.completion() as response:
                self.assertEqual(response.status, 200)
                self.assertEqual(json.load(response)["object"], "chat.completion")
        self.assertEqual(sum(method == "POST" for method, _, _ in self.gateway.requests), 1)

    def start_process(self, port=None):
        env = dict(os.environ, HOME=str(self.home), OPENAI_BASE_URL=self.gateway_base, OPENAI_API_KEY="sk-dummy",
            PYTHONDONTWRITEBYTECODE="1")
        env.pop("PORT", None)
        if port is not None:
            env["PORT"] = str(port)
        process = subprocess.Popen([sys.executable, str(EXAMPLE / "agent.py")], env=env,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        def cleanup():
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(3)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(3)
            process.communicate()
        self.addCleanup(cleanup)
        return process

    @unittest.skipUnless(sys.platform.startswith("linux"), "uses procfs only for the owned child")
    def test_main_listener_and_sigterm(self):
        with socket.socket() as reserved:
            reserved.bind(("127.0.0.1", 0))
            port = reserved.getsockname()[1]
        process = self.start_process(port)
        deadline = time.monotonic() + 5
        while True:
            try:
                with self.opener.open(f"http://127.0.0.1:{port}/healthz", timeout=.2) as response:
                    self.assertEqual(json.load(response), {"status": "ok"})
                break
            except urllib.error.URLError:
                if time.monotonic() > deadline or process.poll() is not None:
                    self.fail("example HTTP process did not start")
                time.sleep(.02)
        rows = Path(f"/proc/{process.pid}/net/tcp").read_text().splitlines()
        self.assertTrue(any(row.split()[1] == f"00000000:{port:04X}" and row.split()[3] == "0A" for row in rows[1:]))
        process.send_signal(signal.SIGTERM)
        process.wait(3)
        self.assertEqual(process.returncode, 0)

    @unittest.skipUnless(sys.platform.startswith("linux"), "uses procfs only for the owned child")
    def test_main_headless_has_no_listener(self):
        process = self.start_process()
        deadline = time.monotonic() + 5
        while True:
            status = Path(f"/proc/{process.pid}/status").read_text()
            caught = next(line.split()[1] for line in status.splitlines() if line.startswith("SigCgt:"))
            if int(caught, 16) & (1 << (signal.SIGTERM - 1)):
                break
            if time.monotonic() > deadline:
                self.fail("headless signal handler did not start")
            time.sleep(.02)
        owned = set()
        for fd in Path(f"/proc/{process.pid}/fd").iterdir():
            try:
                target = os.readlink(fd)
            except FileNotFoundError:
                continue
            if target.startswith("socket:["):
                owned.add(target[8:-1])
        rows = Path(f"/proc/{process.pid}/net/tcp").read_text().splitlines()[1:]
        self.assertFalse(any(row.split()[9] in owned and row.split()[3] == "0A" for row in rows))
        process.terminate()
        process.wait(3)
        self.assertEqual(process.returncode, 0)


if __name__ == "__main__":
    unittest.main()
