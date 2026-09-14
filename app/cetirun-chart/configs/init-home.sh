#!/bin/sh
set -e

SENTINEL="/home-data/.openclaw/.initialized"

# Deny-by-default: a nil UUID that will never match a real AAD object ID.
# Used as the fallback for channels.msteams.allowFrom when the field is absent.
MSTEAMS_ALLOW_FROM_DEFAULT="00000000-0000-0000-0000-000000000000"

# Migrate legacy GitHub Copilot credentials to the current format.
migrate_copilot_credentials() {
  OLD_TOKEN_FILE="/home-data/.litellm/github_copilot/access-token"
  NEW_CRED_DIR="/home-data/.openclaw/credentials"
  NEW_TOKEN_FILE="${NEW_CRED_DIR}/github-copilot.token.json"

  # Skip if already migrated or no old credentials exist
  if [ -f "$NEW_TOKEN_FILE" ] || [ ! -f "$OLD_TOKEN_FILE" ] || [ ! -s "$OLD_TOKEN_FILE" ]; then
    return
  fi

  echo "[init-home] Migrating GitHub Copilot credentials..."
  ACCESS_TOKEN=$(cat "$OLD_TOKEN_FILE")

  API_RESP=$(curl -sf \
    -H "accept: application/json" \
    -H "authorization: token ${ACCESS_TOKEN}" \
    -H "editor-version: vscode/1.85.1" \
    -H "editor-plugin-version: copilot/1.155.0" \
    -H "user-agent: GithubCopilot/1.155.0" \
    "https://api.github.com/copilot_internal/v2/token" 2>/dev/null) || true

  if [ -z "$API_RESP" ] || ! echo "$API_RESP" | grep -q '"token"'; then
    echo "[init-home] Warning: could not fetch Copilot token (access token may be expired)." >&2
    echo "[init-home] Run 'copilot-auth' to re-authenticate." >&2
    return
  fi

  TOKEN_VALUE=$(echo "$API_RESP" | sed -n 's/.*"token" *: *"\([^"]*\)".*/\1/p')
  EXPIRES_AT=$(echo "$API_RESP" | sed -n 's/.*"expires_at" *: *\([0-9]*\).*/\1/p')
  NOW_MS=$(date +%s)000

  if [ -z "$TOKEN_VALUE" ]; then
    echo "[init-home] Warning: failed to parse Copilot token response." >&2
    return
  fi

  EXPIRES_AT_MS="${EXPIRES_AT}000"

  mkdir -p "$NEW_CRED_DIR"
  cat > "$NEW_TOKEN_FILE" << CRED_EOF
{
  "token": "${TOKEN_VALUE}",
  "expiresAt": ${EXPIRES_AT_MS},
  "updatedAt": ${NOW_MS}
}
CRED_EOF
  chmod 600 "$NEW_TOKEN_FILE"

  # Remove legacy access token to avoid leaving valid credentials on the PVC
  rm -f "$OLD_TOKEN_FILE"

  echo "[init-home] Copilot credentials migrated successfully."
}


