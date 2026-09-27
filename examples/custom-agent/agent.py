#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Synthetic Custom Agent: read approved context and call a mock Gateway."""

import argparse
from contextlib import contextmanager, nullcontext
import errno
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import signal
import selectors
import socket
import ssl
import threading
import time
from urllib.parse import urlsplit

MAX_REQUEST_BYTES = 1024 * 1024
MAX_RESPONSE_BYTES = 4 * 1024 * 1024
MAX_SSE_EVENT_BYTES = 256 * 1024
MAX_CONNECTIONS = 32
CLIENT_TIMEOUT = 30
HEADER_TIMEOUT = 30
STREAM_IDLE_TIMEOUT = 120


class GatewayProtocolError(OSError):
    pass


def read_gateway_json(response):
    data = response.read(MAX_RESPONSE_BYTES + 1)
    if len(data) > MAX_RESPONSE_BYTES:
        raise GatewayProtocolError("Gateway response exceeds limit")
    try:
        value = json.loads(data)
    except (ValueError, TypeError) as error:
        raise GatewayProtocolError("Invalid Gateway JSON") from error
    if not isinstance(value, dict):
        raise GatewayProtocolError("Gateway response must be an object")
    return value


class AgentHTTPServer(ThreadingHTTPServer):
    daemon_threads = True
    request_queue_size = 64

    def __init__(self, address, handler, max_connections=MAX_CONNECTIONS, client_timeout=CLIENT_TIMEOUT):
        self.slots = threading.BoundedSemaphore(max_connections)
        self.client_timeout = client_timeout
        super().__init__(address, handler)

    def process_request(self, request, address):
        request.settimeout(self.client_timeout)
        if not self.slots.acquire(blocking=False):
            body = b'{"error":{"type":"rate_limit_error","message":"Concurrent request limit"}}'
            try:
                request.sendall(b"HTTP/1.1 429 Too Many Requests\r\nContent-Type: application/json\r\nConnection: close\r\nContent-Length: " +
                    str(len(body)).encode() + b"\r\n\r\n" + body)
            except OSError:
                pass
            finally:
                self.shutdown_request(request)
            return
        try:
            super().process_request(request, address)
        except BaseException:
            self.slots.release()
            self.shutdown_request(request)
            raise

    def process_request_thread(self, request, address):
        try:
            super().process_request_thread(request, address)
        finally:
            self.slots.release()


class RequestCancelled(Exception):
    pass


class CallContext:
    """Observe downstream disconnect even when the upstream sends no bytes."""

    def __init__(self, downstream):
        self.downstream = downstream
        self.cancelled = threading.Event()
        self.finished = threading.Event()
        self.lock = threading.Lock()
        self.sockets = set()
        self.watcher = threading.Thread(target=self.watch, daemon=True)

    def __enter__(self):
        self.watcher.start()
        return self

    def __exit__(self, *_args):
        self.finished.set()
        self.watcher.join(timeout=1)

    def check(self):
        if self.cancelled.is_set():
            raise RequestCancelled()

    def watch(self):
        try:
            with selectors.DefaultSelector() as selector:
                selector.register(self.downstream, selectors.EVENT_READ)
                while not self.finished.wait(0.02):
                    if selector.select(0) and not self.downstream.recv(1, socket.MSG_PEEK):
                        break
                else:
                    return
        except (OSError, ValueError):
            pass
        with self.lock:
            self.cancelled.set()
            for upstream in self.sockets:
                try:
                    upstream.shutdown(socket.SHUT_RDWR)
                except OSError:
                    pass

    @contextmanager
    def bind(self, upstream):
        # A separate descriptor remains available while HTTPResponse owns a
        # buffered reader, including HTTPS connections with Connection: close.
        duplicate = socket.socket(upstream.family, upstream.type, upstream.proto, fileno=os.dup(upstream.fileno()))
        try:
            with self.lock:
                self.check()
                self.sockets.add(duplicate)
            yield
        finally:
            with self.lock:
                self.sockets.discard(duplicate)
                duplicate.close()


