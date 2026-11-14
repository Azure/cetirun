#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Check the ADX-Mon images that the observability chart pins.

CetiRun does not build these images: they are pulled as the chart renders
them. Each program must accept every flag that the chart passes to it, and no
image may have a fixable high or critical CVE.
"""

import re
import subprocess
import sys

import yaml

from scan_image import scan
from validate import ROOT, run


CHART = ROOT / "app/cetirun-observability/chart"
# Synthetic values that render every component, the alerter included.
VALUES = (
    "--set", "clusterName=ci",
    "--set", "adx.endpoint=https://ci.example.kusto.windows.net",
    "--set", "ingestor.clientId=00000000-0000-0000-0000-000000000001",
    "--set", "alerter.endpoint=https://alerts.example.com",
    "--set", "alerter.clientId=00000000-0000-0000-0000-000000000002",
)
# The telemetry program is part of the operator image, which operator CI
# builds and scans.
CETIRUN_IMAGE = re.compile(r"/cetirun-operator[:@]")


def pinned_images(rendered):
    """Each image other than CetiRun's own, with the flags of each program."""
    images = {}
    for document in yaml.safe_load_all(rendered):
        if not document or document["kind"] not in ("DaemonSet", "Deployment", "StatefulSet"):
            continue
        for container in document["spec"]["template"]["spec"]["containers"]:
            image = container["image"]
            if CETIRUN_IMAGE.search(image):
                continue
            if not re.search(r"@sha256:[0-9a-f]{64}$", image):
                raise ValueError(f"{container['name']} image {image} is not pinned by digest")
            program = container.get("command", [None])[0]
            flags = {arg.split("=", 1)[0] for arg in container.get("args", []) if arg.startswith("--")}
            images.setdefault(image, {}).setdefault(program, set()).update(flags)
    return images


def unknown_flags(usage, flags):
    return sorted(flag for flag in flags if not re.search(r"(?:^|\s)" + re.escape(flag) + r"(?=[\s=,]|$)", usage, re.M))


def main():
    failures = []
    try:
        rendered = run(["helm", "template", "observability", CHART, "--namespace", "cetirun-system", *VALUES],
                       ROOT, capture=True)
        images = pinned_images(rendered)
        for image, programs in images.items():
            run(["docker", "pull", "--quiet", image], ROOT)
            for program, flags in programs.items():
                entrypoint = ["--entrypoint", program] if program else []
                usage = subprocess.run(["docker", "run", "--rm", "--read-only", "--cap-drop", "ALL", *entrypoint,
                                        image, "--help"], capture_output=True, text=True, timeout=120)
                missing = unknown_flags(usage.stdout + usage.stderr, flags)
                if missing:
                    failures.append(f"{image} {program or 'entrypoint'} does not accept {', '.join(missing)}, "
                                    "which the chart passes")
                else:
                    print(f"{image} {program or 'entrypoint'} accepts {len(flags)} flags", flush=True)
            try:
                # Read from the registry: through the Docker daemon of the CI runner, the scanner cannot read an
                # image with a single Docker v2 manifest ("file blobs/sha256/<config> not found in tar"), as the
                # ADX-Mon v0.4.1 ingestor and alerter have. The digest pins the same content either way.
                scan(image, remote=True)
            except subprocess.CalledProcessError:
                failures.append(f"{image} did not pass the CVE gate: the scanner found a fixable high or critical "
                                "CVE, or it failed. Its output above names the cause")
    except (ValueError, OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
        failures.append(str(error))
    for failure in failures:
        print(f"ERROR: {failure}", file=sys.stderr)
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