# Propagate GitHub Copilot auth profile to all agent directories.
# Finds a source auth-profiles.json with the github-copilot:github profile
# and copies it to any agent directory missing that profile.
migrate_agent_auth_profiles() {
  AGENTS_DIR="/home-data/.openclaw/agents"

  # Find a source profile from existing agent directories
  SOURCE_PROFILE=""
  if [ -d "$AGENTS_DIR" ]; then
    for agent_dir in "$AGENTS_DIR"/*/agent; do
      [ -d "$agent_dir" ] || continue
      AUTH_FILE="$agent_dir/auth-profiles.json"
      if [ -f "$AUTH_FILE" ] && jq -e '.profiles["github-copilot:github"].token' "$AUTH_FILE" >/dev/null 2>&1; then
        SOURCE_PROFILE=$(jq '.profiles["github-copilot:github"]' "$AUTH_FILE")
        echo "[init-home] Found GitHub Copilot auth in $(basename "$(dirname "$agent_dir")")"
        break
      fi
    done
  fi

  # Fallback: check the legacy OAuth token (before migrate_copilot_credentials removes it)
  if [ -z "$SOURCE_PROFILE" ]; then
    LEGACY_TOKEN_FILE="/home-data/.litellm/github_copilot/access-token"
    if [ -f "$LEGACY_TOKEN_FILE" ] && [ -s "$LEGACY_TOKEN_FILE" ]; then
      OAUTH_TOKEN=$(cat "$LEGACY_TOKEN_FILE")
      SOURCE_PROFILE=$(jq -n --arg token "$OAUTH_TOKEN" '{
        "type": "token",
        "provider": "github-copilot",
        "token": $token
      }')
      echo "[init-home] Found GitHub Copilot OAuth token in legacy credentials"
    fi
  fi

  if [ -z "$SOURCE_PROFILE" ]; then
    echo "[init-home] No GitHub Copilot auth profile found in any agent. Run 'copilot-auth' to authenticate."
    return
  fi

  # Ensure the main agent directory exists
  MAIN_AGENT_DIR="$AGENTS_DIR/main/agent"
  if [ ! -d "$MAIN_AGENT_DIR" ]; then
    mkdir -p "$MAIN_AGENT_DIR"
  fi

  # Propagate to all agent directories
  for agent_dir in "$AGENTS_DIR"/*/agent; do
    [ -d "$agent_dir" ] || continue
    AUTH_FILE="$agent_dir/auth-profiles.json"
    AGENT_NAME=$(basename "$(dirname "$agent_dir")")

    # Skip if already has the profile with a token
    if [ -f "$AUTH_FILE" ] && jq -e '.profiles["github-copilot:github"].token' "$AUTH_FILE" >/dev/null 2>&1; then
      continue
    fi

    echo "[init-home] Adding GitHub Copilot auth to agent: $AGENT_NAME"
    if [ -f "$AUTH_FILE" ]; then
      # Merge into existing file
      UPDATED=$(jq --argjson profile "$SOURCE_PROFILE" '
        .profiles["github-copilot:github"] = $profile
      ' "$AUTH_FILE" 2>/dev/null) || true
    else
      # Create new file
      UPDATED=$(jq -n --argjson profile "$SOURCE_PROFILE" '{
        "version": 1,
        "profiles": { "github-copilot:github": $profile }
      }') || true
    fi

    if [ -n "$UPDATED" ]; then
      echo "$UPDATED" > "$AUTH_FILE"
    fi
  done
}

# Setup Azure DevOps git credential helper (idempotent, runs on every start)
setup_azdevops_git_credential() {
  # Skip setup when disabled; intentionally does NOT clean up existing config
  # on PVCs where this was previously enabled (no-op is safer than teardown).
  if [ "$AZDEVOPS_GIT_CREDENTIAL_ENABLED" != "true" ]; then
    return
  fi

  # Create credential helper script with restrictive permissions
  old_umask=$(umask)
  umask 077
  cat > /home-data/.git-credential-azdevops.sh << 'SCRIPT'
#!/bin/sh
# Git credential helper that fetches Azure DevOps tokens via Azure CLI
echo "username=azuredevops"
# 499b84ac-1321-427f-aa17-267ca6975798 is the well-known resource ID for Azure DevOps
echo "password=$(az account get-access-token --resource 499b84ac-1321-427f-aa17-267ca6975798 --query accessToken -o tsv)"
SCRIPT
  umask "$old_umask"
  chmod 700 /home-data/.git-credential-azdevops.sh

  # Configure git credential helpers for Azure DevOps domains
  # Note: helper path uses /home/vibe/ (runtime mount), not /home-data/ (init mount)
  GITCONFIG="/home-data/.gitconfig"
  # These git config operations are best-effort: failures should not abort init.
  set +e
  for domain in msazure.visualstudio.com supportability.visualstudio.com dev.azure.com; do
    if ! git config --file "$GITCONFIG" "credential.https://$domain.useHttpPath" true; then
      echo "[init-home] Warning: failed to configure git credential useHttpPath for $domain" >&2
    fi
    if ! git config --file "$GITCONFIG" "credential.https://$domain.provider" generic; then
      echo "[init-home] Warning: failed to configure git credential provider for $domain" >&2
    fi
    if ! git config --file "$GITCONFIG" "credential.https://$domain.helper" "/home/vibe/.git-credential-azdevops.sh"; then
      echo "[init-home] Warning: failed to configure git credential helper for $domain" >&2
    fi
  done
  set -e
}

# Fast restart path: sync new built-in skills from image, then exit.
# NOTE: Personality files (AGENTS.md, SOUL.md, etc.) are intentionally NOT
# synced on restart/upgrade — engineers may have customized them and we must
# preserve those edits. Only built-in skills use copy-if-missing sync.
if [ -f "$SENTINEL" ]; then
  echo "[init-home] Already initialized, running fast-restart sync..."
  # Deep-merge openclaw.json from ConfigMap into PVC config. The ConfigMap
  # (operator/chart) is authoritative for keys it sets; user-added keys
  # (e.g. channels.msteams.allowFrom) survive restarts.
    mkdir -p /home-data/.openclaw/logs
  if [ -f /etc/cetirun/openclaw.json ]; then
    # Deep-merge ConfigMap config into the existing PVC config. The ConfigMap
    # (operator/chart) is authoritative for keys it sets, but user-added keys
    # (e.g. channels.msteams.allowFrom set via the gateway UI) survive restarts
    # as long as the ConfigMap doesn't also set them.
    # jq `*` does recursive object merge: ConfigMap values win for shared keys,
    # PVC-only keys are preserved.
    echo "[init-home] Merging gateway config from ConfigMap..."
    PVC_CONFIG="/home-data/.openclaw/openclaw.json"
    TMP_CONFIG="${PVC_CONFIG}.tmp"
    if [ -f "$PVC_CONFIG" ]; then
      if jq --arg dflt "$MSTEAMS_ALLOW_FROM_DEFAULT" -s '
        .[0] * .[1]
        | if .channels.msteams.enabled then
            .channels.msteams.allowFrom //= [$dflt]
          else . end
      ' "$PVC_CONFIG" /etc/cetirun/openclaw.json > "$TMP_CONFIG"; then
        mv "$TMP_CONFIG" "$PVC_CONFIG"
      else
        echo "[init-home] WARNING: jq merge failed; overwriting with ConfigMap config."
        rm -f "$TMP_CONFIG"
        cp /etc/cetirun/openclaw.json "$PVC_CONFIG"
        # Ensure allowFrom exists — gateway crashes without it.
        # Default to a nil UUID (deny-all) so unconfigured instances reject messages.
        if jq -e '.channels.msteams.enabled' "$PVC_CONFIG" >/dev/null 2>&1; then
          jq --arg dflt "$MSTEAMS_ALLOW_FROM_DEFAULT" '.channels.msteams.allowFrom //= [$dflt]' "$PVC_CONFIG" > "$TMP_CONFIG" \
            && mv "$TMP_CONFIG" "$PVC_CONFIG" \
            || rm -f "$TMP_CONFIG"
        fi
      fi
    else
      cp /etc/cetirun/openclaw.json "$PVC_CONFIG"
      # Ensure allowFrom exists — gateway crashes without it.
      # Default to a nil UUID (deny-all) so unconfigured instances reject messages.
      if jq -e '.channels.msteams.enabled' "$PVC_CONFIG" >/dev/null 2>&1; then
        jq --arg dflt "$MSTEAMS_ALLOW_FROM_DEFAULT" '.channels.msteams.allowFrom //= [$dflt]' "$PVC_CONFIG" > "$TMP_CONFIG" \
          && mv "$TMP_CONFIG" "$PVC_CONFIG" \
          || rm -f "$TMP_CONFIG"
      fi
    fi
    chmod 600 "$PVC_CONFIG"
    # Clear stale config-health.json so the config observer does not flag
    # the new ConfigMap config as anomalous vs. a previous "last known good".
    rm -f /home-data/.openclaw/logs/config-health.json
  fi
  # Plugins are npm-installed at build time and live at /usr/local/share/openclaw-npm,
  # symlinked from ~/.openclaw/npm. On fast-restart, ensure the PVC has the symlink
  # (or replace a stale copied directory with the symlink).
  if [ -L /home/vibe/.openclaw/npm ]; then
    LINK_TARGET=$(readlink /home/vibe/.openclaw/npm)
    if [ -d /home-data/.openclaw/npm ] && [ ! -L /home-data/.openclaw/npm ]; then
      echo "[init-home] Replacing stale plugin directory with symlink..."
      rm -rf /home-data/.openclaw/npm
    fi
    if [ ! -L /home-data/.openclaw/npm ]; then
      ln -s "$LINK_TARGET" /home-data/.openclaw/npm
    fi
  elif [ -d /home/vibe/.openclaw/npm ]; then
    echo "[init-home] Syncing external plugins from image..."
    rm -rf /home-data/.openclaw/npm.new
    if cp -a /home/vibe/.openclaw/npm /home-data/.openclaw/npm.new; then
      rm -rf /home-data/.openclaw/npm
      mv /home-data/.openclaw/npm.new /home-data/.openclaw/npm
    else
      echo "[init-home] WARNING: plugin sync copy failed; keeping existing plugins." >&2
      rm -rf /home-data/.openclaw/npm.new
    fi
  else
    if [ -d /home-data/.openclaw/npm ] || [ -L /home-data/.openclaw/npm ]; then
      echo "[init-home] Image has no external plugins; removing stale PVC plugins."
      rm -rf /home-data/.openclaw/npm
    fi
  fi

  echo "[init-home] Checking for new built-in skills..."
  if [ -d /home/vibe/.openclaw/skills ]; then
    mkdir -p /home-data/.openclaw/skills
    for skill in /home/vibe/.openclaw/skills/*/; do
      skill_name=$(basename "$skill")
      if [ ! -d "/home-data/.openclaw/skills/$skill_name" ]; then
        echo "[init-home] Installing new skill: $skill_name"
        cp -r "$skill" "/home-data/.openclaw/skills/$skill_name"
      fi
    done
  fi
  # Install copilot-auth helper so it's on PATH
  mkdir -p /home-data/.local/bin
  cp /etc/cetirun/openclaw-auth.sh /home-data/.local/bin/copilot-auth
  chmod +x /home-data/.local/bin/copilot-auth
  setup_azdevops_git_credential
  migrate_agent_auth_profiles
  migrate_copilot_credentials
  echo "[init-home] Done."
  exit 0
fi

echo "[init-home] First run detected, initializing home directory..."

# First run: seed /home/vibe skeleton to PVC
SKELETON_SEEDED=0
if [ -z "$(ls -A /home-data 2>/dev/null)" ] || [ ! -d /home-data/.openclaw ]; then
  echo "[init-home] Copying home skeleton to PVC..."
  cp -a /home/vibe/. /home-data/
  SKELETON_SEEDED=1
fi

# Seed configs from ConfigMap (skip if already present)
echo "[init-home] Seeding configs from ConfigMap..."
mkdir -p /home-data/.openclaw /home-data/.acpx
[ -f /etc/cetirun/openclaw.json ] && [ ! -f /home-data/.openclaw/openclaw.json ] && \
  cp /etc/cetirun/openclaw.json /home-data/.openclaw/openclaw.json && \
  chmod 600 /home-data/.openclaw/openclaw.json
# Ensure allowFrom exists — gateway crashes without it.
# Default to a nil UUID (deny-all) so unconfigured instances reject messages.
if [ -f /home-data/.openclaw/openclaw.json ] && jq -e '.channels.msteams.enabled' /home-data/.openclaw/openclaw.json >/dev/null 2>&1; then
  jq --arg dflt "$MSTEAMS_ALLOW_FROM_DEFAULT" '.channels.msteams.allowFrom //= [$dflt]' /home-data/.openclaw/openclaw.json > /home-data/.openclaw/openclaw.json.tmp \
    && mv /home-data/.openclaw/openclaw.json.tmp /home-data/.openclaw/openclaw.json \
    && chmod 600 /home-data/.openclaw/openclaw.json \
    || rm -f /home-data/.openclaw/openclaw.json.tmp
fi
[ -f /etc/cetirun/acpx-config.json ] && [ ! -f /home-data/.acpx/config.json ] && \
  cp /etc/cetirun/acpx-config.json /home-data/.acpx/config.json

# Setup git credential helper
setup_azdevops_git_credential
# USER.md: ConfigMap version (with name/alias from Helm values) overrides
# the blank default from the image skeleton. Only copy when we actually
# seeded the skeleton this run (SKELETON_SEEDED), or when no USER.md exists
# (e.g. fresh PVC). Avoids clobbering a customized USER.md on restored PVCs.
if [ -f /etc/cetirun/user-md ]; then
  mkdir -p /home-data/.openclaw/workspace
  if [ "${SKELETON_SEEDED:-0}" = "1" ] || [ ! -f /home-data/.openclaw/workspace/USER.md ]; then
    cp /etc/cetirun/user-md /home-data/.openclaw/workspace/USER.md
  fi
fi

# Install copilot-auth helper so it's on PATH via ~/.local/bin
mkdir -p /home-data/.local/bin
cp /etc/cetirun/openclaw-auth.sh /home-data/.local/bin/copilot-auth
chmod +x /home-data/.local/bin/copilot-auth

migrate_agent_auth_profiles
migrate_copilot_credentials

# Mark as initialized — subsequent restarts skip everything
touch "$SENTINEL"
echo "[init-home] Initialization complete."
