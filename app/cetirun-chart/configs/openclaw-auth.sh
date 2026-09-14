#!/bin/sh
set -e

# copilot-auth — Authenticate GitHub Copilot for OpenClaw.
#
# Run this from the ttyd web terminal on first setup.
#
# Usage:
#   copilot-auth          # interactive login + bootstrap
#   copilot-auth status   # check current auth status

# --- status subcommand ---------------------------------------------------
cmd_status() {
  openclaw models
}


# --- login (default) -----------------------------------------------------
cmd_login() {
  openclaw models auth login-github-copilot

  # Bootstrap OpenClaw agent after successful authentication
  echo ""
  echo "Bootstrapping OpenClaw agent..."
  supervisorctl -c /tmp/supervisord.conf restart openclaw
  openclaw agent --message "Read your AGENTS.md and bootstrap yourself" --agent main
}

# --- main -----------------------------------------------------------------
case "${1:-}" in
  status) cmd_status ;;
  *)      cmd_login ;;
esac
