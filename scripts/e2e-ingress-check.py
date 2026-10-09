#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Check one native UI's HTTPS sign-in challenge without credentials or redirects."""

import argparse
from contextlib import contextmanager
import http.client
import ipaddress
import json
from pathlib import Path
import re
import signal
import ssl
import sys
from urllib.parse import parse_qs, urlsplit


REQUEST_TIMEOUT = 10
TOTAL_TIMEOUT = 30
MAX_HEADERS_BYTES = 16 * 1024
MAX_LOCATION_BYTES = 8 * 1024
CLIENT_ID = re.compile(r"[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}")
DNS_NAME = re.compile(r"[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)*")


class CheckError(Exception):
    pass


class InvalidInput(CheckError):
    pass


class Terminated(CheckError):
    pass


def origin_parts(value):
    if not isinstance(value, str) or len(value) > 2048 or \
            any(c <= " " or c > "~" for c in value) or any(c in value for c in "\\%?#"):
        raise InvalidInput("invalid-origin")
    try:
        parsed = urlsplit(value)
        host, port = parsed.hostname, parsed.port
    except ValueError as error:
        raise InvalidInput("invalid-origin") from error
    if parsed.scheme != "https" or not host or parsed.username is not None or \
            parsed.password is not None or parsed.path not in ("", "/") or \
            parsed.query or parsed.fragment or (port is not None and not 1 <= port <= 65535):
        raise InvalidInput("invalid-origin")
    try:
        ipaddress.ip_address(host)
    except ValueError:
        if len(host) > 253 or not DNS_NAME.fullmatch(host) or any(len(label) > 63 for label in host.split(".")):
            raise InvalidInput("invalid-origin")
    return parsed


def validate_inputs(origin, agent, client_id, ca_file):
    parsed = origin_parts(origin)
    if not isinstance(agent, str) or not 1 <= len(agent) <= 63 or \
            re.fullmatch(r"[a-z0-9](?:[a-z0-9-]*[a-z0-9])?", agent) is None:
        raise InvalidInput("invalid-agent-name")
    if not isinstance(client_id, str) or CLIENT_ID.fullmatch(client_id) is None:
        raise InvalidInput("invalid-client-id")
    if ca_file is not None:
        path = Path(ca_file)
        if not path.is_absolute() or not path.is_file():
            raise InvalidInput("invalid-ca-file")
    return parsed


def verify_challenge(status, headers, origin, client_id):
    if status != 302:
        raise CheckError("unexpected-http-status")
    if sum(len(name) + len(value) + 4 for name, value in headers) > MAX_HEADERS_BYTES:
        raise CheckError("response-headers-too-large")
    locations = [value for name, value in headers if name.lower() == "location"]
    if len(locations) != 1:
        raise CheckError("missing-or-duplicate-location")
    location = locations[0]
    if not location or len(location) > MAX_LOCATION_BYTES or \
            any(c <= " " or c > "~" for c in location) or "\\" in location:
        raise CheckError("invalid-challenge-location")
    try:
        target = urlsplit(location)
        tenant_path = re.fullmatch(r"/([A-Za-z0-9.-]+)/oauth2/v2\.0/authorize", target.path)
        if target.scheme != "https" or target.hostname != "login.microsoftonline.com" or \
                target.port not in (None, 443) or target.username is not None or \
                target.password is not None or target.fragment or \
                tenant_path is None or tenant_path[1] in (".", ".."):
            raise CheckError("unexpected-challenge-endpoint")
        query = parse_qs(target.query, keep_blank_values=True, strict_parsing=True, max_num_fields=32)
    except ValueError as error:
        raise CheckError("invalid-challenge-location") from error
    clients = query.get("client_id", [])
    if len(clients) != 1 or clients[0].lower() != client_id.lower():
        raise CheckError("unexpected-challenge-client")
    if query.get("response_type") != ["code"]:
        raise CheckError("unexpected-response-type")
    callback = query.get("redirect_uri", [])
    if callback != [origin.rstrip("/") + "/oauth2/callback"]:
        raise CheckError("unexpected-challenge-callback")


@contextmanager
def deadline():
    if not hasattr(signal, "setitimer") or signal.getitimer(signal.ITIMER_REAL) != (0.0, 0.0):
        raise CheckError("deadline-unavailable")

    def expired(_signum, _frame):
        raise TimeoutError

    def terminated(_signum, _frame):
        raise Terminated("cancelled")

    previous_alarm = signal.signal(signal.SIGALRM, expired)
    previous_term = signal.signal(signal.SIGTERM, terminated)
    signal.setitimer(signal.ITIMER_REAL, TOTAL_TIMEOUT)
    try:
        yield
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
        signal.signal(signal.SIGALRM, previous_alarm)
        signal.signal(signal.SIGTERM, previous_term)


def inspect(origin, agent, client_id, ca_file=None):
    parsed = validate_inputs(origin, agent, client_id, ca_file)
    with deadline():
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
        if ca_file is None:
            context.load_default_certs()
        else:
            context.load_verify_locations(cafile=ca_file)
        connection = http.client.HTTPSConnection(parsed.hostname, parsed.port or 443,
                                                timeout=REQUEST_TIMEOUT, context=context)
        try:
            # No ambient proxy, cookie store, authorization or redirect handler.
            connection.request("GET", f"/ui/{agent}/", headers={
                "Accept": "text/html",
                "User-Agent": "cetirun-e2e-ingress-check",
                "Connection": "close",
            })
            response = connection.getresponse()
            try:
                if response.msg.defects:
                    raise CheckError("invalid-response-headers")
                verify_challenge(response.status, response.getheaders(), origin, client_id)
            finally:
                response.close()
        finally:
            connection.close()


def main(argv=None):
    class Parser(argparse.ArgumentParser):
        def error(self, message):
            raise InvalidInput("invalid-arguments")

    parser = Parser(description=__doc__)
    parser.add_argument("--origin", required=True, help="Explicit HTTPS origin without path, query or credentials")
    parser.add_argument("--agent-name", required=True)
    parser.add_argument("--client-id", required=True, help="Expected public OIDC application identifier")
    parser.add_argument("--ca-file", help="Optional absolute PEM CA bundle; otherwise system trust is used")
    result = {"version": "v1", "check": "native-ingress-challenge", "status": "blocked",
              "authenticatedAccess": "not-tested", "qualifiesExecution": False}
    code = 1
    try:
        args = parser.parse_args(argv)
        inspect(args.origin, args.agent_name, args.client_id, args.ca_file)
        result.update(status="observed", reason="expected-sign-in-challenge")
        code = 0
    except InvalidInput as error:
        result["reason"], code = str(error), 2
    except ssl.SSLCertVerificationError:
        result["reason"] = "tls-verification-failed"
    except ssl.SSLError:
        result["reason"] = "tls-connection-failed"
    except TimeoutError:
        result["reason"] = "deadline-exceeded"
    except (OSError, http.client.HTTPException):
        result["reason"] = "request-failed"
    except Terminated:
        result["reason"], code = "cancelled", 143
    except KeyboardInterrupt:
        result["reason"], code = "cancelled", 130
    except CheckError as error:
        result["reason"] = str(error)
    print(json.dumps(result, sort_keys=True))
    return code


if __name__ == "__main__":
    sys.exit(main())
