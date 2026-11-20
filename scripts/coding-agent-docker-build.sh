#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail

case "${1:-}" in
  -h|--help)
    printf '%s\n' \
      "Usage: CODING_RUNTIME=copilot|claude|codex coding-agent-docker-build.sh" \
      "Build one native coding Agent image for linux/amd64; no registry login is performed." \
      "Inputs: REGISTRY, VERSION, IMAGE_TAG, GIT_SHA, BASE_IMAGE, SBOM_GENERATOR." \
      "BASE_IMAGE defaults to REGISTRY/cetirun-base:IMAGE_TAG; build it first." \
      "PUSH defaults to false. Set PUSH=true only for an authorized publication."
    exit 0
    ;;
  "") ;;
  *) printf '%s\n' "Unknown argument; use --help." >&2; exit 2 ;;
esac
[[ $# -eq 0 ]] || { printf '%s\n' "Unexpected arguments; use --help." >&2; exit 2; }

script_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
source "${script_dir}/cetirun-build-attestations.sh"
cetirun_validate_sbom_generator "${SBOM_GENERATOR:-}"
runtime="${CODING_RUNTIME:-}"
case "${runtime}" in
  copilot|claude|codex) ;;
  *) printf '%s\n' "CODING_RUNTIME must be copilot, claude, or codex." >&2; exit 2 ;;
esac
registry="${REGISTRY:-$("${script_dir}/cetirun-release-metadata.sh" registry)}"
release_version="${VERSION:-$("${script_dir}/cetirun-release-metadata.sh" version)}"
image_tag="${IMAGE_TAG:-${release_version}}"
git_sha="${GIT_SHA:-$(git -C "${repo_root}" rev-parse HEAD)}"
base_image="${BASE_IMAGE:-${registry%/}/cetirun-base:${image_tag}}"
push="${PUSH:-false}"
context_dir="${repo_root}/examples/coding-agents/app"

[[ "${registry}" != *"://"* && "${registry}" =~ ^[a-zA-Z0-9][a-zA-Z0-9./:_-]*$ ]] || { printf '%s\n' "Invalid REGISTRY." >&2; exit 2; }
[[ "${image_tag}" =~ ^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,127}$ ]] || { printf '%s\n' "Invalid IMAGE_TAG." >&2; exit 2; }
[[ "${base_image}" != *"://"* && "${base_image}" =~ ^[a-zA-Z0-9][a-zA-Z0-9./:@_-]*$ ]] || { printf '%s\n' "Invalid BASE_IMAGE." >&2; exit 2; }
[[ "${push}" == "true" || "${push}" == "false" ]] || { printf '%s\n' "PUSH must be true or false." >&2; exit 2; }
cetirun_require_sbom_generator_push "${push}"
for file in Dockerfile artifacts.json install_runtime.py native.py server.py index.html; do
  test -f "${context_dir}/${file}" || { printf 'Missing coding Agent build input: %s\n' "${file}" >&2; exit 1; }
done

args=(
  --platform linux/amd64
  -t "${registry%/}/cetirun-${runtime}:${image_tag}"
  --build-arg "BASE_IMAGE=${base_image}"
  --build-arg "CODING_RUNTIME=${runtime}"
  --label "org.opencontainers.image.version=${image_tag}"
  --label "org.opencontainers.image.revision=${git_sha}"
)
if [[ "${push}" == "true" ]]; then
  "${script_dir}/cetirun-release-metadata.sh" check-push-registry "${registry}"
  args+=(--push)
  buildx_driver="$(docker buildx inspect --bootstrap | awk '/^Driver:/ { print $2; exit }')"
  cetirun_configure_sbom_attestation "${buildx_driver}"
  args+=("${CETIRUN_SBOM_ATTESTATION_ARGS[@]}")
else
  args+=(--load)
fi
docker buildx build "${args[@]}" "${context_dir}"
