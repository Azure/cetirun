#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
chart_dir="${CHART_DIR:-${repo_root}/app/cetirun-platform}"

if [ ! -f "${chart_dir}/Chart.yaml" ]; then
  echo "ERROR: Chart.yaml not found in ${chart_dir}" >&2
  exit 1
fi

helm dependency build --skip-refresh "${chart_dir}" > /dev/null

base_args=(
  -f "${chart_dir}/values.yaml"
  --set istio.enabled=true
  --set-string cetirun-operator.domainSuffix=platform.example.invalid
  --set-string cetirun-portal.istio.host=portal.example.invalid
)

echo "==> helm lint ${chart_dir} (default platform)"
helm lint --strict "${chart_dir}" "${base_args[@]}"

echo "==> helm template platform (default platform)"
output=$(helm template platform "${chart_dir}" "${base_args[@]}")
grep -q 'name: cetirun-operator' <<< "${output}" || {
  echo "FAIL: operator was not rendered" >&2
  exit 1
}
grep -q 'name: cetirun-portal' <<< "${output}" || {
  echo "FAIL: portal was not rendered" >&2
  exit 1
}
grep -q 'namespace: default' <<< "${output}" || {
  echo "FAIL: portal was not rendered into the release namespace" >&2
  exit 1
}

echo "==> helm template platform (custom gateway)"
output=$(helm template platform "${chart_dir}" "${base_args[@]}" \
  --set-string cetirun-portal.istio.gateway=custom-namespace/custom-gateway \
  --set-string cetirun-portal.istio.gatewayNamespace=custom-namespace \
  --set-string cetirun-portal.istio.gatewayServiceAccount=custom-gateway-sa)
grep -q -- '- custom-namespace/custom-gateway' <<< "${output}" || {
  echo "FAIL: portal VirtualService did not use the configured gateway" >&2
  exit 1
}
grep -q 'kubernetes.io/metadata.name: custom-namespace' <<< "${output}" || {
  echo "FAIL: portal policies did not use the configured gateway namespace" >&2
  exit 1
}
grep -q 'cluster.local/ns/custom-namespace/sa/custom-gateway-sa' <<< "${output}" || {
  echo "FAIL: portal AuthorizationPolicy did not use the configured gateway service account" >&2
  exit 1
}

echo "==> helm template platform (operator only)"
output=$(helm template platform "${chart_dir}" "${base_args[@]}" --set cetirun-portal.enabled=false)
if grep -q 'name: cetirun-portal' <<< "${output}"; then
  echo "FAIL: portal rendered while disabled" >&2
  exit 1
fi

echo "==> helm template platform (portal only)"
output=$(helm template platform "${chart_dir}" "${base_args[@]}" --set cetirun-operator.enabled=false)
if grep -q 'name: cetirun-operator' <<< "${output}"; then
  echo "FAIL: operator rendered while disabled" >&2
  exit 1
fi

echo "==> helm template platform (missing Istio confirmation, expect failure)"
if helm template platform "${chart_dir}" \
  --set-string cetirun-operator.domainSuffix=platform.example.invalid \
  --set-string cetirun-portal.istio.host=portal.example.invalid 2>/dev/null; then
  echo "FAIL: platform rendered without Istio confirmation" >&2
  exit 1
fi

echo "==> helm template platform (missing portal hostname, expect failure)"
if helm template platform "${chart_dir}" \
  --set istio.enabled=true \
  --set-string cetirun-operator.domainSuffix=platform.example.invalid 2>/dev/null; then
  echo "FAIL: platform rendered without portal hostname" >&2
  exit 1
fi

echo "==> helm template platform (missing gateway service account, expect failure)"
if helm template platform "${chart_dir}" "${base_args[@]}" \
  --set-string cetirun-portal.istio.gatewayServiceAccount= 2>/dev/null; then
  echo "FAIL: platform rendered without gateway service account" >&2
  exit 1
fi

echo "==> helm template platform (missing operator domain suffix, expect failure)"
if helm template platform "${chart_dir}" \
  --set istio.enabled=true \
  --set-string cetirun-portal.istio.host=portal.example.invalid 2>/dev/null; then
  echo "FAIL: platform rendered without operator domain suffix" >&2
  exit 1
fi
