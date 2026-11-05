#!/usr/bin/env bash
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"

chart_dir="${CHART_DIR:-${repo_root}/app/cetirun-chart}"
registry="${REGISTRY:-cetirun.azurecr.io}"
chart_namespace="${CHART_NAMESPACE:-helm}"
values_file="${VALUES_FILE:-${chart_dir}/values.yaml}"
verify_pull="${VERIFY_PULL:-false}"
openclaw_image_digest="${OPENCLAW_IMAGE_DIGEST:-}"
hermes_image_digest="${HERMES_IMAGE_DIGEST:-}"

if [ -n "${openclaw_image_digest}" ] || [ -n "${hermes_image_digest}" ]; then
  if [ -z "${openclaw_image_digest}" ] || [ -z "${hermes_image_digest}" ]; then
    echo "ERROR: OPENCLAW_IMAGE_DIGEST and HERMES_IMAGE_DIGEST must be supplied together" >&2
    exit 1
  fi
fi

if ! command -v helm >/dev/null 2>&1; then
  echo "ERROR: Missing required command: helm" >&2
  exit 1
fi

if [ ! -f "${chart_dir}/Chart.yaml" ]; then
  echo "ERROR: Chart.yaml not found in ${chart_dir}" >&2
  exit 1
fi

chart_name="$(awk -F': *' '$1=="name" {print $2; exit}' "${chart_dir}/Chart.yaml")"
release_version="${VERSION:-$("${script_dir}/cetirun-release-metadata.sh" version)}"
chart_version="${CHART_VERSION:-${release_version}}"
app_version="${APP_VERSION:-${release_version}}"

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
package_chart_dir="${chart_dir}"

if [ -f "${values_file}" ]; then
  echo "==> Linting Helm chart with ${values_file}"
  helm lint "${chart_dir}" -f "${values_file}" \
    --set secrets.openclawGatewayToken=lint-token --set secrets.model=lint-model \
    --set aiGateway.keySecretName=lint-model-key --set-string secrets.apiBase=http://gateway.example.com/v1
else
  echo "==> Linting Helm chart"
  helm lint "${chart_dir}" \
    --set secrets.openclawGatewayToken=lint-token --set secrets.model=lint-model \
    --set aiGateway.keySecretName=lint-model-key --set-string secrets.apiBase=http://gateway.example.com/v1
fi

if [ -n "${openclaw_image_digest}" ] || [ -n "${hermes_image_digest}" ]; then
  package_chart_dir="${package_dir}/runtime-chart"
  echo "==> Locking runtime chart defaults to paired image digests"
  python3 "${script_dir}/cetirun-runtime-chart-lock.py" \
    --source-chart "${chart_dir}" --output-chart "${package_chart_dir}" \
    --registry "${registry}" --version "${app_version}" \
    --openclaw-image-digest "${openclaw_image_digest}" \
    --hermes-image-digest "${hermes_image_digest}"
fi

echo "==> Packaging Helm chart (${chart_name} v${chart_version})"
PACKAGE_ARGS=("${package_chart_dir}" --destination "${package_dir}" --version "${chart_version}" --app-version "${app_version}")
helm package "${PACKAGE_ARGS[@]}"

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
