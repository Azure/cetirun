#!/bin/sh
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -e

HERMES_HOME_DATA="/home-data/.hermes"
SENTINEL="${HERMES_HOME_DATA}/.initialized"
OPENCLAW_HOME_DATA="/home-data/.openclaw"
MIGRATION_MARKER="/home-data/.cetirun.migrated"
MIGRATION_LOG="${HERMES_HOME_DATA}/logs/openclaw-migration.log"
CONFIG_SRC="/etc/cetirun/hermes-config.yaml"
ENV_SRC="/etc/cetirun-secrets/hermes.env"
CONFIG_DST="${HERMES_HOME_DATA}/config.yaml"
ENV_DST="${HERMES_HOME_DATA}/.env"
PYTHON_BIN="${HERMES_PYTHON:-/usr/local/lib/hermes-agent/bin/python}"
if [ ! -x "$PYTHON_BIN" ]; then
  PYTHON_BIN="python3"
fi

merge_config() {
  mkdir -p "$HERMES_HOME_DATA"
  if [ ! -f "$CONFIG_SRC" ]; then
    return
  fi
  if [ -f "$CONFIG_DST" ]; then
    "$PYTHON_BIN" - "$CONFIG_DST" "$CONFIG_SRC" <<'PY'
import copy
import sys
import yaml

dst, src = sys.argv[1], sys.argv[2]

def load(path):
    try:
        with open(path, "r", encoding="utf-8") as f:
            return yaml.safe_load(f) or {}
    except FileNotFoundError:
        return {}

def deep_merge(base, override):
    if isinstance(base, dict) and isinstance(override, dict):
        merged = copy.deepcopy(base)
        for key, value in override.items():
            merged[key] = deep_merge(merged.get(key), value)
        return merged
    return copy.deepcopy(override)

merged = deep_merge(load(dst), load(src))
with open(dst, "w", encoding="utf-8") as f:
    yaml.safe_dump(merged, f, sort_keys=False)
PY
  else
    cp "$CONFIG_SRC" "$CONFIG_DST"
  fi
  chmod 600 "$CONFIG_DST"
}

sync_env() {
  if [ ! -f "$ENV_SRC" ]; then
    return
  fi

  mkdir -p "$HERMES_HOME_DATA"
  if [ ! -f "$ENV_DST" ]; then
    cp "$ENV_SRC" "$ENV_DST"
    chmod 600 "$ENV_DST"
    return
  fi

  old_umask=$(umask)
  umask 077
  if ! TMP_ENV=$(mktemp "${ENV_DST}.XXXXXX"); then
    umask "$old_umask"
    echo "[hermes-init-home] WARNING: failed to create temp .env; preserving existing .env." >&2
    return
  fi
  umask "$old_umask"

  if "$PYTHON_BIN" - "$ENV_DST" "$ENV_SRC" "$TMP_ENV" <<'PY'
import re
import sys
from pathlib import Path

dst, src, tmp = map(Path, sys.argv[1:4])
env_line = re.compile(r"^([A-Za-z_][A-Za-z0-9_]*)=(.*)$")


def read_env(path):
    values = {}
    if not path.exists():
        return values
    for raw_line in path.read_text(encoding="utf-8").splitlines():
        match = env_line.match(raw_line)
        if not match:
            continue
        key, value = match.groups()
        values[key] = value
    return values


merged = read_env(dst)
for key, value in read_env(src).items():
    if value or key not in merged:
        merged[key] = value

content = "".join(f"{key}={value}\n" for key, value in merged.items())
tmp.write_text(content, encoding="utf-8")
PY
  then
    mv "$TMP_ENV" "$ENV_DST"
  else
    echo "[hermes-init-home] WARNING: env merge failed; preserving existing .env." >&2
    rm -f "$TMP_ENV"
  fi
  chmod 600 "$ENV_DST"
}

install_auth_helper() {
  mkdir -p /home-data/.local/bin
  cp /etc/cetirun/hermes-auth.sh /home-data/.local/bin/copilot-auth
  chmod +x /home-data/.local/bin/copilot-auth
}

