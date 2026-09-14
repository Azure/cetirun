#!/usr/bin/env bash
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

registry="${REGISTRY:-cetirun.azurecr.io}"
image_name="${IMAGE_NAME:-cetirun-operator}"
image_tag="${IMAGE_TAG:-latest}"
push="${PUSH:-false}"
dockerfile="${repo_root}/app/cetirun-operator/Dockerfile"

image_ref="${registry}/${image_name}:${image_tag}"

build_args=(
  -f "${dockerfile}"
  -t "${image_ref}"
)

if [ "${push}" = "true" ]; then
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
