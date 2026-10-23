#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Read, update, and verify synchronized CetiRun release versions."""

import argparse
import json
from pathlib import Path
import re
import sys


ROOT = Path(__file__).resolve().parents[1]
# The version is also an image tag, so build metadata is not supported.
NUMBER = r"(?:0|[1-9][0-9]*)"
PRERELEASE_IDENTIFIER = rf"(?:{NUMBER}|[0-9]*[A-Za-z-][0-9A-Za-z-]*)"
VERSION_PATTERN = re.compile(
    rf"{NUMBER}\.{NUMBER}\.{NUMBER}"
    rf"(?:-{PRERELEASE_IDENTIFIER}(?:\.{PRERELEASE_IDENTIFIER})*)?"
)
PENDING_UPDATES = {}


def replace(path, pattern, replacement, expected=1, limit=0):
    source = PENDING_UPDATES.get(path, path.read_text())
    updated, count = re.subn(
        pattern, replacement, source, count=limit, flags=re.MULTILINE
    )
    if count != expected:
        raise ValueError(
            f"{path.relative_to(ROOT)}: expected {expected} version field(s), found {count}"
        )
    PENDING_UPDATES[path] = updated


def top_level_yaml(path, field):
    match = re.search(
        rf"^{re.escape(field)}:\s*[\"']?([^\"'\s#]+)",
        path.read_text(),
        re.MULTILINE,
    )
    if not match:
        raise ValueError(f"{path.relative_to(ROOT)}: missing {field}")
    return match.group(1)


# Subcharts of the platform chart: Operator, Portal, AI Gateway and optional MCP.
PLATFORM_DEPENDENCIES = 4


def set_version(version):
    PENDING_UPDATES.clear()
    charts = (
        ("app/cetirun-chart/Chart.yaml", True, False),
        ("app/cetirun-operator/chart/Chart.yaml", True, False),
        ("app/cetirun-portal/chart/Chart.yaml", True, False),
        ("app/cetirun-platform/Chart.yaml", True, True),
        ("infra/istio/Chart.yaml", True, False),
        ("app/cetirun-ai-gateway/chart/Chart.yaml", True, False),
        ("app/cetirun-agent-broker/chart/Chart.yaml", True, False),
        ("app/cetirun-connections-mcp/chart/Chart.yaml", True, False),
    )
    for relative, has_app_version, has_dependencies in charts:
        path = ROOT / relative
        replace(path, r"^version:\s*.*$", f"version: {version}")
        if has_app_version:
            replace(path, r"^appVersion:\s*.*$", f'appVersion: "{version}"')
        if has_dependencies:
            replace(path, r"^    version:\s*.*$", f"    version: {version}", PLATFORM_DEPENDENCIES)

    replace(
        ROOT / "app/cetirun-operator/chart/values.yaml",
        r'^    version:\s*".*"$',
        f'    version: "{version}"',
    )
    replace(
        ROOT / "app/cetirun-operator/cmd/main.go",
        r'(flag\.StringVar\(&chartVersion, "chart-version", ")[^"]+(")',
        rf"\g<1>{version}\g<2>",
    )
    for relative in (
        "app/cetirun-operator/deploy/deploy.sh",
        "app/cetirun-portal/deploy/deploy.sh",
    ):
        replace(
            ROOT / relative,
            r'^CHART_VERSION="\$\{CHART_VERSION:-[^}]+\}"$',
            f'CHART_VERSION="${{CHART_VERSION:-{version}}}"',
        )

    for relative in ("app/internal/go.mod", "app/cetirun-portal/go.mod"):
        replace(
            ROOT / relative,
            r"^(\s*github\.com/Azure/CetiRun/app/cetirun-operator )v\S+$",
            rf"\g<1>v{version}",
        )
    for directory in ("app/cetirun-portal/web", "examples/pi-agent"):
        for filename in ("package.json", "package-lock.json"):
            replace(
                ROOT / directory / filename,
                r'^  "version": "[^"]+",$',
                f'  "version": "{version}",',
            )
        replace(
            ROOT / directory / "package-lock.json",
            r'^      "version": "[^"]+",$',
            f'      "version": "{version}",',
            1,
            1,
        )
    replace(
        ROOT / "app/cetirun-memory-context/assets/adapters/openclaw/package.json",
        r'^  "version": "[^"]+",$',
        f'  "version": "{version}",',
    )
    replace(
        ROOT
        / "app/cetirun-chart/agents/hermes/plugins/platforms/cetirun_teams/plugin.yaml",
        r"^version:\s*.*$",
        f"version: {version}",
    )
    replace(
        ROOT / "app/cetirun-portal/internal/handler/teamsapp/manifest.json",
        r'^  "version": "[^"]+",$',
        f'  "version": "{version}",',
    )
    for path, content in PENDING_UPDATES.items():
        path.write_text(content)


