#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Fence a published Helm upgrade against a competing release revision.

Helm invokes a post-renderer after rendering and before creating the pending
release revision. This guard reads the release history at that point and
passes the rendered manifest through unchanged only when the expected deployed
revision is still current. A pending or newly completed competing revision is
therefore rejected before this invocation writes any resources.
"""

import argparse
import json
import subprocess
import sys


def main(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument("--expected-revision", required=True, type=int)
    parser.add_argument("--release", required=True)
    parser.add_argument("--namespace", required=True)
    parser.add_argument("--context", required=True)
    args = parser.parse_args(argv)
    if args.expected_revision < 1:
        parser.error("expected revision must be positive")
    try:
        result = subprocess.run(
            ["helm", "history", args.release, "--namespace", args.namespace,
             "--kube-context", args.context, "--max", "1", "-o", "json"],
            capture_output=True, text=True, check=False, timeout=30)
    except (OSError, subprocess.TimeoutExpired):
        print("helm_revision_guard: unable to read release history", file=sys.stderr)
        return 2
    if result.returncode:
        print("helm_revision_guard: unable to read release history", file=sys.stderr)
        return 2
    try:
        history = json.loads(result.stdout)
        if not isinstance(history, list) or not history or not all(isinstance(item, dict) for item in history):
            raise ValueError("invalid history")
        latest = max(history, key=lambda item: int(item["revision"]))
        if latest.get("status") != "deployed" or int(latest["revision"]) != args.expected_revision:
            print("helm_revision_guard: release revision changed", file=sys.stderr)
            return 3
    except (TypeError, ValueError, KeyError):
        print("helm_revision_guard: invalid release history", file=sys.stderr)
        return 2
    sys.stdout.buffer.write(sys.stdin.buffer.read())
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
