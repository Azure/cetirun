#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail
repo_root="$(cd "$(dirname "$0")/.." && pwd)"
registry="${REGISTRY:?REGISTRY is required}"
version="${VERSION:-$("${repo_root}/scripts/cetirun-release-metadata.sh" version)}"
temporary="$(mktemp -d)"
trap 'rm -rf "$temporary"' EXIT
helm lint --strict "${repo_root}/app/cetirun-connections-mcp/chart" \
  --set tls.secretName=tools-tls --set broker.clientTLSSecretName=broker-client-tls \
  --set broker.baseURL=https://broker.example.com --set istio.sidecarInjection=true --set istio.revision=asm-example
helm package "${repo_root}/app/cetirun-connections-mcp/chart" --destination "$temporary" --version "$version" --app-version "$version"
helm push "$temporary/cetirun-connections-mcp-${version}.tgz" "oci://${registry}/${CHART_NAMESPACE:-helm}"
if [[ "${VERIFY_PULL:-true}" == true ]]; then
  mkdir "$temporary/verify"
  helm pull "oci://${registry}/${CHART_NAMESPACE:-helm}/cetirun-connections-mcp" --version "$version" --destination "$temporary/verify"
fi
