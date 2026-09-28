#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail
command -v docker >/dev/null 2>&1 || { echo "ERROR: docker is required" >&2; exit 1; }
docker buildx version >/dev/null 2>&1 || { echo "ERROR: docker buildx is required" >&2; exit 1; }

script_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
registry="${REGISTRY:-cetirun.azurecr.io}"
image_tag="${IMAGE_TAG:-latest}"
image_ref="${registry}/cetirun-agent-broker:${image_tag}"
args=(-f "${repo_root}/app/cetirun-agent-broker/Dockerfile" -t "${image_ref}")
if [[ "${PUSH:-false}" == true ]]; then
  args+=(--platform linux/amd64 --push)
  if docker buildx inspect --bootstrap 2>/dev/null | grep -q 'Driver:.*docker-container'; then
    args+=(--provenance=true --sbom=true)
  fi
else
  args+=(--load)
fi
docker buildx build "${args[@]}" "${repo_root}"
