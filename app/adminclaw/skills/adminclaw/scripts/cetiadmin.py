#!/usr/bin/env python3
"""Narrow client for the Portal CetiAdmin workload API."""

import argparse
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request


NAME_PATTERN = re.compile(r"^[a-z0-9]([-a-z0-9]*[a-z0-9])?$")
OPERATIONS = ("Suspend", "Resume", "Restart", "Reconcile")
MAX_RESPONSE_BYTES = 1024 * 1024


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def configuration():
    base = os.environ.get("CETIADMIN_PORTAL_URL", "").rstrip("/")
    token_file = os.environ.get("CETIADMIN_TOKEN_FILE", "")
    parsed = urllib.parse.urlsplit(base)
    if parsed.scheme not in ("http", "https") or not parsed.netloc or parsed.query or parsed.fragment:
        raise ValueError("CETIADMIN_PORTAL_URL must be an absolute HTTP(S) URL")
    if not token_file.startswith("/"):
        raise ValueError("CETIADMIN_TOKEN_FILE must be an absolute path")
    with open(token_file, "r", encoding="utf-8") as stream:
        token = stream.read().strip()
    if not token:
        raise ValueError("projected Portal token is empty")
    return base, token


def validate_name(value):
    if len(value) > 253 or not NAME_PATTERN.fullmatch(value):
        raise ValueError("resource name is invalid")
    return value


def request(method, path, body=None, idempotency_key=None):
    base, token = configuration()
    encoded = None if body is None else json.dumps(body, separators=(",", ":")).encode("utf-8")
    headers = {"Authorization": "Bearer " + token, "Accept": "application/json"}
    if encoded is not None:
        headers["Content-Type"] = "application/json"
    if idempotency_key is not None:
        headers["Idempotency-Key"] = idempotency_key
    req = urllib.request.Request(base + path, data=encoded, headers=headers, method=method)
    opener = urllib.request.build_opener(NoRedirect())
    try:
        with opener.open(req, timeout=10) as response:
            payload = response.read(MAX_RESPONSE_BYTES + 1)
            if len(payload) > MAX_RESPONSE_BYTES:
                raise ValueError("Portal response exceeds the safety limit")
            return response.status, json.loads(payload)
    except urllib.error.HTTPError as error:
        payload = error.read(MAX_RESPONSE_BYTES)
        try:
            detail = json.loads(payload)
        except (json.JSONDecodeError, UnicodeDecodeError):
            detail = {"error": "portal_request_failed", "status": error.code}
        return error.code, detail


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)

    observe = subparsers.add_parser("observe", help="read one redacted Agent observation")
    observe.add_argument("agent", type=validate_name)

    propose = subparsers.add_parser("propose", help="submit one lifecycle proposal")
    propose.add_argument("agent", type=validate_name)
    propose.add_argument("operation", choices=OPERATIONS)
    propose.add_argument("reason")
    propose.add_argument("--idempotency-key", required=True, help="stable token for safe retries")
    propose.add_argument("--expires-in", type=int, default=900, choices=range(60, 3601), metavar="SECONDS")

    get_action = subparsers.add_parser("get", help="read one proposal created by this workload")
    get_action.add_argument("action", type=validate_name)

    arguments = parser.parse_args()
    if arguments.command == "observe":
        status, result = request("GET", "/internal/v1/agents/" + arguments.agent + "/governance")
    elif arguments.command == "propose":
        reason = arguments.reason.strip()
        if not reason or len(reason) > 1024:
            parser.error("reason must contain 1 to 1024 characters")
        if not re.fullmatch(r"[A-Za-z0-9._~-]{1,128}", arguments.idempotency_key):
            parser.error("idempotency key must be 1-128 URL-safe characters")
        status, result = request(
            "POST",
            "/internal/v1/governance-actions",
            {
                "target": {"kind": "Agent", "name": arguments.agent},
                "operation": arguments.operation,
                "reason": reason,
                "expiresInSeconds": arguments.expires_in,
            },
            arguments.idempotency_key,
        )
    else:
        status, result = request("GET", "/internal/v1/governance-actions/" + arguments.action)

    print(json.dumps(result, indent=2, sort_keys=True))
    return 0 if 200 <= status < 300 else 1


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError) as error:
        print(json.dumps({"error": "cetiadmin_client_error", "message": str(error)}), file=sys.stderr)
        sys.exit(1)
