#!/bin/sh
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# Retire the GitHub Copilot sign-in the in-agent helper made, for an Agent
# that takes its models from the platform AI Gateway: its owner signs in
# through the Portal, and that token stays with the Agent's Copilot proxy.
# The helper left the GitHub token in the home, where the agent can read it:
# in the github-copilot:github auth profile, in the secret-store entries the
# device login keeps it in, in the state directory's .env, and in the auth
# profile files of earlier OpenClaw versions. This removes those through
# OpenClaw's own interface where it has one, once per home, after doctor has
# imported any earlier auth profile files, and keeps every other credential.
# A step that fails is reported with OpenClaw's reason and tried again at the
# next start. It does not keep the agent from starting: an init container
# that keeps failing keeps the Agent down, and its StatefulSet then rolls no
# corrected chart onto the Pod until someone deletes the Pod.
set -u
if [ "${1:-}" = "--help" ]; then
  printf '%s\n' "Remove the in-agent GitHub Copilot sign-in from an OpenClaw state directory, once."
  exit 0
fi

state=${OPENCLAW_STATE_DIR:-$HOME/.openclaw}
marker=$state/.cetirun-copilot-sign-in-retired
profile=github-copilot:github
store_prefix=GITHUB_COPILOT_TOKEN
env_keys='^[[:space:]]*(export[[:space:]]+)?(COPILOT_GITHUB_TOKEN|OPENCLAW_GITHUB_TOKEN)='

[ -d "$state" ] || exit 0
[ -f "$marker" ] && exit 0

log() {
  printf 'retire-copilot-sign-in: %s\n' "$*" >&2
}

if ! scratch=$(mktemp -d); then
  log "could not create a scratch directory; the next start tries again"
  exit 0
fi
trap 'rm -rf "$scratch"' EXIT
output=$scratch/output
errors=$scratch/errors
: >"$output"
: >"$errors"
# The OpenClaw CLI resolves the secrets the Agent's configuration refers to,
# which only the agent container's environment holds, and fails without
# them. The auth profiles and the secret store live in the state directory:
# give the CLI a configuration of its own.
export OPENCLAW_CONFIG_PATH="$scratch/openclaw.json"
printf '{"plugins":{"entries":{}}}\n' >"$OPENCLAW_CONFIG_PATH"

complete=true

# Why the last OpenClaw command failed, in OpenClaw's words: the error of its
# JSON output, or the reason it printed.
reason() {
  why=$(jq -r '.error.message? // empty' "$output" 2>/dev/null)
  [ -n "$why" ] || why=$(sed -n 's/^\[openclaw\] Reason: //p' "$errors" | tail -n 1)
  [ -n "$why" ] || why=$(grep -v '^[[:space:]]*$' "$errors" | tail -n 1 | cut -c1-300)
  printf '%s' "${why:-no reason given}"
}

# Report an OpenClaw step that failed, and leave the home unmarked.
failed() {
  log "could not $1: $(reason); the next start tries again"
  complete=false
}

# Run one OpenClaw command, and report it when it fails.
openclaw_step() {
  description=$1
  shift
  "$@" >"$output" 2>"$errors" && return 0
  failed "$description"
  return 1
}

# Whether OpenClaw's databases mention text, read as bytes: a cheap test that
# spares the OpenClaw CLI in homes the helper never signed in. The shared
# state database holds the secret store and the main agent's auth profiles;
# the agent's own database holds the profiles doctor imported. Deleted rows
# stay in the bytes until a database is compacted.
database_mentions() {
  for file in "$state"/state/openclaw.sqlite "$state"/state/openclaw.sqlite-wal \
    "$state"/agents/*/agent/openclaw-agent.sqlite "$state"/agents/*/agent/openclaw-agent.sqlite-wal; do
    if [ -f "$file" ] && grep -aqF -- "$1" "$file"; then
      return 0
    fi
  done
  return 1
}

# The secret-store entries the device login keeps the token in.
copilot_store_entries() {
  openclaw secrets store list --json >"$output" 2>"$errors" &&
    jq -e 'type == "array"' "$output" >/dev/null 2>&1 &&
    jq -r --arg prefix "$store_prefix" '.[] | .name | select(startswith($prefix))' "$output"
}

