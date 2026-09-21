#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# Lint, package, and push the self-contained CetiRun platform umbrella chart.
set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
chart_dir="${CHART_DIR:-${repo_root}/app/cetirun-platform}"
registry="${REGISTRY:-cetirun.azurecr.io}"
chart_namespace="${CHART_NAMESPACE:-helm}"
verify_pull="${VERIFY_PULL:-false}"

for command in helm; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "ERROR: Missing required command: ${command}" >&2
    exit 1
  fi
done

if [ ! -f "${chart_dir}/Chart.yaml" ]; then
  echo "ERROR: Chart.yaml not found in ${chart_dir}" >&2
  exit 1
fi

chart_name="$(awk -F': *' '$1=="name" {print $2; exit}' "${chart_dir}/Chart.yaml")"
chart_version="${CHART_VERSION:-$(awk -F': *' '$1=="version" {print $2; exit}' "${chart_dir}/Chart.yaml")}"
if [ -z "${chart_name}" ] || [ -z "${chart_version}" ]; then
  echo "ERROR: Failed to parse chart name or version" >&2
  exit 1
fi

package_dir="$(mktemp -d)"
verify_dir=""
cleanup() {
  rm -rf "${package_dir}"
  if [ -n "${verify_dir}" ] && [ -d "${verify_dir}" ]; then
    rm -rf "${verify_dir}"
  fi
}
trap cleanup EXIT

echo "==> Building local chart dependencies"
helm dependency build --skip-refresh "${chart_dir}"
echo "==> Linting Helm chart"
helm lint --strict "${chart_dir}" \
  --set istio.enabled=true \
  --set-string cetirun-operator.domainSuffix=platform.example.invalid \
  --set-string cetirun-portal.istio.host=portal.example.invalid

echo "==> Packaging Helm chart (${chart_name} v${chart_version})"
helm package "${chart_dir}" --destination "${package_dir}" --version "${chart_version}"
chart_archive="${package_dir}/${chart_name}-${chart_version}.tgz"
if [ ! -f "${chart_archive}" ]; then
  echo "ERROR: Helm package artifact not found at ${chart_archive}" >&2
  exit 1
fi

chart_ref="oci://${registry}/${chart_namespace}"
echo "==> Assumes you're already logged in: helm registry login ${registry}"
echo "==> Pushing chart to ${chart_ref}"
helm push "${chart_archive}" "${chart_ref}"

if [ "${verify_pull}" = "true" ]; then
  verify_dir="$(mktemp -d)"
  echo "==> Verifying: helm pull ${chart_ref}/${chart_name} --version ${chart_version}"
  helm pull "${chart_ref}/${chart_name}" --version "${chart_version}" --destination "${verify_dir}"
fi

echo "Release complete."
echo "  helm pull ${chart_ref}/${chart_name} --version ${chart_version}"
