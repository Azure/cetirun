#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
chart_dir="${CHART_DIR:-${repo_root}/app/cetirun-chart}"

if [ ! -f "${chart_dir}/Chart.yaml" ]; then
  echo "ERROR: Chart.yaml not found in ${chart_dir}" >&2
  exit 1
fi

values_files=(
  "values.yaml"
)

for values_file in "${values_files[@]}"; do
  if [ -f "${chart_dir}/${values_file}" ]; then
    echo "==> helm lint ${chart_dir} -f ${values_file} (agentType=openclaw)"
    helm lint "${chart_dir}" -f "${chart_dir}/${values_file}" \
      --set agentType=openclaw \
      --set-string secrets.model=lint-model \
      --set secrets.openclawGatewayToken=lint-token

    echo "==> helm lint ${chart_dir} -f ${values_file} (agentType=hermes)"
    helm lint "${chart_dir}" -f "${chart_dir}/${values_file}" \
      --set agentType=hermes \
      --set-string secrets.model=lint-model
  fi
done
