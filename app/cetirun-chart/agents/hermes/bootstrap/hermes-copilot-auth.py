#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Keep the helper path used by older Charts without importing Hermes internals."""

import argparse
import os


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", nargs="?", choices=("login", "status"), default="login")
    args = parser.parse_args(argv)
    command = (
        ["hermes", "auth", "status", "copilot"]
        if args.command == "status"
        else ["hermes", "model"]
    )
    os.execvp(command[0], command)


if __name__ == "__main__":
    main()