class GatewayConnection(http.client.HTTPConnection):
    def __init__(self, endpoint, calls):
        super().__init__(endpoint.hostname, endpoint.port or (443 if endpoint.scheme == "https" else 80), timeout=5)
        self.calls = calls
        self.tls = ssl.create_default_context() if endpoint.scheme == "https" else None
        if self.tls is not None and self.tls.minimum_version < ssl.TLSVersion.TLSv1_2:
            self.tls.minimum_version = ssl.TLSVersion.TLSv1_2

    def connect(self):
        if self.calls:
            self.sock = self.connect_cancellable()
        else:
            self.sock = socket.create_connection((self.host, self.port), timeout=self.timeout)
        if self.tls:
            # Register the TCP socket before TLS can block. Certificate and
            # hostname verification retain the default SSLContext policy.
            with (self.calls.bind(self.sock) if self.calls else nullcontext()):
                if self.calls:
                    self.calls.check()
                self.sock = self.tls.wrap_socket(self.sock, server_hostname=self.host)

    def connect_cancellable(self):
        self.calls.check()
        addresses = socket.getaddrinfo(self.host, self.port, type=socket.SOCK_STREAM)
        self.calls.check()
        error = OSError("Gateway connection failed")
        for family, kind, protocol, _, address in addresses:
            sock = socket.socket(family, kind, protocol)
            try:
                sock.setblocking(False)
                result = sock.connect_ex(address)
                deadline = time.monotonic() + self.timeout
                self.calls.check()
                if result in (errno.EINPROGRESS, errno.EWOULDBLOCK, errno.EALREADY):
                    with selectors.DefaultSelector() as selector:
                        selector.register(sock, selectors.EVENT_WRITE)
                        while result in (errno.EINPROGRESS, errno.EWOULDBLOCK, errno.EALREADY):
                            self.calls.check()
                            if time.monotonic() >= deadline:
                                raise TimeoutError("Gateway connection timed out")
                            if selector.select(0.02):
                                result = sock.getsockopt(socket.SOL_SOCKET, socket.SO_ERROR)
                self.calls.check()
                if result:
                    raise OSError("Gateway connection failed")
                sock.settimeout(self.timeout)
                return sock
            except RequestCancelled:
                sock.close()
                raise
            except OSError as failure:
                sock.close()
                error = failure
        raise error


class Agent:
    def __init__(self, home, base_url, key):
        self.home = Path(home)
        self.base_url = base_url.rstrip("/")
        parts = urlsplit(self.base_url)
        if parts.scheme not in ("http", "https") or not parts.hostname or parts.username or parts.query or parts.fragment or not key:
            raise ValueError("HOME, HTTP Gateway base and model credential are required")
        self.key = key
        self.context = self.load_context()

    def load_context(self):
        root = self.home / ".agents"
        sources = [root / "AGENTS.md", root / "skills/cetirun-memory/SKILL.md"]
        sources.extend(path for path in sorted((root / "skills").glob("*/SKILL.md")) if path not in sources)
        return "\n\n".join(path.read_text() for path in sources)

    @contextmanager
    def gateway(self, path, body=None, calls=None):
        data = None if body is None else json.dumps(body).encode()
        endpoint = urlsplit(self.base_url + path)
        connection = GatewayConnection(endpoint, calls)
        try:
            connection.connect()
            with (calls.bind(connection.sock) if calls else nullcontext()):
                if calls:
                    calls.check()
                connection.request("GET" if body is None else "POST", endpoint.path, body=data,
                    headers={"Authorization": "Bearer " + self.key, "Content-Type": "application/json", "Connection": "close"})
                transport = connection.sock
                transport.settimeout(HEADER_TIMEOUT)
                response = connection.getresponse()
                transport.settimeout(STREAM_IDLE_TIMEOUT if body and body.get("stream") else HEADER_TIMEOUT)
                try:
                    if calls:
                        calls.check()
                    if response.status < 200 or response.status >= 300:
                        raise OSError("Gateway request failed")
                    yield response
                finally:
                    response.close()
        finally:
            connection.close()

    @contextmanager
    def completion(self, messages, stream=False, calls=None):
        with self.gateway("/models", calls=calls) as response:
            models = read_gateway_json(response).get("data")
        if not isinstance(models, list) or not models or not isinstance(models[0], dict) or not isinstance(models[0].get("id"), str):
            raise GatewayProtocolError("Gateway has no valid allowed models")
        with self.gateway("/chat/completions", {
            "model": models[0]["id"], "messages": [{"role": "system", "content": self.context}, *messages],
            "stream": stream,
        }, calls=calls) as response:
            yield response

    def remember(self, response):
        # A synthetic memory behavior for qualification, not a history service.
        directory = self.home / "memory"
        directory.mkdir(exist_ok=True)
        (directory / "last-result.json").write_text(json.dumps(response))


