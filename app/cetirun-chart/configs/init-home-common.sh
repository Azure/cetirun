#!/bin/sh
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# Sourced by a platform-owned Agent init script, not an Agent entrypoint.
if [ "${1:-}" = "--help" ]; then
  printf '%s\n' "Source this library, implement agent_* stages, then call run_agent_init."
fi

init_error() {
  printf '[agent-init] %s\n' "$*" >&2
  return 1
}

check_common_tools() {
  for tool in cp mkdir mv rm chmod mktemp find cmp; do
    command -v "$tool" >/dev/null 2>&1 || {
      init_error "InitToolsMissing: $tool"
      return 1
    }
  done
}

# Resolve a controlled relative path without following links out of the PVC.
home_path() (
  set -e
  rest=$1
  case "$rest" in ""|/*|*/|*"//"*) init_error "InvalidHomePath"; exit 1;; esac
  result=$TARGET_HOME
  while :; do
    part=${rest%%/*}
    case "$part" in ""|.|..) init_error "InvalidHomePath"; exit 1;; esac
    result=$result/$part
    if [ -L "$result" ]; then
      init_error "HomePathIsSymlink: $1"
      exit 1
    fi
    if [ "$rest" = "$part" ]; then break; fi
    if [ -e "$result" ] && [ ! -d "$result" ]; then
      init_error "HomeParentNotDirectory: $1"
      exit 1
    fi
    rest=${rest#*/}
  done
  printf '%s\n' "$result"
)

home_ensure_dir() (
  set -e
  dest=$(home_path "$1")
  mkdir -p "$dest"
)

home_atomic_copy() (
  set -e
  source=$1
  dest=$(home_path "$2")
  mode=${3:-600}
  [ -f "$source" ] || { init_error "RequiredInputMissing: $2"; exit 1; }
  [ ! -e "$dest" ] || [ -f "$dest" ] || { init_error "HomeTargetNotFile: $2"; exit 1; }
  temp=$(mktemp "${dest}.tmp.XXXXXX")
  trap 'rm -f "$temp"' 0
  cp "$source" "$temp"
  chmod "$mode" "$temp"
  mv -f "$temp" "$dest"
)

home_write_marker() (
  set -e
  dest=$(home_path "$1")
  [ ! -e "$dest" ] || [ -f "$dest" ] || { init_error "InvalidHomeMarker"; exit 1; }
  temp=$(mktemp "${dest}.tmp.XXXXXX")
  trap 'rm -f "$temp"' 0
  shift
  printf '%s\n' "$@" > "$temp"
  chmod 600 "$temp"
  mv -f "$temp" "$dest"
)

immutable_seed_image() {
  case "$CETIRUN_INIT_IMAGE" in
    *@sha256:*)
      digest=${CETIRUN_INIT_IMAGE##*@sha256:}
      [ "${#digest}" -eq 64 ] || return 1
      case "$digest" in *[!0-9a-f]*) return 1;; esac
      ;;
    *) return 1;;
  esac
}

