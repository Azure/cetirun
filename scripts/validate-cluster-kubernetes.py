#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Check the effective AKS Kubernetes version locally before cloud writes."""

import argparse
from pathlib import Path
import re
import subprocess
import sys

from bicep_inputs import compile_inputs, write_snapshots


MINIMUM_VERSION = (1, 37)
STABLE_VERSION = re.compile(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:\.(0|[1-9][0-9]*))?")


def effective_version(template, parameters):
    supplied = parameters["parameters"]
    if "kubernetesVersion" in supplied:
        # A reference or unresolved expression cannot establish compatibility.
        return supplied["kubernetesVersion"]["value"]
    return template["parameters"]["kubernetesVersion"]["defaultValue"]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--template", type=Path, required=True)
    parser.add_argument("--parameters", type=Path, required=True)
    parser.add_argument("--output-dir", type=Path, help="existing private directory for validated JSON inputs")
    args = parser.parse_args(argv)
    try:
        template, parameters = compile_inputs(args.template, args.parameters)
        version = effective_version(template, parameters)
        match = STABLE_VERSION.fullmatch(version) if isinstance(version, str) else None
        if match is None or tuple(map(int, match.group(1, 2))) < MINIMUM_VERSION:
            raise ValueError("unsupported Kubernetes version")
        if args.output_dir is not None:
            write_snapshots(args.output_dir, template, parameters)
    except (OSError, ValueError, KeyError, TypeError, subprocess.CalledProcessError):
        print(
            "Error: could not establish a supported Kubernetes version. "
            "Sandbox and CetiAdmin admission require stable Kubernetes 1.37 or newer. "
            "Set kubernetesVersion to a supported major.minor or major.minor.patch in PARAM_FILE "
            "(or omit it to use the template default). "
            "Ensure the parameter file is valid and the Bicep CLI is installed on PATH. "
            "Confirm that the target AKS region offers the selected version; do not fall back "
            "to an older version.",
            file=sys.stderr,
        )
        return 1
    print(f"Kubernetes version preflight passed: {version}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
