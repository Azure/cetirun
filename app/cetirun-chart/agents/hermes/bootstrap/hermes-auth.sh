#!/bin/sh
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -e

# copilot-auth — Authenticate GitHub Copilot for Hermes.
#
# Delegates to Hermes' supported public CLI instead of importing private
# hermes_cli implementation functions.
#
# Usage:
#   copilot-auth          # select/authenticate a model (Copilot is preselected)
#   copilot-auth status   # check current auth status

export HERMES_HOME="${HERMES_HOME:-/home/vibe/.hermes}"

cmd_status() {
  hermes auth status copilot
}

cmd_login() {
  hermes model
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