migrate_openclaw_copilot_auth() {
  if [ ! -d "$OPENCLAW_HOME_DATA" ]; then
    return 0
  fi

  if HERMES_HOME="$HERMES_HOME_DATA" "$PYTHON_BIN" - "$ENV_DST" "$OPENCLAW_HOME_DATA" <<'PY'
import json
import os
import re
import sys
from pathlib import Path

env_path = Path(sys.argv[1])
openclaw_home = Path(sys.argv[2])
legacy_token_path = openclaw_home.parent / ".litellm" / "github_copilot" / "access-token"
valid_prefixes = ("gho_", "ghu_", "github_pat_")
env_line = re.compile(r"^([A-Za-z_][A-Za-z0-9_]*)=(.*)$")


def read_env(path):
    values = {}
    if not path.exists():
        return values
    for raw_line in path.read_text(encoding="utf-8").splitlines():
        match = env_line.match(raw_line)
        if match:
            values[match.group(1)] = match.group(2)
    return values


def save_env_value(path, key, value):
    lines = []
    replaced = False
    if path.exists():
        for raw_line in path.read_text(encoding="utf-8").splitlines():
            match = env_line.match(raw_line)
            if match and match.group(1) == key:
                lines.append(f"{key}={value}")
                replaced = True
            else:
                lines.append(raw_line)
    if not replaced:
        lines.append(f"{key}={value}")

    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(f".{path.name}.tmp.{os.getpid()}")
    old_umask = os.umask(0o077)
    try:
        tmp.write_text("\n".join(lines) + "\n", encoding="utf-8")
        os.replace(tmp, path)
        path.chmod(0o600)
    finally:
        os.umask(old_umask)
        try:
            tmp.unlink()
        except FileNotFoundError:
            pass


def valid_token(token):
    return isinstance(token, str) and token.startswith(valid_prefixes)


def token_from_profile(path):
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return None
    profile = data.get("profiles", {}).get("github-copilot:github", {})
    token = profile.get("token")
    return token if valid_token(token) else None


if read_env(env_path).get("COPILOT_GITHUB_TOKEN"):
    print("[hermes-init-home] GitHub Copilot token already present in Hermes env; skipping OpenClaw auth import.")
    raise SystemExit(0)

candidates = []
main_profile = openclaw_home / "agents" / "main" / "agent" / "auth-profiles.json"
if main_profile.exists():
    candidates.append(main_profile)
agents_dir = openclaw_home / "agents"
if agents_dir.exists():
    for profile_path in sorted(agents_dir.glob("*/agent/auth-profiles.json")):
        if profile_path not in candidates:
            candidates.append(profile_path)

for profile_path in candidates:
    token = token_from_profile(profile_path)
    if token:
        save_env_value(env_path, "COPILOT_GITHUB_TOKEN", token)
        print(f"[hermes-init-home] Imported GitHub Copilot token from {profile_path}.")
        raise SystemExit(0)

try:
    legacy_token = legacy_token_path.read_text(encoding="utf-8").strip()
except OSError:
    legacy_token = ""
if valid_token(legacy_token):
    save_env_value(env_path, "COPILOT_GITHUB_TOKEN", legacy_token)
    print(f"[hermes-init-home] Imported GitHub Copilot token from {legacy_token_path}.")
else:
    print("[hermes-init-home] No migratable GitHub Copilot token found in OpenClaw credentials.")
PY
  then
    return 0
  fi

  echo "[hermes-init-home] WARNING: OpenClaw Copilot auth import failed; continuing." >&2
  return 0
}

write_migration_marker() {
  migration_status="$1"
  migration_exit_code="${2:-0}"
  migration_message="${3:-}"

  old_umask=$(umask)
  umask 077
  if ! TMP_MARKER=$(mktemp "${MIGRATION_MARKER}.XXXXXX"); then
    umask "$old_umask"
    echo "[hermes-init-home] ERROR: failed to create migration marker temp file." >&2
    return 1
  fi
  umask "$old_umask"

  {
    echo "status=${migration_status}"
    echo "exit_code=${migration_exit_code}"
    echo "timestamp=$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    echo "source=${OPENCLAW_HOME_DATA}"
    echo "target=${HERMES_HOME_DATA}"
    echo "command=hermes claw migrate --source ${OPENCLAW_HOME_DATA} --preset full --migrate-secrets --overwrite --skill-conflict rename --yes"
    echo "log=${MIGRATION_LOG}"
    if [ -n "$migration_message" ]; then
      echo "message=${migration_message}"
    fi
    echo
    echo "result:"
    if [ -f "$MIGRATION_LOG" ]; then
      sed 's/^/  /' "$MIGRATION_LOG"
    fi
  } > "$TMP_MARKER"
  mv "$TMP_MARKER" "$MIGRATION_MARKER"
  chmod 600 "$MIGRATION_MARKER"
}

