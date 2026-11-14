#!/bin/sh
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# session-logger - Copy the CetiRun session log to stdout.
#
# The runtime's CetiRun session log plugin writes one JSON record for each
# user input, assistant output, tool call, and tool result to
# ~/.<agent>/cetirun-session-log/<agent>.jsonl. session-logger.mjs follows that
# file and its rotated predecessor, and writes each record to stdout, where the
# cluster's log collection captures it.
#
# Runs as a sidecar container with the PVC mounted read-only.

set -e

exec node /etc/cetirun/session-logger.mjs
