#!/bin/sh
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# Native Sandbox init runs after doctor, which may import older auth profiles.
# Never print profile contents or CLI errors, which may include credentials.
set -eu
if [ "${1:-}" = "--help" ]; then
  printf '%s\n' "Retire saved OpenClaw model auth before starting a native Sandbox."
  exit 0
fi

fail() {
  printf 'retire-direct-model-auth: %s; refusing to start the Agent\n' "$1" >&2
  exit 1
}

state=${OPENCLAW_STATE_DIR:-$HOME/.openclaw}
[ -d "$state" ] && [ ! -L "$state" ] || fail "state directory unavailable"
for directory in "$state/state" "$state/agents" "$state"/agents/* "$state"/agents/*/agent; do
  [ ! -L "$directory" ] || fail "unsupported auth directory link"
done
for directory in "$state"/agents/*; do
  if [ -d "$directory/agent" ] && [ "${directory##*/}" != main ]; then
    fail "additional Agent model stores are unsupported by the native MVP"
  fi
done
for file in "$state"/state/openclaw.sqlite* \
  "$state"/agents/*/agent/openclaw-agent.sqlite* "$state"/agents/*/agent/auth-profiles.json*; do
  [ ! -L "$file" ] || fail "unsupported auth store link"
done
scratch=$(mktemp -d) || fail "could not create scratch directory"
trap 'rm -rf "$scratch"' 0
output=$scratch/output
errors=$scratch/errors
export OPENCLAW_CONFIG_PATH="$scratch/openclaw.json"
printf '{"plugins":{"entries":{}}}\n' > "$OPENCLAW_CONFIG_PATH"

list_profiles() {
  openclaw models auth list --agent main --json > "$output" 2> "$errors" ||
    fail "could not list model auth profiles"
  jq -e '.profiles | type == "array" and all(.[];
    (.id | type) == "string" and (.id | test("^[a-zA-Z0-9][a-zA-Z0-9_.:@/+-]*$")))' \
    "$output" >/dev/null 2>&1 || fail "invalid model auth profile list"
}

list_profiles
profiles=$(jq -r '.profiles[].id' "$output") || fail "could not parse model auth profiles"
while IFS= read -r profile; do
  [ -n "$profile" ] || continue
  openclaw models auth logout "$profile" --agent main --yes > "$output" 2> "$errors" ||
    fail "could not remove model auth profile"
done <<EOF
$profiles
EOF

for directory in "$state"/agents/*; do
  for file in "$directory"/agent/auth-profiles.json "$directory"/agent/auth-profiles.json.migrated-*; do
    [ -e "$file" ] || continue
    [ -f "$file" ] || fail "invalid legacy auth profile"
    temp=$(mktemp "$file.tmp.XXXXXX") || fail "could not create auth profile replacement"
    if jq -e 'if type != "object" then error("invalid profiles")
      elif has("profiles") then
        if (.profiles | type) != "object" then error("invalid profiles")
        else .profiles = {} end
      else {} end' "$file" > "$temp" 2>/dev/null &&
      chmod 600 "$temp" && mv -f "$temp" "$file"; then
      :
    else
      rm -f "$temp"
      fail "could not clear legacy auth profiles"
    fi
  done
done

# An empty initial list already verified the store; only logout needs readback.
if [ -n "$profiles" ]; then
  list_profiles
  jq -e '.profiles | length == 0' "$output" >/dev/null 2>&1 ||
    fail "model auth profiles remain"
fi

# Repeat compaction on restart, including after an earlier partial failure.
if [ -n "$profiles" ] || [ -f "$state/state/openclaw.sqlite" ]; then
  openclaw doctor --state-sqlite compact > "$output" 2> "$errors" ||
    fail "could not compact shared state"
fi
if [ -f "$state/agents/main/agent/openclaw-agent.sqlite" ]; then
  openclaw doctor --session-sqlite compact --session-sqlite-agent main > "$output" 2> "$errors" ||
    fail "could not compact agent state"
fi
printf 'retire-direct-model-auth: model auth profiles retired\n' >&2
