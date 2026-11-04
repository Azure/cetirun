# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Keep the pinned gateway from opening an alternate port when Webapp startup fails."""

import argparse
import hashlib
from pathlib import Path

EXPECTED_SHA256 = "2899528c748dbf6166704dc080e355d56e7d3f12c9e315f7404b548286bf3465"
BEFORE = "        runner.session_api = await run_api.start_gateway_api(runner)\n"
AFTER = "        # Managed Webapp startup must not open an alternate listener.\n        raise\n"


def prepare_patch(source: str) -> str:
    original = source.replace(AFTER, BEFORE, 1) if AFTER in source else source
    if hashlib.sha256(original.encode()).hexdigest() != EXPECTED_SHA256:
        raise ValueError("Unsupported Hermes gateway source; review managed Webapp startup")
    if original.count(BEFORE) != 1:
        raise ValueError("Hermes Webapp startup integration point changed")
    return original.replace(BEFORE, AFTER, 1)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    path = args.root / "gateway/run_runtime.py"
    try:
        patched = prepare_patch(path.read_text())
        if not args.check:
            path.write_text(patched)
    except (OSError, ValueError) as exc:
        parser.exit(1, str(exc) + "\n")


if __name__ == "__main__":
    main()
