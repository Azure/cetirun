#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"

chart_dir="${CHART_DIR:-${repo_root}/app/cetirun-gateway-chart}"
registry="${REGISTRY:-cetirun.azurecr.io}"
chart_namespace="${CHART_NAMESPACE:-helm}"
verify_pull="${VERIFY_PULL:-false}"

if ! command -v helm >/dev/null 2>&1; then
  echo "ERROR: Missing required command: helm" >&2
  exit 1
fi

if [ ! -f "${chart_dir}/Chart.yaml" ]; then
  echo "ERROR: Chart.yaml not found in ${chart_dir}" >&2
  exit 1
fi

chart_name="$(awk -F': *' '$1=="name" {print $2; exit}' "${chart_dir}/Chart.yaml")"
chart_version="$(awk -F': *' '$1=="version" {print $2; exit}' "${chart_dir}/Chart.yaml")"

if [ -z "${chart_name}" ] || [ -z "${chart_version}" ]; then
  echo "ERROR: Failed to parse chart name/version from ${chart_dir}/Chart.yaml" >&2
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

chart_ref="oci://${registry}/${chart_namespace}"
chart_archive="${package_dir}/${chart_name}-${chart_version}.tgz"

# Lint twice. With the chart's own defaults, which is what a consumer starts
# from, and with the render values, because the defaults carry an empty model
# list and so never reach the install-time validation.
echo "==> Linting Helm chart"
helm lint --strict "${chart_dir}"
helm lint --strict "${chart_dir}" -f "${chart_dir}/ci/render-values.yaml"

echo "==> Packaging Helm chart (${chart_name} v${chart_version})"
helm package "${chart_dir}" --destination "${package_dir}"

if [ ! -f "${chart_archive}" ]; then
  echo "ERROR: Helm package artifact not found at ${chart_archive}" >&2
  exit 1
fi

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
echo "  helm upgrade <release> ${chart_ref}/${chart_name} --version ${chart_version} ..."
