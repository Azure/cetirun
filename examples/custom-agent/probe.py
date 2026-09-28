#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Inspect only the synthetic example HOME during explicit image qualification."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import pwd


RESTART_CONTENT = b"QUALIFICATION-USER-CONTENT\n"


def inspect_home(mode):
    home = Path(os.environ["HOME"])
    if str(home) != "/home/worker":
        raise ValueError("Example HOME must be /home/worker")
    result = {"uid": os.getuid(), "gid": os.getgid(), "username": pwd.getpwuid(os.getuid()).pw_name, "home": str(home)}
    if mode == "identity":
        return result
    flag = (home / ".cetirun-home-seeded").read_text()
    if not flag.startswith("seeded-v1\n"):
        raise ValueError("HOME seed is incomplete")
    result["flag"] = flag
    result["context_readonly"] = bool(os.statvfs(home / ".agents").f_flag & os.ST_RDONLY)
    if mode == "prepare-restart":
        (home / "seed.txt").write_bytes(RESTART_CONTENT)
    result["seed_digest"] = hashlib.sha256((home / "seed.txt").read_bytes()).hexdigest()
    if mode == "memory":
        result["memory"] = json.loads((home / "memory/last-result.json").read_text())
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mode", choices=("identity", "seed", "prepare-restart", "memory"), required=True)
    args = parser.parse_args()
    print(json.dumps(inspect_home(args.mode)))


if __name__ == "__main__":
    main()
