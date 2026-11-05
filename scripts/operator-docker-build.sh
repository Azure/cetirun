#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

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
image_name="${IMAGE_NAME:-cetirun-operator}"
release_version="${VERSION:-$("${script_dir}/cetirun-release-metadata.sh" version)}"
image_tag="${IMAGE_TAG:-${release_version}}"
git_sha="${GIT_SHA:-$(git -C "${repo_root}" rev-parse HEAD 2>/dev/null || echo unknown)}"
push="${PUSH:-false}"
dockerfile="${repo_root}/app/cetirun-operator/Dockerfile"

image_ref="${registry}/${image_name}:${image_tag}"

build_args=(
  -f "${dockerfile}"
  -t "${image_ref}"
  --build-arg "VERSION=${image_tag}"
  --build-arg "GIT_SHA=${git_sha}"
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

docker buildx build "${build_args[@]}" "${repo_root}"
