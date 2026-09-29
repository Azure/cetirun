#!/bin/sh
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -e
if [ "${1:-}" = "--help" ]; then
  printf '%s\n' "Initialize a Hermes HOME using SOURCE_HOME, TARGET_HOME and CETIRUN_INIT_IMAGE."
  exit 0
fi
SOURCE_HOME=${SOURCE_HOME:-/home/vibe}
TARGET_HOME=${TARGET_HOME:-/home-data}
CETIRUN_CONFIG_DIR=${CETIRUN_CONFIG_DIR:-/etc/cetirun}
CETIRUN_SECRET_DIR=${CETIRUN_SECRET_DIR:-/etc/cetirun-secrets}
LEGACY_HOME_MARKER=.hermes/.initialized
PYTHON_BIN=${HERMES_PYTHON:-/usr/local/lib/hermes-agent/venv/bin/python}
if [ ! -x "$PYTHON_BIN" ]; then PYTHON_BIN=python3; fi
. "$CETIRUN_CONFIG_DIR/init-home-common.sh"

agent_check_requirements() {
  command -v "$PYTHON_BIN" >/dev/null 2>&1 &&
    "$PYTHON_BIN" -c 'import yaml' >/dev/null 2>&1 || {
      init_error "HermesPythonUnavailable"
      return 1
    }
  for file in hermes-config.yaml hermes-auth.sh hermes-apply-config.py; do
    [ -f "$CETIRUN_CONFIG_DIR/$file" ] || { init_error "HermesInputMissing: $file"; return 1; }
  done
  [ -f "$CETIRUN_SECRET_DIR/hermes.env" ] || { init_error "HermesEnvironmentMissing"; return 1; }
}

agent_prepare_home() {
  for directory in .hermes .hermes/lazy-packages .hermes/memories .hermes/skills .hermes/sessions .hermes/logs .hermes/cron .local/bin; do
    home_ensure_dir "$directory"
  done
  marker=$(home_path "$LEGACY_HOME_MARKER")
  if [ ! -f "$marker" ]; then
    soul=$(home_path .hermes/SOUL.md)
    if [ ! -e "$soul" ] && [ -f "$CETIRUN_CONFIG_DIR/hermes-soul.md" ]; then
      home_atomic_copy "$CETIRUN_CONFIG_DIR/hermes-soul.md" .hermes/SOUL.md
    fi
    seed_user_file
    memory=$(home_path .hermes/memories/MEMORY.md)
    if [ ! -e "$memory" ]; then home_write_marker .hermes/memories/MEMORY.md ""; fi
  fi
}

seed_user_file() (
  set -e
  dest=$(home_path .hermes/memories/USER.md)
  [ ! -e "$dest" ] && [ -f "$CETIRUN_CONFIG_DIR/hermes-user.md" ] || exit 0
  temp=$(mktemp "${dest}.tmp.XXXXXX")
  trap 'rm -f "$temp"' 0
  cp "$CETIRUN_CONFIG_DIR/hermes-user.md" "$temp"
  if [ -s "$CETIRUN_CONFIG_DIR/hermes-team.md" ]; then
    printf '§ See: TEAM.md\n' >> "$temp"
  fi
  chmod 600 "$temp"
  mv -f "$temp" "$dest"
)

agent_apply_config() {
  home_path .hermes/config.yaml >/dev/null
  home_path .hermes/.env >/dev/null
  "$PYTHON_BIN" "$CETIRUN_CONFIG_DIR/hermes-apply-config.py" \
    --home "$TARGET_HOME" \
    --config "$CETIRUN_CONFIG_DIR/hermes-config.yaml" \
    --env "$CETIRUN_SECRET_DIR/hermes.env"
}

agent_prepare_integrations() {
  home_atomic_copy "$CETIRUN_CONFIG_DIR/hermes-auth.sh" .local/bin/copilot-auth 755
  team=$(home_path .hermes/memories/TEAM.md)
  if [ -s "$CETIRUN_CONFIG_DIR/hermes-team.md" ]; then
    home_atomic_copy "$CETIRUN_CONFIG_DIR/hermes-team.md" .hermes/memories/TEAM.md
  elif [ -f "$team" ]; then
    rm -f "$team"
  fi
}

agent_verify() {
  "$PYTHON_BIN" "$CETIRUN_CONFIG_DIR/hermes-apply-config.py" --home "$TARGET_HOME" --check
  helper=$(home_path .local/bin/copilot-auth)
  [ -x "$helper" ] || { init_error "HermesAuthHelperUnavailable"; return 1; }
  home_write_marker "$LEGACY_HOME_MARKER" initialized-v1
}

run_agent_init
