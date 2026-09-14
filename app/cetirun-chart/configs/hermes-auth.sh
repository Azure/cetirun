#!/bin/sh
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -e

# copilot-auth — Authenticate GitHub Copilot for Hermes.
#
# Runs GitHub Copilot OAuth first, then continues Hermes' native Copilot
# model selection flow.
#
# Usage:
#   copilot-auth          # GitHub login + model setup
#   copilot-auth status   # check current auth status

export HERMES_HOME="${HERMES_HOME:-/home/vibe/.hermes}"
HELPER="${HERMES_COPILOT_AUTH_HELPER:-/usr/local/share/cetirun/hermes-copilot-auth.py}"
PYTHON_BIN="${HERMES_PYTHON:-/usr/local/lib/hermes-agent/bin/python}"
if [ ! -x "$PYTHON_BIN" ]; then
  PYTHON_BIN="python3"
fi

cmd_status() {
  "$PYTHON_BIN" "$HELPER" status
}

cmd_login() {
  "$PYTHON_BIN" "$HELPER" login
  if command -v supervisorctl >/dev/null 2>&1 && [ -S /tmp/supervisor.sock ]; then
    echo ""
    echo "Restarting Hermes gateway..."
    supervisorctl -c /tmp/supervisord.conf restart hermes-gateway || true
  fi
}

case "${1:-}" in
  status) cmd_status ;;
  *)      cmd_login ;;
esac
