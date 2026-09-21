#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
chart_dir="${CHART_DIR:-${repo_root}/infra/istio}"

if [ ! -f "${chart_dir}/Chart.yaml" ]; then
  echo "ERROR: Chart.yaml not found in ${chart_dir}" >&2
  exit 1
fi

base_args=(
  --set-string clientId=lint-client
  --set-string tenantId=lint-tenant
  --set-string allowedGroups=lint-group
  --set-string cookieSecret=lint-cookie-secret
)

assert_ownership_target() {
  local output="$1"
  local host="$2"
  grep -Fq "address: \"${host}\"" <<< "${output}" || {
    echo "FAIL: ownership cluster did not target ${host}" >&2
    exit 1
  }
  grep -Fq "[\":authority\"] = \"${host}\"" <<< "${output}" || {
    echo "FAIL: ownership request authority did not target ${host}" >&2
    exit 1
  }
}

echo "==> helm template cetirun-istio (standalone portal default)"
output=$(helm template cetirun-istio "${chart_dir}" "${base_args[@]}")
assert_ownership_target "${output}" "cetirun-portal.cetirun-portal.svc.cluster.local"

echo "==> helm template cetirun-istio (umbrella portal override)"
output=$(helm template cetirun-istio "${chart_dir}" "${base_args[@]}" \
  --set-string portalAuth.clusterHost=cetirun-portal.cetirun-system.svc.cluster.local)
assert_ownership_target "${output}" "cetirun-portal.cetirun-system.svc.cluster.local"
