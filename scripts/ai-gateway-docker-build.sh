#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# Build, and optionally push, the CetiRun AI Gateway image. The same image
# serves the shared Gateway and the per-agent Copilot proxy.

set -euo pipefail

if ! command -v docker >/dev/null 2>&1; then
  echo "ERROR: docker is not installed or not found in PATH." >&2
  exit 1
fi
if ! docker buildx version >/dev/null 2>&1; then
  echo "ERROR: docker buildx is not available." >&2
  exit 1
fi

script_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"

registry="${REGISTRY:-$("${script_dir}/cetirun-release-metadata.sh" registry)}"
image_name="${IMAGE_NAME:-cetirun-ai-gateway}"
release_version="${VERSION:-$("${script_dir}/cetirun-release-metadata.sh" version)}"
image_tag="${IMAGE_TAG:-${release_version}}"
git_sha="${GIT_SHA:-$(git -C "${repo_root}" rev-parse HEAD 2>/dev/null || echo unknown)}"
push="${PUSH:-false}"
context_dir="${repo_root}/app/cetirun-ai-gateway"
# Package sources, for networks that block the public registries. Same names
# and defaults as the other CetiRun image builds.
pypi_index_url="${PYPI_INDEX_URL:-https://pypi.org/simple/}"
npm_registry="${NPM_REGISTRY:-https://registry.npmjs.org/}"

image_ref="${registry}/${image_name}:${image_tag}"

build_args=(
  -f "${context_dir}/Dockerfile"
  -t "${image_ref}"
  --build-arg "VERSION=${image_tag}"
  --build-arg "GIT_SHA=${git_sha}"
  --build-arg "PYPI_INDEX_URL=${pypi_index_url}"
  --build-arg "NPM_REGISTRY=${npm_registry}"
)

if [ "${push}" = "true" ]; then
  "${script_dir}/cetirun-release-metadata.sh" check-push-registry "${registry}"
  build_args+=(--platform linux/amd64 --push)
  # Attestations (provenance/SBOM) require a non-default buildx driver.
  if docker buildx inspect --bootstrap 2>/dev/null | grep -q 'Driver:.*docker-container'; then
    build_args+=(--provenance=true --sbom=true)
  fi
  echo "==> Building and pushing ${image_ref}"
else
  build_args+=(--load)
  echo "==> Building ${image_ref}"
fi

docker buildx build "${build_args[@]}" "${context_dir}"