def make_handler(agent):
    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, *_args):
            pass

        def send_json(self, status, body):
            data = json.dumps(body).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def do_GET(self):
            if self.path == "/healthz":
                self.send_json(200, {"status": "ok"})
            elif self.path == "/v1/models":
                self.send_json(200, {"object": "list", "data": [{"id": "custom-example", "object": "model"}]})
            else:
                self.send_json(404, {"error": {"message": "Not found"}})

        def do_POST(self):
            if self.path != "/v1/chat/completions":
                self.send_json(404, {"error": {"message": "Not found"}})
                return
            started = False
            calls = None
            try:
                size = int(self.headers.get("Content-Length", "0"))
                if size < 1 or size > MAX_REQUEST_BYTES:
                    raise ValueError("invalid body size")
                request = json.loads(self.rfile.read(size))
                if not isinstance(request, dict) or set(request) - {"model", "messages", "stream"}:
                    raise ValueError("unsupported request fields")
                messages = request.get("messages")
                if request.get("model") != "custom-example" or not isinstance(messages, list) or not messages:
                    raise ValueError("invalid model/messages")
                for message in messages:
                    if not isinstance(message, dict) or set(message) != {"role", "content"} or \
                            message["role"] not in ("system", "user", "assistant") or not isinstance(message["content"], str):
                        raise ValueError("unsupported message fields")
                stream = request.get("stream", False)
                if not isinstance(stream, bool):
                    raise ValueError("invalid stream")
                with CallContext(self.connection) as calls, agent.completion(messages, stream, calls) as upstream:
                    if stream:
                        self.send_response(200)
                        self.send_header("Content-Type", "text/event-stream")
                        self.send_header("Connection", "close")
                        self.end_headers()
                        self.close_connection = True
                        started = True
                        event_size = 0
                        while True:
                            line = upstream.readline(MAX_SSE_EVENT_BYTES + 1)
                            if not line:
                                break
                            event_size += len(line)
                            if event_size > MAX_SSE_EVENT_BYTES:
                                raise GatewayProtocolError("Gateway SSE event exceeds limit")
                            calls.check()
                            self.wfile.write(line)
                            self.wfile.flush()
                            if not line.strip():
                                event_size = 0
                        # Never append a fabricated [DONE] on upstream interruption.
                    else:
                        response = read_gateway_json(upstream)
                        calls.check()
                        agent.remember(response)
                        self.send_json(200, response)
            except (RequestCancelled, BrokenPipeError, ConnectionResetError):
                self.close_connection = True
            except (ValueError, KeyError, TypeError):
                if not started and not (calls and calls.cancelled.is_set()):
                    self.send_json(400, {"error": {"message": "Invalid request or Gateway response", "type": "invalid_request_error"}})
                self.close_connection = True
            except (OSError, http.client.HTTPException):
                if not started and not (calls and calls.cancelled.is_set()):
                    self.send_json(502, {"error": {"message": "Gateway request failed", "type": "upstream_error"}})
                self.close_connection = True

    return Handler


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--headless-once", action="store_true", help="Run one synthetic Gateway/context check and exit")
    args = parser.parse_args()
    agent = Agent(os.environ["HOME"], os.environ["OPENAI_BASE_URL"], os.environ["OPENAI_API_KEY"])
    if args.headless_once:
        with agent.completion([{"role": "user", "content": "Run the example skill"}]) as response:
            agent.remember(read_gateway_json(response))
        return
    port = os.environ.get("PORT")
    if port:
        server = AgentHTTPServer(("0.0.0.0", int(port)), make_handler(agent))
        def stop_http(*_args):
            threading.Thread(target=server.shutdown, daemon=True).start()
        signal.signal(signal.SIGTERM, stop_http)
        signal.signal(signal.SIGINT, stop_http)
        try:
            server.serve_forever(poll_interval=0.1)
        finally:
            server.server_close()
    else:
        # No implicit HTTP listener in headless mode.
        stopped = threading.Event()
        signal.signal(signal.SIGTERM, lambda *_: stopped.set())
        signal.signal(signal.SIGINT, lambda *_: stopped.set())
        stopped.wait()


if __name__ == "__main__":
    main()
