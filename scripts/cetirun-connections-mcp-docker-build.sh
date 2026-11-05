#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail
repo_root="$(cd "$(dirname "$0")/.." && pwd)"
registry="${REGISTRY:-$("${repo_root}/scripts/cetirun-release-metadata.sh" registry)}"
version="${VERSION:-$("${repo_root}/scripts/cetirun-release-metadata.sh" version)}"
git_sha="${GIT_SHA:-$(git -C "${repo_root}" rev-parse HEAD)}"
arguments=(-f "${repo_root}/app/cetirun-connections-mcp/Dockerfile" -t "${registry}/cetirun-connections-mcp:${version}" --build-arg "VERSION=${version}"
  --label "org.opencontainers.image.version=${version}" --label "org.opencontainers.image.revision=${git_sha}")
if [[ "${PUSH:-false}" == true ]]; then arguments+=(--platform linux/amd64 --push); else arguments+=(--load); fi
docker buildx build "${arguments[@]}" "${repo_root}"
