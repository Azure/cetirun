# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

import http.client
import sys


def fail(message: str) -> None:
    print(f"native health probe: {message}", file=sys.stderr)
    raise SystemExit(1)


if len(sys.argv) != 4:
    fail("invalid probe arguments")

try:
    port = int(sys.argv[1])
    path = sys.argv[2]
    timeout_ms = int(sys.argv[3])
except ValueError:
    fail("invalid probe arguments")

if not 1 <= port <= 65535 or path != "/health" or not 1 <= timeout_ms <= 2_147_483_647:
    fail("invalid probe arguments")

connection = http.client.HTTPConnection("127.0.0.1", port, timeout=timeout_ms / 1000)
try:
    connection.request("GET", path)
    response = connection.getresponse()
    response.read()
except (OSError, http.client.HTTPException):
    fail("request failed")
finally:
    connection.close()

if not 200 <= response.status < 400:
    fail(f"HTTP {response.status}")
