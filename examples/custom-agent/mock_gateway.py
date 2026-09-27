#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Local-only Gateway fixture. No model provider or paid API is contacted."""

import argparse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import socket
import threading


MARKER = "CUSTOM-SKILL-READ-7391"


def make_server(port=0, expected_context=None):
    expected_context = expected_context or ("last-result.json", "read-only skills view")
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass

        def json_response(self, status, body):
            data = json.dumps(body).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def authorized(self):
            if self.headers.get("Authorization") != "Bearer sk-dummy":
                self.json_response(401, {"error": {"message": "Synthetic key required"}})
                return False
            return True

        def do_GET(self):
            if not self.authorized():
                return
            self.server.requests.append(("GET", self.path, None))
            self.json_response(200, {"data": [{"id": "mock-upstream-model"}]})

        def do_POST(self):
            if not self.authorized():
                return
            body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
            self.server.requests.append(("POST", self.path, body))
            context = body["messages"][0]["content"]
            if any(text not in context for text in expected_context):
                self.json_response(422, {"error": {"message": "Approved context was not consumed"}})
                return
            prompt = body["messages"][-1]["content"]
            if prompt == "error":
                self.json_response(503, {"error": {"message": "Synthetic failure"}})
                return
            if prompt == "oversized-event":
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                try:
                    self.wfile.write(b"data: " + b"x" * (256 * 1024) + b"\n\n")
                    self.wfile.flush()
                except (BrokenPipeError, ConnectionResetError):
                    pass
                return
            if prompt in ("quiet-before-headers", "quiet-nonstream", "quiet-stream"):
                if prompt == "quiet-stream":
                    self.send_response(200)
                    self.send_header("Content-Type", "text/event-stream")
                    self.end_headers()
                    self.wfile.write(b'data: {"choices":[{"delta":{"content":"started"}}]}\n\n')
                    self.wfile.flush()
                self.server.stream_started.set()
                self.connection.settimeout(2)
                try:
                    if self.connection.recv(1) == b"":
                        self.server.disconnected.set()
                except (BrokenPipeError, ConnectionResetError):
                    self.server.disconnected.set()
                except socket.timeout:
                    pass
                return
            if body.get("stream"):
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                def chunk(delta, finish=None):
                    return json.dumps({
                        "id": "synthetic-result", "object": "chat.completion.chunk",
                        "created": 1, "model": "custom-example",
                        "choices": [{"index": 0, "delta": delta, "finish_reason": finish}],
                    }).encode()
                self.wfile.write(b"data: " + chunk({"role": "assistant", "content": MARKER}) + b"\n\n")
                self.wfile.flush()
                if prompt == "interrupt":
                    return
                if prompt == "cancel":
                    self.server.stream_started.set()
                    self.server.release_stream.wait(timeout=5)
                    try:
                        for _ in range(64):
                            self.wfile.write(b"data: " + b"x" * 8192 + b"\n\n")
                            self.wfile.flush()
                    except (BrokenPipeError, ConnectionResetError):
                        self.server.disconnected.set()
                    return
                self.wfile.write(b"data: " + chunk({}, "stop") + b"\n\ndata: [DONE]\n\n")
            else:
                result = {"id": "synthetic-result", "object": "chat.completion", "created": 1, "model": "custom-example",
                    "choices": [{"index": 0, "message": {"role": "assistant", "content": MARKER}, "finish_reason": "stop"}]}
                self.json_response(200, result)
                self.server.successful_completions.append(result)

    server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
    server.requests = []
    server.successful_completions = []
    server.stream_started = threading.Event()
    server.release_stream = threading.Event()
    server.disconnected = threading.Event()
    return server


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--port", type=int, default=8090)
    args = parser.parse_args()
    make_server(args.port).serve_forever()


if __name__ == "__main__":
    main()
