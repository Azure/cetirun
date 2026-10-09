#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Wait for an MCR version tag and platform manifest to match the backing digest."""

import argparse
import hashlib
import re
import shutil
import subprocess
import sys
import time
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen


IMAGE = re.compile(
    r"mcr\.microsoft\.com/(?P<repository>aks/ai-runtime/cetirun/[a-z0-9]+(?:[._-][a-z0-9]+)*)"
    r":(?P<tag>[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})"
)
ACCEPT = ", ".join((
    "application/vnd.oci.image.index.v1+json",
    "application/vnd.docker.distribution.manifest.list.v2+json",
    "application/vnd.oci.image.manifest.v1+json",
    "application/vnd.docker.distribution.manifest.v2+json",
))
MAX_MANIFEST_BYTES = 4 * 1024 * 1024


def manifest_matches(url: str, expected: str) -> bool:
    try:
        with urlopen(Request(url, headers={"Accept": ACCEPT}), timeout=15) as response:
            manifest = response.read(MAX_MANIFEST_BYTES + 1)
            if len(manifest) > MAX_MANIFEST_BYTES:
                raise ValueError("MCR manifest exceeds the size limit")
            digest = "sha256:" + hashlib.sha256(manifest).hexdigest()
            header = response.headers.get("Docker-Content-Digest")
            if header and header != digest:
                raise ValueError("MCR manifest digest header does not match its content")
            return digest == expected
    except HTTPError as error:
        if error.code in (404, 429) or 500 <= error.code < 600:
            return False
        raise
    except (URLError, TimeoutError):
        return False


def pull_image(image: str) -> bool:
    """Return whether MCR has synchronized the requested platform manifest."""
    try:
        subprocess.run(
            ["docker", "pull", "--platform", "linux/amd64", image],
            check=True,
            capture_output=True,
            text=True,
            timeout=600,
        )
    except (subprocess.CalledProcessError, subprocess.TimeoutExpired):
        return False
    return True


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True, help="tagged mcr.microsoft.com/aks/ai-runtime/cetirun image")
    parser.add_argument("--digest", required=True, help="sha256 digest returned by the backing registry")
    parser.add_argument("--attempts", type=int, default=20, help="manifest attempts, 1..60 (default: 20)")
    parser.add_argument("--interval", type=int, default=15, help="seconds between attempts, 0..60 (default: 15)")
    args = parser.parse_args(argv)
    try:
        image = IMAGE.fullmatch(args.image)
        if image is None or not re.fullmatch(r"sha256:[0-9a-f]{64}", args.digest):
            raise ValueError("Expected a tagged CetiRun MCR image and a full SHA256 digest")
        if not 1 <= args.attempts <= 60 or not 0 <= args.interval <= 60:
            raise ValueError("attempts must be 1..60 and interval must be 0..60")
        if not shutil.which("docker"):
            raise ValueError("Docker is required; install it separately")

        url = f"https://mcr.microsoft.com/v2/{image['repository']}/manifests/{image['tag']}"
        pinned = f"{args.image}@{args.digest}"
        for attempt in range(args.attempts):
            if manifest_matches(url, args.digest) and pull_image(pinned):
                break
            if attempt + 1 == args.attempts:
                raise ValueError(
                    f"MCR did not expose a pullable {args.digest} manifest for "
                    f"{args.image}; check onboarding and retry verification"
                )
            print(f"Waiting for MCR: {args.image} (attempt {attempt + 1}/{args.attempts})", flush=True)
            time.sleep(args.interval)

        platform = subprocess.run(
            ["docker", "image", "inspect", "--format", "{{.Os}}/{{.Architecture}}", pinned],
            check=True, capture_output=True, text=True, timeout=30,
        ).stdout.strip()
        if platform != "linux/amd64":
            raise ValueError("The pulled release image is not linux/amd64")
        print(f"Verified MCR tag, manifest digest and pull: {pinned}")
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print(f"ERROR: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