migrate_openclaw_home() {
  if [ ! -d "$OPENCLAW_HOME_DATA" ]; then
    return
  fi
  if [ -f "$MIGRATION_MARKER" ]; then
    mkdir -p "$HERMES_HOME_DATA/logs"
    migrate_openclaw_copilot_auth >> "$MIGRATION_LOG" 2>&1
    existing_status=$(sed -n 's/^status=//p' "$MIGRATION_MARKER" | head -n 1)
    existing_exit_code=$(sed -n 's/^exit_code=//p' "$MIGRATION_MARKER" | head -n 1)
    write_migration_marker "${existing_status:-skipped}" "${existing_exit_code:-0}" "OpenClaw migration marker already existed; supplemental migration rechecked" || \
      echo "[hermes-init-home] WARNING: failed to refresh migration marker." >&2
    return
  fi

  if ! command -v hermes >/dev/null 2>&1; then
    message="hermes command not found; cannot migrate OpenClaw data"
    echo "[hermes-init-home] ERROR: ${message}." >&2
    write_migration_marker "failed" "127" "$message" || \
      echo "[hermes-init-home] WARNING: failed to write migration failure marker." >&2
    return 0
  fi

  echo "[hermes-init-home] Migrating OpenClaw data into Hermes..."
  mkdir -p "$HERMES_HOME_DATA/logs"
  if HOME=/home-data HERMES_HOME="$HERMES_HOME_DATA" \
    hermes claw migrate \
      --source "$OPENCLAW_HOME_DATA" \
      --preset full \
      --migrate-secrets \
      --overwrite \
      --skill-conflict rename \
      --yes > "$MIGRATION_LOG" 2>&1; then
    merge_config
    sync_env
    migrate_openclaw_copilot_auth >> "$MIGRATION_LOG" 2>&1
    write_migration_marker "success" "0" "" || \
      echo "[hermes-init-home] WARNING: failed to write migration success marker." >&2
    echo "[hermes-init-home] OpenClaw migration complete."
  else
    rc=$?
    migrate_openclaw_copilot_auth >> "$MIGRATION_LOG" 2>&1
    message="OpenClaw migration failed; see ${MIGRATION_LOG}"
    echo "[hermes-init-home] ERROR: ${message}." >&2
    write_migration_marker "failed" "$rc" "$message" || \
      echo "[hermes-init-home] WARNING: failed to write migration failure marker." >&2
    return 0
  fi
}

# Sync TEAM.md from ConfigMap on every start. Overwrites the PVC copy so that
# Team edits propagate to running instances. When the ConfigMap source
# is missing or empty (no team / cleared team), remove the PVC TEAM.md to avoid
# stale team context.
sync_team_md() {
  mkdir -p "$HERMES_HOME_DATA/memories"
  TEAM_SRC="/etc/cetirun/hermes-team.md"
  TEAM_DST="$HERMES_HOME_DATA/memories/TEAM.md"
  if [ -s "$TEAM_SRC" ]; then
    cp "$TEAM_SRC" "$TEAM_DST"
    chmod 600 "$TEAM_DST"
  else
    rm -f "$TEAM_DST"
  fi
}

if [ -f "$SENTINEL" ]; then
  echo "[hermes-init-home] Already initialized, syncing config and secrets..."
  # Ensure skeleton exists (may be missing if first init used an older script)
  if [ ! -f /home-data/.zshrc ]; then
    echo "[hermes-init-home] Copying missing home skeleton to PVC..."
    cp -rn /home/vibe/. /home-data/
  fi
  merge_config
  sync_env
  migrate_openclaw_home
  install_auth_helper
  sync_team_md
  echo "[hermes-init-home] Done."
  exit 0
fi

echo "[hermes-init-home] First run detected, initializing Hermes home..."

# Copy home skeleton to PVC (oh-my-zsh, .zshrc, .local/bin, etc.)
if [ ! -f /home-data/.zshrc ]; then
  echo "[hermes-init-home] Copying home skeleton to PVC..."
  cp -r /home/vibe/. /home-data/
fi

mkdir -p \
  "$HERMES_HOME_DATA" \
  "$HERMES_HOME_DATA/memories" \
  "$HERMES_HOME_DATA/skills" \
  "$HERMES_HOME_DATA/sessions" \
  "$HERMES_HOME_DATA/logs" \
  "$HERMES_HOME_DATA/cron"

merge_config
sync_env
migrate_openclaw_home

if [ -f /etc/cetirun/hermes-soul.md ] && [ ! -f "$HERMES_HOME_DATA/SOUL.md" ]; then
  cp /etc/cetirun/hermes-soul.md "$HERMES_HOME_DATA/SOUL.md"
fi
if [ -f /etc/cetirun/hermes-user.md ] && [ ! -f "$HERMES_HOME_DATA/memories/USER.md" ]; then
  cp /etc/cetirun/hermes-user.md "$HERMES_HOME_DATA/memories/USER.md"
  # Hint that team context is in TEAM.md if it's populated.
  if [ -s /etc/cetirun/hermes-team.md ]; then
    printf '§ See: TEAM.md\n' >> "$HERMES_HOME_DATA/memories/USER.md"
  fi
fi
touch "$HERMES_HOME_DATA/memories/MEMORY.md"

install_auth_helper
sync_team_md
touch "$SENTINEL"
echo "[hermes-init-home] Initialization complete."
