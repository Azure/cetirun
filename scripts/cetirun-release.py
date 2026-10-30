#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Build and publish the shared CetiRun inventory. Never log in or deploy."""

import argparse
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile


ROOT = Path(__file__).resolve().parents[1]
INVENTORY = ROOT / "scripts/release-artifacts.json"


def inventory():
    data = json.loads(INVENTORY.read_text())
    if data.get("schemaVersion") != 1:
        raise ValueError("Unsupported artifact inventory schema")
    for kind in ("images", "charts"):
        names = [entry["name"] for entry in data[kind]]
        if not names or len(names) != len(set(names)):
            raise ValueError(f"Empty or duplicate {kind} inventory")
        for entry in data[kind]:
            path = ROOT / (entry["script"] if kind == "images" else entry["path"] + "/Chart.yaml")
            if not path.is_file():
                raise ValueError(f"Missing artifact source: {path}")
    return data


def run(command, *, env=None, capture=False):
    result = subprocess.run(command, cwd=ROOT, env=env, text=True, check=True,
                            stdout=subprocess.PIPE if capture else None)
    return result.stdout


def coordinates(args):
    if not re.fullmatch(r"[a-z0-9][a-z0-9.:-]*(?:/[a-z0-9][a-z0-9._-]*)*", args.registry):
        raise ValueError("REGISTRY must be an explicit registry[/repository-prefix], without a scheme")
    number = r"(?:0|[1-9][0-9]*)"
    identifier = rf"(?:{number}|[0-9]*[A-Za-z-][0-9A-Za-z-]*)"
    if not re.fullmatch(rf"{number}\.{number}\.{number}(?:-{identifier}(?:\.{identifier})*)?", args.version):
        raise ValueError("VERSION must be X.Y.Z[-prerelease], not latest or a date alone")
    if not re.fullmatch(r"[0-9a-f]{40}", args.git_sha):
        raise ValueError("GIT_SHA must be the full source commit SHA")
    if not re.fullmatch(r"[a-z0-9][a-z0-9._/-]*", args.chart_namespace):
        raise ValueError("Invalid chart namespace")


def require_license():
    if os.environ.get("ACCEPT_VSCODE_SERVER_LICENSE_TERMS") != "true":
        raise ValueError("Set ACCEPT_VSCODE_SERVER_LICENSE_TERMS=true after accepting the VS Code Server license; no images were built")


def image_env(args, entry, push):
    return {
        **os.environ,
        "REGISTRY": args.registry, "VERSION": args.version, "IMAGE_TAG": args.version,
        "GIT_SHA": args.git_sha, "PUSH": str(push).lower(), "IMAGE_NAME": entry["name"],
        "BASE_IMAGE": f"{args.registry}/cetirun-base:{args.version}",
        "BUILD_BASE": "false", "BUILD_IMAGE": "false", "BUILD_HERMES": "false",
        "BUILD_DESKTOP": "false", "PLATFORMS": "linux/amd64", "PLATFORM": "linux/amd64",
        "DOCKER_DEFAULT_PLATFORM": "linux/amd64",
        **entry.get("env", {}),
    }


def preflight(args, data, kinds):
    if args.force:
        print(f"WARNING: explicit overwrite of {args.registry} version {args.version}", flush=True)
        return
    for kind in kinds:
        for entry in data[kind]:
            path = entry["name"] if kind == "images" else f"{args.chart_namespace}/{entry['name']}"
            reference = f"{args.registry}/{path}:{args.version}"
            result = subprocess.run(["docker", "buildx", "imagetools", "inspect", reference],
                                    cwd=ROOT, text=True, capture_output=True)
            if result.returncode == 0:
                raise ValueError(f"{reference} already exists; choose a new version or explicitly use --force")
            message = result.stdout + result.stderr
            missing = re.search(r"manifest[_ ]unknown|name[_ ]unknown|no such manifest", message, re.I)
            missing = missing or re.search(re.escape(reference) + r": not found(?:\s|$)", message, re.I)
            if not missing:
                raise ValueError(f"Cannot determine whether {reference} exists; check registry access before retrying")


def build_images(args, data, push):
    for entry in data["images"]:
        reference = f"{args.registry}/{entry['name']}:{args.version}"
        print(f"==> {'Publish' if push else 'Build'} {reference} (source {args.git_sha})", flush=True)
        run(["bash", entry["script"]], env=image_env(args, entry, push))
        if push:
            run(["docker", "buildx", "imagetools", "inspect", reference], capture=True)
        if args.prune:
            run(["docker", "buildx", "prune", "--all", "--force"])