seed_home_data() (
  set -e
  complete=$(home_path .cetirun-home-seeded)
  pending=$(home_path .cetirun-home-seeding)
  if [ -e "$complete" ]; then
    [ -f "$complete" ] || { init_error "InvalidHomeMarker"; exit 1; }
    IFS= read -r version < "$complete"
    [ "$version" = seeded-v1 ] || { init_error "InvalidHomeMarker"; exit 1; }
    printf '0\n'
    exit 0
  fi

  if [ -n "${LEGACY_HOME_MARKER:-}" ]; then
    legacy=$(home_path "$LEGACY_HOME_MARKER")
    if [ -f "$legacy" ]; then
      home_write_marker .cetirun-home-seeded seeded-v1 legacy
      printf '0\n'
      exit 0
    fi
  fi

  case "${CETIRUN_INIT_IMAGE:-}" in
    ""|*[[:space:]]*) init_error "InvalidSeedImage"; exit 1;;
  esac
  if [ -e "$pending" ]; then
    [ -f "$pending" ] || { init_error "InvalidHomeProgress"; exit 1; }
    {
      IFS= read -r version
      IFS= read -r prior_image
      IFS= read -r prior_stage
    } < "$pending"
    [ "$version" = seeding-v1 ] && [ "$prior_image" = "$CETIRUN_INIT_IMAGE" ] &&
      immutable_seed_image || {
        init_error "HomeSeedImageUnconfirmed: retry requires the same immutable image"
        exit 1
      }
    case "$prior_stage" in
      */*|*[!a-zA-Z0-9.-]*) init_error "InvalidHomeProgress"; exit 1;;
      .cetirun-home-stage.??????) ;;
      *) init_error "InvalidHomeProgress"; exit 1;;
    esac
    stage=$(home_path "$prior_stage")
    # This is only the recorded disposable copy, never the PVC or a HOME entry.
    if [ -e "$stage" ]; then
      [ -d "$stage" ] || { init_error "InvalidHomeProgress"; exit 1; }
      rm -rf "$stage"
    fi
  else
    for entry in "$TARGET_HOME"/* "$TARGET_HOME"/.[!.]* "$TARGET_HOME"/..?*; do
      [ -e "$entry" ] || [ -L "$entry" ] || continue
      if [ "${entry##*/}" = lost+found ] && [ -d "$entry" ] && [ ! -L "$entry" ]; then
        continue
      fi
      init_error "HomeSeedStateUnknown: nonempty HOME has no initialization record"
      exit 1
    done
  fi

  stage=$(mktemp -d "$TARGET_HOME/.cetirun-home-stage.XXXXXX")
  home_write_marker .cetirun-home-seeding seeding-v1 "$CETIRUN_INIT_IMAGE" "${stage##*/}"
  for entry in "$SOURCE_HOME"/* "$SOURCE_HOME"/.[!.]* "$SOURCE_HOME"/..?*; do
    [ -e "$entry" ] || [ -L "$entry" ] || continue
    name=${entry##*/}
    case "$name" in .cetirun-home-*|lost+found) continue;; esac
    special=$(find "$entry" ! -type d ! -type f ! -type l -print)
    [ -z "$special" ] || { init_error "UnsupportedHomeFile"; exit 1; }
    # Each entry is copied to a fresh location, so existing destination links
    # cannot redirect recursive copying outside the PVC.
    cp -a "$entry" "$stage/$name"
  done
  for entry in "$stage"/* "$stage"/.[!.]* "$stage"/..?*; do
    [ -e "$entry" ] || [ -L "$entry" ] || continue
    dest=$TARGET_HOME/${entry##*/}
    # A previous interrupted publish may already have moved whole top-level
    # entries. Preserve those entries rather than recopying into them.
    if [ ! -e "$dest" ] && [ ! -L "$dest" ]; then
      mv "$entry" "$dest"
    fi
  done
  home_write_marker .cetirun-home-seeded seeded-v1 "$CETIRUN_INIT_IMAGE"
  rm -f "$pending"
  rm -rf "$stage"
  printf '1\n'
)

seed_home_once() {
  HOME_SEEDED_THIS_RUN=$(seed_home_data)
}

check_agent_context() {
  if [ -n "${CETIRUN_MEMORY_ASSETS:-}" ]; then
    [ "$CETIRUN_MEMORY_ASSETS" = /opt/cetirun-memory ] &&
      [ -r "$CETIRUN_MEMORY_ASSETS/manifest.json" ] &&
      [ -r "$CETIRUN_MEMORY_ASSETS/AGENTS.md" ] &&
      [ -r "$CETIRUN_MEMORY_ASSETS/skills/cetirun-memory/SKILL.md" ] || {
        init_error "MemoryAssetsUnavailable"
        return 1
      }
  fi
  if [ -n "${CETIRUN_SHARED_SKILLS_DIR:-}" ]; then
    [ -d "$CETIRUN_SHARED_SKILLS_DIR" ] && [ -r "$CETIRUN_SHARED_SKILLS_DIR" ] &&
      [ -x "$CETIRUN_SHARED_SKILLS_DIR" ] || {
        init_error "SharedSkillsUnavailable"
        return 1
      }
  fi
  if [ -n "${CETIRUN_MEMORY_ASSETS:-}" ] || [ -n "${CETIRUN_SHARED_SKILLS_DIR:-}" ]; then
    [ -n "${CETIRUN_CONTEXT_VIEW:-}" ] && [ -d "$CETIRUN_CONTEXT_VIEW" ] &&
      [ ! -L "$CETIRUN_CONTEXT_VIEW" ] && [ -w "$CETIRUN_CONTEXT_VIEW" ] || {
        init_error "ContextViewUnavailable"
        return 1
      }
  fi
}

