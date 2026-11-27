#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Verify a public HTTPS certificate without accessing cluster secrets.

The optional address is used only as the TCP destination; ``hostname`` remains
the SNI and hostname-verification name. This permits pre-DNS checks while
retaining the system trust store and certificate hostname validation.
"""

from __future__ import annotations

import argparse
import hashlib
import ipaddress
import json
import math
import re
import socket
import ssl
import time
from datetime import datetime, timezone
from typing import Any, Callable


DEFAULT_TIMEOUT = 15.0
FINGERPRINT = re.compile(r"^[0-9a-fA-F]{64}$")


def _hostname(value: str) -> str:
    value = value.strip().rstrip(".")
    if not value or len(value) > 253 or "." not in value:
        raise ValueError("hostname must be a DNS name")
    if any(not label or len(label) > 63 or not re.fullmatch(r"[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?", label)
           for label in value.split(".")):
        raise ValueError("hostname must be a DNS name")
    return value


def _address(value: str | None) -> str | None:
    if value is None:
        return None
    try:
        return str(ipaddress.ip_address(value.strip()))
    except ValueError as exc:
        raise ValueError("address must be an IPv4 or IPv6 address") from exc


def _fingerprint(value: str | None) -> str | None:
    if value is None:
        return None
    normalized = value.replace(":", "").replace(" ", "")
    if not FINGERPRINT.fullmatch(normalized):
        raise ValueError("expected-sha256 must contain exactly 64 hexadecimal characters")
    return normalized.lower()


def verify_tls(
    hostname: str,
    address: str | None = None,
    minimum_valid_days: float = 30.0,
    expected_sha256: str | None = None,
    *,
    now: float | None = None,
    context_factory: Callable[[], ssl.SSLContext] = ssl.create_default_context,
    connection_factory: Callable[..., Any] = socket.create_connection,
) -> dict[str, Any]:
    hostname = _hostname(hostname)
    address = _address(address)
    if (type(minimum_valid_days) not in (int, float)
            or not math.isfinite(minimum_valid_days) or minimum_valid_days <= 0):
        raise ValueError("minimum-valid-days must be finite and positive")
    expected_sha256 = _fingerprint(expected_sha256)
    context = context_factory()
    # Keep these explicit even for create_default_context: verification and
    # hostname checks are part of this tool's contract.
    context.verify_mode = ssl.CERT_REQUIRED
    context.check_hostname = True
    destination = address or hostname
    with connection_factory((destination, 443), timeout=DEFAULT_TIMEOUT) as raw:
        with context.wrap_socket(raw, server_hostname=hostname) as tls:
            der = tls.getpeercert(binary_form=True)
            certificate = tls.getpeercert()
    if not der or not certificate:
        raise ssl.SSLError("peer did not provide a certificate")
    fingerprint = hashlib.sha256(der).hexdigest()
    expiry = ssl.cert_time_to_seconds(certificate["notAfter"])
    current = time.time() if now is None else now
    remaining = (expiry - current) / 86400.0
    sans = [name for kind, name in certificate.get("subjectAltName", ()) if kind == "DNS"]
    passed = remaining >= minimum_valid_days and (expected_sha256 is None or fingerprint == expected_sha256)
    result = {
        "passed": passed,
        "sha256": fingerprint,
        "serial": certificate.get("serialNumber", ""),
        "sanDnsNames": sans,
        "expiresAt": datetime.fromtimestamp(expiry, timezone.utc).isoformat().replace("+00:00", "Z"),
        "remainingValidDays": round(remaining, 3),
    }
    if remaining < minimum_valid_days:
        result["failure"] = "certificate validity is below minimum-valid-days"
    elif expected_sha256 is not None and fingerprint != expected_sha256:
        result["failure"] = "certificate SHA-256 fingerprint does not match expected-sha256"
    return result


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--hostname", required=True)
    parser.add_argument("--address")
    parser.add_argument("--minimum-valid-days", type=float, default=30.0)
    parser.add_argument("--expected-sha256")
    args = parser.parse_args(argv)
    try:
        result = verify_tls(args.hostname, args.address, args.minimum_valid_days, args.expected_sha256)
    except (OSError, ssl.SSLError, ValueError) as exc:
        print(json.dumps({"passed": False, "failure": str(exc)}, sort_keys=True))
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0 if result["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
