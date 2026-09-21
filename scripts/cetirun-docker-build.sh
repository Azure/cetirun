#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"

registry="${REGISTRY:-cetirun.azurecr.io}"
image_name="${IMAGE_NAME:-openclaw-gateway}"
image_tag="${IMAGE_TAG:-latest}"
push="${PUSH:-false}"
context_dir="${repo_root}/app/cetirun-chart"
dockerfile="${DOCKERFILE:-Dockerfile.openclaw}"
build_base="${BUILD_BASE:-true}"
build_desktop="${BUILD_DESKTOP:-true}"
build_hermes="${BUILD_HERMES:-false}"

image_ref="${registry}/${image_name}:${image_tag}"
base_image_ref="${BASE_IMAGE:-${registry}/cetirun-base:${image_tag}}"

# Default to the CFS-protected corporate feed. Callers can explicitly override
# this when their environment is configured with a different registry.
npm_registry="${NPM_REGISTRY:-https://packagefeedproxy.microsoft.io/npm/}"
npm_registry_args=(--build-arg "NPM_REGISTRY=${npm_registry}")
# Match the public Dockerfile default; private deployment environments can
# explicitly select their own package proxy with PYPI_INDEX_URL.
pypi_index_url="${PYPI_INDEX_URL:-https://pypi.org/simple/}"
pypi_index_args=(--build-arg "PYPI_INDEX_URL=${pypi_index_url}")

# The corporate feed blocks OpenClaw package ingestion. Keep the exception
# separate so other images continue to use the corporate feed by default.
openclaw_npm_registry="${OPENCLAW_NPM_REGISTRY:-https://registry.npmjs.org/}"
openclaw_npm_registry_args=(--build-arg "NPM_REGISTRY=${openclaw_npm_registry}")

common_output_args=()
if docker buildx inspect --bootstrap 2>/dev/null | grep -q 'Driver:.*docker-container'; then
  common_output_args+=(--provenance=true --sbom=true)
fi
if [ "${push}" = "true" ]; then
  common_output_args+=(--push)
else
  common_output_args+=(--load)
fi

if [ "${build_base}" = "true" ]; then
  base_build_args=(
    --platform linux/amd64
    -f "${context_dir}/Dockerfile.base"
    -t "${base_image_ref}"
    "${npm_registry_args[@]}"
    "${pypi_index_args[@]}"
    "${common_output_args[@]}"
  )
  if [ "${push}" = "true" ]; then
    echo "==> Building and pushing ${base_image_ref} from ${context_dir} (Dockerfile.base)"
  else
    echo "==> Building ${base_image_ref} from ${context_dir} (Dockerfile.base)"
  fi
  docker buildx build "${base_build_args[@]}" "${context_dir}"
fi

image_npm_registry_args=("${npm_registry_args[@]}")
if [ "${dockerfile##*/}" = "Dockerfile.openclaw" ]; then
  image_npm_registry_args=("${openclaw_npm_registry_args[@]}")
fi

build_args=(
  --platform linux/amd64
  -f "${context_dir}/${dockerfile}"
  -t "${image_ref}"
  --build-arg "BASE_IMAGE=${base_image_ref}"
  "${image_npm_registry_args[@]}"
  "${common_output_args[@]}"
)

if [ "${push}" = "true" ]; then
  echo "==> Building and pushing ${image_ref} from ${context_dir} (${dockerfile})"
else
  echo "==> Building ${image_ref} from ${context_dir} (${dockerfile})"
fi

docker buildx build "${build_args[@]}" "${context_dir}"

if [ "${build_hermes}" = "true" ]; then
  hermes_image_ref="${registry}/hermes-gateway:${image_tag}"
  hermes_build_args=(
    --platform linux/amd64
    -f "${context_dir}/Dockerfile.hermes"
    -t "${hermes_image_ref}"
    --build-arg "BASE_IMAGE=${base_image_ref}"
    "${npm_registry_args[@]}"
    "${pypi_index_args[@]}"
    "${common_output_args[@]}"
  )
  if [ "${push}" = "true" ]; then
    echo "==> Building and pushing ${hermes_image_ref} from ${context_dir} (Dockerfile.hermes)"
  else
    echo "==> Building ${hermes_image_ref} from ${context_dir} (Dockerfile.hermes)"
  fi
  docker buildx build "${hermes_build_args[@]}" "${context_dir}"
fi

if [ "${build_desktop}" = "true" ]; then
  desktop_image_ref="${registry}/cetirun-desktop:${image_tag}"
  desktop_build_args=(
    --platform linux/amd64
    -f "${context_dir}/Dockerfile.desktop"
    -t "${desktop_image_ref}"
    "${common_output_args[@]}"
  )
  if [ "${push}" = "true" ]; then
    echo "==> Building and pushing ${desktop_image_ref} from ${context_dir} (Dockerfile.desktop)"
  else
    echo "==> Building ${desktop_image_ref} from ${context_dir} (Dockerfile.desktop)"
  fi
  docker buildx build "${desktop_build_args[@]}" "${context_dir}"
fi