# Compose platform context; native Agents mount only its reserved leaves.
# Sources and the PVC's own .agents contents are never modified.
prepare_agent_context() (
  set -e
  [ -n "${CETIRUN_CONTEXT_VIEW:-}" ] || exit 0
  home_ensure_dir .agents
  if [ -z "${CETIRUN_CONTEXT_DIR:-}" ]; then
    home_ensure_dir .agents/skills
    home_path .agents/AGENTS.md >/dev/null
  fi
  view=$CETIRUN_CONTEXT_VIEW
  [ ! -L "$view/current" ] || { init_error "InvalidContextView"; exit 1; }
  [ ! -e "$view/current" ] || {
    [ -f "$view/current/.ready" ] || { init_error "IncompleteContextView"; exit 1; }
    exit 0
  }
  stage=$(mktemp -d "$view/.context.XXXXXX")
  trap 'rm -rf "$stage"' 0
  mkdir "$stage/skills"
  if [ -n "${CETIRUN_CONTEXT_DIR:-}" ]; then
    # Only the platform ConfigMap projection may use Kubernetes-owned links.
    cp -RL "$CETIRUN_CONTEXT_DIR/AGENTS.md" "$stage/AGENTS.md"
    for entry in "$CETIRUN_CONTEXT_DIR/skills"/* "$CETIRUN_CONTEXT_DIR/skills"/.[!.]* "$CETIRUN_CONTEXT_DIR/skills"/..?*; do
      [ -e "$entry" ] || [ -L "$entry" ] || continue
      if [ -n "${CETIRUN_MEMORY_ASSETS:-}" ] && [ "${entry##*/}" = cetirun-memory ]; then continue; fi
      cp -RL "$entry" "$stage/skills/${entry##*/}"
    done
  else
    : > "$stage/AGENTS.md"
  fi
  if [ -n "${CETIRUN_MEMORY_ASSETS:-}" ]; then
    chmod u+w "$stage/AGENTS.md"
    if [ -s "$stage/AGENTS.md" ]; then printf '\n' >> "$stage/AGENTS.md"; fi
    while IFS= read -r line || [ -n "$line" ]; do
      printf '%s\n' "$line" >> "$stage/AGENTS.md"
    done < "$CETIRUN_MEMORY_ASSETS/AGENTS.md"
    cp -R "$CETIRUN_MEMORY_ASSETS/skills/cetirun-memory" "$stage/skills/cetirun-memory"
  fi
  if [ -n "${CETIRUN_SHARED_SKILLS_DIR:-}" ]; then
    for entry in "$CETIRUN_SHARED_SKILLS_DIR"/* "$CETIRUN_SHARED_SKILLS_DIR"/.[!.]* "$CETIRUN_SHARED_SKILLS_DIR"/..?*; do
      [ -e "$entry" ] || [ -L "$entry" ] || continue
      name=${entry##*/}
      if [ "$name" = cetirun-memory ] || [ -e "$stage/skills/$name" ] || [ -L "$stage/skills/$name" ]; then
        init_error "SharedSkillConflictsWithCanonicalContext"
        exit 1
      fi
      special=$(find "$entry" ! -type d ! -type f ! -type l -print)
      [ -z "$special" ] || { init_error "UnsupportedSkillFile"; exit 1; }
      cp -RP "$entry" "$stage/skills/$name"
    done
  fi
  printf 'context-v1\n' > "$stage/.ready"
  mv "$stage" "$view/current"
)

run_agent_init() {
  set -e
  check_common_tools
  : "${SOURCE_HOME:?SOURCE_HOME is required}"
  : "${TARGET_HOME:?TARGET_HOME is required}"
  [ -d "$SOURCE_HOME" ] && [ -d "$TARGET_HOME" ] &&
    [ ! -L "$SOURCE_HOME" ] && [ ! -L "$TARGET_HOME" ] || {
      init_error "HomeDirectoryUnavailable"
      return 1
    }
  SOURCE_HOME=$(CDPATH= cd -P "$SOURCE_HOME" && pwd -P)
  TARGET_HOME=$(CDPATH= cd -P "$TARGET_HOME" && pwd -P)
  case "$SOURCE_HOME:$TARGET_HOME" in /:*|*:/) init_error "InvalidHomeRoot"; return 1;; esac
  case "$SOURCE_HOME/" in "$TARGET_HOME/"*) init_error "OverlappingHomePaths"; return 1;; esac
  case "$TARGET_HOME/" in "$SOURCE_HOME/"*) init_error "OverlappingHomePaths"; return 1;; esac
  for stage in agent_check_requirements agent_prepare_home agent_apply_config agent_prepare_integrations agent_verify; do
    command -v "$stage" >/dev/null 2>&1 || { init_error "InitStageMissing: $stage"; return 1; }
  done
  agent_check_requirements
  check_agent_context
  printf '[agent-init] seed_home_once\n' >&2
  seed_home_once
  for stage in agent_prepare_home agent_apply_config agent_prepare_integrations prepare_agent_context agent_verify; do
    printf '[agent-init] %s\n' "$stage" >&2
    "$stage"
  done
  printf '[agent-init] complete\n' >&2
}
