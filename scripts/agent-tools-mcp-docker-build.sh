#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail
repo_root="$(cd "$(dirname "$0")/.." && pwd)"
registry="${REGISTRY:?REGISTRY is required}"
version="${VERSION:-$("${repo_root}/scripts/cetirun-release-metadata.sh" version)}"
arguments=(-f "${repo_root}/app/agent-tools-mcp/Dockerfile" -t "${registry}/agent-tools-mcp:${version}" --build-arg "VERSION=${version}")
if [[ "${PUSH:-false}" == true ]]; then arguments+=(--platform linux/amd64 --push); else arguments+=(--load); fi
docker buildx build "${arguments[@]}" "${repo_root}"
