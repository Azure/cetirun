#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Run source tests, or explicitly qualify an existing local example image."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import subprocess
import sys
import tempfile
import threading

from mock_gateway import make_server
from probe import RESTART_CONTENT


EXAMPLE = Path(__file__).resolve().parent
ROOT = EXAMPLE.parents[1]
CHART = ROOT / "app/cetirun-chart"


def run(args, capture=False):
    return subprocess.run(args, check=True, text=True, timeout=60,
        stdout=subprocess.PIPE if capture else None).stdout


def mount(source, target, readonly=False):
    # Docker's --mount parser treats commas as separators; reject ambiguous
    # local paths instead of constructing another Docker option.
    if "," in str(source):
        raise ValueError("Docker mount paths must not contain commas")
    return ["--mount", f"type=bind,src={source},dst={target}" + (",readonly" if readonly else "")]


def qualify_image(image, home):
    if shutil.which("docker") is None or not home.is_dir() or home.is_symlink():
        raise ValueError("Docker and an existing, non-symlink synthetic HOME directory are required")
    if home.resolve() in (Path("/"), Path.home(), ROOT):
        raise ValueError("Use a dedicated synthetic HOME, not a workspace or user directory")
    if any(home.iterdir()):
        raise ValueError("First qualification requires an empty synthetic HOME; existing data is never reset")
    metadata = json.loads(run(["docker", "image", "inspect", image], capture=True))[0]
    user = metadata["Config"]["User"]
    if not re.fullmatch(r"[1-9][0-9]*:[1-9][0-9]*", user):
        raise ValueError("Example image must declare numeric non-root USER uid:gid")
    expected_uid, expected_gid = map(int, user.split(":"))
    image_id = metadata["Id"]
    base = ["docker", "run", "--rm", "--pull=never", "--read-only", "--cap-drop=ALL",
        "--security-opt=no-new-privileges", "--tmpfs", "/tmp:rw,nosuid,nodev"]
    with tempfile.TemporaryDirectory(prefix="cetirun-custom-context-") as temporary:
        context = Path(temporary)
        shutil.copytree(EXAMPLE / "context", context, dirs_exist_ok=True)
        nonces = []
        for file in (context / "AGENTS.md", *sorted((context / "skills").glob("*/SKILL.md"))):
            nonce = secrets.token_hex(16)
            file.write_text(file.read_text() + "\n" + nonce + "\n")
            nonces.append(nonce)
        # Only synthetic context copies are made readable. HOME permissions are
        # not changed: an incompatible numeric identity must fail qualification.
        for directory in (context, *(p for p in context.rglob("*") if p.is_dir())):
            directory.chmod(0o755)
        for file in (p for p in context.rglob("*") if p.is_file()):
            file.chmod(0o444)

        def probe(mode):
            command = base + ["--network=none", "--entrypoint=python3"]
            command += mount(EXAMPLE / "probe.py", "/etc/cetirun/qualify-probe.py", True)
            if mode != "identity":
                command += mount(home.resolve(), "/home/worker")
                command += mount(context, "/home/worker/.agents", True)
            result = json.loads(run(command + [image_id, "/etc/cetirun/qualify-probe.py", "--mode", mode], capture=True))
            if result.get("uid") != expected_uid or result.get("gid") != expected_gid or \
                    result.get("username") != "worker" or result.get("home") != "/home/worker":
                raise ValueError("Actual runtime UID/GID/user/HOME differs from the example contract")
            if mode != "identity" and (not result.get("context_readonly") or result.get("flag") != "seeded-v1\n" + image_id + "\n"):
                raise ValueError("HOME completion marker or read-only context is invalid")
            return result

        identity = probe("identity")
        init = base + ["--network=none", "--entrypoint=/bin/sh"]
        init += mount(home.resolve(), "/home-data")
        init += mount(context, "/etc/cetirun-context", True)
        init += mount(CHART / "configs/init-home-common.sh", "/etc/cetirun/init-home-common.sh", True)
        init += mount(CHART / "agents/custom/bootstrap/init-home.sh", "/etc/cetirun/custom-init-home.sh", True)
        init += ["-e", "SOURCE_HOME=/home/worker", "-e", "TARGET_HOME=/home-data",
            "-e", "CETIRUN_INIT_COMMON=/etc/cetirun/init-home-common.sh",
            "-e", "CETIRUN_CONTEXT_DIR=/etc/cetirun-context",
            "-e", f"CETIRUN_INIT_IMAGE={image_id}", image_id, "/etc/cetirun/custom-init-home.sh"]
        run(init)
        first = probe("seed")
        expected_seed = hashlib.sha256((EXAMPLE / "initial-home/seed.txt").read_bytes()).hexdigest()
        if first.get("seed_digest") != expected_seed:
            raise ValueError("Initial image HOME fixture was not copied")
        changed = probe("prepare-restart")
        preserved_digest = hashlib.sha256(RESTART_CONTENT).hexdigest()
        if changed.get("seed_digest") != preserved_digest:
            raise ValueError("Cannot prepare the synthetic restart control")
        run(init)
        restarted = probe("seed")
        if restarted.get("seed_digest") != preserved_digest or restarted.get("flag") != first["flag"]:
            raise ValueError("Repeated initialization did not preserve user content and the HOME marker")
        gateway = make_server(expected_context=nonces)
        thread = threading.Thread(target=gateway.serve_forever, kwargs={"poll_interval": 0.02}, daemon=True)
        thread.start()
        try:
            # Linux host networking is used only for this explicit local
            # qualification, never in the Kubernetes runtime plan.
            main = base + ["--network=host"]
            main += mount(home.resolve(), "/home/worker")
            main += mount(context, "/home/worker/.agents", True)
            main += ["-e", "HOME=/home/worker", "-e", "PORT=",
                "-e", f"OPENAI_BASE_URL=http://127.0.0.1:{gateway.server_port}/v1",
                "-e", "OPENAI_API_KEY=sk-dummy", image_id, "--headless-once"]
            run(main)
        finally:
            gateway.shutdown()
            gateway.server_close()
            thread.join(2)
        if sum(method == "POST" for method, _, _ in gateway.requests) != 1 or len(gateway.successful_completions) != 1:
            raise ValueError("Image did not complete exactly one approved mock model request")
        memory = probe("memory")
        if memory.get("memory") != gateway.successful_completions[0]:
            raise ValueError("Private memory does not contain the successful mock result")
        if memory.get("seed_digest") != preserved_digest:
            raise ValueError("Main entrypoint changed the preserved HOME control")
        print(json.dumps({"image_id": image_id, "image_user": user, "observed_identity": identity,
            "checks": ["same-local-image", "seed-and-reinitialize", "mock-model-context-memory"],
            "not_checked": ["HTTP in container", "Kubernetes", "CSI", "network policy", "real models", "registry credentials"]}, indent=2))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", help="Explicitly run an already-present local example image; never pulls or builds")
    parser.add_argument("--home-dir", type=Path, help="Empty synthetic directory already writable by the image UID; preserved after execution")
    args = parser.parse_args()
    if bool(args.image) != bool(args.home_dir):
        parser.error("--image and --home-dir must be supplied together")
    if args.image:
        qualify_image(args.image, args.home_dir)
    else:
        if not (ROOT / "scripts/tests/test_home_initialization.py").is_file():
            raise ValueError("Source qualification requires the shared HOME regression suite")
        env = {**os.environ, "PYTHONDONTWRITEBYTECODE": "1"}
        subprocess.run([sys.executable, "-B", "-m", "unittest", "discover", "-s", str(ROOT / "scripts/tests"),
            "-p", "test_custom*.py", "-v"], env=env, check=True, timeout=60)
        subprocess.run([sys.executable, "-B", "-m", "unittest", "discover", "-s", str(ROOT / "scripts/tests"),
            "-p", "test_home_initialization.py", "-v"], env=env, check=True, timeout=60)


if __name__ == "__main__":
    main()
