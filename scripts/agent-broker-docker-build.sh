#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail
command -v docker >/dev/null 2>&1 || { echo "ERROR: docker is required" >&2; exit 1; }
docker buildx version >/dev/null 2>&1 || { echo "ERROR: docker buildx is required" >&2; exit 1; }

script_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
registry="${REGISTRY:-$("${script_dir}/cetirun-release-metadata.sh" registry)}"
release_version="${VERSION:-$("${script_dir}/cetirun-release-metadata.sh" version)}"
image_tag="${IMAGE_TAG:-${release_version}}"
image_ref="${registry}/cetirun-agent-broker:${image_tag}"
git_sha="${GIT_SHA:-$(git -C "${repo_root}" rev-parse HEAD)}"
args=(-f "${repo_root}/app/cetirun-agent-broker/Dockerfile" -t "${image_ref}"
  --label "org.opencontainers.image.version=${image_tag}"
  --label "org.opencontainers.image.revision=${git_sha}")
if [[ "${PUSH:-false}" == true ]]; then
  "${script_dir}/cetirun-release-metadata.sh" check-push-registry "${registry}"
  args+=(--platform linux/amd64 --push)
  if docker buildx inspect --bootstrap 2>/dev/null | grep -q 'Driver:.*docker-container'; then
    args+=(--provenance=true --sbom=true)
  fi
else
  args+=(--load)
fi
docker buildx build "${args[@]}" "${repo_root}"
