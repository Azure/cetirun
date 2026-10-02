#!/bin/sh
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -e
if [ "${1:-}" = "--help" ]; then
  printf '%s\n' "Initialize an OpenClaw HOME using SOURCE_HOME, TARGET_HOME and CETIRUN_INIT_IMAGE."
  exit 0
fi
SOURCE_HOME=${SOURCE_HOME:-/home/vibe}
TARGET_HOME=${TARGET_HOME:-/home-data}
RUNTIME_HOME=${RUNTIME_HOME:-/home/vibe}
CETIRUN_CONFIG_DIR=${CETIRUN_CONFIG_DIR:-/etc/cetirun}
CETIRUN_BUILTIN_SKILL_SYNC=${CETIRUN_BUILTIN_SKILL_SYNC:-/usr/local/share/cetirun/builtin-skill-sync.sh}
LEGACY_HOME_MARKER=.openclaw/.initialized
. "$CETIRUN_CONFIG_DIR/init-home-common.sh"

agent_check_requirements() {
  for tool in jq ln readlink; do
    command -v "$tool" >/dev/null 2>&1 || { init_error "InitToolsMissing: $tool"; return 1; }
  done
  [ -f "$CETIRUN_CONFIG_DIR/openclaw.json" ] || {
    init_error "OpenClawInputMissing"
    return 1
  }
  [ -f "$CETIRUN_BUILTIN_SKILL_SYNC" ] || {
    init_error "OpenClawInputMissing: builtin-skill-sync.sh"
    return 1
  }
}

agent_prepare_home() {
  for directory in .openclaw .openclaw/logs .openclaw/workspace .acpx .local/bin; do
    home_ensure_dir "$directory"
  done
  native_marker=$(home_path "$LEGACY_HOME_MARKER")
  user_file=$(home_path .openclaw/workspace/USER.md)
  # Resume first native initialization even when HOME has already been copied.
  if [ -f "$CETIRUN_CONFIG_DIR/user-md" ] && [ ! -f "$native_marker" ]; then
    if [ ! -e "$user_file" ] ||
      { [ -f "$SOURCE_HOME/.openclaw/workspace/USER.md" ] &&
        cmp -s "$user_file" "$SOURCE_HOME/.openclaw/workspace/USER.md"; }; then
      home_atomic_copy "$CETIRUN_CONFIG_DIR/user-md" .openclaw/workspace/USER.md 600
    fi
  fi
}

agent_apply_config() (
  set -e
  dest=$(home_path .openclaw/openclaw.json)
  source=$CETIRUN_CONFIG_DIR/openclaw.json
  jq -e 'type == "object"' "$source" >/dev/null 2>&1 ||
    { init_error "OpenClawConfigInvalid"; exit 1; }
  if [ -e "$dest" ]; then
    [ -f "$dest" ] && jq -e 'type == "object"' "$dest" >/dev/null 2>&1 ||
      { init_error "OpenClawConfigInvalid: preserving existing configuration"; exit 1; }
  fi
  temp=$(mktemp "${dest}.tmp.XXXXXX")
  trap 'rm -f "$temp"' 0
  # CetiRun owns the model list of an Agent on the platform AI Gateway, which
  # the chart marks with CETIRUN_AI_GATEWAY, so that list replaces the saved
  # one instead of accumulating models the Gateway no longer offers, and the
  # retired in-agent Copilot sign-in profile is dropped. Any other
  # configuration, an openai provider with its own endpoint included, keeps
  # its saved models and their settings.
  if [ "${CETIRUN_AI_GATEWAY:-false}" = true ]; then
    gateway=true
  else
    gateway=false
  fi
  filter='
    .[-1] as $source
    | reduce .[] as $item ({}; . * $item)
    | if $gateway then
        .agents.defaults.models = ($source.agents.defaults.models // {})
        | if (.auth.profiles | type) == "object" then .auth.profiles |= del(.["github-copilot:github"]) else . end
      else . end
    | if .channels.msteams.enabled then
        .channels.msteams.allowFrom //= ["00000000-0000-0000-0000-000000000000"]
      else . end'
  if [ -f "$dest" ]; then
    jq -s --argjson gateway "$gateway" "$filter" "$dest" "$source" > "$temp" 2>/dev/null ||
      { init_error "OpenClawConfigApplyFailed"; exit 1; }
  else
    jq -s --argjson gateway "$gateway" "$filter" "$source" > "$temp" 2>/dev/null ||
      { init_error "OpenClawConfigApplyFailed"; exit 1; }
  fi
  chmod 600 "$temp"
  mv -f "$temp" "$dest"
  health_record=$(home_path .openclaw/logs/config-health.json)
  if [ -f "$health_record" ]; then rm -f "$health_record"; fi
)

prepare_plugin_link() {
  source=$SOURCE_HOME/.openclaw/npm
  parent=$(home_path .openclaw)
  dest=$parent/npm
  if [ -L "$source" ]; then
    link=$(readlink "$source")
    if [ -L "$dest" ]; then
      if [ "$(readlink "$dest")" != "$link" ]; then
        init_error "OpenClawPluginLinkChanged: explicit maintenance required"
        return 1
      fi
    elif [ -e "$dest" ]; then
      printf '[agent-init] preserving existing OpenClaw plugin directory\n' >&2
    else
      ln -s "$link" "$dest"
    fi
  fi
}

install_image_skills() (
  set -e
  source=$SOURCE_HOME/.openclaw/skills
  [ -d "$source" ] && [ ! -L "$source" ] || exit 0
  home_ensure_dir .openclaw/skills
  for entry in "$source"/*; do
    [ -d "$entry" ] && [ ! -L "$entry" ] || continue
    dest=$(home_path ".openclaw/skills/${entry##*/}")
    [ ! -e "$dest" ] || continue
    stage=$(mktemp -d "$TARGET_HOME/.openclaw/skills/.install.XXXXXX")
    trap 'rm -rf "$stage"' 0
    cp -a "$entry" "$stage/skill"
    mv "$stage/skill" "$dest"
    rm -rf "$stage"
  done
)