def package_charts(args, data, destination):
    destination.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="cetirun-release-charts-") as temporary:
        source_root = Path(temporary)
        for entry in data["charts"]:
            shutil.copytree(ROOT / entry["path"], source_root / entry["path"],
                            ignore=shutil.ignore_patterns("charts", "__pycache__"))
        for entry in data["charts"]:
            chart = source_root / entry["path"]
            if entry["name"] == "cetirun-platform":
                run(["helm", "dependency", "build", "--skip-refresh", str(chart)])
            options = []
            if entry.get("namespace"):
                options += ["--namespace", entry["namespace"]]
            if entry.get("valuesFile"):
                options += ["--values", str(source_root / entry["valuesFile"])]
            for key, value in entry.get("lintValues", {}).items():
                options += ["--set", f"{key}={value}"]
            run(["helm", "lint", "--strict", str(chart), *options])
            run(["helm", "template", entry["name"], str(chart), *options], capture=True)
            run(["helm", "package", str(chart), "--destination", str(destination),
                 "--version", args.version, "--app-version", args.version])
            if not (destination / f"{entry['name']}-{args.version}.tgz").is_file():
                raise ValueError(f"Missing packaged chart: {entry['name']}")


def publish_charts(args, data, directory):
    destination = f"oci://{args.registry}/{args.chart_namespace}"
    with tempfile.TemporaryDirectory(prefix="cetirun-chart-verify-") as temporary:
        for entry in data["charts"]:
            archive = directory / f"{entry['name']}-{args.version}.tgz"
            run(["helm", "push", str(archive), destination])
            run(["helm", "pull", f"{destination}/{entry['name']}", "--version", args.version,
                 "--destination", temporary])
            pulled = Path(temporary) / archive.name
            if not pulled.is_file() or archive.read_bytes() != pulled.read_bytes():
                raise ValueError(f"Chart pull does not match the published package: {entry['name']}")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("list", "check", "plan", "build-images", "publish-images",
                                             "package-charts", "publish-charts", "publish"))
    parser.add_argument("kind", nargs="?", choices=("images", "charts", "chart-paths"))
    parser.add_argument("--registry", default=os.environ.get("REGISTRY", ""))
    parser.add_argument("--version", default=os.environ.get("VERSION", ""))
    parser.add_argument("--git-sha", default=os.environ.get("GIT_SHA", ""))
    parser.add_argument("--chart-namespace", default=os.environ.get("CHART_NAMESPACE", "helm"))
    parser.add_argument("--force", action="store_true", help="explicitly permit replacing existing version tags")
    parser.add_argument("--prune", action="store_true", help="prune build cache after each image on a disposable CI agent")
    parser.add_argument("--package-dir", type=Path)
    args = parser.parse_args(argv)
    try:
        data = inventory()
        if args.command == "list":
            if not args.kind:
                raise ValueError("list requires images, charts, or chart-paths")
            entries = data["charts" if args.kind == "chart-paths" else args.kind]
            print("\n".join(entry["path" if args.kind == "chart-paths" else "name"] for entry in entries))
            return 0
        if args.command == "check":
            discovered = {path.parent.relative_to(ROOT).as_posix() for pattern in
                          ("app/*/Chart.yaml", "app/*/chart/Chart.yaml", "infra/*/Chart.yaml")
                          for path in ROOT.glob(pattern)}
            if discovered != {entry["path"] for entry in data["charts"]}:
                raise ValueError("Release chart inventory differs from repository charts")
            print(f"Artifact inventory: {len(data['images'])} images, {len(data['charts'])} charts")
            return 0
        coordinates(args)
        if args.command == "plan":
            print(json.dumps({"registry": args.registry, "version": args.version,
                              "gitSha": args.git_sha, **data}, indent=2))
            return 0
        image_work = args.command in ("build-images", "publish-images", "publish")
        if image_work:
            require_license()
        run([sys.executable, "-B", "scripts/cetirun-version.py", "check", args.version])
        if args.command.startswith("publish"):
            kinds = ("images", "charts") if args.command == "publish" else (args.command.removeprefix("publish-"),)
            preflight(args, data, kinds)
        if args.command == "package-charts":
            if args.package_dir is None:
                raise ValueError("package-charts requires --package-dir")
            package_charts(args, data, args.package_dir.resolve())
        elif args.command in ("publish-charts", "publish"):
            with tempfile.TemporaryDirectory(prefix="cetirun-publish-") as temporary:
                directory = Path(temporary)
                package_charts(args, data, directory)
                if image_work:
                    build_images(args, data, push=True)
                publish_charts(args, data, directory)
        else:
            build_images(args, data, push=args.command == "publish-images")
    except (ValueError, KeyError, OSError, subprocess.CalledProcessError) as error:
        print(f"ERROR: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
