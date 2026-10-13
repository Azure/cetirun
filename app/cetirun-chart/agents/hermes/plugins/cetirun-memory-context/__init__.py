# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

import importlib.util
import os
from pathlib import Path


def register(ctx):
    if os.environ.get("CETIRUN_MEMORY_ASSETS") != "/opt/cetirun-memory":
        return
    root = Path("/opt/cetirun-memory")
    spec = importlib.util.spec_from_file_location("cetirun_memory_adapter", root / "adapters/hermes/__init__.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    module.register(ctx)