def check_version(version):
    errors = []

    charts = (
        ("app/cetirun-chart/Chart.yaml", True),
        ("app/cetirun-operator/chart/Chart.yaml", True),
        ("app/cetirun-portal/chart/Chart.yaml", True),
        ("app/cetirun-platform/Chart.yaml", True),
        ("infra/istio/Chart.yaml", True),
        ("app/cetirun-ai-gateway/chart/Chart.yaml", True),
        ("app/cetirun-agent-broker/chart/Chart.yaml", True),
        ("app/cetirun-connections-mcp/chart/Chart.yaml", True),
    )
    for relative, has_app_version in charts:
        path = ROOT / relative
        if top_level_yaml(path, "version") != version:
            errors.append(f"{relative}: version is not {version}")
        if has_app_version and top_level_yaml(path, "appVersion") != version:
            errors.append(f"{relative}: appVersion is not {version}")

    platform_chart = (ROOT / "app/cetirun-platform/Chart.yaml").read_text()
    platform_lock = (ROOT / "app/cetirun-platform/Chart.lock").read_text()
    dependency_versions = re.findall(
        r"^    version:\s*[\"']?([^\"'\s#]+)", platform_chart, re.MULTILINE
    )
    lock_versions = re.findall(
        r"^  version:\s*[\"']?([^\"'\s#]+)", platform_lock, re.MULTILINE
    )
    if dependency_versions != [version] * PLATFORM_DEPENDENCIES:
        errors.append("app/cetirun-platform/Chart.yaml: dependency versions differ")
    if lock_versions != [version] * PLATFORM_DEPENDENCIES:
        errors.append("app/cetirun-platform/Chart.lock: dependency versions differ")

    expected_text = {
        "app/cetirun-memory-context/assets/adapters/openclaw/package.json": f'"version": "{version}"',
        "app/cetirun-operator/chart/values.yaml": f'version: "{version}"',
        "app/cetirun-operator/cmd/main.go": f'"chart-version", "{version}"',
        "app/cetirun-operator/deploy/deploy.sh": f"CHART_VERSION:-{version}",
        "app/cetirun-portal/deploy/deploy.sh": f"CHART_VERSION:-{version}",
        "app/cetirun-portal/go.mod": f"cetirun-operator v{version}",
        "app/internal/go.mod": f"cetirun-operator v{version}",
        "app/cetirun-chart/agents/hermes/plugins/platforms/cetirun_teams/plugin.yaml": (
            f"version: {version}"
        ),
        "app/cetirun-portal/internal/handler/teamsapp/manifest.json": (
            f'"version": "{version}"'
        ),
    }
    for relative, expected in expected_text.items():
        if expected not in (ROOT / relative).read_text():
            errors.append(f"{relative}: expected {expected!r}")

    for directory in ("app/cetirun-portal/web", "examples/pi-agent"):
        package = json.loads((ROOT / directory / "package.json").read_text())
        package_lock = json.loads((ROOT / directory / "package-lock.json").read_text())
        if package.get("version") != version:
            errors.append(f"{directory}/package.json: version differs")
        if package_lock.get("version") != version:
            errors.append(f"{directory}/package-lock.json: version differs")
        if package_lock.get("packages", {}).get("", {}).get("version") != version:
            errors.append(f"{directory}/package-lock.json: root package differs")

    if errors:
        raise ValueError("\n".join(errors))


def parse_args():
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    subparsers.add_parser("get", help="print the Runtime Chart version")
    for command in ("set", "check"):
        subparser = subparsers.add_parser(command)
        subparser.add_argument("version", help="X.Y.Z[-prerelease], e.g. 0.1.0-dev.1; no +build metadata")
    return parser.parse_args()


def main():
    args = parse_args()
    if args.command == "get":
        print(top_level_yaml(ROOT / "app/cetirun-chart/Chart.yaml", "version"))
        return 0
    if len(args.version) > 128 or not VERSION_PATTERN.fullmatch(args.version):
        raise ValueError(
            f"{args.version!r} must be X.Y.Z or X.Y.Z-prerelease "
            "(at most 128 characters, without build metadata)"
        )
    if args.command == "set":
        set_version(args.version)
    else:
        check_version(args.version)
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError) as error:
        print(f"ERROR: {error}", file=sys.stderr)
        sys.exit(1)
