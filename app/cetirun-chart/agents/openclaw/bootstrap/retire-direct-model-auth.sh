#!/bin/sh
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# Remove every in-home model auth profile after OpenClaw doctor has imported
# legacy profile files. A policy-bound Agent gets its sole model credential from
# the platform AI Gateway, so retaining a profile would let OpenClaw prefer a
# direct provider credential over the Gateway environment variables.
#
# This is an init-container authorization boundary. If cleanup cannot prove
# that every direct credential source was retired, the Agent container must
# not start. Kubernetes retries the init container after a transient failure.
set -eu

state=${OPENCLAW_STATE_DIR:-$HOME/.openclaw}
marker=$state/.cetirun-direct-model-auth-retired
[ -d "$state" ] || exit 0

# The marker is audit evidence only. A running harness can create another
# profile after an earlier cleanup, so every Pod start must enumerate and
# retire the durable auth stores again.

log() {
  printf 'retire-direct-model-auth: %s\n' "$*" >&2
}

scratch=$(mktemp -d) || {
  log "could not create a scratch directory; refusing to start the Agent"
  exit 1
}
trap 'rm -rf "$scratch"' EXIT
output=$scratch/output
errors=$scratch/errors
export OPENCLAW_CONFIG_PATH="$scratch/openclaw.json"
printf '{"plugins":{"entries":{}}}\n' >"$OPENCLAW_CONFIG_PATH"

# OpenClaw imports provider variables from this file. They are durable direct
# credentials, unlike the Pod-only Gateway key, and therefore must not survive
# on a policy-controlled HOME. Keep unrelated tool credentials intact.
env_file=$state/.env
env_keys='^[[:space:]]*(export[[:space:]]+)?(COPILOT_GITHUB_TOKEN|OPENCLAW_GITHUB_TOKEN|OPENAI_API_KEY|ANTHROPIC_API_KEY)='
if [ -f "$env_file" ] && grep -Eq "$env_keys" "$env_file"; then
  temp=$(mktemp "$env_file.tmp.XXXXXX") || {
    log "could not create a temporary file for $env_file; refusing to start the Agent"
    exit 1
  }
  status=0
  grep -Ev "$env_keys" "$env_file" >"$temp" || status=$?
  # grep returns 1 when every line was removed, which is a successful empty
  # result. Any result above 1 is a real read or expression failure.
  if [ "$status" -gt 1 ]; then
    rm -f "$temp"
    log "could not filter $env_file; refusing to start the Agent"
    exit 1
  fi
  if ! chmod 600 "$temp" || ! mv -f "$temp" "$env_file"; then
    rm -f "$temp"
    log "could not rewrite $env_file; refusing to start the Agent"
    exit 1
  fi
  log "removed direct model credentials from $env_file"
fi

if ! openclaw models auth list --agent main --json >"$output" 2>"$errors" ||
  ! jq -e '.profiles | type == "array"' "$output" >/dev/null 2>&1; then
  reason=$(jq -r '.error.message? // empty' "$output" 2>/dev/null)
  [ -n "$reason" ] || reason=$(sed -n 's/^\[openclaw\] Reason: //p' "$errors" | tail -n 1)
  log "could not list model auth profiles: ${reason:-no reason given}; refusing to start the Agent"
  exit 1
fi

complete=true
profiles=$(jq -r '.profiles[]?.id // empty' "$output") || {
  log "could not parse model auth profiles; refusing to start the Agent"
  exit 1
}
while IFS= read -r profile; do
  [ -n "$profile" ] || continue
  if openclaw models auth logout "$profile" --agent main --yes >"$output" 2>"$errors"; then
    log "removed model auth profile $profile"
  else
    log "could not remove model auth profile $profile; refusing to start the Agent"
    complete=false
  fi
done <<EOF
$profiles
EOF

# Profile files can survive a version migration until a later doctor run. They
# are not an allowed credential source for a Gateway-routed Agent either.
for file in "$state"/agents/*/agent/auth-profiles.json "$state"/agents/*/agent/auth-profiles.json.migrated-*; do
  [ -f "$file" ] || continue
  temp=$(mktemp "$file.tmp.XXXXXX") || {
    log "could not create a temporary file for $file; refusing to start the Agent"
    complete=false
    continue
  }
  if jq 'if type == "object" and (.profiles | type) == "object" then .profiles = {} elif type == "object" then {} else . end' \
      "$file" >"$temp" 2>/dev/null && chmod 600 "$temp" && mv -f "$temp" "$file"; then
    :
  else
    rm -f "$temp"
    log "could not clear model auth profiles in $file; refusing to start the Agent"
    complete=false
  fi
done

# SQLite retains deleted rows until compacted. Check this postcondition on
# every retry: logout may have succeeded in an earlier attempt whose compact
# failed, leaving no live profile to signal that token bytes remain.
if [ -f "$state/state/openclaw.sqlite" ] || [ -f "$state/state/openclaw.sqlite-wal" ]; then
  if ! openclaw doctor --state-sqlite compact >"$output" 2>"$errors"; then
    log "could not compact the shared state database; refusing to start the Agent"
    complete=false
  fi
fi
if [ -f "$state/agents/main/agent/openclaw-agent.sqlite" ] ||
  [ -f "$state/agents/main/agent/openclaw-agent.sqlite-wal" ]; then
  if ! openclaw doctor --session-sqlite compact --session-sqlite-agent main >"$output" 2>"$errors"; then
    log "could not compact the agent database; refusing to start the Agent"
    complete=false
  fi
fi

[ "$complete" = true ] || exit 1
if [ -f "$env_file" ] && grep -Eq "$env_keys" "$env_file"; then
  log "direct model credentials remain in $env_file; refusing to start the Agent"
  exit 1
fi
if ! touch "$marker"; then
  log "could not mark $state as done; refusing to start the Agent"
  exit 1
fi
