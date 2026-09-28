#!/bin/sh
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -eu
if [ "${1:-}" = "--help" ]; then
  printf '%s\n' "Initialize Custom HOME with SOURCE_HOME, TARGET_HOME and CETIRUN_INIT_IMAGE."
  exit 0
fi

. "${CETIRUN_INIT_COMMON:-/etc/cetirun/init-home-common.sh}"
LEGACY_HOME_MARKER=

agent_check_requirements() {
  [ -r "$SOURCE_HOME" ] && [ -x "$SOURCE_HOME" ] && [ -w "$TARGET_HOME" ] || {
    init_error "HomePermissionsInvalid"
    return 1
  }
  if [ ! -f "$TARGET_HOME/.cetirun-home-seeded" ] &&
    { [ -L "$SOURCE_HOME/.agents" ] ||
      { [ -e "$SOURCE_HOME/.agents" ] && [ ! -d "$SOURCE_HOME/.agents" ]; }; }; then
    init_error "ImageAgentsPathInvalid"
    return 1
  fi
  [ -r "${CETIRUN_CONTEXT_DIR:-/etc/cetirun-context}/AGENTS.md" ] &&
    [ -r "${CETIRUN_CONTEXT_DIR:-/etc/cetirun-context}/skills/cetirun-memory/SKILL.md" ] || {
      init_error "ContextBindingPending"
      return 1
    }
  if [ -n "${CETIRUN_SHARED_SKILLS_DIR:-}" ]; then
    [ -d "$CETIRUN_SHARED_SKILLS_DIR" ] && [ -r "$CETIRUN_SHARED_SKILLS_DIR" ] &&
      [ -x "$CETIRUN_SHARED_SKILLS_DIR" ] &&
      [ -n "${CETIRUN_CONTEXT_VIEW:-}" ] && [ -d "$CETIRUN_CONTEXT_VIEW" ] &&
      [ ! -L "$CETIRUN_CONTEXT_VIEW" ] && [ -w "$CETIRUN_CONTEXT_VIEW" ] || {
        init_error "SharedSkillsUnavailable"
        return 1
      }
  fi
}

agent_prepare_home() {
  # The whole approved view will shadow this mountpoint in the main container.
  # Refuse a symlink/file; never repair it by removing image or user content.
  home_ensure_dir .agents
}

agent_apply_config() { :; }
agent_prepare_integrations() (
  [ -n "${CETIRUN_SHARED_SKILLS_DIR:-}" ] || exit 0
  view=$CETIRUN_CONTEXT_VIEW
  context=${CETIRUN_CONTEXT_DIR:-/etc/cetirun-context}
  # The completed per-Pod view survives init retries, but never a Pod replacement.
  [ ! -L "$view/current" ] || { init_error "InvalidContextView"; exit 1; }
  [ ! -e "$view/current" ] || {
    [ -f "$view/current/.ready" ] || { init_error "IncompleteContextView"; exit 1; }
    exit 0
  }
  stage=$(mktemp -d "$view/.context.XXXXXX")
  trap 'rm -rf "$stage"' 0
  # ConfigMap projections contain Kubernetes-owned links; dereference only
  # that approved source. Shared material must not redirect canonical writes.
  cp -RL "$context/AGENTS.md" "$stage/AGENTS.md"
  cp -RL "$context/skills" "$stage/skills"
  chmod u+w "$stage/skills"
  for entry in "$CETIRUN_SHARED_SKILLS_DIR"/* "$CETIRUN_SHARED_SKILLS_DIR"/.[!.]* "$CETIRUN_SHARED_SKILLS_DIR"/..?*; do
    [ -e "$entry" ] || [ -L "$entry" ] || continue
    name=${entry##*/}
    if [ -e "$stage/skills/$name" ] || [ -L "$stage/skills/$name" ]; then
      init_error "SharedSkillConflictsWithCanonicalContext"
      exit 1
    fi
    special=$(find "$entry" ! -type d ! -type f ! -type l -print)
    [ -z "$special" ] || { init_error "UnsupportedSkillFile"; exit 1; }
    cp -RP "$entry" "$stage/skills/$name"
  done
  printf 'context-v1\n' > "$stage/.ready"
  mv "$stage" "$view/current"
)
agent_verify() {
  [ -r "$TARGET_HOME/.cetirun-home-seeded" ] || {
    init_error "HomeSeedIncomplete"
    return 1
  }
}

run_agent_init
