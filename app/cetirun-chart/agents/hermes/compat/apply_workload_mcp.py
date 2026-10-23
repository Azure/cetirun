# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Install request-time authentication into the immutable Hermes MCP transport."""

import argparse
import hashlib
from pathlib import Path


EXPECTED_SHA256 = "915023c572f5cb56d0a99e7709a9d358be3a3c00cbd9f02fe7327815ffa7a8a0"
REPLACEMENTS = (
    (
        '        if not _core._MCP_NEW_HTTP:\n',
        '        if not _core._MCP_NEW_HTTP and getattr(oauth_auth, "is_cetirun_workload", False):\n'
        '            raise ImportError("Platform MCP requires the native owned HTTP client")\n'
        '        if not _core._MCP_NEW_HTTP:\n',
    ),
    (
        '        client_kwargs: dict = {"follow_redirects": True,',
        '        client_kwargs: dict = {"follow_redirects": not getattr(oauth_auth, "is_cetirun_workload", False),',
    ),
    (
        '        common = (url, headers, connect_timeout, config.get("ssl_verify", True), _resolve_client_cert(self.name, config),\n'
        '                  self._build_oauth_auth(url, config), bool(config.get("strict_redirect_headers")))\n',
        '        from cetirun_compat.workload_mcp import workload_auth, workload_tls\n'
        '        platform_auth = workload_auth(_core.sdk_httpx(), url, self._build_oauth_auth(url, config))\n'
        '        if getattr(platform_auth, "is_cetirun_workload", False) and (config.get("ssl_verify", True) is not True or config.get("transport") == "sse"):\n'
        '            raise ValueError("Platform MCP requires verified TLS and Streamable HTTP")\n'
        '        common = (url, headers, connect_timeout, workload_tls(url, config.get("ssl_verify", True)), _resolve_client_cert(self.name, config),\n'
        '                  platform_auth, bool(config.get("strict_redirect_headers")))\n',
    ),
)


def prepare_patch(source):
    if isinstance(source, bytes):
        source = source.decode("utf-8")
    original = source
    for before, after in reversed(REPLACEMENTS):
        if after in original:
            original = original.replace(after, before, 1)
    if hashlib.sha256(original.encode("utf-8")).hexdigest() != EXPECTED_SHA256:
        raise ValueError("Unsupported Hermes MCP source; review the pinned integration")
    updated = original
    for before, after in REPLACEMENTS:
        if updated.count(before) != 1:
            raise ValueError("Hermes MCP integration point changed")
        updated = updated.replace(before, after, 1)
    return updated


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    path = args.root / "tools/mcp_tool_transport.py"
    try:
        updated = prepare_patch(path.read_bytes())
        if not args.check:
            path.write_text(updated, encoding="utf-8")
    except (OSError, ValueError) as error:
        parser.exit(1, str(error) + "\n")


if __name__ == "__main__":
    main()
