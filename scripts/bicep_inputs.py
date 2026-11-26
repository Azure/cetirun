#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.
"""Compile Bicep deployment inputs into private JSON snapshots offline."""

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys


def compile_bicep(*args):
    # Never install tools, restore modules, or print compiled parameter values.
    result = subprocess.run(
        ["bicep", *map(str, args), "--stdout", "--no-restore"],
        check=True, text=True, stdout=subprocess.PIPE,
    )
    return json.loads(result.stdout)


def compile_inputs(template_path, parameter_path):
    if parameter_path.suffix == ".bicepparam":
        bundle = compile_bicep("build-params", parameter_path, "--bicep-file", template_path)
        template = json.loads(bundle["templateJson"])
        parameters = json.loads(bundle["parametersJson"])
    else:
        template = compile_bicep("build", template_path)
        parameters = json.loads(parameter_path.read_text(encoding="utf-8"))
    if not isinstance(template, dict):
        raise ValueError("template must be an object")
    if not isinstance(parameters, dict) or not isinstance(parameters.get("parameters"), dict):
        raise ValueError("parameters must contain a parameters object")
    return template, parameters


def write_snapshots(output_dir, template, parameters):
    """Write the compiled inputs once into an existing private directory."""
    for name, value in (("template", template), ("parameters", parameters)):
        path = output_dir / f"{name}.json"
        descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, "w", encoding="utf-8") as output:
            json.dump(value, output)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--template", type=Path, required=True)
    parser.add_argument("--parameters", type=Path, required=True)
    parser.add_argument("--output-dir", type=Path, required=True,
                        help="existing private directory for compiled JSON inputs")
    args = parser.parse_args(argv)
    try:
        template, parameters = compile_inputs(args.template, args.parameters)
        write_snapshots(args.output_dir, template, parameters)
    except (OSError, ValueError, KeyError, TypeError, subprocess.CalledProcessError):
        print("Error: could not snapshot deployment inputs. Ensure the template and parameter file "
              "are valid, the Bicep CLI is installed on PATH, and the output directory is empty "
              "and writable.", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
