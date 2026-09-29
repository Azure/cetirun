#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Install the bounded Dashboard prefix adapter into the pinned Hermes release."""

import argparse
import hashlib
from pathlib import Path


PATCHES = (
    (
        "hermes_cli/web_server.py",
        "dd095ecfe0a8a1dcc9148c170e96efd8eb9a5ce9a3de68d905f0d61024b144a0",
        "    config = uvicorn.Config(\n",
        "    from cetirun_compat.dashboard_prefix import DashboardPrefixMiddleware\n"
        "    served_app = DashboardPrefixMiddleware(served_app)\n"
        "    config = uvicorn.Config(\n",
    ),
    (
        "hermes_cli/dashboard_auth/login_page.py",
        "2dfeb9d1ccd9ab46f1c20845a031fecc1cdf6dc0b8bb75edcf2dc2bd75184085",
        "      fetch('/auth/password-login', {\n",
        "      fetch(new URL('auth/password-login', document.baseURI), {\n",
    ),
    (
        "hermes_cli/dashboard_auth/routes.py",
        "2d179b9a3f2b23d21cb178f4c11e933ac4cf1e64c0e8544e7ebbcc74f009cb36",
        '    return _validate_post_login_target(next_raw) or "/", False\n',
        "    from cetirun_compat.dashboard_prefix import public_path\n"
        '    return public_path(_validate_post_login_target(next_raw) or "/", _prefix(request)), False\n',
    ),
)


def prepare_patch(root, patches=PATCHES):
    updates = []
    for relative, expected, before, after in patches:
        path = (root / relative).resolve()
        if not path.is_relative_to(root):
            raise ValueError(f"Source path escapes Hermes checkout: {relative}")
        source = path.read_bytes()
        if source.count(after.encode()) == 1:
            original = source.replace(after.encode(), before.encode(), 1)
            if hashlib.sha256(original).hexdigest() == expected:
                continue
        if hashlib.sha256(source).hexdigest() != expected:
            raise ValueError(f"Unsupported Hermes source: {relative}; review the prefix adapter")
        if source.count(before.encode()) != 1:
            raise ValueError(f"Expected one integration point: {relative}")
        updates.append((path, source.replace(before.encode(), after.encode(), 1)))
    return updates


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, required=True, help="Pinned Hermes source directory")
    parser.add_argument("--check", action="store_true", help="Validate inputs without changing them")
    args = parser.parse_args()
    try:
        updates = prepare_patch(args.root.resolve())
        if not args.check:
            for path, content in updates:
                path.write_bytes(content)
    except (OSError, ValueError) as error:
        parser.exit(1, f"{error}\n")
    print(f"Dashboard prefix adapter: {len(updates)} file(s) {'validated' if args.check else 'updated'}")


if __name__ == "__main__":
    main()