sync_managed_image_skills() {
  . "$CETIRUN_BUILTIN_SKILL_SYNC"
  cetirun_sync_builtin_skills \
    "$SOURCE_HOME/.openclaw/skills" \
    "$(home_path .openclaw/skills)"
}

setup_azdevops_git_credential() (
  set -e
  [ "${AZDEVOPS_GIT_CREDENTIAL_ENABLED:-false}" = true ] || exit 0
  helper=$(home_path .git-credential-azdevops.sh)
  config=$(home_path .gitconfig)
  temp=$(mktemp "${helper}.tmp.XXXXXX")
  trap 'rm -f "$temp"' 0
  cat > "$temp" <<'SCRIPT'
#!/bin/sh
echo "username=azuredevops"
echo "password=$(az account get-access-token --resource 499b84ac-1321-427f-aa17-267ca6975798 --query accessToken -o tsv)"
SCRIPT
  chmod 700 "$temp"
  mv -f "$temp" "$helper"
  # Installation does not perform login. Optional git settings remain best-effort.
  if ! git config --file "$config" credential.https://dev.azure.com.useHttpPath true; then
    printf '[agent-init] WARNING: Azure DevOps git useHttpPath configuration failed\n' >&2
  fi
  if ! git config --file "$config" credential.https://dev.azure.com.provider generic; then
    printf '[agent-init] WARNING: Azure DevOps git provider configuration failed\n' >&2
  fi
  if ! git config --file "$config" credential.https://dev.azure.com.helper "$RUNTIME_HOME/.git-credential-azdevops.sh"; then
    printf '[agent-init] WARNING: Azure DevOps git helper configuration failed\n' >&2
  fi
)

agent_prepare_integrations() {
  prepare_plugin_link
  sync_managed_image_skills
  install_image_skills
  remove_retired_copilot_helper
  setup_azdevops_git_credential
}

# Models come through the platform AI Gateway, and GitHub Copilot sign-in
# moved to the Portal. Remove the retired in-agent helper earlier releases
# installed, so it does not suggest a login that no longer applies.
remove_retired_copilot_helper() {
  helper=$(home_path .local/bin/copilot-auth)
  if [ -f "$helper" ] && [ ! -L "$helper" ] && grep -q 'Authenticate GitHub Copilot for OpenClaw' "$helper"; then
    rm -f "$helper"
  fi
}

agent_verify() {
  config=$(home_path .openclaw/openclaw.json)
  jq -e 'type == "object"' "$config" >/dev/null 2>&1 ||
    { init_error "OpenClawConfigInvalid"; return 1; }
  home_write_marker "$LEGACY_HOME_MARKER" initialized-v1
}

run_agent_init