# Replace a file with what a filter makes of it, keeping the file private.
rewrite() {
  target=$1
  shift
  temp=$(mktemp "$target.tmp.XXXXXX") || return 1
  if "$@" <"$target" >"$temp" 2>/dev/null && chmod 600 "$temp" && mv -f "$temp" "$target"; then
    return 0
  fi
  rm -f "$temp"
  return 1
}

without_token_variables() {
  grep -Ev "$env_keys"
  # 1 only means that no other line is left.
  [ $? -le 1 ]
}

# Profile files keep the profiles under "profiles" or, the oldest, at the top.
without_profile() {
  jq --arg id "$profile" '
    if type != "object" then .
    elif (.profiles | type) == "object" then .profiles |= del(.[$id])
    elif has("profiles") then .
    else del(.[$id]) end'
}

signed_in=false
if database_mentions "$profile" || database_mentions "$store_prefix"; then
  signed_in=true
fi

# A removed entry is only marked deleted, and OpenClaw keeps its value for 30
# days: overwrite every value first. The profile and the entries go only once
# that worked, as logging the profile out may remove the entry it refers to.
store_ready=true
overwritten=" "
if database_mentions "$store_prefix"; then
  if entries=$(copilot_store_entries); then
    for name in $entries; do
      if openclaw_step "overwrite the secret-store entry $name" \
        openclaw secrets store set "$name" --kind secret --value-file - <<EOF
retired
EOF
      then
        overwritten="$overwritten$name "
      else
        store_ready=false
      fi
    done
  else
    failed "list the secret store"
    store_ready=false
  fi
fi

if [ "$store_ready" = true ] && database_mentions "$profile"; then
  # Among other reasons, OpenClaw refuses an auth store that holds no profile
  # while earlier profile files wait for doctor, when it is turned off.
  if openclaw models auth list --agent main --json >"$output" 2>"$errors" &&
    jq -e '.profiles | type == "array"' "$output" >/dev/null 2>&1; then
    if jq -e --arg id "$profile" 'any(.profiles[]; .id == $id)' "$output" >/dev/null &&
      openclaw_step "remove the $profile auth profile" openclaw models auth logout "$profile" --agent main --yes; then
      log "removed the $profile auth profile"
    fi
  else
    failed "list the auth profiles"
  fi
fi

if [ "$store_ready" = true ] && [ "$overwritten" != " " ]; then
  if entries=$(copilot_store_entries); then
    for name in $entries; do
      case "$overwritten" in
        *" $name "*) ;;
        *) continue ;;
      esac
      if openclaw_step "remove the secret-store entry $name" openclaw secrets store rm "$name" --yes; then
        log "removed the secret-store entry $name"
      fi
    done
  else
    failed "list the secret store"
  fi
fi

# Rewrite the databases so that no deleted row keeps the token's bytes, also
# after an earlier start removed the rows but not their bytes.
if [ "$signed_in" = true ]; then
  openclaw_step "compact the shared state database" openclaw doctor --state-sqlite compact
  if [ -f "$state/agents/main/agent/openclaw-agent.sqlite" ]; then
    openclaw_step "compact the agent database" openclaw doctor --session-sqlite compact --session-sqlite-agent main
  fi
fi

env_file=$state/.env
if [ -f "$env_file" ] && grep -Eq "$env_keys" "$env_file"; then
  if rewrite "$env_file" without_token_variables; then
    log "removed the GitHub token from $env_file"
  else
    log "could not rewrite $env_file; the next start tries again"
    complete=false
  fi
fi

# Profile files of earlier versions, and the copies doctor archived after
# importing them.
for file in "$state"/agents/*/agent/auth-profiles.json "$state"/agents/*/agent/auth-profiles.json.migrated-*; do
  [ -f "$file" ] || continue
  grep -qF "\"$profile\"" "$file" || continue
  if rewrite "$file" without_profile; then
    log "removed the $profile profile from $file"
  else
    log "could not rewrite $file; the next start tries again"
    complete=false
  fi
done

if [ "$complete" = true ] && ! touch "$marker"; then
  log "could not mark $state as done; the next start runs again"
fi
exit 0
