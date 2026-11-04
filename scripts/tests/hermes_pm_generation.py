# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Prepare a synthetic plugin generation, then qualify its offline activation.

Use the same HERMES_HOME mount in both runs. --prepare requires an empty HOME
and permits pinned dependency downloads. Run --verify with --network=none and
a read-only root filesystem; it does not install or resolve dependencies.
"""

import argparse
import json
import os
from pathlib import Path
import sys


ROOT = Path(os.environ.get("HERMES_INSTALL_DIR", "/usr/local/lib/hermes-agent")).resolve()
MARKER = ".cetirun-pm-qualification"


def prepare(home):
    import pm
    from pm.environments import runtime_facts_path
    from cetirun_pm.runtime import REQUIRED_EXTRAS, validate_selection

    if any(home.iterdir()):
        raise RuntimeError("qualification preparation requires an empty synthetic HOME")
    (home / MARKER).write_text("synthetic PM qualification\n")
    validate_selection(ROOT)
    facts = runtime_facts_path(ROOT)
    assert not facts.exists(), "cold startup created a dependency selection"
    plugin = home / "plugins/cetirun-pm-fixture"
    plugin.mkdir(parents=True)
    manifest = plugin / "plugin.yaml"
    manifest.write_text(
        "name: cetirun-pm-fixture\nversion: 1.0.0\n"
        "requires_hermes: '>=0.22.7'\npython_dependencies:\n  - certifi\n"
    )
    (home / "config.yaml").write_text("plugins:\n  enabled:\n    - cetirun-pm-fixture\n")
    pm.sync_venv(list(REQUIRED_EXTRAS), explicit=True, project_root=ROOT)
    original = facts.read_bytes()
    assert validate_selection(ROOT).is_relative_to(home)
    original_manifest = manifest.read_text()
    manifest.write_text(original_manifest.replace("  - certifi\n", "  - certifi==0.0.0\n"))
    try:
        pm.sync_venv(list(REQUIRED_EXTRAS), explicit=True, project_root=ROOT)
    except pm.InstallError:
        pass
    else:
        raise AssertionError("an unavailable dependency was accepted")
    finally:
        manifest.write_text(original_manifest)
    assert facts.read_bytes() == original, "failed preparation changed the selection"
    print("PM generation prepared; failed preparation preserved its selection")


def verify(home):
    from pm.environments import runtime_facts_path
    from cetirun_pm.runtime import activate, validate_selection

    if (home / MARKER).read_text() != "synthetic PM qualification\n":
        raise RuntimeError("qualification HOME marker is invalid")
    facts = runtime_facts_path(ROOT)
    original = facts.read_bytes()
    selected = validate_selection(ROOT)
    assert selected.is_relative_to(home)
    assert facts.read_bytes() == original
    try:
        for condition in ("stale", "missing", "extras"):
            state = json.loads(original)
            fact = state["packages"]["venv"]
            if condition == "stale":
                fact["stamp"] = "0" * 64
            elif condition == "missing":
                fact["environment"] = str(selected.parent / "missing")
            else:
                fact["extras"] = ["all"]
            damaged = json.dumps(state).encode()
            facts.write_bytes(damaged)
            try:
                validate_selection(ROOT)
            except RuntimeError:
                pass
            else:
                raise AssertionError(f"{condition} selection was accepted")
            assert facts.read_bytes() == damaged, f"{condition} selection was modified"
    finally:
        facts.write_bytes(original)
    activate(ROOT)
    import certifi
    import sqlite3

    assert Path(certifi.__file__).is_relative_to(selected)
    assert sqlite3.sqlite_version_info == (3, 53, 4)
    assert facts.read_bytes() == original
    print("Offline PM activation, mismatch rejection and SQLite checks passed")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--prepare", action="store_true")
    mode.add_argument("--verify", action="store_true")
    args = parser.parse_args()
    if not os.environ.get("HERMES_HOME"):
        raise RuntimeError("set HERMES_HOME to a dedicated synthetic qualification directory")
    home = Path(os.environ["HERMES_HOME"]).resolve()
    home.mkdir(parents=True, exist_ok=True)
    os.environ.update(HERMES_HOME=str(home), HERMES_RUNTIME_DIR=str(ROOT / "tools"),
                      HERMES_DISABLE_LAZY_INSTALLS="1")
    sys.path.insert(0, str(ROOT))
    (prepare if args.prepare else verify)(home)


if __name__ == "__main__":
    main()
