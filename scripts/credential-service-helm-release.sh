#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail
script_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
chart_dir="${CHART_DIR:-${repo_root}/app/cetirun-credential-service/chart}"
registry="${REGISTRY:-cetirun.azurecr.io}"
chart_namespace="${CHART_NAMESPACE:-helm}"
chart_version="${CHART_VERSION:?Set CHART_VERSION}"
package_dir="$(mktemp -d)"
trap 'rm -rf "${package_dir}"' EXIT

helm lint "${chart_dir}" \
  --set agentIdentity.tenantId=11111111-1111-1111-1111-111111111111 \
  --set agentIdentity.blueprintClientId=22222222-2222-2222-2222-222222222222 \
  --set agentIdentity.authorizationClientId=33333333-3333-3333-3333-333333333333 \
  --set agentIdentity.encryptionClientId=44444444-4444-4444-4444-444444444444 \
  --set agentIdentity.keyVaultKeyUrl=https://example.vault.azure.net/keys/cache \
  --set agentIdentity.redirectUri=https://portal.example.com/api/v1/agent-authorization/callback \
  --set agentIdentity.portalOrigin=https://portal.example.com \
  --set agentIdentity.portalBaseUrl=https://portal.example.com \
  --set istio.sidecarInjection=true \
  --set-string istio.revision=asm-example
package_args=("${chart_dir}" --destination "${package_dir}" --version "${chart_version}")
[[ -n "${APP_VERSION:-}" ]] && package_args+=(--app-version "${APP_VERSION}")
helm package "${package_args[@]}" >/dev/null
helm push "${package_dir}/cetirun-credential-service-${chart_version}.tgz" "oci://${registry}/${chart_namespace}"
if [[ "${VERIFY_PULL:-false}" == true ]]; then
  verify_dir="$(mktemp -d)"
  helm pull "oci://${registry}/${chart_namespace}/cetirun-credential-service" --version "${chart_version}" --destination "${verify_dir}"
  rm -rf "${verify_dir}"
fi
